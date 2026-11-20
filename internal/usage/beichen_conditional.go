package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Panel snapshots store the original prices in upstream units per token.
// One upstream unit is 100 school credits. This is equivalent to the fixed
// rate path's 10^12 conversion on import and 10^-10 when charging.
type beichenPanelRates struct {
	BillingMode   string       `json:"billing_mode"`
	Input         *json.Number `json:"input_price"`
	Output        *json.Number `json:"output_price"`
	CacheRead     *json.Number `json:"cache_read_price"`
	CacheWrite    *json.Number `json:"cache_write_price"`
	CacheWrite1h  *json.Number `json:"cache_write_1h_price"`
	CacheWrite12h *json.Number `json:"cache_write_12h_price"`
	ImageInput    *json.Number `json:"image_input_price"`
	ImageOutput   *json.Number `json:"image_output_price"`
	VideoInput    *json.Number `json:"video_input_price"`
	VideoOutput   *json.Number `json:"video_output_price"`
	PerRequest    *json.Number `json:"per_request_price"`
}

func (r beichenPanelRates) hasTimedCacheWrite() bool {
	return r.CacheWrite1h != nil || r.CacheWrite12h != nil
}

func (r beichenPanelRates) hasNonTokenPrice() bool {
	return r.ImageInput != nil || r.ImageOutput != nil || r.VideoInput != nil || r.VideoOutput != nil || r.PerRequest != nil
}

type beichenPanelTier struct {
	beichenPanelRates
	Min *json.Number `json:"min_tokens"`
	Max *json.Number `json:"max_tokens"`
}

type beichenPriceConditions struct {
	beichenPanelRates
	Intervals        []beichenPanelTier      `json:"intervals"`
	EffortMultiplier map[string]*json.Number `json:"reasoning_effort_multipliers"`
}

func decodeBeichenConditions(raw json.RawMessage) *beichenPriceConditions {
	var terms *beichenPriceConditions
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&terms) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil
	}
	return terms
}

// BeichenTokenQuoteUsable checks a quote's structure before publication.
// Settlement still needs the actual token counts and any effort or TTL.
func BeichenTokenQuoteUsable(raw json.RawMessage) bool {
	quote := decodeBeichenConditions(raw)
	if quote == nil || quote.BillingMode != "token" || quote.hasNonTokenPrice() {
		return false
	}
	for _, rate := range quote.EffortMultiplier {
		if beichenPriceDecimal(rate) == nil {
			return false
		}
	}
	if len(quote.Intervals) == 0 {
		return quote.usableTokenRates()
	}

	type tokenRange struct{ min, max int64 }
	ranges := make([]tokenRange, 0, len(quote.Intervals))
	for _, tier := range quote.Intervals {
		if !tier.usableTokenRates() || tier.Min == nil {
			return false
		}
		minimum, err := tier.Min.Int64()
		if err != nil || minimum < 0 {
			return false
		}
		bounds := tokenRange{min: minimum, max: -1}
		if tier.Max != nil {
			bounds.max, err = tier.Max.Int64()
			if err != nil || bounds.max <= minimum {
				return false
			}
		}
		ranges = append(ranges, bounds)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].min < ranges[j].min })
	for i := 1; i < len(ranges); i++ {
		if ranges[i-1].max < 0 || ranges[i-1].max > ranges[i].min {
			return false
		}
	}
	return true
}

func (r beichenPanelRates) usableTokenRates() bool {
	if (r.BillingMode != "" && r.BillingMode != "token") || r.hasNonTokenPrice() || r.Input == nil || r.Output == nil {
		return false
	}
	for _, price := range []*json.Number{r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CacheWrite1h, r.CacheWrite12h} {
		if price != nil && beichenPriceDecimal(price) == nil {
			return false
		}
	}
	return true
}

func isBeichenConditional(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	terms := decodeBeichenConditions(raw)
	return terms == nil || terms.BillingMode != "token" || len(terms.Intervals) != 0 ||
		len(terms.EffortMultiplier) != 0 || terms.hasTimedCacheWrite() || terms.hasNonTokenPrice()
}

type beichenConditionalPrice struct {
	Credits    string
	Tier       string
	Multiplier string
	Detail     json.RawMessage
}

type beichenBillableTokens struct {
	Input   int64 `json:"input_tokens"`
	Output  int64 `json:"output_tokens"`
	Read    int64 `json:"cache_read_tokens"`
	Write   int64 `json:"cache_write_tokens"`
	TotalIn int64 `json:"total_input_tokens"`
}

// evaluateBeichenConditional returns base credits before the upstream group
// and school multipliers. Missing effort uses the default pricing tier.
func evaluateBeichenConditional(raw json.RawMessage, metrics CallMetrics, effort *string, ttl, protocol string) (beichenConditionalPrice, string) {
	terms := decodeBeichenConditions(raw)
	if terms == nil {
		return beichenConditionalPrice{}, "invalid_price_conditions"
	}
	if terms.BillingMode != "token" || terms.hasNonTokenPrice() {
		return beichenConditionalPrice{}, "unsupported_billing_mode"
	}
	tokens, reason := normalizeBeichenConditionalTokens(metrics, protocol)
	if reason != "" {
		return beichenConditionalPrice{}, reason
	}
	rates, tier, timedCache, reason := terms.ratesFor(tokens.TotalIn)
	if reason != "" {
		return beichenConditionalPrice{}, reason
	}
	if (rates.BillingMode != "" && rates.BillingMode != "token") || rates.hasNonTokenPrice() {
		return beichenConditionalPrice{}, "unsupported_billing_mode"
	}

	multiplier := big.NewRat(1, 1)
	appliedEffort := ""
	if len(terms.EffortMultiplier) != 0 {
		appliedEffort = DefaultReasoningEffort
		if effort != nil && *effort != "" {
			appliedEffort = *effort
		}
		multiplier = beichenPriceDecimal(terms.EffortMultiplier[appliedEffort])
		if multiplier == nil {
			return beichenConditionalPrice{}, "missing_reasoning_effort_multiplier"
		}
	}

	appliedTTL := ""
	writePrice := rates.CacheWrite
	// OpenAI reports implicit cache writes at the base write rate.
	if tokens.Write > 0 && (protocol == "anthropic" || (protocol == "google_gemini_v1beta" && timedCache)) {
		appliedTTL = beichenCacheTTL(ttl)
		switch appliedTTL {
		case "5m":
		case "1h":
			writePrice = rates.CacheWrite1h
		case "12h":
			writePrice = rates.CacheWrite12h
		default:
			return beichenConditionalPrice{}, "unknown_cache_ttl"
		}
	}
	prices := make(map[string]string, 4)
	credits := new(big.Rat)
	for _, part := range []struct {
		name   string
		count  int64
		price  *json.Number
		reason string
	}{
		{"input", tokens.Input, rates.Input, "missing_input_price"},
		{"output", tokens.Output, rates.Output, "missing_output_price"},
		{"cache_read", tokens.Read, rates.CacheRead, "missing_cache_read_price"},
		{"cache_write", tokens.Write, writePrice, "missing_cache_write_price"},
	} {
		if part.price == nil && part.count == 0 && (part.name == "cache_read" || part.name == "cache_write") {
			continue
		}
		rate := beichenPriceDecimal(part.price)
		if rate == nil {
			return beichenConditionalPrice{}, part.reason
		}
		prices[part.name] = beichenDecimalString(rate)
		credits.Add(credits, rate.Mul(rate, big.NewRat(part.count, 1)))
	}
	credits.Mul(credits, multiplier)
	credits.Mul(credits, big.NewRat(100, 1))
	multiplierText := beichenDecimalString(multiplier)
	detail, err := json.Marshal(struct {
		beichenBillableTokens
		ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
		ReasoningMultiplier string            `json:"reasoning_multiplier"`
		CacheTTL            string            `json:"cache_ttl,omitempty"`
		Prices              map[string]string `json:"upstream_units_per_token"`
	}{tokens, appliedEffort, multiplierText, appliedTTL, prices})
	if err != nil {
		return beichenConditionalPrice{}, "invalid_price_conditions"
	}
	return beichenConditionalPrice{beichenDecimalString(credits), tier, multiplierText, detail}, ""
}

func (c beichenPriceConditions) ratesFor(total int64) (beichenPanelRates, string, bool, string) {
	timedCache := c.hasTimedCacheWrite()
	if len(c.Intervals) == 0 {
		return c.beichenPanelRates, "base", timedCache, ""
	}
	var rates beichenPanelRates
	label := ""
	for _, interval := range c.Intervals {
		timedCache = timedCache || interval.hasTimedCacheWrite()
		if interval.Min == nil {
			return beichenPanelRates{}, "", false, "invalid_price_tier"
		}
		min, err := interval.Min.Int64()
		if err != nil || min < 0 {
			return beichenPanelRates{}, "", false, "invalid_price_tier"
		}
		maxLabel := ""
		if interval.Max != nil {
			max, err := interval.Max.Int64()
			if err != nil || max <= min {
				return beichenPanelRates{}, "", false, "invalid_price_tier"
			}
			if total >= max {
				continue
			}
			maxLabel = strconv.FormatInt(max, 10)
		}
		if total >= min {
			if label != "" {
				return beichenPanelRates{}, "", false, "ambiguous_price_tier"
			}
			rates, label = interval.beichenPanelRates, "["+strconv.FormatInt(min, 10)+","+maxLabel+")"
		}
	}
	if label == "" {
		return beichenPanelRates{}, "", false, "missing_price_tier"
	}
	return rates, label, timedCache, ""
}

// Match the fixed ledger's mutually exclusive counts. OpenAI prompt tokens
// already contain cache reads and writes, while Anthropic reports all three
// separately. OpenAI's cache_miss counter is uncached input, when provided.
func normalizeBeichenConditionalTokens(m CallMetrics, protocol string) (beichenBillableTokens, string) {
	for _, count := range []*int64{m.PromptTokens, m.CompletionTokens, m.CacheHitTokens, m.CacheMissTokens, m.CacheWriteTokens} {
		if count != nil && *count < 0 {
			return beichenBillableTokens{}, "missing_price_or_token_breakdown"
		}
	}
	if m.PromptTokens == nil || m.CompletionTokens == nil {
		return beichenBillableTokens{}, "missing_price_or_token_breakdown"
	}
	input, output := *m.PromptTokens, *m.CompletionTokens
	read, write, miss := m.CacheHitTokens, m.CacheWriteTokens, m.CacheMissTokens
	switch protocol {
	case "openai_compatible", "openai_responses", "openai_realtime":
		// An omitted cache count is zero only if the other categories exhaust
		// the reported prompt. Price zero does not prove count zero.
		zero := int64(0)
		if read == nil && miss != nil && write != nil && *miss == input-*write {
			read = &zero
		}
		if write == nil && miss != nil && read != nil && *miss == input-*read {
			write = &zero
		}
	case "anthropic", "google_gemini_v1beta":
	default:
		return beichenBillableTokens{}, "unsupported_pricing_protocol"
	}
	if read == nil || write == nil {
		return beichenBillableTokens{}, "missing_price_or_token_breakdown"
	}
	if protocol == "anthropic" {
		if *read > math.MaxInt64-input || *write > math.MaxInt64-input-*read {
			return beichenBillableTokens{}, "missing_price_or_token_breakdown"
		}
		return beichenBillableTokens{input, output, *read, *write, input + *read + *write}, ""
	}
	if *read > input || *write > input-*read || (miss != nil && *miss != input-*read-*write) {
		return beichenBillableTokens{}, "missing_price_or_token_breakdown"
	}
	return beichenBillableTokens{input - *read - *write, output, *read, *write, input}, ""
}

func beichenCacheTTL(value string) string {
	switch value {
	case "300":
		return "5m"
	case "3600":
		return "1h"
	case "43200":
		return "12h"
	}
	duration, _ := time.ParseDuration(value)
	switch duration {
	case 5 * time.Minute:
		return "5m"
	case time.Hour:
		return "1h"
	case 12 * time.Hour:
		return "12h"
	}
	return ""
}

// Allow exact decimal strings and scientific notation. JSONB can expand the
// exponent into digits when persisting the original quote.
var beichenPriceNumber = regexp.MustCompile(`^(0|[1-9][0-9]{0,255})(\.[0-9]{1,256})?([eE][+-]?[0-9]{1,3})?$`)

func beichenPriceDecimal(value *json.Number) *big.Rat {
	if value == nil || !beichenPriceNumber.MatchString(value.String()) {
		return nil
	}
	number, _ := new(big.Rat).SetString(value.String())
	return number
}

func beichenDecimalString(number *big.Rat) string {
	// All operands are finite decimals or integers, and only addition and
	// multiplication are used, so the result has a finite decimal expansion.
	places, _ := number.FloatPrec()
	return number.FloatString(places)
}
