package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ReportPeriod 的区间左闭右开；总计没有下界，截止于查询时刻。
type ReportPeriod struct {
	Range    string     `json:"range"`
	Start    *time.Time `json:"start"`
	End      time.Time  `json:"end"`
	Timezone string     `json:"timezone"`
}

func reportPeriod(value string, now time.Time) (ReportPeriod, error) {
	if value == "" {
		value = "all"
	}
	period := ReportPeriod{Range: value, End: now.UTC(), Timezone: "Asia/Shanghai"}
	local := now.In(beijing)
	var start time.Time
	switch value {
	case "day":
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijing)
		period.End = start.AddDate(0, 0, 1).UTC()
	case "month":
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, beijing)
		period.End = start.AddDate(0, 1, 0).UTC()
	case "all":
		return period, nil
	default:
		return ReportPeriod{}, invalid("range 须为 day、month 或 all")
	}
	start = start.UTC()
	period.Start = &start
	return period, nil
}

type UsageTotals struct {
	Credits                   string `json:"credits"`
	Tokens                    int64  `json:"tokens"`
	InputTokens               int64  `json:"input_tokens"`
	OutputTokens              int64  `json:"output_tokens"`
	CacheReadTokens           int64  `json:"cache_read_tokens"`
	CacheWriteTokens          int64  `json:"cache_write_tokens"`
	Calls                     int64  `json:"calls"`
	DispatchedCalls           int64  `json:"dispatched_calls"`
	SettledCalls              int64  `json:"settled_calls"`
	ZeroCostCalls             int64  `json:"zero_cost_calls"`
	PendingCalls              int64  `json:"pending_calls"`
	PendingReviewCalls        int64  `json:"pending_review_calls"`
	PendingReviewCredits      string `json:"pending_review_credits"`
	TokenMissingCalls         int64  `json:"token_missing_calls"`
	ChargedTokensMissingCalls int64  `json:"charged_tokens_missing_calls"`
	Students                  int64  `json:"students"`
	Keys                      int64  `json:"keys"`
}

type ModelUsageMetrics struct {
	UsageTotals
	CacheHitRate  *float64 `json:"cache_hit_rate"`
	TokensMissing int64    `json:"tokens_missing"`
	Completeness  string   `json:"completeness"`
}

type ModelUsageIdentity struct {
	Model           string     `json:"model"`
	ModelKey        string     `json:"model_key"`
	SchoolModelID   *uuid.UUID `json:"school_model_id"`
	SchoolModel     *string    `json:"school_model"`
	UpstreamModels  []string   `json:"upstream_models"`
	IdentitySources []string   `json:"identity_sources"`
}

type ModelUsageItem struct {
	ModelUsageIdentity
	ModelUsageMetrics
}

type ModelUsageKeyItem = ModelUsageItem

type ModelUsageStudent struct {
	UserID      uuid.UUID `json:"user_id"`
	UserName    string    `json:"user_name"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	ModelUsageMetrics
	Models []ModelUsageItem `json:"models"`
}

type ModelUsageKey struct {
	APIKeyID    uuid.UUID `json:"api_key_id"`
	APIKeyName  string    `json:"api_key_name"`
	UserID      uuid.UUID `json:"user_id"`
	UserName    string    `json:"user_name"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	ModelUsageMetrics
	Models []ModelUsageKeyItem `json:"models"`
}

type ModelUsageReport struct {
	ReportPeriod
	Totals   ModelUsageMetrics   `json:"totals"`
	Models   []ModelUsageItem    `json:"models"`
	Students []ModelUsageStudent `json:"students"`
	Keys     []ModelUsageKey     `json:"keys"`
}

// 学校用量和对账共用派发范围、渠道归属与 Token 分项。
const reportAttributionSQL = `
WITH attributed AS (
 SELECT ` + attributedColumnsSQL + `,
   c.project_id, c.project_name, c.user_id, c.user_name, c.api_key_id, c.api_key_name,
   c.beichen_school_multiplier, c.total_tokens, c.protocol, c.usage_state, c.prompt_tokens, c.tool_prompt_tokens`

const reportProviderIDSQL = `COALESCE(c.provider_id, (SELECT pc.provider_id FROM provider_credentials pc WHERE pc.id = a.credential_id))`

// 已知总量可以大于已归类分项。OpenAI 和 Gemini 的输入含缓存，不重复相加。
const reportCallsFromSQL = attributedFromSQL + `
   AND ($1::uuid IS NOT NULL OR EXISTS (
     SELECT 1 FROM providers p WHERE p.id = ` + reportProviderIDSQL + ` AND ` + beichenProvider + `
   ))
), report_tokens AS (
 SELECT a.*,
   num_nonnulls(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) AS token_parts,
   COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0) +
     COALESCE(cache_read_tokens, 0) + COALESCE(cache_write_tokens, 0) AS classified_tokens
 FROM attributed a
), report_totals AS (
 SELECT a.*, token_parts = 4 AND usage_state IS NOT DISTINCT FROM 'reported' AND
     (total_tokens IS NULL OR total_tokens = classified_tokens) AS tokens_complete,
   (token_parts > 0 OR total_tokens IS NOT NULL OR prompt_tokens IS NOT NULL) AS has_observed_tokens,
   GREATEST(classified_tokens, total_tokens,
     CASE WHEN protocol <> 'anthropic' AND prompt_tokens IS NOT NULL
       THEN prompt_tokens::numeric + COALESCE(tool_prompt_tokens,0) + COALESCE(output_tokens,0)
     END) AS tokens
 FROM report_tokens a
), report_calls AS (
 SELECT a.*,
   tokens - classified_tokens AS missing_tokens
 FROM report_totals a
)`

const reportCallsSQL = reportAttributionSQL + `,
   c.billing_state, c.credit_milli, c.unresolved_credit_milli,
   NULLIF(c.model_id, '00000000-0000-0000-0000-000000000000'::uuid) AS call_model_id,
   ` + reportProviderIDSQL + ` AS provider_id
` + reportCallsFromSQL

// 已保存的校内 UUID 即使找不到当前模型资料也继续使用。别名只补充缺少 UUID 的历史调用。
const reportModelIdentitySQL = `,
model_identities AS (
 SELECT a.*, COALESCE(a.call_model_id, target.id) AS school_model_id,
   CASE WHEN a.call_model_id IS NOT NULL THEN 'call_model_id'
        WHEN target.id IS NOT NULL THEN 'configured_alias'
        ELSE 'upstream_name' END AS identity_source
 FROM report_calls a ` + modelAliasJoinStartSQL + `CASE WHEN a.call_model_id IS NULL THEN a.provider_id END` + modelAliasJoinEndSQL + `
 LEFT JOIN models target ON target.model_id = alias.school_model::citext
), model_keys AS (
 SELECT a.*, m.model_id::text AS school_model,
   CASE WHEN a.school_model_id IS NOT NULL THEN 'school:' || a.school_model_id::text
        ELSE 'upstream:' || a.provider_id::text || ':' || lower(a.model) END AS model_key
 FROM model_identities a LEFT JOIN models m ON m.id = a.school_model_id
), report_models AS (
 SELECT a.*, first_value(model) OVER (PARTITION BY model_key ORDER BY sent_at DESC, model) AS model_label
 FROM model_keys a
)`

const tokenCompletenessSQL = `CASE WHEN bool_and(tokens_complete) THEN 'complete'
   WHEN bool_or(has_observed_tokens) THEN 'partial' ELSE 'missing' END`

const chargedTokensMissingCallsSQL = `count(*) FILTER (WHERE beichen_state = 'charged' AND NOT tokens_complete)`
const pendingReviewCallsSQL = `count(*) FILTER (WHERE beichen_state = 'pending_review')`
const settledCallSQL = `COALESCE(beichen_state, billing_state) = 'charged'`
const zeroCostSQL = `CASE WHEN beichen_state IS NULL THEN credit_milli = 0 ELSE beichen_credit_exact = 0 END`

const usageTotalsJSONSQL = `jsonb_build_object(
 'credits', trim_scale(COALESCE(sum(beichen_credit_exact) FILTER (WHERE beichen_state = 'charged'), 0))::text,
 'tokens', COALESCE(sum(tokens), 0),
 'input_tokens', COALESCE(sum(input_tokens), 0),
 'output_tokens', COALESCE(sum(output_tokens), 0),
 'cache_read_tokens', COALESCE(sum(cache_read_tokens), 0),
 'cache_write_tokens', COALESCE(sum(cache_write_tokens), 0), 'calls', count(*),
 'dispatched_calls', count(*), 'settled_calls', count(*) FILTER (WHERE ` + settledCallSQL + `),
 'zero_cost_calls', count(*) FILTER (WHERE ` + settledCallSQL + ` AND ` + zeroCostSQL + `),
 'pending_calls', ` + pendingReviewCallsSQL + `, 'pending_review_calls', ` + pendingReviewCallsSQL + `,
 'pending_review_credits', COALESCE(sum(unresolved_credit_milli) FILTER (WHERE beichen_state = 'pending_review'), 0)::text,
 'token_missing_calls', count(*) FILTER (WHERE NOT has_observed_tokens),
 'charged_tokens_missing_calls', ` + chargedTokensMissingCallsSQL + `,
 'students', count(DISTINCT user_id), 'keys', count(DISTINCT api_key_id))`

const modelUsageMetricsJSONSQL = usageTotalsJSONSQL + ` || jsonb_build_object(
 'cache_hit_rate', sum(cache_read_tokens) FILTER (WHERE tokens_complete) /
   NULLIF(sum(input_tokens + cache_read_tokens) FILTER (WHERE tokens_complete), 0),
 'tokens_missing', COALESCE(sum(missing_tokens), 0),
 'completeness', ` + tokenCompletenessSQL + `)`

const modelUsageIdentityJSONSQL = `jsonb_build_object(
 'model', min(model_label), 'model_key', model_key,
 'school_model_id', min(school_model_id::text), 'school_model', min(school_model),
 'upstream_models', array_agg(DISTINCT model ORDER BY model),
 'identity_sources', array_agg(DISTINCT identity_source ORDER BY identity_source))`

const modelUsageSQL = reportCallsSQL + reportModelIdentitySQL + `,
models AS (
 SELECT model_key, min(model_label) AS model, ` + modelUsageIdentityJSONSQL + ` || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_models GROUP BY model_key
), student_models AS (
 SELECT project_id, user_id, model_key, min(model_label) AS model, ` + modelUsageIdentityJSONSQL + ` || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_models GROUP BY project_id, user_id, model_key
), key_models AS (
 SELECT api_key_id, model_key, min(model_label) AS model, ` + modelUsageIdentityJSONSQL + ` || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_models GROUP BY api_key_id, model_key
), students AS (
 SELECT project_id, user_id, max(user_name) AS user_name, max(project_name) AS project_name,
   ` + modelUsageMetricsJSONSQL + ` AS metrics
 FROM report_calls GROUP BY project_id, user_id
), keys AS (
 SELECT api_key_id, max(api_key_name) AS api_key_name, user_id, max(user_name) AS user_name,
   project_id, max(project_name) AS project_name, ` + modelUsageMetricsJSONSQL + ` AS metrics
 FROM report_calls GROUP BY api_key_id, user_id, project_id
)
SELECT jsonb_build_object(
 'totals', (SELECT ` + modelUsageMetricsJSONSQL + ` FROM report_calls),
 'models', COALESCE((SELECT jsonb_agg(item ORDER BY model, model_key) FROM models), '[]'::jsonb),
 'students', COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'user_id', s.user_id, 'user_name', s.user_name, 'project_id', s.project_id, 'project_name', s.project_name,
   'models', (SELECT jsonb_agg(m.item ORDER BY m.model, m.model_key) FROM student_models m
     WHERE m.project_id = s.project_id AND m.user_id = s.user_id)) || s.metrics
   ORDER BY s.user_name, s.project_name, s.project_id, s.user_id) FROM students s), '[]'::jsonb),
 'keys', COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'api_key_id', k.api_key_id, 'api_key_name', k.api_key_name, 'user_id', k.user_id, 'user_name', k.user_name,
   'project_id', k.project_id, 'project_name', k.project_name,
   'models', (SELECT jsonb_agg(m.item ORDER BY m.model, m.model_key) FROM key_models m WHERE m.api_key_id = k.api_key_id)) || k.metrics
   ORDER BY k.api_key_name, k.api_key_id) FROM keys k), '[]'::jsonb))`

func readModelUsage(ctx context.Context, tx *sql.Tx, providerID *uuid.UUID, period ReportPeriod) (ModelUsageReport, error) {
	var report ModelUsageReport
	var raw []byte
	if err := tx.QueryRowContext(ctx, modelUsageSQL, providerID, period.Start, period.End).Scan(&raw); err != nil {
		return report, err
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, err
	}
	report.ReportPeriod = period
	return report, nil
}

func (s *Service) ModelUsage(ctx context.Context, providerID *uuid.UUID, value string, now time.Time) (ModelUsageReport, error) {
	period, err := reportPeriod(value, now)
	if err != nil {
		return ModelUsageReport{}, err
	}
	if providerID != nil && *providerID == uuid.Nil {
		return ModelUsageReport{}, invalid("需要有效的供应商 ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ModelUsageReport{}, unavailable("开启学校模型用量查询失败", err)
	}
	defer tx.Rollback()
	if providerID != nil {
		if err := checkProvider(ctx, tx, *providerID); err != nil {
			return ModelUsageReport{}, err
		}
	}
	report, err := readModelUsage(ctx, tx, providerID, period)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ModelUsageReport{}, unavailable("读取学校模型用量失败", err)
	}
	return report, nil
}
