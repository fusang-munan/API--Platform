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
	var state, protocol, ttl string
	var recordedWrites int64
	var terms []byte
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.beichen_state,quote.terms,c.protocol,COALESCE(c.cache_ttl,''),COALESCE(c.cache_write_tokens,0)
		FROM beichen_receipt_response_claims r
		JOIN gateway_calls c ON c.id=r.first_call_id
		JOIN beichen_receipt_active_bindings a ON a.credential_id=c.beichen_dispatch_credential_id
		JOIN beichen_panel_access panel ON panel.credential_id=a.credential_id`+beichenConditionQuoteJoin+`
		WHERE r.response_id=$1 AND r.conflicted_at IS NULL
		  AND c.beichen_receipt_response_id=$1 AND NOT c.beichen_receipt_conflict
		  AND c.beichen_dispatch_kind='beichen' AND c.beichen_dispatch_credential_id=$2
		  AND c.beichen_receipt_api_key_id=$3 AND a.upstream_key_id=$3
		  AND a.updated_at=c.beichen_receipt_panel_revision
		  AND c.beichen_dispatch_model=$4 AND c.beichen_state IN ('pending_review','charged')
		  AND $5::timestamptz BETWEEN c.sent_at-interval '2 seconds' AND c.finalized_at+interval '2 minutes'
		FOR UPDATE OF c FOR SHARE OF panel`, receipt.ResponseID, credentialID, receipt.APIKeyID, receipt.Model, receipt.OccurredAt).
		Scan(&callID, &state, &terms, &protocol, &ttl, &recordedWrites)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	applied := state == "pending_review"
	var charge beichenCharge
	if applied {
		// 回执的输入、缓存计数已经互斥；直接加权，不能再次按协议扣除缓存。
		// 跟随费率沿用 0042 的余额单位；教师费率以积分保存，结果须除以 100。
		// 条件价无法使用固定官网价补记，已验证的北辰实扣仍可作为依据。
		conditional := isBeichenConditional(terms) ||
			(protocol == "anthropic" && (receipt.CacheWriteTokens > 0 || recordedWrites > 0) && beichenCacheTTL(ttl) != "5m")
		err = tx.QueryRowContext(ctx, `SELECT coalesce(cost.gateway,cost.receipt,$2::numeric*100)::text,
			CASE WHEN p.mode='manual' AND coalesce(cost.gateway,cost.receipt) IS NOT NULL THEN 'teacher_manual_price'
			     WHEN cost.gateway IS NOT NULL THEN 'gateway_usage_price'
			     WHEN cost.receipt IS NOT NULL THEN 'receipt_usage_price' ELSE 'receipt_actual_cost' END,
			CASE WHEN coalesce(cost.gateway,cost.receipt) IS NOT NULL THEN p.upstream_version END,
			CASE WHEN coalesce(cost.gateway,cost.receipt) IS NOT NULL THEN p.multiplier_version END
			FROM gateway_calls c
			LEFT JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,coalesce(c.beichen_price_at,c.sent_at)) p
			  ON (c.beichen_school_multiplier=1.10 AND NOT $7::boolean) OR p.mode='manual'
			CROSS JOIN LATERAL (SELECT
			  CASE WHEN c.usage_state='reported' THEN coalesce(
			    beichen_token_credits(c.protocol,c.prompt_tokens,c.completion_tokens,c.cache_hit_tokens,c.cache_miss_tokens,
			      c.cache_write_tokens,p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)
			      /CASE WHEN p.mode='manual' THEN 100 ELSE 1 END,
			    CASE WHEN NOT $7::boolean THEN
			      beichen_token_credits(c.protocol,c.prompt_tokens,c.completion_tokens,c.cache_hit_tokens,c.cache_miss_tokens,
			        c.cache_write_tokens,p.upstream_input,p.upstream_output,p.upstream_cache_read,p.upstream_cache_write)
			    END) END AS gateway,
			  coalesce(
			    beichen_weighted_tokens($3,$4,$5,$6,p.school_input,p.school_output,p.school_cache_read,p.school_cache_write)
			      /CASE WHEN p.mode='manual' THEN 100 ELSE 1 END,
			    CASE WHEN NOT $7::boolean THEN
			      beichen_weighted_tokens($3,$4,$5,$6,p.upstream_input,p.upstream_output,p.upstream_cache_read,p.upstream_cache_write)
			    END)
			      *0.0000000001::numeric AS receipt) cost
			WHERE c.id=$1`, callID, receipt.ActualCost, receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens,
			receipt.CacheWriteTokens, conditional).Scan(
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
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return applied, nil
}
