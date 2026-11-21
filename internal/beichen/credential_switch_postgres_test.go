package beichen

import (
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestCredentialSwitchLifecyclePostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	original, other := keys[0], keys[1]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	manager := switchTestManager(t)
	seal := func(key string) []byte {
		t.Helper()
		ciphertext, err := manager.EncryptCredential(key)
		if err != nil {
			t.Fatal(err)
		}
		return ciphertext
	}
	exec("UPDATE provider_credentials SET status='inactive' WHERE id IN ($1,$2)", keys[2], keys[3])
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", original, seal("sk-old-test-channel"))
	exec(`INSERT INTO beichen_datasets(credential_id,kind,next_attempt)
SELECT c.id,k.kind,now()+interval '1 hour' FROM provider_credentials c
CROSS JOIN (VALUES ('billing'),('usage'),('key_usage')) k(kind) WHERE c.id IN ($1,$2)`, original, other)

	const fable = "claude-fable-5"
	models := []string{fable, "no-quote", "wrong-protocol", "missing-model", "fallback"}
	originalCatalog := make([]catalog.ProviderEntry, 0, len(models))
	modelIDs := make(map[string]uuid.UUID)
	for _, name := range models {
		id := uuid.New()
		modelIDs[name] = id
		exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2::text::citext,$2::text,'active',1000,now(),now())`, id, name)
		exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,'anthropic',7,now(),now())`, id, provider, original, name)
		originalCatalog = append(originalCatalog, catalog.ProviderEntry{ID: name, SupportedProtocols: []string{"anthropic"}})
	}
	saveCatalogFixture(t, ctx, db, provider, original, originalCatalog, nil)
	backupProvider, backupKey := uuid.New(), uuid.New()
	exec("INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'backup','https://fixture.test',now(),now())", backupProvider)
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'backup-key',$3,now(),now())`, backupKey, backupProvider, seal("sk-test-backup-provider"))
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,'fallback','anthropic',20,now(),now())`, modelIDs["fallback"], backupProvider, backupKey)
	saveCatalogFixture(t, ctx, db, backupProvider, backupKey, []catalog.ProviderEntry{{ID: "fallback"}}, nil)

	const fakeKey = "sk-test-switch-only"
	service := New(db, manager)
	const bearer = "fixture-switch-session"
	if err := service.ConfigureWebSessionTokens("switch@example.test", bearer, "fixture-switch-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	directory := websiteGroupFixture(7, "1.25", `
	  {"name":"claude-fable-5","supported_protocols":["anthropic"],
	   "pricing":{"input_price":0.0000000001,"output_price":0.0000000005,"cache_read_price":0,"cache_write_price":0}},
	  {"name":"no-quote","supported_protocols":["anthropic"],"pricing":null},
	  {"name":"wrong-protocol","supported_protocols":["openai_responses"],"pricing":{"input_price":0.0000000001,"output_price":0.0000000005}}`)
	var requestLock sync.Mutex
	requests := map[string]int{}
	service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != publicKeyHost || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+bearer {
			return nil, errors.New("mock only accepts the authenticated website")
		}
		requestLock.Lock()
		requests[r.URL.Host+r.URL.Path]++
		requestLock.Unlock()
		var body string
		switch r.URL.Path {
		case "/api/v1/keys":
			body = websiteKeyListFixture(fakeKey, "Claude 新 Key", 41, 7)
		case "/api/v1/channels/available":
			body = directory
		case "/api/v1/usage":
			if r.URL.Query().Get("api_key_id") != "41" {
				return nil, errors.New("usage request used an unexpected Key")
			}
			body = emptyWebsiteUsageFixture
		default:
			return nil, errors.New("unexpected mock request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	label, active, inactive := "Claude 新 Key", "active", "inactive"
	to, err := service.SaveCredential(ctx, provider, uuid.Nil, CredentialEdit{Label: &label, Status: &active, Ciphertext: seal(fakeKey), Replaces: &original})
	if err != nil {
		t.Fatal(err)
	}
	scope := CredentialSwitch{From: original, To: to.ID}
	read := func() ChannelSwitchView {
		t.Helper()
		v, err := service.ChannelSwitch(ctx, provider, scope)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	if before.Preview != nil || before.Counts.EnabledKeys != 3 || before.Counts.MaintenanceModels != 0 {
		t.Fatalf("添加新 Key 后应等待同步并保留原线路: %+v", before)
	}
	for _, model := range before.Models {
		if model.LosingRouteCount != 1 || model.LosesLastRoute != (model.Model != "fallback") {
			t.Fatalf("停用影响没有计入其他供应商的线路: %+v", model)
		}
	}
	if _, err := service.SaveCredential(ctx, provider, original, CredentialEdit{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	retired := read()
	if retired.Counts.EnabledKeys != 2 || retired.Counts.MaintenanceModels != 4 || retired.Counts.StaleRoutes != 5 {
		t.Fatalf("停用后应保留失效绑定和维护中模型: %+v", retired.Counts)
	}
	overview, err := service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	overview.UseActiveCredentialHealth()
	if len(overview.Credentials) != 2 {
		t.Fatal("教师页 Key 覆盖分母包含已停用凭据")
	}
	visible := false
	for _, model := range overview.ModelCatalog {
		if model.Model == fable {
			visible = model.SchoolStatus == "active" && model.ReadyCount == 0 && model.KeyCount == 0 &&
				model.BindingIssue != nil && strings.Contains(model.BindingIssue.Message, "维护中")
		}
	}
	if !visible {
		t.Fatal("教师页遗漏了没有线路的已启用模型或恢复提示")
	}
	entDB := ent.NewClient(ent.Driver(entsql.OpenDB("postgres", db)))
	assertRoute := func(want bool) {
		t.Helper()
		actual, err := catalog.ModelHasActiveRoute(ctx, entDB, modelIDs[fable])
		if err != nil || actual != want {
			t.Fatalf("学生选择和网关共用的线路检查 = %v, want %v (%v)", actual, want, err)
		}
	}
	assertRoute(false)
	jobs, err := service.listJobs(ctx, nil)
	if err != nil || len(jobs) != 2 || jobs[0].credential != to.ID || jobs[0].kind != "billing" ||
		jobs[1].credential != to.ID || jobs[1].kind != "key_usage" {
		t.Fatalf("后台应只排队接替 Key: %+v (%v)", jobs, err)
	}
	if err := service.syncJobs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if requests[publicKeyHost+"/api/v1/keys"] != 2 || requests[publicKeyHost+"/api/v1/channels/available"] != 2 ||
		requests[publicKeyHost+"/api/v1/usage"] != 2 {
		t.Fatalf("同步缺少目录、计费或日用量: %v", requests)
	}
	var multiplier, usageDays int
	if err := db.QueryRowContext(ctx, `SELECT
(SELECT count(*) FROM beichen_multiplier_versions WHERE credential_id=$1 AND multiplier=1.25),
(SELECT count(*) FROM beichen_key_usage_days WHERE credential_id=$1 AND website_key_id=41)`, to.ID).Scan(&multiplier, &usageDays); err != nil ||
		multiplier != 1 || usageDays != 2 {
		t.Fatalf("倍率和用量未写入接替 Key: %d, %d (%v)", multiplier, usageDays, err)
	}
	preview := read().Preview
	if preview == nil || len(preview.Changes) != 1 || len(preview.Blockers) != 4 || preview.Changes[0].After.Priority != 7 {
		t.Fatalf("自动预览应只迁移具备匹配报价和协议的线路: %+v", preview)
	}
	codes := map[string]string{}
	for _, blocker := range preview.Blockers {
		codes[blocker.SchoolModel] = blocker.Code
	}
	if codes["wrong-protocol"] != "missing_protocol" || codes["no-quote"] != "missing_price" || codes["missing-model"] != "missing_model" {
		t.Fatalf("缺失项未区分: %v", codes)
	}
	assertRoute(false)

	// A delayed response from before retirement must not clear the scheduler
	// entry created when the same Key is activated again.
	lease := uuid.New()
	exec("UPDATE beichen_datasets SET lease_id=$2,lease_until=now()+interval '1 minute' WHERE credential_id=$1 AND kind='key_usage'", to.ID, lease)
	for _, status := range []*string{&inactive, &active} {
		if _, err := service.SaveCredential(ctx, provider, to.ID, CredentialEdit{Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.finishJob(ctx, syncJob{provider: provider, credential: to.ID, kind: "key_usage"}, lease, credentialRecord{updatedAt: to.UpdatedAt},
		fetchResult{err: errors.New("obsolete use response")}, false); err == nil {
		t.Fatal("接受了旧版本的同步结果")
	}
	var stillQueued bool
	if err := db.QueryRowContext(ctx, `SELECT last_error IS NULL AND lease_id IS NULL AND next_attempt='-infinity'
FROM beichen_datasets WHERE credential_id=$1 AND kind='key_usage'`, to.ID).Scan(&stillQueued); err != nil || !stillQueued {
		t.Fatalf("旧任务覆盖了新任务: %v", err)
	}
	if err := service.SyncCredential(ctx, provider, to.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmCredentialSwitch(ctx, provider, actor, scope, preview.PreviewToken); domain.ErrorCode(err) != domain.CodeInvalidTransition {
		t.Fatalf("接受了过期的预览: %v", err)
	}
	latest := read().Preview
	if latest == nil {
		t.Fatal("重试后没有预览")
	}
	if _, err := service.ConfirmCredentialSwitch(ctx, provider, actor, scope, latest.PreviewToken); err != nil {
		t.Fatal(err)
	}
	assertRoute(true)
	recovered := read()
	if recovered.Counts.MaintenanceModels != 3 || recovered.Counts.StaleRoutes != 4 || len(recovered.Preview.Changes) != 0 {
		t.Fatalf("确认后应只恢复支持的线路: %+v", recovered)
	}
}

func TestCredentialSwitchInheritsSchoolChannelWithoutCopyingAuthorizationPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	original := keys[3]
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES($1,'测试审计','provision@example.test','!','student','disabled',now(),now())`, ProvisionActorID)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'CC1',sha256('old secret'::bytea),false)`, original)
	exec(`INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at,group_id,upstream_key_id,token_ciphertext)
SELECT id,$2,updated_at,11,42,'fixture' FROM provider_credentials WHERE id=$1`, original, ProvisionActorID)

	const fakeKey = "sk-test-managed-replacement"
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	label, active := "Claude 接替", "active"
	fingerprint := sha256.Sum256([]byte(fakeKey))
	service := New(db, manager)
	row, err := service.SaveCredential(ctx, provider, uuid.Nil, CredentialEdit{
		Label: &label, Status: &active, Ciphertext: ciphertext, Replaces: &original, Fingerprint: fingerprint[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	var managed, pending, originalIntact bool
	err = db.QueryRowContext(ctx, `SELECT
 (SELECT channel='CC1' AND discover_ids FROM beichen_provision_channels WHERE credential_id=$1),
 (SELECT upstream_key_id IS NULL AND token_ciphertext IS NULL AND group_id IS NULL FROM beichen_panel_access WHERE credential_id=$1),
 (SELECT upstream_key_id=42 FROM beichen_receipt_active_bindings WHERE credential_id=$2)`, row.ID, original).
		Scan(&managed, &pending, &originalIntact)
	if err != nil || !managed || !pending || !originalIntact {
		t.Fatalf("应保留学校计费渠道并等待接替 Key 独立授权: %v, %v, %v (%v)", managed, pending, originalIntact, err)
	}
}

func switchTestManager(t *testing.T) *security.Manager {
	t.Helper()
	manager, err := security.NewManager([]byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)),
		[]byte(strings.Repeat("c", 32)), []byte(strings.Repeat("d", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
