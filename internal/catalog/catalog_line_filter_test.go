package catalog

import "testing"

func TestBeichenUsesActualCredentialCatalog(t *testing.T) {
	entries := []ProviderEntry{{ID: "claude-a"}, {ID: "gemini-b"}, {ID: "deepseek-c"}}
	for _, base := range []string{"https://api.beichenapi.com", " https://API.BEICHENAPI.COM/v1/ "} {
		for _, label := range []string{"Gemini", "gpt", "Claude"} {
			if got := FilterProviderEntriesByCredential(label, &label, base, entries); len(got) != len(entries) {
				t.Fatalf("%s %s: lost catalog entries: %v", base, label, got)
			}
		}
	}
	for _, other := range []string{"https://api.beichenapi.com.other.test", "http://api.beichenapi.com", "https://api.beichenapi.com/other"} {
		if got := FilterProviderEntriesByCredential("Gemini", nil, other, entries); len(got) != 1 || got[0].ID != "gemini-b" {
			t.Fatalf("%s: changed non-Beichen filtering: %v", other, got)
		}
	}
	if rows, err := ParseBeichenModels([]byte(`{"object":"list","data":[]}`)); err != nil || len(rows) != 0 {
		t.Fatalf("explicit empty catalog: %v, %v", rows, err)
	}
	for _, body := range []string{`{}`, `{"data":null}`, `{"error":{"message":"failed"},"data":[]}`,
		`{"code":401,"data":[]}`, `{"data":[{"id":"valid"},{}]}`} {
		if _, err := ParseBeichenModels([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete catalog %s", body)
		}
	}
}

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
