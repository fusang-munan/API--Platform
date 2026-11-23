package dataplane

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

const (
	beichenUnsupportedPrice = "unsupported_text_pricing"
	beichenModelMismatch    = "upstream_model_mismatch"
	beichenModelUnconfirmed = "upstream_model_unconfirmed"
	beichenIncompleteUsage  = "incomplete_token_breakdown"
)

func beichenHTTPRequestReview(path string, replay *replayBody, route *usage.CallRoute) string {
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages":
	default:
		return beichenUnsupportedPrice
	}
	if replay == nil {
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
	return beichenRequestReview(data, path, route)
}

func beichenRequestReview(data []byte, path string, route *usage.CallRoute) string {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil || payload == nil {
		return beichenUnsupportedPrice
	}
	var rawEffort any
	switch path {
	case "/v1/chat/completions":
		rawEffort = payload["reasoning_effort"]
	case "/v1/responses":
		rawEffort = objectField(payload, "reasoning")["effort"]
	case "/v1/messages":
		rawEffort = objectField(payload, "output_config")["effort"]
		if rawEffort == nil {
			rawEffort = objectField(payload, "thinking")["effort"]
		}
		if rawEffort == nil {
			rawEffort = payload["effort"]
		}
		route.CacheTTL = anthropicCacheTTL(payload)
	}
	effort := usage.DefaultReasoningEffort
	if rawEffort != nil {
		value, ok := rawEffort.(string)
		if !ok || len(value) > 32 {
			return beichenUnsupportedPrice
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) >= 0 {
			return beichenUnsupportedPrice
		}
		if value != "" {
			effort = value
		}
	}
	route.ReasoningEffort = &effort
	if !beichenTextValue(payload) {
		return beichenUnsupportedPrice
	}
	return ""
}

// A single TTL is usable when all Anthropic cache breakpoints agree. Only
// protocol-defined cache_control fields count, including the automatic one.
func anthropicCacheTTL(payload map[string]any) string {
	var ttl string
	observe := func(node map[string]any) bool {
		raw := node["cache_control"]
		if raw == nil {
			return true
		}
		control, ok := raw.(map[string]any)
		if !ok || (control["type"] != nil && control["type"] != "ephemeral") {
			return false
		}
		duration, _ := control["ttl"].(string)
		if control["ttl"] == nil && control["type"] == "ephemeral" {
			duration = "5m"
		}
		if (duration != "5m" && duration != "1h") || (ttl != "" && ttl != duration) {
			return false
		}
		ttl = duration
		return true
	}
	observeBlocks := func(raw any) bool {
		blocks, _ := raw.([]any)
		for _, block := range blocks {
			node, _ := block.(map[string]any)
			if !observe(node) {
				return false
			}
		}
		return true
	}
	if !observe(payload) || !observeBlocks(payload["system"]) || !observeBlocks(payload["tools"]) {
		return ""
	}
	messages, _ := payload["messages"].([]any)
	for _, message := range messages {
		node, _ := message.(map[string]any)
		if !observeBlocks(node["content"]) {
			return ""
		}
	}
	return ttl
}

func beichenModelReported(payload map[string]json.RawMessage, model string) bool {
	envelope := "response"
	if rawString(payload["type"]) == "message_start" {
		envelope = "message"
	}
	return model != "" && (rawString(payload["model"]) == model ||
		rawString(jsonObject(payload[envelope])["model"]) == model)
}

// An upstream output limit ends generation with final usage. Other
// incomplete responses still require independent billing evidence.
func beichenResponsesOutputLimited(response map[string]json.RawMessage) bool {
	return rawString(response["status"]) == "incomplete" &&
		rawString(jsonObject(response["incomplete_details"])["reason"]) == "max_output_tokens"
}

func beichenPayloadReview(data []byte, model string, request bool) string {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil || payload == nil {
		return beichenUnsupportedPrice
	}
	if !request {
		envelope := "response"
		if payload["type"] == "message_start" {
			envelope = "message"
		}
		nested := objectField(payload, envelope)
		for _, object := range []map[string]any{payload, nested} {
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
		if payload["usage"] != nil && nested["usage"] != nil {
			return beichenIncompleteUsage
		}
	}
	if !beichenTextValue(payload) {
		return beichenUnsupportedPrice
	}
	return ""
}

func beichenCacheAliasesAgree(counts map[string]any) bool {
	for _, fields := range [][2]string{{"cached_tokens", "prompt_cache_hit_tokens"}, {"cache_write_tokens", "prompt_cache_write_tokens"}} {
		if flat, exists := counts[fields[1]]; exists {
			for _, name := range []string{"prompt_tokens_details", "input_tokens_details"} {
				nested, present := objectField(counts, name)[fields[0]]
				if !present {
					continue
				}
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

func beichenReviewMetrics(adapter modelbinding.Adapter, metrics usage.CallMetrics, reason string) (usage.CallMetrics, string) {
	if reason == beichenIncompleteUsage && metrics.UsageReason == "" {
		metrics.UsageReason = "conflicting_usage_fields"
	}
	metrics = metrics.ClassifyBeichenUsage(string(adapter))
	if reason == "" && metrics.State == usage.UsagePartial && !metrics.CanPriceBeichenUsage(string(adapter)) {
		reason = beichenIncompleteUsage
	}
	return metrics, reason
}
