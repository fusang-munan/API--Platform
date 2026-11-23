package pendingaudit

import (
	"math"
	"math/big"
	"strconv"
)

const (
	ClassExact   = "exact"
	ClassNarrow  = "narrow"
	ClassWide    = "wide"
	ClassUnknown = "unknown"
)

// Input is the local evidence for one pending review row. Rates are upstream
// cost per token (one upstream unit equals one yuan before school markup).
type Input struct {
	Protocol         string   `json:"protocol"`
	PromptTokens     *int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64   `json:"completion_tokens,omitempty"`
	TotalTokens      *int64   `json:"total_tokens,omitempty"`
	CacheReadTokens  *int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64   `json:"cache_write_tokens,omitempty"`
	InputRate        *float64 `json:"input_rate,omitempty"`
	OutputRate       *float64 `json:"output_rate,omitempty"`
	CacheReadRate    *float64 `json:"cache_read_rate,omitempty"`
	CacheWriteRate   *float64 `json:"cache_write_rate,omitempty"`
	PriorReadShare   *float64 `json:"prior_read_share,omitempty"`
}

type Result struct {
	Class            string   `json:"class"`
	Missing          []string `json:"missing"`
	UnknownTokens    int64    `json:"unknown_tokens"`
	RateSpread       float64  `json:"rate_spread"`
	EstimatedYuan    float64  `json:"estimated_yuan"`
	MaxErrorYuan     float64  `json:"max_error_yuan"`
	RelativeError    float64  `json:"relative_error"`
	Note             string   `json:"note"`
	MinYuan          float64  `json:"min_yuan"`
	MaxYuan          float64  `json:"max_yuan"`
	RecommendedYuan  float64  `json:"recommended_yuan"`
	RecommendedRead  int64    `json:"recommended_read"`
	RecommendedWrite int64    `json:"recommended_write"`
	CanAutoSettle    bool     `json:"can_auto_settle"`
}

const (
	ordinaryInput = iota
	cacheRead
	cacheWrite
	output
)

// Analyze prices the full range of legal token splits. Its recommendation is
// one legal integer split and does not affect the eligibility decision.
func Analyze(in Input, thresholdYuan, relativeThreshold float64) Result {
	res := Result{Class: ClassUnknown, Missing: []string{}}
	partTokens, remaining := res.split(in)
	if res.Note != "" {
		return res
	}
	res.UnknownTokens = remaining

	share := 0.5
	if in.PriorReadShare != nil {
		share = *in.PriorReadShare
	}
	if !nonnegativeFinite(share) || share > 1 {
		res.Note = "缓存读推荐比例必须在 0 到 1 之间"
		return res
	}

	passedRates := [4]*float64{in.InputRate, in.CacheReadRate, in.CacheWriteRate, in.OutputRate}
	rateFields := [4]string{"input_rate", "cache_read_rate", "cache_write_rate", "output_rate"}
	var rates [4]*big.Rat
	var fixedCost big.Rat
	var minRate, maxRate *big.Rat
	var missingPrice bool
	for part, tokens := range partTokens {
		if (tokens == nil && remaining == 0) || (tokens != nil && *tokens == 0) {
			continue
		}
		rate := passedRates[part]
		if rate == nil || !nonnegativeFinite(*rate) {
			res.Missing = append(res.Missing, rateFields[part])
			missingPrice = true
			continue
		}
		rates[part] = decimal(*rate)
		if tokens != nil {
			fixedCost.Add(&fixedCost, tokenCost(*tokens, rates[part]))
			continue
		}
		if minRate == nil || rates[part].Cmp(minRate) < 0 {
			minRate = rates[part]
		}
		if maxRate == nil || rates[part].Cmp(maxRate) > 0 {
			maxRate = rates[part]
		}
	}
	if missingPrice {
		res.Note = "缺少可产生费用的有效单价，无法计算金额区间"
		return res
	}

	minCost := new(big.Rat).Set(&fixedCost)
	maxCost := new(big.Rat).Set(&fixedCost)
	recommendation := new(big.Rat).Set(&fixedCost)
	var spread big.Rat
	split := recommend(partTokens, remaining, share)
	if remaining > 0 {
		// The sum of all unassigned parts is fixed. Assigning that whole
		// integer balance to the lowest or highest rate attains each bound.
		minCost.Add(minCost, tokenCost(remaining, minRate))
		maxCost.Add(maxCost, tokenCost(remaining, maxRate))
		spread.Sub(maxRate, minRate)
		for part, tokens := range partTokens {
			if tokens == nil && split[part] > 0 {
				recommendation.Add(recommendation, tokenCost(split[part], rates[part]))
			}
		}
	}

	maxYuan, _ := maxCost.Float64()
	if math.IsInf(maxYuan, 0) {
		res.Note = "金额超出可输出的数值范围"
		return res
	}
	width := new(big.Rat).Sub(maxCost, minCost)
	res.MinYuan, _ = minCost.Float64()
	res.MaxYuan = maxYuan
	res.RecommendedYuan, _ = recommendation.Float64()
	res.RecommendedRead, res.RecommendedWrite = split[cacheRead], split[cacheWrite]
	res.EstimatedYuan = res.RecommendedYuan
	res.MaxErrorYuan, _ = width.Float64()
	res.RateSpread, _ = spread.Float64()

	var relativeOK bool
	switch {
	case width.Sign() == 0:
		res.Class, res.CanAutoSettle = ClassExact, true
		res.Note = "合法拆分对应的金额唯一，可自动结算"
		return res
	case minCost.Sign() > 0:
		relative := new(big.Rat).Quo(width, minCost)
		res.RelativeError, _ = relative.Float64()
		res.RelativeError = math.Min(res.RelativeError, math.MaxFloat64)
		relativeOK = within(relative, relativeThreshold)
	default:
		// A positive width over a zero lower bound has no finite relative
		// limit. Keep the existing numeric field JSON-encodable.
		res.RelativeError = math.MaxFloat64
	}
	if within(width, thresholdYuan) || relativeOK {
		res.Class, res.CanAutoSettle = ClassNarrow, true
		res.Note = "金额区间满足结算阈值，可按推荐金额自动结算"
	} else {
		res.Class = ClassWide
		res.Note = "金额区间超过结算阈值，需要补充证据或人工核验"
	}
	return res
}

// split returns the evidence for each billed part and the undistributed
// balance. A nil billed part can absorb any integer share of that balance.
func (res *Result) split(in Input) ([4]*int64, int64) {
	parts := [4]*int64{nil, in.CacheReadTokens, in.CacheWriteTokens, in.CompletionTokens}
	tokenFields := [...]struct {
		name  string
		count *int64
	}{
		{"prompt_tokens", in.PromptTokens},
		{"completion_tokens", in.CompletionTokens},
		{"cache_read_tokens", in.CacheReadTokens},
		{"cache_write_tokens", in.CacheWriteTokens},
	}
	for _, field := range tokenFields {
		if field.count == nil {
			res.Missing = append(res.Missing, field.name)
		}
	}
	if in.PromptTokens == nil || in.CompletionTokens == nil {
		res.Note = "缺少输入或输出总量，无法计算金额区间"
		return parts, 0
	}
	for _, field := range tokenFields {
		if field.count != nil && *field.count < 0 {
			res.Note = "Token 数为负，证据矛盾"
			return parts, 0
		}
	}
	prompt, completion := *in.PromptTokens, *in.CompletionTokens
	if prompt > math.MaxInt64-completion || (in.TotalTokens != nil && *in.TotalTokens < completion) {
		res.Note = "输入输出与总 Token 不相符，或超出计数范围"
		return parts, 0
	}

	budget := prompt
	switch in.Protocol {
	case "anthropic":
		parts[ordinaryInput] = in.PromptTokens
		if in.TotalTokens != nil {
			budget = *in.TotalTokens - completion
			break
		}
		if in.CacheReadTokens == nil || in.CacheWriteTokens == nil {
			res.Missing = append(res.Missing, "total_tokens")
			res.Note = "Anthropic 缺少总 Token，无法给缓存缺项定上界"
			return parts, 0
		}
		for _, cache := range []*int64{in.CacheReadTokens, in.CacheWriteTokens} {
			if *cache > math.MaxInt64-completion-budget {
				res.Note = "分项之和超出总 Token 的计数范围"
				return parts, 0
			}
			budget += *cache
		}
	case "openai_compatible", "openai_responses", "google_gemini_v1beta":
		if in.TotalTokens != nil && *in.TotalTokens != prompt+completion {
			res.Note = "上游总量与输入输出之和不相等，证据矛盾"
			return parts, 0
		}
	default:
		res.Note = "协议缺少适用的纯文本计费口径"
		return parts, 0
	}
	remaining := budget
	for _, tokens := range parts[:output] {
		if tokens != nil {
			if *tokens > remaining {
				res.Note = "已知计费分项超过可分配的 Token 总量，证据矛盾"
				return parts, 0
			}
			remaining -= *tokens
		}
	}
	if in.CacheReadTokens == nil || in.CacheWriteTokens == nil {
		return parts, remaining
	}
	if parts[ordinaryInput] == nil {
		parts[ordinaryInput] = &remaining
	} else if remaining != 0 {
		res.Note = "分项齐全但仍有未归属的 Token，证据矛盾"
	}
	return parts, 0
}

func recommend(parts [4]*int64, remaining int64, readShare float64) [4]int64 {
	var split [4]int64
	for part, tokens := range parts {
		if tokens != nil {
			split[part] = *tokens
		}
	}
	if remaining == 0 {
		return split
	}
	if parts[cacheRead] == nil {
		read := remaining
		if parts[ordinaryInput] == nil || parts[cacheWrite] == nil {
			// Round half up in integers, including for counts near MaxInt64.
			rounded := tokenCost(remaining, decimal(readShare))
			rounded.Add(rounded, big.NewRat(1, 2))
			read = new(big.Int).Quo(rounded.Num(), rounded.Denom()).Int64()
		}
		split[cacheRead] = read
		remaining -= read
	}
	if parts[cacheWrite] == nil {
		write := remaining
		if parts[ordinaryInput] == nil {
			write /= 2
		}
		split[cacheWrite] = write
		remaining -= write
	}
	if parts[ordinaryInput] == nil {
		split[ordinaryInput] = remaining
	}
	return split
}

func tokenCost(tokens int64, rate *big.Rat) *big.Rat {
	return new(big.Rat).Mul(big.NewRat(tokens, 1), rate)
}

// Interpret finite configuration values as round-trippable decimals so a
// threshold comparison does not depend on binary float subtraction.
func decimal(value float64) *big.Rat {
	number, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	return number
}

func nonnegativeFinite(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0)
}

func within(value *big.Rat, threshold float64) bool {
	return nonnegativeFinite(threshold) && value.Cmp(decimal(threshold)) <= 0
}
