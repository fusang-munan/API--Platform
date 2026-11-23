package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
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
		var writes *int64
		mergeCount(&writes, math.MaxInt64, values["cache_creation_input_tokens"])
		cache := jsonObject(values["cache_creation"])
		var short, long *int64
		mergeCount(&short, math.MaxInt64, cache["ephemeral_5m_input_tokens"])
		mergeCount(&long, math.MaxInt64, cache["ephemeral_1h_input_tokens"])
		if short != nil && long != nil && *short <= math.MaxInt64-*long {
			total := *short + *long
			if writes == nil {
				writes = &total
			} else if *writes != total {
				metrics.UsageReason = "conflicting_cache_creation_counts"
			}
		} else if short != nil && long != nil {
			metrics.UsageReason = "invalid_token_count"
		}
		if writes != nil {
			metrics.CacheWriteTokens = writes
		}
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
	var observed *int64
	for _, field := range fields {
		value := bytes.TrimSpace(field)
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			continue
		}
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err == nil && n >= 0 && n <= maxValue {
			if observed != nil && *observed != n {
				metrics.UsageReason = "conflicting_usage_fields"
			} else {
				observed = &n
				*target = observed
			}
			continue
		}
		if metrics.UsageReason == "" {
			metrics.UsageReason = "invalid_token_count"
		}
	}
}
