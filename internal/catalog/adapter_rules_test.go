package catalog

import (
	"slices"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func TestResolveAdapterUsesTokenRules(t *testing.T) {
	tests := []struct {
		signal string
		want   modelbinding.Adapter
	}{
		{"/v1/chat/completions", modelbinding.AdapterOpenaiCompatible},
		{"/v1/embeddings", modelbinding.AdapterOpenaiEmbeddings},
		{"/v1/messages", modelbinding.AdapterAnthropic},
		{"/v2/rerank", modelbinding.AdapterCohereRerankV2},
		{"openai-response", modelbinding.AdapterOpenaiResponses},
		{"custom-provider-embeddings-v2", modelbinding.AdapterOpenaiEmbeddings},
		{"/v1beta/models/gemini-pro:generateContent", modelbinding.AdapterGoogleGeminiV1beta},
		{"/v1beta/models/custom:streamGenerateContent", modelbinding.AdapterGoogleGeminiV1beta},
		{"/v1beta/models/custom:embedContent", modelbinding.AdapterGoogleGeminiV1beta},
		{"/v1beta/models/custom-rerank:embedContent", modelbinding.AdapterGoogleGeminiV1beta},
		{"openai-embeddings-v1beta", modelbinding.AdapterOpenaiEmbeddings},
		{"gemini-embedding-001", modelbinding.AdapterGoogleGeminiV1beta},
		{"embedContent", modelbinding.AdapterGoogleGeminiV1beta},
	}
	for _, test := range tests {
		adapter, ok := ResolveAdapter(test.signal)
		if !ok || adapter != test.want {
			t.Fatalf("signal %q => %#v ok=%v, want %s", test.signal, adapter, ok, test.want)
		}
	}
}

func TestGeminiCatalogProtocols(t *testing.T) {
	for _, tc := range []struct {
		entry ProviderEntry
		want  modelbinding.Adapter
	}{
		{ProviderEntry{ID: "gemini-pro"}, modelbinding.AdapterGoogleGeminiV1beta},
		{ProviderEntry{ID: "gemini-embedding-001"}, modelbinding.AdapterGoogleGeminiV1beta},
		{ProviderEntry{ID: "custom", Raw: map[string]any{"supportedGenerationMethods": []any{"embedContent"}}},
			modelbinding.AdapterGoogleGeminiV1beta},
		{ProviderEntry{ID: "gemini-embedding-001", Raw: map[string]any{"endpoints": []any{"/v1/embeddings"}}},
			modelbinding.AdapterOpenaiEmbeddings},
	} {
		if got := RegisteredProtocols(tc.entry); !slices.Equal(got, []string{string(tc.want)}) {
			t.Errorf("%s: registered %v, want %s", tc.entry.ID, got, tc.want)
		}
		if got := AdaptersFromUpstream(tc.entry); !slices.Equal(got, []modelbinding.Adapter{tc.want}) {
			t.Errorf("%s: bindings %v, want %s", tc.entry.ID, got, tc.want)
		}
	}
}

func TestAdaptersFromUpstreamUsesChatPairForTextModels(t *testing.T) {
	for _, modelID := range []string{"gpt-5.3-codex", "claude-sonnet-4", "gpt-5.6-sol", "deepseek-v3"} {
		adapters := AdaptersFromUpstream(ProviderEntry{ID: modelID, Raw: map[string]any{"type": "model"}})
		found := map[modelbinding.Adapter]bool{}
		for _, adapter := range adapters {
			found[adapter] = true
		}
	if !found[modelbinding.AdapterOpenaiCompatible] || !found[modelbinding.AdapterOpenaiResponses] {
		t.Fatalf("model %q expected chat pair, got %#v", modelID, adapters)
	}
}
}

func TestAdaptersFromUpstreamUsesImagesOnlyForImageModels(t *testing.T) {
	entry := ProviderEntry{ID: "gpt-image-2", Raw: map[string]any{"type": "model"}}
	adapters := AdaptersFromUpstream(entry)
	if len(adapters) != 1 || adapters[0] != modelbinding.AdapterOpenaiImages {
		t.Fatalf("expected image adapter from model id, got %#v", adapters)
	}
}

func TestAdaptersFromUpstreamUsesEndpointSignalsWhenPresent(t *testing.T) {
	entry := ProviderEntry{
		ID: "claude-sonnet-4",
		Raw: map[string]any{
			"supported_endpoint_types": []any{"anthropic", "openai-chat"},
		},
	}
	adapters := AdaptersFromUpstream(entry)
	found := map[modelbinding.Adapter]bool{}
	for _, adapter := range adapters {
		found[adapter] = true
	}
	if !found[modelbinding.AdapterAnthropic] || !found[modelbinding.AdapterOpenaiCompatible] || !found[modelbinding.AdapterOpenaiResponses] {
		t.Fatalf("expected anthropic and chat pair from endpoint signals: %#v", adapters)
	}
}

func TestParseUpstreamMetadataDoesNotGuessFromModelID(t *testing.T) {
	meta := ParseUpstreamMetadata(map[string]any{"id": "qwen3-vl-think"})
	if meta.Category != nil || len(meta.Capabilities) > 0 || meta.ContextWindow != nil {
		t.Fatalf("expected empty metadata, got %#v", meta)
	}
}

func TestParseUpstreamMetadataReadsAliasedFields(t *testing.T) {
	meta := ParseUpstreamMetadata(map[string]any{
		"model_category":    "multimodal",
		"features":            []any{"vision", "tool_calling"},
		"input_modes":         []any{"text", "image"},
		"output_modes":        []any{"text"},
		"context_length":      200000,
		"prompt_token_limit":  200000,
		"completion_token_limit": 8192,
	})
	if meta.Category == nil || string(*meta.Category) != "multimodal" {
		t.Fatalf("unexpected category: %#v", meta.Category)
	}
	if len(meta.Capabilities) != 2 || meta.ContextWindow == nil || *meta.ContextWindow != 200000 {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
}

func TestResolveStringSliceAcceptsDelimitedString(t *testing.T) {
	values, ok := ResolveStringSlice(map[string]any{"capabilities": "vision, tool_calling"}, "capabilities")
	if !ok || len(values) != 2 {
		t.Fatalf("unexpected values: %#v ok=%v", values, ok)
	}
}
