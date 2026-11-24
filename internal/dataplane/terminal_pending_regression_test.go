package dataplane

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func terminalRegressionGateway(t *testing.T) pendingGateway {
	t.Helper()
	f := newPendingGateway(t)
	f.db.SetMaxOpenConns(8)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "known_only")
	t.Setenv("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", "300")
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "100")
	return f
}

func terminalStart(t *testing.T, f pendingGateway, id, model, counters string) {
	t.Helper()
	body := fmt.Sprintf(`{"type":"message_start","model":%q,"message":{"id":"msg_%s","stop_reason":null,"usage":{%s}}}`, model, id, counters)
	f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if w := f.request(id); w.Code != 200 {
		t.Fatalf("%s upstream HTTP %d: %s", id, w.Code, w.Body)
	}
}

func terminalRow(t *testing.T, f pendingGateway, id string) (state, review, reason, credits string) {
	t.Helper()
	if err := f.db.QueryRowContext(f.ctx, `SELECT beichen_state,COALESCE(beichen_review_reason,''),COALESCE(usage_reason,''),
		COALESCE(trim_scale(student_credits)::text,'NULL') FROM gateway_calls WHERE request_id=$1`, id).
		Scan(&state, &review, &reason, &credits); err != nil {
		t.Fatal(err)
	}
	return
}

func TestTerminalPendingModelMismatchStaysReview(t *testing.T) {
	f := terminalRegressionGateway(t)
	terminalStart(t, f, "wrong-model", "different-model-without-price", `"input_tokens":11,"output_tokens":2`)
	state, review, _, credits := terminalRow(t, f, "wrong-model")
	if state != "pending_review" || review != "upstream_model_mismatch" || credits != "NULL" {
		t.Fatalf("state=%s review=%s credits=%s", state, review, credits)
	}
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '301 seconds' WHERE request_id='wrong-model'`)
	r, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil || r.Resolved != 0 || r.Errors != 0 {
		t.Fatalf("resolve=%+v err=%v", r, err)
	}
	state, review, _, credits = terminalRow(t, f, "wrong-model")
	if state != "pending_review" || review != "upstream_model_mismatch" || credits != "NULL" {
		t.Fatalf("after resolve state=%s review=%s credits=%s", state, review, credits)
	}
}

func TestTerminalPendingCacheConflictStaysReview(t *testing.T) {
	f := terminalRegressionGateway(t)
	terminalStart(t, f, "cache-conflict", upstreamModel,
		`"input_tokens":11,"output_tokens":2,"cache_creation_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":1}`)
	state, review, reason, credits := terminalRow(t, f, "cache-conflict")
	if state != "pending_review" || review != "missing_terminal_usage" || reason != "conflicting_cache_creation_counts" || credits != "NULL" {
		t.Fatalf("state=%s review=%s reason=%s credits=%s", state, review, reason, credits)
	}
	preview, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, 10, 0, "")
	if err != nil || preview.TerminalMissing != 0 || preview.BacklogTotal != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '301 seconds' WHERE request_id='cache-conflict'`)
	r, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil || r.Resolved != 0 || r.Errors != 0 {
		t.Fatalf("resolve=%+v err=%v", r, err)
	}
	state, review, reason, credits = terminalRow(t, f, "cache-conflict")
	if state != "pending_review" || review != "missing_terminal_usage" || reason != "conflicting_cache_creation_counts" || credits != "NULL" {
		t.Fatalf("after resolve state=%s review=%s reason=%s credits=%s", state, review, reason, credits)
	}
}

func TestTerminalPendingKnownCacheChild(t *testing.T) {
	f := terminalRegressionGateway(t)
	terminalStart(t, f, "single-child", upstreamModel, `"cache_creation":{"ephemeral_5m_input_tokens":4}`)
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '301 seconds' WHERE request_id='single-child'`)
	r, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil || r.Resolved != 1 || r.Errors != 0 {
		t.Fatalf("resolve=%+v err=%v", r, err)
	}
	var known, child, credits string
	if err := f.db.QueryRowContext(f.ctx, `SELECT pricing_detail->>'known_input_tokens',
		pricing_detail->>'cache_write_5m_tokens',trim_scale(student_credits)::text FROM gateway_calls WHERE request_id='single-child'`).
		Scan(&known, &child, &credits); err != nil {
		t.Fatal(err)
	}
	if known != "4" || child != "4" || credits != "0.0011" {
		t.Fatalf("known=%s child=%s credits=%s", known, child, credits)
	}
}

func TestTerminalPendingFailureDoesNotStarve(t *testing.T) {
	f := terminalRegressionGateway(t)
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "1")
	for _, id := range []string{"old-bad", "later-good"} {
		terminalStart(t, f, id, upstreamModel, `"input_tokens":1`)
	}
	f.exec(t, `UPDATE gateway_calls SET beichen_review_reason='missing_terminal_usage',usage_reason='response_interrupted',
		finalized_at=now()-CASE request_id WHEN 'old-bad' THEN interval '20 minutes' ELSE interval '10 minutes' END`)
	f.exec(t, `CREATE FUNCTION qa_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.request_id='old-bad' AND NEW.beichen_state='charged' THEN RAISE EXCEPTION 'qa intentional fault';END IF;
		RETURN NEW;END $$;
		CREATE TRIGGER qa_reject BEFORE UPDATE ON gateway_calls FOR EACH ROW EXECUTE FUNCTION qa_reject()`)
	for i := 0; i < 3; i++ {
		if _, err := usage.ResolveStaleTerminalPending(f.ctx, f.db, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := terminalCount(t, f, `SELECT count(*) FROM gateway_calls WHERE request_id='later-good' AND beichen_state='charged'`); n != 1 {
		t.Fatal("later valid row starved by one persistent booking failure")
	}
	if n := terminalCount(t, f, `SELECT count(*) FROM gateway_calls WHERE request_id='old-bad' AND beichen_state='pending_review'`); n != 1 {
		t.Fatal("failed row must remain pending")
	}
}

func terminalCount(t *testing.T, f pendingGateway, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
