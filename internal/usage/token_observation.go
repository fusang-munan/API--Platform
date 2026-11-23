package usage

import "math"

// ClassifyBeichenUsage keeps the native counters even when the four priced
// categories cannot be established. Omitted cache counts require evidence
// from the protocol's input equation before we fill them in.
func (m CallMetrics) ClassifyBeichenUsage(protocol string) CallMetrics {
	if m.UsageReason == "" || m.UsageReason == "response_interrupted" {
		m = m.withKnownInputParts(protocol)
	}
	if m.State != UsageReported {
		return m
	}
	if !m.HasTokenUsage() {
		m.State, m.UsageReason = UsageMissing, "usage_not_reported"
		return m
	}
	if m.UsageReason != "" {
		m.State = UsagePartial
		return m
	}
	if protocol == "google_gemini_v1beta" {
		// GenerateContent supplies cached reads and inclusive prompt size.
		// Its usageMetadata has no separate cache-creation count.
		m.State, m.UsageReason = UsagePartial, "cache_write_not_in_usage_metadata"
		return m
	}
	tokens, reason := normalizeBeichenConditionalTokens(m, protocol)
	if reason != "" {
		m.State, m.UsageReason = UsagePartial, "token_breakdown_unconfirmed"
		if m.PromptTokens != nil && m.CompletionTokens != nil && m.CacheHitTokens != nil && m.CacheWriteTokens != nil {
			m.UsageReason = "inconsistent_token_breakdown"
		}
		return m
	}
	if tokens.Output > math.MaxInt64-tokens.TotalIn ||
		(m.TotalTokens != nil && *m.TotalTokens != tokens.TotalIn+tokens.Output) {
		m.State, m.UsageReason = UsagePartial, "inconsistent_token_total"
		return m
	}
	m.CacheHitTokens, m.CacheWriteTokens, m.CacheMissTokens = &tokens.Read, &tokens.Write, &tokens.Input
	return m
}

func (m CallMetrics) withKnownInputParts(protocol string) CallMetrics {
	if m.PromptTokens == nil || *m.PromptTokens < 0 {
		return m
	}
	input := *m.PromptTokens
	read, write, miss := m.CacheHitTokens, m.CacheWriteTokens, m.CacheMissTokens
	switch protocol {
	case "anthropic":
		if miss == nil {
			m.CacheMissTokens = m.PromptTokens
		}
	case "google_gemini_v1beta":
		if read != nil && *read >= 0 && *read <= input {
			uncached := input - *read
			m.CacheMissTokens = &uncached
		}
	case "openai_compatible", "openai_responses", "openai_realtime":
		zero := int64(0)
		// Nonnegative categories are disjoint. If one covers the entire
		// input, any omitted category is necessarily zero.
		exhaustsInput := func(count *int64) bool { return count != nil && *count == input }
		if input > 0 && (exhaustsInput(read) || exhaustsInput(write) || exhaustsInput(miss)) {
			if read == nil {
				m.CacheHitTokens = &zero
			}
			if write == nil {
				m.CacheWriteTokens = &zero
			}
		}
		if read == nil && miss != nil && write != nil && *write >= 0 && *write <= input && *miss == input-*write {
			m.CacheHitTokens = &zero
		}
		if write == nil && miss != nil && read != nil && *read >= 0 && *read <= input && *miss == input-*read {
			m.CacheWriteTokens = &zero
		}
		if miss == nil && read != nil && write != nil && *read >= 0 && *read <= input && *write >= 0 && *write <= input-*read {
			uncached := input - *read - *write
			m.CacheMissTokens = &uncached
		}
	}
	return m
}
