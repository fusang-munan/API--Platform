package beichen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

func TestWebsiteSnapshotSessionVersionPostgres(t *testing.T) {
	for _, source := range []string{"sealed", "delivered_after_clear", "database"} {
		t.Run(source, func(t *testing.T) {
			ctx, db, provider, actor, _ := newActionsPostgres(t)
			s := New(db, switchTestManager(t))
			var clock atomic.Int64
			clock.Store(time.Now().Unix())
			s.now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }

			const oldAccess = "fixture-old-snapshot-access"
			const newAccess = "fixture-new-snapshot-access"
			const oldRefresh = "fixture-old-snapshot-refresh"
			const newRefresh = "fixture-new-snapshot-refresh"
			identity, err := schoolseal.LoadOrCreate(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			saved := func(access, refresh string) *schoolseal.SessionStore {
				t.Helper()
				store, err := identity.SessionStore(schoolseal.Session{WebEmail: "snapshots@school.example",
					WebAccess: access, WebRefresh: refresh, WebExpiresAt: s.now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				return store
			}
			login := func(access string) {
				t.Helper()
				if _, err := s.LoginPanelAccount(ctx, provider, actor, PanelAccountLogin{Mode: panelModeToken, Token: access}); err != nil {
					t.Fatal(err)
				}
			}
			capture := saved(oldAccess, oldRefresh)
			if source == "database" {
				// The database account takes precedence over a configured capture.
				capture = saved("fixture-unused-capture", "fixture-unused-refresh")
				login(oldAccess)
			}
			if err := s.ConfigureSavedWebSession(capture); err != nil {
				t.Fatal(err)
			}
			if source == "delivered_after_clear" {
				if _, err := s.ClearPanelAccount(ctx, provider, actor); err != nil {
					t.Fatal(err)
				}
			}

			directory := func(model string) string {
				return fmt.Sprintf(`[{"id":1,"platforms":[{"platform":"openai","groups":[{"id":11,"name":"group-11"}],
					"models":[{"name":%q,"pricing":{"billing_mode":"token","input_price":0.00001,"output_price":0.00002}}]}]}]`, model)
			}
			inventory := func(id int) string {
				return fmt.Sprintf(`{"code":0,"data":{"items":[{"id":%d,"user_id":7,"name":"fixture-key","status":"active"}],
					"total":1,"page":1,"page_size":100,"pages":1}}`, id)
			}
			dirStarted, keysStarted := make(chan struct{}), make(chan struct{})
			releaseDir, releaseKeys := make(chan struct{}), make(chan struct{})
			var pause atomic.Bool
			s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && r.URL.String() == webAuthURL+"refresh" && source == "delivered_after_clear" {
					return scanResponse(http.StatusOK, fmt.Sprintf(`{"code":0,"data":{"access_token":%q,"refresh_token":%q,"expires_in":3600}}`, newAccess, newRefresh)), nil
				}
				bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if bearer != oldAccess && bearer != newAccess {
					return nil, errors.New("unexpected snapshot authorization")
				}
				model, key := "model-initial", 41
				if bearer == newAccess {
					model, key = "model-current", 51
				} else if pause.Load() {
					started, release := dirStarted, releaseDir
					if r.URL.String() != panelURL {
						started, release = keysStarted, releaseKeys
					}
					close(started)
					select {
					case <-release:
						model, key = "model-late", 61
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				switch r.URL.Path {
				case "/api/v1/channels/available":
					return scanResponse(http.StatusOK, directory(model)), nil
				case "/api/v1/keys":
					return scanResponse(http.StatusOK, inventory(key)), nil
				default:
					return nil, errors.New("unexpected snapshot endpoint")
				}
			})
			refreshKeys := func(wantID int64) WebsiteKeyInventoryReport {
				t.Helper()
				keys, err := s.RefreshWebsiteKeys(ctx, provider)
				if err != nil || keys.Status != "current" || keys.LastSeen == nil || len(keys.Keys) != 1 || keys.Keys[0].ID != wantID {
					t.Fatalf("Key snapshot %d did not commit: %+v, %v", wantID, keys, err)
				}
				return keys
			}
			if _, err := s.syncWebsiteDirectory(ctx, provider, true); err != nil {
				t.Fatal(err)
			}
			initialDir, _ := readScanWebsiteDirectory(t, ctx, db, provider)
			if initialDir.LastSuccess == nil {
				t.Fatal("missing initial directory success")
			}
			initialKeys := refreshKeys(41)

			pause.Store(true)
			readCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			dirDone, keysDone := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := s.syncWebsiteDirectory(readCtx, provider, true)
				dirDone <- err
			}()
			go func() { keysDone <- s.refreshWebsiteKeys(readCtx, provider, true) }()
			for _, started := range []chan struct{}{dirStarted, keysStarted} {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("previous generation did not start both reads")
				}
			}

			clock.Add(int64(time.Hour / time.Second))
			switch source {
			case "sealed":
				if err := s.ConfigureSavedWebSession(saved(newAccess, newRefresh)); err != nil {
					t.Fatal(err)
				}
			case "delivered_after_clear":
				if access, err := s.webPanelToken(ctx); err != nil || access != newAccess {
					t.Fatalf("captured access did not rotate: %v", err)
				}
				if rotated, err := capture.Load(); err != nil || rotated.WebRefresh != newRefresh {
					t.Fatalf("refresh rotation was not saved: %v", err)
				}
			case "database":
				login(newAccess)
			}
			wait := func(done <-chan error) error {
				t.Helper()
				select {
				case err := <-done:
					return err
				case <-ctx.Done():
					t.Fatal("old snapshot request did not finish")
					return ctx.Err()
				}
			}
			close(releaseKeys)
			if err := wait(keysDone); err != nil {
				t.Fatal(err)
			}
			preservedKeys, err := s.WebsiteKeys(ctx, provider)
			if err != nil || preservedKeys.Status != "unavailable" || preservedKeys.LastError != errWebsiteKeySessionChanged.Error() ||
				preservedKeys.LastSeen == nil || !preservedKeys.LastSeen.Equal(*initialKeys.LastSeen) ||
				len(preservedKeys.Keys) != 1 || preservedKeys.Keys[0].ID != 41 {
				t.Fatalf("late read changed the saved Key inventory: %+v, %v", preservedKeys, err)
			}
			freshKeys := refreshKeys(51)
			if !freshKeys.LastSeen.After(*initialKeys.LastSeen) {
				t.Fatal("current session did not advance the Key inventory time")
			}
			close(releaseDir)
			if err := wait(dirDone); !errors.Is(err, errWebsiteDirectorySessionChanged) {
				t.Fatalf("late directory did not report a changed session: %v", err)
			}
			dir, platforms := readScanWebsiteDirectory(t, ctx, db, provider)
			if dir.State != "stale" || dir.LastSuccess == nil || !dir.LastSuccess.Equal(*initialDir.LastSuccess) ||
				dir.LastRun == nil || len(dir.LastRun.Failures) != 1 || dir.LastRun.Failures[0].Path != "session" ||
				!dir.LastRun.Failures[0].Retained || len(platforms) != 1 || len(platforms[0].Models) != 1 ||
				platforms[0].Models[0].Name != "model-initial" {
				t.Fatalf("late read changed the saved directory: %+v, %+v", dir, platforms)
			}
			var raw, history string
			err = db.QueryRowContext(ctx, `
SELECT convert_from(raw_website_response,'UTF8'),COALESCE((
  SELECT string_agg(model,',' ORDER BY model) FROM beichen_group_price_observations
  WHERE provider_id=$1 AND source='website'
),'') FROM beichen_crawl_runs WHERE provider_id=$1`, provider).Scan(&raw, &history)
			if err != nil || raw != directory("model-initial") || history != "model-initial" {
				t.Fatalf("late directory affected evidence: raw matches=%t history=%q err=%v", raw == directory("model-initial"), history, err)
			}
			keysAfter, err := s.WebsiteKeys(ctx, provider)
			if err != nil || keysAfter.Status != "current" || keysAfter.LastSeen == nil || !keysAfter.LastSeen.Equal(*freshKeys.LastSeen) ||
				len(keysAfter.Keys) != 1 || keysAfter.Keys[0].ID != 51 {
				t.Fatalf("old directory affected the new Key snapshot: %+v, %v", keysAfter, err)
			}
			if _, err := s.syncWebsiteDirectory(ctx, provider, true); err != nil {
				t.Fatal(err)
			}
			dir, platforms = readScanWebsiteDirectory(t, ctx, db, provider)
			if dir.State != "fresh" || dir.LastSuccess == nil || !dir.LastSuccess.After(*initialDir.LastSuccess) ||
				len(platforms) != 1 || len(platforms[0].Models) != 1 || platforms[0].Models[0].Name != "model-current" {
				t.Fatalf("current session did not publish its directory: %+v, %+v", dir, platforms)
			}
		})
	}
}
