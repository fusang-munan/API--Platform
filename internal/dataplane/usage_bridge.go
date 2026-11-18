package dataplane

import (
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func toUsageCorrelation(correlation CallCorrelation) usage.CallCorrelation {
	result := usage.CallCorrelation{
		TaskKey:            correlation.TaskKey,
		PreviousResponseID: correlation.PreviousResponseID,
		QuestionState:      correlation.QuestionState,
	}
	header := strings.TrimSpace(correlation.AgentTurnHeader)
	if id, err := uuid.Parse(header); err == nil {
		result.ExplicitTurnID = id
	} else if header != "" && result.TaskKey == "" {
		result.TaskKey = header
	}
	return result
}

// turnCorrelation applies the question boundary, then pins a websocket tool
// loop to the turn already open. A new question is not pinned, so the next
// reply can become its own row. activeTurn is uuid.Nil for ordinary HTTP.
func turnCorrelation(prompt string, correlation CallCorrelation, activeTurn uuid.UUID) (string, usage.CallCorrelation) {
	prompt, correlation = bindQuestionTurn(prompt, correlation)
	result := toUsageCorrelation(correlation)
	if result.QuestionState == usage.QuestionContinue && result.ExplicitTurnID == uuid.Nil && activeTurn != uuid.Nil {
		result.ExplicitTurnID = activeTurn
	}
	return prompt, result
}
