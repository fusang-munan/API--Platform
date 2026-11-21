package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Use the existing ledger's dispatch attribution and protocol-specific token
// normalization. Key IDs pinned at dispatch survive subsequent Key rotation.
const dashboardLocalSQL = reportAttributionSQL + `,
   c.id AS call_id, c.beichen_receipt_api_key_id AS website_key_id,
   c.beichen_receipt_response_id AS response_id, c.beichen_receipt_conflict AS receipt_conflict,
   c.beichen_upstream_price_version AS upstream_price_version,
   c.request_path, c.price_snapshot, c.price_tier,
   COALESCE(c.student_credits, c.beichen_credit_exact) AS student_credits
` + reportCallsFromSQL + `,
evidence AS (
 SELECT to_char(a.sent_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD') AS day, a.model, a.website_key_id,
   a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens,
   CASE WHEN a.total_tokens IS NOT NULL OR a.token_parts = 4 THEN a.tokens END AS tokens,
   a.tokens_complete, a.beichen_state, a.receipt_conflict, r.actual_cost,p.source AS price_source,
   CASE WHEN a.beichen_state = 'charged' THEN a.student_credits END AS student_credits,
   CASE WHEN a.tokens_complete AND a.price_snapshot IS NULL AND NULLIF(a.price_tier, '') IS NULL
     AND a.request_path NOT LIKE '%/images/%' AND a.request_path NOT LIKE '%/video%'
     AND a.request_path NOT LIKE '%/audio/%' AND (a.protocol <> 'anthropic' OR a.cache_write_tokens = 0)
     THEN beichen_weighted_tokens(a.input_tokens::bigint, a.output_tokens::bigint, a.cache_read_tokens,
       a.cache_write_tokens, p.input_rate, p.output_rate, p.cache_read_rate, p.cache_write_rate)
       * 0.000000000001::numeric END AS standard_estimate
 FROM report_calls a
 LEFT JOIN beichen_receipt_response_claims claim ON claim.first_call_id = a.call_id
   AND claim.response_id = a.response_id AND claim.conflicted_at IS NULL AND NOT a.receipt_conflict
 LEFT JOIN beichen_call_receipts r ON r.call_id = a.call_id AND r.response_id = claim.response_id
   AND r.credential_id = a.credential_id AND r.api_key_id = a.website_key_id AND r.model = a.model
 LEFT JOIN beichen_price_versions p ON p.id = a.upstream_price_version AND p.credential_id = a.credential_id
   AND p.model = a.model AND p.track = 'upstream' AND p.basis = 'base'
   AND p.source IN ('billing','panel','derived_from_usage')
   AND p.effective_at <= a.sent_at
)
SELECT day, model, COALESCE(website_key_id,0), count(*),
  CASE WHEN count(tokens)=count(*) THEN sum(tokens) END::bigint,
  CASE WHEN count(input_tokens)=count(*) THEN sum(input_tokens) END::bigint,
  CASE WHEN count(output_tokens)=count(*) THEN sum(output_tokens) END::bigint,
  CASE WHEN count(cache_read_tokens)=count(*) THEN sum(cache_read_tokens) END::bigint,
  CASE WHEN count(cache_write_tokens)=count(*) THEN sum(cache_write_tokens) END::bigint,
  CASE WHEN count(standard_estimate)=count(*) THEN trim_scale(sum(standard_estimate))::text END,
  CASE WHEN count(actual_cost)=count(*) THEN trim_scale(sum(actual_cost))::text END,
  trim_scale(sum(student_credits))::text,
  count(student_credits), count(actual_cost), count(*) FILTER (WHERE beichen_state='pending_review'),
  count(*) FILTER (WHERE NOT tokens_complete), count(*) FILTER (WHERE receipt_conflict),
  count(*) FILTER (WHERE price_source='derived_from_usage'),
  count(*) FILTER (WHERE price_source<>'derived_from_usage')
FROM evidence GROUP BY day, model, COALESCE(website_key_id,0)
`

type dashboardLineKey struct {
	Date  string `json:"date"`
	Model string `json:"model"`
	KeyID int64  `json:"key_id,omitempty"`
}

type dashboardLocalUsage struct {
	dashboardMetrics
	StandardSource     string       `json:"standard_source,omitempty"`
	StandardPriceLabel string       `json:"standard_price_label,omitempty"`
	StudentCredits     *json.Number `json:"student_credits"`
	StudentCNY         *json.Number `json:"student_cny"`
	StudentCalls       int64        `json:"student_charged_calls"`
	ReceiptCalls       int64        `json:"verified_receipt_calls"`
	PendingCalls       int64        `json:"pending_calls"`
	TokenMissingCalls  int64        `json:"token_incomplete_calls"`
	ConflictedCalls    int64        `json:"receipt_conflict_calls"`
}

type dashboardLocalLine struct {
	dashboardLineKey
	KeyName        string               `json:"key_name,omitempty"`
	Status         string               `json:"status"`
	Website        *dashboardMetrics    `json:"website"`
	Local          *dashboardLocalUsage `json:"local"`
	Delta          dashboardMetrics     `json:"website_minus_local"`
	StudentDelta   *json.Number         `json:"student_minus_website_actual_cny"`
	MissingReasons []string             `json:"missing_reasons"`
}

type dashboardLocalCoverage struct {
	WebsiteRows          int      `json:"website_rows"`
	LocalRows            int      `json:"local_rows"`
	MatchedRows          int      `json:"matched_rows"`
	WebsiteOnlyRows      int      `json:"website_only_rows"`
	LocalOnlyRows        int      `json:"local_only_rows"`
	UnmappedLocalRows    int      `json:"unmapped_local_rows"`
	WebsiteCoverage      *float64 `json:"website_row_coverage"`
	LocalCoverage        *float64 `json:"local_row_coverage"`
	LocalRequests        int64    `json:"local_requests"`
	LocalVerifiedCalls   int64    `json:"local_verified_receipt_calls"`
	LocalReceiptCoverage *float64 `json:"local_receipt_coverage"`
}

type dashboardLocalReport struct {
	Currency     string                 `json:"currency"`
	ProviderID   uuid.UUID              `json:"provider_id"`
	ReadAt       time.Time              `json:"local_read_at"`
	WebsiteCheck dashboardTotalsCheck   `json:"website_ledger_check"`
	Coverage     dashboardLocalCoverage `json:"coverage"`
	Lines        []dashboardLocalLine   `json:"lines"`
}

func (d dashboardSession) reconcile(ctx context.Context, report DashboardReport, provider uuid.UUID, ledger []dashboardUsageRecord) (*dashboardLocalReport, error) {
	provider, err := d.service.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return nil, errors.New("无法确认本机北辰供应商，请检查数据库和 --provider 参数")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := d.service.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, errors.New("无法打开本机 PostgreSQL 只读事务")
	}
	defer tx.Rollback()
	at := d.service.now().In(beijing)
	rows, err := readDashboardLocalRows(ctx, tx, provider, report.dashboardWindow)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("未能完成本机账本只读查询")
	}
	out, err := d.compareLocalRows(report, ledger, rows)
	if err != nil {
		return nil, err
	}
	out.ProviderID, out.ReadAt = provider, at
	return out, nil
}

func readDashboardLocalRows(ctx context.Context, tx *sql.Tx, provider uuid.UUID, window dashboardWindow) (map[dashboardLineKey]dashboardLocalUsage, error) {
	start, err := dayStart(window.StartDate)
	if err != nil {
		return nil, err
	}
	end, err := dayStart(window.EndDate)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, dashboardLocalSQL, provider, start, end.AddDate(0, 0, 1))
	if err != nil {
		return nil, errors.New("读取本机 gateway_calls 失败，请确认数据库已应用本分支迁移")
	}
	defer rows.Close()
	out := make(map[dashboardLineKey]dashboardLocalUsage)
	for rows.Next() {
		var key dashboardLineKey
		var result dashboardLocalUsage
		var derived, other int64
		if err := rows.Scan(&key.Date, &key.Model, &key.KeyID, &result.Requests,
			&result.Tokens, &result.Input, &result.Output, &result.CacheRead, &result.CacheWrite,
			&result.Standard, &result.Actual, &result.StudentCredits,
			&result.StudentCalls, &result.ReceiptCalls, &result.PendingCalls, &result.TokenMissingCalls,
			&result.ConflictedCalls, &derived, &other); err != nil {
			return nil, errors.New("本机账本字段格式异常")
		}
		if result.StudentCredits != nil {
			amount := dashboardAmount(*result.StudentCredits)
			if amount == nil {
				return nil, errors.New("本机学生扣分字段格式异常")
			}
			result.StudentCNY = dashboardAmountNumber(amount.Quo(amount, big.NewRat(100, 1)))
		}
		if derived > 0 {
			result.StandardSource, result.StandardPriceLabel = derivedPriceSource, derivedPriceLabel
			if other > 0 {
				result.StandardSource = "mixed"
				result.StandardPriceLabel = "含反推价，待核对"
			}
		} else if result.Standard != nil {
			result.StandardSource = "pinned_base_token_rate_estimate"
		}
		out[key] = result
	}
	if rows.Err() != nil {
		return nil, errors.New("本机账本未读完")
	}
	return out, nil
}

func (d dashboardSession) compareLocalRows(report DashboardReport, records []dashboardUsageRecord, local map[dashboardLineKey]dashboardLocalUsage) (*dashboardLocalReport, error) {
	web := make(map[dashboardLineKey]*dashboardSum)
	for _, record := range records {
		key := dashboardLineKey{record.CreatedAt.In(beijing).Format(dayLayout), record.Model, record.KeyID}
		if key.KeyID == 0 {
			key.KeyID = -1 // Keep rows without attribution on their own side.
		}
		if web[key] == nil {
			web[key] = new(dashboardSum)
		}
		if err := web[key].add(record.metrics()); err != nil {
			return nil, dashboardReadError(dashboardUsage, err)
		}
	}

	keyNames := make(map[int64]string, len(report.Keys))
	for _, key := range report.Keys {
		keyNames[key.ID] = key.Name
	}
	out := &dashboardLocalReport{Currency: dashboardUnit, Lines: make([]dashboardLocalLine, 0, len(web)+len(local))}
	out.Coverage.WebsiteRows, out.Coverage.LocalRows = len(web), len(local)
	websiteTotals := make([]dashboardMetrics, 0, len(web))
	for key, sum := range web {
		website := sum.metrics()
		websiteTotals = append(websiteTotals, website)
		localRow, ok := local[key]
		var matched *dashboardLocalUsage
		if ok {
			matched = &localRow
			out.Coverage.MatchedRows++
		}
		out.Lines = append(out.Lines, d.localLine(key, keyNames, &website, matched))
	}
	for key, row := range local {
		out.Coverage.LocalRequests += row.Requests
		out.Coverage.LocalVerifiedCalls += row.ReceiptCalls
		if key.KeyID == 0 {
			out.Coverage.UnmappedLocalRows++
		}
		if web[key] == nil {
			out.Lines = append(out.Lines, d.localLine(key, keyNames, nil, &row))
		}
	}
	coverage := &out.Coverage
	coverage.WebsiteOnlyRows = coverage.WebsiteRows - coverage.MatchedRows
	coverage.LocalOnlyRows = coverage.LocalRows - coverage.MatchedRows
	coverage.WebsiteCoverage = dashboardRatio(int64(coverage.MatchedRows), int64(coverage.WebsiteRows))
	coverage.LocalCoverage = dashboardRatio(int64(coverage.MatchedRows), int64(coverage.LocalRows))
	coverage.LocalReceiptCoverage = dashboardRatio(coverage.LocalVerifiedCalls, coverage.LocalRequests)
	sort.Slice(out.Lines, func(i, j int) bool {
		a, b := out.Lines[i], out.Lines[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.KeyID != b.KeyID {
			return a.KeyID < b.KeyID
		}
		return a.Status < b.Status
	})
	check, err := compareDashboardTotals(report.Totals, websiteTotals)
	if err != nil {
		return nil, fmt.Errorf("北辰逐笔流水合计 %w", err)
	}
	out.WebsiteCheck = check
	return out, nil
}

func dashboardRatio(numerator, denominator int64) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func (d dashboardSession) localLine(key dashboardLineKey, keyNames map[int64]string, website *dashboardMetrics, local *dashboardLocalUsage) dashboardLocalLine {
	line := dashboardLocalLine{
		dashboardLineKey: key, Status: "matched", Website: website, Local: local, MissingReasons: []string{},
	}
	line.Model = d.safeText(key.Model)
	line.KeyName = keyNames[key.KeyID]
	var a, b dashboardMetrics
	if website == nil {
		line.Status = "local_only"
		line.MissingReasons = append(line.MissingReasons, "website_row_missing")
	} else {
		a = *website
		if a.Tokens == nil {
			line.MissingReasons = append(line.MissingReasons, "website_token_fields_missing")
		}
		if a.Standard == nil || a.Actual == nil {
			line.MissingReasons = append(line.MissingReasons, "website_amount_fields_missing")
		}
	}
	if local == nil {
		line.Status = "website_only"
		line.MissingReasons = append(line.MissingReasons, "local_row_missing")
	} else {
		b = local.dashboardMetrics
		if local.TokenMissingCalls > 0 {
			line.MissingReasons = append(line.MissingReasons, "local_token_fields_incomplete")
		}
		if local.Standard == nil {
			line.MissingReasons = append(line.MissingReasons, "local_standard_estimate_unavailable")
		}
		if local.ReceiptCalls < local.Requests {
			line.MissingReasons = append(line.MissingReasons, "local_verified_receipts_incomplete")
		}
		if local.ConflictedCalls > 0 {
			line.MissingReasons = append(line.MissingReasons, "local_receipt_conflict")
		}
		if local.PendingCalls > 0 {
			line.MissingReasons = append(line.MissingReasons, "local_pending_review")
		}
		if local.StudentCalls < local.Requests {
			line.MissingReasons = append(line.MissingReasons, "local_student_charges_incomplete")
		} else {
			line.StudentDelta = dashboardMoneyDelta(local.StudentCNY, a.Actual)
		}
	}
	if key.KeyID <= 0 {
		if website == nil {
			line.MissingReasons = append(line.MissingReasons, "local_website_key_unpinned")
		} else {
			line.MissingReasons = append(line.MissingReasons, "website_key_id_missing")
		}
		line.KeyID = 0
	} else if _, ok := keyNames[key.KeyID]; !ok {
		line.MissingReasons = append(line.MissingReasons, "key_absent_from_current_inventory")
	}
	line.Delta = a.minus(b)
	return line
}
