package catalog

import (
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func TestIsImageGenerationModel(t *testing.T) {
	t.Parallel()
	for _, modelID := range []string{"gpt-image-2", "GPT-Image-2", "dall-e-3"} {
		if !IsImageGenerationModel(modelID) {
			t.Fatalf("expected %q to be image model", modelID)
		}
	}
	for _, modelID := range []string{"gpt-5.6-luna", "gpt-5.3-codex-spark", "claude-sonnet-4-6"} {
		if IsImageGenerationModel(modelID) {
			t.Fatalf("expected %q not to be image model", modelID)
		}
	}
}

func TestUsesDualChatBindings(t *testing.T) {
	t.Parallel()
	if UsesDualChatBindings("gpt-image-2") {
		t.Fatal("image model should not use dual chat bindings")
	}
	if !UsesDualChatBindings("gpt-5.6-luna") {
		t.Fatal("text model should use dual chat bindings")
	}
}

func TestDefaultAdaptersForModel(t *testing.T) {
	t.Parallel()
	imageAdapters := DefaultAdaptersForModel("gpt-image-2")
	if len(imageAdapters) != 1 || imageAdapters[0] != modelbinding.AdapterOpenaiImages {
		t.Fatalf("unexpected image adapters: %#v", imageAdapters)
	}
	chatAdapters := DefaultAdaptersForModel("gpt-5.6-luna")
	found := map[modelbinding.Adapter]bool{}
	for _, adapter := range chatAdapters {
		found[adapter] = true
	}
	if !found[modelbinding.AdapterOpenaiCompatible] || !found[modelbinding.AdapterOpenaiResponses] {
		t.Fatalf("expected chat pair, got %#v", chatAdapters)
	}
	claudeAdapters := DefaultAdaptersForModel("claude-sonnet-5")
	found = map[modelbinding.Adapter]bool{}
	for _, adapter := range claudeAdapters {
		found[adapter] = true
	}
	if !found[modelbinding.AdapterAnthropic] || !found[modelbinding.AdapterOpenaiCompatible] || !found[modelbinding.AdapterOpenaiResponses] {
		t.Fatalf("expected claude harness trio, got %#v", claudeAdapters)
	}
	if !IsAnthropicFamilyModel("claude-sonnet-5") || IsAnthropicFamilyModel("gpt-5.6-luna") {
		t.Fatal("unexpected anthropic family classification")
	}
	if !IsOpenAIModelsListable("gpt-5.6-luna") || IsOpenAIModelsListable("claude-sonnet-5") {
		t.Fatal("openai models list should exclude claude family")
	}
}

func TestExpandChatGatewayAdapters(t *testing.T) {
	t.Parallel()
	expanded := ExpandChatGatewayAdapters("gpt-5.6-luna", []modelbinding.Adapter{modelbinding.AdapterOpenaiCompatible})
	found := map[modelbinding.Adapter]bool{}
	for _, adapter := range expanded {
		found[adapter] = true
	}
	if !found[modelbinding.AdapterOpenaiResponses] {
		t.Fatalf("expected responses companion, got %#v", expanded)
	}
	if found[modelbinding.AdapterAnthropic] {
		t.Fatal("anthropic must not be inferred from chat adapters")
	}
	if ExpandChatGatewayAdapters("gpt-image-2", []modelbinding.Adapter{modelbinding.AdapterOpenaiImages})[0] != modelbinding.AdapterOpenaiImages {
		t.Fatal("image models should not expand chat adapters")
	}
}

func TestDefaultBindingPriority(t *testing.T) {
	t.Parallel()
	if DefaultBindingPriority(modelbinding.AdapterOpenaiResponses) != 50 {
		t.Fatal("responses should default to priority 50")
	}
	if DefaultBindingPriority(modelbinding.AdapterAnthropic) != 50 {
		t.Fatal("anthropic should default to priority 50")
	}
	if DefaultBindingPriority(modelbinding.AdapterOpenaiCompatible) != 100 {
		t.Fatal("chat should default to priority 100")
	}
}
