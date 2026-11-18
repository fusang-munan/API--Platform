package dataplane

import (
	"testing"
	"time"

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

func TestBeichenPricingRequiresCompleteCompatibleCounters(t *testing.T) {
	for _, tc := range []struct{ usage, reason string }{
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, ""},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cache_write_tokens":0}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":0}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"input_tokens_details":{"cached_tokens":58,"cache_write_tokens":2}}`, beichenIncompleteUsage},
		{`{"input_tokens":59,"output_tokens":3,"total_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, beichenIncompleteUsage},
	} {
		route := usage.CallRoute{SentAt: time.Now()}
		response := parseJSONResponseMetrics(modelbinding.AdapterOpenaiResponses, []byte(`{"usage":`+tc.usage+`}`)).finished(false)
		metrics, reason := beichenReviewMetrics(&route, response, "")
		if reason != tc.reason || (metrics.State == usage.UsageReported) != (tc.reason == "") ||
			metrics.PromptTokens == nil || *metrics.PromptTokens != 59 || route.SentAt.IsZero() {
			t.Fatalf("unexpected pricing eligibility for %s: %q, %+v", tc.usage, reason, metrics)
		}
	}
}
