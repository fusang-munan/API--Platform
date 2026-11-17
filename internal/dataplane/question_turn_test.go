package dataplane

import (
	"testing"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestQuestionStateToolResultStaysOpen(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "找三篇论文"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "WebSearch", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"},
			}},
		},
	})
	if state != usage.QuestionContinue {
		t.Fatalf("tool result opened a new question: %s", state)
	}
}

func TestQuestionStateAfterFinishedReplyIsNew(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "找三篇论文"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "这三篇是……"},
			}},
			map[string]any{"role": "user", "content": "再找 training free 的"},
		},
	})
	if state != usage.QuestionNew {
		t.Fatalf("finished reply did not start a question: %s", state)
	}
}

func TestQuestionStatePendingToolIgnoresTrailingText(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "等四张卡空出来"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "再换一个问题"},
			}},
		},
	})
	if state != usage.QuestionContinue {
		t.Fatalf("unanswered tool split the question: %s", state)
	}
}

func TestQuestionStateFirstMessageIsUnknown(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "你好"}},
	})
	if state != usage.QuestionUnknown {
		t.Fatalf("first message state = %s", state)
	}
}

func TestQuestionStateCodexToolOutputStaysOpen(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "跑一下实验"},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
		},
	})
	if state != usage.QuestionContinue {
		t.Fatalf("codex tool output state = %s", state)
	}
}

func TestQuestionStateCodexFollowUpIsNew(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "跑一下实验"},
			map[string]any{"type": "message", "role": "assistant", "content": "跑完了"},
			map[string]any{"type": "message", "role": "user", "content": "把图换成柱状"},
		},
	})
	if state != usage.QuestionNew {
		t.Fatalf("codex follow-up state = %s", state)
	}
}

func TestQuestionStateCodexDeltaUserMessageIsUnknown(t *testing.T) {
	state := questionStateFromPayload(map[string]any{
		"previous_response_id": "resp_prev",
		"input":                []any{map[string]any{"type": "message", "role": "user", "content": "下一问"}},
	})
	if state != usage.QuestionUnknown {
		t.Fatalf("codex delta state = %s", state)
	}
}

func TestBindQuestionTurnDropsCommandsAndOpenTools(t *testing.T) {
	prompt, correlation := bindQuestionTurn("/model", CallCorrelation{TaskKey: "cc:session-1", QuestionState: usage.QuestionNew})
	if prompt != "" || correlation.QuestionState != usage.QuestionContinue || correlation.TaskKey != "cc:session-1" {
		t.Fatalf("command prompt=%q state=%s key=%s", prompt, correlation.QuestionState, correlation.TaskKey)
	}
	kept, opened := bindQuestionTurn("下一问", CallCorrelation{TaskKey: "cc:session-1", QuestionState: usage.QuestionNew})
	if kept != "下一问" || opened.QuestionState != usage.QuestionNew || opened.TaskKey == "cc:session-1" {
		t.Fatalf("new question prompt=%q state=%s key=%s", kept, opened.QuestionState, opened.TaskKey)
	}
}
