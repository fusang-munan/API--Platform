package dataplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

type pendingGateway struct {
	beichenGatewayFixture
	ctx context.Context
}

func newPendingGateway(t *testing.T) pendingGateway {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := pendingGateway{newBeichenGatewayFixture(t, ctx), ctx}
	t.Setenv("NEBULA_UNRESOLVED_EXPOSURE", "")
	t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "")
	t.Setenv("NEBULA_STUDENT_PENDING_REVIEW_LIMIT", "")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE_YUAN", "")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE_RELATIVE", "")
	t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "")
	f.exec(t, "UPDATE model_bindings SET adapter='anthropic' WHERE id=$1", f.binding)
	f.exec(t, "INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,1)", f.credential)
	f.exec(t, `INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
		VALUES ($1,'G3',decode(repeat('ab',32),'hex'),false)`, f.credential)
	return f
}

func (f pendingGateway) exec(t *testing.T, stmt string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, stmt, args...); err != nil {
		t.Fatal(err)
	}
}

func (f pendingGateway) respond(counters string) {
	f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		body := fmt.Sprintf(`{"model":%q,"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1%s}}`, upstreamModel, counters)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func (f pendingGateway) request(id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		fmt.Sprintf(`{"model":%q,"max_tokens":2,"messages":[],"cache_control":{"type":"ephemeral"}}`, publicModel))).
		WithContext(usage.WithRequestID(f.ctx, id))
	req.Header.Set("Authorization", "Bearer "+studentKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.gateway.ServeHTTP(w, req)
	return w
}

func TestStudentConcurrencyHTTPPostgres(t *testing.T) {
	f := newPendingGateway(t)
	t.Setenv("NEBULA_STUDENT_CONCURRENCY_LIMIT", "")
	t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "10000")
	f.respond(`,"total_tokens":102`)
	upstream := f.gateway.client.Transport
	arrived := make(chan struct{}, 6)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var dispatches atomic.Int32
	f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		if dispatches.Add(1) > 5 {
			return upstream.RoundTrip(r)
		}
		arrived <- struct{}{}
		select {
		case <-release:
			return upstream.RoundTrip(r)
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})

	var workers sync.WaitGroup
	defer workers.Wait()
	defer unblock()
	done := make(chan *httptest.ResponseRecorder, 5)
	var ids [5]string
	for i := range ids {
		ids[i] = fmt.Sprintf("in-flight-%d", i+1)
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			done <- f.request(id)
		}(ids[i])
	}
	// Hold all five at the upstream until the sixth receives its HTTP response.
	for range ids {
		select {
		case <-arrived:
		case w := <-done:
			t.Fatalf("request ended before all five reached upstream: HTTP %d %s", w.Code, w.Body)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	rejected := f.request("sixth")
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("X-Nebula-Error-Code") != usage.StudentConcurrencyExceeded {
		t.Fatalf("sixth request: HTTP %d, code %q", rejected.Code, rejected.Header().Get("X-Nebula-Error-Code"))
	}
	unblock()
	for range ids {
		if w := <-done; w.Code != http.StatusOK {
			t.Fatalf("accepted request: HTTP %d %s", w.Code, w.Body)
		}
	}
	if got := dispatches.Load(); got != 5 {
		t.Fatalf("upstream received %d requests, want 5", got)
	}
	var calls int
	if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM gateway_calls").Scan(&calls); err != nil || calls != len(ids) {
		t.Fatalf("gateway calls=%d, want only the five accepted requests: %v", calls, err)
	}
	for _, id := range ids {
		var charged bool
		err := f.db.QueryRowContext(f.ctx, `SELECT billing_state='charged' AND beichen_state='charged'
			AND student_credits=0.01562 AND unresolved_credit_milli=0 AND pricing_detail @> '{"auto_settled":true}'
			FROM gateway_calls WHERE request_id=$1`, id).Scan(&charged)
		if err != nil || !charged {
			t.Fatalf("%s: expected an automatic charge of 0.01562 credits: %v", id, err)
		}
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, ids[0], "0.07810", 5)
}

func TestBeichenPendingSettlementPostgres(t *testing.T) {
	f := newPendingGateway(t)
	ctx := f.ctx
	exec := func(stmt string, args ...any) { t.Helper(); f.exec(t, stmt, args...) }
	send := func(id, counters string) {
		t.Helper()
		f.respond(counters)
		if id == "unknown-multiplier" {
			upstream := f.gateway.client.Transport
			f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				// Older dispatches can lack the saved school multiplier.
				exec("UPDATE gateway_calls SET beichen_school_multiplier=NULL WHERE request_id=$1", id)
				return upstream.RoundTrip(r)
			})
		}
		w := f.request(id)
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
		AND student_credits <= (pricing_detail->>'max_yuan')::numeric * 100 * 1.10`
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
	check("wide", pending+"28") // ceil(0.250012 * 100 * 1.10)
	send("unknown", "")
	check("unknown", pending+"10")
	t.Run("off", func(t *testing.T) {
		t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
		send("off", narrow)
		check("off", pending+"1")
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
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		SELECT gen_random_uuid(),'other-model',display_name,'active',1250,now(),now() FROM models`)
	exec("UPDATE gateway_calls SET model_id=(SELECT id FROM models WHERE model_id='other-model') WHERE request_id='observed'")
	send("same-name", narrow)
	check("same-name", charged+` AND pricing_detail @> '{"prior_read_share":0.5}' AND student_credits=0.01562`)
	exec("UPDATE gateway_calls SET model_id=(SELECT id FROM models WHERE model_id=$1),model_name='Old display name' WHERE request_id='observed'", publicModel)
	send("prior", narrow)
	check("prior", charged+` AND pricing_detail @> '{"prior_read_share":0.9,"recommended_read_tokens":90,"recommended_write_tokens":10,"recommended_yuan":0.000046}'
		AND student_credits=0.00506 AND (pricing_detail->>'recommended_yuan')::numeric
		BETWEEN (pricing_detail->>'min_yuan')::numeric AND (pricing_detail->>'max_yuan')::numeric`)
	exec("INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,2)", f.credential)
	send("group-two", `,"total_tokens":100002`)
	check("group-two", pending+"56")
	exec("INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,1)", f.credential)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
		VALUES($1,$2,'school','manual','teacher',2000000000,2000000000,2000000000,4000000000,$3)`, f.credential, upstreamModel, f.teacher)
	send("manual-over-bound", narrow)
	check("manual-over-bound", pending+"1")
	send("unknown-multiplier", `,"total_tokens":100002`)
	check("unknown-multiplier", pending+"51") // ceil(0.250012 * 100 * 2)
	send("reserve-cap", `,"total_tokens":5000002`)
	check("reserve-cap", pending+"851")
	var count, sum int
	err = f.db.QueryRowContext(ctx, `SELECT count(*),sum(unresolved_credit_milli) FROM gateway_calls WHERE beichen_state='pending_review'`).Scan(&count, &sum)
	if err != nil || count != 9 || sum != 1000 {
		t.Fatalf("pending rows=%d credits=%d, want 9 and 1000: %v", count, sum, err)
	}
}

func TestBeichenPendingNineReviewsPostgres(t *testing.T) {
	f := newPendingGateway(t)
	f.respond("")
	for i := 0; i < 8; i++ {
		if i == 7 {
			t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "40")
		}
		if w := f.request(fmt.Sprintf("previous-%d", i)); w.Code != http.StatusOK {
			t.Fatalf("previous request %d: HTTP %d %s", i, w.Code, w.Body)
		}
	}
	// QA's 8 existing reviews total 110. The ninth would add 1000 on its own.
	t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "10")
	f.respond(`,"total_tokens":5000002`)
	w := f.request("ninth")
	var count, total, ninth int
	err := f.db.QueryRowContext(f.ctx, `SELECT count(*),sum(unresolved_credit_milli),
		max(unresolved_credit_milli) FILTER (WHERE request_id='ninth') FROM gateway_calls
		WHERE beichen_state='pending_review'`).Scan(&count, &total, &ninth)
	if w.Code != http.StatusOK || err != nil || count != 9 || total != 1000 || ninth != 890 {
		t.Fatalf("HTTP %d, pending=%d total=%d ninth=%d: %v", w.Code, count, total, ninth, err)
	}
}

func TestBeichenPendingConcurrentFinalizePostgres(t *testing.T) {
	for _, tc := range []struct {
		name, counters string
		limit, smaller int
	}{
		{"known upper bound", `,"total_tokens":5000002`, 1000, 0},
		{"unknown upper bound", "", 15, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPendingGateway(t)
			f.db.SetMaxOpenConns(8)
			t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", fmt.Sprint(tc.limit))
			f.respond(tc.counters)
			upstream := f.gateway.client.Transport
			arrived, release := make(chan struct{}, 2), make(chan struct{})
			f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				arrived <- struct{}{}
				select {
				case <-release:
					return upstream.RoundTrip(r)
				case <-f.ctx.Done():
					return nil, f.ctx.Err()
				}
			})
			done := make(chan *httptest.ResponseRecorder, 2)
			for _, id := range []string{"first", "second"} {
				go func() { done <- f.request(id) }()
			}
			for range 2 {
				select {
				case <-arrived:
				case w := <-done:
					close(release)
					t.Fatalf("request ended before dispatch: %d %s", w.Code, w.Body)
				case <-f.ctx.Done():
					close(release)
					t.Fatal(f.ctx.Err())
				}
			}
			close(release)
			for range 2 {
				if w := <-done; w.Code != http.StatusOK {
					t.Fatalf("HTTP %d %s", w.Code, w.Body)
				}
			}
			var sum, smaller, count int
			err := f.db.QueryRowContext(f.ctx, `SELECT count(*),sum(unresolved_credit_milli),min(unresolved_credit_milli)
				FROM gateway_calls WHERE beichen_state='pending_review'`).Scan(&count, &sum, &smaller)
			if err != nil || count != 2 || sum != tc.limit || smaller != tc.smaller {
				t.Fatalf("after concurrent finalize: rows=%d total=%d smaller=%d err=%v", count, sum, smaller, err)
			}
		})
	}
}

func TestBeichenPendingAdmissionPostgres(t *testing.T) {
	f := newPendingGateway(t)
	f.respond(`,"total_tokens":102`)
	checkHTTP := func(id string, status int, code string) {
		t.Helper()
		w := f.request(id)
		if w.Code != status || w.Header().Get("X-Nebula-Error-Code") != code {
			t.Fatalf("%s: HTTP %d %s, want %d %s", id, w.Code, w.Body, status, code)
		}
	}
	t.Setenv("NEBULA_UNRESOLVED_EXPOSURE", "0")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "1")
	for i := 0; i < 20; i++ {
		checkHTTP(fmt.Sprintf("old-%d", i), 200, "")
	}
	checkHTTP("rollback", 429, usage.PendingReviewLimit)
	t.Setenv("NEBULA_UNRESOLVED_EXPOSURE", "1")
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	checkHTTP("new", 200, "")
	var count, sum int
	err := f.db.QueryRowContext(f.ctx, `SELECT count(*),sum(unresolved_credit_milli) FROM gateway_calls WHERE beichen_state='pending_review'`).Scan(&count, &sum)
	if err != nil || count != 21 || sum != 201 {
		t.Fatalf("after switching policies: rows=%d total=%d err=%v", count, sum, err)
	}
	f.exec(t, "UPDATE gateway_calls SET unresolved_credit_milli=unresolved_credit_milli+789 WHERE request_id='new'")
	checkHTTP("beichen", 429, usage.UnresolvedExposureLimit)
	f.exec(t, "UPDATE providers SET base_url='https://fixture.example.test'")
	checkHTTP("other-provider", 200, "")
}

func TestBeichenPendingNearBoundPostgres(t *testing.T) {
	f := newPendingGateway(t)
	f.respond(`,"total_tokens":102`)
	// 50 reads and 50 writes at these manual rates give exactly 0.02882
	// credits when delta=0. One unit of delta changes the price by 1.1e-12.
	for _, tc := range []struct {
		name         string
		delta        int
		state, debit string
	}{
		{"below", -1, "charged", "0.0288199999989"},
		{"equal", 0, "charged", "0.02882"},
		{"inside", 909, "charged", "0.02882"},
		{"outside", 910, "pending_review", ""},
	} {
		f.exec(t, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
			VALUES($1,$2,'school','manual','teacher',$3,200000000,200000000,316000000,$4)`, f.credential, upstreamModel, 200000000+tc.delta, f.teacher)
		w := f.request(tc.name)
		var ok bool
		err := f.db.QueryRowContext(f.ctx, `SELECT beichen_state=$2 AND COALESCE(trim_scale(student_credits)::text,'')=$3
			AND coalesce(pricing_detail @> '{"auto_settled":true}',false)=($2='charged')
			FROM gateway_calls WHERE request_id=$1`, tc.name, tc.state, tc.debit).Scan(&ok)
		if w.Code != 200 || err != nil || !ok {
			t.Fatalf("%s: HTTP %d, want %s %s: %v", tc.name, w.Code, tc.state, tc.debit, err)
		}
	}
	f.exec(t, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
		VALUES($1,$2,'school','manual','teacher',321078432,321078432,321078432,321078432,$3)`, f.credential, upstreamModel, f.teacher)
	w := f.request("unique")
	var ok bool
	err := f.db.QueryRowContext(f.ctx, `SELECT student_credits=0.03602500007040 AND pricing_detail->>'amount_source'='estimated_unique_amount'
		AND NOT pricing_detail ? 'auto_settled' FROM gateway_calls WHERE request_id='unique'`).Scan(&ok)
	if w.Code != 200 || err != nil || !ok {
		t.Fatalf("unique teacher price: HTTP %d, match=%t: %v", w.Code, ok, err)
	}
	var matches bool
	err = f.db.QueryRowContext(f.ctx, `SELECT charged_milli+charged_milli_remainder=(0.0288199999989+2*0.02882+0.03602500007040)*1000
		FROM project_month_credit_buckets`).Scan(&matches)
	if err != nil || !matches {
		t.Fatalf("monthly debit mismatch: %v", err)
	}
}
