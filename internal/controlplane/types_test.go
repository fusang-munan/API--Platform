package controlplane

import "testing"

func TestNormalizeModelDescriptionDropsRedundantParentheses(t *testing.T) {
	modelID := "gpt-5.6-terra"
	description := "gpt-5.6-terra（gpt-5.6-terra）"
	normalized := normalizeModelDescription(modelID, modelID, &description)
	if normalized != nil {
		t.Fatalf("expected redundant description to be cleared, got %q", *normalized)
	}
}

func TestNormalizeModelDescriptionKeepsUsefulText(t *testing.T) {
	modelID := "gpt-5.6-terra"
	displayName := "GPT Terra"
	description := "Fast reasoning model"
	normalized := normalizeModelDescription(modelID, displayName, &description)
	if normalized == nil || *normalized != description {
		t.Fatalf("expected description to remain, got %#v", normalized)
	}
}
