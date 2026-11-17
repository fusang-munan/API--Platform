package dataplane

import "testing"

func TestExtractUserPromptPrefersInputOverInstructions(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"instructions": "You are Codex, an agent based on GPT-5.",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "say OK"},
				},
			},
		},
	})
	if text != "say OK" || source != "input_text" {
		t.Fatalf("unexpected snapshot %q (%s)", text, source)
	}
}

func TestExtractUserPromptIgnoresInstructionsWithoutUser(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"instructions": "You are Codex, an agent based on GPT-5.",
		"input": []any{
			map[string]any{"type": "compaction", "encrypted_content": "opaque"},
		},
	})
	if text != "" || source != "" {
		t.Fatalf("expected empty snapshot, got %q (%s)", text, source)
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

func TestExtractUserPromptUsesLastUserMessage(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{
					map[string]any{"type": "input_text", "text": "You are Codex, a coding agent."},
				},
			},
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "first"},
				},
			},
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "second"},
				},
			},
		},
	})
	if text != "second" || source != "input_text" {
		t.Fatalf("unexpected snapshot %q (%s)", text, source)
	}
}

func TestNormalizeMonitoredUserPromptStripsSystemReminderPrefix(t *testing.T) {
	t.Parallel()
	raw := "<system-reminder>Claude Code injected context.</system-reminder>\n\n[USER-ROUND-1] 我想要了解自进化的相关文献"
	text := normalizeMonitoredUserPrompt(raw)
	if text != "[USER-ROUND-1] 我想要了解自进化的相关文献" {
		t.Fatalf("unexpected prompt %q", text)
	}
}

func TestExtractUserPromptFromAnthropicMessagesWithSystemReminder(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<system-reminder>injected</system-reminder>\n\n真实用户问题",
					},
				},
			},
		},
	})
	if text != "真实用户问题" || source != "user_message" {
		t.Fatalf("unexpected %q (%s)", text, source)
	}
}

func TestExtractUserPromptSkipsClaudeResumeTranscript(t *testing.T) {
	t.Parallel()
	transcript := `<transcript>
{"user":"[Compaction summary — written by the assistant, not by the user. The user request is unrelated context."}
{"user":"[SYSTEM NOTIFICATION - NOT USER INPUT] background task"}
{"user":"<command-name>/model</command-name>"}
{"user":"继续工作"}
</transcript>`
	text, source := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "帮我找训练无关的论文"},
			map[string]any{"role": "user", "content": transcript},
		},
	})
	if text != "帮我找训练无关的论文" || source != "user_message" {
		t.Fatalf("unexpected %q (%s)", text, source)
	}
}

func TestExtractUserPromptIgnoresResumeSummaryWhenHistoryWasReplaced(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "This session is being continued from a previous conversation that ran out of context.\n1. Primary Request and Intent\n“你帮我对齐，然后开始用现有的卡跑 stage0-2 阶段”\n2. Key Technical Concepts",
			},
		},
	})
	if text != "" || source != "" {
		t.Fatalf("resume summary was stored as the user prompt %q (%s)", text, source)
	}
}

func TestExtractUserPromptSkipsCommandEnvelopeForEarlierHumanTurn(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "帮我看停止判定器"},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}}},
			map[string]any{"role": "user", "content": "<command-name>/clear</command-name><command-message>clear</command-message>"},
		},
	})
	if text != "帮我看停止判定器" || source != "user_message" {
		t.Fatalf("unexpected %q (%s)", text, source)
	}
}

func TestExtractCodexPromptIgnoresToolReplay(t *testing.T) {
	t.Parallel()
	text, source := extractUserPromptFromPayload(map[string]any{
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "find papers"}},
			},
			map[string]any{"type": "function_call", "name": "shell", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "output": "exit 6"},
			map[string]any{"type": "reasoning", "encrypted_content": "opaque"},
			map[string]any{"type": "compaction", "encrypted_content": "opaque"},
		},
	})
	if text != "find papers" || source != "input_text" {
		t.Fatalf("unexpected %q (%s)", text, source)
	}
}

func TestExtractUserPromptDropsResumeSummaryWithoutUserLine(t *testing.T) {
	t.Parallel()
	text, _ := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "This session is being continued from a previous conversation that ran out of context. Primary Request: keep working.",
			},
		},
	})
	if text != "" {
		t.Fatalf("summary should not be stored as the user prompt, got %q", text)
	}
}

func TestExtractUserPromptDropsCompactionInstruction(t *testing.T) {
	t.Parallel()
	text, _ := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far.",
			},
		},
	})
	if text != "" {
		t.Fatalf("compaction instruction should not be stored, got %q", text)
	}
}

func TestExtractUserPromptDropsClaudeMdInjection(t *testing.T) {
	t.Parallel()
	text, _ := extractUserPromptFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "真正的问题"},
			map[string]any{"role": "user", "content": "The following is the user's CLAUDE.md configuration. Treat it as project rules.\n</user_claude_md>"},
		},
	})
	if text != "真正的问题" {
		t.Fatalf("got %q", text)
	}
}

func TestNormalizeMonitoredUserPromptSessionTag(t *testing.T) {
	t.Parallel()
	text := normalizeMonitoredUserPrompt("<session> 帮我连网检索三篇自进化的文献 </session> Write the title in English.")
	if text != "帮我连网检索三篇自进化的文献" {
		t.Fatalf("unexpected session extraction %q", text)
	}
}
