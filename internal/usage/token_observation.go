package usage

import "math"

// ClassifyBeichenUsage preserves nullable native counts even when a terminal
// observation has enough evidence to price only the input as a group.
func (m CallMetrics) ClassifyBeichenUsage(protocol string) CallMetrics {
	if m.State != UsageReported && !(m.State == UsageInterrupted && m.UsageReason == "response_interrupted") {
		return m
	}
	if !m.HasTokenUsage() && m.State == UsageReported {
		m.State, m.UsageReason = UsageMissing, "usage_not_reported"
		return m
	}
	if m.State == UsageReported && m.UsageReason != "" {
		m.State = UsagePartial
		return m
	}
	if protocol == "google_gemini_v1beta" {
		// GenerateContent usageMetadata reports inclusive prompt and cached
		// reads. It supplies no evidence about a Beichen cache-creation charge.
		if m.PromptTokens != nil && m.CacheHitTokens != nil && *m.CacheHitTokens >= 0 && *m.CacheHitTokens <= *m.PromptTokens {
			uncached := *m.PromptTokens - *m.CacheHitTokens
			m.CacheMissTokens = &uncached
		}
		if m.State == UsageReported {
			m.State, m.UsageReason = UsagePartial, "cache_write_not_in_usage_metadata"
		}
		return m
	}
	tokens, reason := normalizeBeichenConditionalTokens(m, protocol)
	if reason == "" {
		m.CacheMissTokens, m.CacheHitTokens, m.CacheWriteTokens = tokens.Input, tokens.Read, tokens.Write
	} else if protocol == "anthropic" && m.CacheMissTokens == nil && m.PromptTokens != nil && *m.PromptTokens >= 0 {
		m.CacheMissTokens = m.PromptTokens
	}
	if m.State == UsageReported && (reason != "" || tokens.Unsplit > 0) {
		m.State = UsagePartial
		switch reason {
		case "inconsistent_token_total", "inconsistent_token_breakdown":
			m.UsageReason = reason
		default:
			m.UsageReason = "token_breakdown_unconfirmed"
		}
	}
	return m
}

// CanPriceBeichenUsage admits a partial terminal observation only when its
// sole ambiguity is the allocation of a known number of input tokens.
func (m CallMetrics) CanPriceBeichenUsage(protocol string) bool {
	if m.State == UsageReported {
		return true
	}
	if m.State != UsagePartial || m.UsageReason != "token_breakdown_unconfirmed" {
		return false
	}
	tokens, reason := normalizeBeichenConditionalTokens(m, protocol)
	return reason == "" && tokens.Unsplit > 0
}

func normalizeBeichenConditionalTokens(m CallMetrics, protocol string) (beichenBillableTokens, string) {
	var t beichenBillableTokens
	for _, count := range []*int64{m.PromptTokens, m.CompletionTokens, m.TotalTokens, m.CacheHitTokens, m.CacheMissTokens, m.CacheWriteTokens, m.ReasoningTokens, m.ToolPromptTokens} {
		if count != nil && *count < 0 {
			return t, "inconsistent_token_breakdown"
		}
	}
	if m.PromptTokens == nil || m.CompletionTokens == nil {
		return t, "missing_price_or_token_breakdown"
	}
	t.Input, t.Read, t.Write = m.CacheMissTokens, m.CacheHitTokens, m.CacheWriteTokens
	t.TotalIn, t.Output = *m.PromptTokens, *m.CompletionTokens
	if m.ReasoningTokens != nil && *m.ReasoningTokens > t.Output {
		return t, "inconsistent_token_breakdown"
	}
	switch protocol {
	case "anthropic":
		if t.Input != nil && *t.Input != t.TotalIn {
			return t, "inconsistent_token_breakdown"
		}
		t.Input = m.PromptTokens
		if m.TotalTokens != nil && *m.TotalTokens >= t.Output {
			t.TotalIn = *m.TotalTokens - t.Output
		} else {
			for _, cache := range []*int64{t.Read, t.Write} {
				if cache == nil {
					return t, "missing_price_or_token_breakdown"
				}
				if *cache > math.MaxInt64-t.TotalIn {
					return t, "inconsistent_token_breakdown"
				}
				t.TotalIn += *cache
			}
		}
	case "openai_compatible", "openai_responses", "openai_realtime":
	default:
		return t, "unsupported_pricing_protocol"
	}
	if t.Output > math.MaxInt64-t.TotalIn || (m.TotalTokens != nil && *m.TotalTokens != t.TotalIn+t.Output) {
		return t, "inconsistent_token_total"
	}
	remaining, unknown := t.TotalIn, 0
	parts := []**int64{&t.Input, &t.Read, &t.Write}
	for _, part := range parts {
		if *part == nil {
			unknown++
			continue
		}
		if **part > remaining {
			return t, "inconsistent_token_breakdown"
		}
		remaining -= **part
	}
	if unknown == 0 && remaining != 0 {
		return t, "inconsistent_token_breakdown"
	}
	if unknown <= 1 || remaining == 0 {
		for _, part := range parts {
			if *part == nil {
				*part = &remaining
			}
		}
	} else {
		t.Unsplit = remaining
	}
	return t, ""
}
