package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
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
		if result.StatusCode != 400 || result.LastPassedAt == nil ||
			!result.LastPassedAt.Equal(passes[result.Model]) || !result.CheckedAt.After(passes[result.Model]) {
			t.Fatalf("failed retry should preserve the same key's success time: %+v", result)
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
		AND status_code=400 AND error_code='http_error' AND input_tokens IS NULL AND output_tokens IS NULL
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
			if *result.ErrorCode != "invalid_response" && *result.ErrorCode != "http_error" {
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
