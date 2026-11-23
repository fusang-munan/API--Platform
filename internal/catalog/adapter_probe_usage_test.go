package catalog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenProbeUsageAndIdentification(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body       string
		reason, errorCode          string
		input, output, read, write int64 // -1 denotes an absent field
	}{
		{"chat fields", "openai_compatible", `{"choices":[],"usage":{"prompt_tokens":17,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":10},"prompt_cache_write_tokens":2}}`, "", "", 17, 0, 10, 2},
		{"responses fields", "openai_responses", `{"output":[],"usage":{"input_tokens":9,"output_tokens":3,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":0}}}`, "", "", 9, 3, 2, 0},
		{"anthropic fields", "anthropic", `{"content":[],"usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}`, "", "", 5, 1, 7, 5},
		{"gemini fields", "google_gemini_v1beta", `{"candidates":[],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1,"cachedContentTokenCount":4}}`, "", "", 8, 1, 4, -1},
		{"invalid counter", "openai_compatible", `{"choices":[],"usage":{"prompt_tokens":-2,"completion_tokens":1}}`, "invalid_token_count", "invalid_usage", -1, -1, -1, -1},
		{"conflicting hit aliases", "openai_compatible", `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2},"prompt_cache_hit_tokens":8}}`, "conflicting_usage_fields", "probe_usage_conflict", -1, -1, -1, -1},
		{"agreeing hit aliases", "openai_compatible", `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2},"prompt_cache_hit_tokens":2}}`, "", "", 10, 1, 2, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != "Nebula-Probe/1.0" || r.Header.Get("X-Nebula-Probe") != "1" {
					t.Error("missing probe identification")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			step, err := NewRouteProbe(server.URL, "fixture").ProbeTokenStep(context.Background(), "model", tc.protocol)
			if err != nil || !step.OK {
				t.Fatalf("step=%+v err=%v", step, err)
			}
			if step.Usage.Reason != tc.reason || step.Usage.AccountingErrorCode() != tc.errorCode {
				t.Fatalf("usage reason=%q code=%q, want %q %q", step.Usage.Reason, step.Usage.AccountingErrorCode(), tc.reason, tc.errorCode)
			}
			got := []*int64{step.Usage.InputTokens, step.Usage.OutputTokens, step.Usage.CacheReadTokens, step.Usage.CacheWriteTokens}
			for i, want := range []int64{tc.input, tc.output, tc.read, tc.write} {
				if want < 0 && got[i] != nil || want >= 0 && (got[i] == nil || *got[i] != want) {
					t.Errorf("counter %d = %v, want %d", i, got[i], want)
				}
			}
		})
	}
}
