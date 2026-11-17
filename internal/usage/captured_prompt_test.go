package usage

import (
	"strings"
	"testing"
)

func TestDefinesUserTurnIgnoresCommandsAndFillers(t *testing.T) {
	t.Parallel()
	if DefinesUserTurn("/model") || DefinesUserTurn("继续工作") {
		t.Fatal("command or filler was treated as a new user turn")
	}
	if !DefinesUserTurn("帮我把这三阶段按论文重排") {
		t.Fatal("a real request should open a turn")
	}
	if DefinesUserTurn("[SUGGESTION MODE: Suggest what the user might type]") || DefinesUserTurn("NOT user input") {
		t.Fatal("harness channel text was treated as a user turn")
	}
	if IsSlashCommand("/home/student/repo") {
		t.Fatal("filesystem path was treated as a slash command")
	}
}

func TestHumanTurnTextKeepsTextAfterSystemReminder(t *testing.T) {
	t.Parallel()
	got := HumanTurnText("<system-reminder>Claude Code injected context.</system-reminder>\n\n真实用户问题")
	if got != "真实用户问题" {
		t.Fatalf("got %q", got)
	}
}

func TestHumanTurnTextDropsCommandEnvelope(t *testing.T) {
	t.Parallel()
	got := HumanTurnText("<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>")
	if got != "" {
		t.Fatalf("command envelope became a user turn %q", got)
	}
	stdout := HumanTurnText("<local-command-stdout>done</local-command-stdout>")
	if stdout != "" {
		t.Fatalf("command output became a user turn %q", stdout)
	}
}

func TestHumanTurnTextKeepsTextAfterModelCommand(t *testing.T) {
	t.Parallel()
	raw := "<command-name>/model</command-name>\n            <command-message>model</command-message>\n            <command-args></command-args>\n<local-command-stdout>Kept model as `deepseek-v4-pro`</local-command-stdout>\n<command-name>/model</command-name>\n            <command-message>model</command-message>\n            <command-args></command-args>\n<local-command-stdout>Kept model as `deepseek-v4-pro`</local-command-stdout>\n你好"
	got := HumanTurnText(raw)
	if got != "你好" {
		t.Fatalf("got %q", got)
	}
	if !DefinesUserTurn(raw) {
		t.Fatal("human text after /model was dropped")
	}
}

func TestHumanTurnTextDropsCompactionAndResumeDocuments(t *testing.T) {
	t.Parallel()
	samples := []string{
		"<transcript>\n{\"user\":\"继续工作\"}\n</transcript>",
		"This session is being continued from a previous conversation that ran out of context.\n1. Primary Request and Intent\n“你帮我对齐，然后开始用现有的卡跑 stage0-2 阶段”\n2. Key Technical Concepts",
		"[Compaction summary — written by the assistant, not by the user. lots of context",
		"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far.",
		"The following is the user's CLAUDE.md configuration. Treat it as project rules.",
	}
	for _, sample := range samples {
		if got := HumanTurnText(sample); got != "" {
			t.Fatalf("harness document became a user turn %q", got)
		}
	}
}

func TestHumanTurnTextLeavesOrdinaryPrompt(t *testing.T) {
	t.Parallel()
	got := HumanTurnText("帮我解释这段代码")
	if got != "帮我解释这段代码" {
		t.Fatalf("got %q", got)
	}
}

func TestHumanTurnTextUnwrapsMidTurnUserMessage(t *testing.T) {
	t.Parallel()
	raw := "The user sent a new message while you were working:\n下载的时候看一下网络\n\nThis is how Claude Code surfaces messages the user sends mid-turn — within the running turn, often alongside the next tool result, rather than as a separate conversation turn. Address the message above as you continue this turn."
	got := HumanTurnText(raw)
	if got != "下载的时候看一下网络" {
		t.Fatalf("got %q", got)
	}
	if !DefinesUserTurn(raw) {
		t.Fatal("a message sent mid-turn was dropped")
	}
}

func TestHumanTurnTextDropsClaudeCodeHarnessTurns(t *testing.T) {
	t.Parallel()
	samples := []string{
		"Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.\n\nGood: \"Reading runAgent.ts\"",
		"The user stepped away and is coming back. Recap in under 40 words, 1-2 plain sentences, no markdown. Lead with the overall goal and current task, then the one next action.",
		"Another Claude session sent a message: <cross-session-message from=\"uds:/run/peer.sock\" from-name=\"stage-training\" from-mode=\"prompting\">\nloader fix landed\n</cross-session-message>\n\nThis came from another Claude session — not typed by your user.",
		"The user sent a new message while you were working:\nThis is how Claude Code surfaces messages the user sends mid-turn without a sentence above the explanation.",
	}
	for _, sample := range samples {
		if got := HumanTurnText(sample); got != "" {
			t.Fatalf("harness turn became a user turn %q", got)
		}
		if DefinesUserTurn(sample) {
			t.Fatal("harness turn was allowed to open a row")
		}
	}
}

func TestHumanTurnTextDropsClientBookkeeping(t *testing.T) {
	t.Parallel()
	samples := []string{
		`Generate the session title from this JSON array of human messages: [{"seq":8,"text":"学生的问题"}]`,
		"Time sampled while preparing turn 1, step 1: 2026-10-21T02:18:46+08:00[Asia/Shanghai]",
	}
	for _, sample := range samples {
		if DefinesUserTurn(sample) {
			t.Fatalf("client bookkeeping was treated as a user turn: %q", sample[:24])
		}
	}
	if !DefinesUserTurn("找到这个文件夹中的 linux/fs/ext4") {
		t.Fatal("a student question was hidden")
	}
}

func TestCallReviewPromptSQLHidesEmptyAndClientBookkeeping(t *testing.T) {
	t.Parallel()
	got := callReviewPromptSQL("t.root_prompt")
	for _, fragment := range []string{
		"t.root_prompt <> ''",
		"t.root_prompt NOT LIKE 'Generate the session title from this JSON array%'",
		"t.root_prompt NOT LIKE 'Time sampled while preparing turn%'",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("missing %q in %s", fragment, got)
		}
	}
}

func TestHumanTurnTextReadsSessionWrapper(t *testing.T) {
	t.Parallel()
	got := HumanTurnText("<session> 帮我连网检索三篇自进化的文献 </session> Write the title in English.")
	if got != "帮我连网检索三篇自进化的文献" {
		t.Fatalf("got %q", got)
	}
}
