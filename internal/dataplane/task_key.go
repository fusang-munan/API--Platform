package dataplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

// userTurnTaskKey identifies one user prompt within a client session (one mentor review row).
func userTurnTaskKey(sessionScope, userPrompt string) string {
	prompt := strings.TrimSpace(userPrompt)
	sessionScope = normalizeSessionScope(sessionScope)
	if prompt == "" {
		return sessionScope
	}
	digest := sha256.Sum256([]byte(prompt))
	short := hex.EncodeToString(digest[:8])
	if sessionScope != "" {
		return sessionScope + ":" + short
	}
	return "p:" + short
}

func enrichCallCorrelationForUserTurn(correlation CallCorrelation, userPrompt string) CallCorrelation {
	scope := normalizeSessionScope(correlation.TaskKey)
	if usage.DefinesUserTurn(userPrompt) {
		correlation.TaskKey = userTurnTaskKey(scope, userPrompt)
	} else {
		// Tool calls, /model, and resume transcripts stay on the open session turn.
		correlation.TaskKey = scope
	}
	return correlation
}

// bindQuestionTurn drops text that does not start a question. An open tool
// call, a slash command, or a continuation filler must not retitle the row or
// split it. The finish flag is applied later, when this request is resolved.
func bindQuestionTurn(prompt string, correlation CallCorrelation) (string, CallCorrelation) {
	if correlation.QuestionState == usage.QuestionContinue {
		prompt = ""
	}
	if !usage.DefinesUserTurn(prompt) {
		prompt = ""
		correlation.QuestionState = usage.QuestionContinue
	}
	return prompt, enrichCallCorrelationForUserTurn(correlation, prompt)
}

// normalizeSessionScope collapses Claude Code metadata.user_id JSON to a stable session id.
func normalizeSessionScope(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "{") {
		var meta map[string]any
		if json.Unmarshal([]byte(raw), &meta) == nil {
			if sessionID := stringField(meta, "session_id"); sessionID != "" {
				return "cc:" + sessionID
			}
			if deviceID := stringField(meta, "device_id"); deviceID != "" {
				return "cc:" + deviceID
			}
		}
	}
	return raw
}
