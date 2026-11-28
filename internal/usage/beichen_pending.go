package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage/pendingaudit"
)

var errAuditTokenSplit = errors.New("unpriced terminal token split")

type pendingAuditBooking struct {
	maxCredits string // exact PostgreSQL numeric, before rounding the pending reserve
	detail     json.RawMessage
}

// CanAuditBeichenSplit admits terminal cache allocation gaps. Parser conflicts,
// interrupted streams and request/model review categories retain their own handling.
func (m CallMetrics) CanAuditBeichenSplit() bool {
	if m.State != UsagePartial || !unresolvedExposureEnabled() {
		return false
	}
	switch m.UsageReason {
	case "", "token_breakdown_unconfirmed", cacheWriteSubtotalOnlyReason:
		return true
	}
	return false
}

func pendingThreshold(name string, fallback float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return v
}

type pendingDecision struct {
	result   pendingaudit.Result
	adjusted CallMetrics
	booking  pendingAuditBooking
}

func auditPendingReport(ctx context.Context, tx *sql.Tx, callID uuid.UUID, protocol string, metrics CallMetrics, headroom int64) error {
	d, err := decidePendingReport(ctx, tx, callID, protocol, metrics)
	if err != nil {
		return err
	}
	return writePendingReport(ctx, tx, callID, d, headroom)
}

func writePendingReport(ctx context.Context, tx *sql.Tx, callID uuid.UUID, d pendingDecision, headroom int64) error {
	if d.booking.maxCredits != "" {
		if err := writePendingReserve(ctx, tx, callID, d.booking.maxCredits, headroom); err != nil {
			return err
		}
	}
	if !d.result.CanAutoSettle {
		return refreshLedgerTurn(ctx, tx, callID, "0", false)
	}
	return settleBeichenReport(ctx, tx, callID, d.adjusted, &d.booking)
}

func writePendingReserve(ctx context.Context, tx *sql.Tx, callID uuid.UUID, maxCredits string, headroom int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE gateway_calls SET unresolved_credit_milli=LEAST($3::bigint,1000,GREATEST(1,ceil($2::numeric)))::bigint
		WHERE id=$1 AND beichen_state='pending_review'`, callID, maxCredits, headroom)
	return err
}

func decidePendingReport(ctx context.Context, tx *sql.Tx, callID uuid.UUID, protocol string, metrics CallMetrics) (pendingDecision, error) {
	var d pendingDecision
	if protocol == string(modelbinding.AdapterGoogleGeminiV1beta) {
		// Gemini counts confirm the read and input split. A verified receipt
		// can explain cache creation independently of those counters.
		d.result.Class = pendingaudit.ClassUnknown
		d.result.Note = "Gemini 缓存写入需核对上游回执"
		return d, nil
	}
	in := pendingaudit.Input{
		Protocol: protocol, PromptTokens: metrics.PromptTokens, CompletionTokens: metrics.CompletionTokens,
		TotalTokens: metrics.TotalTokens, CacheReadTokens: metrics.CacheHitTokens, CacheWriteTokens: metrics.CacheWriteTokens,
	}
	var prior float64
	var ttl string
	// beichen_price_at returns millionths of an upstream yuan per million tokens.
	err := tx.QueryRowContext(ctx, `SELECT p.upstream_input*1e-12, p.upstream_output*1e-12,
		p.upstream_cache_read*1e-12, p.upstream_cache_write*1e-12,
		GREATEST(0.05, LEAST(0.95, COALESCE(h.share,0.5))), COALESCE(c.cache_ttl,'')
		FROM gateway_calls c
		CROSS JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,c.beichen_price_at) p
		CROSS JOIN LATERAL (
			SELECT sum(g.cache_hit_tokens)::numeric / nullif(sum(CASE WHEN g.protocol='anthropic'
				THEN g.prompt_tokens::numeric+g.cache_hit_tokens+g.cache_write_tokens ELSE g.prompt_tokens END),0) AS share
			FROM gateway_calls g WHERE g.user_id=c.user_id AND g.model_id=c.model_id AND g.protocol=c.protocol
				AND g.billing_state='charged' AND g.created_at >= c.beichen_price_at - interval '30 days'
				AND g.created_at < c.beichen_price_at AND COALESCE(g.finalized_at,g.created_at) <= c.beichen_price_at
				AND g.prompt_tokens IS NOT NULL AND g.cache_hit_tokens IS NOT NULL
				AND (g.protocol<>'anthropic' OR g.cache_write_tokens IS NOT NULL)
		) h WHERE c.id=$1`, callID).Scan(&in.InputRate, &in.OutputRate, &in.CacheReadRate, &in.CacheWriteRate, &prior, &ttl)
	if err != nil {
		return d, err
	}
	in.PriorReadShare = &prior
	result := pendingaudit.Analyze(in, pendingThreshold("NEBULA_PENDING_AUTO_SETTLE_YUAN", 0.001),
		pendingThreshold("NEBULA_PENDING_AUTO_SETTLE_RELATIVE", 0.01))
	d.result = result
	if result.Class == pendingaudit.ClassUnknown {
		return d, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT ($2::numeric*100*COALESCE(beichen_school_multiplier,2))::text
		FROM gateway_calls WHERE id=$1`, callID, strconv.FormatFloat(result.MaxYuan, 'g', -1, 64)).Scan(&d.booking.maxCredits)
	if err != nil {
		return d, err
	}
	if !result.CanAutoSettle {
		return d, nil
	}
	if strings.TrimSpace(os.Getenv("NEBULA_PENDING_AUTO_SETTLE")) == "0" {
		d.result.CanAutoSettle = false
		d.result.Note = "自动结算已关闭"
		return d, nil
	}
	adjusted := metrics
	adjusted.State, adjusted.UsageReason = UsageReported, ""
	adjusted.CacheHitTokens, adjusted.CacheWriteTokens = &result.RecommendedRead, &result.RecommendedWrite
	observedTTL := observedCacheTTL(metrics)
	if observedTTL == "" {
		observedTTL = beichenCacheTTL(ttl)
	}
	adjusted.CacheTTL = observedTTL
	adjusted = adjusted.ClassifyBeichenUsage(protocol)
	if metrics.UsageReason == cacheWriteSubtotalOnlyReason {
		// A single observed duration is a subtotal. Only use this split if all
		// recommended writes are accounted for at the observed duration.
		// Prefer the duration the response actually observed; the request TTL
		// can differ from the child that was reported.
		matches5m := observedTTL == "5m" && metrics.cacheWrite5m != nil && *metrics.cacheWrite5m == result.RecommendedWrite
		matches1h := observedTTL == "1h" && metrics.cacheWrite1h != nil && *metrics.cacheWrite1h == result.RecommendedWrite
		if !matches5m && !matches1h {
			d.result.CanAutoSettle = false
			d.result.Note = "推荐分项与原始缓存证据或协议不符"
			return d, nil
		}
	}
	if !adjusted.CanPriceBeichenUsage(protocol) {
		d.result.CanAutoSettle = false
		d.result.Note = "推荐分项与原始缓存证据或协议不符"
		return d, nil
	}
	d.booking.detail, err = json.Marshal(map[string]any{
		"auto_settled": true, "policy_version": "pending-audit-v1", "prior_read_share": prior,
		"min_yuan": result.MinYuan, "max_yuan": result.MaxYuan, "recommended_yuan": result.RecommendedYuan,
		"recommended_read_tokens": result.RecommendedRead, "recommended_write_tokens": result.RecommendedWrite,
	})
	if err != nil {
		return d, err
	}
	d.adjusted = adjusted
	return d, nil
}
