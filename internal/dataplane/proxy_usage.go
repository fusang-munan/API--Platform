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
	usageFinishTimeout = 3 * time.Second
	maxHTTPMetricsBody = 8 << 20
)

func usageFinishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), usageFinishTimeout)
}

func (candidate route) callRoute() usage.CallRoute {
	return usage.CallRoute{
		ProviderID: candidate.Provider.ID, ProviderName: candidate.Provider.Name,
		CredentialID: candidate.CredentialID, BindingID: candidate.Binding.ID,
		UpstreamModelName: candidate.Binding.UpstreamModelName,
	}
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

func traceUpstreamWrite(request *http.Request) (*http.Request, func() time.Time) {
	var mu sync.Mutex
	var sentAt time.Time
	record := func() {
		mu.Lock()
		defer mu.Unlock()
		sentAt = time.Now().UTC()
	}
	trace := &httptrace.ClientTrace{
		// An upstream can respond before Go has finished writing the body.
		WroteHeaders: record,
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				record()
			}
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), trace)), func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return sentAt
	}
}

func (g *Gateway) proxyHTTP(w http.ResponseWriter, original *http.Request, replay *replayBody, routes []route, reservation *usage.Reservation) {
	ctx := original.Context()
	var attempted usage.CallRoute
	category, message := "connection", "No upstream provider is available."
	for attempt := 0; len(routes) != 0 && attempt < upstreamRetryAttempts; attempt++ {
		if ctx.Err() != nil || !sleepUpstreamRetry(ctx, attempt) {
			break
		}
		candidate := routes[0]
		request, cleanup, err := g.upstreamRequest(original, replay, candidate)
		if err != nil {
			category, message = "request_rewrite", "Unable to prepare the upstream request."
			break
		}
		attempted = candidate.callRoute()
		started := time.Now()
		var attemptID uuid.UUID
		var sentAt func() time.Time
		if reservation != nil {
			attemptID, _ = g.usage.StartAttempt(ctx, reservation.CallID, attempted)
			request, sentAt = traceUpstreamWrite(request)
		}
		response, err := g.client.Do(request)
		cleanup()
		if err != nil {
			category, message = "connection", "Unable to connect to the upstream provider."
			g.finishAttempt(ctx, attemptID, started, false, nil, category, message)
			continue
		}
		if reservation != nil {
			attempted.SentAt = sentAt()
			sentCtx, cancel := usageFinishContext(ctx)
			_ = g.usage.MarkSent(sentCtx, reservation.CallID)
			cancel()
		}
		status := response.StatusCode
		success := status >= 200 && status < 300
		canRetry := attempt < upstreamRetryAttempts-1 && ctx.Err() == nil
		if upstreamHTTPRetryable(status) && canRetry {
			_ = response.Body.Close()
			category, message = "http", fmt.Sprintf("HTTP %d", status)
			g.finishAttempt(ctx, attemptID, started, false, &status, category, message)
			continue
		}
		if reservation != nil && success && isSSEResponse(response) {
			observer := sseObserver{adapter: candidate.Binding.Adapter}
			buffer := make([]byte, 32<<10)
			n, readErr := response.Body.Read(buffer)
			observer.observe(buffer[:n])
			if canRetry && ctx.Err() == nil && !observer.billable() && (observer.retryable || (n == 0 && readErr != nil && readErr != io.EOF)) {
				_ = response.Body.Close()
				category, message = "stream", "Upstream stream failed before delivery."
				g.finishAttempt(ctx, attemptID, started, false, &status, category, message)
				continue
			}
			interrupted := g.proxySSE(w, original, response, *reservation, attempted, &observer, buffer, n, readErr)
			if interrupted {
				g.finishAttempt(ctx, attemptID, started, false, &status, "stream", "Upstream stream did not complete.")
			} else {
				g.finishAttempt(ctx, attemptID, started, true, &status, "", "")
			}
			return
		}
		g.finishAttempt(ctx, attemptID, started, success, &status, "", "")
		g.proxyHTTPBody(w, original, response, candidate.Binding.Adapter, attempted, reservation, success)
		return
	}
	if reservation != nil {
		state := usage.UsageMissing
		if ctx.Err() != nil || category == "stream" {
			state = usage.UsageInterrupted
		}
		_ = g.finalizeCall(ctx, *reservation, false, category, message, attempted, usage.CallMetrics{State: state})
	}
	writeProtocolError(w, original.URL.Path, http.StatusBadGateway, "server_error", "upstream_unavailable", message, "")
}

func isSSEResponse(response *http.Response) bool {
	mediaType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

func (g *Gateway) proxyHTTPBody(w http.ResponseWriter, original *http.Request, response *http.Response, adapter modelbinding.Adapter, candidate usage.CallRoute, reservation *usage.Reservation, success bool) {
	if reservation == nil {
		_ = copyUpstreamResponse(w, response)
		return
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
			metrics = parseJSONResponseMetrics(adapter, prefix).finished(false)
			if isCreation {
				resourceBody = prefix
			}
		}
	}
	if err := g.finalizeCall(ctx, *reservation, success, category, message, candidate, metrics); err != nil {
		_ = response.Body.Close()
		writeProtocolError(w, original.URL.Path, http.StatusServiceUnavailable, "server_error", "billing_unavailable", "Unable to finalize model usage.", "")
		return
	}
	if resourceBody != nil {
		g.rememberResourceRoute(ctx, resourceBody, resourceType, candidate.BindingID)
	}
	if err := copyUpstreamResponse(w, response); metrics.State != usage.UsageInterrupted && (err != nil || ctx.Err() != nil) {
		g.completeCallMetrics(ctx, reservation.CallID, usage.CallMetrics{State: usage.UsageInterrupted})
	}
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
func (g *Gateway) proxySSE(w http.ResponseWriter, original *http.Request, response *http.Response, reservation usage.Reservation, candidate usage.CallRoute, observer *sseObserver, buffer []byte, n int, readErr error) (interrupted bool) {
	ctx := original.Context()
	controller := http.NewResponseController(w)
	charged, settlementTried, started, writeFailed := false, false, false, false
	for {
		if observer.billable() && !settlementTried {
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
