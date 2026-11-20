package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ReconciliationLocal struct {
	Requests         int64  `json:"requests"`
	Credits          string `json:"credits"`
	PendingCalls     int64  `json:"pending_calls"`
	Tokens           int64  `json:"tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	EstimatedMicros  *int64 `json:"estimated_micros"`
}

type ReconciliationUpstream struct {
	Requests         int64   `json:"requests"`
	Tokens           int64   `json:"tokens"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	BilledMicros     *int64  `json:"billed_micros"`
	Source           *string `json:"source"`
}

type ReconciliationMoney struct {
	StudentYuan string  `json:"student_yuan"`
	BaseYuan    string  `json:"base_yuan"`
	ActualYuan  *string `json:"actual_yuan"`
	MarginYuan  *string `json:"margin_yuan"`
	MarginRate  *string `json:"margin_rate"`
}

type ReconciliationTotals struct {
	Local    ReconciliationLocal    `json:"local"`
	Upstream ReconciliationUpstream `json:"upstream"`
	Money    ReconciliationMoney    `json:"money"`
}

type ReconciliationLine struct {
	Local                 ReconciliationLocal    `json:"local"`
	Upstream              ReconciliationUpstream `json:"upstream"`
	MatchStatus           string                 `json:"match_status"`
	Completeness          string                 `json:"completeness"`
	LocalShare            string                 `json:"local_share"`
	UpstreamShare         string                 `json:"upstream_share"`
	SchoolAbsorbedCredits string                 `json:"school_absorbed_credits"`
	ReconciliationMoney
}

type ReconciliationModel struct {
	Model string `json:"model"`
	ReconciliationLine
}

type ReconciliationKey struct {
	CredentialID uuid.UUID `json:"credential_id"`
	Label        *string   `json:"label"`
	ReconciliationLine
}

type ReconciliationCredentialCoverage struct {
	CredentialID uuid.UUID `json:"credential_id"`
	Label        *string   `json:"label"`
	ExpectedDays int64     `json:"expected_days"`
	SyncedDays   int64     `json:"synced_days"`
	MissingDays  []string  `json:"missing_days"`
}

type ReconciliationCoverage struct {
	ExpectedDays         int64                              `json:"days_expected"`
	SyncedDays           int64                              `json:"days_present"`
	MissingDays          []string                           `json:"missing_days"`
	Credentials          []ReconciliationCredentialCoverage `json:"credentials"`
	PendingCalls         int64                              `json:"pending_calls"`
	UnpricedCalls        int64                              `json:"unpriced_calls"`
	MultiplierMixed      bool                               `json:"multiplier_mixed"`
	VerifiedMatchedCalls int64                              `json:"verified_matched_calls"`
	CoverageKind         string                             `json:"coverage_kind"`
}

type ReconciliationReport struct {
	ReportPeriod
	Coverage ReconciliationCoverage `json:"coverage"`
	Totals   ReconciliationTotals   `json:"totals"`
	Models   []ReconciliationModel  `json:"models"`
	Keys     []ReconciliationKey    `json:"keys"`
}

// grouping=2 按模型，grouping=1 按供应商凭据，grouping=3 为总计。
// 两侧先各自聚合再 FULL JOIN，同名模型跨渠道时不会把学生扣分乘上账单行数。
const reconciliationSQL = reportAttributionSQL + `,
   c.id AS call_id, c.beichen_receipt_response_id, c.beichen_receipt_conflict,
   COALESCE(c.student_credits, c.beichen_credit_exact) AS settled_student_credits,
   c.school_absorbed_credits AS settled_school_credits
` + reportCallsFromSQL + `,
verified_calls AS (
 SELECT count(*) AS requests
 FROM attributed a
 JOIN beichen_receipt_response_claims claim ON claim.first_call_id = a.call_id
   AND claim.response_id = a.beichen_receipt_response_id AND claim.conflicted_at IS NULL
 JOIN beichen_call_receipts r ON r.call_id = a.call_id AND r.response_id = claim.response_id
   AND r.credential_id = a.credential_id AND r.model = a.model
 WHERE a.beichen_state = 'charged' AND NOT a.beichen_receipt_conflict
), priced_calls AS (
 SELECT a.*, CASE WHEN a.tokens_reported THEN
   beichen_weighted_tokens(a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens,
     p.upstream_input, p.upstream_output, p.upstream_cache_read, p.upstream_cache_write)
   END AS estimated_weight
 FROM report_calls a
 CROSS JOIN LATERAL beichen_price_at(a.credential_id, a.model, a.sent_at) p
), local_summary AS (
 SELECT credential_id, model, grouping(credential_id, model) AS level, count(*) AS requests,
   count(*) FILTER (WHERE tokens_reported) AS token_calls,
   COALESCE(sum(tokens), 0) AS tokens,
   COALESCE(sum(input_tokens) FILTER (WHERE tokens_reported), 0) AS input_tokens,
   COALESCE(sum(output_tokens) FILTER (WHERE tokens_reported), 0) AS output_tokens,
   COALESCE(sum(cache_read_tokens) FILTER (WHERE tokens_reported), 0) AS cache_read_tokens,
   COALESCE(sum(cache_write_tokens) FILTER (WHERE tokens_reported), 0) AS cache_write_tokens,
   CASE WHEN count(estimated_weight) = count(*) THEN round(sum(estimated_weight) / 1000000) END AS estimated_micros,
   COALESCE(sum(settled_student_credits) FILTER (WHERE beichen_state = 'charged'), 0) AS student_credits,
   COALESCE(sum(settled_school_credits) FILTER (WHERE beichen_state = 'charged'), 0) AS school_absorbed_credits,
   COALESCE(sum(COALESCE(beichen_base_credits,
     beichen_credit_exact / NULLIF(COALESCE(beichen_school_multiplier, 1), 0)))
     FILTER (WHERE beichen_state = 'charged'), 0) AS base_credits,
   count(*) FILTER (WHERE beichen_state = 'pending_review') AS pending_calls,
   count(*) FILTER (WHERE beichen_state = 'charged' AND beichen_base_credits IS NULL) AS unpriced_calls,
   count(DISTINCT COALESCE(beichen_school_multiplier, 1)) > 1 AS multiplier_mixed
 FROM priced_calls GROUP BY GROUPING SETS ((model), (credential_id), ())
), upstream_summary AS (
 SELECT credential_id, model, grouping(credential_id, model) AS level, count(*) AS row_count,
   count(*) FILTER (WHERE num_nonnulls(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) = 4) AS complete_token_rows,
   count(*) FILTER (WHERE num_nonnulls(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) > 0) AS token_rows,
   COALESCE(sum(requests), 0) AS requests,
   COALESCE(sum(input_tokens), 0) AS input_tokens, COALESCE(sum(output_tokens), 0) AS output_tokens,
   COALESCE(sum(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(sum(cache_write_tokens), 0) AS cache_write_tokens,
   CASE WHEN count(billed_upstream_micros) = count(*) THEN sum(billed_upstream_micros) END AS billed,
   string_agg(DISTINCT source, ',' ORDER BY source) AS source
 FROM beichen_daily_usage
 WHERE provider_id = $1 AND ($4::text IS NULL OR day >= $4::text::date) AND day < $5::text::date
 GROUP BY GROUPING SETS ((model), (credential_id), ())
), paired AS (
 SELECT COALESCE(l.level, u.level) AS level, COALESCE(l.model, u.model) AS model,
   COALESCE(l.credential_id, u.credential_id) AS credential_id,
   CASE WHEN COALESCE(l.requests, 0) > 0 AND COALESCE(u.row_count, 0) > 0 THEN 'matched'
        WHEN COALESCE(l.requests, 0) > 0 THEN 'local_only' ELSE 'upstream_only' END AS match_status,
   CASE WHEN l.token_calls > 0 AND l.token_calls = l.requests
          AND u.complete_token_rows > 0 AND u.complete_token_rows = u.row_count THEN 'complete'
        WHEN l.token_calls > 0 OR u.token_rows > 0 THEN 'partial' ELSE 'missing' END AS completeness,
   trim_scale(COALESCE(l.school_absorbed_credits, 0))::text AS school_absorbed_credits,
   jsonb_build_object('requests', COALESCE(l.requests, 0),
     'credits', trim_scale(COALESCE(l.student_credits, 0))::text, 'pending_calls', COALESCE(l.pending_calls, 0),
     'tokens', COALESCE(l.tokens, 0),
     'input_tokens', COALESCE(l.input_tokens, 0), 'output_tokens', COALESCE(l.output_tokens, 0),
     'cache_read_tokens', COALESCE(l.cache_read_tokens, 0), 'cache_write_tokens', COALESCE(l.cache_write_tokens, 0),
     'estimated_micros', CASE WHEN l.estimated_micros <= 9223372036854775807 THEN l.estimated_micros END) AS local,
   jsonb_build_object('requests', COALESCE(u.requests, 0),
     'tokens', COALESCE(u.input_tokens, 0) + COALESCE(u.output_tokens, 0) + COALESCE(u.cache_read_tokens, 0) + COALESCE(u.cache_write_tokens, 0),
     'input_tokens', COALESCE(u.input_tokens, 0), 'output_tokens', COALESCE(u.output_tokens, 0),
     'cache_read_tokens', COALESCE(u.cache_read_tokens, 0), 'cache_write_tokens', COALESCE(u.cache_write_tokens, 0),
     'billed_micros', u.billed, 'source', u.source) AS upstream,
   COALESCE(l.student_credits, 0) * 0.01::numeric AS student,
   COALESCE(l.base_credits, 0) * 0.01::numeric AS base,
   u.billed * 0.000001::numeric AS actual,
   COALESCE(l.tokens, 0) AS local_tokens,
   COALESCE(u.input_tokens, 0) + COALESCE(u.output_tokens, 0) + COALESCE(u.cache_read_tokens, 0) + COALESCE(u.cache_write_tokens, 0) AS upstream_tokens
 FROM local_summary l FULL JOIN upstream_summary u
   ON l.level = u.level AND l.model IS NOT DISTINCT FROM u.model AND l.credential_id IS NOT DISTINCT FROM u.credential_id
), amounts AS (
 SELECT p.*, jsonb_build_object(
   'student_yuan', trim_scale(student)::text, 'base_yuan', trim_scale(base)::text,
   'actual_yuan', CASE WHEN actual IS NOT NULL THEN trim_scale(actual)::text END,
   'margin_yuan', CASE WHEN actual IS NOT NULL THEN trim_scale(student - actual)::text END,
   'margin_rate', CASE WHEN actual IS NOT NULL THEN trim_scale(CASE WHEN student = 0 THEN 0 ELSE (student - actual) / student END)::text END) AS money
 FROM paired p
), lines AS (
 SELECT a.*, jsonb_build_object('local', a.local, 'upstream', a.upstream, 'match_status', a.match_status,
   'completeness', a.completeness, 'school_absorbed_credits', a.school_absorbed_credits,
   'local_share', trim_scale(CASE WHEN t.local_tokens = 0 THEN 0 ELSE a.local_tokens / t.local_tokens END)::text,
   'upstream_share', trim_scale(CASE WHEN t.upstream_tokens = 0 THEN 0 ELSE a.upstream_tokens / t.upstream_tokens END)::text) || a.money AS item
 FROM amounts a CROSS JOIN amounts t WHERE t.level = 3
), expected_days AS (
 SELECT c.id AS credential_id, c.label, d.day::date AS day
 FROM provider_credentials c CROSS JOIN LATERAL generate_series(
   GREATEST((c.created_at AT TIME ZONE 'Asia/Shanghai')::date, COALESCE($4::text::date, (c.created_at AT TIME ZONE 'Asia/Shanghai')::date))::timestamp,
   (LEAST($5::text::date, $6::text::date) - 1)::timestamp, interval '1 day') d(day)
 WHERE c.provider_id = $1
), coverage_days AS (
 SELECT e.*, u.credential_id IS NOT NULL AS synced
 FROM expected_days e LEFT JOIN beichen_usage_days u ON u.provider_id = $1 AND u.credential_id = e.credential_id AND u.day = e.day
), credential_coverage AS (
 SELECT c.id, c.label, count(d.day) AS expected_days, count(d.day) FILTER (WHERE d.synced) AS synced_days,
   COALESCE(jsonb_agg(to_char(d.day, 'YYYY-MM-DD') ORDER BY d.day) FILTER (WHERE NOT d.synced), '[]'::jsonb) AS missing_days
 FROM provider_credentials c LEFT JOIN coverage_days d ON d.credential_id = c.id
 WHERE c.provider_id = $1 GROUP BY c.id, c.label
)
SELECT jsonb_build_object(
 'totals', (SELECT jsonb_build_object('local', local, 'upstream', upstream, 'money', money) FROM amounts WHERE level = 3),
 'models', COALESCE((SELECT jsonb_agg(jsonb_build_object('model', model) || item ORDER BY model) FROM lines WHERE level = 2), '[]'::jsonb),
 'keys', COALESCE((SELECT jsonb_agg(jsonb_build_object('credential_id', l.credential_id, 'label', c.label) || l.item
   ORDER BY c.label NULLS LAST, l.credential_id) FROM lines l
   LEFT JOIN provider_credentials c ON c.id = l.credential_id AND c.provider_id = $1 WHERE l.level = 1), '[]'::jsonb),
 'coverage', jsonb_build_object(
   'days_expected', (SELECT count(*) FROM coverage_days), 'days_present', (SELECT count(*) FROM coverage_days WHERE synced),
   'missing_days', COALESCE((SELECT jsonb_agg(to_char(day, 'YYYY-MM-DD') ORDER BY day)
     FROM (SELECT DISTINCT day FROM coverage_days WHERE NOT synced) missing), '[]'::jsonb),
   'credentials', COALESCE((SELECT jsonb_agg(jsonb_build_object('credential_id', id, 'label', label,
     'expected_days', expected_days, 'synced_days', synced_days, 'missing_days', missing_days) ORDER BY label, id)
     FROM credential_coverage), '[]'::jsonb),
   'pending_calls', (SELECT pending_calls FROM local_summary WHERE level = 3),
   'unpriced_calls', (SELECT unpriced_calls FROM local_summary WHERE level = 3),
   'verified_matched_calls', (SELECT requests FROM verified_calls),
   'multiplier_mixed', (SELECT multiplier_mixed FROM local_summary WHERE level = 3)))`

func readReconciliation(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, period ReportPeriod, now time.Time) (ReconciliationReport, error) {
	var startDay any
	if period.Start != nil {
		startDay = period.Start.In(beijing).Format(dayLayout)
	}
	end := period.End.In(beijing)
	endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, beijing)
	if end.After(endDay) {
		endDay = endDay.AddDate(0, 0, 1)
	}
	localNow := now.In(beijing)
	// 尚未开始的未来账日不算缺失；当天尚未整日确认的账单仍显示待同步。
	coverageEnd := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, beijing).AddDate(0, 0, 1)
	var report ReconciliationReport
	var raw []byte
	err := tx.QueryRowContext(ctx, reconciliationSQL, providerID, period.Start, period.End,
		startDay, endDay.Format(dayLayout), coverageEnd.Format(dayLayout)).Scan(&raw)
	if err != nil {
		return report, err
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, err
	}
	switch {
	case report.Coverage.SyncedDays == 0:
		report.Coverage.CoverageKind = "none"
	case report.Coverage.SyncedDays == report.Coverage.ExpectedDays:
		report.Coverage.CoverageKind = "full"
	default:
		report.Coverage.CoverageKind = "partial"
	}
	report.ReportPeriod = period
	return report, nil
}

func (s *Service) Reconciliation(ctx context.Context, providerID uuid.UUID, value string, now time.Time) (ReconciliationReport, error) {
	period, err := reportPeriod(value, now)
	if err != nil {
		return ReconciliationReport{}, err
	}
	if providerID == uuid.Nil {
		return ReconciliationReport{}, invalid("需要有效的供应商 ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ReconciliationReport{}, unavailable("开启北辰区间对账失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, providerID); err != nil {
		return ReconciliationReport{}, err
	}
	report, err := readReconciliation(ctx, tx, providerID, period, now)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ReconciliationReport{}, unavailable("读取北辰区间对账失败", err)
	}
	return report, nil
}
