package dataplane

import "testing"

func TestExtractUserPromptFromPayloadInstructions(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"model":        "gpt-5.6-luna",
		"instructions": "请联网检索隐式规划论文",
		"input": []any{
			map[string]any{"type": "compaction", "encrypted_content": "opaque"},
		},
	})
	if text != "请联网检索隐式规划论文" || source != "instructions" {
		t.Fatalf("unexpected snapshot %q (%s)", text, source)
	}
}

func TestExtractUserPromptFromPayloadSkipsCompaction(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"type":  "response.create",
		"model": "gpt-5.3-codex",
		"input": []any{
			map[string]any{"type": "compaction", "encrypted_content": "opaque"},
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "find papers"}},
			},
		},
	})
	if text != "find papers" || source != "input_text" {
		t.Fatalf("unexpected snapshot %q (%s)", text, source)
	}
}
