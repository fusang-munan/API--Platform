package usage

import "strings"

// DefinesUserTurn reports whether this text is a new human request.
// Slash commands, continuation fillers, and harness-authored messages stay on
// the current session turn instead of opening another review row.
func DefinesUserTurn(prompt string) bool {
	human := HumanTurnText(prompt)
	return human != "" && !IsSlashCommand(human) && !isLowValueContinuation(human)
}

// IsSlashCommand matches a single client command such as /model.
// Paths like /home/student/project contain another slash and are not commands.
func IsSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return false
	}
	body := text[1:]
	if body == "" || len(body) > 32 || strings.ContainsAny(body, " \n\t/\\") {
		return false
	}
	return true
}

// HumanTurnText returns the human sentence left after a harness envelope is removed.
// Tool results, command envelopes, compaction documents, and resume transcripts
// are not human turns: the caller keeps the title already stored for the thread.
func HumanTurnText(text string) string {
	text = stripHarnessEnvelopes(strings.TrimSpace(text))
	text = unwrapMidTurnUserMessage(text)
	if text == "" || isHarnessDocument(text) {
		return ""
	}
	if strings.HasPrefix(text, "<session>") {
		start := strings.Index(text, ">")
		if start < 0 {
			return ""
		}
		rest := text[start+1:]
		end := strings.Index(rest, "</session>")
		if end < 0 {
			return ""
		}
		inner := strings.TrimSpace(rest[:end])
		if inner == "" || isHarnessDocument(inner) {
			return ""
		}
		return inner
	}
	return text
}

func stripHarnessEnvelopes(text string) string {
	tags := []string{"system-reminder", "command-name", "command-message", "command-args"}
	for {
		next := text
		for _, tag := range tags {
			next = removeClosedElements(next, tag)
		}
		next = removeLocalCommandElements(next)
		next = strings.TrimSpace(next)
		if next == text {
			return text
		}
		text = next
	}
}

func removeClosedElements(text, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	for {
		start := strings.Index(text, open)
		if start < 0 {
			return text
		}
		rest := text[start+len(open):]
		end := strings.Index(rest, close)
		if end < 0 {
			return text
		}
		text = text[:start] + rest[end+len(close):]
	}
}

func removeLocalCommandElements(text string) string {
	const openPrefix = "<local-command-"
	for {
		start := strings.Index(text, openPrefix)
		if start < 0 {
			return text
		}
		rel := strings.Index(text[start:], ">")
		if rel < 0 {
			return text
		}
		name := text[start+1 : start+rel]
		if name == "" || strings.ContainsAny(name, " \n\t/\"") {
			return text
		}
		close := "</" + name + ">"
		rest := text[start+rel+1:]
		end := strings.Index(rest, close)
		if end < 0 {
			return text
		}
		text = text[:start] + rest[end+len(close):]
	}
}

// unwrapMidTurnUserMessage keeps the sentence a person sent while a turn was
// already running. Claude Code wraps that sentence in an explanation of how
// the injection is delivered; the explanation is not the question.
func unwrapMidTurnUserMessage(text string) string {
	const lead = "The user sent a new message while you were working:"
	const tail = "This is how Claude Code surfaces messages the user sends mid-turn"
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, lead) {
		return text
	}
	rest := strings.TrimSpace(trimmed[len(lead):])
	end := strings.Index(rest, tail)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func isHarnessDocument(text string) bool {
	text = strings.TrimSpace(text)
	return text != "" && (looksLikeAgentTranscript(text) || isStoredAgentNoise(text))
}

func isLowValueContinuation(text string) bool {
	trimmed := strings.Trim(strings.TrimSpace(text), `"'“”`)
	switch strings.ToLower(trimmed) {
	case "继续工作", "继续", "continue", "continue working", "i'll continue", "go on", "ok", "yes", "好的", "好":
		return true
	default:
		return false
	}
}

func looksLikeAgentTranscript(text string) bool {
	return strings.Contains(text, "<transcript>") ||
		strings.Contains(text, "<cross-session-message") ||
		strings.Contains(text, "[Compaction summary") ||
		strings.Contains(text, "This session is being continued from a previous conversation") ||
		strings.Contains(text, "[SYSTEM NOTIFICATION - NOT USER INPUT]")
}
