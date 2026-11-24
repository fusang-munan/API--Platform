package dataplane

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type reviewLedger struct{ state, review, why, credits, tier, write, child5, child1, known, priceReason string }

func reviewRead(t *testing.T, f pendingGateway, id string) reviewLedger {
	t.Helper()
	var r reviewLedger
	err := f.db.QueryRowContext(f.ctx, `SELECT beichen_state,coalesce(beichen_review_reason,''), coalesce(usage_reason,''),
        coalesce(trim_scale(student_credits)::text,'NULL'), coalesce(price_tier,''),coalesce(cache_write_tokens::text,'NULL'),
        coalesce(pricing_detail->>'cache_write_5m_tokens','NULL'),coalesce(pricing_detail->>'cache_write_1h_tokens','NULL'),
        coalesce(pricing_detail->>'known_input_tokens','NULL'),coalesce(pricing_detail->>'reason','')
        FROM gateway_calls WHERE request_id=$1`, id).Scan(&r.state, &r.review, &r.why, &r.credits, &r.tier, &r.write, &r.child5, &r.child1, &r.known, &r.priceReason)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reviewAge(t *testing.T, f pendingGateway, id string) {
	t.Helper()
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-interval '10 minutes' WHERE request_id=$1`, id)
}

func reviewSocket(t *testing.T, f pendingGateway, id, event string) {
	t.Helper()
	upstreamResult := make(chan error, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			upstreamResult <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var create struct{ Type, Model string }
		if err = c.ReadJSON(&create); err == nil && (create.Type != "response.create" || create.Model != upstreamModel) {
			err = fmt.Errorf("unexpected dispatch type=%s model=%s", create.Type, create.Model)
		}
		if err == nil {
			err = c.WriteMessage(websocket.TextMessage, []byte(event))
		}
		upstreamResult <- err
	}))
	defer upstream.Close()
	useBeichenWebSocketFixture(f.gateway, upstream)
	finished := make(chan struct{})
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		f.gateway.ServeHTTP(w, r.WithContext(usage.WithRequestID(r.Context(), id)))
	}))
	defer front.Close()
	c, _, err := websocket.DefaultDialer.DialContext(f.ctx, "ws"+strings.TrimPrefix(front.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + studentKey}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err = c.WriteJSON(map[string]string{"type": "response.create", "model": publicModel, "input": "fixture"}); err != nil {
		t.Fatal(err)
	}
	_, frame, err := c.ReadMessage()
	if err != nil || string(frame) != event {
		t.Fatalf("expected initial frame: %s %v", frame, err)
	}
	select {
	case err = <-upstreamResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case <-finished:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
}

func reviewHTTP(t *testing.T, f pendingGateway, id, contentType, reply string, stream bool) {
	t.Helper()
	f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"fixture","stream":%t}`, publicModel, stream)))
	req = req.WithContext(usage.WithRequestID(f.ctx, id))
	req.Header.Set("Authorization", "Bearer "+studentKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.gateway.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("unexpected HTTP %d %s", w.Code, w.Body)
	}
}

func TestReworkTransports(t *testing.T) {
	for _, path := range []string{"HTTP", "SSE", "WS"} {
		for _, s := range []struct{ name, fields, expected string }{
			{"match", fmt.Sprintf(`"model":%q,`, upstreamModel), "missing_terminal_usage"},
			{"mismatch", `"model":"different-model-without-price",`, "upstream_model_mismatch"},
			{"absent", "", "upstream_model_unconfirmed"},
			{"unsupported", fmt.Sprintf(`"model":%q,"service_tier":"auto",`, upstreamModel), "unsupported_text_pricing"},
			{"aliases", fmt.Sprintf(`"model":%q,`, upstreamModel), "incomplete_token_breakdown"},
		} {
			t.Run(path+"/"+s.name, func(t *testing.T) {
				f := reworkGateway(t)
				f.exec(t, "UPDATE model_bindings SET adapter='openai_responses' WHERE id=$1", f.binding)
				if reworkCount(t, f, "SELECT count(*) FROM beichen_price_versions WHERE model='different-model-without-price'") != 0 {
					t.Fatal("unexpected quote for substituted model")
				}
				id := "qa-" + path + "-" + s.name
				resp := fmt.Sprintf(`{"id":"resp_probe",%s"status":"in_progress","usage":{"input_tokens":11,"output_tokens":2,"prompt_cache_miss_tokens":11,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`, s.fields)
				if s.name == "aliases" {
					resp = strings.Replace(resp, `"input_tokens_details":`, `"prompt_cache_hit_tokens":1,"input_tokens_details":`, 1)
				}
				event := `{"type":"response.created","response":` + resp + `}`
				switch path {
				case "HTTP":
					reviewHTTP(t, f, id, "application/json", resp, false)
				case "SSE":
					reviewHTTP(t, f, id, "text/event-stream", "event: response.created\ndata: "+event+"\n\n", true)
				case "WS":
					reviewSocket(t, f, id, event)
				}
				before := reviewRead(t, f, id)
				reviewAge(t, f, id)
				booked, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
				if err != nil {
					t.Fatal(err)
				}
				after := reviewRead(t, f, id)
				t.Logf("before=%s/%s usage=%s -> resolved=%d errors=%d after=%s credits=%s", before.state, before.review, before.why, booked.Resolved, booked.Errors, after.state, after.credits)
				if before.state != "pending_review" || before.credits != "NULL" || booked.Errors != 0 {
					t.Fatal("unexpected baseline or database error")
				}
				if s.name == "match" {
					if before.review != s.expected || after.state != "charged" || after.credits != "0.00462" || booked.Resolved != 1 {
						t.Fatal("matching priced incomplete call did not settle as expected")
					}
				} else if before.review != s.expected || after.review != s.expected || after.state != "pending_review" || after.credits != "NULL" || booked.Resolved != 0 {
					t.Errorf("expected review=%s to survive interruption and settlement", s.expected)
				}
			})
		}
	}
}

func TestReworkPreview(t *testing.T) {
	f := reworkGateway(t)
	preview := func(limit int) usage.PendingBacklogReport {
		t.Helper()
		p, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, limit, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	add := func(id, counters string) { reworkStart(t, f, id, upstreamModel, counters); reviewAge(t, f, id) }
	add("good", `"input_tokens":1`)
	reworkStart(t, f, "young", upstreamModel, `"input_tokens":1`)
	first := preview(1)
	add("parser-conflict", `"input_tokens":11,"output_tokens":2,"cache_creation_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":1}`)
	second := preview(1)
	_, applyErr := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, 1, 0, first.PreviewToken)
	t.Logf("off-page blocked addition: total=%d->%d terminal=%d->%d page=%d token_changed=%t old_token_rejected=%t", first.BacklogTotal, second.BacklogTotal, first.TerminalMissing, second.TerminalMissing, second.Total, first.PreviewToken != second.PreviewToken, domain.ErrorCode(applyErr) == domain.CodePendingAuditChanged)
	if first.Total != 1 || second.Total != 1 || first.Rows[0].ID != second.Rows[0].ID || first.TerminalMissing != 2 || second.TerminalMissing != 2 || second.BacklogTotal != 3 || first.PreviewToken == second.PreviewToken || domain.ErrorCode(applyErr) != domain.CodePendingAuditChanged {
		t.Fatal("all-pending token or known conflict filter failed")
	}
	reworkStart(t, f, "wrong-model", "different-model-without-price", `"input_tokens":1`)
	reviewAge(t, f, "wrong-model")
	add("raw-conflict", `"input_tokens":11,"output_tokens":2,"total_tokens":4`)
	p := preview(20)
	a, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	if a.Resolved != 1 || a.Errors != 0 || reviewRead(t, f, "good").state != "charged" || reviewRead(t, f, "young").state != "pending_review" {
		t.Fatal("grace or normal row failed", a)
	}
	q := preview(20)
	reviewAge(t, f, "young")
	b, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	c, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	last := preview(20)
	var notice string
	var parserNote string
	for _, x := range last.Rows {
		var id string
		if err := f.db.QueryRowContext(f.ctx, "SELECT request_id FROM gateway_calls WHERE id=$1", x.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id == "raw-conflict" {
			notice = x.Note
		}
		if id == "parser-conflict" {
			parserNote = x.Note
		}
	}
	raw := reviewRead(t, f, "raw-conflict")
	t.Logf("terminal before=%d after first=%d after all eligible=%d; scans=%+v/%+v/%+v; remaining=%d; raw reason=%s/%s state=%s credits=%s note=%q; labelled note=%q", p.TerminalMissing, q.TerminalMissing, last.TerminalMissing, a, b, c, last.BacklogTotal, raw.review, raw.why, raw.state, raw.credits, notice, parserNote)
	if b.Resolved != 1 || c.Resolved != 0 || b.Errors+c.Errors != 0 || last.BacklogTotal != 3 || raw.state != "pending_review" || raw.credits != "NULL" || !strings.Contains(parserNote, "人工待核") {
		t.Fatal("wrong settled or remaining accounts")
	}
	if p.TerminalMissing != 2 || q.TerminalMissing != 1 || last.TerminalMissing != 0 || !strings.Contains(notice, "人工待核") {
		t.Error("mathematically contradictory row still advertised as processable")
	}
}

func TestReworkPartialTier(t *testing.T) {
	f := reworkGateway(t)
	f.exec(t, `WITH m AS (INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,1) RETURNING id,credential_id,effective_at)
        INSERT INTO beichen_panel_snapshots (credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
        SELECT c.id,m.effective_at,c.updated_at,1,now()+interval '1 hour',m.id,
        jsonb_build_object($2::text,'{"billing_mode":"token","intervals":[{"min_tokens":0,"max_tokens":100,"input_price":0.00002,"output_price":0.00003,"cache_read_price":0.00002,"cache_write_price":0.00002},{"min_tokens":100,"input_price":0.0000001,"output_price":0.0000001,"cache_read_price":0.0000001,"cache_write_price":0.0000001}]}'::jsonb),sha256(c.credential_ciphertext)
        FROM m JOIN provider_credentials c ON c.id=m.credential_id`, f.credential, upstreamModel)
	for _, x := range []struct{ id, child string }{
		{"partial", `"ephemeral_1h_input_tokens":0`},
		{"complete", `"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":100`},
	} {
		reworkStart(t, f, x.id, upstreamModel, `"input_tokens":11,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation":{`+x.child+`}`)
		r := reviewRead(t, f, x.id)
		t.Logf("%s before: state=%s review=%s usage=%s raw_write=%s child5=%s child1=%s", x.id, r.state, r.review, r.why, r.write, r.child5, r.child1)
		reviewAge(t, f, x.id)
	}
	reworkRun(t, f, 0, 2)
	p, full := reviewRead(t, f, "partial"), reviewRead(t, f, "complete")
	t.Logf("partial known=%s tier=%q credits=%s reason=%q; complete known=%s tier=%q credits=%s reason=%q", p.known, p.tier, p.credits, p.priceReason, full.known, full.tier, full.credits, full.priceReason)
	if full.state != "charged" || full.credits == "0" || full.credits == "NULL" || full.tier == "" || full.known != "111" {
		t.Fatal("complete observation reference not priced")
	}
	if p.state != "charged" || p.credits != "0" || p.priceReason != "unknown_input_price_tier" {
		t.Errorf("one cache duration is missing and can cross into a cheaper tier")
	}
}

func TestReworkFullWindow(t *testing.T) {
	f := reworkGateway(t)
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "1")
	t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "10000")
	for i := 0; i < 50; i++ {
		reworkStart(t, f, fmt.Sprintf("bad-%02d", i), upstreamModel, `"input_tokens":1`)
	}
	reworkStart(t, f, "later-good", upstreamModel, `"input_tokens":1`)
	f.exec(t, `UPDATE gateway_calls SET finalized_at=now()-CASE WHEN request_id='later-good' THEN interval '10 minutes' ELSE interval '20 minutes' END`)
	f.exec(t, `CREATE FUNCTION probe_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
        IF left(NEW.request_id,4)='bad-' AND NEW.beichen_state='charged' THEN RAISE EXCEPTION 'qa local test fault'; END IF; RETURN NEW; END $$;
        CREATE TRIGGER probe_reject BEFORE UPDATE ON gateway_calls FOR EACH ROW EXECUTE FUNCTION probe_reject()`)
	sawError := false
	for i := 1; i <= 2; i++ {
		r, err := usage.ResolveStaleTerminalPending(f.ctx, f.db, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("full window scan %d resolved=%d errors=%d later=%s", i, r.Resolved, r.Errors, reviewRead(t, f, "later-good").state)
		if r.Errors > 0 {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("failure injection did not run")
	}
	if reviewRead(t, f, "later-good").state != "charged" {
		t.Error("old 50 faults still block healthy row 51 on repeat")
	}
}

// Compare the arithmetic guard when the original response has all operands and
// when output was omitted. Only the latter can explain a larger total.
func TestReworkReportedTotals(t *testing.T) {
	f := reworkGateway(t)
	f.exec(t, "UPDATE model_bindings SET adapter='openai_responses' WHERE id=$1", f.binding)
	for _, s := range []struct {
		id, output string
		total      int
	}{
		{"consistent", `"output_tokens":2,`, 13},
		{"smaller", `"output_tokens":2,`, 4},
		{"larger", `"output_tokens":2,`, 14},
		{"no-output", "", 14},
	} {
		resp := fmt.Sprintf(`{"id":"resp_sum","model":%q,"status":"in_progress","usage":{"input_tokens":11,%s"total_tokens":%d,"prompt_cache_miss_tokens":11,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`, upstreamModel, s.output, s.total)
		reviewHTTP(t, f, s.id, "application/json", resp, false)
		reviewAge(t, f, s.id)
	}
	report, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved=%d errors=%d", report.Resolved, report.Errors)
	for _, id := range []string{"consistent", "smaller", "larger", "no-output"} {
		r := reviewRead(t, f, id)
		t.Logf("%s state=%s review=%s usage=%s credits=%s known=%s", id, r.state, r.review, r.why, r.credits, r.known)
		switch id {
		case "smaller", "larger":
			if r.state != "pending_review" || r.credits != "NULL" {
				t.Errorf("%s explicit contradiction was booked", id)
			}
		case "consistent":
			if r.state != "charged" || r.credits != "0.00462" {
				t.Error("consistent control failed")
			}
		case "no-output":
			if r.state != "charged" || r.credits != "0.00242" {
				t.Error("unreported output control failed")
			}
		}
	}
}

func TestReworkReportedCacheDuration(t *testing.T) {
	f := reworkGateway(t)
	f.exec(t, `WITH m AS (INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,1) RETURNING id,credential_id,effective_at)
        INSERT INTO beichen_panel_snapshots (credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
        SELECT c.id,m.effective_at,c.updated_at,1,now()+interval '1 hour',m.id,
        jsonb_build_object($2::text,'{"billing_mode":"token","input_price":0.000002,"output_price":0.000010,"cache_read_price":0.000001,"cache_write_price":0.0000025,"cache_write_1h_price":0.0000125}'::jsonb),sha256(c.credential_ciphertext)
        FROM m JOIN provider_credentials c ON c.id=m.credential_id`, f.credential, upstreamModel)
	for _, s := range []struct{ id, requested, observed string }{
		{"5m-control", "5m", "5m"}, {"1h-control", "1h", "1h"}, {"conflicting-duration", "1h", "5m"},
	} {
		resp := fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_cache","model":%q,"usage":{"cache_creation":{"ephemeral_%s_input_tokens":4}}}}`, upstreamModel, s.observed)
		f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
			defer r.Body.Close()
			httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(resp))}, nil
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":2,"messages":[],"cache_control":{"type":"ephemeral","ttl":%q}}`, publicModel, s.requested)))
		req = req.WithContext(usage.WithRequestID(f.ctx, s.id))
		req.Header.Set("Authorization", "Bearer "+studentKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.gateway.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != resp {
			t.Fatal("gateway did not forward fixture", w.Code, w.Body)
		}
		reviewAge(t, f, s.id)
	}
	report, err := usage.ResolveProviderTerminalPending(f.ctx, f.db, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved=%d errors=%d", report.Resolved, report.Errors)
	for _, s := range []struct{ id, upper string }{
		{"5m-control", "0.0011"}, {"1h-control", "0.0055"}, {"conflicting-duration", "0.0011"},
	} {
		r := reviewRead(t, f, s.id)
		var requestTTL, pricedTTL string
		if err := f.db.QueryRowContext(f.ctx, `SELECT coalesce(cache_ttl,''),coalesce(pricing_detail->>'cache_ttl','') FROM gateway_calls WHERE request_id=$1`, s.id).Scan(&requestTTL, &pricedTTL); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s request_ttl=%s priced_ttl=%s observed5=%s observed1=%s known=%s state=%s credits=%s", s.id, requestTTL, pricedTTL, r.child5, r.child1, r.known, r.state, r.credits)
		if s.id == "conflicting-duration" {
			if reworkCount(t, f, "SELECT count(*) FROM gateway_calls WHERE request_id=$1 AND student_credits>$2::numeric", s.id, s.upper) != 0 {
				t.Error("directly reported five-minute writes were charged above their dispatch price")
			}
		} else if r.state != "charged" || r.credits != s.upper {
			t.Errorf("%s matching duration control failed", s.id)
		}
	}
}
