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
	PendingCalls              int64  `json:"pending_calls"`
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

type ModelUsageItem struct {
	Model string `json:"model"`
	ModelUsageMetrics
}

type ModelUsageKeyItem struct {
	Model string `json:"model"`
	ModelUsageMetrics
}

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

// attributed 保留日对账的发送时刻及渠道归属；只在全校查询时额外排除其他供应商。
// 区间对账和学校用量都按已归一化分项累计，扣款状态独立于 Token 回报。
const reportAttributionSQL = `
WITH attributed AS (
 SELECT ` + attributedColumnsSQL + `,
   c.project_id, c.project_name, c.user_id, c.user_name, c.api_key_id, c.api_key_name,
   c.beichen_school_multiplier, c.total_tokens, c.protocol, c.usage_state, c.prompt_tokens, c.tool_prompt_tokens`

// 已知总量可以大于已归类分项。OpenAI 和 Gemini 的输入含缓存，不重复相加。
const reportCallsFromSQL = attributedFromSQL + `
   AND ($1::uuid IS NOT NULL OR EXISTS (
     SELECT 1 FROM providers p
     WHERE p.id = COALESCE(c.provider_id, (SELECT pc.provider_id FROM provider_credentials pc WHERE pc.id = a.credential_id))
       AND ` + beichenProvider + `
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

const reportCallsSQL = reportAttributionSQL + reportCallsFromSQL

const tokenCompletenessSQL = `CASE WHEN bool_and(tokens_complete) THEN 'complete'
   WHEN bool_or(token_parts > 0 OR total_tokens IS NOT NULL OR prompt_tokens IS NOT NULL) THEN 'partial' ELSE 'missing' END`

const chargedTokensMissingCallsSQL = `count(*) FILTER (WHERE beichen_state = 'charged' AND NOT tokens_complete)`

const usageTotalsJSONSQL = `jsonb_build_object(
 'credits', trim_scale(COALESCE(sum(beichen_credit_exact) FILTER (WHERE beichen_state = 'charged'), 0))::text,
 'tokens', COALESCE(sum(tokens), 0),
 'input_tokens', COALESCE(sum(input_tokens), 0),
 'output_tokens', COALESCE(sum(output_tokens), 0),
 'cache_read_tokens', COALESCE(sum(cache_read_tokens), 0),
 'cache_write_tokens', COALESCE(sum(cache_write_tokens), 0), 'calls', count(*),
 'pending_calls', count(*) FILTER (WHERE beichen_state = 'pending_review'),
 'charged_tokens_missing_calls', ` + chargedTokensMissingCallsSQL + `,
 'students', count(DISTINCT user_id), 'keys', count(DISTINCT api_key_id))`

const modelUsageMetricsJSONSQL = usageTotalsJSONSQL + ` || jsonb_build_object(
 'cache_hit_rate', sum(cache_read_tokens) FILTER (WHERE tokens_complete) /
   NULLIF(sum(input_tokens + cache_read_tokens) FILTER (WHERE tokens_complete), 0),
 'tokens_missing', COALESCE(sum(missing_tokens), 0),
 'completeness', ` + tokenCompletenessSQL + `)`

const modelUsageSQL = reportCallsSQL + `,
models AS (
 SELECT model, jsonb_build_object('model', model) || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_calls GROUP BY model
), student_models AS (
 SELECT project_id, user_id, model, jsonb_build_object('model', model) || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_calls GROUP BY project_id, user_id, model
), key_models AS (
 SELECT api_key_id, model, jsonb_build_object('model', model) || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM report_calls GROUP BY api_key_id, model
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
 'models', COALESCE((SELECT jsonb_agg(item ORDER BY model) FROM models), '[]'::jsonb),
 'students', COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'user_id', s.user_id, 'user_name', s.user_name, 'project_id', s.project_id, 'project_name', s.project_name,
   'models', (SELECT jsonb_agg(m.item ORDER BY m.model) FROM student_models m
     WHERE m.project_id = s.project_id AND m.user_id = s.user_id)) || s.metrics
   ORDER BY s.user_name, s.project_name, s.project_id, s.user_id) FROM students s), '[]'::jsonb),
 'keys', COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'api_key_id', k.api_key_id, 'api_key_name', k.api_key_name, 'user_id', k.user_id, 'user_name', k.user_name,
   'project_id', k.project_id, 'project_name', k.project_name,
   'models', (SELECT jsonb_agg(m.item ORDER BY m.model) FROM key_models m WHERE m.api_key_id = k.api_key_id)) || k.metrics
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
