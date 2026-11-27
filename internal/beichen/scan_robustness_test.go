package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func scanResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newKeyScanFixture(t *testing.T) (context.Context, *sql.DB, *Service, syncJob) {
	t.Helper()
	ctx, db, provider, _, keys := newActionsPostgres(t)
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential("sk-fixture-scan-key")
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", keys[0], ciphertext)
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET status='inactive' WHERE id<>$1 AND provider_id=$2", keys[0], provider)
	return ctx, db, New(db, manager), syncJob{provider: provider, credential: keys[0], kind: "catalog"}
}

func readScanModels(t *testing.T, ctx context.Context, db *sql.DB, credential uuid.UUID) string {
	t.Helper()
	var names string
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(string_agg(model_id::text,',' ORDER BY model_id),'') FROM provider_model_catalog
WHERE credential_id=$1 AND status='active'`, credential).Scan(&names); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestBeichenScanRobustnessKeySnapshotPostgres(t *testing.T) {
	ctx, db, s, job := newKeyScanFixture(t)
	const full = `{"data":[{"id":"m1"},{"id":"m2"},{"id":"m3"},{"id":"m4"}]}`
	const empty = `{"data":[]}`
	const two = `{"data":[{"id":"m1"},{"id":"m2"}]}`
	body := full
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != modelsPath {
			t.Errorf("unexpected URL path %s", r.URL.Path)
			return nil, errors.New("unexpected request")
		}
		if r.URL.Query().Has("cursor") {
			return scanResponse(http.StatusBadGateway, ""), nil
		}
		return scanResponse(http.StatusOK, body), nil
	})
	run := func(want string, reason string) {
		t.Helper()
		err := s.RescanCatalog(ctx, job.provider, job.credential)
		if (err != nil) != (reason != "") {
			t.Errorf("scan result: %v, expected reason %q", err, reason)
		}
		if got := readScanModels(t, ctx, db, job.credential); got != want {
			t.Errorf("active models: %q, want %q", got, want)
		}
		var failure sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT last_error FROM beichen_datasets WHERE credential_id=$1 AND kind='catalog'", job.credential).Scan(&failure); err != nil {
			t.Fatal(err)
		}
		if reason == "" && failure.Valid || reason != "" && !strings.Contains(failure.String, reason) {
			t.Errorf("stored scan failure: %q, want %q", failure.String, reason)
		}
	}
	const old = "m1,m2,m3,m4"
	run(old, "")
	body = empty
	run(old, "1/3")
	body = `{"data":[{"id":"m1"}],"next_cursor":"page2"}`
	run(old, "第 2 页")
	body = two
	run(old, "1/3")
	body = `{"data":[{"id":"m1"},{"id":"m3"}]}`
	run(old, "1/3")
	body = two
	run(old, "1/3")
	body = `{"data":[{"id":"M2"},{"id":"m1"}]}`
	run(old, "2/3")
	body = two
	run("m1,m2", "")
	body = empty
	run("m1,m2", "1/3")
	run("m1,m2", "2/3")
	run("", "")
}

func TestBeichenScanRobustnessCrossWriterPostgres(t *testing.T) {
	ctx, db, s, job := newKeyScanFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	var commandReads atomic.Int32
	http.DefaultTransport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != modelsPath {
			return nil, errors.New("unexpected request")
		}
		if commandReads.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return scanResponse(http.StatusOK, `{"data":[{"id":"old-command"}]}`), nil
	})
	runner := catalog.ProviderSyncRunner{DB: db, Ent: ent.NewClient(), Decrypt: s.crypto.DecryptCredential}
	ran := make(chan error, 1)
	go func() {
		_, err := runner.SyncActiveProviders(ctx)
		ran <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	execCatalogTestSQL(t, ctx, db, `UPDATE beichen_datasets SET lease_until=clock_timestamp()-interval '1 second'
WHERE credential_id=$1 AND kind='billing'`, job.credential)
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != modelsPath {
			return nil, errors.New("unexpected request")
		}
		return scanResponse(http.StatusOK, `{"data":[{"id":"new-scheduled"}]}`), nil
	})
	job.kind = "billing"
	if busy, _ := s.runJob(ctx, job, false); busy {
		t.Fatal("scheduled round should take over expired lease")
	}
	if got := readScanModels(t, ctx, db, job.credential); got != "new-scheduled" {
		t.Errorf("scheduled scan did not write its snapshot: %s", got)
	}
	close(release)
	select {
	case err := <-ran:
		if err == nil {
			t.Error("expired command returned success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got := readScanModels(t, ctx, db, job.credential); got != "new-scheduled" {
		t.Errorf("expired command replaced the scheduled snapshot: %s", got)
	}

	execCatalogTestSQL(t, ctx, db, `UPDATE beichen_datasets SET lease_id=gen_random_uuid(),lease_until=clock_timestamp()+interval '30 seconds'
WHERE credential_id=$1 AND kind='billing'`, job.credential)
	if count, err := runner.SyncActiveProviders(ctx); err != nil || count != 0 || commandReads.Load() != 1 {
		t.Errorf("command competed with scheduled lease: count=%d err=%v reads=%d", count, err, commandReads.Load())
	}
}

func TestBeichenScanRobustnessWebsiteCoveragePostgres(t *testing.T) {
	ctx, db, s, job := newKeyScanFixture(t)
	const openaiSection = `{"id":1,"name":"channel-1","platforms":[{"platform":"openai","groups":[{"id":11,"name":"group-11"}],"models":["model-a","model-b"]}]}`
	const claudeSection = `{"id":2,"name":"channel-2","platforms":[{"platform":"anthropic","groups":[{"id":12,"name":"group-12"}],"models":["model-c"]}]}`
	full := "[" + openaiSection + "," + claudeSection + "]"
	body, status := full, http.StatusOK
	if err := s.ConfigureWebSessionTokens("scan@example.test", "website-scan-session", "website-scan-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	s.now = func() time.Time { return at }
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != panelURL {
			return nil, errors.New("unexpected request")
		}
		return scanResponse(status, body), nil
	})
	run := func(want int, stale bool) WebsiteDirectoryStatus {
		t.Helper()
		_, err := s.syncWebsiteDirectory(ctx, job.provider, true)
		if (err != nil) != stale {
			t.Errorf("website scan: %v, stale=%v", err, stale)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		view, platforms, err := readWebsiteDirectory(ctx, tx, job.provider)
		if err != nil || len(platforms) != want {
			t.Errorf("website snapshot has %d platforms, want %d: %v", len(platforms), want, err)
		}
		return view
	}
	first := run(2, false)
	at = at.Add(time.Minute)
	body = "[" + openaiSection + "]"
	pending := run(2, true)
	if first.LastSuccess == nil || pending.LastSuccess == nil || !pending.LastSuccess.Equal(*first.LastSuccess) ||
		pending.LastRun == nil || len(pending.LastRun.Failures) == 0 || pending.LastRun.Failures[0].Platform != "anthropic" {
		t.Errorf("platform omission was not recorded: %+v", pending)
	}
	encoded, _ := json.Marshal(pending)
	if !strings.Contains(string(encoded), `"retained":true`) || !strings.Contains(string(encoded), `"observed_at":"`+at.Add(-time.Minute).Format(time.RFC3339Nano)) {
		t.Errorf("retained group has no original observation time: %s", encoded)
	}
	status = http.StatusBadGateway
	run(2, true)
	status = http.StatusOK
	run(2, true)
	run(2, true)
	run(1, false)
	body = "[" + strings.Replace(openaiSection, `"model-a","model-b"`, `"model-a"`, 1) + "]"
	run(1, true)
}

func scanAccountKeysPage(page, total int) string {
	items := make([]string, 0, 100)
	for id := (page-1)*100 + 1; id <= min(page*100, total); id++ {
		items = append(items, fmt.Sprintf(`{"id":%d,"user_id":7,"name":"key-%d","key":"sk-fixture-item-%04d"}`, id, id, id))
	}
	return fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"page":%d,"page_size":100,"pages":%d}}`,
		strings.Join(items, ","), total, page, max(1, (total+99)/100))
}

func TestBeichenScanRobustnessKeyInventoryPostgres(t *testing.T) {
	ctx, _, s, job := newKeyScanFixture(t)
	s.ConfigurePanelFallback("website-scan-fixture")
	firstPageReads, secondPageReads := 0, 0
	reject := false
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/keys" {
			return nil, errors.New("unexpected request")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch page {
		case 1:
			firstPageReads++
			return scanResponse(http.StatusOK, scanAccountKeysPage(1, 101)), nil
		case 2:
			secondPageReads++
			if reject {
				return scanResponse(http.StatusBadGateway, ""), nil
			}
			if firstPageReads == 1 {
				return scanResponse(http.StatusOK, scanAccountKeysPage(2, 102)), nil
			}
			return scanResponse(http.StatusOK, scanAccountKeysPage(2, 101)), nil
		default:
			return nil, errors.New("unexpected page")
		}
	})
	initial, err := s.RefreshWebsiteKeys(ctx, job.provider)
	if err != nil || initial.Status != "current" || initial.LastSeen == nil || len(initial.Keys) != 101 ||
		firstPageReads != 2 || secondPageReads != 2 {
		t.Errorf("failed to re-read the unstable inventory: status=%s keys=%d pages=%d,%d err=%v",
			initial.Status, len(initial.Keys), firstPageReads, secondPageReads, err)
	}
	reject = true
	failed, err := s.RefreshWebsiteKeys(ctx, job.provider)
	raw, _ := json.Marshal(failed)
	if err != nil || failed.Status != "unavailable" || failed.LastSeen == nil || initial.LastSeen == nil ||
		!failed.LastSeen.Equal(*initial.LastSeen) || len(failed.Keys) != 101 ||
		!strings.Contains(string(raw), `"failed_page":2`) || !strings.Contains(string(raw), "HTTP 502") {
		t.Errorf("failed page or last success is missing: status=%s keys=%d err=%v", failed.Status, len(failed.Keys), err)
	}
	if firstPageReads != 4 || secondPageReads != 4 {
		t.Errorf("failed page did not get a bounded whole-round retry: %d,%d", firstPageReads, secondPageReads)
	}
}

func TestBeichenScanRobustnessUsagePageStatusPostgres(t *testing.T) {
	ctx, db, s, job := newKeyScanFixture(t)
	const day = "2026-11-21"
	at := time.Date(2026, 11, 21, 14, 0, 0, 0, beijing)
	s.now = func() time.Time { return at }
	s.ConfigurePanelFallback("panel-fixture-page-status")
	keys := strings.ReplaceAll(websiteFixture(t, "cc2-website-keys"), "sk-fixture-cc2", "sk-fixture-scan-key")
	channels := websiteFixture(t, "cc2-website-channels")
	usage := websiteFixture(t, "cc2-website-usage")
	var envelope struct {
		Data struct{ Items []map[string]any }
	}
	if err := json.Unmarshal([]byte(usage), &envelope); err != nil || len(envelope.Data.Items) == 0 {
		t.Fatal("bad usage fixture", err)
	}
	item := envelope.Data.Items[0]
	items := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		item["id"] = i
		row, _ := json.Marshal(item)
		items = append(items, string(row))
	}
	phase := 0
	padding := strings.Repeat("x", 1700<<10)
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/keys":
			return scanResponse(http.StatusOK, keys), nil
		case "/api/v1/channels/available":
			return scanResponse(http.StatusOK, channels), nil
		case "/api/v1/groups/rates":
			return scanResponse(http.StatusOK, `{"code":0,"data":{"301":0.30,"602":0.60}}`), nil
		case "/api/v1/usage":
			if r.URL.Query().Get("start_date") != day {
				return scanResponse(http.StatusOK, `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"pages":0}}`), nil
			}
			if phase == 0 {
				return scanResponse(http.StatusOK, usage), nil
			}
			if phase == 4 {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				rows := make([]string, 0, 100)
				for id := (page-1)*100 + 1; id <= page*100; id++ {
					item["id"] = id
					row, _ := json.Marshal(item)
					rows = append(rows, string(row))
				}
				body := fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":1000,"page":%d,"page_size":100,"pages":10},"padding":%q}`,
					strings.Join(rows, ","), page, padding)
				return scanResponse(http.StatusOK, body), nil
			}
			if r.URL.Query().Get("page") == "1" {
				total, pages := 101, 2
				if phase == 3 {
					total, pages = 10001, 101
				}
				body := fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"page":1,"page_size":100,"pages":%d}}`,
					strings.Join(items, ","), total, pages)
				return scanResponse(http.StatusOK, body), nil
			}
			if phase == 2 {
				resp := scanResponse(http.StatusTooManyRequests, "")
				resp.Header.Set("Retry-After", "30")
				return resp, nil
			}
			body := fmt.Sprintf(`{"code":0,"data":{"items":[%s,%s],"total":102,"page":2,"page_size":100,"pages":2}}`,
				items[0], items[1])
			return scanResponse(http.StatusOK, body), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})
	run := func() error { return s.RefreshKeyUsage(ctx, job.provider, job.credential, KeyUsageTarget{}) }
	if err := run(); err != nil {
		t.Fatal(err)
	}
	old := at.UTC()
	for phase = 1; phase <= 4; phase++ {
		at = at.Add(time.Minute)
		if err := run(); err == nil {
			t.Errorf("incomplete usage was accepted for phase %d", phase)
		}
		var dataset, observation string
		var saved time.Time
		if err := db.QueryRowContext(ctx, `
SELECT d.last_error,o.usage_error,u.fetched_at
FROM beichen_datasets d JOIN beichen_key_observations o USING (credential_id)
JOIN beichen_key_usage_days u USING (credential_id,key_digest)
WHERE d.credential_id=$1 AND d.kind='key_usage' AND u.day=$2::date`, job.credential, day).
			Scan(&dataset, &observation, &saved); err != nil {
			t.Fatal(err)
		}
		page := "第 2 页"
		if phase == 3 {
			page = "第 1 页"
		}
		if phase == 4 {
			page = "第 10 页"
		}
		for _, message := range []string{dataset, observation} {
			if !strings.Contains(message, day) || !strings.Contains(message, page) ||
				!strings.Contains(message, old.Format(time.RFC3339)) || !saved.Equal(old) {
				t.Errorf("phase %d missing page or prior evidence date: %s (saved %v)", phase, message, saved)
			}
		}
	}
}

func TestBeichenScanRobustnessBillingMissingFieldsPostgres(t *testing.T) {
	ctx, db, s, job := newKeyScanFixture(t)
	job.kind = "billing"
	s.ConfigurePanelFallback("fixture-billing-coverage-session")
	execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_datasets(credential_id,kind,last_success)
VALUES($1,'prices','2026-11-21 12:00:00Z')`, job.credential)
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/api/v1/keys":
			body = websiteKeyListFixture("sk-fixture-scan-key", "Gemini", 42, 16)
		case "/api/v1/channels/available":
			body = `{"code":0,"data":[{"platforms":[{"platform":"openai","groups":[{"id":11}],"models":["another-model"]}]}]}`
		case "/api/v1/groups/rates":
			body = `{"code":0,"data":{}}`
		case billingPath:
			body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
	"effective_rate_multiplier":0.6,"peak_rate_enabled":false,"observed_at":%q}`,
				time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano))
		case modelsPath:
			body = `{"data":[{"id":"key-model"}]}`
		default:
			return nil, errors.New("unexpected fixture path")
		}
		return scanResponse(http.StatusOK, body), nil
	})
	if busy, err := s.runJob(ctx, job, true); busy || err != nil {
		t.Fatal("independent website identity did not corroborate the Key multiplier", err)
	}
	var message, missing string
	var oldPrice time.Time
	if err := db.QueryRowContext(ctx, `
SELECT p.last_error,p.last_success,b.website_group_observation->>'key_billing_missing_fields'
FROM beichen_datasets p JOIN beichen_datasets b USING (credential_id)
WHERE p.credential_id=$1 AND p.kind='prices' AND b.kind='billing'`, job.credential).Scan(&message, &oldPrice, &missing); err != nil {
		t.Fatal(err)
	}
	if !oldPrice.Equal(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)) ||
		message != errPanelEmpty.Error() || !strings.Contains(missing, "key_id") ||
		!strings.Contains(missing, "group_id") || !strings.Contains(missing, "pricing") {
		t.Errorf("incomplete Key response recorded as a full price observation: %s at %v, missing %s", message, oldPrice, missing)
	}
}

func TestBeichenScanRobustnessKeyIdentityReasons(t *testing.T) {
	const plain = "sk-fixture-masked-key-1234"
	for _, tc := range []struct {
		name string
		keys []listedKey
		want string
	}{
		{"insufficient", []listedKey{{ID: 1, Name: "G3", Masked: "sk-****34", GroupID: 10}}, "遮罩"},
		{"tied", []listedKey{
			{ID: 1, Name: "G3", Masked: "sk-fix****1234", GroupID: 10},
			{ID: 2, Name: "G3", Masked: "sk-fix****1234", GroupID: 11},
		}, "同等"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := identifyChannel(plain, "G3", "", tc.keys); !errors.Is(err, errKeyMatch) ||
				!strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), plain) {
				t.Fatalf("unhelpful identity failure: %v", err)
			}
		})
	}
}

func TestBeichenScanRobustnessCrawlGroupConflict(t *testing.T) {
	s := New(nil, nil)
	const secret, bearer = "sk-fixture-conflicting-group", "fixture-scan-group-session"
	channels := `[{"id":1,"platforms":[{"platform":"openai","models":["m1"],"groups":[{"id":12}]}]},
	{"id":2,"platforms":[{"platform":"anthropic","models":["m2"],"groups":[{"id":12}]}]}]`
	keys := `{"code":0,"data":{"items":[{"id":5,"user_id":7,"key":"` + secret + `","group_id":12,"group":{"id":12,"name":"fixture","rate_multiplier":1}}],
	"total":1,"page":1,"page_size":100,"pages":1}}`
	mockCrawlKeyEndpoints(t, s, bearer, &channels, &keys, map[string]crawlModelFixture{
		"Bearer " + secret: {http.StatusOK, `{"data":[{"id":"m1"}]}`},
	})
	report, err := s.CrawlDirectory(context.Background(), CrawlOptions{PanelToken: bearer, IncludeKeys: true})
	if report.Keys == nil || len(*report.Keys) != 1 {
		t.Fatal("missing Key report", err)
	}
	key := (*report.Keys)[0]
	if report.Complete || err == nil || key.GroupCheck != "unverified" || key.OnlyGroup != nil || key.OnlyKey != nil {
		t.Fatalf("duplicate group was treated as a verified difference: %+v err=%v", key, err)
	}
}
