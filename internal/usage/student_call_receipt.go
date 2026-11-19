package usage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type StudentCallReceipt struct {
	RequestID        string  `json:"request_id"`
	ModelID          string  `json:"model_id"`
	Channel          string  `json:"channel"`
	BillingState     string  `json:"billing_state"`
	BaseCredits      *string `json:"base_credits"`
	BaseSource       *string `json:"base_source"`
	SchoolMultiplier *string `json:"school_multiplier"`
	ChargedCredits   *string `json:"charged_credits"`
	ActualCost       *string `json:"actual_cost"`
	ReceiptApplied   *bool   `json:"receipt_applied"`
}

// A caller can reuse X-Request-ID. Refuse ambiguous lookups within the student's
// own calls, including collisions with calls using other providers.
const studentCallReceiptSQL = `
	WITH candidates AS MATERIALIZED (
		SELECT c.id, c.request_id, c.model_id, c.beichen_dispatch_kind,
		       c.beichen_state,c.beichen_credit_exact,c.beichen_base_credits,c.beichen_base_source,c.beichen_school_multiplier,
		       c.beichen_receipt_response_id,c.beichen_receipt_conflict
		FROM gateway_calls c
		JOIN api_keys k ON k.id = c.api_key_id AND k.owner_user_id = c.user_id
		WHERE c.user_id = $1 AND c.request_id = $2
		LIMIT 2
	)
	SELECT c.request_id, m.model_id,
	       CASE c.beichen_state WHEN 'reserved' THEN 'pending' ELSE c.beichen_state END,
	       trim_scale(c.beichen_credit_exact)::text, trim_scale(r.actual_cost)::text, r.applied,
	       trim_scale(coalesce(c.beichen_base_credits,c.beichen_credit_exact))::text,
	       coalesce(c.beichen_base_source,CASE WHEN c.beichen_state='charged' THEN 'legacy' END),
	       coalesce(c.beichen_school_multiplier,1)::text
	FROM candidates c
	JOIN models m ON m.id = c.model_id
	LEFT JOIN beichen_receipt_response_claims claim ON claim.response_id=c.beichen_receipt_response_id
	  AND claim.first_call_id=c.id AND claim.conflicted_at IS NULL AND NOT c.beichen_receipt_conflict
	LEFT JOIN beichen_call_receipts r ON r.call_id = c.id AND r.response_id=claim.response_id
	WHERE c.beichen_dispatch_kind = 'beichen' AND c.beichen_state IS NOT NULL
	  AND (SELECT count(*) FROM candidates) = 1`

func (s *Store) StudentCallReceipt(ctx context.Context, userID uuid.UUID, requestID string) (StudentCallReceipt, error) {
	notFound := domain.NewError(domain.CodeNotFound, "call receipt not found")
	// The gateway accepts caller-supplied IDs up to 128 bytes and otherwise
	// generates a UUIDv7. The lookup takes one decoded URL path segment.
	if requestID == "" || len(requestID) > 128 || !utf8.ValidString(requestID) ||
		strings.TrimSpace(requestID) != requestID || strings.ContainsRune(requestID, '/') ||
		strings.ContainsFunc(requestID, unicode.IsControl) {
		return StudentCallReceipt{}, notFound
	}

	receipt := StudentCallReceipt{Channel: "北辰"}
	err := s.db.QueryRowContext(ctx, studentCallReceiptSQL, userID, requestID).Scan(
		&receipt.RequestID, &receipt.ModelID, &receipt.BillingState,
		&receipt.ChargedCredits, &receipt.ActualCost, &receipt.ReceiptApplied,
		&receipt.BaseCredits, &receipt.BaseSource, &receipt.SchoolMultiplier,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return StudentCallReceipt{}, notFound
	}
	if err != nil {
		return StudentCallReceipt{}, domain.WrapError(domain.CodeDependencyUnavailable, "query call receipt", err)
	}
	return receipt, nil
}
