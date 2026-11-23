package dataplane

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenHTTPAttemptCancellation(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprintf("connected=%t", connected), func(t *testing.T) {
			client, cancelClient := context.WithCancel(context.Background())
			defer cancelClient()
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(client)
			grace := time.Hour
			if connected {
				grace = 25 * time.Millisecond
			}
			r, _, finish := beichenHTTPAttempt(r, grace)
			defer finish()
			if connected {
				httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{})
				time.Sleep(2 * grace)
				if r.Context().Err() != nil {
					t.Fatal("grace began before client disconnect")
				}
			}
			at := time.Now()
			cancelClient()
			select {
			case <-r.Context().Done():
				if connected && time.Since(at) < grace {
					t.Fatal("sent attempt canceled early")
				}
			case <-time.After(time.Second):
				t.Fatal("attempt not canceled")
			}
		})
	}
}

func TestBeichenHTTPStreamSelection(t *testing.T) {
	for _, tc := range []struct {
		path, body, accept string
		grace              bool
	}{
		{"/v1/responses", `{}`, "application/json", true},
		{"/v1/responses", `{"stream":false}`, "application/json, text/event-stream", true},
		{"/v1/messages", `{"stream":false}`, "", true},
		{"/v1/chat/completions", `{"stream":true}`, "", false},
		{"/v1/audio/speech", `{"stream_format":"sse"}`, "", false},
		{"/v1/responses", `{}`, "text/event-stream", false},
		{"/v1beta/models/m:streamGenerateContent", `{}`, "", false},
		{"/v1beta/models/m:generateContent?alt=sse", `{}`, "", false},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.path, nil)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", tc.accept)
		if got := beichenNonStreamingRequest(r, &replayBody{memory: []byte(tc.body)}); got != tc.grace {
			t.Errorf("grace for %s %s Accept=%q: got %t", tc.path, tc.body, tc.accept, got)
		}
	}
}

type disconnectWriter struct {
	*httptest.ResponseRecorder
	headers, writes int
	failWrite       bool
}

func (w *disconnectWriter) WriteHeader(status int) {
	w.headers++
	w.ResponseRecorder.WriteHeader(status)
}

func (w *disconnectWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestBeichenHTTPDisconnectLedger(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	f := newBeichenGatewayFixture(t, ctx)
	if _, err := f.db.ExecContext(ctx, "INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,0.1)", f.credential); err != nil {
		t.Fatal(err)
	}
	const fields = `"usage":{"input_tokens":4415,"output_tokens":2326,"total_tokens":6741,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`
	const full = `{"model":"fixture-g3","status":"completed",` + fields + `}`
	const missing = `{"model":"fixture-g3","status":"completed"}`
	var charged int64
	for _, tc := range []struct {
		name, cancelAt, body       string
		waitForTimeout, wantCharge bool
	}{
		{"live", "", full, false, true},
		{"late-headers", "headers", full, false, true},
		{"late-body", "body", full, false, true},
		{"missing-usage", "body", missing, false, false},
		{"broken-json", "body", strings.TrimSuffix(full, "}"), false, false},
		{"nonterminal", "body", `{"model":"fixture-g3","status":"in_progress",` + fields + `}`, false, false},
		{"over-limit", "body", full + strings.Repeat(" ", maxHTTPMetricsBody), false, false},
		{"deadline-headers", "headers", full, true, false},
		{"deadline-body", "body", full, true, false},
		{"downstream-write-error", "", full, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.gateway.disconnectGrace = beichenDisconnectGrace
			if tc.waitForTimeout {
				f.gateway.disconnectGrace = 160 * time.Millisecond
			}
			clientCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			receipt := uuid.New()
			var sends atomic.Int32
			activeID := make(chan uuid.UUID, 1)
			firstRead := make(chan struct{})
			upstreamDone := make(chan struct{})
			var disconnectAt time.Time
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sends.Add(1) != 1 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				defer close(upstreamDone)
				_, _ = io.Copy(io.Discard, r.Body)
				// Capture the original reserved call before allowing the disconnect.
				var id uuid.UUID
				err := f.db.QueryRowContext(ctx, `SELECT id FROM gateway_calls WHERE request_id=$1
					AND beichen_state='reserved' AND lease_expires_at>now()`, tc.name).Scan(&id)
				if err != nil {
					t.Errorf("in-flight call/lease: %v", err)
				}
				activeID <- id
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(beichenReceiptHeader, receipt.String())
				body := tc.body
				if tc.cancelAt == "body" {
					_, _ = io.WriteString(w, body[:1])
					w.(http.Flusher).Flush()
					body = body[1:]
					select {
					case <-firstRead:
					case <-ctx.Done():
						t.Error("gateway did not start reading the upstream body")
						return
					}
				}
				if tc.cancelAt != "" {
					disconnectAt = time.Now()
					cancel()
					if tc.waitForTimeout {
						select {
						case <-r.Context().Done():
						case <-time.After(2 * time.Second):
							t.Error("gateway did not close the blocked upstream")
						}
						return
					}
					select {
					case <-r.Context().Done():
						t.Error("upstream canceled before delayed JSON")
						return
					case <-time.After(20 * time.Millisecond):
					}
				}
				_, _ = io.WriteString(w, body)
			}))
			defer up.Close()
			addr, err := url.Parse(up.URL)
			if err != nil {
				t.Fatal(err)
			}
			f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				req := r.Clone(r.Context())
				req.URL.Scheme, req.URL.Host, req.Host = addr.Scheme, addr.Host, addr.Host
				resp, err := up.Client().Transport.RoundTrip(req)
				if err == nil && tc.cancelAt == "body" {
					body := resp.Body
					resp.Body = struct {
						io.Reader
						io.Closer
					}{beichenReadOnce(func() io.Reader {
						close(firstRead)
						return body
					}), body}
				}
				return resp, err
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"fixture-public","input":"hello"}`)).
				WithContext(usage.WithRequestID(clientCtx, tc.name))
			req.Header.Set("Authorization", "Bearer "+studentKey)
			req.Header.Set("Content-Type", "application/json")
			w := &disconnectWriter{ResponseRecorder: httptest.NewRecorder(), failWrite: tc.name == "downstream-write-error"}
			f.gateway.ServeHTTP(w, req)
			if sends.Load() != 1 {
				t.Fatalf("upstream dispatched %d times (HTTP %d)", sends.Load(), w.Code)
			}
			<-upstreamDone
			if tc.waitForTimeout && time.Since(disconnectAt) < f.gateway.disconnectGrace {
				t.Fatal("attempt ended before the disconnect grace elapsed")
			}
			if tc.cancelAt != "" && (w.headers != 0 || w.writes != 0) {
				t.Fatalf("disconnected client received %d headers and %d writes", w.headers, w.writes)
			}
			if tc.cancelAt == "" && (w.Code != 200 || w.headers != 1 || w.writes != 1 || (!w.failWrite && w.Body.String() != full)) {
				t.Fatalf("connected client: HTTP %d, headers=%d writes=%d", w.Code, w.headers, w.writes)
			}
			var state string
			var input, output, total sql.NullInt64
			var attempts, calls int
			var storedReceipt uuid.NullUUID
			var debitOK, leaseReleased bool
			err = f.db.QueryRowContext(ctx, `SELECT beichen_state,prompt_tokens,completion_tokens,total_tokens,
				beichen_receipt_response_id,lease_expires_at IS NULL,
				CASE WHEN $3 THEN beichen_credit_exact=0.3209 ELSE beichen_credit_exact IS NULL END,
				(SELECT count(*) FROM gateway_call_attempts WHERE call_id=c.id),
				(SELECT count(*) FROM gateway_calls WHERE request_id=$2)
				FROM gateway_calls c WHERE id=$1`, <-activeID, tc.name, tc.wantCharge).
				Scan(&state, &input, &output, &total, &storedReceipt, &leaseReleased, &debitOK, &attempts, &calls)
			if err != nil {
				t.Fatal(err)
			}
			wantReceipt := uuid.NullUUID{UUID: receipt, Valid: true}
			if tc.name == "deadline-headers" {
				wantReceipt = uuid.NullUUID{}
			}
			if !debitOK || !leaseReleased || calls != 1 || attempts != 1 || storedReceipt != wantReceipt {
				t.Fatalf("debit=%t released=%t calls=%d attempts=%d receipt=%v", debitOK, leaseReleased, calls, attempts, storedReceipt)
			}
			if tc.wantCharge {
				charged++
				if state != "charged" || !input.Valid || input.Int64 != 4415 || !output.Valid || output.Int64 != 2326 || !total.Valid || total.Int64 != 6741 {
					t.Fatalf("state=%s input=%v output=%v total=%v", state, input, output, total)
				}
			} else if state != "pending_review" || (tc.name != "nonterminal" && (input.Valid || output.Valid || total.Valid)) {
				t.Fatalf("state=%s input=%v output=%v total=%v", state, input, output, total)
			}
			assertBeichenGatewayMonth(t, ctx, f.db, tc.name, fmt.Sprintf("%d.%04d", charged*3209/10000, charged*3209%10000), charged)
		})
	}
}

func TestBeichenHTTPNonStreamingTerminalLedger(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	f := newBeichenGatewayFixture(t, ctx)
	if _, err := f.db.ExecContext(ctx, "INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,0.1)", f.credential); err != nil {
		t.Fatal(err)
	}
	const anthropicRequest = `{"model":"fixture-public","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`
	const responsesRequest = `{"model":"fixture-public","input":"hello"}`
	cases := []struct {
		name, adapter, path, request, responseType, response string
		cancel, wantCharge                                   bool
		wantInput, wantOutput, wantTotal                     int64
	}{
		{
			"anthropic-message-start-only", "anthropic", "/v1/messages", anthropicRequest, "application/json",
			`{"type":"message_start","model":"fixture-g3","message":{"id":"msg_start","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`,
			true, false, 5, 1, -1,
		},
		{
			"anthropic-message-stop", "anthropic", "/v1/messages", anthropicRequest, "application/json",
			`{"type":"message_stop","model":"fixture-g3","message":{"id":"msg_stop","stop_reason":"end_turn","usage":{"input_tokens":4415,"output_tokens":2326,"total_tokens":6741,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
			false, true, 4415, 2326, 6741,
		},
		{
			"unexpected-sse", "openai_responses", "/v1/responses", responsesRequest, "text/event-stream",
			`data: {"type":"response.created","response":{"id":"resp_sse"}}

`,
			true, false, -1, -1, -1,
		},
	}
	var chargedTotal int64
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.db.ExecContext(ctx, "UPDATE model_bindings SET adapter=$2 WHERE id=$1", f.binding, tc.adapter); err != nil {
				t.Fatal(err)
			}
			clientCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var sends atomic.Int32
			activeID := make(chan uuid.UUID, 1)
			upstreamDone := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sends.Add(1) != 1 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				defer close(upstreamDone)
				_, _ = io.Copy(io.Discard, r.Body)
				var id uuid.UUID
				if err := f.db.QueryRowContext(ctx, `SELECT id FROM gateway_calls WHERE request_id=$1
					AND beichen_state='reserved' AND lease_expires_at>now()`, tc.name).Scan(&id); err != nil {
					t.Errorf("in-flight call/lease: %v", err)
				}
				activeID <- id
				if tc.cancel {
					cancel()
					select {
					case <-r.Context().Done():
						t.Error("upstream canceled before delayed response")
						return
					case <-time.After(20 * time.Millisecond):
					}
				}
				w.Header().Set("Content-Type", tc.responseType)
				w.Header().Set(beichenReceiptHeader, uuid.New().String())
				_, _ = io.WriteString(w, tc.response)
				if tc.responseType == "text/event-stream" {
					w.(http.Flusher).Flush()
				}
			}))
			defer up.Close()
			addr, err := url.Parse(up.URL)
			if err != nil {
				t.Fatal(err)
			}
			f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				req := r.Clone(r.Context())
				req.URL.Scheme, req.URL.Host, req.Host = addr.Scheme, addr.Host, addr.Host
				return up.Client().Transport.RoundTrip(req)
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request)).
				WithContext(usage.WithRequestID(clientCtx, tc.name))
			req.Header.Set("Authorization", "Bearer "+studentKey)
			req.Header.Set("Content-Type", "application/json")
			w := &disconnectWriter{ResponseRecorder: httptest.NewRecorder()}
			f.gateway.ServeHTTP(w, req)
			if sends.Load() != 1 {
				t.Fatalf("upstream dispatched %d times", sends.Load())
			}
			<-upstreamDone
			if tc.cancel && (w.headers != 0 || w.writes != 0) {
				t.Fatalf("disconnected client received %d headers and %d writes", w.headers, w.writes)
			}
			var state string
			var reviewReason, usageState string
			var input, output, total sql.NullInt64
			var attempts, calls int
			var charged bool
			var leaseReleased bool
			err = f.db.QueryRowContext(ctx, `SELECT beichen_state,COALESCE(beichen_review_reason,''),
				COALESCE(usage_state,''),prompt_tokens,completion_tokens,total_tokens,
				beichen_credit_exact IS NOT NULL,lease_expires_at IS NULL,
				(SELECT count(*) FROM gateway_call_attempts WHERE call_id=c.id),
				(SELECT count(*) FROM gateway_calls WHERE request_id=$2)
				FROM gateway_calls c WHERE id=$1`, <-activeID, tc.name).
				Scan(&state, &reviewReason, &usageState, &input, &output, &total, &charged, &leaseReleased, &attempts, &calls)
			if err != nil {
				t.Fatal(err)
			}
			wantUsageState := "interrupted"
			if tc.wantCharge {
				wantUsageState = "reported"
			}
			if !leaseReleased || calls != 1 || attempts != 1 || charged != tc.wantCharge || usageState != wantUsageState {
				t.Fatalf("released=%t calls=%d attempts=%d charged=%t usage=%s", leaseReleased, calls, attempts, charged, usageState)
			}
			if tc.wantCharge {
				chargedTotal++
				if state != "charged" || !input.Valid || input.Int64 != tc.wantInput ||
					!output.Valid || output.Int64 != tc.wantOutput || !total.Valid || total.Int64 != tc.wantTotal || reviewReason != "" {
					t.Fatalf("state=%s review=%q input=%v output=%v total=%v", state, reviewReason, input, output, total)
				}
			} else if state != "pending_review" {
				t.Fatalf("state=%s, want pending_review", state)
			}
			if tc.name == "anthropic-message-start-only" && reviewReason != "missing_terminal_usage" {
				t.Fatalf("review reason=%q, want missing_terminal_usage", reviewReason)
			}
			if !tc.wantCharge && !tc.cancel && (input.Valid || output.Valid || total.Valid) {
				t.Fatalf("unexpected retained usage: %v %v %v", input, output, total)
			}
			if tc.cancel && (input.Valid != (tc.wantInput >= 0) || (input.Valid && input.Int64 != tc.wantInput)) {
				t.Fatalf("expected partial input evidence: %v", input)
			}
			if tc.cancel && (output.Valid != (tc.wantOutput >= 0) || (output.Valid && output.Int64 != tc.wantOutput)) {
				t.Fatalf("expected partial output evidence: %v", output)
			}
			assertBeichenGatewayMonth(t, ctx, f.db, tc.name,
				fmt.Sprintf("%d.%04d", chargedTotal*3209/10000, chargedTotal*3209%10000), chargedTotal)
		})
	}
}
