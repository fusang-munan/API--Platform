package catalog

import "testing"

func TestFilterProviderEntriesByLine(t *testing.T) {
	entries := []ProviderEntry{
		{ID: "claude-opus-5"},
		{ID: "gpt-5.4"},
		{ID: "codex-auto-review"},
	}

	codex := FilterProviderEntriesByCredential("codex", nil, "https://fx.rjj.cc/codex", entries)
	if len(codex) != 2 || codex[0].ID != "gpt-5.4" {
		t.Fatalf("unexpected codex filter: %+v", codex)
	}

	claude := FilterProviderEntriesByCredential("claude", nil, "https://fx.rjj.cc/codex", entries)
	if len(claude) != 1 || claude[0].ID != "claude-opus-5" {
		t.Fatalf("unexpected claude filter: %+v", claude)
	}

	plain := FilterProviderEntriesByCredential("default", nil, "https://api.llmapi.site", entries)
	if len(plain) != 3 {
		t.Fatalf("unexpected plain filter: %+v", plain)
	}
}
