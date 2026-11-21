package usage

import (
	"time"

	"github.com/google/uuid"
)

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
	PromptTokens       *int64
	CompletionTokens   *int64
	TotalTokens        *int64
	CacheHitTokens     *int64
	CacheMissTokens    *int64
	CacheWriteTokens   *int64
	ReasoningTokens    *int64
	ToolPromptTokens   *int64
	// UpstreamCreditMicros is the explicit upstream credit in millionths.
	UpstreamCreditMicros *int64
	State                UsageState
	UsageReason          string
	// ReplyClosed is set when the upstream stop flag is known.
	// False means the model asked for a tool and the question is still open.
	ReplyClosed *bool
}

func (m CallMetrics) HasUsage() bool {
	return m.HasTokenUsage() || m.UpstreamCreditMicros != nil
}

func (m CallMetrics) HasTokenUsage() bool {
	return m.PromptTokens != nil || m.CompletionTokens != nil || m.TotalTokens != nil ||
		m.CacheHitTokens != nil || m.CacheMissTokens != nil || m.CacheWriteTokens != nil ||
		m.ReasoningTokens != nil || m.ToolPromptTokens != nil
}

type UsageState string

const DefaultReasoningEffort = "medium"

const (
	UsageReported    UsageState = "reported"
	UsagePartial     UsageState = "partial"
	UsageMissing     UsageState = "missing"
	UsageInterrupted UsageState = "interrupted"
)

type CallRoute struct {
	Beichen           bool
	DefinitelyNotSent bool
	ProviderID        uuid.UUID
	ProviderName      string
	CredentialID      uuid.UUID
	BindingID         uuid.UUID
	UpstreamModelName string
	// ReasoningEffort is the observed level or the default for an omitted level.
	// A nil value means the gateway could not observe the request.
	ReasoningEffort *string
	// CacheTTL is the verified single duration for cache writes. Empty means unknown.
	CacheTTL string
	// SentAt is the observed upstream write time of this HTTP attempt.
	SentAt time.Time
}
