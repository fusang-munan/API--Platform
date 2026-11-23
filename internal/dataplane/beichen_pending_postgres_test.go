package dataplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenPendingSettlementPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newBeichenGatewayFixture(t, ctx)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE_YUAN", "")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE_RELATIVE", "")
	t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "")
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := f.db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE model_bindings SET adapter='anthropic' WHERE id=$1", f.binding)
	exec("INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,1)", f.credential)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
		VALUES ($1,'G3',decode(repeat('ab',32),'hex'),false)`, f.credential)
	send := func(id, counters string) {
		t.Helper()
		f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
			defer r.Body.Close()
			httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			if id == "unknown-multiplier" {
				// Older dispatches can lack the saved school multiplier.
				exec("UPDATE gateway_calls SET beichen_school_multiplier=NULL WHERE request_id=$1", id)
			}
			body := fmt.Sprintf(`{"model":%q,"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1%s}}`, upstreamModel, counters)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			fmt.Sprintf(`{"model":%q,"max_tokens":2,"messages":[],"cache_control":{"type":"ephemeral"}}`, publicModel))).
			WithContext(usage.WithRequestID(ctx, id))
		req.Header.Set("Authorization", "Bearer "+studentKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.gateway.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", id, w.Code, w.Body.String())
		}
	}
	check := func(id, predicate string) {
		t.Helper()
		var ok bool
		err := f.db.QueryRowContext(ctx, "SELECT COALESCE(("+predicate+"),false) FROM gateway_calls WHERE request_id=$1", id).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("%s: expected %s: %v", id, predicate, err)
		}
	}
	const narrow = `,"total_tokens":102`
	const charged = `beichen_state='charged' AND billing_state='charged' AND beichen_review_reason IS NULL
		AND unresolved_credit_milli=0 AND usage_state='partial' AND cache_write_tokens IS NULL
		AND pricing_detail @> '{"auto_settled":true,"policy_version":"pending-audit-v1","min_yuan":0.000022,"max_yuan":0.000262}'
		AND pricing_detail->>'base_source'='gateway_usage_price' AND pricing_detail ? 'base_credits'
		AND student_credits <= (pricing_detail->>'max_yuan')::numeric * 100 * 1.25 * 1.10`
	const pending = `beichen_state='pending_review' AND beichen_review_reason='incomplete_token_breakdown'
		AND student_credits IS NULL AND beichen_credit_exact IS NULL AND credit_milli=0
		AND NOT coalesce(pricing_detail ? 'auto_settled',false) AND unresolved_credit_milli=`
	send("narrow", narrow)
	check("narrow", charged+` AND pricing_detail @> '{"prior_read_share":0.5,"recommended_read_tokens":50,"recommended_yuan":0.000142}'
		AND student_credits=0.01562`)
	var reservation usage.Reservation
	var route usage.CallRoute
	route.Beichen = true
	err := f.db.QueryRowContext(ctx, `SELECT id,beichen_dispatch_credential_id,beichen_dispatch_model,beichen_price_at
		FROM gateway_calls WHERE request_id='narrow'`).Scan(&reservation.CallID, &route.CredentialID, &route.UpstreamModelName, &route.SentAt)
	if err != nil {
		t.Fatal(err)
	}
	one, total := int64(1), int64(102)
	report := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &one, CompletionTokens: &one, TotalTokens: &total}
	if err = f.gateway.usage.Finalize(ctx, reservation, true, "succeeded", "", "", route, report); err != nil {
		t.Fatal(err)
	}
	send("wide", `,"total_tokens":100002`)
	check("wide", pending+"35") // ceil(0.250012 * 100 * 1.25 * 1.10)
	send("unknown", "")
	check("unknown", pending+"10")
	t.Run("off", func(t *testing.T) {
		t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
		send("off", narrow)
		check("off", pending+"10")
	})
	for _, table := range []string{"project_month_credit_buckets", "project_member_month_credit_buckets", "api_key_month_credit_buckets", "monthly_usage_cube"} {
		var once bool
		if err := f.db.QueryRowContext(ctx, "SELECT charged_milli+charged_milli_remainder=15.62 FROM "+table).Scan(&once); err != nil || !once {
			t.Fatalf("%s: expected exactly one charge: %v", table, err)
		}
	}
	// A lone cache_creation child makes the native report unpriceable even
	// before checking rates. Preserve the child alongside the booking detail.
	send("subtotal", narrow+`,"cache_creation":{"ephemeral_5m_input_tokens":50}`)
	check("subtotal", charged+` AND usage_reason='cache_write_subtotal_only'
		AND pricing_detail @> '{"cache_write_5m_tokens":50,"recommended_write_tokens":50}'`)
	send("incompatible-subtotal", narrow+`,"cache_creation":{"ephemeral_5m_input_tokens":60}`)
	check("incompatible-subtotal", pending+"1")
	t.Run("thresholds", func(t *testing.T) {
		t.Setenv("NEBULA_PENDING_AUTO_SETTLE_YUAN", "0")
		t.Setenv("NEBULA_PENDING_AUTO_SETTLE_RELATIVE", "0")
		send("strict", narrow)
		check("strict", pending+"1")
		t.Setenv("NEBULA_PENDING_AUTO_SETTLE_RELATIVE", "11")
		send("relative", narrow)
		check("relative", charged)
	})
	send("observed", `,"cache_read_input_tokens":90,"cache_creation_input_tokens":9`)
	send("prior", narrow)
	check("prior", charged+` AND pricing_detail @> '{"prior_read_share":0.9,"recommended_read_tokens":90,"recommended_write_tokens":10,"recommended_yuan":0.000046}'
		AND student_credits=0.00506 AND (pricing_detail->>'recommended_yuan')::numeric
		BETWEEN (pricing_detail->>'min_yuan')::numeric AND (pricing_detail->>'max_yuan')::numeric`)
	exec("UPDATE models SET credit_multiplier_milli=0")
	send("zero-multiplier", narrow)
	check("zero-multiplier", pending+"1")
	exec("UPDATE models SET credit_multiplier_milli=1250")
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
		VALUES($1,$2,'school','manual','teacher',2000000000,2000000000,2000000000,4000000000,$3)`, f.credential, upstreamModel, f.teacher)
	send("manual-over-bound", narrow)
	check("manual-over-bound", pending+"1")
	send("unknown-multiplier", `,"total_tokens":100002`)
	check("unknown-multiplier", pending+"51") // ceil(0.250012 * 100 * 2)
	send("reserve-cap", `,"total_tokens":5000002`)
	check("reserve-cap", pending+"1000")
}
