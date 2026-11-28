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
	stopExplicit  bool
	terminal      bool
	geminiModel   string
	geminiReview  string
	geminiSeen    map[int]bool
	geminiDone    map[int]bool
	geminiUsage   bool
	geminiOutput  bool
	geminiLag     bool
	geminiBlocked bool
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

// Non-streaming generation responses must prove that the server finished the
// turn. Initial events and preparatory snapshots can contain partial usage.
func (metrics ResponseMetrics) finishedJSON(adapter modelbinding.Adapter, requestPath string, interrupted bool) usage.CallMetrics {
	if interrupted {
		return metrics.finished(true)
	}
	if jsonRequiresTerminal(adapter, requestPath) && !metrics.terminal {
		result := metrics.CallMetrics
		result.State = usage.UsageInterrupted
		if result.UsageReason == "" {
			if adapter == modelbinding.AdapterGoogleGeminiV1beta && (metrics.geminiOutput || metrics.geminiLag) {
				result.UsageReason = "terminal_usage_incomplete"
			} else {
				result.UsageReason = "terminal_usage_missing"
			}
		}
		return result
	}
	return metrics.finished(false)
}

func jsonRequiresTerminal(adapter modelbinding.Adapter, requestPath string) bool {
	path := strings.ToLower(strings.TrimSuffix(requestPath, "/"))
	switch adapter {
	case modelbinding.AdapterOpenaiCompatible:
		// Only generation endpoints carry a finish_reason. Embeddings, audio and
		// other OpenAI-compatible routes report usage without a turn terminal.
		return path == "" || strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/completions")
	case modelbinding.AdapterOpenaiResponses:
		// Compaction returns a finished summary that has no generation status.
		return path == "" || !strings.HasSuffix(path, "/responses/compact")
	case modelbinding.AdapterAnthropic:
		return path == "" || strings.HasSuffix(path, "/messages")
	case modelbinding.AdapterGoogleGeminiV1beta:
		// Gemini embeddings have no generation terminal marker.
		return !strings.HasSuffix(path, ":embedcontent") && !strings.HasSuffix(path, ":batchembedcontents")
	default:
		return false
	}
}

func (metrics *ResponseMetrics) applyPayload(adapter modelbinding.Adapter, payload map[string]json.RawMessage) {
	applyReplyState(adapter, metrics, payload)
	if adapter == modelbinding.AdapterGoogleGeminiV1beta {
		metrics.noteResponseID(payload["responseId"])
		metrics.MergeProtocolUsage(adapter, payload["usageMetadata"])
		if len(bytes.TrimSpace(payload["usageMetadata"])) != 0 && string(bytes.TrimSpace(payload["usageMetadata"])) != "null" {
			metrics.geminiUsage, metrics.geminiLag = true, false
		}
		metrics.noteGeminiPricingEvidence(payload)
		metrics.terminal = metrics.geminiTerminalComplete()
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
			applyReplyState(adapter, metrics, nested)
			metrics.noteResponseID(nested["id"])
			metrics.MergeProtocolUsage(adapter, nested["usage"])
		}
	}
	metrics.MergeProtocolUsage(adapter, payload["usage"])
}

func (metrics ResponseMetrics) geminiTerminalComplete() bool {
	if !metrics.geminiUsage {
		return false
	}
	if metrics.geminiBlocked {
		return true
	}
	if len(metrics.geminiSeen) == 0 || metrics.geminiLag {
		return false
	}
	for index := range metrics.geminiSeen {
		if !metrics.geminiDone[index] {
			return false
		}
	}
	return true
}

func (metrics *ResponseMetrics) noteGeminiPricingEvidence(payload map[string]json.RawMessage) {
	for _, field := range []string{"modelVersion", "model"} {
		if reported := strings.TrimPrefix(rawString(payload[field]), "models/"); reported != "" {
			if metrics.geminiModel != "" && metrics.geminiModel != reported {
				metrics.geminiReview = beichenModelMismatch
			}
			metrics.geminiModel = reported
		}
	}
	if metrics.geminiReview != "" {
		return
	}
	if payload == nil || (len(payload["error"]) != 0 && !bytes.Equal(bytes.TrimSpace(payload["error"]), []byte("null"))) {
		metrics.geminiReview = beichenUnsupportedPrice
		return
	}
	for _, field := range []string{"candidates", "usageMetadata"} {
		var value any
		if json.Unmarshal(payload[field], &value) == nil && !beichenGeminiTextValue(value) {
			metrics.geminiReview = beichenUnsupportedPrice
			return
		}
	}
}

func (metrics ResponseMetrics) beichenGeminiResponseReview(expectedModel string) string {
	// Gemini binds the model in the dispatched URL. modelVersion, when
	// present, can invalidate that evidence. It need not appear in every frame.
	if metrics.geminiModel != "" && metrics.geminiModel != strings.TrimPrefix(expectedModel, "models/") {
		return beichenModelMismatch
	}
	return metrics.geminiReview
}

// OpenAI's final usage is a snapshot. Earlier counters can still explain a
// partial observation. Independently priceable final snapshots stand alone.
func (metrics *ResponseMetrics) applyUsageSnapshot(adapter modelbinding.Adapter, payload map[string]json.RawMessage) {
	final := ResponseMetrics{}
	final.applyPayload(adapter, payload)
	if metrics.HasUsage() && !final.finished(false).ClassifyBeichenUsage(string(adapter)).CanPriceBeichenUsage(string(adapter)) {
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

func applyReplyState(adapter modelbinding.Adapter, metrics *ResponseMetrics, payload map[string]json.RawMessage) {
	if adapter == modelbinding.AdapterGoogleGeminiV1beta {
		if metrics.geminiSeen == nil {
			metrics.geminiSeen, metrics.geminiDone = map[int]bool{}, map[int]bool{}
		}
		var candidates []map[string]json.RawMessage
		if json.Unmarshal(payload["candidates"], &candidates) == nil {
			for position, candidate := range candidates {
				index := position
				var number float64
				if json.Unmarshal(candidate["index"], &number) == nil && number >= 0 && number <= 1_000_000 {
					index = int(number)
				}
				metrics.geminiSeen[index] = true
				if reason := rawString(candidate["finishReason"]); reason != "" && reason != "FINISH_REASON_UNSPECIFIED" {
					metrics.geminiDone[index] = true
				}
				if geminiCandidateHasOutput(candidate) {
					metrics.geminiOutput, metrics.geminiLag = true, true
				}
			}
		}
		if reason := rawString(jsonObject(payload["promptFeedback"])["blockReason"]); reason != "" && reason != "BLOCK_REASON_UNSPECIFIED" {
			metrics.geminiBlocked = true
		}
		return
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
			if adapter == modelbinding.AdapterAnthropic {
				noteStopReason(metrics, rawString(delta["stop_reason"]))
			} else {
				noteStopReason(metrics, rawString(delta["finish_reason"]))
			}
			noteToolRequest(metrics, deltaRaw)
		}
	}
	if adapter == modelbinding.AdapterAnthropic {
		noteStopReason(metrics, rawString(payload["stop_reason"]))
	} else {
		noteStopReason(metrics, rawString(payload["finish_reason"]))
	}
	eventType := strings.ToLower(rawString(payload["type"]))
	status := strings.ToLower(rawString(payload["status"]))
	messageStop := adapter == modelbinding.AdapterAnthropic && eventType == "message_stop"
	responseCompleted := adapter == modelbinding.AdapterOpenaiResponses && eventType == "response.completed"
	responseStatus := adapter == modelbinding.AdapterOpenaiResponses && (status == "completed" || status == "incomplete")
	if metrics.ReplyClosed == nil && (messageStop || responseCompleted || responseStatus) {
		closed := true
		metrics.ReplyClosed = &closed
	}
	if messageStop || responseCompleted || responseStatus {
		metrics.terminal = true
	}
}

func geminiCandidateHasOutput(candidate map[string]json.RawMessage) bool {
	var content map[string]json.RawMessage
	if json.Unmarshal(candidate["content"], &content) != nil {
		return false
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(content["parts"], &parts) != nil {
		return false
	}
	for _, part := range parts {
		if rawString(part["text"]) != "" || len(bytes.TrimSpace(part["functionCall"])) != 0 ||
			len(bytes.TrimSpace(part["executableCode"])) != 0 {
			return true
		}
	}
	return false
}

func noteStopReason(metrics *ResponseMetrics, reason string) {
	if strings.TrimSpace(reason) != "" {
		metrics.terminal = true
	}
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
