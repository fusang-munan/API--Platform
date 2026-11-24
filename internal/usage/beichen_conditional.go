package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func (r beichenPanelRates) usesTokenBilling() bool {
	return (r.BillingMode == "" || r.BillingMode == "token") &&
		r.ImageInput == nil && r.ImageOutput == nil && r.VideoInput == nil && r.VideoOutput == nil && r.PerRequest == nil
}

type beichenPanelTier struct {
	beichenPanelRates
	Min   *json.Number `json:"min_tokens"`
	Max   *json.Number `json:"max_tokens"`
	Label string       `json:"tier_label"`
}

type beichenPriceConditions struct {
	beichenPanelRates
	Intervals        []beichenPanelTier      `json:"intervals"`
	EffortMultiplier map[string]*json.Number `json:"reasoning_effort_multipliers"`
	tierBaseFallback bool
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
	code, _ := BeichenTokenQuoteBlocker(raw)
	return code == ""
}

// BeichenTokenQuoteBlocker returns the first obstacle to enabling a quote,
// or two empty strings when the quote can be enabled.
func BeichenTokenQuoteBlocker(raw json.RawMessage) (code, message string) {
	return beichenQuoteBlocker(raw, false)
}

// BeichenDirectoryQuoteBlocker also accepts a complete fixed non-Token quote
// for the directory. Publication and settlement still use the Token check.
func BeichenDirectoryQuoteBlocker(raw json.RawMessage) string {
	code, _ := beichenQuoteBlocker(raw, true)
	return code
}

func beichenQuoteBlocker(raw json.RawMessage, directory bool) (code, message string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "missing_price", "北辰线路缺少可用报价，请同步官网价或设置手动价"
	}
	quote := decodeBeichenConditions(raw)
	if quote == nil {
		return "price_conditions_invalid", "官网报价格式无效或含有暂不支持的计价字段"
	}
	if directory && len(quote.Intervals) == 0 {
		switch quote.BillingMode {
		case "image", "per_request", "video":
			if quote.PerRequest == nil {
				return "missing_per_request", "官网报价缺少每次请求或每秒单价"
			}
			if code, message := quote.optionalTokenRateBlocker(); code != "" {
				return code, "官网报价" + message
			}
			return "", ""
		}
	}
	const nonTokenCode = "billing_mode_non_token"
	const nonTokenMessage = "包含按次、图像或视频等非 Token 计价"
	if !quote.usesTokenBilling() {
		return nonTokenCode, "官网报价" + nonTokenMessage
	}
	for _, rate := range quote.EffortMultiplier {
		if beichenPriceDecimal(rate) == nil {
			return "effort_multiplier_invalid", "官网推理强度倍率无效，应为非负数"
		}
	}
	if len(quote.Intervals) == 0 || quote.tierBaseFallback {
		code, message := quote.tokenRateBlocker()
		if code != "" {
			return code, "官网报价" + message
		}
	}

	ranges := make([]tokenRange, 0, len(quote.Intervals))
	for i, tier := range quote.Intervals {
		if !tier.usesTokenBilling() {
			return nonTokenCode, fmt.Sprintf("第 %d 档%s", i+1, nonTokenMessage)
		}
		if code, message := tier.tokenRateBlocker(); code != "" {
			return "tier_" + code, fmt.Sprintf("第 %d 档%s", i+1, message)
		}
		bounds, ok := tier.tokenRange()
		if !ok {
			return "tier_range_invalid", fmt.Sprintf("第 %d 档的 Token 范围无效，下限须为非负整数，有限上限须为大于下限的整数", i+1)
		}
		ranges = append(ranges, bounds)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].min < ranges[j].min })
	for i := 1; i < len(ranges); i++ {
		if ranges[i-1].max < 0 || ranges[i-1].max > ranges[i].min {
			return "tier_range_invalid", "官网报价的 Token 档位范围重叠"
		}
	}
	return "", ""
}

func (r beichenPanelRates) tokenRateBlocker() (code, message string) {
	switch {
	case r.Input == nil:
		return "missing_input", "缺少输入 Token 单价"
	case r.Output == nil:
		return "missing_output", "缺少输出 Token 单价"
	}
	return r.optionalTokenRateBlocker()
}

func (r beichenPanelRates) optionalTokenRateBlocker() (code, message string) {
	for _, field := range []struct {
		label string
		price *json.Number
	}{
		{"输入", r.Input}, {"输出", r.Output}, {"缓存读取", r.CacheRead},
		{"缓存写入", r.CacheWrite}, {"1 小时缓存写入", r.CacheWrite1h}, {"12 小时缓存写入", r.CacheWrite12h},
	} {
		if field.price != nil && beichenPriceDecimal(field.price) == nil {
			return "price_invalid", "的" + field.label + "单价无效，应为非负数"
		}
	}
	return "", ""
}

type tokenRange struct{ min, max int64 }

func (tier beichenPanelTier) tokenRange() (tokenRange, bool) {
	if tier.Min == nil {
		return tokenRange{}, false
	}
	minimum, err := tier.Min.Int64()
	if err != nil || minimum < 0 {
		return tokenRange{}, false
	}
	bounds := tokenRange{min: minimum, max: -1}
	if tier.Max != nil {
		bounds.max, err = tier.Max.Int64()
		if err != nil || bounds.max <= minimum {
			return tokenRange{}, false
		}
	}
	return bounds, true
}

func isBeichenConditional(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	terms := decodeBeichenConditions(raw)
	return terms == nil || !terms.usesTokenBilling() || len(terms.Intervals) != 0 ||
		len(terms.EffortMultiplier) != 0 || terms.hasTimedCacheWrite()
}

type beichenConditionalPrice struct {
	Credits    string
	Tier       string
	Multiplier string
	Detail     json.RawMessage
}

type beichenBillableTokens struct {
	Input   *int64 `json:"input_tokens"`
	Output  int64  `json:"output_tokens"`
	Read    *int64 `json:"cache_read_tokens"`
	Write   *int64 `json:"cache_write_tokens"`
	TotalIn int64  `json:"total_input_tokens"`
	Unsplit int64  `json:"unsplit_input_tokens,omitempty"`
}

// evaluateBeichenConditional returns base credits before the upstream group
// and school multipliers. Missing effort uses the default pricing tier.
func evaluateBeichenConditional(raw json.RawMessage, metrics CallMetrics, effort *string, ttl, protocol string, zeroGroup bool) (beichenConditionalPrice, string) {
	terms := decodeBeichenConditions(raw)
	if terms == nil {
		return beichenConditionalPrice{}, "invalid_price_conditions"
	}
	if !terms.usesTokenBilling() {
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
	if !rates.usesTokenBilling() {
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
	if (tokens.Write == nil || *tokens.Write > 0) && (protocol == "anthropic" || (protocol == "google_gemini_v1beta" && timedCache)) {
		appliedTTL = beichenCacheTTL(ttl)
		switch appliedTTL {
		case "5m":
		case "1h":
			writePrice = rates.CacheWrite1h
		case "12h":
			writePrice = rates.CacheWrite12h
		case "min":
			five, one := beichenPriceDecimal(rates.CacheWrite), beichenPriceDecimal(rates.CacheWrite1h)
			if five == nil || one == nil {
				// Only one duration is priced. The unconfirmed remainder may
				// belong to the unpriced duration, so the single price cannot
				// cover all writes.
				return beichenConditionalPrice{}, "missing_cache_write_price"
			}
			switch {
			case five.Cmp(one) <= 0:
				writePrice = rates.CacheWrite
			default:
				writePrice = rates.CacheWrite1h
			}
		default:
			return beichenConditionalPrice{}, "unknown_cache_ttl"
		}
	}
	effective := multiplier
	if zeroGroup && tokens.Unsplit > 0 {
		// The ledger records base credits after the group multiplier. Its
		// exact zero erases price differences but cannot supply missing rates.
		effective = new(big.Rat)
	}
	credits, prices, reason := priceBeichenTokenParts(tokens, rates.Input, rates.Output, rates.CacheRead, writePrice, effective)
	if reason != "" {
		return beichenConditionalPrice{}, reason
	}
	credits.Mul(credits, big.NewRat(100, 1))
	multiplierText := beichenDecimalString(multiplier)
	detail, err := json.Marshal(struct {
		beichenBillableTokens
		ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
		ReasoningMultiplier string            `json:"reasoning_multiplier"`
		CacheTTL            string            `json:"cache_ttl,omitempty"`
		AmountSource        string            `json:"amount_source,omitempty"`
		Prices              map[string]string `json:"upstream_units_per_token"`
	}{tokens, appliedEffort, multiplierText, appliedTTL, tokens.amountSource(), prices})
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
		bounds, ok := interval.tokenRange()
		if !ok {
			return beichenPanelRates{}, "", false, "invalid_price_tier"
		}
		maxLabel := ""
		if bounds.max >= 0 {
			if total >= bounds.max {
				continue
			}
			maxLabel = strconv.FormatInt(bounds.max, 10)
		}
		if total >= bounds.min {
			if label != "" {
				return beichenPanelRates{}, "", false, "ambiguous_price_tier"
			}
			rates, label = interval.beichenPanelRates, "["+strconv.FormatInt(bounds.min, 10)+","+maxLabel+")"
		}
	}
	if label == "" {
		if c.tierBaseFallback {
			return c.beichenPanelRates, "base", timedCache, ""
		}
		return beichenPanelRates{}, "", false, "missing_price_tier"
	}
	return rates, label, timedCache, ""
}

func (t beichenBillableTokens) amountSource() string {
	if t.Unsplit > 0 {
		return "estimated_unique_amount"
	}
	return ""
}

// Every unknown category can take the entire remainder. With a fixed input
// tier the price is linear, so equality of their effective rates is necessary
// and sufficient. Compare exact rationals before any ledger rounding.
func priceBeichenTokenParts(t beichenBillableTokens, input, output, read, write *json.Number, multiplier *big.Rat) (*big.Rat, map[string]string, string) {
	prices := make(map[string]string, 4)
	total := new(big.Rat)
	var shared *big.Rat
	for _, part := range []struct {
		name  string
		count *int64
		price *json.Number
	}{
		{"input", t.Input, input}, {"output", &t.Output, output},
		{"cache_read", t.Read, read}, {"cache_write", t.Write, write},
	} {
		if part.price == nil && part.count != nil && *part.count == 0 && (part.name == "cache_read" || part.name == "cache_write") {
			continue
		}
		rate := beichenPriceDecimal(part.price)
		if rate == nil {
			return nil, nil, "missing_" + part.name + "_price"
		}
		prices[part.name] = beichenDecimalString(rate)
		rate.Mul(rate, multiplier)
		if part.count == nil {
			if shared != nil && shared.Cmp(rate) != 0 {
				return nil, nil, "incomplete_token_breakdown"
			}
			shared = rate
		} else {
			total.Add(total, rate.Mul(rate, big.NewRat(*part.count, 1)))
		}
	}
	if shared != nil {
		total.Add(total, shared.Mul(shared, big.NewRat(t.Unsplit, 1)))
	}
	return total, prices, ""
}

func beichenCacheTTL(value string) string {
	switch value {
	case "min":
		return "min"
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
