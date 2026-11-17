package dataplane

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CallCorrelation struct {
	TaskKey            string
	PreviousResponseID string
	ExplicitTurnID     string
	AgentTurnHeader    string
	QuestionState      string
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
		nestedStringField(payload, "metadata", "user_id"),
		nestedStringField(payload, "metadata", "agent_task_id"),
		stringField(payload, "user"),
		strings.TrimSpace(headers.Get("X-Agent-Task-Id")),
	)
	correlation.QuestionState = questionStateFromPayload(payload)
	return correlation
}

func extractPromptFromJSON(message []byte) (string, string) {
	var payload map[string]any
	if json.Unmarshal(message, &payload) != nil {
		return "", ""
	}
	return extractUserPromptFromPayload(payload)
}

func normalizeMonitoredUserPrompt(text string) string {
	return usage.HumanTurnText(text)
}

func extractUserPromptFromPayload(payload map[string]any) (string, string) {
	if messages, ok := payload["messages"].([]any); ok {
		if text, source := lastUserTextFromRoleMessages(messages, "content"); text != "" {
			return text, source
		}
		if text, source := firstUserTextFromRoleMessages(messages, "content"); text != "" {
			return text, source
		}
	}
	if contents, ok := payload["contents"].([]any); ok {
		if text, source := lastUserTextFromRoleMessages(contents, "parts"); text != "" {
			return text, source
		}
	}
	if input, ok := payload["input"].([]any); ok {
		if text, source := extractPromptInputArray(input); text != "" {
			return text, source
		}
	}
	if value, ok := payload["input"].(string); ok {
		if text := normalizeMonitoredUserPrompt(value); text != "" {
			return text, "input"
		}
	}
	if value, ok := payload["prompt"].(string); ok {
		if text := normalizeMonitoredUserPrompt(value); text != "" {
			return text, "prompt"
		}
	}
	return "", ""
}

func lastUserTextFromRoleMessages(items []any, contentKey string) (string, string) {
	for i := len(items) - 1; i >= 0; i-- {
		if text, source := userTextFromMessageItem(items[i], contentKey); text != "" {
			return text, source
		}
	}
	return "", ""
}

func firstUserTextFromRoleMessages(items []any, contentKey string) (string, string) {
	for i := 0; i < len(items); i++ {
		if text, source := userTextFromMessageItem(items[i], contentKey); text != "" {
			return text, source
		}
	}
	return "", ""
}

func userTextFromMessageItem(item any, contentKey string) (string, string) {
	m, ok := item.(map[string]any)
	if !ok || !strings.EqualFold(fmt.Sprint(m["role"]), "user") {
		return "", ""
	}
	if text := normalizeMonitoredUserPrompt(allTextFromJSONValue(m[contentKey])); text != "" {
		return text, "user_message"
	}
	return "", ""
}

func extractPromptInputArray(input []any) (string, string) {
	for i := len(input) - 1; i >= 0; i-- {
		item, isObject := input[i].(map[string]any)
		if !isObject || !promptInputItem(item) {
			continue
		}
		role := strings.TrimSpace(fmt.Sprint(item["role"]))
		if role != "" && !strings.EqualFold(role, "user") {
			continue
		}
		if text := normalizeMonitoredUserPrompt(allTextFromJSONValue(item)); text != "" {
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
			nestedStringField(payload, "metadata", "user_id"),
			nestedStringField(payload, "metadata", "agent_task_id"),
			stringField(payload, "user"),
		),
		QuestionState: questionStateFromPayload(payload),
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
