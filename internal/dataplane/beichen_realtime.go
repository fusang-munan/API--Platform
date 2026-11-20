package dataplane

import (
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func (g *Gateway) serveBeichenRealtime(w http.ResponseWriter, r *http.Request, selected route, keyID uuid.UUID, target string, headers http.Header) {
	ctx := r.Context()
	callRoute := selected.callRoute()
	reservation, err := g.usage.Reserve(ctx, keyID, selected.Binding.ModelID, requestIDFromContext(r),
		string(selected.Binding.Adapter), r.URL.Path, "", "", usage.CallCorrelation{}, callRoute)
	if err != nil {
		writeModelCallRejection(w, r.URL.Path, err)
		return
	}
	started := time.Now()
	attempt, err := g.usage.StartAttempt(ctx, reservation, callRoute, 0)
	if err != nil {
		callRoute.DefinitelyNotSent = true
		_ = g.finalizeCall(ctx, reservation, false, "dispatch_rejected", "Unable to authorize upstream dispatch.", callRoute, usage.CallMetrics{})
		writeModelCallRejection(w, r.URL.Path, err)
		return
	}
	err = g.usage.PrepareDispatch(ctx, reservation.CallID, callRoute)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		callRoute.DefinitelyNotSent = true
		message := "Unable to record upstream Realtime dispatch."
		_ = g.finalizeCall(ctx, reservation, false, "connection", message, callRoute, usage.CallMetrics{State: usage.UsageMissing})
		g.finishAttempt(ctx, attempt, started, false, nil, "connection", message)
		writeOpenAIError(w, http.StatusServiceUnavailable, "server_error", "billing_unavailable", message, "")
		return
	}

	upstream, response, err := g.dialer.DialContext(ctx, target, headers)
	if upstream != nil || response != nil {
		g.markCallSent(ctx, reservation.CallID)
	}
	if err != nil {
		var status *int
		if response != nil {
			status = &response.StatusCode
			_ = response.Body.Close()
		}
		message := "Unable to establish the upstream Realtime connection."
		_ = g.finalizeCall(ctx, reservation, false, "dispatch_interrupted", message, callRoute, usage.CallMetrics{State: usage.UsageInterrupted})
		g.finishAttempt(ctx, attempt, started, false, status, "connection", message)
		writeOpenAIError(w, http.StatusBadGateway, "server_error", "upstream_unavailable", message, "")
		return
	}
	defer upstream.Close()
	status := http.StatusSwitchingProtocols
	g.finishAttempt(ctx, attempt, started, true, &status, "", "")
	observed := beichenRealtimeUsage{metrics: usage.CallMetrics{State: usage.UsageMissing}}
	upgrader := websocket.Upgrader{CheckOrigin: g.validWebSocketOrigin}
	client, err := upgrader.Upgrade(w, r, nil)
	if err == nil {
		client.SetReadLimit(g.config.MaxRequestBytes)
		upstream.SetReadLimit(g.config.MaxRequestBytes)
		proxyWebSockets(client, upstream, observed.observe, func() {
			leaseCtx, cancel := usageFinishContext(ctx)
			defer cancel()
			_ = g.usage.RenewLease(leaseCtx, reservation.CallID)
		})
	}
	// MarkSent records dispatch. Leaving the pricing timestamp unset and
	// keeping metrics in review prevents a connection from using text rates.
	_ = g.finalizeCall(ctx, reservation, err == nil, beichenUnsupportedPrice,
		"Realtime connection usage requires billing review.", callRoute, observed.metrics)
}

type beichenRealtimeUsage struct {
	seen    map[string]struct{}
	metrics usage.CallMetrics
}

func (s *beichenRealtimeUsage) observe(messageType int, data []byte) {
	if messageType != websocket.TextMessage || s.metrics.State == usage.UsageInterrupted {
		return
	}
	payload := jsonObject(data)
	switch rawString(payload["type"]) {
	case "response.done", "response.completed", "response.incomplete", "response.failed":
	default:
		return
	}
	id := rawString(jsonObject(payload["response"])["id"])
	if id == "" || len(id) > 128 || len(s.seen) >= 4096 {
		s.metrics = usage.CallMetrics{State: usage.UsageInterrupted}
		return
	}
	if _, duplicate := s.seen[id]; duplicate {
		return
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	s.seen[id] = struct{}{}
	next := parseJSONResponseMetrics(modelbinding.AdapterOpenaiResponses, data).CallMetrics
	// A connection has no single upstream response ID. These totals are
	// diagnostic and keep any field unknown if any response omitted it.
	first := len(s.seen) == 1
	s.metrics.PromptTokens = sumRealtimeCount(first, s.metrics.PromptTokens, next.PromptTokens, math.MaxInt32)
	s.metrics.CompletionTokens = sumRealtimeCount(first, s.metrics.CompletionTokens, next.CompletionTokens, math.MaxInt32)
	s.metrics.TotalTokens = sumRealtimeCount(first, s.metrics.TotalTokens, next.TotalTokens, math.MaxInt32)
	s.metrics.CacheHitTokens = sumRealtimeCount(first, s.metrics.CacheHitTokens, next.CacheHitTokens, math.MaxInt64)
	s.metrics.CacheWriteTokens = sumRealtimeCount(first, s.metrics.CacheWriteTokens, next.CacheWriteTokens, math.MaxInt64)
}

func sumRealtimeCount(first bool, current, next *int64, limit int64) *int64 {
	if first {
		return next
	}
	if current == nil || next == nil || *current > limit-*next {
		return nil
	}
	sum := *current + *next
	return &sum
}
