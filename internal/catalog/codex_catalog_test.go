package catalog

import "testing"

func TestBuildCodexModelCatalogIncludesReasoningLevels(t *testing.T) {
	t.Parallel()
	doc := BuildCodexModelCatalog(
		[]string{"gpt-5.6-luna", "claude-sonnet-5"},
		map[string]string{"gpt-5.6-luna": "Luna"},
	)
	if len(doc.Models) != 1 || doc.Models[0].Slug != "gpt-5.6-luna" {
		t.Fatalf("expected only codex-listable model, got %#v", doc.Models)
	}
	if len(doc.Models[0].SupportedReasoningLevels) != 5 {
		t.Fatalf("expected official gpt-5.6-luna reasoning levels, got %#v", doc.Models[0].SupportedReasoningLevels)
	}
	if doc.Models[0].SupportedReasoningLevels[0].Effort != "low" || doc.Models[0].SupportedReasoningLevels[4].Effort != "max" {
		t.Fatalf("expected low..max for gpt-5.6-luna, got %#v", doc.Models[0].SupportedReasoningLevels)
	}

	docSol := BuildCodexModelCatalog([]string{"gpt-5.6-sol"}, nil)
	if len(docSol.Models[0].SupportedReasoningLevels) != 6 {
		t.Fatalf("expected official gpt-5.6-sol reasoning levels incl. ultra, got %#v", docSol.Models[0].SupportedReasoningLevels)
	}

	docCodex := BuildCodexModelCatalog([]string{"gpt-5.3-codex"}, nil)
	if len(docCodex.Models[0].SupportedReasoningLevels) != 3 {
		t.Fatalf("expected low/medium/high fallback for unknown model, got %#v", docCodex.Models[0].SupportedReasoningLevels)
	}
	if doc.Models[0].DefaultReasoningLevel != "" {
		t.Fatalf("expected no forced default reasoning level, got %q", doc.Models[0].DefaultReasoningLevel)
	}
	if doc.Models[0].BaseInstructions == "" {
		t.Fatal("expected base_instructions so Codex can load reasoning picker metadata")
	}
}
