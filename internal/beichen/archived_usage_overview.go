package beichen

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// ArchivedUsage groups only rows actually seen on the upstream website. A
// provider correction changes this comparison without editing a student ledger.
type ArchivedUsage struct {
	Model                 string    `json:"model"`
	CredentialID          uuid.UUID `json:"credential_id"`
	Rows                  int64     `json:"rows"`
	UnmatchedRows         int64     `json:"unmatched_rows"`
	UnpricedRows          int64     `json:"unpriced_rows"`
	ObservedCost          *string   `json:"observed_cost"`
	MatchedCost           *string   `json:"matched_cost"`
	MatchedBaseCredits    *string   `json:"matched_base_credits"`
	MatchedChargedCredits *string   `json:"matched_charged_credits"`
	MarginCredits         *string   `json:"margin_credits"`
}

func readArchivedUsage(ctx context.Context, tx *sql.Tx, provider uuid.UUID, start time.Time) ([]ArchivedUsage, error) {
	rows, err := tx.QueryContext(ctx, `
WITH observed AS (
 SELECT u.model,u.credential_id,
   CASE WHEN u.billing_type=0 AND u.subscription_id IS NULL AND coalesce(u.currency,'USD')='USD'
        THEN u.actual_cost END AS cost,
   c.id AS call_id,coalesce(c.beichen_base_credits,c.beichen_credit_exact) AS base,
   c.beichen_credit_exact AS charged
 FROM beichen_upstream_usage_rows u
 LEFT JOIN beichen_call_receipts r ON r.row_id=u.row_id AND r.credential_id=u.credential_id
   AND r.api_key_id=u.upstream_key_id AND u.request_id='client:'||r.response_id::text
   AND r.model=u.model AND r.occurred_at=u.occurred_at
 LEFT JOIN beichen_receipt_response_claims claim ON claim.response_id=r.response_id
   AND claim.first_call_id=r.call_id AND claim.conflicted_at IS NULL
 LEFT JOIN gateway_calls c ON c.id=claim.first_call_id AND c.beichen_receipt_response_id=claim.response_id
   AND NOT c.beichen_receipt_conflict AND c.beichen_state='charged'
 WHERE u.provider_id=$1 AND u.occurred_at >= $2 AND u.occurred_at < $3
)
SELECT model,credential_id,count(*),count(*) FILTER (WHERE call_id IS NULL),count(*)-count(cost),
  trim_scale(sum(cost))::text,
  trim_scale(sum(cost) FILTER (WHERE call_id IS NOT NULL))::text,
  trim_scale(sum(base) FILTER (WHERE cost IS NOT NULL AND call_id IS NOT NULL))::text,
  trim_scale(sum(charged) FILTER (WHERE cost IS NOT NULL AND call_id IS NOT NULL))::text,
  trim_scale(sum(charged-cost*100) FILTER (WHERE cost IS NOT NULL AND call_id IS NOT NULL))::text
FROM observed GROUP BY credential_id,model ORDER BY model,credential_id`, provider, start, start.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchivedUsage{}
	for rows.Next() {
		var value ArchivedUsage
		err := rows.Scan(&value.Model, &value.CredentialID, &value.Rows, &value.UnmatchedRows, &value.UnpricedRows,
			&value.ObservedCost, &value.MatchedCost, &value.MatchedBaseCredits, &value.MatchedChargedCredits, &value.MarginCredits)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
