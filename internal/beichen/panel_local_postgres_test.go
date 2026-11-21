package beichen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBeichenPanelAccountSharedImportPostgres(t *testing.T) {
	ctx, db, provider, actor, _ := newActionsPostgres(t)
	manager := switchTestManager(t)
	server := New(db, manager)
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	var refreshes, channelReads atomic.Int32
	const access, refresh, newer = "fixture-shared-access", "fixture-shared-refresh", "fixture-shared-renewed"
	const latest = "fixture-shared-latest"
	usePanelAuthFixture(t, server, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/auth/login":
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":30}`, access, refresh)
		case "POST /api/v1/auth/refresh":
			token := newer
			if refreshes.Add(1) > 1 {
				token = latest
			}
			fmt.Fprintf(w, `{"access_token":%q,"expires_in":600}`, token)
		case "GET /api/v1/auth/session":
			fmt.Fprint(w, `{"authenticated":false}`)
		case "GET /api/v1/channels/available":
			read := channelReads.Add(1)
			if read == 2 || read == 1 && r.Header.Get("Authorization") != "Bearer "+newer ||
				read > 2 && r.Header.Get("Authorization") != "Bearer "+latest {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, panelAccountDirectory)
		default:
			t.Error("未允许的假北辰请求")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	view, err := server.LoginPanelAccount(ctx, provider, actor,
		PanelAccountLogin{Mode: "password", Email: "shared@example.test", Password: "shared-password-fixture"})
	if err != nil || view.Status != "active" {
		t.Fatalf("登录失败: %v", err)
	}
	var authority atomic.Pointer[Service]
	authority.Store(server)
	school := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/beichen/local-session/"+provider.String() {
			t.Error("本机会话请求未使用约定路径")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		session, err := authority.Load().ReadSharedPanelSession(r.Context(), provider,
			strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), body["rejected_token_sha256"])
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": session})
	}))
	defer school.Close()
	export, err := server.ExportPanelConfiguration(ctx, provider, actor, school.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{access, refresh, "shared-password-fixture"} {
		if strings.Contains(export.Env(), secret) {
			t.Fatal("导出文件携带北辰账号秘密")
		}
	}
	path := filepath.Join(t.TempDir(), "beichen-session.env")
	if err := os.WriteFile(path, []byte(export.Env()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEICHEN_SESSION_LOCAL_FILE", path)
	local := New(nil, nil)
	if err := local.ConfigureImportedPanelSession(); err != nil {
		t.Fatal(err)
	}
	local.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "beichenapi.com" {
			return server.client.Transport.RoundTrip(req)
		}
		return school.Client().Transport.RoundTrip(req)
	})
	local.ConfigurePanelFallback("unused-local-env-token")

	// Emulate a new server process using the same database after access expiry.
	restarted := New(db, manager)
	now = now.Add(time.Minute)
	restarted.now, restarted.client = server.now, server.client
	authority.Store(restarted)
	summary, err := local.CheckPanelAccount(ctx, provider)
	if err != nil || summary.Models == nil || *summary.Models != 1 || refreshes.Load() != 1 || channelReads.Load() != 1 {
		t.Fatalf("导入后未复用服务器重启时保存的续期会话: %v", err)
	}
	if _, err := local.CheckPanelAccount(ctx, provider); err == nil || refreshes.Load() != 2 || channelReads.Load() != 2 {
		t.Fatalf("本机检查遇 401 时应只读一次渠道页并由服务器续期: %v", err)
	}
	if _, err := local.CheckPanelAccount(ctx, provider); err != nil || channelReads.Load() != 3 {
		t.Fatalf("同一份本机配置应继续使用已续期的 Token: %v", err)
	}
	if _, err := restarted.ExportPanelConfiguration(ctx, provider, actor, school.URL); err != nil {
		t.Fatal(err)
	}
	if err := local.CheckSharedPanelConnection(ctx, provider); err == nil || channelReads.Load() != 3 {
		t.Fatal("先前的本机配置没有撤销或静默用了环境变量")
	}
}
