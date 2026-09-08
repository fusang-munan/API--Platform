package dataplane

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

type CallCorrelation struct {
	TaskKey            string
	PreviousResponseID string
	ExplicitTurnID     string
	AgentTurnHeader    string
}

func extractCallCorrelation(requestPath, contentType string, replay *replayBody, headers http.Header) CallCorrelation {
	correlation := CallCorrelation{AgentTurnHeader: strings.TrimSpace(headers.Get("X-Agent-Turn-Id"))}
	if replay == nil {
		return correlation
	}
	reader, err := replay.Open()
	if err != nil {
		return correlation
	}
	defer reader.Close()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return correlation
	}
	var payload map[string]any
	switch mediaType {
	case "application/json":
		if json.NewDecoder(reader).Decode(&payload) != nil {
			return correlation
		}
	case "multipart/form-data":
		form := multipart.NewReader(reader, params["boundary"])
		for {
			part, partErr := form.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil || part.FormName() != "json" {
				if part != nil {
					_ = part.Close()
				}
				continue
			}
			_ = json.NewDecoder(part).Decode(&payload)
			_ = part.Close()
			break
		}
	default:
		return correlation
	}
	if payload == nil {
		return correlation
	}
	correlation.PreviousResponseID = firstNonEmpty(
		stringField(payload, "previous_response_id"),
		stringField(payload, "previous_response"),
	)
	correlation.TaskKey = firstNonEmpty(
		stringField(payload, "prompt_cache_key"),
		nestedStringField(payload, "client_metadata", "session_id"),
		nestedStringField(payload, "metadata", "session_id"),
		nestedStringField(payload, "metadata", "agent_task_id"),
		stringField(payload, "user"),
		strings.TrimSpace(headers.Get("X-Agent-Task-Id")),
	)
	return correlation
}

func extractPromptFromJSON(message []byte) (string, string) {
	var payload map[string]any
	if json.Unmarshal(message, &payload) != nil {
		return "", ""
	}
	return extractUserPromptFromPayload(payload)
}

func isInjectedAgentContent(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "<system-reminder>") ||
		strings.HasPrefix(trimmed, "<local-command-") ||
		strings.HasPrefix(trimmed, "<command-message>")
}

func extractUserPromptFromPayload(payload map[string]any) (string, string) {
	if value, ok := payload["prompt"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), "prompt"
	}
	if value, ok := payload["instructions"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), "instructions"
	}
	if value, ok := payload["input"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), "input"
	}
	if messages, ok := payload["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			if m, ok := messages[i].(map[string]any); ok && strings.EqualFold(fmt.Sprint(m["role"]), "user") {
				if text := allTextFromJSONValue(m["content"]); text != "" && !isInjectedAgentContent(text) {
					return text, "user_message"
				}
			}
		}
	}
	if contents, ok := payload["contents"].([]any); ok {
		for i := len(contents) - 1; i >= 0; i-- {
			if item, ok := contents[i].(map[string]any); ok && strings.EqualFold(fmt.Sprint(item["role"]), "user") {
				if text := allTextFromJSONValue(item["parts"]); text != "" {
					return text, "user_message"
				}
			}
		}
	}
	if input, ok := payload["input"].([]any); ok {
		if text, source := extractPromptInputArray(input); text != "" {
			return text, source
		}
	}
	return "", ""
}

func extractPromptInputArray(input []any) (string, string) {
	for _, raw := range input {
		item, isObject := raw.(map[string]any)
		if !isObject {
			if text := allTextFromJSONValue(raw); text != "" {
				return text, "input_text"
			}
			continue
		}
		if !promptInputItem(item) {
			continue
		}
		if role := strings.TrimSpace(fmt.Sprint(item["role"])); role != "" && !strings.EqualFold(role, "user") && !strings.EqualFold(role, "developer") {
			continue
		}
		if text := allTextFromJSONValue(raw); text != "" {
			return text, "input_text"
		}
	}
	return "", ""
}

func promptInputItem(item map[string]any) bool {
	typeName := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["type"])))
	switch typeName {
	case "", "message", "text", "input_text":
		return true
	case "compaction", "function_call", "function_call_output", "reasoning", "item_reference", "file_search_call", "web_search_call", "computer_call", "computer_call_output":
		return false
	default:
		return !strings.HasSuffix(typeName, "_call") && !strings.HasSuffix(typeName, "_output")
	}
}

func extractCallCorrelationFromJSON(message []byte) CallCorrelation {
	var payload map[string]any
	if json.Unmarshal(message, &payload) != nil {
		return CallCorrelation{}
	}
	return CallCorrelation{
		PreviousResponseID: firstNonEmpty(
			stringField(payload, "previous_response_id"),
			stringField(payload, "previous_response"),
		),
		TaskKey: firstNonEmpty(
			stringField(payload, "prompt_cache_key"),
			nestedStringField(payload, "client_metadata", "session_id"),
			nestedStringField(payload, "metadata", "session_id"),
			nestedStringField(payload, "metadata", "agent_task_id"),
			stringField(payload, "user"),
		),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringField(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(fmt.Sprint(value), `"`))
}

func nestedStringField(payload map[string]any, keys ...string) string {
	current := any(payload)
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	if current == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(current))
}
