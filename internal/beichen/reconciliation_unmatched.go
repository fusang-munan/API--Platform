package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
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
	LocalStatus    *string   `json:"local_status"`
	LocalSource    *string   `json:"local_source"`
	CallTokenParts
	ownerID int64
	rowID   int64
}

type UnmatchedUpstreamPage struct {
	Rows       []UnmatchedUpstreamRow
	Total      int64
	NextCursor *string
}

const unmatchedUpstreamPageSize = 50

// The tuple is unique per upstream account and remains stable when two rows
// share a timestamp. The cursor is bound to the provider, report boundary and
// date scope so it cannot silently page a different query.
type unmatchedUpstreamCursor struct {
	Provider uuid.UUID  `json:"provider"`
	Scope    string     `json:"scope"`
	Filter   string     `json:"filter"`
	Start    *time.Time `json:"start"`
	End      time.Time  `json:"end"`
	Time     time.Time  `json:"at"`
	Row      int64      `json:"row"`
	Owner    int64      `json:"owner"`
}

func unmatchedUpstreamPosition(raw string, provider uuid.UUID, scope string, start *time.Time, end time.Time,
	filter string) (unmatchedUpstreamCursor, error) {
	pos := unmatchedUpstreamCursor{
		Provider: provider, Scope: scope, Filter: filter, Start: start, End: end,
		Time: end, Row: math.MaxInt64, Owner: math.MaxInt64,
	}
	if raw == "" {
		return pos, nil
	}
	var decoded unmatchedUpstreamCursor
	if len(raw) <= 512 {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err == nil && json.Unmarshal(data, &decoded) == nil && decoded.Provider == provider &&
			decoded.Scope == scope && decoded.Filter == filter && sameReportStart(decoded.Start, start) && decoded.End.Equal(end) &&
			decoded.Row > 0 && decoded.Owner > 0 && !decoded.Time.After(end) &&
			(start == nil || !decoded.Time.Before(*start)) {
			return decoded, nil
		}
	}
	return unmatchedUpstreamCursor{}, invalid("未关联流水分页位置无效，请重新查询当前账日口径")
}

func sameReportStart(first, second *time.Time) bool {
	return first == nil && second == nil || first != nil && second != nil && first.Equal(*second)
}

func unmatchedDateFilterKey(dates []string) string {
	sum := sha256.Sum256([]byte(strings.Join(dates, "\n")))
	return hex.EncodeToString(sum[:])
}

func encodeUnmatchedUpstreamPosition(pos unmatchedUpstreamCursor) (string, error) {
	data, err := json.Marshal(pos)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func readUnmatchedUpstream(ctx context.Context, tx *sql.Tx, provider uuid.UUID, start *time.Time, end time.Time,
	dates []string, scope, cursor string) (UnmatchedUpstreamPage, error) {
	if scope == unmatchedScopeComparable && len(dates) == 0 {
		return UnmatchedUpstreamPage{Rows: []UnmatchedUpstreamRow{}}, nil
	}
	filter := unmatchedDateFilterKey(dates)
	pos, err := unmatchedUpstreamPosition(cursor, provider, scope, start, end, filter)
	if err != nil {
		return UnmatchedUpstreamPage{}, err
	}
	var dateFilter any
	if scope == unmatchedScopeComparable {
		dateFilter = dates
	}
	page := UnmatchedUpstreamPage{Rows: []UnmatchedUpstreamRow{}}
	if err := tx.QueryRowContext(ctx, `
SELECT count(*)`+archivedMatchesSQL+`
 WHERE u.provider_id=$1 AND ($2::timestamptz IS NULL OR u.occurred_at >= $2)
   AND u.occurred_at < $3 AND c.id IS NULL
   AND ($4::text[] IS NULL OR (u.occurred_at AT TIME ZONE 'Asia/Shanghai')::date = ANY($4::text[]::date[]))`,
		provider, start, end, dateFilter).Scan(&page.Total); err != nil {
		return UnmatchedUpstreamPage{}, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT u.occurred_at,u.upstream_user_id,u.row_id,u.model,u.credential_id,
       COALESCE(NULLIF(pc.label,''),u.channel),u.request_id,r.response_id::text,
       CASE WHEN direct_call.id IS NOT NULL THEN direct_call.beichen_state ELSE lc.beichen_state END,
       CASE WHEN direct_call.id IS NOT NULL OR claim.response_id IS NOT NULL THEN 'unique_claim'
            WHEN r.response_id IS NOT NULL THEN 'receipt_without_unique_claim' END,
       u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_write_tokens`+archivedMatchesSQL+`
 LEFT JOIN gateway_calls lc ON lc.id=claim.first_call_id
 LEFT JOIN beichen_receipt_response_claims direct_claim ON claim.response_id IS NULL
   AND direct_claim.conflicted_at IS NULL AND direct_claim.response_id=CASE
     WHEN u.request_id ~ '^client:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
     THEN substr(u.request_id,8)::uuid END
 LEFT JOIN gateway_calls direct_call ON direct_call.id=direct_claim.first_call_id
   AND direct_call.beichen_dispatch_credential_id=u.credential_id
   AND direct_call.beichen_receipt_response_id=direct_claim.response_id
 LEFT JOIN provider_credentials pc ON pc.id=u.credential_id AND pc.provider_id=u.provider_id
 WHERE u.provider_id=$1 AND ($2::timestamptz IS NULL OR u.occurred_at >= $2)
   AND u.occurred_at < $3 AND c.id IS NULL
   AND ($4::text[] IS NULL OR (u.occurred_at AT TIME ZONE 'Asia/Shanghai')::date = ANY($4::text[]::date[]))
   AND (u.occurred_at,u.row_id,u.upstream_user_id) < ($5,$6,$7)
 ORDER BY u.occurred_at DESC,u.row_id DESC,u.upstream_user_id DESC LIMIT $8`,
		provider, start, end, dateFilter, pos.Time, pos.Row, pos.Owner, unmatchedUpstreamPageSize+1)
	if err != nil {
		return UnmatchedUpstreamPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row UnmatchedUpstreamRow
		if err := rows.Scan(&row.OccurredAt, &row.ownerID, &row.rowID, &row.Model, &row.CredentialID,
			&row.Label, &row.RequestID, &row.ReceiptID, &row.LocalStatus, &row.LocalSource, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite); err != nil {
			return UnmatchedUpstreamPage{}, err
		}
		row.OccurredAt = row.OccurredAt.In(beijing)
		row.UpstreamUserID = strconv.FormatInt(row.ownerID, 10)
		row.RowID = strconv.FormatInt(row.rowID, 10)
		page.Rows = append(page.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return UnmatchedUpstreamPage{}, err
	}
	if len(page.Rows) > unmatchedUpstreamPageSize {
		last := page.Rows[unmatchedUpstreamPageSize-1]
		next, err := encodeUnmatchedUpstreamPosition(unmatchedUpstreamCursor{
			Provider: provider, Scope: scope, Filter: filter, Start: start, End: end,
			Time: last.OccurredAt.UTC(), Row: last.rowID, Owner: last.ownerID,
		})
		if err != nil {
			return UnmatchedUpstreamPage{}, err
		}
		page.NextCursor = &next
		page.Rows = page.Rows[:unmatchedUpstreamPageSize]
	}
	return page, nil
}
