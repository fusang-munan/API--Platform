package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type ReconciliationLocal struct {
	Requests                  int64  `json:"requests"`
	Credits                   string `json:"credits"`
	PendingCalls              int64  `json:"pending_calls"`
	Tokens                    int64  `json:"tokens"`
	InputTokens               int64  `json:"input_tokens"`
	OutputTokens              int64  `json:"output_tokens"`
	CacheReadTokens           int64  `json:"cache_read_tokens"`
	CacheWriteTokens          int64  `json:"cache_write_tokens"`
	TokensMissing             int64  `json:"tokens_missing"`
	ChargedTokensMissingCalls int64  `json:"charged_tokens_missing_calls"`
	Completeness              string `json:"completeness"`
	EstimatedMicros           *int64 `json:"estimated_micros"`
}

type ReconciliationUpstream struct {
	Requests         int64                      `json:"requests"` // 北辰日账回报的调用笔数。
	Tokens           int64                      `json:"tokens"`   // 北辰 Token 总量，为四项已回报分项的合计。
	InputTokens      int64                      `json:"input_tokens"`
	OutputTokens     int64                      `json:"output_tokens"`
	CacheReadTokens  int64                      `json:"cache_read_tokens"`
	CacheWriteTokens int64                      `json:"cache_write_tokens"`
	Completeness     string                     `json:"completeness"`
	BilledMicros     *int64                     `json:"billed_micros"`
	Source           *string                    `json:"source"`
	ActualCost       *string                    `json:"actual_cost"`
	Unit             *string                    `json:"unit"`
	MixedUnits       bool                       `json:"mixed_units"`
	Stale            bool                       `json:"stale"`
	AmountsByUnit    []ReconciliationUnitAmount `json:"amounts_by_unit,omitempty"`
}

type ReconciliationUnitAmount struct {
	Unit       *string `json:"unit"`
	ActualCost *string `json:"actual_cost"`
}

type ReconciliationMoney struct {
	StudentYuan     string  `json:"student_yuan"`
	BaseYuan        string  `json:"base_yuan"`
	ActualYuan      *string `json:"actual_yuan"`
	MarginYuan      *string `json:"margin_yuan"`
	MarginRate      *string `json:"margin_rate"`
	MarginAvailable bool    `json:"margin_available"`
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
	SchoolAbsorbedCredits *string                `json:"school_absorbed_credits"`
	ReconciliationMoney
}

type ReconciliationModel struct {
	CredentialID uuid.UUID `json:"credential_id"`
	Label        *string   `json:"label"`
	Model        string    `json:"model"`
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

type ReconciliationExcludedDay struct {
	Date    string   `json:"date"`
	Reasons []string `json:"reasons"`
}

type ReconciliationCoverage struct {
	ExcludedDays                      []ReconciliationExcludedDay        `json:"excluded_days"`
	Comparable                        bool                               `json:"comparable"`
	EligibleDates                     []string                           `json:"eligible_dates"`
	ExpectedDays                      int64                              `json:"days_expected"`
	SyncedDays                        int64                              `json:"days_present"`
	ComparableDays                    int64                              `json:"comparable_days"`
	MissingDays                       []string                           `json:"missing_days"`
	Credentials                       []ReconciliationCredentialCoverage `json:"credentials"`
	PendingCalls                      int64                              `json:"pending_calls"`
	UnpricedCalls                     int64                              `json:"unpriced_calls"`
	MultiplierMixed                   bool                               `json:"multiplier_mixed"`
	VerifiedMatchedCalls              int64                              `json:"verified_matched_calls"`
	VerifiedMatchedCompleteTokenCalls int64                              `json:"verified_matched_complete_token_calls"`
	CoverageKind                      string                             `json:"coverage_kind"`
	KeyUsageDays                      int64                              `json:"key_usage_days"`
	KeyUsageModelDays                 int64                              `json:"key_usage_model_days"`
	StaleKeyUsageDays                 int64                              `json:"stale_key_usage_days"`
}

type ReconciliationView struct {
	Coverage ReconciliationCoverage `json:"coverage"`
	Totals   ReconciliationTotals   `json:"totals"`
	Models   []ReconciliationModel  `json:"models"`
	Keys     []ReconciliationKey    `json:"keys"`
}

const (
	unmatchedScopeObserved   = "observed"
	unmatchedScopeComparable = "comparable"
)

type ReconciliationReport struct {
	ReportPeriod
	ReconciliationView
	ComparableOnly      *ReconciliationView    `json:"comparable_only"`
	UnmatchedUpstream   []UnmatchedUpstreamRow `json:"unmatched_upstream"`
	UnmatchedTotal      int64                  `json:"unmatched_total"`
	UnmatchedNextCursor *string                `json:"unmatched_next_cursor"`
	UnmatchedScope      string                 `json:"unmatched_scope"`
}

// grouping=0 按凭据和别名解析后的模型，grouping=1 按凭据，grouping=3 为总计。
// 两侧先各自聚合再 FULL JOIN，保留每条渠道独有的模型记录。
const reconciliationSQL = reportAttributionSQL + `,
   c.id AS call_id, c.beichen_receipt_response_id, c.beichen_receipt_conflict,
   COALESCE(c.student_credits, c.beichen_credit_exact) AS settled_student_credits,
   CASE WHEN c.student_credits IS NOT NULL THEN c.school_absorbed_credits END AS settled_school_credits,
   COALESCE(c.beichen_base_credits, (c.pricing_detail->>'base_credits')::numeric) AS recorded_base_credits,
   COALESCE(c.actual_credits, c.beichen_credit_exact) AS full_charge_credits
` + reportCallsFromSQL + `,
usage_scope AS (
 SELECT $1::uuid AS provider_id,$4::text::date AS first_day,$5::text::date AS end_day,
   $6::text::date AS today,$7::timestamptz AS as_of
)` + usage.BeichenDailyEvidenceSQL + reconciliationKeyFactsSQL + `,
selected_calls AS (
 SELECT * FROM report_calls WHERE $8::text[] IS NULL OR (sent_at AT TIME ZONE 'Asia/Shanghai')::date = ANY($8::text[]::date[])
), selected_models AS (
 SELECT * FROM model_usage_rows WHERE $8::text[] IS NULL OR day = ANY($8::text[]::date[])
), selected_key_facts AS (
 SELECT * FROM key_facts WHERE $8::text[] IS NULL OR day = ANY($8::text[]::date[])
), selected_key_days AS (
 SELECT * FROM key_usage_days WHERE $8::text[] IS NULL OR day = ANY($8::text[]::date[])
), verified_calls AS (
 SELECT count(*) AS requests, count(*) FILTER (WHERE a.tokens_complete) AS complete_token_calls
 FROM selected_calls a
 JOIN beichen_receipt_response_claims claim ON claim.first_call_id = a.call_id
   AND claim.response_id = a.beichen_receipt_response_id AND claim.conflicted_at IS NULL
 JOIN beichen_call_receipts r ON r.call_id = a.call_id AND r.response_id = claim.response_id
   AND r.credential_id = a.credential_id AND r.model = a.model
 WHERE a.beichen_state = 'charged' AND NOT a.beichen_receipt_conflict
), priced_calls AS (
 SELECT a.*, COALESCE(alias.school_model, a.model) AS school_model,
   CASE WHEN a.tokens_complete AND a.beichen_state IS DISTINCT FROM 'pending_review' THEN
   beichen_weighted_tokens(a.input_tokens::bigint, a.output_tokens::bigint, a.cache_read_tokens, a.cache_write_tokens,
     p.upstream_input, p.upstream_output, p.upstream_cache_read, p.upstream_cache_write)
   END AS estimated_weight
 FROM selected_calls a ` + joinModelAliasSQL + `
 CROSS JOIN LATERAL beichen_price_at(a.credential_id, a.model, a.sent_at) p
), local_summary AS (
 SELECT credential_id, school_model AS model, grouping(credential_id, school_model) AS level, count(*) AS requests,
   ` + tokenCompletenessSQL + ` AS token_completeness,
   COALESCE(sum(missing_tokens), 0) AS tokens_missing,
   ` + chargedTokensMissingCallsSQL + ` AS charged_tokens_missing_calls,
   COALESCE(sum(tokens), 0) AS tokens,
   COALESCE(sum(input_tokens), 0) AS input_tokens,
   COALESCE(sum(output_tokens), 0) AS output_tokens,
   COALESCE(sum(cache_read_tokens), 0) AS cache_read_tokens,
   COALESCE(sum(cache_write_tokens), 0) AS cache_write_tokens,
   CASE WHEN count(estimated_weight) = count(*) THEN round(sum(estimated_weight) / 1000000) END AS estimated_micros,
   COALESCE(sum(settled_student_credits) FILTER (WHERE beichen_state = 'charged'), 0) AS student_credits,
   sum(settled_school_credits) FILTER (WHERE beichen_state = 'charged') AS school_absorbed_credits,
   COALESCE(sum(COALESCE(recorded_base_credits,
     full_charge_credits / NULLIF(COALESCE(beichen_school_multiplier, 1), 0)))
     FILTER (WHERE beichen_state = 'charged'), 0) AS base_credits,
   count(*) FILTER (WHERE beichen_state = 'pending_review') AS pending_calls,
   count(*) FILTER (WHERE beichen_state = 'charged' AND recorded_base_credits IS NULL) AS unpriced_calls,
   count(DISTINCT COALESCE(beichen_school_multiplier, 1)) > 1 AS multiplier_mixed
 FROM priced_calls GROUP BY GROUPING SETS ((credential_id, school_model), (credential_id), ())
)` + reconciliationSourcesSQL + `,
credential_lifecycle AS (
 SELECT id, created_at, COALESCE(status,'active')='active' AS required,
   (retired_at AT TIME ZONE 'Asia/Shanghai')::date AS retired_day
 FROM provider_credentials WHERE provider_id = $1
), expected_days AS (
 SELECT c.id AS credential_id, d.day::date AS day
 FROM credential_lifecycle c CROSS JOIN LATERAL generate_series(
   GREATEST((c.created_at AT TIME ZONE 'Asia/Shanghai')::date, $4::text::date)::timestamp,
   ($5::text::date - 1)::timestamp, interval '1 day') d(day)
 WHERE c.required OR (c.retired_day IS NOT NULL AND d.day::date <= c.retired_day)
 UNION
 SELECT f.credential_id, f.day
 FROM (
   SELECT credential_id, (sent_at AT TIME ZONE 'Asia/Shanghai')::date AS day FROM report_calls
   UNION
   SELECT credential_id, day FROM model_usage_rows
   UNION
   SELECT credential_id, day FROM key_usage_days
 ) f
 LEFT JOIN credential_lifecycle c ON c.id = f.credential_id
 WHERE c.id IS NULL OR c.required OR (c.retired_day IS NOT NULL AND f.day <= c.retired_day)
), coverage_days AS (
 SELECT e.*, COALESCE(NOT d.stale,false) OR w.credential_id IS NOT NULL AS synced,
   COALESCE(d.stale,false) AND w.credential_id IS NULL AS stale
 FROM expected_days e
 LEFT JOIN scoped_days d ON d.credential_id=e.credential_id AND d.day=e.day
 LEFT JOIN website_days w ON w.credential_id=e.credential_id AND w.day=e.day
 WHERE $8::text[] IS NULL OR e.day = ANY($8::text[]::date[])
), day_cost_quality AS (
 SELECT day, bool_and(actual_cost IS NOT NULL) AS amounts_known,
   bool_and((unit IS NULL OR unit='CNY' OR (unit='unknown' AND requests=0 AND actual_cost=0)) IS TRUE) AS units_compatible,
   bool_or(stale) AS stale
 FROM selected_key_facts GROUP BY day
), day_coverage AS (
 SELECT day, bool_and(synced) AS synced, bool_or(stale) AS stale FROM coverage_days GROUP BY day
), day_eligibility AS (
 SELECT d.day, d.synced, array_remove(ARRAY[
   CASE WHEN d.day >= $6::text::date THEN 'open_day' END,
   CASE WHEN NOT d.synced THEN 'incomplete_sync' END,
   CASE WHEN d.stale OR q.stale THEN 'stale' END,
   CASE WHEN q.amounts_known = false OR (d.synced AND q.day IS NULL) THEN 'missing_cost' END,
   CASE WHEN q.units_compatible = false THEN 'incompatible_unit' END
 ],NULL) AS reasons
 FROM day_coverage d LEFT JOIN day_cost_quality q USING(day)
), day_summary AS (
 SELECT COALESCE(jsonb_agg(to_char(day,'YYYY-MM-DD') ORDER BY day) FILTER
   (WHERE cardinality(reasons)=0), '[]'::jsonb) AS eligible_dates,
   COALESCE(jsonb_agg(jsonb_build_object('date',to_char(day,'YYYY-MM-DD'),'reasons',reasons) ORDER BY day)
     FILTER (WHERE cardinality(reasons)>0), '[]'::jsonb) AS excluded_days,
   count(*) FILTER (WHERE synced) AS comparable_days, COALESCE(bool_and(synced),false) AS all_days_complete,
   COALESCE(jsonb_agg(to_char(day, 'YYYY-MM-DD') ORDER BY day) FILTER (WHERE NOT synced), '[]'::jsonb) AS missing_days
 FROM day_eligibility
), credential_coverage AS (
 SELECT ids.credential_id AS id, c.label, count(d.day) AS expected_days, count(d.day) FILTER (WHERE d.synced) AS synced_days,
   COALESCE(jsonb_agg(to_char(d.day, 'YYYY-MM-DD') ORDER BY d.day) FILTER (WHERE NOT d.synced), '[]'::jsonb) AS missing_days
 FROM (SELECT id AS credential_id FROM provider_credentials WHERE provider_id = $1
       UNION SELECT credential_id FROM coverage_days) ids
 LEFT JOIN provider_credentials c ON c.id = ids.credential_id AND c.provider_id = $1
 LEFT JOIN coverage_days d ON d.credential_id = ids.credential_id
 GROUP BY ids.credential_id, c.label
), paired AS (
 SELECT COALESCE(l.level, u.level) AS level, COALESCE(l.model, u.model) AS model,
   COALESCE(l.credential_id, u.credential_id) AS credential_id,
   CASE WHEN COALESCE(l.requests, 0) > 0 AND COALESCE(u.row_count, 0) > 0 THEN 'matched'
        WHEN COALESCE(l.requests, 0) > 0 THEN 'local_only' ELSE 'upstream_only' END AS match_status,
   CASE WHEN l.token_completeness = 'complete' AND u.token_completeness = 'complete' THEN 'complete'
        WHEN l.token_completeness IN ('complete', 'partial') OR u.token_completeness IN ('complete', 'partial') THEN 'partial'
        ELSE 'missing' END AS completeness,
   trim_scale(l.school_absorbed_credits)::text AS school_absorbed_credits,
   jsonb_build_object('requests', COALESCE(l.requests, 0),
     'credits', trim_scale(COALESCE(l.student_credits, 0))::text, 'pending_calls', COALESCE(l.pending_calls, 0),
     'tokens', COALESCE(l.tokens, 0),
     'input_tokens', COALESCE(l.input_tokens, 0), 'output_tokens', COALESCE(l.output_tokens, 0),
     'cache_read_tokens', COALESCE(l.cache_read_tokens, 0), 'cache_write_tokens', COALESCE(l.cache_write_tokens, 0),
     'tokens_missing', COALESCE(l.tokens_missing, 0), 'charged_tokens_missing_calls', COALESCE(l.charged_tokens_missing_calls, 0),
     'completeness', COALESCE(l.token_completeness, 'missing'),
     'estimated_micros', CASE WHEN l.estimated_micros <= 9223372036854775807 THEN l.estimated_micros END) AS local,
   jsonb_build_object('requests', COALESCE(u.requests, 0),
     'tokens', COALESCE(u.tokens, 0),
     'input_tokens', COALESCE(u.input_tokens, 0), 'output_tokens', COALESCE(u.output_tokens, 0),
     'cache_read_tokens', COALESCE(u.cache_read_tokens, 0), 'cache_write_tokens', COALESCE(u.cache_write_tokens, 0),
     'completeness', COALESCE(u.token_completeness, 'missing'),
     'billed_micros', CASE WHEN u.unit IS NULL AND NOT u.mixed_units AND u.actual_cost * 1000000 <= 9223372036854775807
       THEN (u.actual_cost * 1000000)::bigint END,
     'actual_cost', trim_scale(u.actual_cost)::text, 'unit', u.unit,
     'amounts_by_unit', CASE WHEN u.mixed_units THEN (
       SELECT jsonb_agg(jsonb_build_object('unit', a.unit, 'actual_cost', trim_scale(a.actual_cost)::text) ORDER BY a.unit NULLS LAST)
       FROM upstream_amounts a WHERE a.level=u.level AND a.credential_id IS NOT DISTINCT FROM u.credential_id
         AND a.model IS NOT DISTINCT FROM u.model
     ) END,
     'mixed_units', COALESCE(u.mixed_units,false), 'stale', COALESCE(u.stale,false), 'source', u.source) AS upstream,
   COALESCE(l.student_credits, 0) * 0.01::numeric AS student,
   COALESCE(l.base_credits, 0) * 0.01::numeric AS base,
   CASE WHEN (u.unit IS NULL OR u.unit = 'CNY') AND NOT u.mixed_units THEN u.actual_cost END AS actual,
   COALESCE(u.stale,false) AS upstream_stale,
   COALESCE(l.tokens, 0) AS local_tokens,
   COALESCE(u.tokens, 0) AS upstream_tokens
 FROM local_summary l FULL JOIN upstream_summary u
   ON l.level = u.level AND l.model IS NOT DISTINCT FROM u.model AND l.credential_id IS NOT DISTINCT FROM u.credential_id
), amounts AS (
 SELECT p.*, jsonb_build_object(
   'student_yuan', trim_scale(student)::text, 'base_yuan', trim_scale(base)::text,
   'actual_yuan', CASE WHEN actual IS NOT NULL THEN trim_scale(actual)::text END,
   'margin_available', m.available,
   'margin_yuan', CASE WHEN m.available THEN trim_scale(student - actual)::text END,
   'margin_rate', CASE WHEN m.available THEN trim_scale(CASE WHEN student = 0 THEN 0 ELSE (student - actual) / student END)::text END) AS money
 FROM paired p CROSS JOIN day_summary d
 CROSS JOIN LATERAL (SELECT d.all_days_complete AND p.actual IS NOT NULL AND NOT p.upstream_stale AND
   (p.level <> 0 OR NOT EXISTS (SELECT 1 FROM selected_key_days k
     WHERE k.credential_id=p.credential_id AND k.models_covered IS DISTINCT FROM true)) AS available) m
), lines AS (
 SELECT a.level, a.model, a.credential_id, c.label, jsonb_build_object(
   'credential_id', a.credential_id, 'label', c.label, 'local', a.local, 'upstream', a.upstream, 'match_status', a.match_status,
   'completeness', a.completeness, 'school_absorbed_credits', a.school_absorbed_credits,
   'local_share', trim_scale(CASE WHEN t.local_tokens = 0 THEN 0 ELSE a.local_tokens / t.local_tokens END)::text,
   'upstream_share', trim_scale(CASE WHEN t.upstream_tokens = 0 THEN 0 ELSE a.upstream_tokens / t.upstream_tokens END)::text) || a.money AS item
 FROM amounts a CROSS JOIN amounts t
 LEFT JOIN provider_credentials c ON c.id = a.credential_id AND c.provider_id = $1
 WHERE t.level = 3
)
SELECT jsonb_build_object(
 'totals', (SELECT jsonb_build_object('local', local, 'upstream', upstream, 'money', money) FROM amounts WHERE level = 3),
 'models', COALESCE((SELECT jsonb_agg(jsonb_build_object('model', model) || item
   ORDER BY model, label NULLS LAST, credential_id) FROM lines WHERE level = 0), '[]'::jsonb),
 'keys', COALESCE((SELECT jsonb_agg(item ORDER BY label NULLS LAST, credential_id) FROM lines WHERE level = 1), '[]'::jsonb),
 'coverage', jsonb_build_object(
   'days_expected', (SELECT count(*) FROM coverage_days), 'days_present', (SELECT count(*) FROM coverage_days WHERE synced),
   'eligible_dates', (SELECT eligible_dates FROM day_summary), 'excluded_days', (SELECT excluded_days FROM day_summary),
   'comparable_days', (SELECT comparable_days FROM day_summary),
   'key_usage_days', (SELECT count(*) FROM selected_key_days),
   'key_usage_model_days', (SELECT count(*) FROM selected_key_days WHERE models_covered AND NOT stale),
   'stale_key_usage_days', (SELECT count(*) FROM selected_key_days WHERE stale),
   'missing_days', (SELECT missing_days FROM day_summary),
   'credentials', COALESCE((SELECT jsonb_agg(jsonb_build_object('credential_id', id, 'label', label,
     'expected_days', expected_days, 'synced_days', synced_days, 'missing_days', missing_days) ORDER BY label, id)
     FROM credential_coverage), '[]'::jsonb),
   'pending_calls', (SELECT pending_calls FROM local_summary WHERE level = 3),
   'unpriced_calls', (SELECT unpriced_calls FROM local_summary WHERE level = 3),
   'verified_matched_calls', (SELECT requests FROM verified_calls),
   'verified_matched_complete_token_calls', (SELECT complete_token_calls FROM verified_calls),
   'multiplier_mixed', (SELECT multiplier_mixed FROM local_summary WHERE level = 3)))`

func readReconciliation(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, period ReportPeriod, now time.Time) (ReconciliationReport, error) {
	return readReconciliationPage(ctx, tx, providerID, period, now, unmatchedScopeObserved, "")
}

func readReconciliationPage(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, period ReportPeriod, now time.Time,
	scope, cursor string) (ReconciliationReport, error) {
	var startDay any
	if period.Start != nil {
		startDay = period.Start.In(beijing).Format(dayLayout)
	}
	localNow := now.In(beijing)
	// 汇总和逐笔查询使用同一截止时间，游标在同一北京时间账日内有效。
	end := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, beijing).AddDate(0, 0, 1)
	period.End = end.UTC()
	var report ReconciliationReport
	readView := func(dates []string, view *ReconciliationView) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, reconciliationSQL, providerID, period.Start, period.End,
			startDay, end.Format(dayLayout), localNow.Format(dayLayout), now.UTC(), dates).Scan(&raw)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, view); err != nil {
			return err
		}
		view.describeCoverage()
		return nil
	}
	if err := readView(nil, &report.ReconciliationView); err != nil {
		return report, err
	}
	if dates := report.Coverage.EligibleDates; len(dates) > 0 {
		report.ComparableOnly = new(ReconciliationView)
		if err := readView(dates, report.ComparableOnly); err != nil {
			return report, err
		}
	}
	var dates []string
	if scope == unmatchedScopeComparable {
		dates = report.Coverage.EligibleDates
	}
	page, err := readUnmatchedUpstream(ctx, tx, providerID, period.Start, period.End, dates, scope, cursor)
	if err != nil {
		return report, err
	}
	report.UnmatchedUpstream = page.Rows
	report.UnmatchedTotal = page.Total
	report.UnmatchedNextCursor = page.NextCursor
	report.UnmatchedScope = scope
	report.ReportPeriod = period
	return report, nil
}

func (view *ReconciliationView) describeCoverage() {
	view.Coverage.Comparable = view.Totals.Money.MarginAvailable
	switch {
	case view.Coverage.SyncedDays == 0:
		view.Coverage.CoverageKind = "none"
	case view.Coverage.SyncedDays == view.Coverage.ExpectedDays:
		view.Coverage.CoverageKind = "full"
	default:
		view.Coverage.CoverageKind = "partial"
	}
}

func (s *Service) Reconciliation(ctx context.Context, providerID uuid.UUID, value string, now time.Time) (ReconciliationReport, error) {
	return s.ReconciliationPage(ctx, providerID, value, unmatchedScopeObserved, "", now)
}

func (s *Service) ReconciliationPage(ctx context.Context, providerID uuid.UUID, value, scope, cursor string, now time.Time) (ReconciliationReport, error) {
	if scope == "" {
		scope = unmatchedScopeObserved
	}
	if scope != unmatchedScopeObserved && scope != unmatchedScopeComparable {
		return ReconciliationReport{}, invalid("unmatched_scope 须为 observed 或 comparable")
	}
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
	report, err := readReconciliationPage(ctx, tx, providerID, period, now, scope, cursor)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ReconciliationReport{}, unavailable("读取北辰区间对账失败", err)
	}
	return report, nil
}
