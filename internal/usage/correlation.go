package usage

import "github.com/google/uuid"

const (
	// QuestionContinue is a tool result or harness replay of the open question.
	QuestionContinue = "continue"
	// QuestionNew is human text after a reply that did not ask for a tool.
	QuestionNew = "new"
	// QuestionUnknown has human text but no assistant reply in this body.
	// Resolution then uses the stored finish flag, or the idle gap.
	QuestionUnknown = "unknown"
)

type CallCorrelation struct {
	TaskKey            string
	PreviousResponseID string
	ExplicitTurnID     uuid.UUID
	ModelID            uuid.UUID
	QuestionState      string
}

type CallMetrics struct {
	UpstreamResponseID string
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	// ReplyClosed is set when the upstream stop flag is known.
	// False means the model asked for a tool and the question is still open.
	ReplyClosed *bool
}

func (m CallMetrics) HasTokens() bool {
	return m.PromptTokens > 0 || m.CompletionTokens > 0 || m.TotalTokens > 0
}
