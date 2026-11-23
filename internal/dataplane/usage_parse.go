package dataplane

import (
	"bytes"
	"encoding/json"

	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type ResponseMetrics struct {
	usage.CallMetrics
	stopExplicit bool
}

func parseJSONResponseMetrics(adapter modelbinding.Adapter, body []byte) ResponseMetrics {
	var payload map[string]json.RawMessage
	metrics := ResponseMetrics{}
	if json.Unmarshal(body, &payload) == nil {
		metrics.applyPayload(adapter, payload)
	} else if adapter == modelbinding.AdapterGoogleGeminiV1beta && bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		// Gemini's REST streaming endpoint can return a JSON array when
		// the caller does not select SSE.
		var frames []map[string]json.RawMessage
		if json.Unmarshal(body, &frames) != nil {
			metrics.UsageReason = "invalid_usage_payload"
			return metrics
		}
		for _, frame := range frames {
			metrics.applyPayload(adapter, frame)
		}
	} else {
		metrics.UsageReason = "invalid_usage_payload"
	}
	return metrics
}

func (metrics ResponseMetrics) finished(interrupted bool) usage.CallMetrics {
	result := metrics.CallMetrics
	switch {
	case interrupted:
		result.State = usage.UsageInterrupted
		if result.UsageReason == "" {
			result.UsageReason = "response_interrupted"
		}
	case result.HasUsage():
		result.State = usage.UsageReported
	default:
		result.State = usage.UsageMissing
		if result.UsageReason == "" {
			result.UsageReason = "usage_not_reported"
		}
	}
	return result
}

func (metrics *ResponseMetrics) applyPayload(adapter modelbinding.Adapter, payload map[string]json.RawMessage) {
	applyReplyState(metrics, payload)
	if adapter == modelbinding.AdapterGoogleGeminiV1beta {
		metrics.noteResponseID(payload["responseId"])
		metrics.MergeProtocolUsage(adapter, payload["usageMetadata"])
		return
	}
	metrics.noteResponseID(payload["id"])
	var envelope string
	switch adapter {
	case modelbinding.AdapterOpenaiResponses:
		envelope = "response"
		metrics.noteResponseID(payload["response_id"])
	case modelbinding.AdapterAnthropic:
		envelope = "message"
	}
	if envelope != "" {
		if nested := jsonObject(payload[envelope]); nested != nil {
			applyReplyState(metrics, nested)
			metrics.noteResponseID(nested["id"])
			metrics.MergeProtocolUsage(adapter, nested["usage"])
		}
	}
	metrics.MergeProtocolUsage(adapter, payload["usage"])
}

// OpenAI's final usage is a snapshot. Earlier counters can still explain a
// partial observation, but only a complete final snapshot can authorize a debit.
func (metrics *ResponseMetrics) applyUsageSnapshot(adapter modelbinding.Adapter, payload map[string]json.RawMessage) {
	final := ResponseMetrics{}
	final.applyPayload(adapter, payload)
	if metrics.HasUsage() && final.finished(false).ClassifyBeichenUsage(string(adapter)).State != usage.UsageReported {
		metrics.applyPayload(adapter, payload)
		if metrics.UsageReason == "" {
			metrics.UsageReason = "terminal_usage_incomplete"
		}
		return
	}
	if final.UpstreamResponseID == "" {
		final.UpstreamResponseID = metrics.UpstreamResponseID
	}
	if final.ReplyClosed == nil {
		final.ReplyClosed, final.stopExplicit = metrics.ReplyClosed, metrics.stopExplicit
	}
	*metrics = final
}

func (metrics *ResponseMetrics) noteResponseID(raw json.RawMessage) {
	id := rawString(raw)
	if id != "" && len(id) <= 128 && !strings.ContainsRune(id, 0) {
		metrics.UpstreamResponseID = id
	}
}

func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func applyReplyState(metrics *ResponseMetrics, payload map[string]json.RawMessage) {
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
		closed := false
		metrics.ReplyClosed = &closed
	case "end_turn", "stop", "stop_sequence", "refusal", "max_tokens", "length":
		metrics.stopExplicit = true
		closed := true
		metrics.ReplyClosed = &closed
	}
}

func noteToolRequest(metrics *ResponseMetrics, raw json.RawMessage) {
	if metrics.stopExplicit || len(raw) == 0 || !jsonHasToolRequest(raw) {
		return
	}
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
	return ""
}
