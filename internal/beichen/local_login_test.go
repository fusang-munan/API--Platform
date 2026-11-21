package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalLoginPersistsAndRenewsForCrawlers(t *testing.T) {
	const email = "window@example.test"
	const password = "fixture-window-password"
	const access = "fixture-window-access"
	const refresh = "fixture-window-refresh"
	const renewed = "fixture-window-renewed"
	const captcha = "481429"
	store := &LocalLoginStore{dir: t.TempDir()}
	window, crawler := New(nil, nil), New(nil, nil)
	window.UseLocalLogin(store)
	crawler.UseLocalLogin(&LocalLoginStore{dir: store.dir})
	now := time.Now().UTC().Truncate(time.Second)
	window.now = func() time.Time { return now }
	crawler.now = window.now
	var signIns, refreshes, sessionChecks atomic.Int32
	var rejectAccess atomic.Bool
	usePanelAuthFixture(t, window, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/auth/login":
			var fields map[string]string
			if json.NewDecoder(r.Body).Decode(&fields) != nil || fields["email"] != email || fields["password"] != password || fields["code"] != captcha {
				t.Error("unexpected login fields")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			signIns.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "fixture_refresh", Value: refresh, Path: localAuthPath + "refresh", MaxAge: 600, Secure: true, HttpOnly: true})
			fmt.Fprintf(w, `{"code":0,"data":{"access_token":%q,"expires_in":60}}`, access)
		case "POST /api/v1/auth/refresh":
			cookie, err := r.Cookie("fixture_refresh")
			if err != nil || cookie.Value != refresh {
				t.Error("encrypted refresh cookie was not sent")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			refreshes.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "fixture_refresh", Path: localAuthPath + "refresh", MaxAge: -1})
			fmt.Fprintf(w, `{"access_token":%q,"expires_in":600}`, renewed)
		case "GET /api/v1/auth/session":
			sessionChecks.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+access && r.Header.Get("Authorization") != "Bearer "+renewed {
				t.Error("auth/session did not receive a bearer")
			}
			if _, err := r.Cookie("fixture_refresh"); err == nil {
				t.Error("refresh-only cookie was sent to auth/session")
			}
			fmt.Fprintf(w, `{"code":0,"data":{"user":{"email":%q},"authenticated":true}}`, email)
		case "GET /api/v1/channels/available":
			bearer := r.Header.Get("Authorization")
			if bearer != "Bearer "+renewed && (bearer != "Bearer "+access || rejectAccess.Load()) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `[{"platforms":[{"platform":"fixture","models":["fixture-model"]},{"platform":"broken","models":{}}]}]`)
		default:
			t.Error("unexpected site endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	crawler.client = window.client
	ctx := context.Background()
	if status, err := window.ReadLocalLogin(ctx); err != nil || status != nil {
		t.Fatalf("unexpected initial status: %v, %v", status, err)
	}
	status, err := window.LoginLocally(ctx, LocalLoginInput{
		PanelAccountLogin: PanelAccountLogin{Mode: "password", Email: email, Password: password}, Code: captcha,
	})
	if err != nil || status.Account != email || !status.CanRefresh || status.ExpiresAt == nil ||
		!status.ExpiresAt.Equal(now.Add(time.Minute)) || status.LastVerifiedAt == nil || !status.LastVerifiedAt.Equal(now) {
		t.Fatalf("wrong verified login status: %v, %v", status, err)
	}
	stored, err := store.load()
	if err != nil || stored.Password != "" || len(stored.Cookies) != 1 || stored.Access != access {
		t.Fatal("session file did not retain the expected renewal data")
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, localSessionFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{password, email, access, refresh, captcha, "access_token", "refresh_token"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("encrypted session file reveals a private field")
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{localSessionFile, localSessionKey} {
			info, err := os.Stat(filepath.Join(store.dir, name))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("unsafe permissions on %s", name)
			}
		}
	}
	assertCrawl := func(service *Service) {
		t.Helper()
		report, err := service.CrawlDirectory(ctx, CrawlOptions{})
		if !errors.Is(err, errWebsiteDirectoryPartial) || report.PlatformCount != 1 || report.ModelCount != 1 || len(report.Failures) != 1 || report.Failures[0].Platform != "broken" {
			t.Errorf("wrong crawler report: %+v, %v", report.CrawlRun, err)
		}
	}
	assertCrawl(crawler)
	// Reject an unexpired bearer to exercise the same post-401 path used by the CLI.
	rejectAccess.Store(true)
	var group sync.WaitGroup
	for _, service := range []*Service{window, crawler} {
		group.Add(1)
		go func() {
			defer group.Done()
			assertCrawl(service)
		}()
	}
	group.Wait()
	if refreshes.Load() != 1 || signIns.Load() != 1 || sessionChecks.Load() != 2 {
		t.Fatalf("expected one login, one serialized refresh and two verifications; got %d, %d, %d", signIns.Load(), refreshes.Load(), sessionChecks.Load())
	}
	status, err = crawler.ReadLocalLogin(ctx)
	if err != nil || status.CanRefresh || status.ExpiresAt == nil || !status.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("rotation or cookie deletion was not persisted: %v, %v", status, err)
	}
	now = now.Add(time.Hour)
	if _, err := crawler.CrawlDirectory(ctx, CrawlOptions{}); !errors.Is(err, errLocalReauthenticate) {
		t.Fatalf("expired session without renewal data should request login: %v", err)
	}
}

func TestLocalLoginShowsSafeRejectionAndVerification(t *testing.T) {
	service := New(nil, nil)
	store := &LocalLoginStore{dir: t.TempDir()}
	service.UseLocalLogin(store)
	const wrong = "fixture-wrong-secret"
	const token = "fixture-rejected-token"
	usePanelAuthFixture(t, service, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/auth/login":
			var fields map[string]string
			_ = json.NewDecoder(r.Body).Decode(&fields)
			if fields["password"] == wrong {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"message":"Incorrect password: %s"}`, wrong)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"code":"CAPTCHA_REQUIRED","message":"请完成人机验证"}`)
		case "GET /api/v1/auth/session":
			if r.Header.Get("Authorization") == "Bearer fixture-valid-web-token" {
				fmt.Fprint(w, ` {"data":{"user":{"email":"token@example.test"}}} `)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"message":"Token expired: %s"}`, token)
		default:
			t.Error("unexpected auth request")
		}
	})
	for _, test := range []struct {
		input  PanelAccountLogin
		want   string
		secret string
	}{
		{PanelAccountLogin{Mode: "password", Email: "window@example.test", Password: wrong}, "Incorrect password", wrong},
		{PanelAccountLogin{Mode: "password", Email: "window@example.test", Password: "fixture-captcha-password"}, "使用 Token 模式", "fixture-captcha-password"},
		{PanelAccountLogin{Mode: "token", Token: token}, "Token expired", token},
	} {
		_, err := service.LoginLocally(context.Background(), LocalLoginInput{PanelAccountLogin: test.input})
		if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), test.secret) {
			t.Fatal("login failure did not show the safe reason")
		}
	}
	if exists, err := store.Exists(); exists || err != nil {
		t.Fatalf("failed login should not create a session: %v", err)
	}
	status, err := service.LoginLocally(context.Background(), LocalLoginInput{
		PanelAccountLogin: PanelAccountLogin{Mode: "token", Token: "fixture-valid-web-token"},
	})
	if err != nil || status.Account != "token@example.test" || status.CanRefresh || status.LastVerifiedAt == nil {
		t.Fatalf("Token mode did not save the verified session: %v, %v", status, err)
	}
}

func TestLocalLoginRefreshesExpiredJSONToken(t *testing.T) {
	store := &LocalLoginStore{dir: t.TempDir()}
	service := New(nil, nil)
	service.UseLocalLogin(store)
	now := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return now }
	var refreshes atomic.Int32
	usePanelAuthFixture(t, service, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/auth/login":
			fmt.Fprint(w, `{"access_token":"fixture-short-access","refresh_token":"fixture-short-refresh","expires_in":1}`)
		case "POST /api/v1/auth/refresh":
			var fields map[string]string
			if json.NewDecoder(r.Body).Decode(&fields) != nil || fields["refresh_token"] != "fixture-short-refresh" {
				t.Error("refresh failed to carry saved JSON token")
			}
			refreshes.Add(1)
			fmt.Fprint(w, `{"access_token":"fixture-next-access","expires_in":600}`)
		case "GET /api/v1/auth/session":
			fmt.Fprint(w, `{"authenticated":true}`)
		default:
			t.Error("unexpected auth request")
		}
	})
	ctx := context.Background()
	const password = "fixture-remembered-password"
	if _, err := service.LoginLocally(ctx, LocalLoginInput{
		PanelAccountLogin: PanelAccountLogin{Mode: "password", Email: "window@example.test", Password: password}, Remember: true,
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	status, err := service.VerifyLocalLogin(ctx)
	if err != nil || status.Expired || refreshes.Load() != 1 || status.LastVerifiedAt == nil || !status.LastVerifiedAt.Equal(now) {
		t.Fatalf("expired account did not refresh: %v, %v", status, err)
	}
	stored, err := store.load()
	if err != nil || stored.Password != password || stored.Refresh != "fixture-short-refresh" || stored.Access != "fixture-next-access" {
		t.Fatal("encrypted storage did not retain selected password and rotated tokens")
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, localSessionFile))
	if err != nil || bytes.Contains(raw, []byte(password)) {
		t.Fatal("remembered password was not protected")
	}
	var sealed sealedLocalLogin
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Ciphertext[len(sealed.Ciphertext)-1] ^= 1
	changed, _ := json.Marshal(sealed)
	if err := writePrivateSessionFile(filepath.Join(store.dir, localSessionFile), changed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); !errors.Is(err, errLocalSessionCipher) {
		t.Fatal("modified ciphertext was accepted")
	}
}
