package dataplane

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

const (
	beichenUnsupportedPrice = "unsupported_text_pricing"
	beichenModelMismatch    = "upstream_model_mismatch"
	beichenModelUnconfirmed = "upstream_model_unconfirmed"
	beichenIncompleteUsage  = "incomplete_token_breakdown"
)

func beichenHTTPRequestReview(path string, replay *replayBody, model string) string {
	if (path != "/v1/chat/completions" && path != "/v1/responses") || replay == nil {
		return beichenUnsupportedPrice
	}
	body, err := replay.Open()
	if err != nil {
		return beichenUnsupportedPrice
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxHTTPMetricsBody+1))
	if err != nil || len(data) > maxHTTPMetricsBody {
		return beichenUnsupportedPrice
	}
	return beichenPayloadReview(data, model, true)
}

func beichenModelReported(payload map[string]json.RawMessage, model string) bool {
	return model != "" && (rawString(payload["model"]) == model ||
		rawString(jsonObject(payload["response"])["model"]) == model)
}

func beichenPayloadReview(data []byte, model string, request bool) string {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil || payload == nil {
		return beichenUnsupportedPrice
	}
	if !request {
		for _, object := range []map[string]any{payload, objectField(payload, "response")} {
			if actual, ok := object["model"]; ok && actual != model {
				return beichenModelMismatch
			}
			if object["service_tier"] == "auto" {
				return beichenUnsupportedPrice
			}
			if !beichenCacheAliasesAgree(objectField(object, "usage")) {
				return beichenIncompleteUsage
			}
		}
		if payload["usage"] != nil && objectField(payload, "response")["usage"] != nil {
			return beichenIncompleteUsage
		}
	}
	if !beichenTextValue(payload) {
		return beichenUnsupportedPrice
	}
	return ""
}

func beichenCacheAliasesAgree(counts map[string]any) bool {
	details := objectField(counts, "prompt_tokens_details")
	for _, fields := range [][2]string{{"cached_tokens", "prompt_cache_hit_tokens"}, {"cache_write_tokens", "prompt_cache_write_tokens"}} {
		if nested, exists := details[fields[0]]; exists {
			if flat, exists := counts[fields[1]]; exists {
				left, leftOK := nested.(float64)
				right, rightOK := flat.(float64)
				if !leftOK || !rightOK || left != right {
					return false
				}
			}
		}
	}
	return true
}

func objectField(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	return value
}

func beichenTextValue(value any) bool {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if !beichenTextValue(child) {
				return false
			}
		}
	case map[string]any:
		for key, child := range node {
			switch key {
			case "modalities", "output_modalities":
				if list, ok := child.([]any); !ok || len(list) != 1 || list[0] != "text" {
					return false
				}
			case "service_tier":
				if child != nil && child != "default" && child != "auto" {
					return false
				}
			case "tools", "tool_calls", "functions", "function_call", "audio", "image_url",
				"file_id", "file_data", "file_url", "previous_response_id", "conversation", "prompt",
				"web_search_options", "prediction", "background":
				switch v := child.(type) {
				case nil:
				case bool:
					if v {
						return false
					}
				case []any:
					if len(v) != 0 {
						return false
					}
				default:
					return false
				}
			case "audio_tokens", "image_tokens", "video_tokens", "accepted_prediction_tokens", "rejected_prediction_tokens":
				if count, ok := child.(float64); !ok || count != 0 {
					return false
				}
			case "role":
				if child == "tool" || child == "function" {
					return false
				}
			case "type":
				if kind, ok := child.(string); ok {
					for _, part := range []string{"audio", "image", "video", "file", "tool", "function_call", "computer", "web_search", "mcp", "code_interpreter"} {
						if strings.Contains(kind, part) {
							return false
						}
					}
				}
			}
			if !beichenTextValue(child) {
				return false
			}
		}
	}
	return true
}

func beichenReviewMetrics(route *usage.CallRoute, metrics usage.CallMetrics, reason string) (usage.CallMetrics, string) {
	if reason == beichenUnsupportedPrice || reason == beichenModelMismatch {
		// MarkSent still records dispatch. Without a valid pricing identity,
		// the ledger must not replay this call against the bound text price.
		route.SentAt = time.Time{}
	}
	if metrics.State != usage.UsageReported {
		return metrics, reason
	}
	if reason == "" {
		if metrics.PromptTokens == nil || metrics.CompletionTokens == nil ||
			metrics.CacheHitTokens == nil || metrics.CacheWriteTokens == nil {
			reason = beichenIncompleteUsage
		} else {
			input, hit, write := *metrics.PromptTokens, *metrics.CacheHitTokens, *metrics.CacheWriteTokens
			if hit > input || write > input-hit ||
				(metrics.CacheMissTokens != nil && *metrics.CacheMissTokens != input-hit-write) ||
				(metrics.TotalTokens != nil && *metrics.TotalTokens != input+*metrics.CompletionTokens) {
				reason = beichenIncompleteUsage
			}
		}
	}
	if reason != "" {
		metrics.State = usage.UsageMissing
	}
	return metrics, reason
}
