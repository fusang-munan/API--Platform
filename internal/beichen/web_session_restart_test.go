package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

func TestSavedWebSessionSurvivesProcessRestart(t *testing.T) {
	const dirEnv = "NEBULA_TEST_ROTATION_DIR"
	const phaseEnv = "NEBULA_TEST_ROTATION_PHASE"
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	seed := schoolseal.Session{
		WebEmail: "school@example.test", WebAccess: "capture-access-fixture",
		WebRefresh: "capture-refresh-fixture", WebExpiresAt: now.Add(-time.Minute),
	}
	dir := os.Getenv(dirEnv)
	if dir == "" {
		dir = filepath.Join(t.TempDir(), "delivery")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, phase := range []string{"refresh", "restart"} {
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSavedWebSessionSurvivesProcessRestart$")
			child.Env = append(os.Environ(), dirEnv+"="+dir, phaseEnv+"="+phase)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("%s child failed: %v\n%s", phase, err, output)
			}
		}
		files, err := filepath.Glob(filepath.Join(dir, "web-session-*.sealed.json"))
		if err != nil || len(files) != 1 {
			t.Fatal("expected a single persisted rotation for the capture")
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{seed.WebEmail, "rotated-access-fixture", "rotated-refresh-fixture", "second-access-fixture", "second-refresh-fixture"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("plaintext session data on the persistent volume")
			}
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(files[0])
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("persisted session must have mode 0600")
			}
		}
		id, err := schoolseal.LoadOrCreate(dir)
		if err != nil {
			t.Fatal(err)
		}
		seed.WebRefresh = "new-capture-refresh-fixture"
		store, err := id.SessionStore(seed)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := store.Load(); err != nil || got.WebRefresh != seed.WebRefresh {
			t.Fatal("a new capture loaded tokens from the previous capture")
		}
		return
	}

	phase := os.Getenv(phaseEnv)
	id, err := schoolseal.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := id.SessionStore(seed)
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil)
	s.now = func() time.Time { return now }
	if err := s.ConfigureSavedWebSession(store); err != nil {
		t.Fatal(err)
	}

	refreshes := 0
	wantRefresh, wantAccess := seed.WebRefresh, "rotated-access-fixture"
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.Method + " " + r.URL.String() {
		case "POST " + webAuthURL + "refresh":
			refreshes++
			var fields map[string]string
			if json.NewDecoder(r.Body).Decode(&fields) != nil || fields["refresh_token"] != wantRefresh {
				t.Fatal("refresh did not use the expected persisted credential")
			}
			body = `{"code":0,"data":{"access_token":"rotated-access-fixture","refresh_token":"rotated-refresh-fixture","token_type":"Bearer","expires_in":3600}}`
			if phase == "restart" {
				body = `{"code":0,"data":{"access_token":"second-access-fixture","refresh_token":"second-refresh-fixture","token_type":"Bearer","expires_in":3600}}`
			}
		case "GET " + panelURL:
			if r.Header.Get("Authorization") != "Bearer "+wantAccess {
				t.Fatal("price read did not use the rotated access")
			}
			body = panelPayload(panelSecond)
		default:
			t.Fatal("unexpected request in restart test")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	readPrice := func(wantRefreshes int) {
		t.Helper()
		group, peak := int64(2), false
		info := billingInfo{GroupID: &group, PeakEnabled: &peak, ObservedAt: now, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
		quote := s.fetchChannelQuote(context.Background(), priceAccess{channel: "G3"}, "sk-fixture", info, []catalog.ProviderEntry{{ID: "shared"}})
		if quote.err != nil || len(quote.rows) != 1 || refreshes != wantRefreshes {
			t.Fatalf("price read failed after %d refreshes: %v", refreshes, quote.err)
		}
	}
	if phase == "refresh" {
		readPrice(1)
	} else {
		readPrice(0)
		now = now.Add(2 * time.Hour)
		wantRefresh, wantAccess = "rotated-refresh-fixture", "second-access-fixture"
		readPrice(1)
	}
}
