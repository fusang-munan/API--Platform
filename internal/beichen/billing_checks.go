package beichen

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type BillingCheck struct {
	CredentialID    uuid.UUID `json:"credential_id"`
	Model           string    `json:"model"`
	Day             string    `json:"day"`
	LocalCredits    *string   `json:"local_credits"`
	UpstreamCredits *string   `json:"upstream_credits"`
	DiffCredits     *string   `json:"diff_credits"`
	Status          string    `json:"status"`
}

func readBillingChecks(ctx context.Context, tx *sql.Tx, provider uuid.UUID, start, end time.Time) ([]BillingCheck, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT credential_id, model, to_char(day, 'YYYY-MM-DD'),
  trim_scale(local_credits)::text, trim_scale(upstream_credits)::text, trim_scale(diff_credits)::text, status
FROM billing_checks WHERE provider_id=$1 AND day >= $2::text::date AND day < $3::text::date
ORDER BY day DESC, credential_id, model`, provider, start.Format(dayLayout), end.Format(dayLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := []BillingCheck{}
	for rows.Next() {
		var check BillingCheck
		if err := rows.Scan(&check.CredentialID, &check.Model, &check.Day,
			&check.LocalCredits, &check.UpstreamCredits, &check.DiffCredits, &check.Status); err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}
