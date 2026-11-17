package dataplane

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// DeepSeek thinking mode is on by default. Once a request carries tools, every
// earlier assistant turn must include a thinking block or the API returns 400.
// Claude Code drops those blocks on replay. Keep a block the client already
// sent, and fill only the turns that no longer have one.
func repairDeepSeekAnthropicThinking(payload map[string]json.RawMessage) error {
	if deepseekThinkingDisabled(payload["thinking"]) || !deepseekRequestCarriesTools(payload["tools"]) {
		return nil
	}
	raw, ok := payload["messages"]
	if !ok {
		return nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil
	}
	changed := false
	for i, message := range messages {
		if !bytes.Equal(bytes.TrimSpace(message["role"]), []byte(`"assistant"`)) {
			continue
		}
		content, ok := message["content"]
		if !ok {
			continue
		}
		repaired, did, err := ensureAssistantThinking(content)
		if err != nil || !did {
			continue
		}
		messages[i]["content"] = repaired
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	payload["messages"] = encoded
	return nil
}

func deepseekHost(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.deepseek.com")
}

func deepseekThinkingDisabled(raw json.RawMessage) bool {
	var body struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(bytes.TrimSpace(raw), &body) != nil {
		return false
	}
	return body.Type == "disabled"
}

func deepseekRequestCarriesTools(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	return len(tools) > 0
}

func ensureAssistantThinking(content json.RawMessage) (json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		repaired, err := json.Marshal([]any{emptyThinkingBlock()})
		return repaired, err == nil, err
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return content, false, err
		}
		repaired, err := json.Marshal([]any{
			emptyThinkingBlock(),
			textContentBlock{Type: "text", Text: text},
		})
		return repaired, err == nil, err
	}
	if trimmed[0] != '[' {
		return content, false, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return content, false, err
	}
	for _, block := range blocks {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(block, &head) == nil && head.Type == "thinking" {
			return content, false, nil
		}
	}
	thinking, err := json.Marshal(emptyThinkingBlock())
	if err != nil {
		return content, false, err
	}
	blocks = append([]json.RawMessage{thinking}, blocks...)
	repaired, err := json.Marshal(blocks)
	return repaired, err == nil, err
}

type thinkingContentBlock struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type textContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func emptyThinkingBlock() thinkingContentBlock {
	return thinkingContentBlock{Type: "thinking", Thinking: ""}
}
