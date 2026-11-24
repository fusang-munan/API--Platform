package dataplane

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestTerminalPendingPostgres(t *testing.T) {
	f := newPendingGateway(t)
	f.db.SetMaxOpenConns(8)
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "hold")
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "100")
	t.Setenv("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", "1") // clamped to 60 seconds
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	f.respond(`,"total_tokens":102`)
	type sample struct {
		prompt, output, hit, miss, write, total any
		credits, reason                         string
		knownIn, knownOut                       int
	}
	cases := []sample{
		{10, 2, nil, nil, nil, nil, "0.0044", "", 10, 2},
		{10, nil, nil, nil, nil, 999, "0.0022", "", 10, 0},
		{nil, nil, nil, nil, nil, nil, "0", "no_known_billable_tokens", 0, 0},
		{10, 2, nil, nil, nil, nil, "0", "missing_dispatch_price", 10, 2},
		{10, 2, 20, 10, 4, 36, "0.00572", "", 34, 2},
	}
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("terminal-%02d", i)
		if w := f.request(id); w.Code != 200 {
			t.Fatalf("%s HTTP %d %s", id, w.Code, w.Body)
		}
		s := cases[i%len(cases)]
		f.exec(t, `UPDATE gateway_calls SET outcome='outcome_unknown',usage_state='interrupted',usage_reason='response_interrupted',
			beichen_review_reason='missing_terminal_usage',unresolved_credit_milli=7,
			finalized_at=now()-interval '2 minutes'+$9::int*interval '1 second',
			prompt_tokens=$2,completion_tokens=$3,cache_hit_tokens=$4,cache_miss_tokens=$5,cache_write_tokens=$6,total_tokens=$7,
			beichen_price_at=CASE WHEN $8 THEN NULL ELSE beichen_price_at END,cache_ttl='300',
			pricing_detail='{"original_evidence":true}' WHERE request_id=$1`, id, s.prompt, s.output, s.hit, s.miss, s.write, s.total, s.reason == "missing_dispatch_price", i)
	}
	checkRun := func(limit, want int) {
		t.Helper()
		r, err := usage.ResolveStaleTerminalPending(f.ctx, f.db, limit)
		if err != nil || r.Errors != 0 || r.Resolved != want {
			t.Fatalf("resolve: %+v, want %d: %v", r, want, err)
		}
	}
	checkRun(1, 0)
	r, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, 1, 0, "")
	if err != nil || r.TerminalMissing != 15 || r.TerminalEnabled || r.AutoSettle != 0 {
		t.Fatalf("paged preview: %+v %v", r, err)
	}
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "known_only")
	other, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, uuid.New())
	if err != nil || other.Resolved != 0 || other.Errors != 0 {
		t.Fatalf("provider scope: %+v %v", other, err)
	}
	checkRun(1, 1)
	assertBeichenGatewayMonth(t, f.ctx, f.db, "terminal-00", "0.0044", 1)
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '20 seconds' WHERE request_id='terminal-13'`)
	f.exec(t, `UPDATE gateway_calls SET beichen_review_reason='incomplete_token_breakdown' WHERE request_id='terminal-14'`)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { defer close(done); usage.RunTerminalPending(ctx, f.db) }()
	defer func() { cancel(); <-done }()
	manual, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil || manual.Errors != 0 {
		t.Fatalf("manual: %+v %v", manual, err)
	}
	var remaining int
	for {
		if err = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM gateway_calls WHERE beichen_state='pending_review'`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 2 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	checkRun(0, 0)
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '10 minutes',beichen_review_reason='missing_terminal_usage'
		WHERE request_id IN ('terminal-13','terminal-14') AND beichen_state='pending_review'`)
	// The worker runs its first batch immediately and exits on cancellation.
	worker, cancelWorker := context.WithCancel(f.ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); usage.RunTerminalPending(worker, f.db) }()
	defer func() { cancelWorker(); <-finished }()
	for remaining > 0 {
		if err = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM gateway_calls WHERE beichen_state='pending_review'`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining > 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancelWorker()
	<-finished
	checkRun(0, 0)
	for i := 0; i < 15; i++ {
		s := cases[i%len(cases)]
		var ok bool
		err = f.db.QueryRowContext(f.ctx, `SELECT COALESCE(beichen_state='charged' AND billing_state='charged' AND student_credits=$2::numeric
			AND beichen_review_reason IS NULL AND unresolved_credit_milli=0 AND pricing_detail @> '{"terminal_missing":true,"terminal_policy":"known_only","original_evidence":true}'
			AND pricing_detail->>'known_input_tokens'=$3 AND pricing_detail->>'known_output_tokens'=$4 AND pricing_detail ? 'processed_at'
			AND (pricing_detail->>'priced')::boolean=$5 AND COALESCE(pricing_detail->>'reason','')=$6
			AND completion_tokens IS NOT DISTINCT FROM $7::bigint AND usage_state='interrupted',false)
			FROM gateway_calls WHERE request_id=$1`, fmt.Sprintf("terminal-%02d", i), s.credits, fmt.Sprint(s.knownIn), fmt.Sprint(s.knownOut), s.reason == "", s.reason, s.output).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("sample %d expected credits %s, reason %q: %v", i, s.credits, s.reason, err)
		}
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "terminal-00", "0.03696", 9)
	var settledID uuid.UUID
	if err := f.db.QueryRowContext(f.ctx, "SELECT id FROM gateway_calls WHERE request_id='terminal-02'").Scan(&settledID); err != nil {
		t.Fatal(err)
	}
	lateOutput := int64(100)
	if err := f.gateway.usage.CompleteCallMetrics(f.ctx, settledID, usage.CallMetrics{State: usage.UsageReported, CompletionTokens: &lateOutput}); err != nil {
		t.Fatal(err)
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "terminal-02", "0.03696", 9)
	var zeros int
	if err = f.db.QueryRowContext(f.ctx, "SELECT sum(zero_cost_count) FROM monthly_usage_cube").Scan(&zeros); err != nil || zeros != 6 {
		t.Fatalf("zero postings=%d: %v", zeros, err)
	}
}
func TestTerminalPendingOpenAIKnownCountersPostgres(t *testing.T) {
	f := newPendingGateway(t)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "known_only")
	t.Setenv("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", "300")
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "2")
	f.respond(`,"total_tokens":102`)
	cases := []struct {
		id                                      string
		prompt, output, total, hit, miss, write any
		credits                                 string
		priced                                  bool
	}{
		{"inclusive", 34, 2, 36, 20, 10, 4, "0.00572", true},
		{"cache-only", nil, nil, nil, 20, nil, nil, "0.00022", true},
		{"output-only", nil, 2, nil, nil, nil, nil, "0.0022", true},
		{"total-only", nil, nil, 200, nil, nil, nil, "0", false},
		{"no-quote", 34, 2, 36, 20, 10, 4, "0", false},
		{"conflicting", 10, 2, 4, 0, 10, 0, "0", false},
		{"ambiguous-input", 34, 2, 36, nil, nil, nil, "0", false},
	}
	for _, s := range cases {
		if w := f.request(s.id); w.Code != 200 {
			t.Fatalf("%s HTTP %d", s.id, w.Code)
		}
		f.exec(t, `UPDATE gateway_calls SET beichen_review_reason='missing_terminal_usage',
			finalized_at=now()-interval '10 minutes',protocol='openai_responses',
			usage_reason='response_interrupted',prompt_tokens=$2,completion_tokens=$3,total_tokens=$4,
			cache_hit_tokens=$5,cache_miss_tokens=$6,cache_write_tokens=$7,
			beichen_dispatch_model=CASE WHEN request_id='no-quote' THEN 'unknown-price' ELSE beichen_dispatch_model END
			WHERE request_id=$1`, s.id, s.prompt, s.output, s.total, s.hit, s.miss, s.write)
	}
	for _, want := range []int{2, 2, 2, 1, 0} {
		r, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
		if err != nil || r.Resolved != want || r.Errors != 0 {
			t.Fatalf("batch %+v want %d: %v", r, want, err)
		}
	}
	for _, s := range cases {
		var ok bool
		err := f.db.QueryRowContext(f.ctx, `SELECT COALESCE(student_credits=$2::numeric AND
			(pricing_detail->>'priced')::bool=$3 AND (pricing_detail ? 'reason')<>$3 AND
			beichen_state='charged' AND unresolved_credit_milli=0 AND beichen_review_reason IS NULL,false)
			FROM gateway_calls WHERE request_id=$1`, s.id, s.credits, s.priced).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("%s expected %s credits, priced=%v: %v", s.id, s.credits, s.priced, err)
		}
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "inclusive", "0.00814", 3)
}
