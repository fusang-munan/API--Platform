package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TerminalPendingResult struct {
	Resolved int `json:"resolved"`
	Errors   int `json:"errors"`
}

func terminalPendingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEBULA_TERMINAL_PENDING_POLICY"))) {
	case "", "known_only":
		return true
	}
	return false
}

func terminalSetting(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

// Parser conflicts are evidence problems, not "no known usage". They must stay
// in the manual queue instead of being written off as zero by the terminal
// policy.
const terminalBlockedUsageSQL = `('invalid_usage_payload','invalid_token_count','conflicting_usage_fields','conflicting_cache_creation_counts','inconsistent_token_total','inconsistent_token_breakdown')`

// A failed booking is parked for just under one worker interval. The marker lives in the
// existing JSONB evidence column so no migration is needed, and failed rows
// cannot occupy every candidate window forever.
const terminalRetryEpochSQL = `CASE WHEN COALESCE(pricing_detail->>'terminal_retry_epoch','') ~ '^[0-9]+$'
	THEN (pricing_detail->>'terminal_retry_epoch')::bigint END`

const staleTerminalFilter = `beichen_state='pending_review' AND beichen_review_reason='missing_terminal_usage'
	AND COALESCE(usage_reason,'') NOT IN ` + terminalBlockedUsageSQL + `
	AND (` + terminalRetryEpochSQL + ` IS NULL OR ` + terminalRetryEpochSQL + ` <= $3)
	AND EXTRACT(EPOCH FROM (now()-COALESCE(finalized_at,created_at))) >= $1
	AND ($2::uuid IS NULL OR provider_id=$2)`

func terminalEvidenceBlocked(reason string) bool {
	switch reason {
	case "invalid_usage_payload", "invalid_token_count", "conflicting_usage_fields", cacheWriteConflictReason,
		"inconsistent_token_total", "inconsistent_token_breakdown":
		return true
	}
	return false
}

// observedCacheTTL turns the response's cache-creation children into the
// duration that was actually billed. Mixed non-zero durations have no single
// rate and must not be priced as if they were all one duration.
func observedCacheTTL(m CallMetrics) string {
	five, one := m.cacheWrite5m, m.cacheWrite1h
	switch {
	case five != nil && *five > 0 && one != nil && *one > 0:
		return "mixed"
	case five != nil && *five > 0:
		return "5m"
	case one != nil && *one > 0:
		return "1h"
	}
	return ""
}

func ResolveStaleTerminalPending(ctx context.Context, db *sql.DB, limit int) (TerminalPendingResult, error) {
	return resolveTerminalPending(ctx, db, nil, limit)
}

func ResolveProviderTerminalPending(ctx context.Context, db *sql.DB, provider uuid.UUID) (TerminalPendingResult, error) {
	return resolveTerminalPending(ctx, db, &provider, 0)
}

func resolveTerminalPending(ctx context.Context, db *sql.DB, provider *uuid.UUID, limit int) (TerminalPendingResult, error) {
	var result TerminalPendingResult
	if !terminalPendingEnabled() {
		return result, nil
	}
	batch := max(1, min(500, terminalSetting("NEBULA_TERMINAL_PENDING_BATCH", 100)))
	if limit <= 0 || limit > batch {
		limit = batch
	}
	// One persistently failing row must not occupy the whole batch. Read a
	// wider candidate window and count only successful settlements toward the
	// batch limit; errors stay in the queue for the next scan.
	window := min(500, max(200, limit*8))
	grace := max(60, terminalSetting("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", 300))
	retryBefore := time.Now().Add(-55 * time.Second).Unix()
	rows, err := db.QueryContext(ctx, `SELECT id FROM gateway_calls WHERE `+staleTerminalFilter+
		` ORDER BY (`+terminalRetryEpochSQL+` IS NOT NULL),COALESCE(finalized_at,created_at),id LIMIT $4`,
		grace, provider, retryBefore, window)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	for _, id := range ids {
		if result.Resolved >= limit {
			break
		}
		resolved, err := resolveTerminalCall(ctx, db, provider, grace, retryBefore, id)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			slog.ErrorContext(ctx, "缺终态结算失败", "call_id", id, "error", err)
			result.Errors++
			if ctx.Err() == nil {
				if markErr := markTerminalRetry(ctx, db, id); markErr != nil {
					slog.WarnContext(ctx, "记录缺终态重试时间失败", "call_id", id, "error", markErr)
				}
			}
		} else if resolved {
			result.Resolved++
		}
	}
	return result, nil
}

func resolveTerminalCall(ctx context.Context, db *sql.DB, provider *uuid.UUID, grace int, retryBefore int64, id uuid.UUID) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=(SELECT user_id FROM gateway_calls WHERE id=$1) FOR NO KEY UPDATE`, id); err != nil {
		return false, err
	}
	var protocol string
	var m CallMetrics
	err = tx.QueryRowContext(ctx, `SELECT protocol,prompt_tokens,completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,cache_write_tokens,COALESCE(usage_reason,''),
		CASE WHEN COALESCE(pricing_detail->>'cache_write_5m_tokens','') ~ '^[0-9]+$'
			THEN (pricing_detail->>'cache_write_5m_tokens')::bigint END,
		CASE WHEN COALESCE(pricing_detail->>'cache_write_1h_tokens','') ~ '^[0-9]+$'
			THEN (pricing_detail->>'cache_write_1h_tokens')::bigint END
		FROM gateway_calls WHERE `+staleTerminalFilter+` AND id=$4 FOR UPDATE`, grace, provider, retryBefore, id).
		Scan(&protocol, &m.PromptTokens, &m.CompletionTokens, &m.TotalTokens, &m.CacheHitTokens, &m.CacheMissTokens, &m.CacheWriteTokens, &m.UsageReason,
			&m.cacheWrite5m, &m.cacheWrite1h)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gateway_calls SET pricing_detail=COALESCE(pricing_detail,'{}'::jsonb)-'terminal_retry_epoch' WHERE id=$1`, id); err != nil {
		return false, err
	}
	// Prefer the cache duration the upstream actually reported. A request can
	// ask for 1h while the response proves only 5m writes; pricing those writes
	// at the 1h rate would overcharge. Mixed durations cannot be represented by
	// one rate, so they are written off with an explicit reason instead.
	mixedCacheTTL := false
	if protocol == "anthropic" {
		m.CacheTTL = observedCacheTTL(m)
		if m.CacheTTL == "mixed" {
			mixedCacheTTL = true
			m.CacheTTL = ""
		}
	}
	// Decide completeness from the stored evidence before knownTerminalMetrics
	// fills protocol-required zero counters. A filled zero is not an observed
	// input count and must not unlock a cheaper price tier.
	inputComplete := m.PromptTokens != nil && (protocol != "anthropic" ||
		(m.CacheHitTokens != nil && (m.CacheWriteTokens != nil || (m.cacheWrite5m != nil && m.cacheWrite1h != nil))))
	input, output, reason := knownTerminalMetrics(&m, protocol)
	if terminalEvidenceBlocked(reason) {
		return false, nil
	}
	if mixedCacheTTL {
		reason = "unknown_cache_ttl"
	}
	var charge beichenCharge
	if reason == "" {
		var missingRate string
		charge, reason, missingRate, err = quoteBeichenReport(ctx, tx, id, m)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			reason = "missing_dispatch_price"
		case err != nil:
			return false, err
		case charge.base == "" && reason == "":
			reason = missingRate
			if !charge.upstreamVersion.Valid {
				reason = "missing_upstream_price"
			}
		case !inputComplete && charge.tier != "" && len(charge.snapshot) > 0:
			// The real input may cross a cheaper tier. A lower token count
			// alone does not establish a lower bound for a tiered price.
			var terms struct {
				Intervals []json.RawMessage `json:"intervals"`
			}
			if err := json.Unmarshal(charge.snapshot, &terms); err != nil {
				return false, err
			}
			if len(terms.Intervals) > 0 {
				reason = "unknown_input_price_tier"
			}
		}
	}
	detail := map[string]any{"terminal_missing": true, "terminal_policy": "known_only", "known_input_tokens": input,
		"known_output_tokens": output, "processed_at": time.Now().UTC(), "priced": reason == ""}
	if reason != "" {
		detail["reason"] = reason
		charge = beichenCharge{base: "0", source: "gateway_usage_price"}
	}
	charge.annotation, err = json.Marshal(detail)
	if err != nil {
		return false, err
	}
	if err = bookBeichenCharge(ctx, tx, id, charge); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func markTerminalRetry(ctx context.Context, db *sql.DB, id uuid.UUID) error {
	_, err := db.ExecContext(ctx, `UPDATE gateway_calls SET pricing_detail=COALESCE(pricing_detail,'{}'::jsonb) ||
		jsonb_build_object('terminal_retry_epoch',$2::bigint) WHERE id=$1 AND beichen_state='pending_review'`, id, time.Now().Unix())
	return err
}

// Prepare a pricing-only copy. Keep the original nullable observations in the
// call row. A total can contradict known parts but never supplies missing output.
func knownTerminalMetrics(m *CallMetrics, protocol string) (input, output int64, reason string) {
	zero := int64(0)
	outputKnown := m.CompletionTokens != nil
	if m.CompletionTokens == nil {
		m.CompletionTokens = &zero
	}
	output = *m.CompletionTokens
	// A directly observed 5m/1h cache-creation child is known usage even when
	// the parent total was not reported. Only use the parent when it exists;
	// conflicting evidence is rejected below by usage_reason.
	if protocol == "anthropic" && m.CacheWriteTokens == nil {
		switch {
		case m.cacheWriteReported != nil:
			m.CacheWriteTokens = m.cacheWriteReported
		case m.cacheWrite5m != nil || m.cacheWrite1h != nil:
			var sum int64
			for _, n := range []*int64{m.cacheWrite5m, m.cacheWrite1h} {
				if n != nil {
					sum += *n
				}
			}
			m.CacheWriteTokens = &sum
		}
	}
	var parts int64
	for _, n := range []*int64{m.CacheMissTokens, m.CacheHitTokens, m.CacheWriteTokens} {
		if n != nil {
			if *n < 0 || *n > math.MaxInt64-parts {
				return 0, output, "inconsistent_token_breakdown"
			}
			parts += *n
		}
	}
	if protocol == "anthropic" {
		if m.PromptTokens == nil {
			m.PromptTokens = m.CacheMissTokens
		}
		if m.PromptTokens == nil {
			m.PromptTokens = &zero
		}
		if m.CacheMissTokens != nil && *m.CacheMissTokens != *m.PromptTokens {
			return *m.PromptTokens, output, "inconsistent_token_breakdown"
		}
		if m.CacheHitTokens == nil {
			m.CacheHitTokens = &zero
		}
		if m.CacheWriteTokens == nil {
			m.CacheWriteTokens = &zero
		}
		input = *m.CacheHitTokens + *m.CacheWriteTokens
		if *m.PromptTokens > math.MaxInt64-input {
			return 0, output, "inconsistent_token_breakdown"
		}
		input += *m.PromptTokens
	} else {
		// OpenAI prompt already includes cache. Without a prompt count,
		// only the explicitly reported, disjoint input parts are billable.
		if m.PromptTokens == nil {
			m.PromptTokens = &parts
		}
		input = *m.PromptTokens
		if input < parts {
			return input, output, "inconsistent_token_breakdown"
		}
	}
	if *m.PromptTokens < 0 || output < 0 || input > math.MaxInt64-output ||
		(m.TotalTokens != nil && *m.TotalTokens < input+output) {
		return input, output, "inconsistent_token_total"
	}
	// When output was explicitly reported, a total larger than every known
	// billable part proves an unaccounted category. Do not quietly book the
	// known parts and discard the contradiction. A missing output is different:
	// the extra total can legitimately be the output we were not told about.
	if outputKnown && m.TotalTokens != nil && *m.TotalTokens > input+output {
		switch protocol {
		case "openai_compatible", "openai_responses", "anthropic":
			return input, output, "inconsistent_token_total"
		}
	}
	if input == 0 && output == 0 {
		return input, output, "no_known_billable_tokens"
	}
	switch m.UsageReason {
	case "invalid_usage_payload", "invalid_token_count", "conflicting_usage_fields", cacheWriteConflictReason, "inconsistent_token_total", "inconsistent_token_breakdown":
		return input, output, m.UsageReason
	}
	m.TotalTokens = nil
	m.State, m.UsageReason = UsageReported, ""
	// The SQL price path accepts explicit parts. Fill only those uniquely
	// determined by the protocol; the quote rejects ambiguous unequal rates.
	if split, blocker := normalizeBeichenConditionalTokens(*m, protocol); blocker == "" {
		m.CacheMissTokens, m.CacheHitTokens, m.CacheWriteTokens = split.Input, split.Read, split.Write
	}
	return input, output, ""
}

func RunTerminalPending(ctx context.Context, db *sql.DB) {
	if !terminalPendingEnabled() {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_, err := ResolveStaleTerminalPending(ctx, db, 0)
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "缺终态待核扫描失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
