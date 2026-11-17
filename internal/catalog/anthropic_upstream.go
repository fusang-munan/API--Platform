package catalog

import (
	"net/url"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

// AnthropicUpstreamBase returns the provider base used for Anthropic Messages.
// DeepSeek keeps the OpenAI catalog at https://api.deepseek.com/v1/models and
// serves Claude Code at https://api.deepseek.com/anthropic/v1/messages.
// Other providers already expose /v1/messages on their configured base.
func AnthropicUpstreamBase(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" {
		return trimmed
	}
	if !strings.EqualFold(parsed.Hostname(), "api.deepseek.com") {
		return trimmed
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.Contains(strings.ToLower(path), "/anthropic") {
		return trimmed
	}
	if path == "/v1" {
		path = ""
	}
	parsed.Path = path + "/anthropic"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

// UpstreamBaseForAdapter selects the provider base for one binding adapter.
func UpstreamBaseForAdapter(baseURL string, adapter modelbinding.Adapter) string {
	if adapter == modelbinding.AdapterAnthropic {
		return AnthropicUpstreamBase(baseURL)
	}
	return strings.TrimSpace(baseURL)
}
