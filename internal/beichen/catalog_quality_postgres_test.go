package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func execCatalogTestSQL(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func saveKeyCatalogFixture(ctx context.Context, tx *sql.Tx, job syncJob, entries []catalog.ProviderEntry, at time.Time) error {
	if _, err := catalog.ReplaceCredentialEntries(ctx, tx, job.provider, job.credential, entries, at); err != nil {
		return err
	}
	return recordSuccess(ctx, tx, job.credential, "catalog")
}

func saveCatalogFixture(t *testing.T, ctx context.Context, db *sql.DB, provider, credential uuid.UUID, entries []catalog.ProviderEntry, website []string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := saveKeyCatalogFixture(ctx, tx, syncJob{provider: provider, credential: credential, kind: "catalog"}, entries, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if website != nil {
		var revision time.Time
		if err := tx.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", credential).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if err := saveWebsiteCatalog(ctx, tx, credential, revision, panelQuote{groupID: 7, websiteModels: website}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogQualityWebsiteOnlyAndScopedRescanPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	first, second := keys[0], keys[1]
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET status='inactive' WHERE id IN ($1,$2)", keys[2], keys[3])
	manager, err := security.NewManager([]byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)),
		[]byte(strings.Repeat("c", 32)), []byte(strings.Repeat("d", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const fakeKey = "fixture-catalog-rescan"
	ciphertext, err := manager.EncryptCredential(fakeKey)
	if err != nil {
		t.Fatal("无法加密测试凭据")
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", first, ciphertext)
	saveCatalogFixture(t, ctx, db, provider, first, []catalog.ProviderEntry{{ID: "old-key-model"}}, []string{"shared", "web-only", "promoted"})
	saveCatalogFixture(t, ctx, db, provider, second, []catalog.ProviderEntry{{ID: "Shared"}}, []string{"Web-Only"})
	execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_datasets (credential_id,kind,last_success,last_error,next_attempt)
VALUES ($1,'billing','2026-01-01 00:00Z','fixture', '2026-02-01 00:00Z')`, first)
	service := New(db, manager)
	status, requests, rotate := http.StatusOK, 0, false
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet || req.URL.Host != "api.beichenapi.com" || req.URL.Path != "/v1/models" ||
			req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer "+fakeKey {
			return nil, errors.New("测试只允许选中 Key 的模型清单请求")
		}
		body := `{"data":[{"id":"promoted","object":"model"}]}`
		if status != http.StatusOK {
			body = fakeKey
		}
		if rotate {
			_, err := db.ExecContext(ctx, "UPDATE provider_credentials SET updated_at=clock_timestamp() WHERE id=$1", first)
			if err != nil {
				return nil, err
			}
			body = `{"data":[{"id":"stale-key-model","object":"model"}]}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	overview := func() Overview {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	before := overview()
	if before.WebsiteOnlyStatus != "compared" || len(before.WebsiteOnlyModels) != 2 ||
		before.WebsiteOnlyModels[0].Model != "promoted" || len(before.WebsiteOnlyModels[1].Channels) != 2 {
		t.Fatalf("仅网页目录没有跨 Key 去重或排除已列出的模型: %+v", before.WebsiteOnlyModels)
	}
	if _, err := service.EnableModel(ctx, provider, actor, "web-only", ""); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatal("仅网页模型应禁止启用")
	}
	skipped, err := service.EnableModelsBatch(ctx, provider, actor, []string{"web-only"})
	if err != nil || len(skipped.Enabled) != 0 || len(skipped.Skipped) != 1 || skipped.Skipped[0].Reason != "not_found" {
		t.Fatal("仅网页模型应从批量启用跳过")
	}
	for _, id := range []uuid.UUID{keys[2], uuid.New()} {
		if err := service.RescanCatalog(ctx, provider, id); domain.ErrorCode(err) != domain.CodeNotFound {
			t.Fatal("未知或停用渠道应被拒绝")
		}
	}
	execCatalogTestSQL(t, ctx, db, `UPDATE beichen_datasets SET lease_id=gen_random_uuid(),lease_until=now()+interval '1 minute'
WHERE kind='billing' AND credential_id=$1`, first)
	if err := service.RescanCatalog(ctx, provider, first); domain.ErrorCode(err) != domain.CodeInvalidTransition || requests != 0 {
		t.Fatal("重扫应与同渠道的计费同步互斥")
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE beichen_datasets SET lease_until=now()-interval '1 second' WHERE kind='billing' AND credential_id=$1", first)
	if err := service.RescanCatalog(ctx, provider, first); err != nil || requests != 1 {
		t.Fatalf("指定渠道重扫失败: %v", err)
	}
	after := overview()
	if after.WebsiteOnlyStatus != "compared" || len(after.WebsiteOnlyModels) != 1 || !strings.EqualFold(after.WebsiteOnlyModels[0].Model, "web-only") {
		t.Fatal("重扫后新增模型应退出仅网页清单")
	}
	if len(after.Models) != 2 || !reflect.DeepEqual(after.CatalogDifferences[0].CheckedAt, before.CatalogDifferences[0].CheckedAt) ||
		!reflect.DeepEqual(after.CatalogDifferences[1].CheckedAt, before.CatalogDifferences[1].CheckedAt) {
		t.Fatal("重扫应保留网页读回时间和其他渠道目录")
	}
	var billingUnchanged bool
	if err := db.QueryRowContext(ctx, `SELECT last_success='2026-01-01 00:00Z' AND next_attempt='2026-02-01 00:00Z'
AND last_error='fixture' AND lease_id IS NULL FROM beichen_datasets WHERE credential_id=$1 AND kind='billing'`, first).
		Scan(&billingUnchanged); err != nil || !billingUnchanged {
		t.Fatal("目录重扫写入了账单状态")
	}
	status = http.StatusForbidden
	if err := service.RescanCatalog(ctx, provider, first); domain.ErrorCode(err) != domain.CodeDependencyUnavailable || strings.Contains(err.Error(), fakeKey) {
		t.Fatal("重扫失败应返回安全的错误")
	}
	if view := overview(); view.WebsiteOnlyStatus != "unverified" || len(view.Models) != 2 {
		t.Fatal("失败扫描不能替换上轮完整目录或断定模型仅在网页可见")
	}
	status, rotate = http.StatusOK, true
	if err := service.RescanCatalog(ctx, provider, first); domain.ErrorCode(err) != domain.CodeDependencyUnavailable {
		t.Fatal("扫描期间轮换 Key 应丢弃旧结果")
	}
	for _, row := range overview().Models {
		if row.Model == "stale-key-model" {
			t.Fatal("过期 Key 的响应写入了目录")
		}
	}
}

func TestCatalogQualityAliasCRUDAndRebindPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	first, second := keys[0], keys[1]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	exec("UPDATE provider_credentials SET status='inactive' WHERE id IN ($1,$2)", keys[2], keys[3])
	school, classics, retired, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,'school-model','课堂模型','active',1000,now(),now()),
($2,'classics','旧模型','inactive',1000,now(),now()),($3,'retired','退役模型','active',1000,now(),now())`, school, classics, retired)
	exec(`INSERT INTO providers(id,name,base_url,status,created_at,updated_at)
VALUES ($1,'other','https://example.test','active',now(),now())`, other)
	foreignKey := uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'other','fixture',now(),now())`, foreignKey, other)
	oldBinding, inactiveBinding, staleBinding, foreignBinding := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
VALUES ($1,$5,$8,$9,'old-name','openai_responses','active',9,now(),now()),
       ($2,$6,$8,$9,'classics','openai_compatible','inactive',35,now(),now()),
       ($3,$7,$8,$9,'retired','openai_responses','active',15,now(),now()),
       ($4,$5,$10,$11,'foreign','anthropic','active',200,now(),now())`,
		oldBinding, inactiveBinding, staleBinding, foreignBinding, school, classics, retired, provider, second, other, foreignKey)
	saveCatalogFixture(t, ctx, db, provider, first, []catalog.ProviderEntry{
		{ID: "New-Name", SupportedProtocols: []string{"openai_responses", "anthropic"}},
		{ID: "unconfigured", SupportedProtocols: []string{"openai_responses"}},
	}, nil)
	saveCatalogFixture(t, ctx, db, provider, second, []catalog.ProviderEntry{{ID: "classics", SupportedProtocols: []string{"openai_compatible"}}}, nil)
	service := New(db, nil)
	var audit bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&audit, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })
	schoolModel, classicsModel, nonexistent := "SCHOOL-model", "classics", "not-there"
	if err := service.PutModelAlias(ctx, provider, actor, &foreignKey, "new-name", &schoolModel); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatal("不应接受其他供应商的渠道")
	}
	if err := service.PutModelAlias(ctx, provider, actor, &first, "new-name", &nonexistent); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatal("不应接受不存在的校内模型")
	}
	put := func(id *uuid.UUID, upstream string, model *string) {
		t.Helper()
		if err := service.PutModelAlias(ctx, provider, actor, id, upstream, model); err != nil {
			t.Fatal(err)
		}
	}
	put(nil, "NEW-NAME", &classicsModel)
	put(&first, "New-Name", &schoolModel)
	put(&first, "NEW-NAME", &schoolModel)
	aliases, err := service.ModelAliases(ctx, provider)
	if err != nil || len(aliases) != 2 || aliases[0].CredentialID != nil || aliases[1].SchoolModel != "school-model" ||
		aliases[1].CredentialID == nil || *aliases[1].CredentialID != first {
		t.Fatalf("同渠道的别名未按不区分大小写的键更新: %+v (%v)", aliases, err)
	}
	preview, err := service.Rebind(ctx, provider, actor, true, "")
	if err != nil || !preview.Preview || len(preview.Changes) != 4 || len(preview.PreviewToken) != 64 || audit.Len() != 0 {
		t.Fatalf("预览不应写审计，且应包含四条影响: %+v (%v)", preview, err)
	}
	wantActions := map[string]int{"add": 1, "enable": 1, "update": 1, "disable": 1}
	for _, change := range preview.Changes {
		wantActions[change.Action]--
		if change.Action == "add" && (change.After.Priority != 201 || change.Before != nil) {
			t.Fatal("新线路应排在其他供应商线路之后")
		}
		if change.Action == "update" && (change.Before.Priority != 9 || change.After.Priority != 9 ||
			change.After.UpstreamModel != "New-Name" || *change.After.CredentialID != first) {
			t.Fatal("重建应保留优先级并使用渠道别名选线")
		}
	}
	for _, count := range wantActions {
		if count != 0 {
			t.Fatalf("预览动作错误: %v", wantActions)
		}
	}
	var upstream string
	if err := db.QueryRowContext(ctx, "SELECT upstream_model_name FROM model_bindings WHERE id=$1", oldBinding).Scan(&upstream); err != nil || upstream != "old-name" {
		t.Fatal("预览改变了绑定")
	}
	put(&first, "new-name", &classicsModel)
	if _, err := service.Rebind(ctx, provider, actor, false, preview.PreviewToken); domain.ErrorCode(err) != domain.CodeInvalidTransition || audit.Len() != 0 {
		t.Fatal("过期预览提交了绑定或记录了成功审计")
	}
	put(&first, "new-name", nil)
	aliases, err = service.ModelAliases(ctx, provider)
	if err != nil || len(aliases) != 1 || aliases[0].CredentialID != nil {
		t.Fatal("删除单渠道别名应保留默认别名")
	}
	put(&first, "new-name", &schoolModel)
	result, err := service.Rebind(ctx, provider, actor, false, preview.PreviewToken)
	if err != nil || result.Preview || !reflect.DeepEqual(preview.Changes, result.Changes) {
		t.Fatalf("确认结果与预览不一致: %v", err)
	}
	if strings.Count(audit.String(), "beichen_bindings_rebuilt") != 1 || !strings.Contains(audit.String(), actor.String()) ||
		!strings.Contains(audit.String(), provider.String()) || !strings.Contains(audit.String(), preview.PreviewToken) {
		t.Fatal("重建审计缺少操作人、供应商或变更记录")
	}
	var preserved bool
	err = db.QueryRowContext(ctx, `SELECT
  (SELECT status='inactive' FROM models WHERE id=$1)
  AND (SELECT priority=200 AND upstream_model_name='foreign' AND status='active' FROM model_bindings WHERE id=$2)
  AND (SELECT status='inactive' FROM model_bindings WHERE id=$3)`, classics, foreignBinding, staleBinding).Scan(&preserved)
	if err != nil || !preserved {
		t.Fatal("重建改变了模型状态、其他供应商线路或未停用过期线路")
	}
	secondPreview, err := service.Rebind(ctx, provider, actor, true, "")
	if err != nil || len(secondPreview.Changes) != 0 {
		t.Fatalf("目录未变化时，重建应幂等: %+v (%v)", secondPreview, err)
	}
	exec("UPDATE beichen_datasets SET last_error='fixture' WHERE credential_id=$1 AND kind='catalog'", second)
	if _, err := service.Rebind(ctx, provider, actor, true, ""); domain.ErrorCode(err) != domain.CodeInvalidTransition {
		t.Fatal("目录扫描失败时不应重建绑定")
	}
}
