package dataplane

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseJSONResponseMetrics(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"resp_abc","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	metrics := parseResponseMetrics("application/json", body)
	if metrics.UpstreamResponseID != "resp_abc" || metrics.PromptTokens != 12 || metrics.CompletionTokens != 3 || metrics.TotalTokens != 15 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestParseSSEResponseMetrics(t *testing.T) {
	t.Parallel()
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"resp_stream\",\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\ndata: [DONE]\n")
	metrics := parseResponseMetrics("text/event-stream", body)
	if metrics.UpstreamResponseID != "resp_stream" || metrics.TotalTokens != 10 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestParseResponsesCompletedSSE(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"type":"response.completed","response":{"id":"resp_codex_1","usage":{"input_tokens":120,"output_tokens":45,"total_tokens":165}}}` + "\n\n")
	metrics := parseResponseMetrics("text/event-stream", body)
	if metrics.UpstreamResponseID != "resp_codex_1" || metrics.PromptTokens != 120 || metrics.CompletionTokens != 45 || metrics.TotalTokens != 165 {
		t.Fatalf("unexpected metrics: %#v", metrics)
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
