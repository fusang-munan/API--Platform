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

const staleTerminalFilter = `beichen_state='pending_review' AND beichen_review_reason='missing_terminal_usage'
	AND EXTRACT(EPOCH FROM (now()-COALESCE(finalized_at,created_at))) >= $1
	AND ($2::uuid IS NULL OR provider_id=$2)`

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
	grace := max(60, terminalSetting("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", 300))
	rows, err := db.QueryContext(ctx, `SELECT id FROM gateway_calls WHERE `+staleTerminalFilter+
		` ORDER BY COALESCE(finalized_at,created_at),id LIMIT $3`, grace, provider, limit)
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
		resolved, err := resolveTerminalCall(ctx, db, provider, grace, id)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			slog.ErrorContext(ctx, "缺终态结算失败", "call_id", id, "error", err)
			result.Errors++
		} else if resolved {
			result.Resolved++
		}
	}
	return result, nil
}

func resolveTerminalCall(ctx context.Context, db *sql.DB, provider *uuid.UUID, grace int, id uuid.UUID) (bool, error) {
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
	err = tx.QueryRowContext(ctx, `SELECT protocol,prompt_tokens,completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,cache_write_tokens,COALESCE(usage_reason,'')
		FROM gateway_calls WHERE `+staleTerminalFilter+` AND id=$3 FOR UPDATE`, grace, provider, id).
		Scan(&protocol, &m.PromptTokens, &m.CompletionTokens, &m.TotalTokens, &m.CacheHitTokens, &m.CacheMissTokens, &m.CacheWriteTokens, &m.UsageReason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	inputComplete := m.PromptTokens != nil && (protocol != "anthropic" || (m.CacheHitTokens != nil && m.CacheWriteTokens != nil))
	input, output, reason := knownTerminalMetrics(&m, protocol)
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

// Prepare a pricing-only copy. Keep the original nullable observations in the
// call row. A total can contradict known parts but never supplies missing output.
func knownTerminalMetrics(m *CallMetrics, protocol string) (input, output int64, reason string) {
	zero := int64(0)
	if m.CompletionTokens == nil {
		m.CompletionTokens = &zero
	}
	output = *m.CompletionTokens
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
