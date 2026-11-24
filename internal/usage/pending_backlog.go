package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage/pendingaudit"
)

type PendingBacklogRow struct {
	ID             uuid.UUID `json:"id"`
	Student        string    `json:"student"`
	Model          string    `json:"model"`
	Protocol       string    `json:"protocol"`
	Reason         string    `json:"reason"`
	Class          string    `json:"class"`
	Min            float64   `json:"min"`
	Max            float64   `json:"max"`
	Recommended    float64   `json:"recommended"`
	Note           string    `json:"note"`
	Auto           bool      `json:"can_auto_settle"`
	StudentCredits string    `json:"student_credits,omitempty"`
}

type PendingBacklogReport struct {
	ProviderID        uuid.UUID           `json:"provider_id"`
	GeneratedAt       time.Time           `json:"generated_at"`
	Total             int                 `json:"total"`
	BacklogTotal      int                 `json:"backlog_total"`
	NextOffset        *int                `json:"next_offset"`
	Exact             int                 `json:"exact"`
	Narrow            int                 `json:"narrow"`
	Wide              int                 `json:"wide"`
	Unknown           int                 `json:"unknown"`
	AutoSettle        int                 `json:"auto_settle"`
	MaxErrorYuan      float64             `json:"max_error_yuan"`
	EstimatedAutoYuan float64             `json:"estimated_auto_yuan"`
	PreviewToken      string              `json:"preview_token"`
	Rows              []PendingBacklogRow `json:"rows"`
	Charged           int                 `json:"charged"`
	StillPending      int                 `json:"still_pending"`
	Skipped           int                 `json:"skipped"`
	Errors            int                 `json:"errors"`
	CreditsCharged    string              `json:"credits_charged"`
	RemainingPending  int                 `json:"remaining_pending"`
}

const backlogFilter = `provider_id=$1 AND beichen_state='pending_review'`

// PendingAuditBacklog confirms one snapshot, then rechecks each confirmed row
// under the same student-before-call lock order used by gateway finalization.
func PendingAuditBacklog(ctx context.Context, db *sql.DB, providerID uuid.UUID, apply bool, limit, offset int, previewToken string) (PendingBacklogReport, error) {
	if offset < 0 {
		return PendingBacklogReport{}, domain.NewError(domain.CodeValidation, "offset must be non-negative")
	}
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 500)
	report, err := previewBacklog(ctx, db, providerID, limit, offset)
	if err != nil || !apply {
		return report, err
	}
	if previewToken != report.PreviewToken {
		return PendingBacklogReport{}, domain.NewError(domain.CodePendingAuditChanged, "待核集合已变化，请重新回放")
	}
	var sum big.Rat
	for _, row := range report.Rows {
		if !row.Auto {
			report.StillPending++
			continue
		}
		status, debit, err := applyBacklogRow(ctx, db, providerID, row)
		if err != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			slog.ErrorContext(ctx, "待核调用结算失败", "provider_id", providerID, "call_id", row.ID, "error", err)
			report.Errors++
			continue
		}
		switch status {
		case "charged":
			report.Charged++
			sum.Add(&sum, debit)
		case "pending":
			report.StillPending++
		default:
			report.Skipped++
		}
	}
	report.CreditsCharged = beichenDecimalString(&sum)
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_calls WHERE `+backlogFilter, providerID).Scan(&report.RemainingPending)
	return report, err
}

func previewBacklog(ctx context.Context, db *sql.DB, providerID uuid.UUID, limit, offset int) (PendingBacklogReport, error) {
	report := PendingBacklogReport{ProviderID: providerID, GeneratedAt: time.Now().UTC(), Rows: []PendingBacklogRow{}, CreditsCharged: "0"}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	// Stream all IDs into the token. An insertion beyond the batch boundary
	// must also invalidate confirmation. Only the selected batch needs pricing.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM gateway_calls WHERE `+backlogFilter+` ORDER BY created_at,id`, providerID)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	hash := sha256.New()
	hash.Write(providerID[:])
	fmt.Fprintf(hash, "/%d/%d/", offset, limit)
	ids := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return report, err
		}
		hash.Write(id[:])
		if report.BacklogTotal >= offset && len(ids) < limit {
			ids = append(ids, id)
		}
		report.BacklogTotal++
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	report.Total = len(ids)
	if report.BacklogTotal-offset > report.Total {
		next := offset + report.Total
		report.NextOffset = &next
	}
	report.RemainingPending = report.BacklogTotal
	for _, id := range ids {
		row, d, err := readBacklogRow(ctx, tx, providerID, id, false)
		if err != nil {
			return report, err
		}
		report.Rows = append(report.Rows, row)
		switch row.Class {
		case pendingaudit.ClassExact:
			report.Exact++
		case pendingaudit.ClassNarrow:
			report.Narrow++
		case pendingaudit.ClassWide:
			report.Wide++
		default:
			report.Unknown++
		}
		report.MaxErrorYuan = math.Max(report.MaxErrorYuan, d.result.MaxErrorYuan)
		if row.Auto {
			report.AutoSettle++
			report.EstimatedAutoYuan += row.Recommended
		}
	}
	// Bind the batch, classifications and actual quoted debit for each call.
	if err := json.NewEncoder(hash).Encode(report.Rows); err != nil {
		return report, err
	}
	fmt.Fprintf(hash, "/%d/%d/%d/%d", report.Exact, report.Narrow, report.Wide, report.Unknown)
	report.PreviewToken = hex.EncodeToString(hash.Sum(nil))
	return report, tx.Commit()
}

func readBacklogRow(ctx context.Context, tx *sql.Tx, providerID, id uuid.UUID, lock bool) (PendingBacklogRow, pendingDecision, error) {
	row := PendingBacklogRow{ID: id}
	var metrics CallMetrics
	var terminal, priced bool
	query := `SELECT user_name,COALESCE(beichen_dispatch_model,model_name),protocol,COALESCE(beichen_review_reason,''),
  outcome='succeeded' AND COALESCE(error_category,'')='',COALESCE(usage_state,''),COALESCE(usage_reason,''),
  prompt_tokens,completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,cache_write_tokens,reasoning_tokens,tool_prompt_tokens,
  CASE WHEN beichen_price_at IS NOT NULL THEN (pricing_detail->>'cache_write_5m_tokens')::bigint END,
  CASE WHEN beichen_price_at IS NOT NULL THEN (pricing_detail->>'cache_write_1h_tokens')::bigint END,
  beichen_price_at IS NOT NULL
  FROM gateway_calls WHERE ` + backlogFilter + ` AND id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, query, providerID, id).Scan(&row.Student, &row.Model, &row.Protocol, &row.Reason, &terminal, &metrics.State, &metrics.UsageReason,
		&metrics.PromptTokens, &metrics.CompletionTokens, &metrics.TotalTokens, &metrics.CacheHitTokens, &metrics.CacheMissTokens, &metrics.CacheWriteTokens,
		&metrics.ReasoningTokens, &metrics.ToolPromptTokens, &metrics.cacheWrite5m, &metrics.cacheWrite1h, &priced)
	var d pendingDecision
	if err != nil {
		return row, d, err
	}
	if !priced {
		row.Class, row.Note = pendingaudit.ClassUnknown, "缺少派发价格，保留待核"
		return row, d, nil
	}
	metrics = metrics.ClassifyBeichenUsage(row.Protocol)
	// Historical rows may already have complete terminal counters. Recheck the
	// stored quote and the student's bound before admitting those rows too.
	complete := metrics.State == UsageReported && unresolvedExposureEnabled()
	if !terminal || (!complete && !metrics.CanAuditBeichenSplit()) {
		row.Class, row.Note = pendingaudit.ClassUnknown, "缺少可用于自动结算的终态缓存分项证据，或待核估算已关闭"
		return row, d, nil
	}
	d, err = decidePendingReport(ctx, tx, id, row.Protocol, metrics)
	if err != nil {
		return row, d, err
	}
	row.Class, row.Note = d.result.Class, d.result.Note
	row.Min, row.Max, row.Recommended = d.result.MinYuan, d.result.MaxYuan, d.result.RecommendedYuan
	if !d.result.CanAutoSettle {
		return row, d, nil
	}
	charge, reason, _, err := quoteBeichenReport(ctx, tx, id, d.adjusted)
	if err != nil {
		return row, d, err
	}
	if charge.base == "" {
		row.Note = "派发时的定价证据不足，保留待核 " + reason
		return row, d, nil
	}
	err = tx.QueryRowContext(ctx, beichenChargeAmountSQL+` SELECT trim_scale(student)::text FROM amount WHERE `+auditBoundPredicate,
		id, charge.base, charge.quoteMultiplier, d.booking.maxCredits).Scan(&row.StudentCredits)
	if errors.Is(err, sql.ErrNoRows) {
		row.Note = "实际学生扣款超过待核金额上界，保留待核"
		return row, d, nil
	}
	row.Auto = err == nil
	return row, d, err
}

func applyBacklogRow(ctx context.Context, db *sql.DB, providerID uuid.UUID, confirmed PendingBacklogRow) (string, *big.Rat, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=(SELECT user_id FROM gateway_calls WHERE `+backlogFilter+` AND id=$2) FOR NO KEY UPDATE`, providerID, confirmed.ID)
	if err != nil {
		return "", nil, err
	}
	row, d, err := readBacklogRow(ctx, tx, providerID, confirmed.ID, true)
	if errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if !row.Auto || row != confirmed {
		return "skipped", nil, nil
	}
	headroom, err := pendingHeadroom(ctx, tx, row.ID)
	if err != nil {
		return "", nil, err
	}
	if err := writePendingReport(ctx, tx, row.ID, d, headroom); err != nil {
		return "", nil, err
	}
	var state, debit string
	err = tx.QueryRowContext(ctx, `SELECT beichen_state,COALESCE(trim_scale(student_credits)::text,'') FROM gateway_calls WHERE id=$1`, row.ID).Scan(&state, &debit)
	if err != nil {
		return "", nil, err
	}
	if state != "charged" {
		return "pending", nil, tx.Commit()
	}
	// If pricing evidence changed during booking, roll back this row.
	if debit != confirmed.StudentCredits {
		return "skipped", nil, nil
	}
	amount, ok := new(big.Rat).SetString(debit)
	if !ok {
		return "", nil, fmt.Errorf("invalid student debit for call %s", row.ID)
	}
	return "charged", amount, tx.Commit()
}
