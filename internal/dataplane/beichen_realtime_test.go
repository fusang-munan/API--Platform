package dataplane

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenRealtimeObservesDistinctResponsesWithoutPricing(t *testing.T) {
	session := beichenRealtimeUsage{metrics: usage.CallMetrics{State: usage.UsageMissing}}
	for _, event := range []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.done","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}}`,
		`{"type":"response.done","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}}`,
		`{"type":"response.done","response":{"id":"resp_2","usage":{"input_tokens":8,"output_tokens":3}}}`,
	} {
		session.observe(websocket.TextMessage, []byte(event))
	}
	metrics := session.metrics
	if metrics.State != usage.UsageMissing || metrics.UpstreamResponseID != "" ||
		metrics.PromptTokens == nil || *metrics.PromptTokens != 18 ||
		metrics.CompletionTokens == nil || *metrics.CompletionTokens != 5 || metrics.CacheHitTokens != nil {
		t.Fatalf("unexpected connection metrics: %+v", metrics)
	}
	session.observe(websocket.TextMessage, []byte(`{"type":"response.done","response":{"usage":{"input_tokens":100}}}`))
	if session.metrics.State != usage.UsageInterrupted || session.metrics.HasUsage() {
		t.Fatalf("ambiguous terminal event should invalidate connection totals: %+v", session.metrics)
	}
}
