package dataplane

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenChatOnlyRequestsMissingStreamUsage(t *testing.T) {
	cases := []struct {
		name, base, path, body, wantOptions string
	}{
		{"auto", "https://api.beichenapi.com/v1", "/v1/chat/completions", `{"stream":true}`, `{"include_usage":true}`},
		{"merge", "https://api.beichenapi.com", "/v1/chat/completions", `{"stream":true,"stream_options":{"other":2}}`, `{"include_usage":true,"other":2}`},
		{"false", "https://api.beichenapi.com", "/v1/chat/completions", `{"stream":true,"stream_options":{"include_usage":false}}`, `{"include_usage":false}`},
		{"null", "https://api.beichenapi.com", "/v1/chat/completions", `{"stream":true,"stream_options":null}`, "null"},
		{"ordinary", "https://api.beichenapi.com", "/v1/chat/completions", `{"stream":false}`, ""},
		{"legacy", "https://api.beichenapi.com", "/v1/completions", `{"stream":true}`, ""},
		{"other host", "https://api.beichenapi.com.other.test", "/v1/chat/completions", `{"stream":true}`, ""},
		{"insecure", "http://api.beichenapi.com", "/v1/chat/completions", `{"stream":true}`, ""},
		{"other port", "https://api.beichenapi.com:8443", "/v1/chat/completions", `{"stream":true}`, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			replay, err := captureBody(io.NopCloser(strings.NewReader(test.body)), 4096)
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close()
			body, _, _, cleanup, err := rewriteUpstreamBody("application/json", replay, "upstream", modelbinding.AdapterOpenaiCompatible, test.base, test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			defer body.Close()
			raw, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(jsonObject(raw)["stream_options"]); got != test.wantOptions {
				t.Fatalf("stream_options = %q, want %q", got, test.wantOptions)
			}
		})
	}
}

func TestBeichenReceiptIDRequiresOneCanonicalResponseHeader(t *testing.T) {
	const canonical = "a83bb259-7457-4a42-a233-007661ce8c7f"
	for _, tc := range []struct {
		name   string
		header http.Header
		valid  bool
	}{
		{"one", http.Header{beichenReceiptHeader: {canonical}}, true},
		{"one lowercase header name", http.Header{strings.ToLower(beichenReceiptHeader): {canonical}}, true},
		{"missing", nil, false},
		{"repeated identical", http.Header{beichenReceiptHeader: {canonical, canonical}}, false},
		{"conflicting header names", http.Header{beichenReceiptHeader: {canonical}, strings.ToLower(beichenReceiptHeader): {uuid.NewString()}}, false},
		{"combined", http.Header{beichenReceiptHeader: {canonical + ", " + canonical}}, false},
		{"uppercase UUID", http.Header{beichenReceiptHeader: {strings.ToUpper(canonical)}}, false},
		{"compact UUID", http.Header{beichenReceiptHeader: {strings.ReplaceAll(canonical, "-", "")}}, false},
		{"wrapped UUID", http.Header{beichenReceiptHeader: {"{" + canonical + "}"}}, false},
		{"nil UUID", http.Header{beichenReceiptHeader: {uuid.Nil.String()}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := uuid.Nil
			if tc.valid {
				want = uuid.MustParse(canonical)
			}
			if got := beichenReceiptID(tc.header); got != want {
				t.Fatalf("receipt = %s, want %s", got, want)
			}
		})
	}
}

func TestBeichenUpstreamRequestStripsStudentReceipt(t *testing.T) {
	gateway, _, candidate, _ := newProxyUsageTest(t)
	candidate.Provider.BaseURL = "https://api.beichenapi.com"
	replay, err := captureBody(io.NopCloser(strings.NewReader(`{"model":"public"}`)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(beichenReceiptHeader, uuid.NewString())
	request.Header[strings.ToLower(beichenReceiptHeader)] = []string{uuid.NewString()}
	upstream, cleanup, err := gateway.upstreamRequest(request, replay, candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer upstream.Body.Close()
	for name := range upstream.Header {
		if strings.EqualFold(name, beichenReceiptHeader) {
			t.Fatal("student receipt was forwarded upstream")
		}
	}
	if request.Header.Get(beichenReceiptHeader) == "" {
		t.Fatal("original request header was mutated")
	}
}

func newProxyUsageTest(t *testing.T) (*Gateway, sqlmock.Sqlmock, route, usage.Reservation) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	candidate := route{
		Binding:  &ent.ModelBinding{ID: uuid.New(), Adapter: modelbinding.AdapterOpenaiCompatible, UpstreamModelName: "served-model"},
		Provider: &ent.Provider{ID: uuid.New(), Name: "Test Provider", BaseURL: "http://fake-upstream.test"}, CredentialID: uuid.New(),
	}
	return &Gateway{usage: usage.NewStore(db)}, mock, candidate, usage.Reservation{CallID: uuid.New()}
}

func expectSuccessfulProxyCharge(mock sqlmock.Sqlmock, candidate route, reservation usage.Reservation, sentAt driver.Value) {
	mock.ExpectBegin()
	expectLegacyLedgerLock(mock, reservation.CallID)
	mock.ExpectExec(`UPDATE gateway_calls SET billing_state=`).WithArgs(
		"charged", "succeeded", "", "", candidate.Provider.ID, candidate.Provider.Name,
		reservation.CallID, candidate.CredentialID, candidate.Binding.ID, candidate.Binding.UpstreamModelName, sentAt,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO monthly_usage_cube`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE monitored_inputs SET visible=true`).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectProxyReservationReleased(mock sqlmock.Sqlmock, candidate route, reservation usage.Reservation) {
	mock.ExpectBegin()
	expectLegacyLedgerLock(mock, reservation.CallID)
	mock.ExpectExec(`UPDATE gateway_calls SET billing_state=`).WithArgs(
		"failed", "upstream_failed", "stream", "Stream ended before output or completion.",
		candidate.Provider.ID, candidate.Provider.Name, reservation.CallID, candidate.CredentialID,
		candidate.Binding.ID, candidate.Binding.UpstreamModelName, nil,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE project_month_credit_buckets SET pending_milli=pending_milli-\$1,updated_at`).
		WithArgs(reservation.MultiplierMilli, reservation.ProjectID, reservation.Month).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE project_member_month_credit_buckets SET pending_milli=pending_milli-\$1,updated_at`).
		WithArgs(reservation.MultiplierMilli, reservation.ProjectID, reservation.UserID, reservation.Month).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE api_key_month_credit_buckets SET pending_milli=pending_milli-\$1,updated_at`).
		WithArgs(reservation.MultiplierMilli, reservation.APIKeyID, reservation.Month).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM monitored_inputs`).WithArgs(reservation.CallID).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectLegacyLedgerLock(mock sqlmock.Sqlmock, callID uuid.UUID) {
	mock.ExpectQuery("SELECT beichen_state IS NOT NULL.*FOR UPDATE").WithArgs(callID).
		WillReturnRows(sqlmock.NewRows([]string{"routed", "pending", "expired"}).AddRow(false, true, false))
}

func expectUsageWrite(mock sqlmock.Sqlmock, callID uuid.UUID, responseID string, prompt, completion, total driver.Value, state usage.UsageState) {
	mock.ExpectQuery(`SELECT turn_id,COALESCE.*FOR UPDATE`).WithArgs(callID).
		WillReturnRows(sqlmock.NewRows([]string{"turn_id", "prompt", "completion", "total"}).AddRow(nil, 0, 0, 0))
	mock.ExpectExec(`UPDATE gateway_calls SET upstream_response_id`).WithArgs(callID, responseID, prompt, completion, total,
		nil, nil, nil, nil, nil, nil, string(state)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestSSEForwardsFirstReadAndLongEventBeforeTail(t *testing.T) {
	gateway, mock, candidate, reservation := newProxyUsageTest(t)
	mock.ExpectExec(`WITH started AS .*UPDATE gateway_calls SET provider_id`).WithArgs(
		sqlmock.AnyArg(), reservation.CallID, candidate.Provider.ID, candidate.Binding.ID, candidate.Provider.Name,
		candidate.CredentialID, candidate.Binding.UpstreamModelName,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE gateway_calls SET sent_at`).WithArgs(reservation.CallID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectSuccessfulProxyCharge(mock, candidate, reservation, nil)
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectLegacyReportReview(mock, reservation.CallID)
	expectUsageWrite(mock, reservation.CallID, "chat_end", int64(0), int64(2), nil, usage.UsageReported)
	mock.ExpectExec(`UPDATE gateway_call_attempts SET status=`).WithArgs("succeeded", 200, "", "", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	first := "data: {\"choices\":["
	restOfFirst := "{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	longEvent := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 1<<20) + "\"}}]}\n\n"
	tail := "data: {\"id\":\"chat_end\",\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
	reader, writer := io.Pipe()
	defer writer.Close()
	gateway.client = &http.Client{Transport: responseTransport{response: &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader,
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.proxyHTTP(w, r, nil, []route{candidate}, &reservation)
	}))
	defer server.Close()
	t.Cleanup(func() { _ = reader.Close() })
	go func() { _, _ = io.WriteString(writer, first) }()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	defer response.Body.Close()
	for _, chunk := range []string{first, restOfFirst, longEvent, tail} {
		if chunk != first {
			go func() { _, _ = io.WriteString(writer, chunk) }()
		}
		got := make([]byte, len(chunk))
		if _, err := io.ReadFull(response.Body, got); err != nil || string(got) != chunk {
			_ = writer.Close()
			t.Fatalf("stream chunk of length %d corrupted or blocked: %v", len(chunk), err)
		}
	}
	_ = writer.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesSSEFlushesPreludeThenReleasesUnchargedReservation(t *testing.T) {
	for _, ending := range []string{"protocol failure", "upstream EOF", "client cancellation"} {
		t.Run(ending, func(t *testing.T) {
			gateway, mock, candidate, reservation := newProxyUsageTest(t)
			candidate.Binding.Adapter = modelbinding.AdapterOpenaiResponses
			reservation.MultiplierMilli = 1000
			reservation.Month = usage.Month(time.Now())
			mock.ExpectExec(`WITH started AS`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE gateway_calls SET sent_at`).WillReturnResult(sqlmock.NewResult(0, 1))
			expectProxyReservationReleased(mock, candidate, reservation)
			expectUsageWrite(mock, reservation.CallID, "resp_prepared", nil, nil, nil, usage.UsageInterrupted)
			mock.ExpectExec(`UPDATE gateway_call_attempts SET status=`).
				WithArgs("failed", 200, "stream", "Upstream stream did not complete.", sqlmock.AnyArg(), sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))

			const prelude = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_prepared\"}}\n\n"
			const failure = "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_prepared\",\"status\":\"failed\"}}\n\n"
			sentPrelude := make(chan struct{})
			upstreamDone := make(chan struct{})
			advance := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, prelude)
				w.(http.Flusher).Flush()
				close(sentPrelude)
				select {
				case <-r.Context().Done():
					return
				case <-advance:
				}
				if ending == "protocol failure" {
					_, _ = io.WriteString(w, failure)
					w.(http.Flusher).Flush()
					// The gateway must close the stream even if upstream keeps it open.
					<-r.Context().Done()
				}
			}))
			defer upstream.Close()
			candidate.Provider.BaseURL = upstream.URL
			gateway.client = upstream.Client()
			proxyDone := make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(proxyDone)
				gateway.proxyHTTP(w, r, nil, []route{candidate}, &reservation)
			}))
			defer proxy.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := proxy.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got := make([]byte, len(prelude))
			if _, err := io.ReadFull(response.Body, got); err != nil || response.StatusCode != http.StatusOK || string(got) != prelude {
				t.Fatalf("prelude was not flushed unchanged before settlement: HTTP %d, %q, %v", response.StatusCode, got, err)
			}
			<-sentPrelude
			if ending == "client cancellation" {
				cancel()
			} else {
				close(advance)
				tail, err := io.ReadAll(response.Body)
				want := ""
				if ending == "protocol failure" {
					want = failure
				}
				if err != nil || string(tail) != want {
					t.Fatalf("tail = %q, %v, want %q", tail, err, want)
				}
			}
			for _, done := range []<-chan struct{}{upstreamDone, proxyDone} {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("upstream or proxy context was not released")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type responseTransport struct{ response *http.Response }

func (t responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return t.response, nil
}

type upstreamSendWindow struct {
	dialReleasedAt    time.Time
	headersReleasedAt time.Time
}

type upstreamSendTimeArgument struct {
	window *atomic.Pointer[upstreamSendWindow]
}

func (arg upstreamSendTimeArgument) Match(value driver.Value) bool {
	sentAt, ok := value.(time.Time)
	window := arg.window.Load()
	return ok && window != nil && !sentAt.Before(window.dialReleasedAt) && !sentAt.After(window.headersReleasedAt)
}

func TestGatewayFinalizesAtActualRetrySendTime(t *testing.T) {
	for _, sse := range []bool{false, true} {
		name := "HTTP JSON"
		if sse {
			name = "HTTP SSE"
		}
		t.Run(name, func(t *testing.T) {
			gateway, mock, candidate, reservation := newProxyUsageTest(t)
			var window atomic.Pointer[upstreamSendWindow]
			expectStartedAndSent := func() {
				mock.ExpectExec(`WITH started AS`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE gateway_calls SET sent_at=COALESCE\(sent_at,now\(\)\).*billing_state='pending'`).
					WithArgs(reservation.CallID).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			expectStartedAndSent()
			mock.ExpectExec(`UPDATE gateway_call_attempts SET status=`).
				WithArgs("failed", 429, "http", "HTTP 429", sqlmock.AnyArg(), sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			expectStartedAndSent()
			expectSuccessfulAttempt := func() {
				mock.ExpectExec(`UPDATE gateway_call_attempts SET status=`).
					WithArgs("succeeded", 200, "", "", sqlmock.AnyArg(), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if !sse {
				expectSuccessfulAttempt()
			}
			expectSuccessfulProxyCharge(mock, candidate, reservation, upstreamSendTimeArgument{window: &window})
			if sse {
				mock.ExpectCommit()
				mock.ExpectBegin()
				expectLegacyReportReview(mock, reservation.CallID)
			}
			expectUsageWrite(mock, reservation.CallID, "second", int64(0), nil, nil, usage.UsageReported)
			if sse {
				expectSuccessfulAttempt()
			}

			retryDialing := make(chan struct{})
			releaseRetryDial := make(chan struct{})
			retryReceived := make(chan struct{})
			releaseRetryHeaders := make(chan struct{})
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if requests.Add(1) == 1 {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				close(retryReceived)
				select {
				case <-r.Context().Done():
					return
				case <-releaseRetryHeaders:
				}
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"second\",\"usage\":{\"prompt_tokens\":0}}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"second","usage":{"prompt_tokens":0}}`)
			}))
			defer upstream.Close()
			candidate.Provider.BaseURL = upstream.URL
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var dials atomic.Int32
			dialer := net.Dialer{}
			transport := &http.Transport{
				DisableKeepAlives: true,
				DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
					if dials.Add(1) == 2 {
						close(retryDialing)
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case <-releaseRetryDial:
						}
					}
					return dialer.DialContext(dialCtx, network, address)
				},
			}
			defer transport.CloseIdleConnections()
			gateway.client = &http.Client{Transport: transport}
			wrote := make(chan struct{}, 2)
			trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					select {
					case wrote <- struct{}{}:
					case <-ctx.Done():
					}
				}
			}}
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(httptrace.WithClientTrace(ctx, trace))
			request.Header.Set("Content-Type", "application/json")
			replay, err := captureBody(io.NopCloser(strings.NewReader(`{"model":"example"}`)), 4096)
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close()
			w := httptest.NewRecorder()
			completed := make(chan struct{})
			go func() {
				defer close(completed)
				gateway.proxyHTTP(w, request, replay, []route{candidate}, &reservation)
			}()
			await := func(ch <-chan struct{}, stage string) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatalf("timed out waiting for %s", stage)
				}
			}
			await(wrote, "first attempt write")
			await(retryDialing, "retry dial")
			dialReleasedAt := time.Now()
			close(releaseRetryDial)
			await(wrote, "retry write")
			await(retryReceived, "retry received by upstream")
			window.Store(&upstreamSendWindow{dialReleasedAt: dialReleasedAt, headersReleasedAt: time.Now()})
			close(releaseRetryHeaders)
			await(completed, "gateway settlement")
			if w.Code != http.StatusOK || requests.Load() != 2 {
				t.Fatalf("HTTP status = %d, attempts = %d", w.Code, requests.Load())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectLegacyReportReview(mock sqlmock.Sqlmock, callID uuid.UUID) {
	mock.ExpectQuery("SELECT COALESCE\\(beichen_state='pending_review'.*FOR UPDATE").WithArgs(callID).
		WillReturnRows(sqlmock.NewRows([]string{"review"}).AddRow(false))
}

func TestHTTPUsageLimitDoesNotTruncateResponse(t *testing.T) {
	gateway, mock, candidate, reservation := newProxyUsageTest(t)
	expectSuccessfulProxyCharge(mock, candidate, reservation, nil)
	expectUsageWrite(mock, reservation.CallID, "", nil, nil, nil, usage.UsageMissing)
	body := strings.Repeat("x", maxHTTPMetricsBody+80) + "end of body"
	response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	gateway.proxyHTTPBody(w, request, response, candidate.Binding.Adapter, candidate.callRoute(), &reservation, true, false, "")
	if w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("response status %d or length %d differs from upstream", w.Code, w.Body.Len())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAndSSECanSettleAfterClientCancellation(t *testing.T) {
	for _, sse := range []bool{false, true} {
		label := "HTTP"
		if sse {
			label = "SSE"
		}
		t.Run(label, func(t *testing.T) {
			gateway, mock, candidate, reservation := newProxyUsageTest(t)
			expectSuccessfulProxyCharge(mock, candidate, reservation, nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(errorReader{})}
			w := httptest.NewRecorder()
			if sse {
				mock.ExpectCommit()
				mock.ExpectBegin()
				expectUsageWrite(mock, reservation.CallID, "", int64(0), nil, nil, usage.UsageInterrupted)
				body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}],\"usage\":{\"prompt_tokens\":0}}\n\n")
				observer := observeSSE(candidate.Binding.Adapter, body)
				gateway.proxySSE(w, request, response, reservation, candidate.callRoute(), &observer, body, len(body), context.Canceled, false)
			} else {
				expectUsageWrite(mock, reservation.CallID, "", nil, nil, nil, usage.UsageInterrupted)
				gateway.proxyHTTPBody(w, request, response, candidate.Binding.Adapter, candidate.callRoute(), &reservation, true, false, "")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("synthetic interrupted body") }
