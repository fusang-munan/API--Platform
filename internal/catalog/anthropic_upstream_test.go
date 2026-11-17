package catalog

import "testing"

func TestAnthropicUpstreamBase(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.deepseek.com":           "https://api.deepseek.com/anthropic",
		"https://api.deepseek.com/":          "https://api.deepseek.com/anthropic",
		"https://api.deepseek.com/v1":        "https://api.deepseek.com/anthropic",
		"https://api.deepseek.com/anthropic": "https://api.deepseek.com/anthropic",
		"https://api.llmapi.site":            "https://api.llmapi.site",
		"https://fx.rjj.cc/claude/super":     "https://fx.rjj.cc/claude/super",
	}
	for input, want := range cases {
		if got := AnthropicUpstreamBase(input); got != want {
			t.Fatalf("AnthropicUpstreamBase(%q) = %q, want %q", input, got, want)
		}
	}
}
