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
	// ReplyClosed is nil until a stop flag is seen. False means the model
	// asked for a tool, so the question is still open.
	ReplyClosed   *bool
	toolRequested bool
	stopExplicit  bool
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
	applyReplyState(metrics, payload)
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

func applyReplyState(metrics *ResponseMetrics, payload map[string]json.RawMessage) {
	if responseRaw, ok := payload["response"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(responseRaw, &nested) == nil {
			applyReplyState(metrics, nested)
		}
	}
	noteToolRequest(metrics, payload["content"])
	noteToolRequest(metrics, payload["output"])
	noteToolRequest(metrics, payload["content_block"])
	noteToolRequest(metrics, payload["item"])
	if choicesRaw, ok := payload["choices"]; ok {
		var choices []map[string]json.RawMessage
		if json.Unmarshal(choicesRaw, &choices) == nil {
			for _, choice := range choices {
				noteStopReason(metrics, rawString(choice["finish_reason"]))
				noteToolRequest(metrics, choice["message"])
				noteToolRequest(metrics, choice["delta"])
				var delta map[string]json.RawMessage
				if json.Unmarshal(choice["delta"], &delta) == nil {
					noteStopReason(metrics, rawString(delta["finish_reason"]))
				}
			}
		}
	}
	if deltaRaw, ok := payload["delta"]; ok {
		var delta map[string]json.RawMessage
		if json.Unmarshal(deltaRaw, &delta) == nil {
			noteStopReason(metrics, rawString(delta["stop_reason"]))
			noteStopReason(metrics, rawString(delta["finish_reason"]))
			noteToolRequest(metrics, deltaRaw)
		}
	}
	noteStopReason(metrics, rawString(payload["stop_reason"]))
	noteStopReason(metrics, rawString(payload["finish_reason"]))
	eventType := strings.ToLower(rawString(payload["type"]))
	status := strings.ToLower(rawString(payload["status"]))
	if metrics.ReplyClosed == nil && (eventType == "message_stop" || eventType == "response.completed" || status == "completed" || status == "incomplete") {
		closed := true
		metrics.ReplyClosed = &closed
	}
}

func noteStopReason(metrics *ResponseMetrics, reason string) {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_use", "tool_calls", "pause_turn":
		metrics.stopExplicit = true
		metrics.toolRequested = true
		closed := false
		metrics.ReplyClosed = &closed
	case "end_turn", "stop", "stop_sequence", "refusal", "max_tokens", "length":
		metrics.stopExplicit = true
		metrics.toolRequested = false
		closed := true
		metrics.ReplyClosed = &closed
	}
}

func noteToolRequest(metrics *ResponseMetrics, raw json.RawMessage) {
	if metrics.stopExplicit || len(raw) == 0 || !jsonHasToolRequest(raw) {
		return
	}
	metrics.toolRequested = true
	closed := false
	metrics.ReplyClosed = &closed
}

func jsonHasToolRequest(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return valueHasToolRequest(value)
}

func valueHasToolRequest(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		typeName, _ := typed["type"].(string)
		switch strings.ToLower(typeName) {
		case "tool_use", "server_tool_use", "tool_call", "function_call", "custom_tool_call", "computer_call":
			return true
		}
		if calls, ok := typed["tool_calls"].([]any); ok && len(calls) > 0 {
			return true
		}
		if _, ok := typed["functionCall"]; ok {
			return true
		}
		for _, child := range typed {
			if valueHasToolRequest(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valueHasToolRequest(child) {
				return true
			}
		}
	}
	return false
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
