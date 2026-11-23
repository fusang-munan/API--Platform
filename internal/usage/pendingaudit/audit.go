package pendingaudit

import "math"

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
}

type Result struct {
	Class         string   `json:"class"`
	Missing       []string `json:"missing"`
	UnknownTokens int64    `json:"unknown_tokens"`
	RateSpread    float64  `json:"rate_spread"`
	EstimatedYuan float64  `json:"estimated_yuan"`
	MaxErrorYuan  float64  `json:"max_error_yuan"`
	RelativeError float64  `json:"relative_error"`
	Note          string   `json:"note"`
}

func intValue(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func rateValue(v *float64) (float64, bool) {
	if v == nil || *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0, false
	}
	return *v, true
}

// Analyze gives an upper bound on the money that can be misallocated when the
// token split is incomplete. It never guesses a split; it only decides whether
// the remaining uncertainty is small enough to settle automatically.
func Analyze(in Input, thresholdYuan, relativeThreshold float64) Result {
	res := Result{Class: ClassUnknown, Missing: []string{}}
	if in.PromptTokens == nil || in.CompletionTokens == nil {
		if in.PromptTokens == nil {
			res.Missing = append(res.Missing, "prompt_tokens")
		}
		if in.CompletionTokens == nil {
			res.Missing = append(res.Missing, "completion_tokens")
		}
		res.Note = "缺少输入或输出总量，无法给出金额上界"
		return res
	}
	prompt := *in.PromptTokens
	completion := *in.CompletionTokens
	read := in.CacheReadTokens
	write := in.CacheWriteTokens
	if read == nil {
		res.Missing = append(res.Missing, "cache_read_tokens")
	}
	if write == nil {
		res.Missing = append(res.Missing, "cache_write_tokens")
	}

	var unknown int64
	switch in.Protocol {
	case "anthropic":
		if in.TotalTokens == nil && (read == nil || write == nil) {
			res.Note = "Anthropic 缺少总 Token，无法给缓存缺项定上界"
			return res
		}
		if in.TotalTokens != nil {
			known := prompt + completion + intValue(read) + intValue(write)
			if *in.TotalTokens < known {
				res.Note = "已知分项之和超过上游总量，证据矛盾"
				return res
			}
			unknown = *in.TotalTokens - known
		}
	case "openai_compatible", "openai_responses", "openai_realtime", "google_gemini_v1beta":
		if in.TotalTokens != nil && *in.TotalTokens != prompt+completion {
			res.Note = "上游总量与输入输出不相等，无法唯一确定缓存拆分"
			return res
		}
		if read == nil {
			unknown = prompt
		} else if write == nil {
			unknown = prompt - *read
			if unknown < 0 {
				res.Note = "缓存读超过输入总量，证据矛盾"
				return res
			}
		}
	default:
		res.Note = "未知协议，无法判断缓存口径"
		return res
	}
	res.UnknownTokens = unknown
	if unknown == 0 {
		res.Class = ClassExact
		res.Note = "所有计费分项已知，可直接结算"
		return res
	}

	// Only categories that can absorb the unknown tokens count. Output cannot
	// absorb cache ambiguity, and Anthropic prompt tokens exclude cache reads.
	eligible := []*float64{in.InputRate, in.CacheReadRate, in.CacheWriteRate}
	if in.Protocol == "anthropic" {
		eligible = []*float64{in.CacheReadRate, in.CacheWriteRate}
	}
	rates := make([]float64, 0, len(eligible))
	for _, r := range eligible {
		if value, ok := rateValue(r); ok {
			rates = append(rates, value)
		}
	}
	if len(rates) < 2 {
		res.Note = "缺少足够的价格分项，无法判断拆分对金额的影响"
		return res
	}
	minRate, maxRate := rates[0], rates[0]
	for _, r := range rates[1:] {
		minRate = math.Min(minRate, r)
		maxRate = math.Max(maxRate, r)
	}
	res.RateSpread = maxRate - minRate

	res.EstimatedYuan = float64(prompt)*value(in.InputRate) + float64(completion)*value(in.OutputRate) +
		float64(intValue(read))*value(in.CacheReadRate) + float64(intValue(write))*value(in.CacheWriteRate)
	res.MaxErrorYuan = float64(unknown) * res.RateSpread
	if res.EstimatedYuan > 0 {
		res.RelativeError = res.MaxErrorYuan / res.EstimatedYuan
	}
	switch {
	case res.RateSpread == 0:
		res.Class = ClassExact
		res.Note = "拆分不影响金额，可直接结算"
	case res.MaxErrorYuan <= thresholdYuan || res.RelativeError <= relativeThreshold:
		res.Class = ClassNarrow
		res.Note = "金额区间很窄，可按保守值自动结算并标记低置信度"
	default:
		res.Class = ClassWide
		res.Note = "金额区间较宽，需要历史估计或人工核验"
	}
	return res
}

func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
