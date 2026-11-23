package dataplane

import (
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenTextPricingReview(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		request             bool
	}{
		{"text request", `{"model":"public","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[]}`, "", true},
		{"image input", `{"input":[{"content":[{"type":"input_image"}]}]}`, beichenUnsupportedPrice, true},
		{"audio output requested", `{"modalities":["text","audio"]}`, beichenUnsupportedPrice, true},
		{"tool available", `{"tools":[{"type":"web_search"}]}`, beichenUnsupportedPrice, true},
		{"unknown context", `{"previous_response_id":"resp_1"}`, beichenUnsupportedPrice, true},
		{"model substitution", `{"response":{"model":"different"}}`, beichenModelMismatch, false},
		{"alternate tariff", `{"model":"bound","service_tier":"priority"}`, beichenUnsupportedPrice, false},
		{"audio counters", `{"model":"bound","usage":{"output_tokens_details":{"audio_tokens":7}}}`, beichenUnsupportedPrice, false},
		{"tool output frame", `{"type":"response.output_item.added","item":{"type":"function_call"}}`, beichenUnsupportedPrice, false},
		{"conflicting hit counters", `{"usage":{"prompt_cache_hit_tokens":0,"prompt_tokens_details":{"cached_tokens":4}}}`, beichenIncompleteUsage, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := beichenPayloadReview([]byte(tc.payload), "bound", tc.request); got != tc.want {
				t.Fatalf("review = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBeichenRequestPricingEvidence(t *testing.T) {
	cases := []struct {
		name, path, body string
		effort, ttl      string
	}{
		{"chat", "/v1/chat/completions", `{"reasoning_effort":"high"}`, "high", ""},
		{"responses", "/v1/responses", `{"reasoning":{"effort":"low"}}`, "low", ""},
		{"responses default", "/v1/responses", `{"reasoning":{"summary":"auto"}}`, "medium", ""},
		{"thinking effort", "/v1/messages", `{"thinking":{"effort":"high"},"messages":[{"content":[{"type":"text","cache_control":{"ttl":"1h"}}]}]}`, "high", "1h"},
		{"Anthropic output effort", "/v1/messages", `{"output_config":{"effort":"max"},"thinking":{"effort":"high"},"cache_control":{"type":"ephemeral"}}`, "max", "5m"},
		{"Anthropic effort fallback", "/v1/messages", `{"effort":"low","cache_control":{"ttl":"1h"}}`, "low", "1h"},
		{"only a thinking budget", "/v1/messages", `{"thinking":{"type":"enabled","budget_tokens":1024}}`, "medium", ""},
		{"same TTL", "/v1/messages", `{"cache_control":{"ttl":"5m"},"system":[{"type":"text","cache_control":{"type":"ephemeral"}}]}`, "medium", "5m"},
		{"mixed TTL", "/v1/messages", `{"cache_control":{"ttl":"1h"},"system":[{"type":"text","cache_control":{"ttl":"5m"}}]}`, "medium", ""},
		{"invalid TTL", "/v1/messages", `{"cache_control":{"ttl":"2h"}}`, "medium", ""},
		{"empty cache declaration", "/v1/messages", `{"cache_control":{}}`, "medium", ""},
		{"similarly named metadata", "/v1/messages", `{"metadata":{"cache_control":{"ttl":"1h"}}}`, "medium", ""},
		{"OpenAI uses implicit cache", "/v1/chat/completions", `{"cache_control":{"ttl":"1h"}}`, "medium", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var route usage.CallRoute
			if reason := beichenRequestReview([]byte(tc.body), tc.path, &route); reason != "" {
				t.Fatalf("unexpected pricing review: %s", reason)
			}
			if route.ReasoningEffort == nil || *route.ReasoningEffort != tc.effort || route.CacheTTL != tc.ttl {
				t.Fatalf("expected effort %q and TTL %q, got effort %v and TTL %q", tc.effort, tc.ttl, route.ReasoningEffort, route.CacheTTL)
			}
		})
	}
}

func TestBeichenPricingRequiresCompleteCompatibleCounters(t *testing.T) {
	for _, tc := range []struct{ usage, reason string }{
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, ""},
		{`{"input_tokens":59,"output_tokens":3,"prompt_cache_miss_tokens":55,"prompt_cache_hit_tokens":4}`, ""},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cache_write_tokens":0}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":0}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":58,"cache_write_tokens":2}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"total_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, beichenIncompleteUsage},
	} {
		response := parseJSONResponseMetrics(modelbinding.AdapterOpenaiResponses, []byte(`{"status":"completed","usage":`+tc.usage+`}`)).finishedJSON(modelbinding.AdapterOpenaiResponses, "/v1/responses", false)
		metrics, reason := beichenReviewMetrics(modelbinding.AdapterOpenaiResponses, response, "")
		if reason != tc.reason || (metrics.State == usage.UsageReported) != (tc.reason == "") ||
			metrics.PromptTokens == nil || *metrics.PromptTokens != 59 {
			t.Fatalf("unexpected pricing eligibility for %s: %q, %+v", tc.usage, reason, metrics)
		}
	}
}

func TestBeichenAnthropicStreamPricingUsesMessageModelAndSeparateCounters(t *testing.T) {
	const opening = "data: {\"type\":\"message_start\",\"message\":{\"model\":\"bound\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0,\"cache_read_input_tokens\":50,\"cache_creation_input_tokens\":500}}}\n\n"
	const terminal = "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	observer := sseObserver{adapter: modelbinding.AdapterAnthropic, beichen: true, expectedModel: "bound"}
	observer.observe([]byte(opening + terminal))
	metrics, reason := beichenReviewMetrics(observer.adapter, observer.metrics.finished(!observer.terminal), observer.review)
	if !observer.modelReported || reason != "" || metrics.State != usage.UsageReported {
		t.Fatalf("Anthropic stream was not eligible for token pricing: %q, %+v", reason, metrics)
	}
	mismatch := sseObserver{adapter: modelbinding.AdapterAnthropic, beichen: true, expectedModel: "different"}
	mismatch.observe([]byte(opening + terminal))
	if mismatch.review != beichenModelMismatch {
		t.Fatalf("nested model mismatch was not detected: %q", mismatch.review)
	}
}
