package dataplane

import (
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func toUsageCorrelation(correlation CallCorrelation) usage.CallCorrelation {
	result := usage.CallCorrelation{
		TaskKey:            correlation.TaskKey,
		PreviousResponseID: correlation.PreviousResponseID,
	}
	if id, err := uuid.Parse(correlation.AgentTurnHeader); err == nil {
		result.ExplicitTurnID = id
	}
	return result
}

func toUsageMetrics(metrics ResponseMetrics) usage.CallMetrics {
	return usage.CallMetrics{
		UpstreamResponseID: metrics.UpstreamResponseID,
		PromptTokens:       metrics.PromptTokens,
		CompletionTokens:   metrics.CompletionTokens,
		TotalTokens:        metrics.TotalTokens,
	}
}
