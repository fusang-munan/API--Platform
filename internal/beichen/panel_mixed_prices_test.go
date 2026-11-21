package beichen

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func mixedPanelFixture(t *testing.T, names ...string) (panelQuote, billingInfo) {
	t.Helper()
	body, err := os.ReadFile("testdata/panel-mixed-prices.json")
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil)
	s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != panelURL {
			t.Fatalf("unexpected fixture request: %s %s", req.Method, req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	entries := make([]catalog.ProviderEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, catalog.ProviderEntry{ID: name})
	}
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1", ObservedAt: s.now()}
	quote := s.fetchPanelQuote(context.Background(), "", "fixture", 1, priceAccess{}, info, entries)
	if quote.err != nil {
		t.Fatal(quote.err)
	}
	return quote, info
}

func TestPanelMixedPriceFixture(t *testing.T) {
	quote, _ := mixedPanelFixture(t, "gpt-6.1-sol", "gpt-6.2-sol", "incomplete-tier", "empty-tier", "ambiguous-fee", "explicit-video", "unknown-fee")
	for _, tc := range []struct {
		model, blocker, field string
	}{
		{model: "gpt-6.1-sol"},
		{model: "gpt-6.2-sol"},
		{model: "incomplete-tier", blocker: "tier_missing_output", field: "output_price"},
		{model: "empty-tier", blocker: "tier_missing_input", field: "input_price"},
		{model: "ambiguous-fee", blocker: "billing_mode_non_token", field: "per_request_price"},
		{model: "explicit-video", blocker: "billing_mode_non_token", field: "billing_mode"},
		{model: "unknown-fee", blocker: "price_conditions_invalid", field: "cache_write_30m_price"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			terms := quote.terms[tc.model]
			code, message := usage.BeichenTokenQuoteBlocker(terms)
			if code != tc.blocker || usage.BeichenTokenQuoteUsable(terms) != (tc.blocker == "") {
				t.Errorf("blocker=(%q, %q), want %q", code, message, tc.blocker)
			}
			readiness := routePriceReadiness(false, terms)
			if readiness.ready() != (tc.blocker == "") {
				t.Errorf("readiness=%+v, want blocker %q", readiness.blocker, tc.blocker)
			} else if readiness.blocker != nil && !strings.Contains(readiness.blocker.Message, tc.field) {
				t.Errorf("blocker message %q did not identify %s", readiness.blocker.Message, tc.field)
			}
		})
	}
	if len(quote.rows) != 1 || quote.rows[0].Model != "gpt-6.1-sol" {
		t.Fatalf("only the flat quote should publish fixed rates: %+v", quote.rows)
	}
	rates := quote.rows[0].Rates
	if *rates.Input != 2_000_000 || *rates.Output != 12_000_000 || rates.CacheRead != nil || rates.CacheWrite != nil {
		t.Fatalf("wrong flat rates: %+v", rates)
	}
}
