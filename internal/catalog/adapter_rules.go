package catalog

import (
	"slices"
	"sort"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

// AdapterRule matches upstream endpoint/path hints to a gateway binding adapter.
// Rules are ordered by priority (higher first); first match wins.
type AdapterRule struct {
	Adapter    modelbinding.Adapter
	Priority   int
	AllTokens  []string
	AnyTokens  []string
	NoneTokens []string
}

var adapterRules = []AdapterRule{
	{Adapter: modelbinding.AdapterGoogleGeminiV1beta, Priority: 110, AnyTokens: []string{"v1beta-models", "generatecontent", "embedcontent", "generate-content", "embed-content"}},
	{Adapter: modelbinding.AdapterCohereRerankV2, Priority: 100, AnyTokens: []string{"rerank"}},
	{Adapter: modelbinding.AdapterGoogleGeminiV1beta, Priority: 92, AnyTokens: []string{"gemini"}, NoneTokens: []string{"openai"}},
	{Adapter: modelbinding.AdapterOpenaiEmbeddings, Priority: 90, AnyTokens: []string{"embed"}},
	{Adapter: modelbinding.AdapterGoogleGeminiV1beta, Priority: 85, AnyTokens: []string{"v1beta"}},
	{Adapter: modelbinding.AdapterAnthropic, Priority: 80, AnyTokens: []string{"anthropic", "claude"}, NoneTokens: []string{"openai"}},
	{Adapter: modelbinding.AdapterAnthropic, Priority: 79, AllTokens: []string{"message"}, NoneTokens: []string{"openai", "embed", "image"}},
	{Adapter: modelbinding.AdapterOpenaiResponses, Priority: 70, AnyTokens: []string{"response"}, NoneTokens: []string{"embed"}},
	{Adapter: modelbinding.AdapterOpenaiImages, Priority: 65, AnyTokens: []string{"image", "dall", "draw", "diffusion"}},
	{Adapter: modelbinding.AdapterOpenaiAudio, Priority: 60, AnyTokens: []string{"audio", "speech", "tts", "whisper", "transcribe"}},
	{Adapter: modelbinding.AdapterOpenaiVideo, Priority: 55, AnyTokens: []string{"video", "sora", "kling", "vidu"}},
	{Adapter: modelbinding.AdapterOpenaiRealtime, Priority: 50, AnyTokens: []string{"realtime", "live"}},
	{Adapter: modelbinding.AdapterOpenaiModerations, Priority: 45, AnyTokens: []string{"moderation", "moderate"}},
	{Adapter: modelbinding.AdapterOpenaiCompatible, Priority: 10, AnyTokens: []string{"chat", "completion", "openai", "llm", "text"}},
}

func init() {
	sort.SliceStable(adapterRules, func(i, j int) bool {
		return adapterRules[i].Priority > adapterRules[j].Priority
	})
}

func ResolveAdapter(signal string) (modelbinding.Adapter, bool) {
	normalized := NormalizeSignal(signal)
	if normalized == "" {
		return "", false
	}
	for _, rule := range adapterRules {
		if rule.matches(normalized) {
			return rule.Adapter, true
		}
	}
	return "", false
}

func ResolveAdapterFromRequestPath(path string) modelbinding.Adapter {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/v1beta/models/") {
		return modelbinding.AdapterGoogleGeminiV1beta
	}
	if adapter, ok := ResolveAdapter(path); ok {
		return adapter
	}
	return modelbinding.AdapterOpenaiCompatible
}

func AdaptersFromSignals(signals ...string) []modelbinding.Adapter {
	seen := map[modelbinding.Adapter]struct{}{}
	result := make([]modelbinding.Adapter, 0, len(signals))
	for _, signal := range signals {
		adapter, ok := ResolveAdapter(signal)
		if !ok {
			continue
		}
		if _, exists := seen[adapter]; exists {
			continue
		}
		seen[adapter] = struct{}{}
		result = append(result, adapter)
	}
	return result
}

func AdaptersFromUpstream(entry ProviderEntry) []modelbinding.Adapter {
	if adapters := AdaptersFromCatalogSignals(entry); len(adapters) > 0 {
		return adapters
	}
	return DefaultAdaptersForModel(entry.ID)
}

// RegisteredProtocols preserves an explicit list without inferring companion
// protocols. Legacy catalog rows with no declaration still use model defaults.
func RegisteredProtocols(entry ProviderEntry) []string {
	values := entry.SupportedProtocols
	if len(values) == 0 {
		values = extractStringSlice(entry.Raw, "supported_protocols")
	}
	if len(values) == 0 {
		values = explicitEndpointSignals(entry)
	}
	protocols := make([]string, 0, len(values))
	for _, value := range values {
		adapter := modelbinding.Adapter(strings.ToLower(strings.TrimSpace(value)))
		if modelbinding.AdapterValidator(adapter) != nil {
			var ok bool
			adapter, ok = ResolveAdapter(value)
			if !ok {
				continue
			}
		}
		if !slices.Contains(protocols, string(adapter)) {
			protocols = append(protocols, string(adapter))
		}
	}
	if len(values) == 0 && !hasDeclaredProtocolList(entry.Raw) {
		for _, adapter := range AdaptersFromUpstream(entry) {
			protocols = append(protocols, string(adapter))
		}
	}
	return protocols
}

func hasDeclaredProtocolList(raw map[string]any) bool {
	for _, key := range []string{"supported_protocols", "supported_endpoint_types", "endpoints", "endpoint_types", "supportedGenerationMethods"} {
		switch raw[key].(type) {
		case []any, []string:
			return true
		}
	}
	return false
}

func explicitEndpointSignals(entry ProviderEntry) []string {
	signals := extractStringSlice(entry.Raw, "supported_endpoint_types")
	if len(signals) == 0 {
		signals = extractStringSlice(entry.Raw, "endpoints", "endpoint_types")
	}
	if len(signals) == 0 {
		signals = extractStringSlice(entry.Raw, "supportedGenerationMethods")
	}
	return cleanStringList(signals)
}

func specialtyAdapterFromModelID(modelID string) (modelbinding.Adapter, bool) {
	normalized := NormalizeSignal(modelID)
	for _, rule := range adapterRules {
		if rule.Adapter == modelbinding.AdapterOpenaiCompatible || rule.Adapter == modelbinding.AdapterAnthropic {
			continue
		}
		if rule.matches(normalized) {
			return rule.Adapter, true
		}
	}
	return "", false
}

func EndpointSignals(entry ProviderEntry) []string {
	signals := explicitEndpointSignals(entry)
	for _, key := range []string{"endpoint", "endpoint_type", "adapter", "protocol", "type", "object"} {
		if value, ok := ResolveString(entry.Raw, key); ok {
			signals = append(signals, value)
		}
	}
	return cleanStringList(signals)
}

func NormalizeSignal(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("_", "-", " ", "-", "/", "-", ".", "-", ":", "-")
	value = replacer.Replace(value)
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return strings.Trim(value, "-")
}

func (rule AdapterRule) matches(signal string) bool {
	for _, token := range rule.AllTokens {
		if !containsToken(signal, token) {
			return false
		}
	}
	if len(rule.AnyTokens) > 0 {
		matched := false
		for _, token := range rule.AnyTokens {
			if containsToken(signal, token) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, token := range rule.NoneTokens {
		if containsToken(signal, token) {
			return false
		}
	}
	return len(rule.AllTokens) > 0 || len(rule.AnyTokens) > 0
}

func containsToken(signal, token string) bool {
	token = NormalizeSignal(token)
	if token == "" {
		return false
	}
	if signal == token {
		return true
	}
	return strings.Contains(signal, token)
}
