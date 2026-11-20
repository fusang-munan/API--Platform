package catalog

import (
	"slices"
	"testing"
)

func TestParseBeichenModelsRegistersDeclaredProtocols(t *testing.T) {
	entries, err := ParseBeichenModels([]byte(`{"data":[{"id":"shared",
		"supported_protocols":["/v1/messages","google_gemini_v1beta","anthropic","unknown-protocol"]}]}`))
	if err != nil || len(entries) != 1 {
		t.Fatalf("parse Beichen catalog: %v", err)
	}
	if !slices.Equal(entries[0].SupportedProtocols, []string{"anthropic", "google_gemini_v1beta"}) {
		t.Fatalf("registered protocols: %v", entries[0].SupportedProtocols)
	}
}

func TestParseProviderModels_llmapiShape(t *testing.T) {
	body := []byte(`{
		"data": [
			{
				"id": "gpt-5.3-codex",
				"type": "model",
				"created_at": "2024-01-01T00:00:00Z",
				"display_name": "gpt-5.3-codex"
			}
		]
	}`)
	entries, err := ParseProviderModels(body)
	if err != nil {
		t.Fatalf("parse models: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.ID != "gpt-5.3-codex" {
		t.Fatalf("unexpected id: %s", entry.ID)
	}
	if entry.Object != "model" {
		t.Fatalf("unexpected object/type: %s", entry.Object)
	}
	if entry.DisplayName != "gpt-5.3-codex" {
		t.Fatalf("unexpected display name: %s", entry.DisplayName)
	}
	if entry.CreatedAt != "2024-01-01T00:00:00Z" {
		t.Fatalf("unexpected created_at: %s", entry.CreatedAt)
	}
	if entry.Description != "" || entry.OwnedBy != "" {
		t.Fatalf("expected empty description/owned_by, got %q / %q", entry.Description, entry.OwnedBy)
	}
}

func TestParseProviderModels_openaiShape(t *testing.T) {
	body := []byte(`{
		"object": "list",
		"data": [
			{
				"id": "gpt-4.1",
				"object": "model",
				"created": 1686935002,
				"owned_by": "openai"
			}
		]
	}`)
	entries, err := ParseProviderModels(body)
	if err != nil {
		t.Fatalf("parse models: %v", err)
	}
	entry := entries[0]
	if entry.ID != "gpt-4.1" || entry.Object != "model" || entry.OwnedBy != "openai" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.CreatedAt == "" {
		t.Fatalf("expected created timestamp")
	}
}
