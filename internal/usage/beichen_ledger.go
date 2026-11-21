package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PrepareDispatch records intent before sending anything billable upstream.
// The gateway must stop sending if this write fails.
func (s *Store) PrepareDispatch(ctx context.Context, callID uuid.UUID, route CallRoute) error {
	kind := "legacy"
	if route.Beichen {
		kind = "beichen"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gateway_calls SET
		beichen_state=CASE WHEN $2='beichen' THEN 'reserved' ELSE beichen_state END,
		beichen_school_multiplier=CASE WHEN $2='beichen' THEN CASE WHEN EXISTS (
			SELECT 1 FROM beichen_provision_channels school
			JOIN provider_credentials pc ON pc.id=school.credential_id
			WHERE school.credential_id=$3 AND pc.provider_id=$6
		) THEN 1.10 ELSE 1 END END,
		beichen_dispatch_kind=$2,beichen_dispatch_at=$5,
		beichen_dispatch_credential_id=$3,beichen_dispatch_model=NULLIF($4,''),
		reasoning_effort=CASE WHEN $2='beichen' THEN $7::text ELSE reasoning_effort END,
		cache_ttl=CASE WHEN $2='beichen' THEN NULLIF($8,'') ELSE cache_ttl END,
		lease_expires_at=clock_timestamp()+interval '15 minutes'
		WHERE id=$1 AND billing_state='pending'`, callID, kind, nullableUUID(route.CredentialID), route.UpstreamModelName,
		time.Now().UTC().Truncate(time.Microsecond), nullableUUID(route.ProviderID), route.ReasoningEffort, route.CacheTTL)
	return requireLedgerRow(result, err)
}

func requireLedgerRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("usage ledger row no longer available or reservation is inconsistent")
	}
	return err
}

// finishRoutedCall runs with the gateway call locked. A review releases the
// temporary hold once; a later terminal report can settle that same call.
func finishRoutedCall(ctx context.Context, tx *sql.Tx, callID uuid.UUID, success bool, outcome, category, message string, route CallRoute, metrics CallMetrics) error {
	var state, kind, model, oldOutcome string
	var hold, multiplier int64
	var dispatchAt sql.NullTime
	var credential uuid.NullUUID
	err := tx.QueryRowContext(ctx, `SELECT beichen_state,credit_milli,multiplier_milli,
		COALESCE(beichen_dispatch_kind,''),beichen_dispatch_at,beichen_dispatch_credential_id,
		COALESCE(beichen_dispatch_model,''),outcome FROM gateway_calls WHERE id=$1`, callID).
		Scan(&state, &hold, &multiplier, &kind, &dispatchAt, &credential, &model, &oldOutcome)
	if err != nil {
		return err
	}
	if state == "released" {
		return nil
	}
	if state == "charged" {
		// A recovery sweep may let a receipt settle before the terminal
		// response arrives. Preserve that observation without changing the debit
		// or replacing the counters used in an earlier gateway settlement.
		if !metrics.HasTokenUsage() {
			return nil
		}
		var captureLate bool
		err := tx.QueryRowContext(ctx, `SELECT usage_state IS DISTINCT FROM 'reported' AND EXISTS(
			SELECT 1 FROM beichen_call_receipts WHERE call_id=$1 AND applied
		) FROM gateway_calls WHERE id=$1`, callID).Scan(&captureLate)
		if err != nil || !captureLate {
			return err
		}
		return writeCallMetrics(ctx, tx, callID, metrics)
	}
	if state == "pending_review" {
		hold = 0
		if category == "recovered_after_crash" || category == "deactivated_with_request_in_flight" || route.DefinitelyNotSent {
			return nil
		}
	}
	legacyCharge := success && outcome == "succeeded" && kind == "legacy" && !route.Beichen && route.ProviderID != uuid.Nil
	next, billing, reason := "pending_review", "failed", "unconfirmed_dispatch"
	recoveredBeforeDispatch := !dispatchAt.Valid && (category == "recovered_after_crash" || category == "deactivated_with_request_in_flight")
	if recoveredBeforeDispatch || (!success && route.DefinitelyNotSent) {
		next, reason = "released", ""
	}
	if legacyCharge {
		next, billing, reason = "released", "charged", ""
	}
	var priceAt sql.NullTime
	if route.Beichen && kind == "beichen" && credential.Valid && credential.UUID == route.CredentialID &&
		model != "" && model == route.UpstreamModelName && dispatchAt.Valid &&
		!route.SentAt.Before(dispatchAt.Time) {
		priceAt = sql.NullTime{Time: route.SentAt, Valid: true}
		if next == "pending_review" {
			reason = "missing_terminal_usage"
			if success && category != "" {
				reason = category
			} else if metrics.State == UsagePartial {
				reason = "incomplete_token_breakdown"
			}
		}
	}
	if state == "pending_review" && oldOutcome == "succeeded" && outcome != "succeeded" {
		outcome = oldOutcome
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gateway_calls SET
		beichen_state=$2,billing_state=$3,beichen_review_reason=NULLIF($4,''),credit_milli=$5,
		outcome=$6,error_category=NULLIF($7,''),error_message=NULLIF($8,''),
		beichen_price_at=COALESCE($9,beichen_price_at),sent_at=COALESCE($9,sent_at),
		provider_id=COALESCE($10,provider_id),provider_name=COALESCE(NULLIF($11,''),provider_name),
		provider_credential_id=COALESCE($12,provider_credential_id),binding_id=COALESCE($13,binding_id),
		upstream_model_name=COALESCE(NULLIF($14,''),upstream_model_name),
		finalized_at=now(),lease_expires_at=NULL WHERE id=$1`, callID, next, billing, reason,
		int64(boolInt(legacyCharge))*multiplier, outcome, category, sanitize(message), priceAt,
		nullableUUID(route.ProviderID), route.ProviderName, nullableUUID(route.CredentialID),
		nullableUUID(route.BindingID), route.UpstreamModelName); err != nil {
		return err
	}
	if err = postLedgerBuckets(ctx, tx, callID, hold, "0"); err != nil {
		return err
	}
	if err = writeCallMetrics(ctx, tx, callID, metrics); err != nil {
		return err
	}
	if legacyCharge {
		milli := fmt.Sprint(multiplier)
		if err := postLedgerCharge(ctx, tx, callID, milli); err != nil {
			return err
		}
		return refreshLedgerTurn(ctx, tx, callID, milli, true)
	}
	if next == "released" {
		_, err = tx.ExecContext(ctx, `DELETE FROM monitored_inputs WHERE call_id=$1`, callID)
		if err != nil {
			return err
		}
		return refreshLedgerTurn(ctx, tx, callID, "0", false)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE monitored_inputs SET visible=true WHERE call_id=$1`, callID); err != nil {
		return err
	}
	if priceAt.Valid && success && category == "" && metrics.State == UsageReported {
		return settleBeichenReport(ctx, tx, callID, metrics)
	}
	return refreshLedgerTurn(ctx, tx, callID, "0", false)
}

// Use the same dispatch-time snapshot and credential validation as beichen_price_at.
// Applying the validity checks after LIMIT prevents a fallback to a superseded quote.
const beichenConditionQuoteJoin = `
	LEFT JOIN LATERAL (
		SELECT NULLIF(s.model_pricing -> c.beichen_dispatch_model,'null'::jsonb) AS terms,
			m.id AS multiplier_version,m.multiplier
		FROM (
			SELECT * FROM beichen_panel_snapshots
			WHERE credential_id=c.beichen_dispatch_credential_id
				AND effective_at<=COALESCE(c.beichen_price_at,c.sent_at)
			ORDER BY effective_at DESC LIMIT 1
		) s
		LEFT JOIN beichen_multiplier_versions m ON m.id=s.multiplier_version
			AND m.credential_id=s.credential_id AND m.effective_at<=s.effective_at
		WHERE COALESCE(c.beichen_price_at,c.sent_at)<s.expires_at AND NOT EXISTS (
			SELECT 1 FROM provider_credentials credential WHERE credential.id=s.credential_id
				AND credential.updated_at<=COALESCE(c.beichen_price_at,c.sent_at)
				AND NOT COALESCE(s.credential_ciphertext_sha256=sha256(credential.credential_ciphertext),
					s.credential_revision=credential.updated_at)
		)
	) quote ON true`

type beichenCharge struct {
	base, source      string
	upstreamVersion   sql.NullInt64
	multiplierVersion sql.NullInt64
	quoteMultiplier   sql.NullString
	tier              string
	snapshot, detail  json.RawMessage
}

const beichenBaseCreditsSQL = `coalesce(c.beichen_base_credits,(c.pricing_detail->>'base_credits')::numeric,c.beichen_credit_exact)`
const beichenBaseSourceSQL = `coalesce(c.beichen_base_source,c.pricing_detail->>'base_source')`

func settleBeichenReport(ctx context.Context, tx *sql.Tx, callID uuid.UUID, metrics CallMetrics) error {
	var school, upstream, effort sql.NullString
	var mode, protocol, ttl, missingRate, upstreamSource string
	var terms []byte
	var quoteVersion sql.NullInt64
	var quoteMultiplier sql.NullString
	var noFixedSchool bool
	var charge beichenCharge
	// 0042 的跟随费率仍是上游余额单位；仅手动费率已换成积分，须除以 100，避免多扣。
	err := tx.QueryRowContext(ctx, `SELECT cost.school::text,cost.upstream::text,
		p.mode,p.upstream_version,p.multiplier_version,COALESCE(version.source,''),
		num_nonnulls(p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)=0,
		c.protocol,c.reasoning_effort,COALESCE(c.cache_ttl,''),
		quote.terms,quote.multiplier_version,quote.multiplier::text,
		CASE
		  WHEN COALESCE($4::bigint,0)>0 AND p.upstream_cache_read IS NULL THEN 'missing_cache_read_price'
		  WHEN COALESCE($6::bigint,0)>0 AND p.upstream_cache_write IS NULL THEN 'missing_cache_write_price'
		  ELSE 'missing_price_or_token_breakdown' END
		FROM gateway_calls c
		CROSS JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,c.beichen_price_at) p
		LEFT JOIN beichen_price_versions version ON version.id=p.upstream_version
		CROSS JOIN LATERAL (SELECT
			beichen_token_credits(c.protocol,$2,$3,$4,$5,$6,
				p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)
				/CASE WHEN p.mode='manual' THEN 100 ELSE 1 END AS school,
			beichen_token_credits(c.protocol,$2,$3,$4,$5,$6,
				p.upstream_input,p.upstream_output,p.upstream_cache_read,p.upstream_cache_write) AS upstream
		) cost`+beichenConditionQuoteJoin+`
		WHERE c.id=$1 AND c.beichen_state='pending_review'
		  AND c.beichen_price_at IS NOT NULL
		FOR UPDATE OF c`, callID, metrics.PromptTokens, metrics.CompletionTokens,
		metrics.CacheHitTokens, metrics.CacheMissTokens, metrics.CacheWriteTokens).Scan(
		&school, &upstream, &mode, &charge.upstreamVersion, &charge.multiplierVersion, &upstreamSource, &noFixedSchool,
		&protocol, &effort, &ttl, &terms, &quoteVersion, &quoteMultiplier, &missingRate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	charge.source = "gateway_usage_price"
	if mode == "manual" && school.Valid {
		charge.source = "teacher_manual_price"
	} else if upstreamSource == "derived_from_usage" {
		charge.source = upstreamSource
	} else if mode == "manual" {
		charge.source = "teacher_manual_price"
	}
	base := school
	if !base.Valid {
		base = upstream
	}
	reason := ""
	manualPriced := mode == "manual" && school.Valid
	anthropicWrite := protocol == "anthropic" && metrics.CacheWriteTokens != nil && *metrics.CacheWriteTokens > 0
	cacheDuration := beichenCacheTTL(ttl)
	switch {
	case metrics.ClassifyBeichenUsage(protocol).State != UsageReported:
		base = sql.NullString{}
		reason = "incomplete_token_breakdown"
	// A complete teacher-set price has one fixed rate for all cache writes.
	case anthropicWrite && cacheDuration == "" && !manualPriced:
		base = sql.NullString{}
		reason = "unknown_cache_ttl"
	case len(terms) != 0 && !manualPriced && (noFixedSchool || isBeichenConditional(terms) || anthropicWrite):
		var observedEffort *string
		if effort.Valid {
			observedEffort = &effort.String
		}
		var evaluated beichenConditionalPrice
		evaluated, reason = evaluateBeichenConditional(terms, metrics, observedEffort, ttl, protocol)
		if reason == "" && !quoteMultiplier.Valid {
			reason = "missing_upstream_multiplier"
		}
		base = sql.NullString{String: evaluated.Credits, Valid: reason == ""}
		charge = beichenCharge{
			source: "gateway_usage_price", multiplierVersion: quoteVersion, quoteMultiplier: quoteMultiplier,
			tier: evaluated.Tier, snapshot: terms, detail: evaluated.Detail,
		}
	case anthropicWrite && cacheDuration != "5m" && !manualPriced:
		base = sql.NullString{}
		reason = "missing_cache_write_price"
	}
	if !base.Valid {
		_, err = tx.ExecContext(ctx, `UPDATE gateway_calls SET beichen_review_reason=CASE
			WHEN $4<>'' THEN $4
			WHEN $2::bigint IS NULL THEN 'missing_upstream_price'
			WHEN $3::bigint IS NULL AND EXISTS (SELECT 1 FROM beichen_price_versions WHERE id=$2 AND basis='base')
				THEN 'missing_upstream_multiplier'
			ELSE $5 END,
			beichen_upstream_price_version=$2,beichen_multiplier_version=$3 WHERE id=$1`,
			callID, charge.upstreamVersion, charge.multiplierVersion, reason, missingRate)
		if err != nil {
			return err
		}
		return refreshLedgerTurn(ctx, tx, callID, "0", false)
	}
	charge.base = base.String
	if !manualPriced && len(terms) != 0 {
		charge.detail = recordIgnoredBeichenPanelPrices(terms, charge.detail)
		if len(charge.detail) != 0 {
			charge.snapshot = terms
		}
	}
	return bookBeichenCharge(ctx, tx, callID, charge)
}

// Both terminal usage and verified receipts book the student's part once.
// The legacy base fields require base * school_multiplier = debit. A capped
// debit keeps its full base and source in pricing_detail for audit.
func bookBeichenCharge(ctx context.Context, tx *sql.Tx, callID uuid.UUID, charge beichenCharge) error {
	var milli string
	err := tx.QueryRowContext(ctx, `WITH price AS (
		SELECT $2::numeric * COALESCE($9::numeric,1) AS base,
			COALESCE(beichen_school_multiplier,1) AS school_multiplier,
			CASE WHEN student_cap_milli IS NOT NULL
				THEN GREATEST(student_cap_milli,0)*0.001::numeric END AS cap
		FROM gateway_calls WHERE id=$1 AND beichen_state='pending_review'
	), full_price AS (
		SELECT *,base*school_multiplier AS actual FROM price
	), amount AS (
		SELECT *,LEAST(actual,cap) AS student FROM full_price
	)
	UPDATE gateway_calls c SET beichen_state='charged',billing_state='charged',
		beichen_credit_exact=amount.student,credit_milli=trunc(amount.student*1000)::bigint,
		actual_credits=amount.actual,student_credits=amount.student,school_absorbed_credits=amount.actual-amount.student,
		beichen_base_credits=CASE WHEN amount.actual=amount.student THEN amount.base END,
		beichen_base_source=CASE WHEN amount.actual=amount.student THEN $3::text END,
		beichen_review_reason=NULL,beichen_upstream_price_version=$4,beichen_multiplier_version=$5,
		price_tier=COALESCE(NULLIF($6,''),c.price_tier),
		price_snapshot=COALESCE(NULLIF($7,'')::jsonb,c.price_snapshot),
		pricing_detail=CASE WHEN $8<>'' OR amount.actual>amount.student THEN
			COALESCE(NULLIF($8,'')::jsonb,c.pricing_detail,'{}'::jsonb) ||
			jsonb_build_object('base_credits',trim_scale(amount.base)::text,'base_source',$3::text,
				'school_multiplier',trim_scale(amount.school_multiplier)::text) ||
			CASE WHEN $9::numeric IS NOT NULL
				THEN jsonb_build_object('upstream_multiplier',trim_scale($9::numeric)::text)
				ELSE '{}'::jsonb END
			ELSE c.pricing_detail END
	FROM amount WHERE c.id=$1 AND c.beichen_state='pending_review'
	RETURNING (amount.student*1000)::text`, callID, charge.base, charge.source,
		charge.upstreamVersion, charge.multiplierVersion, charge.tier, string(charge.snapshot), string(charge.detail),
		charge.quoteMultiplier).Scan(&milli)
	if err != nil {
		return err
	}
	if err := postLedgerCharge(ctx, tx, callID, milli); err != nil {
		return err
	}
	return refreshLedgerTurn(ctx, tx, callID, milli, true)
}

func postLedgerBuckets(ctx context.Context, tx *sql.Tx, callID uuid.UUID, hold int64, milli string) error {
	for _, bucket := range []struct{ table, match string }{
		{"project_month_credit_buckets", "b.project_id=c.project_id"},
		{"project_member_month_credit_buckets", "b.project_id=c.project_id AND b.user_id=c.user_id"},
		{"api_key_month_credit_buckets", "b.api_key_id=c.api_key_id"},
	} {
		result, err := tx.ExecContext(ctx, `UPDATE `+bucket.table+` b SET
			pending_milli=b.pending_milli-$2,
			charged_milli=b.charged_milli+trunc(b.charged_milli_remainder+$3::numeric)::bigint,
			charged_milli_remainder=mod(b.charged_milli_remainder+$3::numeric,1),
			updated_at=now()
			FROM gateway_calls c WHERE c.id=$1 AND b.month=c.month AND `+bucket.match+` AND b.pending_milli >= $2`, callID, hold, milli)
		if err := requireLedgerRow(result, err); err != nil {
			return err
		}
	}
	return nil
}

func postLedgerCharge(ctx context.Context, tx *sql.Tx, callID uuid.UUID, milli string) error {
	if err := postLedgerBuckets(ctx, tx, callID, 0, milli); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_milli_remainder,
		charged_count,zero_cost_count,created_at,updated_at)
		SELECT gen_random_uuid(),month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,trunc($2::numeric)::bigint,mod($2::numeric,1),
		($2::numeric>0)::int,($2::numeric=0)::int,now(),now() FROM gateway_calls WHERE id=$1
		ON CONFLICT(month,project_id,user_id,api_key_id,model_id) DO UPDATE SET
		charged_milli=monthly_usage_cube.charged_milli+EXCLUDED.charged_milli+
			trunc(monthly_usage_cube.charged_milli_remainder+EXCLUDED.charged_milli_remainder)::bigint,
		charged_milli_remainder=mod(monthly_usage_cube.charged_milli_remainder+EXCLUDED.charged_milli_remainder,1),
		charged_count=monthly_usage_cube.charged_count+EXCLUDED.charged_count,
		zero_cost_count=monthly_usage_cube.zero_cost_count+EXCLUDED.zero_cost_count,updated_at=now()`, callID, milli); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE monitored_inputs SET visible=true WHERE call_id=$1`, callID); err != nil {
		return err
	}
	return nil
}

func refreshLedgerTurn(ctx context.Context, tx *sql.Tx, callID uuid.UUID, milli string, charged bool) error {
	var turn uuid.NullUUID
	var index int
	err := tx.QueryRowContext(ctx, `SELECT turn_id,call_index FROM gateway_calls WHERE id=$1`, callID).Scan(&turn, &index)
	if err != nil || !turn.Valid {
		return err
	}
	if err = applyTurnBilling(ctx, tx, turn.UUID, index, charged, "", 0); err != nil {
		return err
	}
	if !charged {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE gateway_call_turns SET
		credit_milli=credit_milli+trunc(charged_milli_remainder+$2::numeric)::bigint,
		charged_milli_remainder=mod(charged_milli_remainder+$2::numeric,1)
		WHERE id=$1`, turn.UUID, milli)
	return err
}
