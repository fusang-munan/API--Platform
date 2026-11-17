package usage

import "strings"

func normalizeStoredUserPrompt(content string) string {
	return HumanTurnText(content)
}

// clientSidePromptPrefixes are requests a coding client writes for itself.
// They are not the student's question, including when a copy was already stored.
var clientSidePromptPrefixes = []string{
	"Generate the session title from this JSON array",
	"Time sampled while preparing turn",
}

// callReviewPromptSQL keeps a turn in 调用审查 only when its stored title is a
// student question. Empty titles and client bookkeeping stay in the call log
// but do not become review rows. column is a fixed identifier from the query.
func callReviewPromptSQL(column string) string {
	var b strings.Builder
	b.WriteString(column)
	b.WriteString(" <> ''")
	for _, prefix := range clientSidePromptPrefixes {
		b.WriteString(" AND ")
		b.WriteString(column)
		b.WriteString(" NOT LIKE ")
		b.WriteString(sqlStringLiteral(prefix + "%"))
	}
	return b.String()
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func isStoredAgentNoise(text string) bool {
	for _, prefix := range clientSidePromptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return strings.HasPrefix(text, "<system-reminder>") ||
		strings.HasPrefix(text, "<local-command-") ||
		strings.HasPrefix(text, "<command-message>") ||
		strings.HasPrefix(text, "Perform a web search for the query:") ||
		strings.HasPrefix(text, "You are Codex,") ||
		strings.HasPrefix(text, "You are a coding agent") ||
		strings.HasPrefix(text, "CRITICAL: Respond with TEXT ONLY") ||
		strings.HasPrefix(text, "Your task is to create a detailed summary of the conversation so far") ||
		strings.HasPrefix(text, "The following is the user's CLAUDE.md") ||
		strings.HasPrefix(text, "[SUGGESTION MODE:") ||
		strings.HasPrefix(text, "[SYSTEM NOTIFICATION") ||
		strings.HasPrefix(text, "Describe your most recent action in 3-5 words") ||
		strings.HasPrefix(text, "The user stepped away and is coming back.") ||
		strings.HasPrefix(text, "Another Claude session sent a message:") ||
		strings.HasPrefix(text, "The user sent a new message while you were working:") ||
		strings.EqualFold(text, "NOT user input")
}

func scoreStoredPrompt(content, source string) int {
	text := normalizeStoredUserPrompt(content)
	if text == "" {
		return -1
	}
	score := 1000 - len([]rune(text))/4
	switch source {
	case "input_text", "user_message":
		score += 500
	case "input":
		score += 200
	case "instructions":
		return -1
	case "prompt":
		score += 100
	default:
		score += 50
	}
	if len(text) <= 512 {
		score += 100
	}
	return score
}
