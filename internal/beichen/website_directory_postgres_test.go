package beichen

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestBeichenWebsiteDirectoryPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	fixture, err := os.ReadFile("testdata/website-directory.json")
	if err != nil {
		t.Fatal(err)
	}
	manager := switchTestManager(t)
	service := New(db, manager)
	const bearer = "fixture-website-access"
	if err := service.ConfigureWebSessionTokens("website@example.test", bearer, "fixture-website-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	body, websiteStatus, websiteReads := string(fixture), http.StatusOK, 0
	const newKey = "sk-fixture-cc1-replacement"
	keyResponses := map[string]string{
		"sk-fixture-gemini": `{"data":[]}`,
		"sk-fixture-gm1": `{"data":[{"id":"channel-ds","supported_protocols":["openai_compatible"]},
			{"id":"glm5"},{"id":"gpt-6.1-sol-20261119","supported_protocols":["openai_compatible"]}]}`,
		"sk-fixture-g3": `{"data":[{"id":"gpt-6.1-sol","supported_protocols":["openai_responses"]},
			{"id":"gpt-6.2-sol-2026-11-20"},{"id":"gpt_mini-2026-11-20"},
			{"id":"fixture-key-only","supported_protocols":[]}]}`,
		"sk-fixture-original-cc1": `{"data":[{"id":"claude-fable-5","supported_protocols":["anthropic"]}]}`,
		newKey:                    `{"data":[]}`,
	}
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method == http.MethodGet && r.URL.String() == panelURL && token == bearer {
			websiteReads++
			return &http.Response{StatusCode: websiteStatus, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if r.Method == http.MethodGet && r.URL.Host == "api.beichenapi.com" && r.URL.Path == modelsPath && keyResponses[token] != "" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(keyResponses[token]))}, nil
		}
		return nil, errors.New("请求超出假上游允许的路径")
	})
	exec(`INSERT INTO beichen_datasets (credential_id,kind,next_attempt)
SELECT c.id,k.kind,now()+interval '1 hour'
FROM provider_credentials c CROSS JOIN (VALUES ('billing'),('usage'),('key_usage')) k(kind) WHERE c.provider_id=$1`, provider)
	if err := service.syncWithWebsite(ctx, nil); err != nil || websiteReads != 1 {
		t.Fatalf("独立调度没有读取官网: %d 次, %v", websiteReads, err)
	}
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES($1,'审计','website-audit@example.test','fixture','student','disabled',now(),now())`, ProvisionActorID)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'CC1',sha256('fixture-original'::bytea),false)`, keys[3])
	school := uuid.New()
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,'claude-fable-5','Claude Fable 5','active',1000,now(),now()),
      ($2,'lesson-ds','教学模型','inactive',1000,now(),now())`, school, uuid.New())
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,'claude-fable-5','anthropic','active',10,now(),now())`, school, provider, keys[3])
	exec(`INSERT INTO beichen_panel_access(credential_id,group_id,token_ciphertext,updated_by,updated_at)
SELECT id,21,'fixture',$2,updated_at FROM provider_credentials WHERE id=$1`, keys[2], actor)

	type persisted struct {
		platforms string
		success   time.Time
		failures  int
	}
	readSnapshot := func() persisted {
		t.Helper()
		var saved persisted
		if err := db.QueryRowContext(ctx, `SELECT platforms::text,last_success,failure_count
FROM beichen_website_directories WHERE provider_id=$1`, provider).Scan(&saved.platforms, &saved.success, &saved.failures); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	first := readSnapshot()
	var platformCount, occurrences int
	var completeTerms, keptNull bool
	if err := db.QueryRowContext(ctx, `
SELECT jsonb_array_length(d.platforms), sum(jsonb_array_length(p.platform->'models')),
  bool_or(p.platform @? '$.models[*] ? (@.name == "GPT-6.1-SOL" && @.pricing.intervals[0].max_tokens == null)'),
  bool_or(p.platform @? '$.models[*] ? (@.name == "Claude Fable 5" && @.pricing == null)')
FROM beichen_website_directories d CROSS JOIN LATERAL jsonb_array_elements(d.platforms) p(platform)
WHERE d.provider_id=$1 GROUP BY d.platforms`, provider).Scan(&platformCount, &occurrences, &completeTerms, &keptNull); err != nil ||
		platformCount != 9 || occurrences != 21 || !completeTerms || !keptNull {
		t.Fatalf("官网平台、价格条件或 null 价格未完整入库: %d 平台 %d 条 %v %v (%v)", platformCount, occurrences, completeTerms, keptNull, err)
	}
	overview := func() Overview {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		view.UseActiveCredentialHealth()
		return view
	}
	findModel := func(view Overview, name string) CatalogModel {
		t.Helper()
		for _, row := range view.ModelCatalog {
			if row.Model == name {
				return row
			}
		}
		t.Fatalf("教师目录缺少 %s", name)
		return CatalogModel{}
	}
	web := overview()
	fullRun := web.WebsiteDirectory.LastRun
	if web.WebsiteDirectory.State != "fresh" || len(web.ModelCatalog) != 20 || len(web.WebsiteOnlyModels) != 19 ||
		fullRun == nil || fullRun.StartedAt.IsZero() || fullRun.PlatformCount != 9 || fullRun.GroupCount != 5 ||
		fullRun.ModelCount != 20 || fullRun.ExpectedModelCount != 20 || len(fullRun.Failures) != 0 {
		t.Fatalf("完整抓取后官网目录或最近抓取报告不符: %+v", web.WebsiteDirectory)
	}
	if row := findModel(web, "claude-fable-5"); row.CatalogState != "published_unavailable" ||
		row.KeyCount != 0 || row.BindingIssue == nil || slices.ContainsFunc(web.WebsiteOnlyModels, func(model WebsiteOnlyModel) bool {
		return strings.EqualFold(model.Model, row.Model)
	}) {
		t.Fatalf("已发布 Claude 应保留在异常目录: %+v", row)
	}
	secrets := []string{"sk-fixture-gemini", "sk-fixture-gm1", "sk-fixture-g3", "sk-fixture-original-cc1"}
	for i, key := range keys {
		cipher, err := manager.EncryptCredential(secrets[i])
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", key, cipher)
		if err := service.RescanCatalog(ctx, provider, key); err != nil {
			t.Fatal(err)
		}
	}
	target := "lesson-ds"
	if err := service.PutModelAlias(ctx, provider, actor, nil, "deepseek-internal-web", &target); err != nil {
		t.Fatal(err)
	}
	if err := service.PutModelAlias(ctx, provider, actor, &keys[1], "channel-ds", &target); err != nil {
		t.Fatal(err)
	}
	both := overview()
	if len(both.ModelCatalog) != 22 || len(both.WebsiteOnlyModels) != 14 || both.WebsiteOnlyStatus != "compared" {
		t.Fatalf("官网与 Key 合并计数不符: %d 个模型，%d 个仅官网，状态 %s", len(both.ModelCatalog), len(both.WebsiteOnlyModels), both.WebsiteOnlyStatus)
	}
	if row := findModel(both, "gpt-6.1-sol"); row.Source != "both" || row.KeyCount != 2 ||
		!slices.Equal(row.Protocols, []string{"openai_compatible", "openai_responses"}) {
		t.Fatalf("GPT 两把 Key 的原名和协议未被核对: %+v", row)
	}
	if row := findModel(both, "gpt-6.2-sol-2026-11-20"); row.Source != "both" || row.KeyCount != 1 ||
		row.WebsiteLocations[0].Name != "GPT 6.2 Sol" || row.CatalogKeys[0].UpstreamModel != row.Model ||
		row.CatalogKeys[0].Match != "normalized" {
		t.Fatalf("带日期的 Key ID 未关联官网拼写: %+v", row)
	}
	if row := findModel(both, "channel-ds"); row.Source != "both" || len(row.WebsiteLocations) != 1 ||
		row.WebsiteLocations[0].Name != "deepseek-internal-web" || row.CatalogKeys[0].Match != "alias" {
		t.Fatalf("教师别名未关联目录: %+v", row)
	}
	report, err := service.CrawlDirectory(ctx, CrawlOptions{Fixture: fixture, MatchDB: true})
	if err != nil || report.KeyMatching != "checked" || report.ModelCount != 20 || report.ExpectedModelCount != 20 {
		t.Fatalf("验收工具未读到当前 Key 和官网目录: %+v, %v", report.CrawlRun, err)
	}
	match := crawlTestModel(t, report, "deepseek-internal-web").KeyMatches
	if !slices.ContainsFunc(match, func(key CatalogKey) bool {
		return key.CredentialID == keys[1] && key.State == "listed" && key.Match == "alias" && key.UpstreamModel == "channel-ds"
	}) {
		t.Fatalf("验收报告没有教师别名匹配结果: %+v", match)
	}
	if !overview().WebsiteDirectory.LastRun.StartedAt.Equal(fullRun.StartedAt) || websiteReads != 1 {
		t.Fatal("离线验收改变了官网抓取记录或发出了网页请求")
	}
	if row := findModel(both, "grok-4.1"); row.CatalogState != "website_only" || len(row.WebsiteLocations) != 2 ||
		row.WebsiteLocations[0].Groups[0].ID != 27 || row.WebsiteLocations[1].Groups[0].ID != 28 || row.CanEnable {
		t.Fatalf("仅官网模型未保留全部分组: %+v", row)
	}
	if _, err := service.EnableModel(ctx, provider, actor, "grok-4.1", ""); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatal("仅官网的模型不能发布为线路")
	}
	if row := findModel(both, "fixture-key-only"); row.Source != "key" || row.KeyCount != 1 || len(row.Protocols) != 0 || row.PriceState != "missing" {
		t.Fatalf("无报价和协议不应抹去 Key 目录证据: %+v", row)
	}

	encryption, err := manager.EncryptCredential(newKey)
	if err != nil {
		t.Fatal(err)
	}
	label, active, inactive := "Claude 接替", "active", "inactive"
	fingerprint := sha256.Sum256([]byte(newKey))
	replacement, err := service.SaveCredential(ctx, provider, uuid.Nil, CredentialEdit{
		Label: &label, Status: &active, Ciphertext: encryption, Replaces: &keys[3], Fingerprint: fingerprint[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveCredential(ctx, provider, keys[3], CredentialEdit{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	checkClaude := func(state string) CatalogModel {
		t.Helper()
		row := findModel(overview(), "claude-fable-5")
		if len(row.CatalogKeys) != 1 || row.CatalogKeys[0].CredentialID != replacement.ID ||
			row.CatalogKeys[0].State != state || row.CatalogKeys[0].SchoolChannel != "CC1" || !row.CatalogKeys[0].Replacement ||
			len(row.WebsiteLocations) != 1 || row.WebsiteLocations[0].Groups[0].ID != 24 {
			t.Fatalf("换 Key 后的 Claude 官网来源与核对状态错误: %+v", row)
		}
		return row
	}
	checkClaude("pending")
	if err := service.RescanCatalog(ctx, provider, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if row := checkClaude("missing"); row.Source != "website" || row.CatalogState != "published_unavailable" ||
		row.KeyCount != 0 || len(row.Protocols) != 0 || row.BindingIssue == nil {
		t.Fatalf("新 Key 无 Claude 时应标明官网和原有绑定状态: %+v", row)
	}
	keyResponses[newKey] = keyResponses[secrets[3]]
	if err := service.RescanCatalog(ctx, provider, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if row := checkClaude("listed"); row.Source != "both" || row.KeyCount != 1 || !slices.Equal(row.Protocols, []string{"anthropic"}) {
		t.Fatalf("新 Key 出现 Claude 时未恢复覆盖和协议: %+v", row)
	}
	if websiteReads != 1 {
		t.Fatal("重扫 Key 不应重抓官网")
	}
	exec(`UPDATE beichen_datasets SET next_attempt=now()+interval '1 hour' WHERE kind IN ('billing','usage')`)

	body = strings.Replace(string(fixture), `"platform": "anthropic",`, `"platform": {"bad": true},`, 1)
	body = strings.Replace(body, "grok-4.3-fast", "grok-4.4", 1)
	body = strings.Replace(body, `"input_price": 0, "output_price": 0`, `"input_price": {}, "output_price": 0`, 1)
	if err := service.syncWebsiteDirectories(ctx, &provider); !errors.Is(err, errWebsiteDirectoryPartial) {
		t.Fatalf("单平台解析失败应报告部分更新: %v", err)
	}
	partial := readSnapshot()
	if !partial.success.Equal(first.success) || partial.failures != 1 || partial.platforms == first.platforms {
		t.Fatal("部分更新应保留完整成功时间并写入有效平台")
	}
	stale := overview()
	partialRun := stale.WebsiteDirectory.LastRun
	if stale.WebsiteDirectory.State != "stale" || findModel(stale, "grok-4.4").Source != "website" ||
		!findModel(stale, "claude-fable-5").WebsiteLocations[0].Stale ||
		!findModel(stale, "grok-4.2").WebsiteLocations[0].HasPrice {
		t.Fatal("部分失败未保留旧平台或未采纳有效平台")
	}
	if partialRun == nil || !partialRun.StartedAt.After(fullRun.StartedAt) ||
		partialRun.PlatformCount != 8 || partialRun.ModelCount != 17 || partialRun.ExpectedModelCount != 20 ||
		len(partialRun.Failures) != 2 || partialRun.Failures[0].ChannelID != 100 ||
		partialRun.Failures[1].Platform != "xai" || !strings.Contains(partialRun.Failures[1].Reason, "pricing") {
		t.Fatalf("最近抓取记录应仅统计本次响应并列出失败项: %+v", partialRun)
	}
	reads := websiteReads
	if err := service.syncWebsiteDirectories(ctx, nil); err != nil || websiteReads != reads {
		t.Fatal("失败后调度未退避")
	}
	for i, broken := range []string{`{"code":0,"data":null}`, `{"code":0,"data":[]}`, `{"code":0,"data":`} {
		body = broken
		exec("UPDATE beichen_website_directories SET next_attempt=now()-interval '1 second' WHERE provider_id=$1", provider)
		if err := service.syncWebsiteDirectories(ctx, nil); err == nil {
			t.Fatal("错误或空的官网响应应报告失败")
		}
		if saved := readSnapshot(); saved != (persisted{partial.platforms, partial.success, i + 2}) {
			t.Fatal("失败的官网响应改变了缓存目录或上次成功时间")
		}
	}
	websiteStatus, body = http.StatusServiceUnavailable, "fixture HTTP 503"
	if err := service.syncWebsiteDirectories(ctx, &provider); !errors.Is(err, upstreamHTTPStatus(http.StatusServiceUnavailable)) {
		t.Fatalf("网络失败应报告 HTTP 503: %v", err)
	}
	if saved := readSnapshot(); saved.platforms != partial.platforms || !saved.success.Equal(first.success) {
		t.Fatal("网络失败清除了上次官网数据")
	}
	if failed := overview().WebsiteDirectory.LastRun; failed == nil || failed.ModelCount != 0 ||
		len(failed.Failures) != 1 || !strings.Contains(failed.Failures[0].Reason, "HTTP 503") {
		t.Fatalf("网络失败未出现在最近抓取记录: %+v", failed)
	}
	websiteStatus, body = http.StatusOK, string(fixture)
	if err := service.syncWebsiteDirectories(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	recovered := overview().WebsiteDirectory
	if saved := readSnapshot(); saved.failures != 0 || !saved.success.After(first.success) ||
		recovered.State != "fresh" || recovered.LastRun == nil || recovered.LastRun.ModelCount != 20 || len(recovered.LastRun.Failures) != 0 {
		t.Fatal("官网恢复后应更新快照时间并清空退避状态")
	}
	t.Log("官网 9 个平台范围、21 条记录、20 个不同型号，合并后 22 个原始型号行（14 个仅官网），Claude 覆盖 1/4 → 0/4 → 1/4")
}

func TestBeichenScanRobustnessWebsiteDirectorySessionVersionPostgres(t *testing.T) {
	ctx, db, provider, actor, _ := newActionsPostgres(t)
	s := New(db, switchTestManager(t))
	const oldSession = "old-fixture-panel-session"
	const newSession = "new-fixture-panel-session"
	const baseline = `[{"id":1,"name":"channel-1","platforms":[{"platform":"openai","groups":[{"id":11,"name":"group-11"}],
		"models":[{"name":"model-baseline","pricing":{"billing_mode":"token","input_price":0.00001,"output_price":0.00002}}]}]}]`
	late := strings.Replace(baseline, "model-baseline", "model-late", 1)
	current := strings.Replace(baseline, "model-baseline", "model-current", 1)
	oldStarted, releaseOld := make(chan struct{}), make(chan struct{})
	pauseOld := false
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.URL.String() == panelURL && token == oldSession:
			if pauseOld {
				close(oldStarted)
				select {
				case <-releaseOld:
					return scanResponse(http.StatusOK, late), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return scanResponse(http.StatusOK, baseline), nil
		case r.URL.String() == panelURL && token == newSession:
			return scanResponse(http.StatusOK, current), nil
		case r.URL.Path == "/api/v1/keys" && token == newSession:
			return scanResponse(http.StatusOK, `{"code":0,"data":{"items":[{"id":51,"user_id":7,"name":"new-key","status":"active"}],
				"total":1,"page":1,"page_size":100,"pages":1}}`), nil
		default:
			return nil, errors.New("unexpected fixture request")
		}
	})
	login := func(token string) {
		t.Helper()
		if _, err := s.LoginPanelAccount(ctx, provider, actor, PanelAccountLogin{Mode: panelModeToken, Token: token}); err != nil {
			t.Fatal(err)
		}
	}
	assertEvidence := func(wantRaw, wantHistory string) {
		t.Helper()
		var raw []byte
		var history string
		err := db.QueryRowContext(ctx, `
SELECT r.raw_website_response,COALESCE((
  SELECT string_agg(model,',' ORDER BY model) FROM beichen_group_price_observations
  WHERE provider_id=r.provider_id AND group_id=11 AND source='website'
),'')
FROM beichen_crawl_runs r WHERE r.provider_id=$1`, provider).Scan(&raw, &history)
		if err != nil || string(raw) != wantRaw || history != wantHistory {
			t.Fatalf("unexpected directory evidence: raw matches=%t history=%q err=%v", string(raw) == wantRaw, history, err)
		}
	}
	login(oldSession)
	if _, err := s.syncWebsiteDirectory(ctx, provider, true); err != nil {
		t.Fatal(err)
	}
	initial, _ := readScanWebsiteDirectory(t, ctx, db, provider)
	if initial.LastSuccess == nil {
		t.Fatal("missing initial directory success")
	}
	assertEvidence(baseline, "model-baseline")

	pauseOld = true
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.syncWebsiteDirectory(scanCtx, provider, true)
		done <- err
	}()
	select {
	case <-oldStarted:
	case <-ctx.Done():
		t.Fatal("old session did not start the directory request")
	}
	login(newSession)
	keys, err := s.RefreshWebsiteKeys(ctx, provider)
	if err != nil || keys.Status != "current" || keys.LastSeen == nil || len(keys.Keys) != 1 || keys.Keys[0].ID != 51 {
		t.Fatalf("new session did not publish its Key list: %+v, %v", keys, err)
	}
	close(releaseOld)
	select {
	case err := <-done:
		if !errors.Is(err, errWebsiteDirectorySessionChanged) {
			t.Fatalf("old directory session was not rejected: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("old directory request did not finish")
	}
	view, platforms := readScanWebsiteDirectory(t, ctx, db, provider)
	if view.State != "stale" || view.LastSuccess == nil || !view.LastSuccess.Equal(*initial.LastSuccess) ||
		view.LastRun == nil || len(view.LastRun.Failures) != 1 || view.LastRun.Failures[0].Path != "session" ||
		len(platforms) != 1 || len(platforms[0].Models) != 1 || platforms[0].Models[0].Name != "model-baseline" {
		t.Fatalf("late session changed the saved directory: %+v, %+v", view, platforms)
	}
	assertEvidence(baseline, "model-baseline")
	keysAfter, err := s.WebsiteKeys(ctx, provider)
	if err != nil || keysAfter.Status != "current" || keysAfter.LastSeen == nil || !keysAfter.LastSeen.Equal(*keys.LastSeen) ||
		len(keysAfter.Keys) != 1 || keysAfter.Keys[0].ID != 51 {
		t.Fatalf("late directory changed the new Key list: %+v, %v", keysAfter, err)
	}
	if _, err := s.syncWebsiteDirectory(ctx, provider, true); err != nil {
		t.Fatalf("old session did not release the directory lease: %v", err)
	}
	recovered, platforms := readScanWebsiteDirectory(t, ctx, db, provider)
	if recovered.State != "fresh" || recovered.LastSuccess == nil || !recovered.LastSuccess.After(*initial.LastSuccess) ||
		len(platforms) != 1 || len(platforms[0].Models) != 1 || platforms[0].Models[0].Name != "model-current" {
		t.Fatalf("new session did not publish its directory: %+v, %+v", recovered, platforms)
	}
	assertEvidence(current, "model-baseline,model-current")
}
