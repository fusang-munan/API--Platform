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
	Credits          string `json:"credits"`
	Tokens           int64  `json:"tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Calls            int64  `json:"calls"`
	PendingCalls     int64  `json:"pending_calls"`
	Students         int64  `json:"students"`
	Keys             int64  `json:"keys"`
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
	UsageTotals
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
	UsageTotals
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
// 缺少任一缓存分项时无法确认总 Token，整笔不计入 Token 汇总，调用数仍保留。
const reportAttributionSQL = `
WITH attributed AS (
 SELECT ` + attributedColumnsSQL + `,
   c.project_id, c.project_name, c.user_id, c.user_name, c.api_key_id, c.api_key_name,
   c.beichen_base_credits, c.beichen_school_multiplier`

const reportCallsFromSQL = attributedFromSQL + `
   AND ($1::uuid IS NOT NULL OR EXISTS (
     SELECT 1 FROM providers p
     WHERE p.id = COALESCE(c.provider_id, (SELECT pc.provider_id FROM provider_credentials pc WHERE pc.id = a.credential_id))
       AND ` + beichenProvider + `
   ))
), report_calls AS (
 SELECT a.*,
   beichen_state IS DISTINCT FROM 'pending_review' AND input_tokens IS NOT NULL
     AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL AS tokens_reported,
   CASE WHEN beichen_state IS DISTINCT FROM 'pending_review' THEN
     COALESCE(input_tokens::numeric + output_tokens + cache_read_tokens + cache_write_tokens, 0)
   ELSE 0::numeric END AS tokens
 FROM attributed a
)`

const reportCallsSQL = reportAttributionSQL + reportCallsFromSQL

const usageTotalsJSONSQL = `jsonb_build_object(
 'credits', trim_scale(COALESCE(sum(beichen_credit_exact) FILTER (WHERE beichen_state = 'charged'), 0))::text,
 'tokens', COALESCE(sum(tokens), 0),
 'input_tokens', COALESCE(sum(input_tokens) FILTER (WHERE tokens_reported), 0),
 'output_tokens', COALESCE(sum(output_tokens) FILTER (WHERE tokens_reported), 0),
 'cache_read_tokens', COALESCE(sum(cache_read_tokens) FILTER (WHERE tokens_reported), 0),
 'cache_write_tokens', COALESCE(sum(cache_write_tokens) FILTER (WHERE tokens_reported), 0), 'calls', count(*),
 'pending_calls', count(*) FILTER (WHERE beichen_state = 'pending_review'),
 'students', count(DISTINCT user_id), 'keys', count(DISTINCT api_key_id))`

const modelUsageMetricsJSONSQL = usageTotalsJSONSQL + ` || jsonb_build_object(
 'cache_hit_rate', sum(cache_read_tokens) FILTER (WHERE tokens_reported) /
   NULLIF(sum(input_tokens::numeric + cache_read_tokens) FILTER (WHERE tokens_reported), 0),
 'tokens_missing', COALESCE(sum(missing_tokens), 0),
 'completeness', CASE
   WHEN count(*) FILTER (WHERE tokens_reported) = 0 THEN 'missing'
   WHEN bool_and(tokens_reported) THEN 'complete'
   ELSE 'partial' END)`

// 待核调用优先采用已归一化的 Token。分项不全或中断时，保留已知的总量或可确定的部分。
// Anthropic 的输入不含缓存，其他协议只取已知的输入与缓存合计中的较大值，避免重复计数。
const modelUsageSQL = reportAttributionSQL + `,
   c.total_tokens, c.protocol, c.prompt_tokens, c.completion_tokens,
   c.cache_hit_tokens, c.cache_miss_tokens, c.cache_write_tokens AS raw_cache_write_tokens
` + reportCallsFromSQL + `,
usage_calls AS (
 SELECT model, user_id, user_name, project_id, project_name, api_key_id, api_key_name,
   beichen_state, beichen_credit_exact, tokens, tokens_reported,
   input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
   CASE WHEN NOT tokens_reported THEN
   COALESCE(input_tokens::numeric + output_tokens + cache_read_tokens + cache_write_tokens,
     GREATEST(total_tokens,
       CASE WHEN protocol = 'anthropic' THEN
         COALESCE(cache_miss_tokens, prompt_tokens, 0)::numeric + COALESCE(cache_hit_tokens, 0) + COALESCE(raw_cache_write_tokens, 0)
       ELSE GREATEST(prompt_tokens,
         COALESCE(cache_miss_tokens, 0)::numeric + COALESCE(cache_hit_tokens, 0) + COALESCE(raw_cache_write_tokens, 0))
       END + COALESCE(completion_tokens, 0))) END AS missing_tokens
 FROM report_calls
),
models AS (
 SELECT model, jsonb_build_object('model', model) || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM usage_calls GROUP BY model
), student_models AS (
 SELECT project_id, user_id, model, jsonb_build_object('model', model) || ` + modelUsageMetricsJSONSQL + ` AS item
 FROM usage_calls GROUP BY project_id, user_id, model
), key_models AS (
 SELECT api_key_id, model, jsonb_build_object('model', model) || ` + usageTotalsJSONSQL + ` AS item
 FROM usage_calls GROUP BY api_key_id, model
), students AS (
 SELECT project_id, user_id, max(user_name) AS user_name, max(project_name) AS project_name,
   ` + modelUsageMetricsJSONSQL + ` AS metrics
 FROM usage_calls GROUP BY project_id, user_id
), keys AS (
 SELECT api_key_id, max(api_key_name) AS api_key_name, user_id, max(user_name) AS user_name,
   project_id, max(project_name) AS project_name, ` + usageTotalsJSONSQL + ` AS metrics
 FROM usage_calls GROUP BY api_key_id, user_id, project_id
)
SELECT jsonb_build_object(
 'totals', (SELECT ` + modelUsageMetricsJSONSQL + ` FROM usage_calls),
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
