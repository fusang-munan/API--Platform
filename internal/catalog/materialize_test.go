package catalog

import "testing"

func TestUpstreamNameSetNormalizesCase(t *testing.T) {
	set := upstreamNameSet([]ProviderEntry{
		{ID: " GPT-4o "},
		{ID: "claude-3-5-sonnet"},
		{ID: ""},
	})
	if _, ok := set["gpt-4o"]; !ok {
		t.Fatal("expected normalized upstream name")
	}
	if _, ok := set["claude-3-5-sonnet"]; !ok {
		t.Fatal("expected claude upstream name")
	}
	if len(set) != 2 {
		t.Fatalf("expected 2 names, got %d", len(set))
	}
}
