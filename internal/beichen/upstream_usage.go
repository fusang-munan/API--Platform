package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Account discovery and the independent upstream ledger have their own time
// budget. A slow website cannot delay serving requests or matching local calls.
func (s *Service) runUpstreamUsage(ctx context.Context) {
	for ctx.Err() == nil {
		discovery, cancel := context.WithTimeout(ctx, receiptSweepTimeout)
		err := s.discoverChannels(discovery)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("北辰渠道身份待确认")
		}
		sweep, cancel := context.WithTimeout(ctx, receiptSweepTimeout)
		err = s.archiveUsage(sweep)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("北辰独立账单未采集完整，稍后续传")
		}
		if !waitReceiptInterval(ctx, receiptInterval) {
			return
		}
	}
}

type usageCheckpoint struct {
	credential           uuid.UUID
	channel              string
	key, owner, previous int64
	generation           int64
	revision             time.Time
	page                 int
	restart              bool
}

func (s *Service) archiveUsage(ctx context.Context) error {
	// New or rotated mappings start at page one. Refreshing a web session does
	// not rotate a database revision. A daily complete pass also catches repairs
	// or late insertions behind a previously acknowledged page boundary.
	rows, err := s.db.QueryContext(ctx, `SELECT b.credential_id,j.channel,b.upstream_key_id,
 COALESCE(j.upstream_user_id,cp.upstream_user_id,0),b.updated_at,
 COALESCE(cp.page,1),COALESCE(cp.previous_last_id,0),COALESCE(cp.generation,0),
 COALESCE(cp.caught_up AND cp.scan_started_at < clock_timestamp()-interval '1 day',false)
FROM beichen_receipt_active_bindings b JOIN beichen_provision_channels j ON j.credential_id=b.credential_id
LEFT JOIN beichen_usage_checkpoints cp ON cp.credential_id=b.credential_id AND cp.revision=b.updated_at AND cp.upstream_key_id=b.upstream_key_id
ORDER BY cp.last_success NULLS FIRST,b.credential_id`)
	if err != nil {
		return err
	}
	var jobs []usageCheckpoint
	for rows.Next() {
		var j usageCheckpoint
		if err := rows.Scan(&j.credential, &j.channel, &j.key, &j.owner, &j.revision, &j.page, &j.previous, &j.generation, &j.restart); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failed error
	for _, j := range jobs {
		jobCtx, cancel := context.WithTimeout(ctx, receiptKeyTimeout)
		err := s.archiveChannelUsage(jobCtx, j)
		cancel()
		if err != nil {
			failed = err
		}
	}
	return failed
}

func (s *Service) archiveChannelUsage(ctx context.Context, j usageCheckpoint) (err error) {
	defer func() {
		if err != nil {
			status, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_, _ = s.db.ExecContext(status, `UPDATE beichen_usage_checkpoints SET last_error='北辰账单读取未完成',caught_up=false
			 WHERE credential_id=$1 AND revision=$2 AND generation=$3`, j.credential, j.revision, j.generation)
		}
	}()
	_, err = s.db.ExecContext(ctx, `INSERT INTO beichen_usage_checkpoints(credential_id,upstream_key_id,revision)
SELECT credential_id,upstream_key_id,updated_at FROM beichen_receipt_active_bindings WHERE credential_id=$1 AND updated_at=$2
ON CONFLICT(credential_id) DO UPDATE SET upstream_key_id=excluded.upstream_key_id,revision=excluded.revision,
 upstream_user_id=NULL,page=1,previous_last_id=0,generation=0,last_success=NULL,last_error=NULL,caught_up=false,scan_started_at=clock_timestamp()
WHERE beichen_usage_checkpoints.revision < excluded.revision`, j.credential, j.revision)
	if err != nil {
		return err
	}
	token, key, revision, err := s.PanelUsageAccess(ctx, j.credential)
	if err != nil {
		return err
	}
	if token == "" || key != j.key || !revision.Equal(j.revision) {
		return nil
	}
	query := url.Values{"api_key_id": {strconv.FormatInt(key, 10)}, "start_date": {"1970-01-01"},
		"end_date": {s.now().In(beijing).Format(dayLayout)}, "timezone": {"Asia/Shanghai"},
		"page_size": {strconv.Itoa(receiptPageSize)}, "sort_by": {"id"}, "sort_order": {"asc"}}
	if !j.restart && j.page > 1 {
		prior, err := fetchReceiptPage(ctx, s.client, token, query, j.page-1)
		if err != nil && !errors.Is(err, errReceiptPagination) {
			return s.webReadError(token, err)
		}
		last := prior.Data.Items
		j.restart = err != nil || len(last) != receiptPageSize || last[len(last)-1].ID != j.previous
	}
	if j.restart {
		// CAS also protects a second backend which made progress during the fetch.
		res, err := s.db.ExecContext(ctx, `UPDATE beichen_usage_checkpoints SET page=1,previous_last_id=0,
 caught_up=false,generation=generation+1,scan_started_at=clock_timestamp() WHERE credential_id=$1 AND revision=$2 AND generation=$3`, j.credential, j.revision, j.generation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		j.page, j.previous = 1, 0
		j.generation++
	}
	for i := 0; i < receiptMaxPages; i++ {
		if i > 0 && !waitReceiptInterval(ctx, receiptPageInterval) {
			return ctx.Err()
		}
		data, err := fetchReceiptPage(ctx, s.client, token, query, j.page)
		if err != nil {
			return s.webReadError(token, err)
		}
		items := data.Data.Items
		last := j.previous
		for _, r := range items {
			if j.owner == 0 {
				j.owner = r.UserID
			}
			if r.ID <= last || r.KeyID != j.key || r.UserID <= 0 || r.UserID != j.owner || r.CreatedAt.IsZero() || !validModel(r.Model) {
				return errReceiptPagination
			}
			last = r.ID
		}
		tail := j.page == data.Data.Pages
		// Keep re-reading a partial final page so rows appended on the next poll
		// are never skipped. When the final page is full it is also a safe anchor.
		nextPage, nextBoundary := j.page+1, last
		if tail {
			nextPage, nextBoundary = j.page, j.previous
		}
		saved, err := s.saveUsagePage(ctx, j, items, nextPage, nextBoundary, tail)
		if err != nil || !saved || tail {
			return err
		}
		j.page, j.previous = nextPage, nextBoundary
		j.generation++
	}
	return nil
}

func (s *Service) saveUsagePage(ctx context.Context, j usageCheckpoint, items []receiptRow, next int, boundary int64, tail bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// The credential lock is shared with all panel writes. Never archive a
	// response fetched under an obsolete account, disabled key, or stale cursor.
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT c.id FROM beichen_usage_checkpoints cp JOIN provider_credentials c ON c.id=cp.credential_id
 JOIN providers p ON p.id=c.provider_id JOIN beichen_panel_access a ON a.credential_id=c.id
 WHERE c.id=$1 AND cp.revision=$2 AND cp.page=$3 AND cp.previous_last_id=$4 AND cp.upstream_key_id=$5
 AND (cp.upstream_user_id IS NULL OR cp.upstream_user_id=$6) AND a.upstream_key_id=$5 AND a.updated_at=$2 AND c.updated_at=$2
 AND cp.generation=$7 AND c.status='active' AND p.status='active' AND `+beichenProvider+` FOR SHARE OF p,c,a FOR UPDATE OF cp`, j.credential, j.revision, j.page, j.previous, j.key, j.owner, j.generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(items) > 0 {
		type archivedRow struct {
			receiptRow
			Details receiptCostDetails `json:"cost_details"`
		}
		batch := make([]archivedRow, 0, len(items))
		for _, row := range items {
			if len(row.RequestID) > 1024 || strings.IndexByte(row.RequestID, 0) >= 0 {
				return false, errReceiptPagination
			}
			if len(row.ActualCost) > 0 && string(row.ActualCost) != "null" {
				// Archive the upstream numeric value exactly, including adjustments.
				// School settlement has its own stricter eligibility/precision checks.
				first := row.ActualCost[0]
				if first != '-' && (first < '0' || first > '9') {
					return false, errReceiptPagination
				}
			}
			batch = append(batch, archivedRow{row, row.receiptCostDetails})
		}
		raw, err := json.Marshal(batch)
		if err != nil {
			return false, err
		}
		// A stable source row may be corrected by the website. Identity fields
		// must still agree. Update only the independent financial evidence.
		res, err := tx.ExecContext(ctx, `INSERT INTO beichen_upstream_usage_rows AS u
 (upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,channel,request_id,model,occurred_at,actual_cost,
  currency,billing_type,cost_details,subscription_id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens)
SELECT r.user_id,r.id,r.api_key_id,(SELECT provider_id FROM provider_credentials WHERE id=$1),$1,$2,nullif(r.request_id,''),r.model,r.created_at,r.actual_cost,
 nullif(r.currency,''),r.billing_type,r.cost_details,r.subscription_id,r.input_tokens,r.output_tokens,r.cache_read_tokens,r.cache_creation_tokens
FROM jsonb_to_recordset($3::jsonb) AS r(user_id bigint,id bigint,api_key_id bigint,request_id text,model text,
 created_at timestamptz,actual_cost numeric,currency text,billing_type int,cost_details jsonb,subscription_id bigint,
 input_tokens bigint,output_tokens bigint,cache_read_tokens bigint,cache_creation_tokens bigint)
ON CONFLICT(upstream_user_id,row_id) DO UPDATE SET
 actual_cost=excluded.actual_cost,currency=excluded.currency,billing_type=excluded.billing_type,cost_details=excluded.cost_details,subscription_id=excluded.subscription_id,
 input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,cache_read_tokens=excluded.cache_read_tokens,cache_write_tokens=excluded.cache_write_tokens
WHERE u.provider_id=excluded.provider_id AND u.upstream_key_id=excluded.upstream_key_id AND u.request_id IS NOT DISTINCT FROM excluded.request_id
 AND u.model=excluded.model AND u.occurred_at=excluded.occurred_at AND u.channel=excluded.channel`, j.credential, j.channel, string(raw))
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if n != int64(len(items)) {
			return false, errors.New("北辰历史流水归属冲突")
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE beichen_usage_checkpoints SET page=$2,previous_last_id=$3,upstream_user_id=nullif($4,0),
	 last_success=clock_timestamp(),last_error=NULL,caught_up=$5,generation=generation+1 WHERE credential_id=$1`, j.credential, next, boundary, j.owner, tail)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
