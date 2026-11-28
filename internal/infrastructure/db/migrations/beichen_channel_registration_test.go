package migrations_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	store "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type channelFixture struct {
	ctx      context.Context
	db       *sql.DB
	provider uuid.UUID
	service  *controlplane.Service
	crypto   *security.Manager
	handler  http.Handler
	token    string
}

func newChannelFixture(t *testing.T) channelFixture {
	t.Helper()
	dsn := os.Getenv("NEBULA_CHANNEL_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("需要单独的本机 nebula_test_* PostgreSQL 测试库")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || len(config.Fallbacks) != 0 || config.TLSConfig != nil ||
		!strings.HasPrefix(config.Database, "nebula_test_") || config.Password != "" {
		t.Fatal("只允许无密码、回环地址、sslmode=disable 的独立 nebula_test_* 库")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin, err := sql.Open(store.DriverName, dsn)
	if err != nil {
		t.Fatal("无法打开独立测试库")
	}
	t.Cleanup(func() { admin.Close() })
	schema := "channel_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	isolated, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("无效的测试库 URL")
	}
	params := isolated.Query()
	params.Set("search_path", schema+",public")
	isolated.RawQuery = params.Encode()
	client, db, err := store.Open(ctx, isolated.String())
	if err != nil {
		t.Fatal("无法连接独立测试 schema")
	}
	t.Cleanup(func() { client.Close() })
	db.SetMaxOpenConns(1)
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)
	material := bytes.Repeat([]byte{41}, 32)
	manager, err := security.NewManager(material, material, material, material, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{})
	f := channelFixture{ctx: ctx, db: db, service: service, crypto: manager}
	actor := uuid.New()
	f.exec(t, `
INSERT INTO users (id,name,email,password_hash,role,status,created_at,updated_at)
VALUES ($1,'渠道测试教师','teacher@example.test','!','teacher','active',now(),now())`, actor)
	if err := f.row("SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&f.provider); err != nil {
		t.Fatal(err)
	}
	f.token, _, err = manager.IssueAccessToken(actor, "teacher")
	if err != nil {
		t.Fatal(err)
	}
	f.handler = httpapi.NewServer(service, manager, nil, nil, httpapi.HealthDependencies{},
		httpapi.Config{AllowedOrigin: "https://school.example"}).Handler()
	return f
}

func (f channelFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f channelFixture) row(query string, args ...any) *sql.Row {
	return f.db.QueryRowContext(f.ctx, query, args...)
}

func (f channelFixture) request(t *testing.T, method, path, transport string, payload map[string]any, want int, message string) controlplane.ProviderCredentialView {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://school.example"
	if transport != "tls" {
		baseURL = "http://school.example"
	}
	req := httptest.NewRequest(method, baseURL+"/api/v1/teacher/providers/"+f.provider.String()+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	if transport == "forwarded" || transport == "spoofed" {
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Origin", "https://school.example")
		req.RemoteAddr = "172.30.10.4:45678"
		if transport == "spoofed" {
			req.RemoteAddr = "203.0.113.2:45678"
		}
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != want || !strings.Contains(recorder.Body.String(), message) {
		t.Fatalf("%s %s (%s): HTTP %d, want %d with %q", method, path, transport, recorder.Code, want, message)
	}
	if key, ok := payload["credential"].(string); ok && strings.Contains(recorder.Body.String(), key) {
		t.Fatal("响应含有推理 Key")
	}
	var response struct {
		Data controlplane.ProviderCredentialView
	}
	if want == http.StatusCreated || path != "" && want == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.ID == "" {
			t.Fatal("凭据响应缺少编号")
		}
	}
	return response.Data
}

func TestBeichenIndependentChannelRegistration(t *testing.T) {
	f := newChannelFixture(t)
	keys := make(map[string]uuid.UUID)
	for _, channel := range []string{"CC1", "G3", "GM1", "Gemini"} {
		key := "sk-local-registration-" + channel
		body := map[string]any{"label": "学校 " + channel, "credential": key, "channel": channel}
		if channel == "Gemini" {
			for _, transport := range []string{"plain", "spoofed"} {
				f.request(t, http.MethodPost, "/credentials", transport, body, http.StatusBadRequest, "可信 HTTPS")
			}
		}
		view := f.request(t, http.MethodPost, "/credentials", "forwarded", body, http.StatusCreated, "")
		id := uuid.MustParse(view.ID)
		keys[channel] = id
		digest := sha256.Sum256([]byte(key))
		var registered, pending bool
		var jobs int
		err := f.row(`
SELECT j.channel=$2 AND j.key_fingerprint=$3 AND j.discover_ids AND j.registration_provider_id=$4,
       a.group_id IS NULL AND a.upstream_key_id IS NULL AND a.token_ciphertext IS NULL AND a.updated_at=c.updated_at,
       (SELECT count(*) FROM beichen_datasets WHERE credential_id=c.id AND kind IN ('billing','key_usage'))
FROM provider_credentials c JOIN beichen_provision_channels j ON j.credential_id=c.id
JOIN beichen_panel_access a ON a.credential_id=c.id WHERE c.id=$1`, id, channel, digest[:], f.provider).
			Scan(&registered, &pending, &jobs)
		if err != nil || !registered || !pending || jobs != 2 {
			t.Fatalf("%s 未同时登记渠道、授权占位和同步任务：%v", channel, err)
		}
	}
	f.request(t, http.MethodPost, "/credentials", "tls",
		map[string]any{"label": "另一标签", "credential": "sk-local-duplicate-channel", "channel": "Gemini"}, http.StatusConflict, "已有启用")
	f.request(t, http.MethodPost, "/credentials", "tls",
		map[string]any{"label": "学校 gemini", "credential": "sk-local-duplicate-label", "channel": "Gemini"}, http.StatusConflict, "同名渠道")
	f.request(t, http.MethodPost, "/credentials", "tls",
		map[string]any{"label": "错误渠道", "credential": "sk-local-invalid-channel", "channel": "other"}, http.StatusBadRequest, "学校渠道须为")
	f.request(t, http.MethodPost, "/credentials", "tls", map[string]any{
		"label": "冲突操作", "credential": "sk-local-conflicting-mode", "channel": "Gemini", "replaces_credential_id": keys["GM1"],
	}, http.StatusBadRequest, "选择一种方式")
	f.request(t, http.MethodPatch, "/credentials/"+keys["GM1"].String(), "tls",
		map[string]any{"channel": "Gemini"}, http.StatusBadRequest, "request validation failed")
	var count int
	if err := f.row("SELECT count(*) FROM provider_credentials WHERE provider_id=$1", f.provider).Scan(&count); err != nil || count != 4 {
		t.Fatalf("预期只有四把登记的 Key，实际 %d：%v", count, err)
	}

	f.request(t, http.MethodPatch, "", "tls",
		map[string]any{"base_url": "https://different.example"}, http.StatusConflict, "不能修改接口地址")
	f.request(t, http.MethodPatch, "", "tls",
		map[string]any{"name": "更新名称", "base_url": "https://api.beichenapi.com/v1/"}, http.StatusOK, "")

	call := insertChannelCall(t, f, "gemini-dispatch")
	if err := usage.NewStore(f.db).PrepareDispatch(f.ctx, call, usage.CallRoute{
		Beichen: true, ProviderID: f.provider, CredentialID: keys["Gemini"], UpstreamModelName: "gemini-fixture",
	}); err != nil {
		t.Fatal(err)
	}
	var correctMultiplier bool
	if err := f.row(`
SELECT beichen_dispatch_credential_id=$2 AND beichen_school_multiplier=1.10
FROM gateway_calls WHERE id=$1`, call, keys["Gemini"]).Scan(&correctMultiplier); err != nil || !correctMultiplier {
		t.Fatalf("Gemini 派发缺少学校系数 1.10：%v", err)
	}

	replacement := f.request(t, http.MethodPost, "/credentials", "tls", map[string]any{
		"label": "接替 CC1", "credential": "sk-local-cc1-replacement", "replaces_credential_id": keys["CC1"],
	}, http.StatusCreated, "")
	var holder uuid.UUID
	owner := func(want uuid.UUID) {
		t.Helper()
		if err := f.row(`
SELECT credential_id FROM beichen_provision_channels WHERE registration_provider_id=$1 AND channel='CC1'`, f.provider).
			Scan(&holder); err != nil || holder != want {
			t.Fatalf("接替中学校登记的持有者错误：%v", err)
		}
	}
	owner(keys["CC1"])
	retire := "/credentials/" + keys["CC1"].String()
	f.request(t, http.MethodPatch, retire, "tls", map[string]any{"status": "inactive"}, http.StatusOK, "")
	owner(uuid.MustParse(replacement.ID))
	f.request(t, http.MethodPatch, retire, "tls", map[string]any{"status": "active"}, http.StatusConflict, "已有启用")
}

func insertChannelCall(t *testing.T, f channelFixture, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(t, `
INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
 organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,multiplier_milli,credit_milli,created_at)
VALUES ($1,$2,date_trunc('month',now()),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
 'fixture','fixture','fixture','fixture','gemini-fixture','gemini','/v1beta/models',1000,0,now())`, id, name)
	return id
}

func TestBeichenRestoresRetiredGeminiInPlace(t *testing.T) {
	f := newChannelFixture(t)
	const initial = "sk-local-retired-gemini"
	ciphertext, err := f.crypto.EncryptCredential(initial)
	if err != nil {
		t.Fatal(err)
	}
	legacy := uuid.New()
	f.exec(t, `
INSERT INTO provider_credentials(id,provider_id,label,group_hint,credential_ciphertext,status,created_at,updated_at)
VALUES ($1,$2,'哈吉米','5',$3,'active',now()-interval '1 day',now()),
       (gen_random_uuid(),$2,'幸存渠道',NULL,'fixture','active',now(),now())`, legacy, f.provider, ciphertext)
	f.exec(t, `
INSERT INTO beichen_panel_access(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
SELECT c.id,5,12098,'fixture',u.id,c.updated_at FROM provider_credentials c JOIN users u ON u.role='teacher'
WHERE c.id=$1`, legacy)
	call := insertChannelCall(t, f, "dispatch-before-gemini-retirement")
	if err := usage.NewStore(f.db).PrepareDispatch(f.ctx, call, usage.CallRoute{
		Beichen: true, ProviderID: f.provider, CredentialID: legacy, UpstreamModelName: "gemini-fixture",
	}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `
INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,channel,model,occurred_at,actual_cost,cost_details)
VALUES (99,1,12098,$1,$2,'Gemini','gemini-fixture',now()-interval '1 day',0.25,'{}')`, f.provider, legacy)
	retirement, err := os.ReadFile("0065_retire_beichen_gemini_credential.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, string(retirement))
	f.exec(t, "UPDATE provider_credentials SET retired_at=updated_at WHERE id=$1 AND status='inactive'", legacy)
	raw := func(secret, override string) string {
		return `[{"channel":"GM1","status":"disabled"},{"channel":"G3","status":"disabled"},
{"channel":"CC1","status":"disabled"},{"channel":"Gemini","api_key":"` + secret + `"` + override + `}]`
	}
	if _, err := f.service.ProvisionBeichenChannels(f.ctx, raw("sk-local-wrong-gemini", ""), ""); err == nil ||
		!strings.Contains(err.Error(), "restore=true") {
		t.Fatal("预配置接受了与停用记录不一致的 Key，或没有明确错误")
	}
	assertActive := func(secret string) {
		t.Helper()
		var updated []byte
		var ownRegistration, pending, receiptIntact bool
		var recordedCost string
		err := f.row(`
SELECT c.credential_ciphertext,j.channel='Gemini' AND j.discover_ids AND j.registration_provider_id=$2,
       a.token_ciphertext IS NULL AND a.upstream_key_id IS NULL AND a.group_id IS NULL AND a.updated_at=c.updated_at,
       (SELECT trim_scale(actual_cost)::text FROM beichen_upstream_usage_rows
        WHERE credential_id=c.id AND upstream_key_id=12098 AND row_id=1),
       (SELECT beichen_dispatch_credential_id=c.id AND beichen_receipt_api_key_id=12098
               AND beichen_receipt_panel_revision IS NOT NULL FROM gateway_calls WHERE id=$3)
FROM provider_credentials c JOIN beichen_provision_channels j ON j.credential_id=c.id
JOIN beichen_panel_access a ON a.credential_id=c.id
WHERE c.id=$1 AND c.status='active' AND c.retired_at IS NULL`, legacy, f.provider, call).
			Scan(&updated, &ownRegistration, &pending, &recordedCost, &receiptIntact)
		got, decryptErr := f.crypto.DecryptCredential(updated)
		if err != nil || decryptErr != nil || got != secret || !ownRegistration || !pending || recordedCost != "0.25" || !receiptIntact {
			t.Fatalf("恢复未保留 Gemini 编号、账单金额或待核授权状态：%v", err)
		}
	}
	restored, err := f.service.ProvisionBeichenChannels(f.ctx, raw(initial, ""), "")
	if err != nil || len(restored.AwaitingAccess) != 1 {
		t.Fatalf("预配置未以一致指纹恢复旧凭据：%v", err)
	}
	assertActive(initial)
	patch := "/credentials/" + legacy.String()
	f.request(t, http.MethodPatch, patch, "tls", map[string]any{"status": "inactive"}, http.StatusOK, "")
	const rotated = "sk-local-rotated-gemini"
	restored, err = f.service.ProvisionBeichenChannels(f.ctx, raw(rotated, `,"restore":true`), "")
	if err != nil || len(restored.AwaitingAccess) != 1 {
		t.Fatalf("预配置未按明确配置恢复和轮换：%v", err)
	}
	assertActive(rotated)
	f.request(t, http.MethodPatch, patch, "tls", map[string]any{"status": "inactive"}, http.StatusOK, "")
	const finalKey = "sk-local-patched-gemini"
	body := map[string]any{"status": "active", "credential": finalKey}
	f.request(t, http.MethodPatch, patch, "plain", body, http.StatusBadRequest, "可信 HTTPS")
	if view := f.request(t, http.MethodPatch, patch, "tls", body, http.StatusOK, ""); view.ID != legacy.String() {
		t.Fatal("PATCH 更换了旧凭据编号")
	}
	assertActive(finalKey)
	f.request(t, http.MethodPatch, patch, "tls", body, http.StatusBadRequest, "停用的 Gemini")
}
