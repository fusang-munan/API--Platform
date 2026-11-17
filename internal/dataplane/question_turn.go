package dataplane

import (
	"fmt"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

// questionStateFromPayload reads the harness transcript for a turn boundary.
// A reply that stopped on a tool call is still the same question. Human text
// after a reply that did not ask for a tool is the next question. A body with
// no assistant reply yet is unknown; the caller then uses the stored finish
// flag, or the idle gap when that flag was not captured.
func questionStateFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if messages, ok := payload["messages"].([]any); ok {
		return roleThreadState(messages, "content")
	}
	if contents, ok := payload["contents"].([]any); ok {
		return roleThreadState(contents, "parts")
	}
	if input, ok := payload["input"].([]any); ok {
		return inputThreadState(input)
	}
	if text, _ := extractUserPromptFromPayload(payload); text != "" {
		return usage.QuestionUnknown
	}
	return usage.QuestionContinue
}

func roleThreadState(items []any, contentKey string) string {
	lastAssistant := -1
	for i, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringField(message, "role"))
		if role == "assistant" || role == "model" {
			lastAssistant = i
		}
	}
	if lastAssistant < 0 {
		if text, _ := lastUserTextFromRoleMessages(items, contentKey); text != "" {
			return usage.QuestionUnknown
		}
		return usage.QuestionContinue
	}
	if unansweredToolUse(items, lastAssistant, contentKey) {
		return usage.QuestionContinue
	}
	if humanTextAfter(items, lastAssistant, contentKey) != "" {
		return usage.QuestionNew
	}
	return usage.QuestionContinue
}

func humanTextAfter(items []any, assistantIndex int, contentKey string) string {
	var last string
	for i := assistantIndex + 1; i < len(items); i++ {
		if text, _ := userTextFromMessageItem(items[i], contentKey); text != "" {
			last = text
		}
	}
	return last
}

func unansweredToolUse(items []any, assistantIndex int, contentKey string) bool {
	named := map[string]int{}
	anonymous := 0
	for _, block := range contentBlocks(items[assistantIndex], contentKey) {
		if !isToolRequestBlock(block) {
			continue
		}
		if id := toolRequestID(block); id != "" {
			named[id]++
		} else {
			anonymous++
		}
	}
	if len(named) == 0 && anonymous == 0 {
		return false
	}
	for i := assistantIndex + 1; i < len(items); i++ {
		for _, block := range contentBlocks(items[i], contentKey) {
			if !isToolResultBlock(block) {
				continue
			}
			if id := toolResultID(block); id != "" && named[id] > 0 {
				named[id]--
				continue
			}
			if anonymous > 0 {
				anonymous--
			}
		}
	}
	if anonymous > 0 {
		return true
	}
	for _, waiting := range named {
		if waiting > 0 {
			return true
		}
	}
	return false
}

func contentBlocks(message any, contentKey string) []map[string]any {
	body, ok := message.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := body[contentKey].([]any)
	if !ok {
		return nil
	}
	blocks := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		block, ok := item.(map[string]any)
		if ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func isToolRequestBlock(block map[string]any) bool {
	switch strings.ToLower(stringField(block, "type")) {
	case "tool_use", "server_tool_use", "tool_call":
		return true
	}
	_, ok := block["functionCall"]
	return ok
}

func isToolResultBlock(block map[string]any) bool {
	switch strings.ToLower(stringField(block, "type")) {
	case "tool_result", "tool_result_error":
		return true
	}
	_, ok := block["functionResponse"]
	return ok
}

func toolRequestID(block map[string]any) string {
	if id := firstNonEmpty(stringField(block, "id"), stringField(block, "tool_use_id")); id != "" {
		return id
	}
	if call, ok := block["functionCall"].(map[string]any); ok {
		return firstNonEmpty(stringField(call, "id"), stringField(call, "name"))
	}
	return ""
}

func toolResultID(block map[string]any) string {
	if id := firstNonEmpty(stringField(block, "tool_use_id"), stringField(block, "id")); id != "" {
		return id
	}
	if response, ok := block["functionResponse"].(map[string]any); ok {
		return firstNonEmpty(stringField(response, "id"), stringField(response, "name"))
	}
	return ""
}

func inputThreadState(input []any) string {
	lastAssistant := -1
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typeName := strings.ToLower(stringField(item, "type"))
		role := strings.ToLower(stringField(item, "role"))
		if (typeName == "message" || typeName == "") && role == "assistant" {
			lastAssistant = i
		}
	}
	if unansweredInputCalls(input, lastAssistant) {
		return usage.QuestionContinue
	}
	if lastAssistant >= 0 {
		if userInputTextAfter(input, lastAssistant) != "" {
			return usage.QuestionNew
		}
		return usage.QuestionContinue
	}
	// Tool output with no assistant message is still this question. A user
	// message that arrives after that output has no finish flag in the body.
	if lastTool := lastInputToolIndex(input); lastTool >= 0 {
		if userInputTextAfter(input, lastTool) != "" {
			return usage.QuestionUnknown
		}
		return usage.QuestionContinue
	}
	if text, _ := extractPromptInputArray(input); text != "" {
		return usage.QuestionUnknown
	}
	return usage.QuestionContinue
}

func lastInputToolIndex(input []any) int {
	last := -1
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typeName := strings.ToLower(stringField(item, "type"))
		if isInputToolCall(typeName) || isInputToolOutput(typeName) {
			last = i
		}
	}
	return last
}

func unansweredInputCalls(input []any, lastAssistant int) bool {
	named := map[string]int{}
	anonymous := 0
	for i := lastAssistant + 1; i < len(input); i++ {
		item, ok := input[i].(map[string]any)
		if !ok {
			continue
		}
		typeName := strings.ToLower(stringField(item, "type"))
		switch {
		case isInputToolCall(typeName):
			if id := firstNonEmpty(stringField(item, "call_id"), stringField(item, "id")); id != "" {
				named[id]++
			} else {
				anonymous++
			}
		case isInputToolOutput(typeName):
			id := firstNonEmpty(stringField(item, "call_id"), stringField(item, "id"))
			if id != "" && named[id] > 0 {
				named[id]--
				continue
			}
			if anonymous > 0 {
				anonymous--
			}
		}
	}
	if anonymous > 0 {
		return true
	}
	for _, waiting := range named {
		if waiting > 0 {
			return true
		}
	}
	return false
}

func isInputToolCall(typeName string) bool {
	switch typeName {
	case "function_call", "custom_tool_call", "computer_call", "shell_call", "local_shell_call":
		return true
	default:
		return false
	}
}

func isInputToolOutput(typeName string) bool {
	switch typeName {
	case "function_call_output", "custom_tool_call_output", "computer_call_output", "shell_call_output", "local_shell_call_output":
		return true
	default:
		return strings.HasSuffix(typeName, "_call_output")
	}
}

func userInputTextAfter(input []any, lastAssistant int) string {
	var last string
	for i := lastAssistant + 1; i < len(input); i++ {
		item, ok := input[i].(map[string]any)
		if !ok || !promptInputItem(item) {
			continue
		}
		role := strings.TrimSpace(fmt.Sprint(item["role"]))
		if role != "" && !strings.EqualFold(role, "user") {
			continue
		}
		if text := normalizeMonitoredUserPrompt(allTextFromJSONValue(item)); text != "" {
			last = text
		}
	}
	return last
}
