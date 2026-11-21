package usage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBeichenMultiplierTierBoundaries(t *testing.T) {
	const raw = `{"billing_mode":"token","input_price":0.000002,"output_price":0.00001,
	"cache_read_price":0.0000002,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004,
	"intervals":[{"min_tokens":499999,"max_tokens":500001,"tier_label":">=500000:whole-request-x2",
	"input_price":null,"output_price":null,"input_multiplier":"2","output_multiplier":2,
	"cache_read_multiplier":2,"cache_write_multiplier":2}]}`
	terms := json.RawMessage(raw)
	if code := BeichenDirectoryQuoteBlocker(terms); code != "" || !BeichenTokenQuoteUsable(terms) {
		t.Fatalf("valid multiplier quote was blocked: %q", code)
	}
	if input, _, _, _ := BeichenFlatTokenPrices(terms); input != nil {
		t.Fatal("a quote with multiplier tiers was projected as a flat price")
	}
	for _, tc := range []struct {
		input int64
		tier  string
		cost  string
	}{
		{499999, "base", "100.00645"},
		{500000, "[500000,500002)", "200.0133"},
		{500001, "[500000,500002)", "200.0137"},
		{500002, "base", "100.00705"},
	} {
		output, read, write := int64(15), int64(60), int64(49)
		metrics := CallMetrics{PromptTokens: &tc.input, CompletionTokens: &output, CacheHitTokens: &read, CacheWriteTokens: &write}
		got, blocker := evaluateBeichenConditional(terms, metrics, nil, "", "openai_responses")
		if blocker != "" || got.Tier != tc.tier || got.Credits != tc.cost {
			t.Fatalf("%d input: tier %q, credits %q, blocker %q", tc.input, got.Tier, got.Credits, blocker)
		}
	}
	var display struct{ Intervals []beichenPanelTier }
	if err := json.Unmarshal(BeichenPanelDisplayTerms(terms), &display); err != nil || len(display.Intervals) != 1 {
		t.Fatal("normalized tier missing from display terms", err)
	}
	tier := display.Intervals[0]
	if tier.CacheWrite1h == nil || *tier.CacheWrite1h != "0.000008" || tier.Min == nil || *tier.Min != "500000" {
		t.Fatal("timed cache quote or lower bound was not normalized")
	}
}

func TestBeichenMultiplierTierRejectsInvalidFields(t *testing.T) {
	const base = `{"billing_mode":"token","input_price":0.000002,"output_price":0.00001,"intervals":[`
	for _, tc := range []struct{ tiers, code, field string }{
		{`{"min_tokens":0,"input_multiplier":-2}`, "price_conditions_invalid", "input_multiplier"},
		{`{"min_tokens":0,"output_multiplier":"two"}`, "price_conditions_invalid", "output_multiplier"},
		{`{"min_tokens":2,"max_tokens":2,"input_multiplier":2}`, "price_conditions_invalid", "max_tokens"},
		{`{"min_tokens":0,"input_multiplier":2,"price_multiplier":2}`, "price_conditions_invalid", "price_multiplier"},
		{`{"min_tokens":0,"input_multiplier":2,"tier_label":{}}`, "price_conditions_invalid", "tier_label"},
		{`{"min_tokens":0,"max_tokens":100,"input_multiplier":2},{"min_tokens":99,"input_multiplier":3}`, "tier_range_invalid", "max_tokens"},
	} {
		terms := json.RawMessage(base + tc.tiers + `]}`)
		code, message := BeichenPanelQuoteBlocker(terms)
		if code != tc.code || !strings.Contains(message, tc.field) || BeichenDirectoryQuoteBlocker(terms) != tc.code {
			t.Errorf("expected %s for %s, got %s %s", tc.code, tc.field, code, message)
		}
	}
}

func TestBeichenDirectoryFixedNonTokenQuote(t *testing.T) {
	for _, tc := range []struct{ pricing, code string }{
		{`{"billing_mode":"image","per_request_price":0.04}`, ""},
		{`{"billing_mode":"image","per_request_price":"0"}`, ""},
		{`{"billing_mode":"image","per_request_price":null}`, "missing_per_request"},
		{`{"billing_mode":"image","per_request_price":-1}`, "price_conditions_invalid"},
		{`{"billing_mode":"image","per_request_price":1,"input_price":-1}`, "price_invalid"},
		{`{"billing_mode":"video","per_request_price":1,"extra_price":2}`, "price_conditions_invalid"},
		{`{"billing_mode":"unverified","per_request_price":1}`, "billing_mode_non_token"},
		{`{"per_request_price":1}`, "billing_mode_non_token"},
	} {
		terms := json.RawMessage(tc.pricing)
		if code := BeichenDirectoryQuoteBlocker(terms); code != tc.code || BeichenTokenQuoteUsable(terms) {
			t.Errorf("directory code %q, wanted %q for %s", code, tc.code, tc.pricing)
		}
	}
}
