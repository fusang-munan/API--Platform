package catalog

import (
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func IsChatGatewayAdapter(adapter modelbinding.Adapter) bool {
	switch adapter {
	case modelbinding.AdapterOpenaiCompatible, modelbinding.AdapterOpenaiResponses:
		return true
	default:
		return false
	}
}

func ChatGatewayAdapterSet() []modelbinding.Adapter {
	return []modelbinding.Adapter{
		modelbinding.AdapterOpenaiResponses,
		modelbinding.AdapterOpenaiCompatible,
	}
}

func CompanionChatGatewayAdapter(adapter modelbinding.Adapter) (modelbinding.Adapter, bool) {
	switch adapter {
	case modelbinding.AdapterOpenaiCompatible:
		return modelbinding.AdapterOpenaiResponses, true
	case modelbinding.AdapterOpenaiResponses:
		return modelbinding.AdapterOpenaiCompatible, true
	default:
		return "", false
	}
}

func DefaultBindingPriority(adapter modelbinding.Adapter) int {
	switch adapter {
	case modelbinding.AdapterOpenaiResponses, modelbinding.AdapterAnthropic:
		return 50
	default:
		return 100
	}
}

// IsImageGenerationModel reports models that only route through /v1/images/* (e.g. gpt-image-2).
func IsImageGenerationModel(modelID string) bool {
	normalized := NormalizeSignal(modelID)
	for _, token := range []string{"gpt-image", "dall-e", "dalle"} {
		if containsToken(normalized, token) {
			return true
		}
	}
	return false
}

// UsesDualChatBindings reports whether a model should always expose Chat + Responses bindings.
func UsesDualChatBindings(modelID string) bool {
	return strings.TrimSpace(modelID) != "" && !IsImageGenerationModel(modelID)
}

// IsOpenAIModelsListable reports models suitable for the dedicated Codex catalog.
// Claude-family models are routed via /v1/messages and belong in the Claude Code harness only.
func IsOpenAIModelsListable(modelID string) bool {
	return !IsAnthropicFamilyModel(modelID)
}

// IsAnthropicFamilyModel reports Claude Code model IDs that require /v1/messages routing.
func IsAnthropicFamilyModel(modelID string) bool {
	normalized := NormalizeSignal(modelID)
	if normalized == "" || IsImageGenerationModel(modelID) {
		return false
	}
	if strings.HasPrefix(normalized, "gpt") {
		return false
	}
	for _, token := range []string{"claude", "opus", "sonnet", "haiku"} {
		if containsToken(normalized, token) {
			return true
		}
	}
	return false
}

func DefaultAdaptersForModel(modelID string) []modelbinding.Adapter {
	if IsImageGenerationModel(modelID) {
		return []modelbinding.Adapter{modelbinding.AdapterOpenaiImages}
	}
	if IsAnthropicFamilyModel(modelID) {
		return []modelbinding.Adapter{
			modelbinding.AdapterAnthropic,
			modelbinding.AdapterOpenaiResponses,
			modelbinding.AdapterOpenaiCompatible,
		}
	}
	return ChatGatewayAdapterSet()
}

// ExpandChatGatewayAdapters adds the missing OpenAI Chat/Responses companion for Codex harness.
// Anthropic (/v1/messages) is never inferred here — it must be route-probed explicitly.
func ExpandChatGatewayAdapters(modelID string, adapters []modelbinding.Adapter) []modelbinding.Adapter {
	if !UsesDualChatBindings(modelID) {
		return adapters
	}
	hasChat := false
	for _, adapter := range adapters {
		if IsChatGatewayAdapter(adapter) {
			hasChat = true
			break
		}
	}
	if !hasChat {
		return adapters
	}
	seen := make(map[modelbinding.Adapter]struct{}, len(adapters)+len(ChatGatewayAdapterSet()))
	result := make([]modelbinding.Adapter, 0, len(adapters)+len(ChatGatewayAdapterSet()))
	for _, adapter := range adapters {
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		result = append(result, adapter)
	}
	for _, adapter := range ChatGatewayAdapterSet() {
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		result = append(result, adapter)
	}
	return result
}
