package usage

import (
	"encoding/json"
	"testing"
)

func TestEvaluateBeichenConditional(t *testing.T) {
	const tiers = `{
		"billing_mode":"token","input_price":0.009,"output_price":0.009,
		"intervals":[
			{"min_tokens":0,"max_tokens":100,"input_price":1e-6,"output_price":2e-6},
			{"min_tokens":100,"max_tokens":200,"input_price":5e-6,"output_price":6e-6,
				"cache_read_price":7e-6,"cache_write_price":8e-6},
			{"min_tokens":200,"max_tokens":null,"input_price":9e-6,"output_price":10e-6}
		]}`
	const rates = `{"billing_mode":"token","input_price":"0.000001","output_price":2e-6,
		"cache_read_price":3e-6,"cache_write_price":4e-6`
	const reasoning = rates + `,"reasoning_effort_multipliers":{"medium":2,"high":"1.5"}}`
	const timed = rates + `,"cache_write_1h_price":7e-6,"cache_write_12h_price":8e-6}`
	const noCache = `{"billing_mode":"token","input_price":1e-6,"output_price":2e-6}`
	metrics := func(input, output, read, write int64) CallMetrics {
		return CallMetrics{PromptTokens: &input, CompletionTokens: &output, CacheHitTokens: &read, CacheWriteTokens: &write}
	}
	zero, uncached := int64(0), int64(80)
	openaiCache := metrics(100, 0, 15, 5)
	openaiCache.CacheMissTokens = &uncached
	provenZero := metrics(0, 0, 0, 0)
	provenZero.CacheMissTokens, provenZero.CacheWriteTokens = &zero, nil
	incomplete := provenZero
	incomplete.CacheMissTokens = nil
	omitted, high := "", "high"
	tests := []struct {
		name, terms                       string
		usage                             CallMetrics
		protocol, ttl                     string
		effort                            *string
		credits, tier, multiplier, review string
	}{
		{name: "tier 1", terms: tiers, usage: metrics(99, 0, 0, 0), credits: "0.0099", tier: "[0,100)"},
		{name: "tier 2 lower edge", terms: tiers, usage: metrics(100, 0, 0, 0), credits: "0.05", tier: "[100,200)"},
		{name: "tier 2 upper edge", terms: tiers, usage: metrics(199, 0, 0, 0), credits: "0.0995", tier: "[100,200)"},
		{name: "tier 3 lower edge", terms: tiers, usage: metrics(200, 0, 0, 0), credits: "0.18", tier: "[200,)"},
		{name: "tier 3 unbounded", terms: tiers, usage: metrics(10000, 0, 0, 0), credits: "9", tier: "[200,)"},
		{name: "OpenAI cache already in prompt", terms: tiers, usage: openaiCache, credits: "0.0545", tier: "[100,200)"},
		{name: "Anthropic cache counts separate", terms: tiers, usage: metrics(80, 0, 15, 5), protocol: "anthropic", credits: "0.0545", tier: "[100,200)"},
		{name: "tier gap ignores base rates", terms: rates + `,"intervals":[{"min_tokens":100}]}`,
			usage: metrics(99, 0, 0, 0), review: "missing_price_tier"},
		{name: "tiers overlap", terms: rates + `,"intervals":[{"min_tokens":0},{"min_tokens":50}]}`,
			usage: metrics(99, 0, 0, 0), review: "ambiguous_price_tier"},
		{name: "effort scales all four categories", terms: reasoning, usage: metrics(11, 3, 4, 5),
			effort: &high, credits: "0.006", multiplier: "1.5"},
		{name: "omitted effort defaults to medium", terms: reasoning, usage: metrics(11, 3, 4, 5),
			effort: &omitted, credits: "0.008", multiplier: "2"},
		{name: "medium absent", terms: rates + `,"reasoning_effort_multipliers":{"high":1.5}}`,
			usage: metrics(11, 3, 4, 5), effort: &omitted, review: "missing_reasoning_effort_multiplier"},
		{name: "effort unrecorded", terms: reasoning, usage: metrics(11, 3, 4, 5), review: "unknown_reasoning_effort"},
		{name: "5m uses base rate", terms: timed, usage: metrics(4, 0, 0, 3), ttl: "5m", credits: "0.0013"},
		{name: "1h rate", terms: timed, usage: metrics(4, 0, 0, 3), ttl: "3600", credits: "0.0022"},
		{name: "12h rate", terms: timed, usage: metrics(4, 0, 0, 3), ttl: "12h", credits: "0.0025"},
		{name: "unknown cache duration", terms: timed, usage: metrics(4, 0, 0, 3), ttl: "20m", review: "unknown_cache_ttl"},
		{name: "unrecorded cache duration", terms: timed, usage: metrics(4, 0, 0, 3), review: "unknown_cache_ttl"},
		{name: "no cache writes needs no duration", terms: timed, usage: metrics(1, 0, 0, 0), credits: "0.0001"},
		{name: "TTL rate missing", terms: rates + `,"cache_write_1h_price":7e-6}`,
			usage: metrics(4, 0, 0, 3), ttl: "12h", review: "missing_cache_write_price"},
		{name: "cache write without price", terms: noCache, usage: metrics(4, 0, 0, 3), review: "missing_cache_write_price"},
		{name: "cache read without price", terms: noCache, usage: metrics(4, 0, 3, 0), review: "missing_cache_read_price"},
		{name: "confirmed zero write price", terms: noCache[:len(noCache)-1] + `,"cache_write_price":0}`,
			usage: metrics(4, 0, 0, 4), credits: "0"},
		{name: "per request", terms: `{"billing_mode":"per_request","per_request_price":1}`,
			usage: metrics(1, 0, 0, 0), review: "unsupported_billing_mode"},
		{name: "image price mixed in", terms: rates + `,"image_output_price":0}`,
			usage: metrics(1, 0, 0, 0), review: "unsupported_billing_mode"},
		{name: "video billing", terms: `{"billing_mode":"video","video_output_price":1}`,
			usage: metrics(1, 0, 0, 0), review: "unsupported_billing_mode"},
		{name: "unknown condition", terms: rates + `,"cache_write_30m_price":1e-6}`,
			usage: metrics(1, 0, 0, 0), review: "invalid_price_conditions"},
		{name: "unsupported protocol", terms: noCache, usage: metrics(1, 0, 0, 0),
			protocol: "openai_images", review: "unsupported_pricing_protocol"},
		{name: "cache write proved zero", terms: noCache, usage: provenZero, credits: "0"},
		{name: "cache write count unknown", terms: noCache, usage: incomplete, review: "missing_price_or_token_breakdown"},
		{name: "inconsistent OpenAI counts", terms: rates + "}", usage: metrics(2, 1, 2, 1), review: "missing_price_or_token_breakdown"},
		{name: "preserves long decimals", terms: `{"billing_mode":"token","input_price":0.000012000000000000000019,
			"output_price":0,"reasoning_effort_multipliers":{"high":1.25}}`,
			usage: metrics(1, 0, 0, 0), effort: &high, credits: "0.001500000000000000002375", multiplier: "1.25"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol := tc.protocol
			if protocol == "" {
				protocol = "openai_responses"
			}
			got, reason := evaluateBeichenConditional(json.RawMessage(tc.terms), tc.usage, tc.effort, tc.ttl, protocol)
			if reason != tc.review || got.Credits != tc.credits {
				t.Fatalf("credits=%q, review=%q; want credits=%q, review=%q", got.Credits, reason, tc.credits, tc.review)
			}
			if reason != "" {
				return
			}
			if tc.tier == "" {
				tc.tier = "base"
			}
			if tc.multiplier == "" {
				tc.multiplier = "1"
			}
			if got.Tier != tc.tier || got.Multiplier != tc.multiplier || !json.Valid(got.Detail) {
				t.Fatalf("tier=%q multiplier=%q detail=%s; want tier=%q multiplier=%q", got.Tier, got.Multiplier, got.Detail, tc.tier, tc.multiplier)
			}
		})
	}
}
