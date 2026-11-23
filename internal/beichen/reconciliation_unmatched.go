package beichen

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// A row without a verified local debit can still have a pending gateway call.
type UnmatchedUpstreamRow struct {
	OccurredAt     time.Time `json:"occurred_at"`
	UpstreamUserID string    `json:"upstream_user_id"`
	RowID          string    `json:"row_id"`
	Model          string    `json:"model"`
	CredentialID   uuid.UUID `json:"credential_id"`
	Label          string    `json:"label"`
	RequestID      *string   `json:"request_id"`
	ReceiptID      *string   `json:"receipt_id"`
	CallTokenParts
}

func readUnmatchedUpstream(ctx context.Context, tx *sql.Tx, provider uuid.UUID, start *time.Time, end time.Time) ([]UnmatchedUpstreamRow, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT u.occurred_at,u.upstream_user_id::text,u.row_id::text,u.model,u.credential_id,
       COALESCE(NULLIF(pc.label,''),u.channel),u.request_id,r.response_id::text,
       u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_write_tokens`+archivedMatchesSQL+`
 LEFT JOIN provider_credentials pc ON pc.id=u.credential_id AND pc.provider_id=u.provider_id
 WHERE u.provider_id=$1 AND ($2::timestamptz IS NULL OR u.occurred_at >= $2)
   AND u.occurred_at < $3 AND c.id IS NULL
 ORDER BY u.occurred_at DESC,u.row_id DESC,u.upstream_user_id DESC`, provider, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnmatchedUpstreamRow{}
	for rows.Next() {
		var row UnmatchedUpstreamRow
		if err := rows.Scan(&row.OccurredAt, &row.UpstreamUserID, &row.RowID, &row.Model, &row.CredentialID,
			&row.Label, &row.RequestID, &row.ReceiptID, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite); err != nil {
			return nil, err
		}
		row.OccurredAt = row.OccurredAt.In(beijing)
		out = append(out, row)
	}
	return out, rows.Err()
}
