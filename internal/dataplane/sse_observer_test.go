package dataplane

import (
	"bytes"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenResponsesKeepsEarlierCacheEvidenceWithoutCharging(t *testing.T) {
	observer := sseObserver{adapter: modelbinding.AdapterOpenaiResponses, beichen: true}
	observer.observe([]byte(`data: {"type":"response.created","response":{"id":"resp_fixture","usage":{"input_tokens":59,"input_tokens_details":{"cached_tokens":17,"cache_write_tokens":0}}}}

data: {"type":"response.completed","response":{"id":"resp_fixture","status":"completed","usage":{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":0}}}}

data: {"type":"response.completed","response":{"id":"other","usage":{"input_tokens":100,"output_tokens":5}}}

`))
	metrics := observer.metrics
	report, reason := beichenReviewMetrics(observer.adapter, metrics.finished(false), "")
	if !observer.terminal || observer.incomplete || metrics.UpstreamResponseID != "resp_fixture" ||
		!tokenIs(metrics.CacheHitTokens, 0) || !tokenIs(metrics.CacheWriteTokens, 0) ||
		!tokenIs(metrics.CompletionTokens, 3) || report.State != usage.UsagePartial ||
		report.UsageReason != "terminal_usage_incomplete" || reason != beichenIncompleteUsage {
		t.Fatalf("unexpected terminal SSE usage: %+v", metrics)
	}
}

func TestSSEOverloadCanRetryOnlyBeforeOutput(t *testing.T) {
	const overload = "event: error\r\ndata: {\"error\":{\"code\":\"server_is_overloaded\"}}\r\n\r\n"
	observer := observeSSE(modelbinding.AdapterOpenaiResponses, []byte(overload))
	if !observer.retryable || observer.billable() {
		t.Fatal("expected a retryable SSE error before confirmation")
	}
	first := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"error message example\"}\r\n\r\n"
	observer = observeSSE(modelbinding.AdapterOpenaiResponses, []byte(first+overload))
	if !observer.billable() || !observer.failed {
		t.Fatal("lost the initial response or the later failure")
	}
}

func TestSSEObserverSkipsOversizeAndParsesNextMultilineEvent(t *testing.T) {
	observer := sseObserver{adapter: modelbinding.AdapterOpenaiCompatible}
	observer.observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\""))
	for range 20 {
		observer.observe(bytes.Repeat([]byte("x"), 32<<10))
	}
	observer.observe([]byte("\"}}]}\r"))
	observer.observe([]byte("\n\r"))
	observer.observe([]byte("\ndata: {\"id\":\"chat_1\",\rdata: \"usage\":{\"prompt_tokens\":0}}\r\r"))
	// An event with short lines can still exceed the aggregate event limit.
	for range 100 {
		observer.observe([]byte("data: " + strings.Repeat("a", 1024) + "\n"))
	}
	observer.observe([]byte("\ndata: [DO"))
	observer.observe([]byte("NE]\n\n"))
	if cap(observer.line) > maxSSEEventBytes || cap(observer.data) > maxSSEEventBytes {
		t.Fatal("observer exceeded its per-line or per-event memory limit")
	}
	if !observer.terminal || observer.metrics.UpstreamResponseID != "chat_1" || !tokenIs(observer.metrics.PromptTokens, 0) {
		t.Fatalf("usage after overflow was lost: %+v", observer.metrics)
	}
}

func TestSSEObserverKeepsTerminalNameWhenPayloadExceedsLimit(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"response\":{\"output\":\"" +
		strings.Repeat("x", maxSSEEventBytes) + "\"}}\n\n")
	observer := observeSSE(modelbinding.AdapterOpenaiResponses, body)
	if !observer.billable() || !observer.terminal || observer.metrics.HasUsage() {
		t.Fatalf("expected completion with no observable usage: %+v", observer.metrics)
	}
}

func TestSSEKeepsPreparatoryEventsPending(t *testing.T) {
	cases := []struct {
		name    string
		adapter modelbinding.Adapter
		prelude []string
		output  string
		done    string
	}{
		{
			"responses", modelbinding.AdapterOpenaiResponses,
			[]string{`{"type":"response.created","response":{"id":"resp_prepared"}}`,
				`{"type":"response.in_progress"}`,
				`{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","arguments":""}}`,
				`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`,
				`{"type":"response.output_text.delta","delta":""}`, "[DONE]"},
			`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
			`{"type":"response.completed","response":{"id":"resp_prepared"}}`,
		},
		{
			"chat", modelbinding.AdapterOpenaiCompatible,
			[]string{`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_prepared"}]}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":15}}`},
			`{"choices":[{"delta":{"content":"hello"}}]}`, "[DONE]",
		},
		{
			"anthropic", modelbinding.AdapterAnthropic,
			[]string{`{"type":"message_start","message":{"id":"msg_prepared","usage":{"input_tokens":10}}}`,
				`{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_prepared","name":"lookup","input":{}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`},
			`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"message_stop"}`,
		},
		{
			"gemini", modelbinding.AdapterGoogleGeminiV1beta,
			[]string{`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"promptTokenCount":10}}`},
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`,
			`{"candidates":[{"finishReason":"STOP"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := sseObserver{adapter: tc.adapter}
			for _, prelude := range tc.prelude {
				observer.observe([]byte("data: " + prelude + "\n\n"))
				if observer.billable() {
					t.Fatalf("preparatory event is billable: %s", prelude)
				}
			}
			completedWithoutOutput := observer
			completedWithoutOutput.observe([]byte("data: " + tc.done + "\n\n"))
			if !completedWithoutOutput.billable() || !completedWithoutOutput.terminal {
				t.Fatal("a completed stream should settle even without generated content")
			}
			observer.observe([]byte("data: " + tc.output + "\n\n"))
			if !observer.billable() || observer.terminal {
				t.Fatal("generated output should settle before the stream ends")
			}
			observer.observe([]byte("data: {\"type\":\"error\",\"error\":{\"code\":\"upstream_unavailable\"}}\n\n"))
			if !observer.billable() || !observer.failed {
				t.Fatal("a failure after generated output must keep the original charge")
			}
		})
	}
}
