package beichen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestProbeFrequencyWindowsPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	key := keys[0]
	_, err := db.ExecContext(ctx, "UPDATE provider_credentials SET updated_at=now()-interval '1 day' WHERE id=$1", key)
	if err != nil {
		t.Fatal(err)
	}
	store := catalog.ProviderCatalogSyncer{DB: db}
	entries := []catalog.ProviderEntry{
		{ID: "healthy", SupportedProtocols: []string{"openai_compatible"}},
		{ID: "failure", SupportedProtocols: []string{"openai_compatible"}},
	}
	if _, err := store.UpsertCredentialEntries(ctx, provider, key, entries, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
	VALUES ($1,$2,'healthy','openai_compatible',200,NULL,1,now()-interval '2 hours'),
	($1,$2,'failure','openai_compatible',200,'invalid_response',1,now()-interval '65 minutes')`, provider, key)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("x", 32))
	manager, err := security.NewManager(secret, secret, secret, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, manager)
	check := func(force bool, want int, first string) {
		t.Helper()
		targets, _, err := s.listProbeTargets(ctx, probeJob{provider: provider, startedAt: time.Now(), force: force})
		if err != nil || len(targets) != want || want > 0 && targets[0].model != first {
			t.Fatalf("force=%t, targets=%v, err=%v", force, targets, err)
		}
	}
	check(false, 1, "failure")
	check(true, 2, "healthy")
	_, err = db.ExecContext(ctx, `UPDATE upstream_probes SET checked_at=CASE WHEN model='healthy' THEN now()-interval '361 minutes' ELSE now()-interval '59 minutes' END WHERE provider_id=$1`, provider)
	if err != nil {
		t.Fatal(err)
	}
	check(false, 1, "healthy")
}

func TestProbeUsageDayBoundsPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	_, err := db.ExecContext(ctx, `INSERT INTO beichen_probe_usage(id,provider_id,credential_id,model,protocol,checked_at,input_tokens)
	SELECT gen_random_uuid(),$1,$2,'day','anthropic',stamp,tokens FROM (VALUES
	  ('2026-11-22 15:59:59+00'::timestamptz,50), ('2026-11-22 16:00:00+00',0),
	  ('2026-11-23 15:59:59+00',NULL), ('2026-11-23 16:00:00+00',30)) v(stamp,tokens)`, provider, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, nil)
	day, err := s.ProbeUsageForDay(ctx, provider, "2026-11-23")
	if err != nil || day.Attempts != 2 || day.Input.Reported != 1 || day.Input.Tokens == nil || *day.Input.Tokens != 0 {
		t.Fatalf("Shanghai day with unknown and zero: %+v %v", day, err)
	}
	empty, err := s.ProbeUsageForDay(ctx, keys[1], "2026-11-23")
	if err != nil || empty.Attempts != 0 || empty.Input.Tokens != nil {
		t.Fatalf("provider filter: %+v %v", empty, err)
	}
}

func TestProbeRetryAndSweepReservationPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "500")
	s := New(db, nil)
	job := probeJob{provider: provider, lease: keys[1], id: keys[2]}
	if _, err := db.ExecContext(ctx, `INSERT INTO beichen_probe_jobs(provider_id,lease_id,lease_until,job_id) VALUES ($1,$2,now()+interval '1 minute',$3)`, provider, job.lease, job.id); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	target := probeTarget{credential: keys[0], model: "retry", protocol: "openai_compatible", client: &catalog.RouteProbe{BaseURL: origin, Credential: "fixture", Client: &http.Client{Transport: panelTransport(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("fixture transport error")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0}}`))}, nil
	})}}}
	run := func(ctx context.Context, batch []probeTarget) ([]probeObservation, error) {
		return s.runProbeRound(ctx, job, batch)
	}
	results, err := run(ctx, []probeTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := retryFailedProbes(ctx, results, 0, run); err != nil || results[0].statusCode != 200 {
		t.Fatalf("retried: %v %v", results, err)
	}
	var failed, passed int
	err = db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status_code IS NULL AND error_code='network_error' AND input_tokens IS NULL),
	count(*) FILTER(WHERE status_code=200 AND error_code IS NULL AND input_tokens=7 AND output_tokens=0)
	FROM beichen_probe_usage WHERE job_id=$1`, job.id).Scan(&failed, &passed)
	if err != nil || failed != 1 || passed != 1 {
		t.Fatalf("failures=%d successes=%d err=%v", failed, passed, err)
	}
	// Overlapping reservations compete for the last slots of this same sweep.
	batch := make([]probeTarget, probeSweepLimit)
	for i := range batch {
		batch[i] = target
	}
	type reserved struct {
		n   int
		err error
	}
	done := make(chan reserved, 2)
	for range 2 {
		go func() {
			ids, err := s.reserveProbeAttempts(ctx, job, batch)
			done <- reserved{len(ids), err}
		}()
	}
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.n+b.n != probeSweepLimit-2 {
		t.Fatalf("reservations: %+v %+v", a, b)
	}
}
