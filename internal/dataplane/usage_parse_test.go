package dataplane

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func tokenIs(value *int64, expected int64) bool {
	return value != nil && *value == expected
}

func observeSSE(adapter modelbinding.Adapter, body []byte) sseObserver {
	observer := sseObserver{adapter: adapter}
	observer.observe(body)
	return observer
}

func TestParseJSONResponseMetrics(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"resp_abc","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, body)
	if metrics.UpstreamResponseID != "resp_abc" || !tokenIs(metrics.PromptTokens, 12) || !tokenIs(metrics.CompletionTokens, 3) || !tokenIs(metrics.TotalTokens, 15) {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestParseSSEResponseMetrics(t *testing.T) {
	t.Parallel()
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"resp_stream\",\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\ndata: [DONE]\n")
	metrics := observeSSE(modelbinding.AdapterOpenaiCompatible, body).metrics
	if metrics.UpstreamResponseID != "resp_stream" || !tokenIs(metrics.TotalTokens, 10) {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestParseResponsesCompletedSSE(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"type":"response.completed","response":{"id":"resp_codex_1","usage":{"input_tokens":120,"output_tokens":45,"total_tokens":165}}}` + "\n\n")
	metrics := observeSSE(modelbinding.AdapterOpenaiResponses, body).metrics
	if metrics.UpstreamResponseID != "resp_codex_1" || !tokenIs(metrics.PromptTokens, 120) || !tokenIs(metrics.CompletionTokens, 45) || !tokenIs(metrics.TotalTokens, 165) {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestParseAnthropicToolUseStaysOpen(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"msg_1","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}],"usage":{"input_tokens":4,"output_tokens":2}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterAnthropic, body)
	if metrics.ReplyClosed == nil || *metrics.ReplyClosed {
		t.Fatalf("tool_use should keep the question open: %#v", metrics.ReplyClosed)
	}
	if !tokenIs(metrics.PromptTokens, 4) || !tokenIs(metrics.CompletionTokens, 2) {
		t.Fatalf("usage lost: %#v", metrics)
	}
}

func TestParseAnthropicEndTurnCloses(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"msg_2","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterAnthropic, body)
	if metrics.ReplyClosed == nil || !*metrics.ReplyClosed {
		t.Fatalf("end_turn should close the question: %#v", metrics.ReplyClosed)
	}
}

func TestParseSSEStopReasonWinsOverToolBlock(t *testing.T) {
	t.Parallel()
	body := []byte("data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	metrics := observeSSE(modelbinding.AdapterAnthropic, body).metrics
	if metrics.ReplyClosed == nil || !*metrics.ReplyClosed {
		t.Fatalf("explicit end_turn should close: %#v", metrics.ReplyClosed)
	}
}

func TestParseResponsesFunctionCallStaysOpen(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"shell"}],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}` + "\n\n")
	metrics := observeSSE(modelbinding.AdapterOpenaiResponses, body).metrics
	if metrics.UpstreamResponseID != "resp_1" || !tokenIs(metrics.TotalTokens, 11) {
		t.Fatalf("metrics lost: %#v", metrics)
	}
	if metrics.ReplyClosed == nil || *metrics.ReplyClosed {
		t.Fatalf("function_call should keep the question open: %#v", metrics.ReplyClosed)
	}
}

func TestUsageKeepsExplicitZerosAndOverlappingOpenAICache(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":25,"completion_tokens":0,"credit":0,"prompt_tokens_details":{"cached_tokens":25},"completion_tokens_details":{"reasoning_tokens":0},"debug":"private content"}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, body).finished(false)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 25) ||
		!tokenIs(metrics.CacheHitTokens, 25) || !tokenIs(metrics.CompletionTokens, 0) ||
		!tokenIs(metrics.ReasoningTokens, 0) || !tokenIs(metrics.UpstreamCreditMicros, 0) ||
		metrics.TotalTokens != nil || metrics.CacheMissTokens != nil {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	bad := []byte(`{"usage":{"prompt_tokens":-1,"completion_tokens":"0","total_tokens":2147483648,"credit":1e100,"debug":"private content"}}`)
	if metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, bad).finished(false); metrics.State != usage.UsageMissing {
		t.Fatalf("accepted unknown or invalid counters: %+v", metrics)
	}
}

func TestAnthropicUsageMergesCumulativeFields(t *testing.T) {
	body := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_a\",\"usage\":{\"input_tokens\":25,\"output_tokens\":1,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":20}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":0}}\n\n")
	observer := observeSSE(modelbinding.AdapterAnthropic, body)
	metrics := observer.metrics.finished(!observer.terminal)
	if metrics.State != usage.UsageInterrupted || !tokenIs(metrics.PromptTokens, 25) ||
		!tokenIs(metrics.CompletionTokens, 0) || !tokenIs(metrics.CacheHitTokens, 0) ||
		!tokenIs(metrics.CacheWriteTokens, 20) || metrics.TotalTokens != nil {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestGeminiUsageMapsExplicitMetadata(t *testing.T) {
	body := []byte(`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"cachedContentTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":1,"totalTokenCount":9}}` + "\n\n")
	observer := observeSSE(modelbinding.AdapterGoogleGeminiV1beta, body)
	metrics := observer.metrics.finished(!observer.terminal)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 6) ||
		!tokenIs(metrics.CacheHitTokens, 4) || metrics.CacheMissTokens != nil ||
		!tokenIs(metrics.ReasoningTokens, 1) || !tokenIs(metrics.TotalTokens, 9) {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestExtractCallCorrelation(t *testing.T) {
	t.Parallel()
	replay, err := captureBody(io.NopCloser(strings.NewReader(`{"model":"gpt-5.3-codex","previous_response_id":"resp_prev","prompt_cache_key":"thread-1","input":[{"role":"user","content":"build login"}]}`)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	headers := make(http.Header)
	headers.Set("X-Agent-Turn-Id", "018f5a2a-0000-7000-8000-000000000001")
	correlation := extractCallCorrelation("/v1/responses", "application/json", replay, headers)
	if correlation.PreviousResponseID != "resp_prev" || correlation.TaskKey != "thread-1" || correlation.AgentTurnHeader == "" {
		t.Fatalf("unexpected correlation: %#v", correlation)
	}
}
