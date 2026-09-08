package catalog

import "testing"

func TestUnionProviderEntriesDedupesByModelID(t *testing.T) {
	union := UnionProviderEntries(
		[]ProviderEntry{{ID: "gpt-4o", DisplayName: "GPT-4o"}},
		[]ProviderEntry{{ID: " GPT-4o ", Description: "from second key"}, {ID: "claude-3-5-sonnet"}},
	)
	if len(union) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(union))
	}
	if union[0].ID != "gpt-4o" || union[0].Description != "from second key" {
		t.Fatalf("unexpected merged entry: %#v", union[0])
	}
}
