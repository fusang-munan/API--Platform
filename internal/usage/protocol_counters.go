package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

const (
	cacheWriteConflictReason     = "conflicting_cache_creation_counts"
	cacheWriteSubtotalOnlyReason = "cache_write_subtotal_only"
)

// Token counts across events are cumulative. A missing field leaves the last
// explicit value intact, including when that value was zero.
func (metrics *CallMetrics) MergeProtocolUsage(adapter modelbinding.Adapter, raw json.RawMessage) {
	values := jsonObject(raw)
	if values == nil {
		if field := bytes.TrimSpace(raw); len(field) != 0 && !bytes.Equal(field, []byte("null")) {
			metrics.UsageReason = "invalid_usage_payload"
		}
		return
	}
	mergeCount := metrics.mergeCount
	switch adapter {
	case modelbinding.AdapterGoogleGeminiV1beta:
		mergeCount(&metrics.PromptTokens, math.MaxInt32, values["promptTokenCount"])
		mergeCount(&metrics.CompletionTokens, math.MaxInt32, values["candidatesTokenCount"])
		mergeCount(&metrics.TotalTokens, math.MaxInt32, values["totalTokenCount"])
		mergeCount(&metrics.CacheHitTokens, math.MaxInt64, values["cachedContentTokenCount"])
		mergeCount(&metrics.ReasoningTokens, math.MaxInt64, values["thoughtsTokenCount"])
		mergeCount(&metrics.ToolPromptTokens, math.MaxInt64, values["toolUsePromptTokenCount"])
		return
	case modelbinding.AdapterAnthropic:
		mergeCount(&metrics.PromptTokens, math.MaxInt32, values["input_tokens"])
		mergeCount(&metrics.CompletionTokens, math.MaxInt32, values["output_tokens"])
		mergeCount(&metrics.CacheHitTokens, math.MaxInt64, values["cache_read_input_tokens"])
		cache := jsonObject(values["cache_creation"])
		mergeCount(&metrics.cacheWriteReported, math.MaxInt64, values["cache_creation_input_tokens"])
		mergeCount(&metrics.cacheWrite5m, math.MaxInt64, cache["ephemeral_5m_input_tokens"])
		mergeCount(&metrics.cacheWrite1h, math.MaxInt64, cache["ephemeral_1h_input_tokens"])
		mergeCount(&metrics.ReasoningTokens, math.MaxInt64, jsonObject(values["output_tokens_details"])["thinking_tokens"])
	case modelbinding.AdapterOpenaiResponses:
		input := jsonObject(values["input_tokens_details"])
		output := jsonObject(values["output_tokens_details"])
		mergeCount(&metrics.PromptTokens, math.MaxInt32, values["input_tokens"])
		mergeCount(&metrics.CompletionTokens, math.MaxInt32, values["output_tokens"])
		mergeCount(&metrics.CacheHitTokens, math.MaxInt64, input["cached_tokens"], values["prompt_cache_hit_tokens"])
		mergeCount(&metrics.CacheMissTokens, math.MaxInt64, values["prompt_cache_miss_tokens"])
		mergeCount(&metrics.CacheWriteTokens, math.MaxInt64, input["cache_write_tokens"], values["prompt_cache_write_tokens"])
		mergeCount(&metrics.ReasoningTokens, math.MaxInt64, output["reasoning_tokens"])
	default:
		input := jsonObject(values["prompt_tokens_details"])
		output := jsonObject(values["completion_tokens_details"])
		mergeCount(&metrics.PromptTokens, math.MaxInt32, values["prompt_tokens"])
		mergeCount(&metrics.CompletionTokens, math.MaxInt32, values["completion_tokens"])
		mergeCount(&metrics.CacheHitTokens, math.MaxInt64, input["cached_tokens"], values["prompt_cache_hit_tokens"])
		mergeCount(&metrics.CacheMissTokens, math.MaxInt64, values["prompt_cache_miss_tokens"])
		mergeCount(&metrics.CacheWriteTokens, math.MaxInt64, input["cache_write_tokens"], values["prompt_cache_write_tokens"])
		mergeCount(&metrics.ReasoningTokens, math.MaxInt64, output["reasoning_tokens"], values["completion_thinking_tokens"])
	}
	mergeCount(&metrics.TotalTokens, math.MaxInt32, values["total_tokens"])
	if adapter == modelbinding.AdapterAnthropic {
		metrics.reconcileAnthropicCacheWrites()
	}
	rawCredit := bytes.TrimSpace(values["credit"])
	if len(rawCredit) != 0 && rawCredit[0] >= '0' && rawCredit[0] <= '9' {
		credit, err := strconv.ParseFloat(string(rawCredit), 64)
		micros := math.Round(credit * 1e6)
		if err == nil && micros < float64(math.MaxInt64) {
			value := int64(micros)
			metrics.UpstreamCreditMicros = &value
		}
	}
}

func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func (metrics *CallMetrics) mergeCount(target **int64, maxValue int64, fields ...json.RawMessage) {
	var candidate *int64
	for _, field := range fields {
		value := bytes.TrimSpace(field)
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			continue
		}
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err == nil && n >= 0 && n <= maxValue {
			if candidate != nil && *candidate != n {
				metrics.UsageReason = "conflicting_usage_fields"
			}
			if candidate == nil {
				candidate = &n
			}
			continue
		}
		if metrics.UsageReason == "" {
			metrics.UsageReason = "invalid_token_count"
		}
	}
	if candidate != nil {
		*target = candidate
	}
}

// Anthropic can report the cache-creation total, the 5m and 1h children, or
// both. A child is evidence even when the parent is omitted. Reconcile it with
// the parent and the inclusive prompt equation before token_observation fills
// any missing category from a remainder.
func (metrics *CallMetrics) reconcileAnthropicCacheWrites() {
	childCount, childSum := 0, int64(0)
	for _, child := range []*int64{metrics.cacheWrite5m, metrics.cacheWrite1h} {
		if child == nil {
			continue
		}
		childCount++
		if *child > math.MaxInt64-childSum {
			metrics.setCacheWriteReason(cacheWriteConflictReason)
			return
		}
		childSum += *child
	}
	if childCount == 0 && metrics.cacheWriteReported == nil {
		return
	}

	conflict := false
	var totalWrite *int64
	if metrics.cacheWriteReported != nil {
		value := *metrics.cacheWriteReported
		totalWrite = &value
	}
	if childCount == 2 {
		if totalWrite != nil && *totalWrite != childSum {
			conflict = true
		} else if totalWrite == nil {
			value := childSum
			totalWrite = &value
		}
	} else if childCount == 1 && totalWrite != nil && childSum > *totalWrite {
		conflict = true
	}

	// Anthropic's total_tokens includes input + cache read + cache write +
	// output. Only use the equation when all of its other terms are explicit.
	if metrics.TotalTokens != nil && metrics.PromptTokens != nil &&
		metrics.CompletionTokens != nil && metrics.CacheHitTokens != nil {
		totalInput := *metrics.TotalTokens - *metrics.CompletionTokens
		if totalInput < 0 {
			conflict = true
		} else {
			derived := totalInput - *metrics.PromptTokens - *metrics.CacheHitTokens
			switch {
			case derived < 0:
				conflict = true
			case totalWrite != nil && *totalWrite != derived:
				conflict = true
			case childCount == 2 && childSum != derived:
				conflict = true
			case childCount == 1 && childSum > derived:
				conflict = true
			case totalWrite == nil:
				value := derived
				totalWrite = &value
			}
		}
	}

	if conflict {
		// Keep the strongest observed evidence instead of replacing it with a
		// zero inferred from an equation that the same evidence contradicts.
		switch {
		case metrics.cacheWriteReported != nil:
			value := *metrics.cacheWriteReported
			metrics.CacheWriteTokens = &value
		case childCount > 0:
			value := childSum
			metrics.CacheWriteTokens = &value
		case totalWrite != nil:
			metrics.CacheWriteTokens = totalWrite
		}
		metrics.setCacheWriteReason(cacheWriteConflictReason)
		return
	}
	if totalWrite == nil {
		metrics.setCacheWriteReason(cacheWriteSubtotalOnlyReason)
		return
	}
	value := *totalWrite
	metrics.CacheWriteTokens = &value
	if childCount == 1 && childSum < *totalWrite {
		metrics.setCacheWriteReason(cacheWriteSubtotalOnlyReason)
		return
	}
	if metrics.UsageReason == cacheWriteSubtotalOnlyReason {
		metrics.UsageReason = ""
	}
}

func (metrics *CallMetrics) setCacheWriteReason(reason string) {
	if metrics.UsageReason == "" || metrics.UsageReason == cacheWriteSubtotalOnlyReason {
		metrics.UsageReason = reason
	}
}
