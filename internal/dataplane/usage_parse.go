package dataplane

import (
	"bytes"
	"encoding/json"
	"strings"
)

type ResponseMetrics struct {
	UpstreamResponseID string
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
}

func parseResponseMetrics(contentType string, body []byte) ResponseMetrics {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "text/event-stream":
		return parseSSEResponseMetrics(body)
	default:
		return parseJSONResponseMetrics(body)
	}
}

func parseJSONResponseMetrics(body []byte) ResponseMetrics {
	if len(body) == 0 {
		return ResponseMetrics{}
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return ResponseMetrics{}
	}
	metrics := ResponseMetrics{}
	applyPayloadMetrics(&metrics, payload)
	return metrics
}

func parseSSEResponseMetrics(body []byte) ResponseMetrics {
	metrics := ResponseMetrics{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmed[5:])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		applyPayloadMetrics(&metrics, payload)
	}
	return metrics
}

func applyPayloadMetrics(metrics *ResponseMetrics, payload map[string]json.RawMessage) {
	if id := rawString(payload["id"]); id != "" {
		metrics.UpstreamResponseID = id
	}
	if responseRaw, ok := payload["response"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(responseRaw, &nested) == nil {
			if id := rawString(nested["id"]); id != "" {
				metrics.UpstreamResponseID = id
			}
			if usageRaw, ok := nested["usage"]; ok {
				applyUsageRaw(metrics, usageRaw)
			}
		}
	}
	if usageRaw, ok := payload["usage"]; ok {
		applyUsageRaw(metrics, usageRaw)
	}
}

func applyUsageRaw(metrics *ResponseMetrics, usageRaw json.RawMessage) {
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
	}
	if json.Unmarshal(usageRaw, &usage) != nil {
		return
	}
	prompt := usage.PromptTokens
	completion := usage.CompletionTokens
	total := usage.TotalTokens
	if prompt == 0 && usage.InputTokens > 0 {
		prompt = usage.InputTokens
	}
	if completion == 0 && usage.OutputTokens > 0 {
		completion = usage.OutputTokens
	}
	if total == 0 && prompt+completion > 0 {
		total = prompt + completion
	}
	metrics.PromptTokens = prompt
	metrics.CompletionTokens = completion
	metrics.TotalTokens = total
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}
