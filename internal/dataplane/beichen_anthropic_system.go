package dataplane

import (
	"bytes"
	"encoding/json"
)

// Beichen's Anthropic endpoint silently ignores the standard top-level
// `system` field. It does honor a leading system message inside `messages`,
// including cache_control blocks, so move the instructions there before
// forwarding. Anthropic itself and other upstreams keep the standard shape.
func promoteBeichenAnthropicSystem(payload map[string]json.RawMessage) error {
	system, ok := payload["system"]
	if !ok {
		return nil
	}
	trimmed := bytes.TrimSpace(system)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var messages []json.RawMessage
	if raw, ok := payload["messages"]; ok {
		if err := json.Unmarshal(raw, &messages); err != nil {
			return nil
		}
	}
	if len(messages) > 0 {
		var first struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(messages[0], &first) == nil && first.Role == "system" {
			return nil
		}
	}
	var content json.RawMessage
	var text string
	if json.Unmarshal(system, &text) == nil {
		blocks, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
		if err != nil {
			return err
		}
		content = blocks
	} else {
		var blocks []json.RawMessage
		if json.Unmarshal(system, &blocks) != nil {
			return nil
		}
		content = system
	}
	message, err := json.Marshal(map[string]json.RawMessage{
		"role":    json.RawMessage(`"system"`),
		"content": content,
	})
	if err != nil {
		return err
	}
	messages = append([]json.RawMessage{message}, messages...)
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	payload["messages"] = raw
	delete(payload, "system")
	return nil
}
