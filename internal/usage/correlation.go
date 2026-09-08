package usage

import "github.com/google/uuid"

type CallCorrelation struct {
	TaskKey            string
	PreviousResponseID string
	ExplicitTurnID     uuid.UUID
}

type CallMetrics struct {
	UpstreamResponseID string
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
}

func (m CallMetrics) HasTokens() bool {
	return m.PromptTokens > 0 || m.CompletionTokens > 0 || m.TotalTokens > 0
}
