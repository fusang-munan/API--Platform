package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

const (
	usageFinishTimeout   = 3 * time.Second
	maxHTTPMetricsBody   = 8 << 20
	beichenReceiptHeader = "X-Client-Request-ID"
)

func beichenReceiptID(header http.Header) uuid.UUID {
	var raw string
	count := 0
	for name, values := range header {
		if strings.EqualFold(name, beichenReceiptHeader) {
			count += len(values)
			if len(values) == 1 {
				raw = values[0]
			}
		}
	}
	if count != 1 || len(raw) != 36 {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil || id.String() != raw {
		return uuid.Nil
	}
	return id
}

func (g *Gateway) recordBeichenReceipt(ctx context.Context, callID, credentialID uuid.UUID, header http.Header) {
	id := beichenReceiptID(header)
	if id == uuid.Nil {
		return
	}
	ctx, cancel := usageFinishContext(ctx)
	defer cancel()
	// Receipt matching is independent of the ordinary terminal usage report.
	_ = g.usage.RecordBeichenReceiptID(ctx, callID, credentialID, id)
}

func usageFinishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), usageFinishTimeout)
}

func (candidate route) callRoute() usage.CallRoute {
	return usage.CallRoute{
		Beichen:    isBeichenAPI(candidate.Provider.BaseURL),
		ProviderID: candidate.Provider.ID, ProviderName: candidate.Provider.Name,
		CredentialID: candidate.CredentialID, BindingID: candidate.Binding.ID,
		UpstreamModelName: candidate.Binding.UpstreamModelName,
	}
}

func beichenReservationRoutes(routes []route) []usage.CallRoute {
	for _, candidate := range routes {
		if isBeichenAPI(candidate.Provider.BaseURL) {
			result := make([]usage.CallRoute, 0, len(routes))
			for _, item := range routes {
				result = append(result, item.callRoute())
			}
			return result
		}
	}
	return nil
}

func (g *Gateway) finalizeCall(ctx context.Context, reservation usage.Reservation, success bool, category, message string, candidate usage.CallRoute, metrics usage.CallMetrics) error {
	ctx, cancel := usageFinishContext(ctx)
	defer cancel()
	outcome := "upstream_failed"
	if success {
		outcome = "succeeded"
	}
	return g.usage.Finalize(ctx, reservation, success, outcome, category, message, candidate, metrics)
}

func (g *Gateway) finishAttempt(ctx context.Context, id uuid.UUID, started time.Time, success bool, status *int, category, message string) {
	if id == uuid.Nil {
		return
	}
	ctx, cancel := usageFinishContext(ctx)
	defer cancel()
	_ = g.usage.FinishAttempt(ctx, id, success, status, category, message, started)
}

func traceUpstreamWrite(request *http.Request, routed bool) (*http.Request, func() (time.Time, bool)) {
	var mu sync.Mutex
	var sentAt time.Time
	var connected bool
	record := func() {
		mu.Lock()
		defer mu.Unlock()
		if !routed || sentAt.IsZero() {
			sentAt = time.Now().UTC()
		}
	}
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			mu.Lock()
			defer mu.Unlock()
			connected = true
		},
		// An upstream can respond before Go has finished writing the body.
		WroteHeaders: record,
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				record()
			}
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), trace)), func() (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		return sentAt, connected
	}
}

func (g *Gateway) proxyHTTP(w http.ResponseWriter, original *http.Request, replay *replayBody, routes []route, reservation *usage.Reservation) {
	ctx := original.Context()
	routed := reservation != nil && len(beichenReservationRoutes(routes)) != 0
	attempted := usage.CallRoute{DefinitelyNotSent: routed}
	var rejection error
	category, message := "connection", "No upstream provider is available."
	for attempt := 0; len(routes) != 0 && attempt < upstreamRetryAttempts; attempt++ {
		if ctx.Err() != nil || !sleepUpstreamRetry(ctx, attempt) {
			break
		}
		candidate := routes[0]
		candidateRoute := candidate.callRoute()
		north := candidateRoute.Beichen
		review := ""
		var adjustments []func(map[string]json.RawMessage) error
		var quotaBudget int64
		if north && reservation != nil {
			review = beichenHTTPRequestReview(original.URL.Path, replay, candidate.Binding.UpstreamModelName)
			field := quotaTokenField(original.URL.Path)
			if limit := g.quotaTokenLimit(ctx, *reservation, candidateRoute, field); limit > 0 {
				adjustments = append(adjustments, func(payload map[string]json.RawMessage) error {
					quotaBudget = applyOutputBudget(payload, field, limit)
					return nil
				})
			}
		}
		request, cleanup, err := g.upstreamRequest(original, replay, candidate, adjustments...)
		if err != nil {
			category, message = "request_rewrite", "Unable to prepare the upstream request."
			break
		}
		attempted = candidateRoute
		started := time.Now()
		var attemptID uuid.UUID
		var writeObservation func() (time.Time, bool)
		if reservation != nil {
			attempted.DefinitelyNotSent = true
			attemptID, err = g.usage.StartAttempt(ctx, *reservation, attempted, quotaBudget)
			if err != nil {
				cleanup()
				category, message = "dispatch_rejected", "Unable to authorize upstream dispatch."
				rejection = err
				break
			}
			if routed {
				err = g.usage.PrepareDispatch(ctx, reservation.CallID, attempted)
				if err != nil {
					cleanup()
					category, message = "connection", "Unable to record upstream dispatch."
					g.finishAttempt(ctx, attemptID, started, false, nil, category, message)
					break
				}
			}
			request, writeObservation = traceUpstreamWrite(request, routed)
		}
		client := g.client
		if north || routed {
			request.GetBody = nil
			noRedirects := *client
			noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			client = &noRedirects
		}
		response, err := client.Do(request)
		cleanup()
		if reservation != nil && routed {
			var connected bool
			attempted.SentAt, connected = writeObservation()
			attempted.DefinitelyNotSent = !connected && response == nil && attempted.SentAt.IsZero()
			if response != nil || !attempted.SentAt.IsZero() {
				g.markCallSent(ctx, reservation.CallID)
			}
		}
		if err != nil {
			category, message = "connection", "Unable to connect to the upstream provider."
			g.finishAttempt(ctx, attemptID, started, false, nil, category, message)
			if routed && !attempted.DefinitelyNotSent {
				category = "dispatch_interrupted"
				break
			}
			continue
		}
		if reservation != nil && !routed {
			attempted.SentAt, _ = writeObservation()
			g.markCallSent(ctx, reservation.CallID)
		}
		status := response.StatusCode
		success := status >= 200 && status < 300
		canRetry := !routed && attempt < upstreamRetryAttempts-1 && ctx.Err() == nil
		if upstreamHTTPRetryable(status) && canRetry {
			_ = response.Body.Close()
			category, message = "http", fmt.Sprintf("HTTP %d", status)
			g.finishAttempt(ctx, attemptID, started, false, &status, category, message)
			continue
		}
		if north && reservation != nil {
			// Any response after this point belongs to the final dispatched
			// attempt. Routed calls never switch or retry once it responds.
			g.recordBeichenReceipt(ctx, reservation.CallID, attempted.CredentialID, response.Header)
		}
		if reservation != nil && success && isSSEResponse(response) {
			observer := sseObserver{adapter: candidate.Binding.Adapter, beichen: north, expectedModel: attempted.UpstreamModelName, review: review}
			buffer := make([]byte, 32<<10)
			n, readErr := response.Body.Read(buffer)
			observer.observe(buffer[:n])
			if canRetry && ctx.Err() == nil && !observer.billable() && (observer.retryable || (n == 0 && readErr != nil && readErr != io.EOF)) {
				_ = response.Body.Close()
				category, message = "stream", "Upstream stream failed before delivery."
				g.finishAttempt(ctx, attemptID, started, false, &status, category, message)
				continue
			}
			interrupted := g.proxySSE(w, original, response, *reservation, attempted, &observer, buffer, n, readErr, north)
			if interrupted {
				g.finishAttempt(ctx, attemptID, started, false, &status, "stream", "Upstream stream did not complete.")
			} else {
				g.finishAttempt(ctx, attemptID, started, true, &status, "", "")
			}
			return
		}
		if !north {
			g.finishAttempt(ctx, attemptID, started, success, &status, "", "")
		}
		interrupted := g.proxyHTTPBody(w, original, response, candidate.Binding.Adapter, attempted, reservation, success, north, review)
		if north {
			switch {
			case interrupted:
				g.finishAttempt(ctx, attemptID, started, false, &status, "stream", "Upstream response did not complete.")
			case !success:
				g.finishAttempt(ctx, attemptID, started, false, &status, "http", fmt.Sprintf("HTTP %d", status))
			default:
				g.finishAttempt(ctx, attemptID, started, true, &status, "", "")
			}
		}
		return
	}
	if reservation != nil {
		state := usage.UsageMissing
		if ctx.Err() != nil || category == "stream" || category == "dispatch_interrupted" {
			state = usage.UsageInterrupted
		}
		_ = g.finalizeCall(ctx, *reservation, false, category, message, attempted, usage.CallMetrics{State: state})
	}
	if rejection != nil {
		writeModelCallRejection(w, original.URL.Path, rejection)
		return
	}
	writeProtocolError(w, original.URL.Path, http.StatusBadGateway, "server_error", "upstream_unavailable", message, "")
}

func (g *Gateway) markCallSent(ctx context.Context, callID uuid.UUID) {
	ctx, cancel := usageFinishContext(ctx)
	defer cancel()
	_ = g.usage.MarkSent(ctx, callID)
}

func isSSEResponse(response *http.Response) bool {
	mediaType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

func (g *Gateway) proxyHTTPBody(w http.ResponseWriter, original *http.Request, response *http.Response, adapter modelbinding.Adapter, candidate usage.CallRoute, reservation *usage.Reservation, success, north bool, review string) bool {
	if reservation == nil {
		_ = copyUpstreamResponse(w, response)
		return false
	}
	ctx := original.Context()
	metrics := usage.CallMetrics{State: usage.UsageMissing}
	category, message := "http", fmt.Sprintf("HTTP %d", response.StatusCode)
	var resourceBody []byte
	resourceType, isCreation := createdResourceType(original.Method, original.URL.Path)
	if success {
		category, message = "", ""
		limit := int64(maxHTTPMetricsBody)
		if isCreation {
			limit = 10 << 20
		}
		// Replay the inspected prefix and copy the original remainder unchanged.
		// A large or binary response remains valid even without observable usage.
		prefix, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		body := response.Body
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.MultiReader(bytes.NewReader(prefix), body), Closer: body}
		switch {
		case err != nil:
			metrics.State = usage.UsageInterrupted
		case int64(len(prefix)) <= limit:
			interrupted := false
			if north && review == "" {
				review = beichenPayloadReview(prefix, candidate.UpstreamModelName, false)
				if review == "" && !beichenModelReported(jsonObject(prefix), candidate.UpstreamModelName) {
					review = beichenModelUnconfirmed
				}
			}
			if north && adapter == modelbinding.AdapterOpenaiResponses {
				status := rawString(jsonObject(prefix)["status"])
				interrupted = status != "" && status != "completed"
			}
			metrics = parseJSONResponseMetrics(adapter, prefix).finished(interrupted)
			if isCreation {
				resourceBody = prefix
			}
		}
	}
	if north && success {
		metrics, review = beichenReviewMetrics(&candidate, metrics, review)
		if review != "" {
			category, message = review, "The response requires billing review."
		}
	}
	if north && metrics.State == usage.UsageInterrupted {
		success, category, message = false, "stream", "Upstream response did not complete."
	}
	if err := g.finalizeCall(ctx, *reservation, success, category, message, candidate, metrics); err != nil {
		_ = response.Body.Close()
		writeProtocolError(w, original.URL.Path, http.StatusServiceUnavailable, "server_error", "billing_unavailable", "Unable to finalize model usage.", "")
		return metrics.State == usage.UsageInterrupted
	}
	if resourceBody != nil {
		g.rememberResourceRoute(ctx, resourceBody, resourceType, candidate.BindingID)
	}
	if err := copyUpstreamResponse(w, response); !north && metrics.State != usage.UsageInterrupted && (err != nil || ctx.Err() != nil) {
		g.completeCallMetrics(ctx, reservation.CallID, usage.CallMetrics{State: usage.UsageInterrupted})
	}
	return metrics.State == usage.UsageInterrupted
}

func (g *Gateway) rememberResourceRoute(ctx context.Context, body []byte, resourceType string, bindingID uuid.UUID) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if resourceID := firstString(payload, "id", "task_id"); resourceID != "" {
			ctx, cancel := usageFinishContext(ctx)
			defer cancel()
			_ = g.cache.SetGatewayResourceRoute(ctx, resourceType, resourceID, bindingID.String(), g.config.ResourceRouteTTL)
		}
	}
}

// proxySSE consumes the first read already inspected for retry eligibility.
// Every read is forwarded and flushed before waiting for the next one.
func (g *Gateway) proxySSE(w http.ResponseWriter, original *http.Request, response *http.Response, reservation usage.Reservation, candidate usage.CallRoute, observer *sseObserver, buffer []byte, n int, readErr error, north bool) (interrupted bool) {
	ctx := original.Context()
	controller := http.NewResponseController(w)
	charged, settlementTried, started, writeFailed := false, false, false, false
	for {
		if !north && observer.billable() && !settlementTried {
			settlementTried = true
			err := g.finalizeCall(ctx, reservation, true, "", "", candidate, usage.CallMetrics{})
			if err != nil && !started {
				_ = response.Body.Close()
				writeProtocolError(w, original.URL.Path, http.StatusServiceUnavailable, "server_error", "billing_unavailable", "Unable to finalize model usage.", "")
				return true
			}
			charged = err == nil
		}
		if !started && (n > 0 || readErr != nil) {
			copyHeaders(w.Header(), response.Header)
			removeHopByHop(w.Header())
			w.WriteHeader(response.StatusCode)
			started = true
		}
		if n > 0 {
			if err := forwardResponseChunk(w, controller, buffer[:n]); err != nil {
				writeFailed = true
				break
			}
		}
		if north && observer.terminal && observer.adapter != modelbinding.AdapterGoogleGeminiV1beta {
			break
		}
		if readErr != nil || observer.failed {
			break
		}
		if readErr = ctx.Err(); readErr != nil {
			break
		}
		n, readErr = response.Body.Read(buffer)
		observer.observe(buffer[:n])
	}
	_ = response.Body.Close()
	interrupted = writeFailed || readErr != io.EOF || observer.failed || !observer.terminal
	if north {
		// Once the upstream terminal frame has been observed, a downstream
		// write error cannot invalidate the final upstream usage.
		interrupted = observer.failed || observer.incomplete || !observer.terminal
		metrics := observer.metrics.finished(interrupted)
		if observer.review == "" && !observer.modelReported {
			observer.review = beichenModelUnconfirmed
		}
		metrics, review := beichenReviewMetrics(&candidate, metrics, observer.review)
		if interrupted {
			_ = g.finalizeCall(ctx, reservation, false, "stream", "Upstream stream did not complete.", candidate, metrics)
		} else {
			message := ""
			if review != "" {
				message = "The response requires billing review."
			}
			_ = g.finalizeCall(ctx, reservation, true, review, message, candidate, metrics)
		}
		return interrupted
	}
	metrics := observer.metrics.finished(interrupted)
	if charged {
		g.completeCallMetrics(ctx, reservation.CallID, metrics)
		return interrupted
	}
	if observer.billable() {
		_ = g.finalizeCall(ctx, reservation, true, "", "", candidate, metrics)
		return interrupted
	}
	_ = g.finalizeCall(ctx, reservation, false, "stream", "Stream ended before output or completion.", candidate, metrics)
	return interrupted
}

func (g *Gateway) completeCallMetrics(ctx context.Context, callID uuid.UUID, metrics usage.CallMetrics) {
	ctx, cancel := usageFinishContext(ctx)
	defer cancel()
	_ = g.usage.CompleteCallMetrics(ctx, callID, metrics)
}

func forwardResponseChunk(w http.ResponseWriter, controller *http.ResponseController, chunk []byte) error {
	n, err := w.Write(chunk)
	if err != nil {
		return err
	}
	if n != len(chunk) {
		return io.ErrShortWrite
	}
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func isBeichenAPI(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "api.beichenapi.com") && (u.Port() == "" || u.Port() == "443")
}

func requestBeichenStreamUsage(payload map[string]json.RawMessage) error {
	if !bytes.Equal(bytes.TrimSpace(payload["stream"]), []byte("true")) {
		return nil
	}
	options := make(map[string]json.RawMessage)
	if raw, explicit := payload["stream_options"]; explicit {
		options = jsonObject(raw)
		if options == nil {
			return nil
		}
		if _, explicit := options["include_usage"]; explicit {
			return nil
		}
	}
	options["include_usage"] = json.RawMessage("true")
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	payload["stream_options"] = encoded
	return nil
}
