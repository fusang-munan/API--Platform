package dataplane

import (
	"encoding/json"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

// SSE connection and progress events can carry IDs, roles and even usage.
// Only generated output or a protocol terminal event can settle the charge.
func sseHasOutput(adapter modelbinding.Adapter, eventType string, payload map[string]json.RawMessage) bool {
	switch adapter {
	case modelbinding.AdapterOpenaiResponses:
		switch eventType {
		case "response.output_text.delta", "response.refusal.delta", "response.function_call_arguments.delta",
			"response.custom_tool_call_input.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
			"response.output_audio.delta", "response.output_audio_transcript.delta":
			return hasText(payload, "delta")
		case "response.output_text.done", "response.reasoning_summary_text.done", "response.reasoning_text.done":
			return hasText(payload, "text")
		case "response.refusal.done":
			return hasText(payload, "refusal")
		case "response.function_call_arguments.done":
			return hasText(payload, "arguments")
		case "response.custom_tool_call_input.done":
			return hasText(payload, "input")
		case "response.content_part.added", "response.content_part.done":
			return hasText(jsonObject(payload["part"]), "text", "refusal")
		case "response.output_item.done":
			item := jsonObject(payload["item"])
			switch rawString(item["type"]) {
			case "message":
				for _, part := range sseObjects(item["content"]) {
					if hasText(part, "text", "refusal") {
						return true
					}
				}
			case "function_call", "custom_tool_call", "computer_call":
				return hasText(item, "name", "arguments", "input") || jsonObject(item["action"]) != nil
			}
		}
	case modelbinding.AdapterAnthropic:
		switch eventType {
		case "content_block_start":
			block := jsonObject(payload["content_block"])
			return hasText(block, "text", "thinking") || len(jsonObject(block["input"])) != 0
		case "content_block_delta":
			return hasText(jsonObject(payload["delta"]), "text", "thinking", "partial_json")
		}
	case modelbinding.AdapterGoogleGeminiV1beta:
		for _, candidate := range sseObjects(payload["candidates"]) {
			for _, part := range sseObjects(jsonObject(candidate["content"])["parts"]) {
				if hasText(part, "text") ||
					hasText(jsonObject(part["functionCall"]), "name") ||
					hasText(jsonObject(part["inlineData"]), "data") ||
					hasText(jsonObject(part["executableCode"]), "code") ||
					hasText(jsonObject(part["codeExecutionResult"]), "output") {
					return true
				}
			}
		}
	default:
		for _, choice := range sseObjects(payload["choices"]) {
			if hasText(choice, "text") || hasChatOutput(jsonObject(choice["delta"])) || hasChatOutput(jsonObject(choice["message"])) {
				return true
			}
		}
	}
	return false
}

func hasChatOutput(value map[string]json.RawMessage) bool {
	if hasText(value, "content", "refusal", "reasoning_content", "reasoning") ||
		hasText(jsonObject(value["audio"]), "data", "transcript") ||
		hasText(jsonObject(value["function_call"]), "name", "arguments") {
		return true
	}
	for _, call := range sseObjects(value["tool_calls"]) {
		if hasText(jsonObject(call["function"]), "name", "arguments") || hasText(jsonObject(call["custom"]), "name", "input") {
			return true
		}
	}
	return false
}

func hasText(value map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		var text string
		if json.Unmarshal(value[field], &text) == nil && text != "" {
			return true
		}
	}
	return false
}

func sseObjects(raw json.RawMessage) []map[string]json.RawMessage {
	var objects []map[string]json.RawMessage
	if json.Unmarshal(raw, &objects) != nil {
		return nil
	}
	return objects
}
