package dataplane

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
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

func TestJSONUsageRequiresTerminalEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		adapter    modelbinding.Adapter
		terminal   bool
		prompt     int64
		output     int64
	}{
		{
			"Anthropic message_start is not terminal",
			`{"type":"message_start","message":{"id":"msg_start","model":"m","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`,
			modelbinding.AdapterAnthropic, false, 5, 1,
		},
		{
			"Anthropic stop_reason is terminal",
			`{"id":"msg_done","type":"message","model":"m","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`,
			modelbinding.AdapterAnthropic, true, 5, 1,
		},
		{
			"Anthropic message_stop is terminal",
			`{"type":"message_stop","model":"m","message":{"id":"msg_stop","usage":{"input_tokens":5,"output_tokens":1}}}`,
			modelbinding.AdapterAnthropic, true, 5, 1,
		},
		{
			"OpenAI finish_reason is terminal",
			`{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			modelbinding.AdapterOpenaiCompatible, true, 5, 1,
		},
		{
			"OpenAI missing finish_reason is not terminal",
			`{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			modelbinding.AdapterOpenaiCompatible, false, 5, 1,
		},
		{
			"Responses completed status is terminal",
			`{"model":"m","status":"completed","usage":{"input_tokens":5,"output_tokens":1}}`,
			modelbinding.AdapterOpenaiResponses, true, 5, 1,
		},
		{
			"Responses in_progress status is not terminal",
			`{"model":"m","status":"in_progress","usage":{"input_tokens":5,"output_tokens":1}}`,
			modelbinding.AdapterOpenaiResponses, false, 5, 1,
		},
		{
			"Gemini finishReason is terminal",
			`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`,
			modelbinding.AdapterGoogleGeminiV1beta, true, 5, 1,
		},
		{
			"Gemini missing finishReason is not terminal",
			`{"candidates":[{"content":{"parts":[]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`,
			modelbinding.AdapterGoogleGeminiV1beta, false, 5, 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metrics := parseJSONResponseMetrics(tc.adapter, []byte(tc.body)).finishedJSON(tc.adapter, "", false)
			if !tc.terminal {
				if metrics.State != usage.UsageInterrupted || metrics.UsageReason != "terminal_usage_missing" ||
					!tokenIs(metrics.PromptTokens, tc.prompt) || !tokenIs(metrics.CompletionTokens, tc.output) {
					t.Fatalf("partial terminal evidence was billable: %+v", metrics)
				}
				return
			}
			if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, tc.prompt) ||
				!tokenIs(metrics.CompletionTokens, tc.output) {
				t.Fatalf("terminal usage was not reported: %+v", metrics)
			}
		})
	}
}

func TestJSONWithoutTerminalCannotAuthorizeUsage(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"message_start","message":{"id":"msg_empty","model":"m","stop_reason":null}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterAnthropic, body).finishedJSON(modelbinding.AdapterAnthropic, "/v1/messages", false)
	if metrics.State != usage.UsageInterrupted || metrics.UsageReason != "terminal_usage_missing" || metrics.HasUsage() {
		t.Fatalf("missing terminal evidence authorized usage: %+v", metrics)
	}
}

func TestJSONChatStatusAloneIsNotTerminal(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","status":"completed","usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, body).
		finishedJSON(modelbinding.AdapterOpenaiCompatible, "/v1/chat/completions", false)
	if metrics.State != usage.UsageInterrupted || metrics.UsageReason != "terminal_usage_missing" {
		t.Fatalf("a Responses-style status authorized a Chat debit: %+v", metrics)
	}
}

func TestJSONResponsesCompactDoesNotNeedGenerationTerminal(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"cmp_1","object":"response.compaction","usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiResponses, body).
		finishedJSON(modelbinding.AdapterOpenaiResponses, "/v1/responses/compact", false)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 5) || !tokenIs(metrics.CompletionTokens, 1) {
		t.Fatalf("compact usage was treated as an incomplete generation: %+v", metrics)
	}
}

func TestJSONOpenAIEmbeddingDoesNotNeedGenerationTerminal(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"embedding-1","usage":{"prompt_tokens":7,"total_tokens":7}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, body).
		finishedJSON(modelbinding.AdapterOpenaiCompatible, "/v1/embeddings", false)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 7) {
		t.Fatalf("embedding usage was treated as an incomplete generation: %+v", metrics)
	}
}

func TestJSONEmbeddingUsageDoesNotNeedGenerationTerminal(t *testing.T) {
	t.Parallel()
	body := []byte(`{"usageMetadata":{"promptTokenCount":5,"totalTokenCount":5}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterGoogleGeminiV1beta, body).
		finishedJSON(modelbinding.AdapterGoogleGeminiV1beta, "/v1beta/models/embed:embedContent", false)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 5) {
		t.Fatalf("embedding usage was treated as an incomplete generation: %+v", metrics)
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
	body := []byte(`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":0,"credit":0,"prompt_tokens_details":{"cached_tokens":25},"completion_tokens_details":{"reasoning_tokens":0},"debug":"private content"}}`)
	metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, body).finishedJSON(modelbinding.AdapterOpenaiCompatible, "/v1/chat/completions", false)
	if metrics.State != usage.UsageReported || !tokenIs(metrics.PromptTokens, 25) ||
		!tokenIs(metrics.CacheHitTokens, 25) || !tokenIs(metrics.CompletionTokens, 0) ||
		!tokenIs(metrics.ReasoningTokens, 0) || !tokenIs(metrics.UpstreamCreditMicros, 0) ||
		metrics.TotalTokens != nil || metrics.CacheMissTokens != nil {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	bad := []byte(`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":"0","total_tokens":2147483648,"credit":1e100,"debug":"private content"}}`)
	if metrics := parseJSONResponseMetrics(modelbinding.AdapterOpenaiCompatible, bad).finishedJSON(modelbinding.AdapterOpenaiCompatible, "/v1/chat/completions", false); metrics.State != usage.UsageMissing {
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

func TestBeichenUsageProtocolMatrix(t *testing.T) {
	cases := []struct {
		name, transport, payload, reason string
		adapter                          modelbinding.Adapter
		state                            usage.UsageState
		prompt, output, read, write      int64 // -1 is an omitted counter
		reasoning, tool                  int64
	}{
		{"Chat JSON", "json", `{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}}`, "",
			modelbinding.AdapterOpenaiCompatible, usage.UsageReported, 10, 2, 4, 0, -1, -1},
		{"Chat SSE", "sse", "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n",
			"token_breakdown_unconfirmed", modelbinding.AdapterOpenaiCompatible, usage.UsagePartial, 10, 0, 0, -1, -1, -1},
		{"Chat invalid cache write", "json", `{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6,"prompt_cache_write_tokens":-1}}`,
			"invalid_token_count", modelbinding.AdapterOpenaiCompatible, usage.UsagePartial, 10, 2, 4, -1, -1, -1},
		{"Responses JSON", "json", `{"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`, "",
			modelbinding.AdapterOpenaiResponses, usage.UsageReported, 10, 2, 4, 1, 1, -1},
		{"Responses SSE", "sse", "data: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n",
			"response_interrupted", modelbinding.AdapterOpenaiResponses, usage.UsageInterrupted, 10, 2, -1, -1, -1, -1},
		{"Responses SSE output limit", "sse", "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":33267,\"output_tokens\":8,\"prompt_cache_miss_tokens\":33267}}}\n\n",
			"", modelbinding.AdapterOpenaiResponses, usage.UsageReported, 33267, 8, 0, 0, -1, -1},
		{"Responses SSE output limit without final cache evidence", "sse", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":33267,\"output_tokens\":8,\"input_tokens_details\":{\"cached_tokens\":32768}}}}\n\n",
			"token_breakdown_unconfirmed", modelbinding.AdapterOpenaiResponses, usage.UsagePartial, 33267, 8, 32768, -1, -1, -1},
		{"Responses WebSocket", "ws", `{"type":"response.done","response":{"id":"ws-1","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":1}}}}`, "",
			modelbinding.AdapterOpenaiResponses, usage.UsageReported, 10, 2, 4, 1, -1, -1},
		{"Anthropic JSON", "json", `{"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":0}}}`, "",
			modelbinding.AdapterAnthropic, usage.UsageReported, 5, 2, 4, 1, -1, -1},
		{"Anthropic SSE", "sse", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"cache_read_input_tokens\":4,\"cache_creation_input_tokens\":1}}}\n\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "",
			modelbinding.AdapterAnthropic, usage.UsageReported, 5, 2, 4, 1, -1, -1},
		{"Anthropic conflicting writes", "json", `{"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":4,"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}`,
			"conflicting_cache_creation_counts", modelbinding.AdapterAnthropic, usage.UsagePartial, 5, 2, 4, 1, -1, -1},
		{"Anthropic single cache child contradicts total", "json", `{"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":3}}}`,
			"conflicting_cache_creation_counts", modelbinding.AdapterAnthropic, usage.UsagePartial, 5, 2, 0, 3, -1, -1},
		{"Anthropic SSE single cache child contradicts total", "sse", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"cache_read_input_tokens\":0,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3}}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2,\"total_tokens\":7}}\n\ndata: {\"type\":\"message_stop\"}\n\n",
			"conflicting_cache_creation_counts", modelbinding.AdapterAnthropic, usage.UsagePartial, 5, 2, 0, 3, -1, -1},
		{"Anthropic single cache child agrees with total", "json", `{"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":10,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":3}}}`,
			"", modelbinding.AdapterAnthropic, usage.UsageReported, 5, 2, 0, 3, -1, -1},
		{"Anthropic single cache child without total", "json", `{"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":3}}}`,
			"cache_write_subtotal_only", modelbinding.AdapterAnthropic, usage.UsagePartial, 5, 2, 0, -1, -1, -1},
		{"Gemini JSON array", "json", `[{"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4}},{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":2,"thoughtsTokenCount":1,"toolUsePromptTokenCount":3,"totalTokenCount":16}}]`,
			"cache_write_not_in_usage_metadata", modelbinding.AdapterGoogleGeminiV1beta, usage.UsagePartial, 10, 2, 4, -1, 1, 3},
		{"Gemini SSE", "sse", "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"cachedContentTokenCount\":4,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":1,\"toolUsePromptTokenCount\":3,\"totalTokenCount\":16}}\n\n",
			"cache_write_not_in_usage_metadata", modelbinding.AdapterGoogleGeminiV1beta, usage.UsagePartial, 10, 2, 4, -1, 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var metrics usage.CallMetrics
			switch tc.transport {
			case "json":
				metrics = parseJSONResponseMetrics(tc.adapter, []byte(tc.payload)).finishedJSON(tc.adapter, "", false)
			case "sse":
				observer := sseObserver{adapter: tc.adapter, beichen: true}
				observer.observe([]byte(tc.payload))
				metrics = observer.metrics.finished(!observer.terminal || observer.incomplete || observer.failed)
			case "ws":
				id := uuid.New()
				observation := legacyResponsesObservation{callID: id, seen: map[string]struct{}{}}
				observation.observe([]byte(`{"type":"response.created","response":{"id":"ws-1"}}`))
				found, observed := observation.observe([]byte(tc.payload))
				if found != id {
					t.Fatal("terminal usage wasn't linked to response.created")
				}
				metrics = observed
			}
			metrics = metrics.ClassifyBeichenUsage(string(tc.adapter))
			if metrics.State != tc.state || metrics.UsageReason != tc.reason {
				t.Fatalf("state=%q reason=%q, want %q %q", metrics.State, metrics.UsageReason, tc.state, tc.reason)
			}
			for _, part := range []struct {
				name string
				got  *int64
				want int64
			}{
				{"prompt", metrics.PromptTokens, tc.prompt}, {"output", metrics.CompletionTokens, tc.output},
				{"read", metrics.CacheHitTokens, tc.read}, {"write", metrics.CacheWriteTokens, tc.write},
				{"reasoning", metrics.ReasoningTokens, tc.reasoning}, {"tool prompt", metrics.ToolPromptTokens, tc.tool},
			} {
				if (part.want < 0 && part.got != nil) || (part.want >= 0 && !tokenIs(part.got, part.want)) {
					t.Errorf("unexpected %s, expected %d: %+v", part.name, part.want, metrics)
				}
			}
		})
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
