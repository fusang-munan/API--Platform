package usage

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type BeichenReceipt struct {
	ResponseID uuid.UUID
	RowID      int64
	APIKeyID   int64
	Model      string
	ActualCost string
	OccurredAt time.Time

	// InputTokens counts uncached input only, independently of read/write.
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
}

var beichenReceiptCost = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,64})?$`)

// RecordBeichenReceiptID accepts a UUID observed in a trusted HTTP/SSE response.
// The gateway must call it before finalization, including on disconnects.
func (s *Store) RecordBeichenReceiptID(ctx context.Context, callID, credentialID, responseID uuid.UUID) error {
	if callID == uuid.Nil || credentialID == uuid.Nil || responseID == uuid.Nil {
		return errors.New("invalid Beichen receipt association")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockBeichenResponse(ctx, tx, responseID); err != nil {
		return err
	}
	var previous uuid.NullUUID
	var conflicted bool
	err = tx.QueryRowContext(ctx, `SELECT beichen_receipt_response_id,beichen_receipt_conflict
		FROM gateway_calls WHERE id=$1 AND beichen_dispatch_credential_id=$2
		AND beichen_dispatch_kind='beichen'
		AND (beichen_state='reserved' OR (beichen_state IN ('pending_review','charged')
			AND beichen_receipt_response_id=$3)) FOR UPDATE`, callID, credentialID, responseID).Scan(&previous, &conflicted)
	if err != nil {
		return err
	}
	var collision bool
	err = tx.QueryRowContext(ctx, `INSERT INTO beichen_receipt_response_claims AS claims (response_id,first_call_id)
		VALUES ($1,$2) ON CONFLICT(response_id) DO UPDATE SET
		  conflicting_call_id=COALESCE(claims.conflicting_call_id,
		    CASE WHEN claims.first_call_id<>EXCLUDED.first_call_id THEN EXCLUDED.first_call_id END),
		  conflicted_at=COALESCE(claims.conflicted_at,
		    CASE WHEN claims.first_call_id<>EXCLUDED.first_call_id THEN clock_timestamp() END)
		RETURNING conflicting_call_id IS NOT NULL`, responseID, callID).Scan(&collision)
	if err != nil {
		return err
	}
	conflicted = conflicted || collision || (previous.Valid && previous.UUID != responseID)
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_calls SET
		beichen_receipt_response_id=COALESCE(beichen_receipt_response_id,$2),
		beichen_receipt_conflict=$3 WHERE id=$1`, callID, responseID, conflicted); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if conflicted {
		return errors.New("conflicting Beichen response UUID")
	}
	return nil
}

func lockBeichenResponse(ctx context.Context, tx *sql.Tx, responseID uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,3491750035))`, responseID.String())
	return err
}

// ApplyBeichenReceipt posts a verified upstream actual cost once. The upstream
// cost includes its multiplier already. Original protocol metrics stay intact.
func (s *Store) ApplyBeichenReceipt(ctx context.Context, credentialID uuid.UUID, receipt BeichenReceipt) (bool, error) {
	if credentialID == uuid.Nil || receipt.ResponseID == uuid.Nil || receipt.RowID <= 0 || receipt.APIKeyID <= 0 ||
		strings.TrimSpace(receipt.Model) == "" || len(receipt.Model) > 256 || strings.ContainsRune(receipt.Model, 0) ||
		receipt.OccurredAt.IsZero() || !beichenReceiptCost.MatchString(receipt.ActualCost) ||
		receipt.InputTokens < 0 || receipt.OutputTokens < 0 || receipt.CacheReadTokens < 0 || receipt.CacheWriteTokens < 0 {
		return false, errors.New("invalid Beichen receipt")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := lockBeichenResponse(ctx, tx, receipt.ResponseID); err != nil {
		return false, err
	}
	// Recheck the mapping while holding the provider, credential, and panel
	// rows in the same order as configuration writes.
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT true FROM providers p
		JOIN provider_credentials c ON c.provider_id=p.id WHERE c.id=$1 FOR SHARE OF p,c`, credentialID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var callID uuid.UUID
	var state string
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.beichen_state
		FROM beichen_receipt_response_claims r
		JOIN gateway_calls c ON c.id=r.first_call_id
		JOIN beichen_receipt_active_bindings a ON a.credential_id=c.beichen_dispatch_credential_id
		JOIN beichen_panel_access panel ON panel.credential_id=a.credential_id
		WHERE r.response_id=$1 AND r.conflicted_at IS NULL
		  AND c.beichen_receipt_response_id=$1 AND NOT c.beichen_receipt_conflict
		  AND c.beichen_dispatch_kind='beichen' AND c.beichen_dispatch_credential_id=$2
		  AND c.beichen_receipt_api_key_id=$3 AND a.upstream_key_id=$3
		  AND a.updated_at=c.beichen_receipt_panel_revision
		  AND c.beichen_dispatch_model=$4 AND c.beichen_state IN ('pending_review','charged')
		  AND $5::timestamptz BETWEEN c.sent_at-interval '2 seconds' AND c.finalized_at+interval '2 minutes'
		FOR UPDATE OF c FOR SHARE OF panel`, receipt.ResponseID, credentialID, receipt.APIKeyID, receipt.Model, receipt.OccurredAt).
		Scan(&callID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receiptRecorded bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM beichen_call_receipts WHERE call_id=$1 AND response_id=$2
	)`, callID, receipt.ResponseID).Scan(&receiptRecorded); err != nil {
		return false, err
	}
	if receiptRecorded {
		// A replay must not construct and validate another receipt row. In
		// particular, a capped supplement stores a base that differs from
		// actual_cost, and that row would violate the check before ON CONFLICT.
		return false, nil
	}
	applied := state == "pending_review"
	var charge beichenCharge
	if applied {
		// A verified balance debit is authoritative for pending follow-price
		// calls. The dispatch-time teacher price still applies when evaluable.
		err = tx.QueryRowContext(ctx, `SELECT coalesce(cost.manual,$2::numeric*100)::text,
			CASE WHEN cost.manual IS NOT NULL THEN 'teacher_manual_price' ELSE 'receipt_actual_cost' END,
			CASE WHEN cost.manual IS NOT NULL THEN p.upstream_version END,
			CASE WHEN cost.manual IS NOT NULL THEN p.multiplier_version END
			FROM gateway_calls c
			LEFT JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,coalesce(c.beichen_price_at,c.sent_at)) p
			  ON p.mode='manual'
			CROSS JOIN LATERAL (SELECT
			  coalesce(CASE WHEN c.usage_state='reported' AND c.usage_reason IS NULL
			    AND c.beichen_review_reason NOT IN ('unsupported_text_pricing','upstream_model_mismatch','upstream_model_unconfirmed') THEN
			    beichen_token_credits(c.protocol,c.prompt_tokens,c.completion_tokens,c.cache_hit_tokens,c.cache_miss_tokens,
			      c.cache_write_tokens,p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)/100 END,
			    beichen_weighted_tokens($3,$4,$5,$6,p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)
			      *0.000000000001::numeric) AS manual) cost
			WHERE c.id=$1`, callID, receipt.ActualCost, receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens,
			receipt.CacheWriteTokens).Scan(
			&charge.base, &charge.source, &charge.upstreamVersion, &charge.multiplierVersion)
		if err != nil {
			return false, err
		}
	}
	// The receipt's legacy check ties credits_booked to its full base and
	// school multiplier. The gateway call records the debit and cap split.
	result, err := tx.ExecContext(ctx, `INSERT INTO beichen_call_receipts
		(row_id,call_id,credential_id,response_id,api_key_id,model,occurred_at,actual_cost,
		 input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,credits_booked,applied,base_credits,school_multiplier)
		SELECT $2,c.id,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11,$12,
		  CASE WHEN $13::boolean THEN NULLIF($14,'')::numeric*coalesce(c.beichen_school_multiplier,1)
		       ELSE coalesce(c.actual_credits,c.beichen_credit_exact) END,$13,
		  coalesce(NULLIF($14,'')::numeric,`+beichenBaseCreditsSQL+`),coalesce(c.beichen_school_multiplier,1)
		FROM gateway_calls c WHERE c.id=$1 ON CONFLICT DO NOTHING`, callID, receipt.RowID, credentialID,
		receipt.ResponseID, receipt.APIKeyID, receipt.Model, receipt.OccurredAt, receipt.ActualCost,
		receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens, receipt.CacheWriteTokens, applied, charge.base)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return false, err
	}
	if applied {
		if err := bookBeichenCharge(ctx, tx, callID, charge); err != nil {
			return false, err
		}
	} else {
		supplemented, err := applyReceiptSupplement(ctx, tx, callID, receipt)
		if err != nil {
			return false, err
		}
		if supplemented {
			if _, err := tx.ExecContext(ctx, `UPDATE beichen_call_receipts r SET
				applied=true,credits_booked=r.actual_cost*100,
				base_credits=COALESCE(c.beichen_base_credits,r.actual_cost*100/COALESCE(r.school_multiplier,1))
				FROM gateway_calls c WHERE r.call_id=$1 AND c.id=r.call_id`, callID); err != nil {
				return false, err
			}
			applied = true
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return applied, nil
}

// applyReceiptSupplement adds only the amount left unknown by an earlier
// partial charge. The receipt row and response lock make this idempotent.
func applyReceiptSupplement(ctx context.Context, tx *sql.Tx, callID uuid.UUID, receipt BeichenReceipt) (bool, error) {
	var supplemented bool
	var deltaMilli string
	err := tx.QueryRowContext(ctx, `WITH price AS (
		SELECT p.* FROM gateway_calls c
		CROSS JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,
			c.beichen_dispatch_model,COALESCE(c.beichen_price_at,c.sent_at)) p
		WHERE c.id=$1
	), weighted AS (
		SELECT c.id,
			COALESCE(c.student_credits,0) AS old_student,
			COALESCE(c.actual_credits,0) AS old_actual,
			CASE WHEN c.student_cap_milli IS NOT NULL
				THEN GREATEST(c.student_cap_milli,0)*0.001::numeric END AS cap,
			COALESCE(c.beichen_school_multiplier,1) AS school_multiplier,
			beichen_weighted_tokens($2::bigint,$3::bigint,$4::bigint,$5::bigint,
				COALESCE(p.school_input,p.upstream_input),
				COALESCE(p.school_output,p.upstream_output),
				COALESCE(p.school_cache_read,p.upstream_cache_read),
				COALESCE(p.school_cache_write,p.upstream_cache_write))
				* 0.0000000001::numeric / CASE WHEN p.mode='manual' THEN 100 ELSE 1 END AS base
		FROM gateway_calls c CROSS JOIN price p
		WHERE c.id=$1 AND c.beichen_state='charged'
		  AND c.usage_state='partial'
		  AND COALESCE(c.pricing_detail->>'amount_source','') IN ('confirmed_tokens_only','estimated_unique_amount')
	), amount AS (
		SELECT id,old_student,old_actual,base,base*school_multiplier AS actual,
			LEAST(base*school_multiplier,cap) AS allowed
		FROM weighted
	), next AS (
		SELECT id,old_student,old_actual,base,actual,
			old_student + GREATEST(0,COALESCE(allowed,actual)-old_student) AS student
		FROM amount
	), updated AS (
		UPDATE gateway_calls c SET
			beichen_credit_exact=next.student,
			credit_milli=trunc(next.student*1000)::bigint,
			student_credits=next.student,
			actual_credits=GREATEST(next.old_actual,next.actual),
			school_absorbed_credits=GREATEST(next.old_actual,next.actual)-next.student,
			beichen_base_credits=CASE WHEN next.student=GREATEST(next.old_actual,next.actual)
				THEN next.base END,
			beichen_base_source=CASE WHEN next.student=GREATEST(next.old_actual,next.actual)
				THEN 'receipt_usage_price' END,
			pricing_detail=COALESCE(c.pricing_detail,'{}'::jsonb) || jsonb_build_object(
				'receipt_supplement_credits',trim_scale(next.student-next.old_student)::text,
				'receipt_supplement_student_credits',trim_scale(next.student)::text)
		FROM next WHERE c.id=next.id AND next.student>next.old_student
		RETURNING c.id
	)
	SELECT true,trim_scale((next.student-next.old_student)*1000)::text
	FROM updated JOIN next ON next.id=updated.id`,
		callID, receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens, receipt.CacheWriteTokens).
		Scan(&supplemented, &deltaMilli)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := postReceiptSupplement(ctx, tx, callID, deltaMilli); err != nil {
		return false, err
	}
	if err := refreshLedgerTurn(ctx, tx, callID, deltaMilli, true); err != nil {
		return false, err
	}
	return supplemented, nil
}

func postReceiptSupplement(ctx context.Context, tx *sql.Tx, callID uuid.UUID, milli string) error {
	if err := postLedgerBuckets(ctx, tx, callID, 0, milli); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE monthly_usage_cube u SET
		charged_milli=u.charged_milli+trunc(u.charged_milli_remainder+$2::numeric)::bigint,
		charged_milli_remainder=mod(u.charged_milli_remainder+$2::numeric,1),
		updated_at=now()
		FROM gateway_calls c WHERE c.id=$1 AND u.month=c.month
		  AND u.project_id=c.project_id AND u.user_id=c.user_id
		  AND u.api_key_id=c.api_key_id AND u.model_id=c.model_id`, callID, milli)
	return err
}
