package usage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBeichenTokenQuoteBlocker(t *testing.T) {
	for _, tc := range []struct {
		name, quote, code, messagePart string
	}{
		{name: "omitted mode", quote: `{"input_price":0,"output_price":1e-8}`},
		{name: "empty mode", quote: `{"billing_mode":"","input_price":"0","output_price":"0.000000000000000000000000000001","cache_write_1h_price":"0.1"}`},
		{name: "valid tiers", quote: `{"billing_mode":"token","intervals":[
			{"min_tokens":0,"max_tokens":100,"input_price":0,"output_price":1},
			{"min_tokens":100,"max_tokens":null,"input_price":"1","output_price":"2"}]}`},
		{name: "missing flat output", quote: `{"billing_mode":"token","input_price":0,"output_price":null}`,
			code: "missing_output", messagePart: "缺少输出"},
		{name: "malformed price", quote: `{"billing_mode":"token","input_price":0,"output_price":"n/a"}`,
			code: "price_conditions_invalid", messagePart: "格式无效"},
		{name: "negative price", quote: `{"billing_mode":"token","input_price":0,"output_price":-1}`,
			code: "price_invalid", messagePart: "输出单价无效"},
		{name: "no input in tier", quote: `{"intervals":[{"min_tokens":0,"output_price":1}]}`,
			code: "tier_missing_input", messagePart: "第 1 档缺少输入"},
		{name: "no output in one tier even with a base price", quote: `{"input_price":0,"output_price":1,"intervals":[
			{"min_tokens":0,"max_tokens":100,"input_price":0,"output_price":1},
			{"min_tokens":100,"input_price":2}]}`, code: "tier_missing_output", messagePart: "第 2 档缺少输出"},
		{name: "explicit non-token mode", quote: `{"billing_mode":"video","input_price":0,"output_price":1}`,
			code: "billing_mode_non_token", messagePart: "非 Token 计价"},
		{name: "per request placeholder with omitted mode", quote: `{"input_price":0,"output_price":1,"per_request_price":0}`},
		{name: "image price mixed with token prices", quote: `{"billing_mode":"token","input_price":0,"output_price":1,"image_output_price":2}`},
		{name: "video price in token tier", quote: `{"intervals":[{"min_tokens":0,"input_price":0,"output_price":1,"video_output_price":2}]}`},
		{name: "explicit non-token tier", quote: `{"billing_mode":"token","intervals":[{"billing_mode":"video","min_tokens":0,"input_price":0,"output_price":1,"video_output_price":0}]}`,
			code: "billing_mode_non_token", messagePart: "第 1 档"},
		{name: "free token prices with placeholders", quote: `{"billing_mode":null,"input_price":0,"output_price":0,
			"per_request_price":"0e-8","image_input_price":null,"image_output_price":"","video_output_price":-0}`},
		{name: "ambiguously free with active per-request price", quote: `{"input_price":0,"output_price":0,"per_request_price":1}`,
			code: "billing_mode_non_token", messagePart: "非 Token 计价"},
		{name: "invalid range", quote: `{"intervals":[{"min_tokens":10,"max_tokens":10,"input_price":0,"output_price":1}]}`,
			code: "tier_range_invalid", messagePart: "第 1 档"},
		{name: "overlapping ranges", quote: `{"intervals":[
			{"min_tokens":0,"max_tokens":100,"input_price":0,"output_price":1},
			{"min_tokens":90,"input_price":0,"output_price":1}]}`,
			code: "tier_range_invalid", messagePart: "重叠"},
		{name: "invalid effort multiplier", quote: `{"input_price":0,"output_price":1,"reasoning_effort_multipliers":{"medium":-1}}`,
			code: "effort_multiplier_invalid", messagePart: "推理强度倍率无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.quote)
			code, message := BeichenTokenQuoteBlocker(raw)
			if code != tc.code || !strings.Contains(message, tc.messagePart) || (code == "") != (message == "") {
				t.Fatalf("got blocker (%q, %q), want (%q, containing %q)", code, message, tc.code, tc.messagePart)
			}
			if BeichenTokenQuoteUsable(raw) != (tc.code == "") {
				t.Fatalf("unexpected usability for blocker %q", tc.code)
			}
		})
	}
}

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
	omitted, high, low := "", "high", "low"
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
		{name: "Anthropic cache counts separate", terms: tiers, usage: metrics(80, 0, 15, 5), protocol: "anthropic", ttl: "5m", credits: "0.0545", tier: "[100,200)"},
		{name: "tiered default mode", terms: `{"intervals":[{"min_tokens":0,"input_price":1e-6,"output_price":2e-6}]}`,
			usage: metrics(1, 1, 0, 0), credits: "0.0003", tier: "[0,)"},
		{name: "tier missing output ignores base rate", terms: rates + `,"intervals":[{"min_tokens":0,"input_price":1e-6}]}`,
			usage: metrics(1, 1, 0, 0), review: "missing_output_price"},
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
		{name: "unrecorded effort defaults to medium", terms: reasoning, usage: metrics(11, 3, 4, 5), credits: "0.008", multiplier: "2"},
		{name: "unquoted observed effort", terms: reasoning, usage: metrics(11, 3, 4, 5), effort: &low, review: "missing_reasoning_effort_multiplier"},
		{name: "5m uses base rate", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 3), ttl: "5m", credits: "0.0013"},
		{name: "1h rate", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 3), ttl: "3600", credits: "0.0022"},
		{name: "12h rate", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 3), ttl: "12h", credits: "0.0025"},
		{name: "unknown cache duration", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 3), ttl: "20m", review: "unknown_cache_ttl"},
		{name: "unrecorded cache duration", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 3), review: "unknown_cache_ttl"},
		{name: "no cache writes needs no duration", terms: timed, protocol: "anthropic", usage: metrics(1, 0, 0, 0), credits: "0.0001"},
		{name: "OpenAI prices implicit cache at base", terms: timed, usage: metrics(4, 0, 0, 3), ttl: "1h", credits: "0.0013"},
		{name: "OpenAI missing TTL uses base", terms: timed, protocol: "openai_compatible", usage: metrics(4, 0, 0, 3), credits: "0.0013"},
		{name: "TTL rate missing", terms: rates + `,"cache_write_1h_price":7e-6}`,
			protocol: "anthropic", usage: metrics(1, 0, 0, 3), ttl: "12h", review: "missing_cache_write_price"},
		{name: "cache write without price", terms: noCache, usage: metrics(4, 0, 0, 3), review: "missing_cache_write_price"},
		{name: "cache read without price", terms: noCache, usage: metrics(4, 0, 3, 0), review: "missing_cache_read_price"},
		{name: "confirmed zero write price", terms: noCache[:len(noCache)-1] + `,"cache_write_price":0}`,
			usage: metrics(4, 0, 0, 4), credits: "0"},
		{name: "per request", terms: `{"billing_mode":"per_request","per_request_price":1}`,
			usage: metrics(1, 0, 0, 0), review: "unsupported_billing_mode"},
		{name: "image price mixed in", terms: rates + `,"image_output_price":0.75,"video_input_price":"","per_request_price":0}`,
			usage: metrics(1, 0, 0, 0), credits: "0.0001"},
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
