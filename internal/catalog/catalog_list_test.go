package catalog

import "testing"

func TestFilterAndPaginateProviderEntries(t *testing.T) {
	entries := []ProviderEntry{
		{ID: "gpt-5", DisplayName: "GPT 5"},
		{ID: "claude-4", DisplayName: "Claude 4"},
		{ID: "kimi-k2", DisplayName: "Kimi K2"},
	}

	filtered := FilterProviderEntries(entries, "claude")
	if len(filtered) != 1 || filtered[0].ID != "claude-4" {
		t.Fatalf("unexpected filter result: %+v", filtered)
	}

	sorted := FilterProviderEntries(entries, "")
	if sorted[0].ID != "claude-4" || sorted[2].ID != "kimi-k2" {
		t.Fatalf("unexpected sort order: %+v", sorted)
	}

	page, total := PaginateProviderEntries(sorted, 2, 2)
	if total != 3 || len(page) != 1 || page[0].ID != "kimi-k2" {
		t.Fatalf("unexpected page: total=%d page=%+v", total, page)
	}
}
