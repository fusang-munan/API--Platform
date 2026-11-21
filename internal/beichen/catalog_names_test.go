package beichen

import (
	"slices"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

func TestBeichenDirectoryNameMatching(t *testing.T) {
	tests := []struct {
		name, query string
		names       []string
		aliases     map[string]string
		want        modelNameMatch
	}{
		{"exact wins", "GPT-6.1-SOL", []string{"GPT-6.1-SOL", "gpt_6.1_sol"}, map[string]string{"gpt-6.1-sol": "other"}, modelNameMatch{"GPT-6.1-SOL", "exact"}},
		{"format wins over alias", " GPT 6.1 Sol ", []string{"gpt-6.1-sol", "other"}, map[string]string{"gpt 6.1 sol": "other"}, modelNameMatch{"gpt-6.1-sol", "normalized"}},
		{"explicit dated spellings", "GPT-Mini 2026-11-20", []string{"gpt_mini-20261120"}, nil, modelNameMatch{"gpt_mini-20261120", "normalized"}},
		{"one pinned option", "GPT 6.2 Sol", []string{"gpt-6.2-sol-2026-11-20"}, nil, modelNameMatch{"gpt-6.2-sol-2026-11-20", "normalized"}},
		{"latest selector", "Grok-4.1-latest", []string{"grok-4.1"}, nil, modelNameMatch{"grok-4.1", "normalized"}},
		{"ambiguous pinned options", "GPT 6.2 Sol", []string{"gpt-6.2-sol-2026-11-20", "gpt-6.2-sol-2026-11-21"}, nil, modelNameMatch{kind: "ambiguous"}},
		{"distinct pinned versions", "gpt-6.2-sol-20261121", []string{"gpt-6.2-sol-20261120"}, nil, modelNameMatch{}},
		{"distinct model versions", "gpt-6.2-sol", []string{"gpt-6.1-sol", "gpt-62-sol"}, nil, modelNameMatch{}},
		{"distinct model variants", "grok-4.3-fast", []string{"grok-4.3", "grok-4.3-preview"}, nil, modelNameMatch{}},
		{"teacher alias", "website-label", []string{"supplier-label"}, map[string]string{"website-label": "school", "supplier-label": "school"}, modelNameMatch{"supplier-label", "alias"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := newCatalogNameMatcher(test.names, test.aliases).match(test.query, true); got != test.want {
				t.Fatalf("match = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestBeichenOneKeyIDDoesNotHideDistinctWebsiteReleases(t *testing.T) {
	key := newCatalogNameMatcher([]string{"gpt-6.2-sol"}, nil)
	names := []string{"gpt-6.2-sol-20261120", "gpt-6.2-sol-20261121"}
	website := newCatalogNameMatcher(names, nil)
	for _, name := range names {
		if got := matchWebsiteModel(name, key, website); got.name != "" || got.kind != "ambiguous" {
			t.Fatalf("generic Key ID must leave %s visible as a separate website model: %+v", name, got)
		}
	}
}

func TestBeichenPanelPricesMatchEquivalentKeySpelling(t *testing.T) {
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	section := strings.Replace(panelFirst, `"name":"shared","platform":"openai"`, `"name":"GPT 6.1 Sol"`, 1)
	channels, err := decodePanelChannels([]byte(panelPayload(section)))
	if err != nil {
		t.Fatal(err)
	}
	entries := []catalog.ProviderEntry{{ID: "gpt-6.1-sol"}, {ID: "gpt-6.2-sol"}}
	quote, err := matchPanelPrices(channels, 1, info, entries)
	if err != nil || len(quote.rows) != 1 || quote.rows[0].Model != "gpt-6.1-sol" ||
		len(quote.terms["gpt-6.1-sol"]) == 0 || !slices.Equal(quote.notListed, []string{"gpt-6.2-sol"}) {
		t.Fatalf("expected a quote under the original Key ID: %+v, %v", quote, err)
	}
	entries = append(entries, catalog.ProviderEntry{ID: "gpt_6.1_sol"})
	quote, err = matchPanelPrices(channels, 1, info, entries)
	if err != nil || len(quote.rows) != 0 || len(quote.terms) != 0 {
		t.Fatalf("ambiguous spelling yielded a quote: %+v, %v", quote, err)
	}
}
