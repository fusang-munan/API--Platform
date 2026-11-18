package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type Dataset struct {
	CredentialID        uuid.UUID    `json:"credential_id"`
	Kind                string       `json:"kind"`
	LastSuccess         *time.Time   `json:"last_success"`
	LastError           *string      `json:"last_error"`
	EffectiveMultiplier *json.Number `json:"effective_multiplier"`
}

type Model struct {
	CredentialID  uuid.UUID `json:"credential_id"`
	Model         string    `json:"model"`
	Mode          string    `json:"mode"`
	UpstreamRates *Rates    `json:"upstream_rates"`
	SchoolRates   *Rates    `json:"school_rates"`
}

type Comparison struct {
	CredentialID             uuid.UUID `json:"credential_id"`
	Model                    string    `json:"model"`
	LocalRequests            *int64    `json:"local_requests"`
	UpstreamRequests         *int64    `json:"upstream_requests"`
	LocalInputTokens         *int64    `json:"local_input_tokens"`
	UpstreamInputTokens      *int64    `json:"upstream_input_tokens"`
	LocalOutputTokens        *int64    `json:"local_output_tokens"`
	UpstreamOutputTokens     *int64    `json:"upstream_output_tokens"`
	LocalCacheReadTokens     *int64    `json:"local_cache_read_tokens"`
	UpstreamCacheReadTokens  *int64    `json:"upstream_cache_read_tokens"`
	LocalCacheWriteTokens    *int64    `json:"local_cache_write_tokens"`
	UpstreamCacheWriteTokens *int64    `json:"upstream_cache_write_tokens"`
	EstimatedUpstreamMicros  *int64    `json:"estimated_upstream_micros"`
	BilledUpstreamMicros     *int64    `json:"billed_upstream_micros"`
	UpstreamUsageSource      *string   `json:"upstream_usage_source"`
	TrialCreditMilli         *int64    `json:"trial_credit_milli"`
	TrialUnpricedCalls       *int64    `json:"trial_unpriced_calls"`
}

type Overview struct {
	Datasets   []Dataset    `json:"datasets"`
	Models     []Model      `json:"models"`
	Comparison []Comparison `json:"comparison"`
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkProvider(ctx context.Context, db rowQueryer, providerID uuid.UUID) error {
	var found uuid.UUID
	err := db.QueryRowContext(ctx, "SELECT p.id FROM providers p WHERE p.id = $1 AND "+beichenProvider, providerID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "北辰供应商不存在")
	}
	if err != nil {
		return unavailable("读取北辰供应商失败", err)
	}
	return nil
}

func (s *Service) Overview(ctx context.Context, providerID uuid.UUID, day string) (Overview, error) {
	if providerID == uuid.Nil {
		return Overview{}, invalid("需要供应商 ID")
	}
	if day == "" {
		day = s.now().In(beijing).Format(dayLayout)
	}
	start, err := dayStart(day)
	if err != nil {
		return Overview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Overview{}, unavailable("开启北辰概览查询失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, providerID); err != nil {
		return Overview{}, err
	}
	view := Overview{Datasets: []Dataset{}, Models: []Model{}, Comparison: []Comparison{}}
	if err = readDatasets(ctx, tx, providerID, &view); err == nil {
		err = readModels(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readComparison(ctx, tx, providerID, start, &view)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return Overview{}, unavailable("读取北辰概览失败", err)
	}
	return view, nil
}

func readDatasets(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, kinds.kind, d.last_success, d.last_error, CASE WHEN kinds.kind = 'billing' THEN (
  SELECT m.multiplier::text FROM beichen_multiplier_versions m WHERE m.credential_id = c.id
  ORDER BY effective_at DESC, id DESC LIMIT 1
) END
FROM provider_credentials c
CROSS JOIN (VALUES ('catalog'), ('billing'), ('prices'), ('usage')) AS kinds(kind)
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = kinds.kind
WHERE c.provider_id = $1
ORDER BY c.label, c.id, kinds.kind`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Dataset
		var multiplier *string
		if err := rows.Scan(&item.CredentialID, &item.Kind, &item.LastSuccess, &item.LastError, &multiplier); err != nil {
			return err
		}
		if multiplier != nil {
			value := json.Number(*multiplier)
			item.EffectiveMultiplier = &value
		}
		if item.LastSuccess != nil {
			utc := item.LastSuccess.UTC()
			item.LastSuccess = &utc
		}
		view.Datasets = append(view.Datasets, item)
	}
	return rows.Err()
}

func readModels(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	rows, err := tx.QueryContext(ctx, `
WITH credential_models AS (
  SELECT credential_id, model_id::text AS model FROM provider_model_catalog
  WHERE provider_id = $1 AND status = 'active'
  UNION
  SELECT v.credential_id, v.model FROM beichen_price_versions v
  JOIN provider_credentials c ON c.id = v.credential_id WHERE c.provider_id = $1
)
SELECT m.credential_id, m.model, p.mode,
  round(p.upstream_input)::bigint, round(p.upstream_output)::bigint,
  round(p.upstream_cache_read)::bigint, round(p.upstream_cache_write)::bigint,
  round(p.school_input)::bigint, round(p.school_output)::bigint,
  round(p.school_cache_read)::bigint, round(p.school_cache_write)::bigint
FROM credential_models m CROSS JOIN LATERAL beichen_price_at(m.credential_id, m.model, statement_timestamp()) p
ORDER BY m.credential_id, m.model`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model Model
		var upstream, school Rates
		err := rows.Scan(&model.CredentialID, &model.Model, &model.Mode,
			&upstream.Input, &upstream.Output, &upstream.CacheRead, &upstream.CacheWrite,
			&school.Input, &school.Output, &school.CacheRead, &school.CacheWrite)
		if err != nil {
			return err
		}
		if upstream.Input != nil {
			model.UpstreamRates = &upstream
		}
		if school.Input != nil {
			model.SchoolRates = &school
		}
		view.Models = append(view.Models, model)
	}
	return rows.Err()
}

func readComparison(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, start time.Time, view *Overview) error {
	rows, err := tx.QueryContext(ctx, comparisonSQL, providerID, start, start.AddDate(0, 0, 1), start.Format(dayLayout))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Comparison
		err := rows.Scan(&item.CredentialID, &item.Model, &item.LocalRequests, &item.UpstreamRequests,
			&item.LocalInputTokens, &item.UpstreamInputTokens, &item.LocalOutputTokens, &item.UpstreamOutputTokens,
			&item.LocalCacheReadTokens, &item.UpstreamCacheReadTokens, &item.LocalCacheWriteTokens, &item.UpstreamCacheWriteTokens,
			&item.EstimatedUpstreamMicros, &item.BilledUpstreamMicros, &item.TrialCreditMilli, &item.TrialUnpricedCalls,
			&item.UpstreamUsageSource)
		if err != nil {
			return err
		}
		view.Comparison = append(view.Comparison, item)
	}
	return rows.Err()
}

// Upstream input_tokens is uncached. Anthropic reports it directly; OpenAI
// chat/responses report inclusive input, so require cache details or a
// separately reported miss count before treating it as uncached.
const comparisonSQL = `
WITH attributed AS (
  SELECT c.provider_credential_id AS credential_id, c.upstream_model_name AS model, c.sent_at,
    CASE WHEN c.usage_state = 'reported' THEN
      CASE WHEN c.cache_miss_tokens IS NOT NULL THEN c.cache_miss_tokens
           WHEN c.protocol = 'anthropic' THEN c.prompt_tokens::bigint
           WHEN c.protocol IN ('openai_compatible','openai_responses')
             AND c.cache_hit_tokens IS NOT NULL
             AND c.cache_hit_tokens <= c.prompt_tokens
             AND COALESCE(c.cache_write_tokens, 0) <= c.prompt_tokens::bigint - c.cache_hit_tokens
           THEN c.prompt_tokens::bigint - c.cache_hit_tokens - COALESCE(c.cache_write_tokens, 0)
      END
    END AS input_tokens,
    CASE WHEN c.usage_state = 'reported' THEN c.completion_tokens::bigint END AS output_tokens,
    CASE WHEN c.usage_state = 'reported' THEN c.cache_hit_tokens END AS cache_read_tokens,
    CASE WHEN c.usage_state = 'reported' THEN
      CASE WHEN c.cache_write_tokens IS NOT NULL THEN c.cache_write_tokens
           WHEN c.protocol IN ('openai_compatible','openai_responses') AND
             (c.prompt_tokens IS NOT NULL OR c.cache_miss_tokens IS NOT NULL) THEN 0::bigint
      END
    END AS cache_write_tokens
  FROM gateway_calls c
  WHERE c.provider_id = $1 AND c.sent_at >= $2 AND c.sent_at < $3
    AND c.provider_credential_id IS NOT NULL AND c.binding_id IS NOT NULL AND c.sent_at IS NOT NULL AND c.outcome <> 'pending'
    AND c.upstream_model_name <> ''
), priced_calls AS (
  SELECT a.*,
    beichen_weighted_tokens(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
      p.upstream_input, p.upstream_output, p.upstream_cache_read, p.upstream_cache_write) AS estimated_weight,
    beichen_weighted_tokens(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
      p.school_input, p.school_output, p.school_cache_read, p.school_cache_write) AS trial_weight
  FROM attributed a CROSS JOIN LATERAL beichen_price_at(a.credential_id, a.model, a.sent_at) p
), local AS (
  SELECT credential_id, model, count(*) AS requests,
    CASE WHEN count(input_tokens) = count(*) THEN sum(input_tokens) END AS input_tokens,
    CASE WHEN count(output_tokens) = count(*) THEN sum(output_tokens) END AS output_tokens,
    CASE WHEN count(cache_read_tokens) = count(*) THEN sum(cache_read_tokens) END AS cache_read_tokens,
    CASE WHEN count(cache_write_tokens) = count(*) THEN sum(cache_write_tokens) END AS cache_write_tokens,
    CASE WHEN count(estimated_weight) = count(*) THEN round(sum(estimated_weight) / 1000000) END AS estimated_micros,
    round(sum(trial_weight) / 1000000000) AS trial_credit_milli,
    count(*) FILTER (WHERE trial_weight IS NULL) AS unpriced_calls
  FROM priced_calls
  GROUP BY credential_id, model
), upstream AS (
  SELECT * FROM beichen_daily_usage WHERE provider_id = $1 AND day = $4::text::date
), confirmed_empty AS (
  SELECT credential_id FROM beichen_usage_days WHERE provider_id = $1 AND day = $4::text::date AND empty
)
SELECT COALESCE(l.credential_id, u.credential_id), COALESCE(l.model, u.model), l.requests,
  CASE WHEN u.credential_id IS NOT NULL THEN u.requests WHEN e.credential_id IS NOT NULL THEN 0::bigint END,
  CASE WHEN l.input_tokens <= 9223372036854775807 THEN l.input_tokens::bigint END, u.input_tokens,
  CASE WHEN l.output_tokens <= 9223372036854775807 THEN l.output_tokens::bigint END, u.output_tokens,
  CASE WHEN l.cache_read_tokens <= 9223372036854775807 THEN l.cache_read_tokens::bigint END, u.cache_read_tokens,
  CASE WHEN l.cache_write_tokens <= 9223372036854775807 THEN l.cache_write_tokens::bigint END, u.cache_write_tokens,
  CASE WHEN l.estimated_micros <= 9223372036854775807 THEN l.estimated_micros::bigint END, u.billed_upstream_micros,
  CASE WHEN l.trial_credit_milli <= 9223372036854775807 THEN l.trial_credit_milli::bigint END, l.unpriced_calls,
  COALESCE(u.source, CASE WHEN e.credential_id IS NOT NULL THEN 'usage' END)
FROM local l FULL JOIN upstream u USING (credential_id, model)
LEFT JOIN confirmed_empty e ON e.credential_id = l.credential_id
ORDER BY 1, 2`
