package beichen

import (
	"bytes"
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
	CredentialID         uuid.UUID       `json:"credential_id"`
	Model                string          `json:"model"`
	Mode                 string          `json:"mode"`
	SchoolStatus         string          `json:"school_status"`
	RouteReady           bool            `json:"route_ready"`
	PriceStatus          string          `json:"price_status"`
	PriceConditions      json.RawMessage `json:"price_conditions"`
	PriceConditionsExact map[string]any  `json:"price_conditions_exact,omitempty"`
	UpstreamRates        *DisplayRates   `json:"upstream_rates"`
	SchoolRates          *DisplayRates   `json:"school_rates"`
}

// The effective multiplier can yield fractions of a micro-unit. Decimal
// strings also preserve integral amounts above JavaScript's safe range.
type DisplayRates struct {
	Input      *string `json:"input"`
	Output     *string `json:"output"`
	CacheRead  *string `json:"cache_read"`
	CacheWrite *string `json:"cache_write"`
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
	TrialCredits             *string   `json:"trial_credits"`
	TrialUnpricedCalls       *int64    `json:"trial_unpriced_calls"`
	ChargedCredits           *string   `json:"charged_credits"`
	PendingReviewCalls       *int64    `json:"pending_review_calls"`
}

type Overview struct {
	WebLoginRequired bool              `json:"web_login_required,omitempty"`
	WebsiteSync      WebsiteSyncStatus `json:"website_sync"`
	ArchivedUsage    []ArchivedUsage   `json:"archived_usage"`
	Datasets         []Dataset         `json:"datasets"`
	Models           []Model           `json:"models"`
	ModelCatalog     []CatalogModel    `json:"model_catalog"`
	Comparison       []Comparison      `json:"comparison"`
}

type CatalogModel struct {
	CanEnable       bool             `json:"can_enable"`
	SchoolStatus    string           `json:"school_status"`
	SchoolModel     string           `json:"school_model,omitempty"`
	Model           string           `json:"model"`
	CredentialIDs   []uuid.UUID      `json:"credential_ids"`
	PreferredRoutes []PreferredRoute `json:"preferred_routes"`
}

type PreferredRoute struct {
	SchoolModel             string       `json:"school_model"`
	UpstreamModel           string       `json:"upstream_model"`
	ProviderName            string       `json:"provider_name"`
	CredentialLabel         *string      `json:"credential_label"`
	Adapter                 string       `json:"adapter"`
	ProviderID              uuid.UUID    `json:"provider_id"`
	CredentialID            *uuid.UUID   `json:"credential_id"`
	Priority                int          `json:"priority"`
	State                   string       `json:"state"`
	EffectiveMultiplier     *json.Number `json:"effective_multiplier"`
	EffectiveMultiplierText string       `json:"effective_multiplier_text,omitempty"`
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
	if s.webAccount != nil {
		view.WebLoginRequired = s.webAccount.humanVerification.Load()
	}
	if err = readDatasets(ctx, tx, providerID, &view); err == nil {
		err = readModels(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readPreferredRoutes(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readComparison(ctx, tx, providerID, start, &view)
	}
	if err == nil {
		view.ArchivedUsage, err = readArchivedUsage(ctx, tx, providerID, start)
	}
	if err == nil {
		view.WebsiteSync, err = readWebsiteSyncStatus(ctx, tx, providerID)
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
SELECT m.credential_id, m.model_id::text, p.mode,
  COALESCE(route.school_status, named.status, 'not_configured'),
  owner.status = 'active' AND c.status = 'active' AND COALESCE(route.binding_ready, false),
  CASE WHEN snap.expires_at > statement_timestamp()
         AND COALESCE(snap.credential_ciphertext_sha256 = sha256(c.credential_ciphertext), snap.credential_revision = c.updated_at)
       THEN snap.model_pricing -> (m.model_id::text) END,
  trim_scale(p.upstream_input)::text, trim_scale(p.upstream_output)::text,
  trim_scale(p.upstream_cache_read)::text, trim_scale(p.upstream_cache_write)::text,
  trim_scale(p.school_input * f.factor)::text, trim_scale(p.school_output * f.factor)::text,
  trim_scale(p.school_cache_read * f.factor)::text, trim_scale(p.school_cache_write * f.factor)::text
FROM provider_model_catalog m
JOIN provider_credentials c ON c.id = m.credential_id AND c.provider_id = m.provider_id
JOIN providers owner ON owner.id = c.provider_id
LEFT JOIN models named ON named.model_id = m.model_id
LEFT JOIN LATERAL (
  SELECT expires_at, credential_revision, credential_ciphertext_sha256, model_pricing FROM beichen_panel_snapshots
  WHERE credential_id = c.id AND effective_at <= statement_timestamp() ORDER BY effective_at DESC LIMIT 1
) snap ON true
LEFT JOIN LATERAL (
  SELECT school.status AS school_status, b.status = 'active' AS binding_ready
  FROM model_bindings b JOIN models school ON school.id = b.model_id
  WHERE b.provider_id = m.provider_id AND b.credential_id = m.credential_id
    AND lower(btrim(b.upstream_model_name)) = lower(m.model_id::text)
  ORDER BY (b.status = 'active') DESC, (school.status = 'active') DESC,
    (school.status = 'pending_configuration') DESC, b.priority, b.id
  LIMIT 1
) route ON true
CROSS JOIN LATERAL beichen_price_at(m.credential_id, m.model_id::text, statement_timestamp()) p
CROSS JOIN LATERAL (SELECT CASE WHEN p.mode = 'follow' THEN 100 ELSE 1 END AS factor) f
WHERE m.provider_id = $1 AND m.status = 'active'
ORDER BY m.credential_id, m.model_id`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model Model
		var upstream, school DisplayRates
		var terms []byte
		err := rows.Scan(&model.CredentialID, &model.Model, &model.Mode,
			&model.SchoolStatus, &model.RouteReady, &terms,
			&upstream.Input, &upstream.Output, &upstream.CacheRead, &upstream.CacheWrite,
			&school.Input, &school.Output, &school.CacheRead, &school.CacheWrite)
		if err != nil {
			return err
		}
		model.PriceStatus = "pending_confirmation"
		model.PriceConditions = terms
		if len(terms) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(terms))
			decoder.UseNumber()
			if err := decoder.Decode(&model.PriceConditionsExact); err != nil {
				return err
			}
			stringifyPriceNumbers(model.PriceConditionsExact)
		}
		if upstream.Input != nil && upstream.Output != nil {
			model.PriceStatus = "confirmed"
			model.UpstreamRates = &upstream
		} else if conditionalPanelPricing(terms) {
			model.PriceStatus = "conditional"
		}
		if school.Input != nil {
			model.SchoolRates = &school
		}
		view.Models = append(view.Models, model)
	}
	return rows.Err()
}

// Rates and tier boundaries can exceed JavaScript's numeric precision.
func stringifyPriceNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		return v.String()
	case map[string]any:
		for k, child := range v {
			v[k] = stringifyPriceNumbers(child)
		}
	case []any:
		for i, child := range v {
			v[i] = stringifyPriceNumbers(child)
		}
	}
	return value
}

func readPreferredRoutes(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	models, err := loadActivationCatalog(ctx, tx, providerID, "")
	if err != nil {
		return err
	}
	view.ModelCatalog = make([]CatalogModel, 0, len(models))
	for _, model := range models {
		view.ModelCatalog = append(view.ModelCatalog, model.overview())
	}
	return nil
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
			&item.UpstreamUsageSource, &item.TrialCredits, &item.ChargedCredits, &item.PendingReviewCalls)
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
  SELECT a.credential_id, a.model, a.sent_at, c.beichen_state, c.beichen_credit_exact,
    CASE WHEN c.usage_state = 'reported' THEN
      CASE WHEN c.cache_miss_tokens IS NOT NULL THEN c.cache_miss_tokens
           WHEN c.protocol = 'anthropic' THEN c.prompt_tokens::bigint
           WHEN c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
             AND cache.reads::numeric + cache.writes <= c.prompt_tokens
           THEN c.prompt_tokens::bigint - cache.reads - cache.writes
      END
    END AS input_tokens,
    CASE WHEN c.usage_state = 'reported' THEN c.completion_tokens::bigint END AS output_tokens,
    CASE WHEN c.usage_state = 'reported' THEN cache.reads END AS cache_read_tokens,
    CASE WHEN c.usage_state = 'reported' THEN cache.writes END AS cache_write_tokens
  FROM gateway_calls c
  CROSS JOIN LATERAL (SELECT
    COALESCE(c.cache_hit_tokens, CASE WHEN c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
      AND c.cache_miss_tokens::numeric + c.cache_write_tokens = c.prompt_tokens THEN 0::bigint END) AS reads,
    COALESCE(c.cache_write_tokens, CASE WHEN c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
      AND c.cache_miss_tokens::numeric + c.cache_hit_tokens = c.prompt_tokens THEN 0::bigint END) AS writes
  ) cache
  CROSS JOIN LATERAL (SELECT
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN c.beichen_dispatch_credential_id ELSE c.provider_credential_id END AS credential_id,
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN c.beichen_dispatch_model ELSE c.upstream_model_name END AS model,
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN COALESCE(c.beichen_price_at, c.beichen_dispatch_at) ELSE c.sent_at END AS sent_at
  ) a
  WHERE (c.provider_id = $1 OR (c.provider_id IS NULL AND c.beichen_dispatch_kind = 'beichen' AND EXISTS (
    SELECT 1 FROM provider_credentials pc WHERE pc.id = a.credential_id AND pc.provider_id = $1
  ))) AND a.sent_at >= $2 AND a.sent_at < $3
    AND a.credential_id IS NOT NULL AND a.model <> ''
    AND (c.beichen_dispatch_kind = 'beichen' OR c.binding_id IS NOT NULL) AND c.outcome <> 'pending'
), priced_calls AS (
  SELECT a.*,
    CASE WHEN cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL THEN
      beichen_weighted_tokens(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
      p.upstream_input, p.upstream_output, p.upstream_cache_read, p.upstream_cache_write) END AS estimated_weight,
    CASE WHEN cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL THEN
      beichen_weighted_tokens(input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
      p.school_input, p.school_output, p.school_cache_read, p.school_cache_write)
      * CASE WHEN p.mode = 'follow' THEN 100 ELSE 1 END END AS trial_weight
  FROM attributed a CROSS JOIN LATERAL beichen_price_at(a.credential_id, a.model, a.sent_at) p
), local AS (
  SELECT credential_id, model, count(*) AS requests,
    CASE WHEN count(input_tokens) = count(*) THEN sum(input_tokens) END AS input_tokens,
    CASE WHEN count(output_tokens) = count(*) THEN sum(output_tokens) END AS output_tokens,
    CASE WHEN count(cache_read_tokens) = count(*) THEN sum(cache_read_tokens) END AS cache_read_tokens,
    CASE WHEN count(cache_write_tokens) = count(*) THEN sum(cache_write_tokens) END AS cache_write_tokens,
    CASE WHEN count(estimated_weight) = count(*) THEN round(sum(estimated_weight) / 1000000) END AS estimated_micros,
    round(sum(trial_weight) / 1000000000) AS trial_credit_milli,
    trim_scale(sum(trial_weight) * 0.000000000001::numeric)::text AS trial_credits,
    trim_scale(sum(beichen_credit_exact) FILTER (WHERE beichen_state = 'charged'))::text AS charged_credits,
    count(*) FILTER (WHERE beichen_state = 'pending_review') AS pending_review_calls,
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
  COALESCE(u.source, CASE WHEN e.credential_id IS NOT NULL THEN 'usage' END), l.trial_credits,
  l.charged_credits, l.pending_review_calls
FROM local l FULL JOIN upstream u USING (credential_id, model)
LEFT JOIN confirmed_empty e ON e.credential_id = l.credential_id
ORDER BY 1, 2`
