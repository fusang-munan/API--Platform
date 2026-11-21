package beichen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

const panelAccountDirectory = `{"code":0,"data":[{"name":"fixture","platforms":[{"platform":"openai","models":["fixture-model"]}]}]}`

func usePanelAuthFixture(t *testing.T, service *Service, handler http.HandlerFunc) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	service.client.Transport = transportFunc(func(in *http.Request) (*http.Response, error) {
		if in.URL.Host != "beichenapi.com" {
			return nil, fmt.Errorf("unexpected host in panel test")
		}
		req := in.Clone(in.Context())
		req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
		return upstream.Client().Transport.RoundTrip(req)
	})
}

func TestBeichenPanelAccountPasswordPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	manager := switchTestManager(t)
	service, restarted := New(db, manager), New(db, manager)
	now := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return now }
	restarted.now = service.now
	service.ConfigurePanelFallback("unused-panel-environment-token")

	const email = "session@example.test"
	const password = "fixture-only-panel-password"
	const deniedPassword = "fixture-only-denied-password"
	const initial = "fixture-initial-access"
	const renewed = "fixture-renewed-access"
	const relogged = "fixture-relogin-access"
	const firstRefresh = "fixture-cookie-refresh"
	const secondRefresh = "fixture-cookie-next"
	const newRefresh = "fixture-json-refresh"
	var logins, refreshes, verifications, reads atomic.Int32
	usePanelAuthFixture(t, service, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/auth/login":
			var fields map[string]string
			if json.NewDecoder(r.Body).Decode(&fields) != nil || fields["email"] != email {
				t.Error("wrong login request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if fields["password"] != password {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"message":%q}`, fields["password"])
				return
			}
			if logins.Add(1) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: firstRefresh, HttpOnly: true, Secure: true})
				fmt.Fprintf(w, `{"code":0,"data":{"access_token":%q,"expires_in":60}}`, initial)
			} else {
				fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":600}`, relogged, newRefresh)
			}
		case "POST /api/v1/auth/refresh":
			refreshes.Add(1)
			cookie, err := r.Cookie("refresh_token")
			if err != nil || cookie.Value != firstRefresh && cookie.Value != secondRefresh {
				t.Error("refresh cookie missing or wrong")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if cookie.Value == secondRefresh {
				fmt.Fprint(w, `{"code":401,"message":"invalid refresh cookie"}`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: secondRefresh, HttpOnly: true, Secure: true})
			fmt.Fprintf(w, `{"code":0,"data":{"access_token":%q,"token_type":"Bearer","expires_in":600}}`, renewed)
		case "GET /api/v1/auth/session":
			verifications.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+initial {
				t.Error("wrong bearer for session verification")
			}
			fmt.Fprint(w, `{"code":0,"data":{"authenticated":true}}`)
		case "GET /api/v1/channels/available":
			reads.Add(1)
			switch r.Header.Get("Authorization") {
			case "Bearer " + renewed:
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer " + relogged:
				fmt.Fprint(w, panelPayload(panelSecond))
			default:
				t.Error("pricing used an unexpected bearer")
				w.WriteHeader(http.StatusUnauthorized)
			}
		default:
			t.Error("request was not an allowed website endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	restarted.client = service.client

	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	wrong := PanelAccountLogin{Mode: "password", Email: email, Password: deniedPassword}
	for i := 1; i <= 2; i++ {
		view, err := service.LoginPanelAccount(ctx, provider, actor, wrong)
		if err != nil || view.Status != "error" || view.Configured || view.FailureCount != i ||
			view.LastError == nil || !strings.Contains(*view.LastError, "核对账号") {
			t.Fatalf("登录失败未按中文状态记录，第 %d 次: %v", i, err)
		}
	}
	if _, _, err := service.managedPanelAuthorization(ctx, provider, "another-old-token"); err == nil || reads.Load() != 0 {
		t.Fatal("失败的存储账号允许使用旧 Token")
	}
	view, err := service.LoginPanelAccount(ctx, provider, actor, PanelAccountLogin{Mode: "password", Email: email, Password: password})
	if err != nil || !view.Configured || view.Status != "active" || view.Email != email || view.FailureCount != 0 ||
		view.ExpiresAt == nil || !view.ExpiresAt.Equal(now.Add(time.Minute)) || view.LastVerifiedAt == nil {
		t.Fatalf("登录成功状态错误: %v", err)
	}
	account, _, err := readPanelAccount(ctx, db, provider)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		data   []byte
		secret string
	}{{account.email, email}, {account.password, password}, {account.token, initial}, {account.refresh, firstRefresh}} {
		if len(field.data) == 0 || bytes.Contains(field.data, []byte(field.secret)) {
			t.Fatal("凭证列为空或存在明文")
		}
	}
	view, err = service.VerifyPanelAccount(ctx, provider)
	if err != nil || view.Status != "active" || verifications.Load() != 1 {
		t.Fatalf("auth/session 验证未成功: %v", err)
	}

	now = now.Add(2 * time.Minute)
	var wg sync.WaitGroup
	for _, runner := range []*Service{service, restarted} {
		wg.Add(1)
		go func(runner *Service) {
			defer wg.Done()
			auth, found, err := runner.storedPanelAuthorization(ctx, provider)
			if err != nil || !found || auth.bearer != renewed {
				t.Errorf("多进程读到的续期 Token 错误: %v", err)
			}
		}(runner)
	}
	wg.Wait()
	if refreshes.Load() != 1 || logins.Load() != 1 {
		t.Fatal("过期时应只有一次 refresh")
	}
	access, err := restarted.loadPriceAccess(ctx, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	access.legacyToken = "unused-old-per-key-token"
	group := int64(2)
	peak := false
	quote := restarted.fetchChannelQuote(ctx, access, "sk-fixture-inference", billingInfo{
		GroupID: &group, ObservedAt: now, PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1",
	}, []catalog.ProviderEntry{{ID: "shared"}})
	if quote.err != nil || len(quote.rows) != 1 || reads.Load() != 2 || refreshes.Load() != 2 || logins.Load() != 2 {
		t.Fatalf("采价遇 401 应先刷新、刷新被拒后重新登录并重试一次: %v", quote.err)
	}
	status, err := restarted.PanelAccountStatus(ctx, provider)
	if err != nil || status.Status != "active" || status.FailureCount != 0 || status.LastError != nil ||
		status.LastVerifiedAt == nil || !status.LastVerifiedAt.Equal(now) {
		t.Fatalf("自动重登后未持久化健康状态: %v", err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, deniedPassword, initial, renewed, relogged, firstRefresh, secondRefresh, newRefresh} {
		if strings.Contains(logs.String(), secret) || bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("日志或状态响应包含测试秘密")
		}
	}
}

func TestBeichenPanelAccountTokenAndCrawlPostgres(t *testing.T) {
	ctx, db, provider, actor, credentials := newActionsPostgres(t)
	service := New(db, switchTestManager(t))
	const stored = "fixture-token-mode"
	const fallback = "fixture-env-bearer"
	var reads, sessionReads, logouts, usageReads atomic.Int32
	var rejected atomic.Bool
	usePanelAuthFixture(t, service, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/auth/session":
			sessionReads.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "POST /api/v1/auth/logout":
			logouts.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+stored {
				t.Error("logout used a different bearer")
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/v1/keys":
			if r.Header.Get("Authorization") != "Bearer "+stored {
				t.Error("网页 Key 列表没有使用保存的会话")
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":42,"user_id":17,"group_id":2,"status":"active"}],"total":1,"page":1,"page_size":100,"pages":1}}`)
		case "GET /api/v1/usage":
			usageReads.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+stored || r.URL.Query().Get("api_key_id") != "42" {
				t.Error("网页用量没有使用同一份会话或未核对 Key")
			}
			fmt.Fprintf(w, `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":%d,"pages":1}}`, receiptPageSize)
		case "GET /api/v1/channels/available":
			count := reads.Add(1)
			want := stored
			if count == 1 {
				want = fallback
			}
			if r.Header.Get("Authorization") != "Bearer "+want {
				t.Error("crawler did not choose the expected bearer")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if rejected.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, panelAccountDirectory)
		default:
			t.Error("token mode must not sign in or refresh")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	service.ConfigurePanelFallback(fallback)
	view, err := service.PanelAccountStatus(ctx, provider)
	if err != nil || view.Status != "none" || view.Configured || view.Crawl.LastRunAt != nil {
		t.Fatalf("未配置状态错误: %v", err)
	}
	check, err := service.CheckPanelAccount(ctx, provider)
	if err != nil || check.Platforms == nil || *check.Platforms != 1 || *check.Models != 1 || reads.Load() != 1 {
		t.Fatalf("环境变量兜底检查应读一次官网: %v", err)
	}
	login := PanelAccountLogin{Mode: "token", Token: stored}
	if _, err := service.LoginPanelAccount(ctx, provider, uuid.New(), login); err == nil {
		t.Fatal("未授权的本机账号可以保存凭证")
	}
	view, err = service.LoginPanelAccount(ctx, provider, actor, login)
	if err != nil || !view.Configured || view.LastVerifiedAt != nil || reads.Load() != 1 {
		t.Fatalf("Token 模式应直接入库: %v", err)
	}
	view, err = service.VerifyPanelAccount(ctx, provider)
	if err != nil || view.LastVerifiedAt == nil || view.Status != "active" || sessionReads.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("session 不可用时应验证渠道页: %v", err)
	}
	keyID := int64(42)
	if err := service.SetPanelAccess(ctx, provider, credentials[0], actor,
		PanelAccessInput{Enabled: true, GroupID: 2, UpstreamKeyID: &keyID, Token: "unused-legacy-usage-token"}); err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_provision_channels
  (credential_id,channel,key_fingerprint,discover_ids,upstream_user_id) VALUES ($1,'Gemini',sha256('fixture'::bytea),false,17)`, credentials[0])
	binding, err := service.panelUsageAccess(ctx, credentials[0])
	if err != nil || !binding.managed || binding.auth.bearer != stored {
		t.Fatalf("逐笔用量没有从统一入口取得凭证: %v", err)
	}
	if _, err := readPanelUsage(ctx, service, &binding, func(bearer string) (receiptPage, error) {
		return fetchReceiptPage(ctx, service.client, bearer, url.Values{"api_key_id": {"42"}}, 1)
	}); err != nil || usageReads.Load() != 1 {
		t.Fatalf("未按匹配的账号和 Key 读取网页用量: %v", err)
	}

	service.ConfigurePanelFallback("")
	if err := service.syncWebsiteDirectories(ctx, nil); err != nil || reads.Load() != 3 {
		t.Fatalf("只有数据库会话时，定时抓取也应启动: %v", err)
	}
	service.ConfigurePanelFallback(fallback)
	summary, err := service.CrawlPanelDirectory(ctx, provider)
	if err != nil || summary.LastRunAt == nil || summary.Models == nil || *summary.Models != 1 ||
		*summary.Failures != 0 || reads.Load() != 4 {
		t.Fatalf("教师手动抓取未用存储会话或未保存摘要: %v", err)
	}
	rejected.Store(true)
	_, err = service.CrawlPanelDirectory(ctx, provider)
	if err == nil || !strings.Contains(err.Error(), "粘贴新的 Token") || reads.Load() != 5 {
		t.Fatalf("Token 401 后应提示更新，且无兜底请求: %v", err)
	}
	view, err = service.PanelAccountStatus(ctx, provider)
	if err != nil || view.Status != "expired" || view.FailureCount != 1 || view.LastVerifiedAt == nil {
		t.Fatalf("Token 过期状态未入库: %v", err)
	}
	if view.Crawl.Failures == nil || *view.Crawl.Failures != 1 || len(view.Crawl.FailureDetails) != 1 ||
		view.Crawl.FailureDetails[0].Path != "response" {
		t.Fatal("教师页应能重新读取抓取失败详情")
	}
	if _, err := service.CheckPanelAccount(ctx, provider); err == nil || reads.Load() != 5 {
		t.Fatal("状态已过期的 Token 被重复使用")
	}
	view, err = service.ClearPanelAccount(ctx, provider, actor)
	if err != nil || view.Configured || view.Status != "none" || view.Crawl.LastRunAt == nil || logouts.Load() != 1 {
		t.Fatalf("未清除状态或未保留抓取摘要: %v", err)
	}
	if _, err := service.CheckPanelAccount(ctx, provider); err == nil || reads.Load() != 5 {
		t.Fatal("清除后静默恢复环境变量")
	}

	now := time.Now()
	service.now = func() time.Time { return now }
	exp := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Minute).Unix())))
	view, err = service.LoginPanelAccount(ctx, provider, actor, PanelAccountLogin{Mode: "token", Token: "header." + exp + ".signature"})
	if err != nil || view.ExpiresAt == nil || view.Status != "active" {
		t.Fatalf("无法读出 Token 到期时间: %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := service.storedPanelAuthorization(ctx, provider); err == nil || reads.Load() != 5 {
		t.Fatal("已过期的 Token 仍可用于抓取")
	}
}
