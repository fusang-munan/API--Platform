package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func TestManualProbeRefreshesRecentFailureOnItsCatalogKeyPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "6")
	manager, err := security.NewManager([]byte(strings.Repeat("j", 32)), []byte(strings.Repeat("a", 32)),
		[]byte(strings.Repeat("k", 32)), []byte(strings.Repeat("e", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const fixtureKey = "gm1-probe-test-only"
	gm1, g3 := keys[1], keys[2]
	entries := []catalog.ProviderEntry{
		{ID: "deepseek-v4-pro", SupportedProtocols: []string{"openai_compatible"}},
		{ID: "deepseek-v4.1-flash", SupportedProtocols: []string{"openai_compatible"}},
	}
	store := catalog.ProviderCatalogSyncer{DB: db}
	for key, secret := range map[uuid.UUID]string{gm1: fixtureKey, g3: "g3-probe-test-only"} {
		cipher, err := manager.EncryptCredential(secret)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE provider_credentials SET credential_ciphertext = $1 WHERE id = $2", cipher, key)
		if _, err := store.UpsertCredentialEntries(ctx, provider, key, entries, time.Now().UTC(), nil); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE provider_model_catalog SET catalog_source='website' WHERE provider_id=$1 AND credential_id=$2", provider, g3)
	exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
SELECT provider_id, credential_id, model_id::text, 'openai_compatible', 400, 'http_error', 0, statement_timestamp()
FROM provider_model_catalog WHERE provider_id=$1 AND credential_id=$2`, provider, gm1)

	s := New(db, manager)
	previous := func() ProbesView {
		t.Helper()
		view, err := s.LatestProbes(ctx, provider)
		if err != nil || len(view.Results) != len(entries) {
			t.Fatalf("expected two GM1 probe results: %+v, %v", view, err)
		}
		return view
	}
	before := previous()
	if before.Run.Budget.Used != 0 || before.Results[0].StatusCode != 400 {
		t.Fatalf("legacy latest rows must not fabricate usage history: %+v", before)
	}
	auto, err := s.claimProbeJob(ctx, true)
	if err != nil || auto.force {
		t.Fatalf("automatic claim = %+v, %v", auto, err)
	}
	if targets, hasTargets, err := s.listProbeTargets(ctx, auto); err != nil || !hasTargets || len(targets) != 0 {
		t.Fatalf("automatic sweep should skip the recent failures: %d %t %v", len(targets), hasTargets, err)
	}
	if run, err := s.RequestProbes(ctx, provider); err != nil || run.State != "running" {
		t.Fatalf("manual request should queue during an automatic sweep: %+v %v", run, err)
	}
	s.runProbeJob(ctx, auto)

	status := http.StatusOK
	var requestsMu sync.Mutex
	requests := 0
	s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer "+fixtureKey {
			t.Error("DeepSeek probe used a key outside the GM1 catalog")
		}
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		body := `{"error":{"type":"bad_request"}}`
		if status == http.StatusOK {
			body = `{"choices":[{}],"usage":{"prompt_tokens":20,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0}}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	checkManual := func() ProbesView {
		t.Helper()
		job, err := s.claimProbeJob(ctx, false)
		if err != nil || !job.force {
			t.Fatalf("manual claim = %+v, %v", job, err)
		}
		targets, _, err := s.listProbeTargets(ctx, job)
		if err != nil || len(targets) != len(entries) {
			t.Fatalf("manual sweep should select both DeepSeek models: %d %v", len(targets), err)
		}
		for _, target := range targets {
			if target.credential != gm1 {
				t.Fatalf("unexpected credential for %s: %s", target.model, target.credential)
			}
		}
		s.runProbeJob(ctx, job)
		view := previous()
		if view.Run.State != "completed" {
			t.Fatalf("manual sweep did not complete: %+v", view.Run)
		}
		return view
	}
	after := checkManual()
	passes := map[string]time.Time{}
	for _, result := range after.Results {
		if result.CredentialID != gm1 || result.StatusCode != 200 || result.LastPassedAt == nil ||
			!result.LastPassedAt.Equal(result.CheckedAt) || !result.CheckedAt.After(before.Results[0].CheckedAt) {
			t.Fatalf("GET probes did not return the new success: %+v", result)
		}
		passes[result.Model] = result.CheckedAt
	}
	requestManual := func() {
		t.Helper()
		if run, err := s.RequestProbes(ctx, provider); err != nil || run.State != "queued" {
			t.Fatalf("manual request = %+v %v", run, err)
		}
	}
	status = http.StatusBadRequest
	requestManual()
	for _, result := range checkManual().Results {
		if result.StatusCode != 400 || result.LastPassedAt != nil || !result.CheckedAt.After(passes[result.Model]) {
			t.Fatalf("definitive HTTP feedback should invalidate sticky availability: %+v", result)
		}
	}
	exec("UPDATE provider_credentials SET updated_at = clock_timestamp() WHERE id = $1", gm1)
	exec("UPDATE provider_model_catalog SET last_seen_at = clock_timestamp() WHERE credential_id = $1", gm1)
	requestManual()
	for _, result := range checkManual().Results {
		if result.StatusCode != 400 || result.LastPassedAt != nil {
			t.Fatalf("failure after key rotation inherited old success: %+v", result)
		}
	}
	requestManual()
	if final := checkManual(); !final.Run.Budget.Exhausted || final.Run.Budget.Used != 6 {
		t.Fatalf("forced sweep exceeded daily limit: %+v", final.Run.Budget)
	}
	day := time.Now().In(beijing).Format(dayLayout)
	totals, err := s.ProbeUsageForDay(ctx, provider, day)
	if err != nil || totals.Attempts != 6 || totals.Input.Reported != 2 || totals.Input.Tokens == nil || *totals.Input.Tokens != 40 ||
		totals.Output.Tokens == nil || *totals.Output.Tokens != 2 || totals.CacheRead.Tokens == nil || *totals.CacheRead.Tokens != 0 || totals.CacheWrite.Tokens != nil {
		t.Fatalf("unexpected probe-only summary: %+v %v", totals, err)
	}
	var failures int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM beichen_probe_usage WHERE credential_id=$1 AND job_id IS NOT NULL
		AND status_code=400 AND error_code='http_400' AND input_tokens IS NULL AND output_tokens IS NULL
		AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL`, gm1).Scan(&failures); err != nil || failures != 4 {
		t.Fatalf("failure ledger rows %d, err %v", failures, err)
	}
	if requests != 6 {
		t.Fatalf("manual probe attempts = %d, want 6", requests)
	}
}

func TestBeichenProbeAndCatalogPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	manager, err := security.NewManager([]byte(strings.Repeat("j", 32)), []byte(strings.Repeat("a", 32)),
		[]byte(strings.Repeat("k", 32)), []byte(strings.Repeat("e", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, second := keys[0], keys[1]
	const firstSecret, secondSecret = "fixture-probe-one", "fixture-probe-two"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	cipherA, err := manager.EncryptCredential(firstSecret)
	if err != nil {
		t.Fatal("encrypt first fixture")
	}
	cipherB, err := manager.EncryptCredential(secondSecret)
	if err != nil {
		t.Fatal("encrypt second fixture")
	}
	exec("UPDATE provider_credentials SET credential_ciphertext = $1 WHERE id = $2", cipherA, first)
	exec("UPDATE provider_credentials SET credential_ciphertext = $1 WHERE id = $2", cipherB, second)

	store := catalog.ProviderCatalogSyncer{DB: db}
	listA := []catalog.ProviderEntry{
		{ID: "shared", SupportedProtocols: []string{"openai_responses", "openai_compatible"}},
		{ID: "key-only", SupportedProtocols: []string{"openai_compatible"}},
	}
	if _, err := store.UpsertCredentialEntries(ctx, provider, first, listA, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertCredentialEntries(ctx, provider, second, []catalog.ProviderEntry{
		{ID: "shared", SupportedProtocols: []string{"anthropic", "google_gemini_v1beta"}},
		{ID: "gpt-image-2", SupportedProtocols: []string{"openai_images"}},
	}, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	// A catalog row from before 0045 still has both inferred protocols.
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'legacy',now(),now(),now(),now())`, provider, first)
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'inactive-key-model',now(),now(),now(),now())`, provider, keys[2])
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", keys[2])

	s := New(db, manager)
	seen := make(map[string]int)
	var mutex sync.Mutex
	s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		var body map[string]json.RawMessage
		if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "api.beichenapi.com" ||
			req.URL.RawQuery != "" || json.NewDecoder(req.Body).Decode(&body) != nil {
			return nil, errors.New("invalid fake probe request")
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		var maxTokens json.RawMessage
		status, response := http.StatusOK, ""
		switch req.URL.Path {
		case "/v1/chat/completions":
			maxTokens = body["max_tokens"]
			response = `{"choices":[{}]}`
			if model == "key-only" {
				status, response = http.StatusForbidden, firstSecret
			}
		case "/v1/responses":
			maxTokens = body["max_output_tokens"]
			response = `{"status":"incomplete","output":[]}`
			if model == "legacy" {
				response = `{"error":{"message":"` + firstSecret + `"},"output":[]}`
			}
		case "/v1/messages":
			if req.Header.Get("x-api-key") != secondSecret || req.Header.Get("Authorization") != "" {
				return nil, errors.New("incorrect Anthropic auth")
			}
			maxTokens, response = body["max_tokens"], `{"type":"message","content":[]}`
		case "/v1beta/models/shared:generateContent":
			if req.Header.Get("x-goog-api-key") != secondSecret || req.Header.Get("Authorization") != "" {
				return nil, errors.New("incorrect Gemini auth")
			}
			var generation map[string]json.RawMessage
			_ = json.Unmarshal(body["generationConfig"], &generation)
			model, maxTokens, response = "shared", generation["maxOutputTokens"],
				`{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"MAX_TOKENS"}]}`
		default:
			return nil, errors.New("unregistered protocol requested")
		}
		if string(maxTokens) != "1" ||
			(strings.HasPrefix(req.URL.Path, "/v1/chat/") || req.URL.Path == "/v1/responses") &&
				req.Header.Get("Authorization") != "Bearer "+firstSecret {
			return nil, errors.New("invalid output cap or auth")
		}
		mutex.Lock()
		seen[req.URL.Path+"/"+model]++
		mutex.Unlock()
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response))}, nil
	})

	recent, err := s.LatestProbes(ctx, provider)
	if err != nil || recent.Run.State != "idle" || len(recent.Results) != 0 {
		t.Fatalf("initial probe state: %v, %v", recent.Run.State, err)
	}
	if _, err := s.claimProbeJob(ctx, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("automatic probes should be disabled: %v", err)
	}
	for range 2 {
		run, err := s.RequestProbes(ctx, provider)
		if err != nil || run.State != "queued" {
			t.Fatalf("manual probe not queued: %v, %v", run.State, err)
		}
	}
	job, err := s.claimProbeJob(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimProbeJob(ctx, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second worker claimed running probe: %v", err)
	}
	if code, err := s.executeProbeJob(ctx, job); code != "" || err != nil {
		t.Fatalf("probe execution: %s, %v", code, err)
	}
	exec("UPDATE beichen_probe_jobs SET lease_until = now()-interval '1 second' WHERE provider_id=$1", provider)
	resumed, err := s.claimProbeJob(ctx, false)
	if err != nil || !resumed.startedAt.Equal(job.startedAt) || resumed.id != job.id || resumed.lease == job.lease {
		t.Fatalf("expired probe did not resume: %v", err)
	}
	s.runProbeJob(ctx, resumed)

	recent, err = s.LatestProbes(ctx, provider)
	if err != nil || recent.Run.State != "completed" || len(recent.Results) != 7 || len(seen) != 7 {
		t.Fatalf("probe results = %d, attempts = %d, state = %v, err = %v", len(recent.Results), len(seen), recent.Run.State, err)
	}
	for _, attempts := range seen {
		if attempts != 1 {
			t.Fatal("a resumed sweep replayed a stored result")
		}
	}
	var failures int
	for _, result := range recent.Results {
		if result.ErrorCode != nil {
			failures++
			if *result.ErrorCode != "invalid_response" && *result.ErrorCode != "credential_invalid" {
				t.Fatal("unexpected probe failure")
			}
		}
	}
	public, err := json.Marshal(recent)
	if failures != 2 || err != nil || strings.Contains(string(public), firstSecret) || strings.Contains(string(public), secondSecret) {
		t.Fatal("incorrect failures or leaked upstream data")
	}
	var protocols string
	if err := db.QueryRowContext(ctx, "SELECT to_json(supported_protocols)::text FROM provider_model_catalog WHERE credential_id=$1 AND model_id='legacy'", first).
		Scan(&protocols); err != nil || protocols != `["openai_responses","openai_compatible"]` {
		t.Fatalf("legacy probes must write the complete protocol list: %s %v", protocols, err)
	}
	if _, err := s.claimProbeJob(ctx, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("early automatic probe scheduled: %v", err)
	}
	exec("UPDATE beichen_probe_jobs SET finished_at = now()-interval '61 minutes' WHERE provider_id=$1", provider)
	hourly, err := s.claimProbeJob(ctx, true)
	if err != nil || !hourly.startedAt.After(resumed.startedAt) {
		t.Fatalf("automatic probe did not become due: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, credential := range []uuid.UUID{first, second} {
		if err := recordSuccess(ctx, tx, credential, "catalog"); err != nil {
			t.Fatal(err)
		}
	}
	var revision time.Time
	if err := tx.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id = $1", first).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := saveWebsiteCatalog(ctx, tx, first, revision, panelQuote{groupID: 7, websiteModels: []string{"Shared", "legacy", "web-only"}, err: errPanelEmpty}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	overview := func() Overview {
		t.Helper()
		view, err := s.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	view := overview()
	diffCounts := 0
	for _, difference := range view.CatalogDifferences {
		if difference.State == "compared" {
			diffCounts++
			if difference.CredentialID != first || !reflect.DeepEqual(difference.WebsiteOnly, []string{"web-only"}) ||
				!reflect.DeepEqual(difference.KeyOnly, []string{"key-only"}) {
				t.Fatal("inconsistent directory comparison")
			}
		}
	}
	if diffCounts != 1 {
		t.Fatalf("expected one matched channel, got %d", diffCounts)
	}
	for _, model := range view.ModelCatalog {
		switch model.Model {
		case "shared":
			if model.ProbeState != "passed" || model.LastProbeAt == nil || model.ReadyCount != 0 || len(model.Protocols) != 4 {
				t.Fatal("successful probe was not kept distinct from publication")
			}
		case "key-only":
			if model.ProbeState != "failed" || model.LastProbeAt == nil {
				t.Fatal("missing failed model probe")
			}
		case "gpt-image-2":
			if model.ProbeState != "unprobed" || model.LastProbeAt != nil {
				t.Fatal("image model was probed")
			}
		}
	}
	for _, credential := range view.Credentials {
		if credential.CredentialID == first && (credential.Health.AvailableDimensions != 2 ||
			credential.Health.Catalog.State != "available" || credential.Health.Probe.State != "unavailable") {
			t.Fatalf("unexpected four-dimensional health: %+v", credential.Health)
		}
	}

	if _, err := store.UpsertCredentialEntries(ctx, provider, second, []catalog.ProviderEntry{
		{ID: "shared", SupportedProtocols: []string{"anthropic"}},
		{ID: "gpt-image-2", SupportedProtocols: []string{"openai_images"}},
	}, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT to_json(supported_protocols)::text FROM provider_model_catalog WHERE credential_id=$1 AND model_id='shared'", second).
		Scan(&protocols); err != nil || protocols != `["anthropic","google_gemini_v1beta"]` {
		t.Fatalf("refresh dropped a protocol confirmed by the current key: %s %v", protocols, err)
	}

	exec("UPDATE provider_credentials SET updated_at = clock_timestamp() WHERE id = $1", first)
	recent, err = s.LatestProbes(ctx, provider)
	if err != nil || len(recent.Results) != 2 {
		t.Fatalf("rotated key still exposed previous evidence: %d %v", len(recent.Results), err)
	}
	if _, err := s.LatestProbes(context.Background(), keys[3]); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatal("unexpected provider scope")
	}
}

func TestBeichenProbeFailureAttributionPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "500")
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential("fixture-attribution")
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$1 WHERE provider_id=$2", ciphertext, provider)
	for i, name := range []string{"recover-429", "denied-403", "missing-404", "temporary-503", "invalid-401"} {
		key := keys[i%len(keys)]
		saveCatalogFixture(t, ctx, db, provider, key, []catalog.ProviderEntry{
			{ID: name, SupportedProtocols: []string{"openai_compatible"}},
		}, nil)
		// The 429 recovery and the 401 both use the first Key.
		if i == len(keys) {
			saveCatalogFixture(t, ctx, db, provider, key, []catalog.ProviderEntry{
				{ID: "recover-429", SupportedProtocols: []string{"openai_compatible"}},
				{ID: name, SupportedProtocols: []string{"openai_compatible"}},
			}, nil)
		}
		execCatalogTestSQL(t, ctx, db, `WITH school AS (
 INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
 VALUES(gen_random_uuid(),$3::text,$3::text,'active',1000,now(),now()) RETURNING id)
INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,$3,'openai_compatible',7,now(),now() FROM school`, provider, key, name)
		execCatalogTestSQL(t, ctx, db, `INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at,last_passed_at)
VALUES($1,$2,$3,'openai_compatible',200,1,statement_timestamp(),statement_timestamp())`, provider, key, name)
	}

	service := New(db, manager)
	var mu sync.Mutex
	calls := map[string]int{}
	groupRecovered := false
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Path == modelsPath {
			return scanResponse(http.StatusOK, `{"data":[{"id":"denied-403","supported_protocols":["openai_compatible"]}],"total":1}`), nil
		}
		var payload struct{ Model string }
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		calls[payload.Model]++
		switch payload.Model {
		case "recover-429":
			if calls[payload.Model] == 1 {
				return scanResponse(http.StatusTooManyRequests, "{}"), nil
			}
		case "denied-403":
			if !groupRecovered {
				return scanResponse(http.StatusForbidden, `{"error":{"reason":"GROUP_NOT_ALLOWED","message":"fixture private detail"}}`), nil
			}
		case "missing-404":
			return scanResponse(http.StatusNotFound, "{}"), nil
		case "temporary-503":
			return scanResponse(http.StatusServiceUnavailable, "{}"), nil
		case "invalid-401":
			return scanResponse(http.StatusUnauthorized, "{}"), nil
		default:
			return nil, errors.New("unexpected fixture model")
		}
		return scanResponse(http.StatusOK, `{"choices":[{}]}`), nil
	})
	if _, err := service.RequestProbes(ctx, provider); err != nil {
		t.Fatal(err)
	}
	job, err := service.claimProbeJob(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	targets, _, err := service.listProbeTargets(ctx, job)
	if err != nil || len(targets) != 5 {
		t.Fatalf("targets=%d error=%v", len(targets), err)
	}
	round := func(ctx context.Context, batch []probeTarget) ([]probeObservation, error) {
		return service.runProbeRound(ctx, job, batch)
	}
	observations, err := round(ctx, targets)
	if err != nil {
		t.Fatal(err)
	}
	if err := retryFailedProbes(ctx, observations, 0, round); err != nil {
		t.Fatal(err)
	}
	if err := service.saveProbeBatch(ctx, job, observations); err != nil {
		t.Fatal(err)
	}
	recent, err := service.LatestProbes(ctx, provider)
	if err != nil || len(recent.Results) != 5 {
		t.Fatalf("probe report: %+v error=%v", recent, err)
	}
	wantCodes := map[string]string{
		"recover-429": "", "denied-403": "GROUP_NOT_ALLOWED", "missing-404": "endpoint_unsupported",
		"temporary-503": "upstream_error", "invalid-401": "credential_invalid",
	}
	for _, result := range recent.Results {
		var code string
		if result.ErrorCode != nil {
			code = *result.ErrorCode
		}
		if code != wantCodes[result.Model] {
			t.Fatalf("%s attribution = %q, want %q", result.Model, code, wantCodes[result.Model])
		}
	}
	if calls["recover-429"] != 2 || calls["temporary-503"] != 3 ||
		calls["denied-403"] != 1 || calls["missing-404"] != 1 || calls["invalid-401"] != 1 {
		t.Fatalf("retries did not match the error classes: %v", calls)
	}
	state := func() map[string]catalog.ModelCapability {
		t.Helper()
		view, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
		if err != nil {
			t.Fatal(err)
		}
		results := map[string]catalog.ModelCapability{}
		for _, row := range view.Capabilities {
			if row.CredentialID != nil {
				results[row.Model] = row
			}
		}
		return results
	}
	current := state()
	if current["recover-429"].Availability != "available" || current["temporary-503"].Availability != "degraded" ||
		current["missing-404"].Availability != "maintenance" || current["invalid-401"].Availability != "maintenance" ||
		current["denied-403"].CatalogState != "group_blocked" || current["denied-403"].BindingState != "invalid" {
		t.Fatal("availability did not distinguish the recovered, temporary, and definitive results")
	}
	overview, err := service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	wantReason := map[string]string{"missing-404": "上游未提供该协议端点", "temporary-503": "HTTP 503", "invalid-401": "未通过上游认证"}
	for _, row := range overview.ModelCatalog {
		if reason := wantReason[row.Model]; reason != "" && !strings.Contains(row.ModelAvailability.AvailabilityReason, reason) {
			t.Fatalf("%s missing specific availability explanation: %s", row.Model, row.ModelAvailability.AvailabilityReason)
		}
	}
	if err := service.RescanCatalog(ctx, provider, keys[1]); err != nil || state()["denied-403"].CatalogState != "group_blocked" {
		t.Fatalf("a catalog read should not clear its generation probe's group denial: %v", err)
	}
	mu.Lock()
	groupRecovered = true
	mu.Unlock()
	for _, target := range targets {
		if target.model == "denied-403" {
			observations, err = round(ctx, []probeTarget{target})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.saveProbeBatch(ctx, job, observations); err != nil {
				t.Fatal(err)
			}
		}
	}
	if row := state()["denied-403"]; row.Availability != "available" || row.BindingState != "ready" {
		t.Fatal("a passing generation probe should clear its group denial")
	}
}

func TestBindingRouteBlocksConfirmedBeichenFailuresPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential("fixture-route-resolution")
	if err != nil {
		t.Fatal(err)
	}
	const upstream = "routed-model"
	key, alternate := keys[1], keys[0] // A legacy binding sorts this Key first.
	for _, id := range []uuid.UUID{key, alternate} {
		execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", id, ciphertext)
		saveCatalogFixture(t, ctx, db, provider, id, []catalog.ProviderEntry{
			{ID: upstream, SupportedProtocols: []string{"openai_responses", "openai_compatible"}},
		}, nil)
	}
	binding := ent.ModelBinding{ProviderID: provider, CredentialID: &key, UpstreamModelName: " Routed-Model ",
		Adapter: modelbinding.AdapterOpenaiResponses, Status: modelbinding.StatusActive}
	const school = "school-routed"
	modelID := uuid.New()
	execCatalogTestSQL(t, ctx, db, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2::text,$2::text,'active',1000,now(),now())`, modelID, school)
	execCatalogTestSQL(t, ctx, db, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,'openai_responses',7,now(),now())`, modelID, provider, key, binding.UpstreamModelName)
	var project uuid.UUID
	if err := db.QueryRowContext(ctx, `
WITH org AS (INSERT INTO organizations(id,name,created_at,updated_at)
  VALUES(gen_random_uuid(),'route fixture',now(),now()) RETURNING id)
INSERT INTO projects(id,organization_id,name,created_at,updated_at)
SELECT gen_random_uuid(),id,'fixture project',now(),now() FROM org RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	const studentKey = "fixture-route-student-key"
	execCatalogTestSQL(t, ctx, db, `
WITH issued AS (INSERT INTO api_keys(id,owner_user_id,project_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
  VALUES(gen_random_uuid(),$1,$2,'fixture','active',$3,'fixture',now(),now(),now()) RETURNING id)
INSERT INTO api_key_models(id,api_key_id,model_id,created_at) SELECT gen_random_uuid(),id,$4,now() FROM issued`,
		actor, project, manager.HashAPIKey(studentKey), modelID)
	// Denied routes stop before reserving usage or opening an upstream connection.
	gateway := dataplane.NewGateway(client, nil, nil, manager, dataplane.Config{MaxRequestBytes: 4096})
	checkRequestBlocked := func() {
		t.Helper()
		body := strings.NewReader(`{"model":"` + school + `","input":"fixture"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+studentKey)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "model_unavailable") {
			t.Fatalf("gateway should reject the authorized model's blocked route: HTTP %d", response.Code)
		}
	}
	check := func(route *ent.ModelBinding, want uuid.UUID) {
		t.Helper()
		ready, err := catalog.BindingRouteReady(ctx, client, route)
		if err != nil || ready != (want != uuid.Nil) {
			t.Fatalf("readiness=%t want=%t (%v)", ready, want != uuid.Nil, err)
		}
		credential, got, err := catalog.ResolveBindingCredential(ctx, client, manager, route)
		if err != nil || got != want || (credential != "") != (want != uuid.Nil) {
			t.Fatalf("gateway resolved Key ID %s, want %s (%v)", got, want, err)
		}
	}
	recordProbe := func(code int, failure bool) {
		t.Helper()
		execCatalogTestSQL(t, ctx, db, `
INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at,last_passed_at)
VALUES($1,$2,$3,'openai_responses',$4,CASE WHEN $5 THEN 'http_error' END,1,statement_timestamp(),statement_timestamp())
ON CONFLICT(provider_id,credential_id,model,protocol) DO UPDATE
SET status_code=EXCLUDED.status_code,error_code=EXCLUDED.error_code,checked_at=EXCLUDED.checked_at`,
			provider, key, upstream, code, failure)
	}
	checkStudent := func(availability, state, reason string) {
		t.Helper()
		protocols, err := catalog.LoadPublishedProtocols(ctx, client, []uuid.UUID{modelID}, manager)
		if err != nil {
			t.Fatal(err)
		}
		channels, err := ReadStudentModelCatalog(ctx, db, []string{school})
		if err != nil {
			t.Fatal(err)
		}
		view := catalog.SummarizeModelAvailability(school, entmodel.StatusActive, protocols[modelID])
		view.SetStudentState(school, entmodel.StatusActive, channels[school])
		if view.Availability != availability || view.AvailabilityState != state || !strings.Contains(view.AvailabilityReason, reason) {
			t.Fatalf("student state = %+v, want %s/%s with %q", view, availability, state, reason)
		}
	}
	check(&binding, key)
	recordProbe(401, true)
	check(&binding, uuid.Nil)
	checkRequestBlocked()
	checkStudent("maintenance", "probe_failed", "未通过上游认证")
	other := binding
	other.CredentialID = &alternate
	check(&other, alternate)
	other = binding
	other.Adapter = modelbinding.AdapterOpenaiCompatible
	check(&other, key)
	legacy := binding
	legacy.CredentialID = nil
	check(&legacy, uuid.Nil)
	for _, status := range []int{403, 404, 429, 503, 200} {
		recordProbe(status, status != 200)
		expected := key
		if status == 403 {
			expected = uuid.Nil
		}
		check(&binding, expected)
		switch status {
		case 404:
			checkStudent("maintenance", "probe_failed", "HTTP 404")
		case 429, 503:
			checkStudent("degraded", "degraded", "可能请求失败")
		}
	}
	// The directory/group marker blocks the Key even on another adapter
	// which has no failed generation probe.
	denial := func(denied bool) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var revision time.Time
		if err := tx.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1 FOR UPDATE", key).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveBeichenProbeGroupDenial(ctx, tx, key, revision, upstream, "openai_responses", denied); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	denial(true)
	check(&binding, uuid.Nil)
	checkRequestBlocked()
	checkStudent("maintenance", "channel_unavailable", "分组权限")
	check(&other, uuid.Nil)
	check(&legacy, uuid.Nil)
	alternateRoute := binding
	alternateRoute.CredentialID = &alternate
	check(&alternateRoute, alternate)
	denial(false)
	check(&legacy, key)
	checkStudent("available", "available", "")
	denial(true)
	recordProbe(401, true)
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET updated_at=clock_timestamp() WHERE id=$1", key)
	check(&binding, key) // The denial and probe belong to the previous Key version.
}

func TestCatalogAuthFailureBlocksGatewayUntilSuccessfulScanPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	manager := switchTestManager(t)
	const upstream = "auth-routed-model"
	key := keys[1]
	ciphertext, err := manager.EncryptCredential("fixture-auth-routing")
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", key, ciphertext)
	entries := []catalog.ProviderEntry{{ID: upstream, SupportedProtocols: []string{"openai_responses"}}}
	saveCatalogFixture(t, ctx, db, provider, key, entries, nil)
	var revision time.Time
	if err := db.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	binding := &ent.ModelBinding{ProviderID: provider, CredentialID: &key, UpstreamModelName: upstream,
		Adapter: modelbinding.AdapterOpenaiResponses, Status: modelbinding.StatusActive}
	check := func(want bool) {
		t.Helper()
		ready, err := catalog.BindingRouteReady(ctx, client, binding)
		if err != nil || ready != want {
			t.Fatalf("route readiness = %t, want %t (%v)", ready, want, err)
		}
		credential, id, err := catalog.ResolveBindingCredential(ctx, client, manager, binding)
		if err != nil || (credential != "") != want || (id != uuid.Nil) != want {
			t.Fatalf("gateway credential = %q/%s, want ready=%t (%v)", credential, id, want, err)
		}
	}
	scan := func(snapshot catalog.BeichenModelSnapshot, fetchErr error) {
		t.Helper()
		lease, err := catalog.AcquireBeichenDatasetLease(ctx, db, key, "billing", true)
		if err != nil || lease == uuid.Nil {
			t.Fatalf("catalog lease: %s %v", lease, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		current, err := catalog.LockBeichenScan(ctx, tx, provider, key, "billing", lease, revision)
		if err != nil || !current {
			t.Fatalf("catalog scan lock: current=%t %v", current, err)
		}
		if _, err := catalog.SaveBeichenKeyScan(ctx, tx, provider, key, revision, time.Now().UTC(), snapshot, fetchErr); err != nil {
			t.Fatal(err)
		}
		if err := catalog.ReleaseBeichenDatasetLease(ctx, tx, key, "billing"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
	scan(catalog.BeichenModelSnapshot{}, catalog.BeichenHTTPStatus(http.StatusUnauthorized))
	check(false)
	var marker bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_state
WHERE name='beichen_catalog_auth/'||$1::text)`, key).Scan(&marker); err != nil || !marker {
		t.Fatalf("missing credential-version auth marker: %t %v", marker, err)
	}
	// A later transient failure cannot disprove the earlier 401.
	scan(catalog.BeichenModelSnapshot{}, catalog.BeichenHTTPStatus(http.StatusTooManyRequests))
	check(false)
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_state
WHERE name='beichen_catalog_auth/'||$1::text)`, key).Scan(&marker); err != nil || !marker {
		t.Fatalf("transient scan cleared auth marker: %t %v", marker, err)
	}
	scan(catalog.BeichenModelSnapshot{Entries: entries, Complete: true}, nil)
	check(true)
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_state
WHERE name='beichen_catalog_auth/'||$1::text)`, key).Scan(&marker); err != nil || marker {
		t.Fatalf("successful scan did not clear auth marker: %t %v", marker, err)
	}
}

func TestGroupDenialExpiresAfterObservedGroupChangePostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	const upstream = "group-scoped-model"
	key := keys[1]
	saveCatalogFixture(t, ctx, db, provider, key, []catalog.ProviderEntry{
		{ID: upstream, SupportedProtocols: []string{"openai_responses"}},
	}, nil)
	school := uuid.New()
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2::text,$2::text,'active',1000,now(),now())`, school, upstream)
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,'openai_responses',7,now(),now())`,
		school, provider, key, upstream)
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at)
VALUES($1,0.15,15,statement_timestamp()-interval '2 minutes')`, key)
	var revision time.Time
	if err := db.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT c.id FROM provider_credentials c WHERE c.id=$1 FOR UPDATE", key); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveBeichenProbeGroupDenial(ctx, tx, key, revision, upstream, "openai_responses", true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state := func() catalog.ModelCapability {
		t.Helper()
		view, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range view.Capabilities {
			if row.Model == upstream && row.CredentialID != nil && *row.CredentialID == key {
				return row
			}
		}
		t.Fatal("missing group-scoped capability")
		return catalog.ModelCapability{}
	}
	if row := state(); row.GroupStatus != "not_allowed" || row.BindingState != "invalid" {
		t.Fatalf("current group denial was not applied: %+v", row)
	}
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at)
VALUES($1,0.6,60,statement_timestamp()-interval '1 minute')`, key)
	if row := state(); row.GroupStatus == "not_allowed" || row.BindingState != "ready" {
		t.Fatalf("previous group denial survived the observed group change: %+v", row)
	}
	binding := &ent.ModelBinding{ProviderID: provider, CredentialID: &key, UpstreamModelName: upstream,
		Adapter: modelbinding.AdapterOpenaiResponses, Status: modelbinding.StatusActive}
	if ready, err := catalog.BindingRouteReady(ctx, client, binding); err != nil || !ready {
		t.Fatalf("gateway route kept the previous group denial: ready=%t %v", ready, err)
	}
}
