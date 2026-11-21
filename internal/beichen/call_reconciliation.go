package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CallTokenParts struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_tokens"`
	CacheWrite *int64 `json:"cache_write_tokens"`
}

type CallReconciliation struct {
	ID                    uuid.UUID         `json:"id"`
	RequestID             string            `json:"request_id"`
	CredentialID          uuid.UUID         `json:"credential_id"`
	SentAt                time.Time         `json:"sent_at"`
	Model                 string            `json:"model"`
	Protocol              string            `json:"protocol"`
	Outcome               string            `json:"outcome"`
	UsageState            string            `json:"usage_state"`
	RecordedUsageState    *string           `json:"recorded_usage_state"`
	UsageReason           *string           `json:"usage_reason"`
	LocalTokens           CallTokenParts    `json:"local_tokens"`
	KnownTotalTokens      *int64            `json:"known_total_tokens"`
	ReportedTotalTokens   *int64            `json:"reported_total_tokens"`
	RawPromptTokens       *int64            `json:"raw_prompt_tokens"`
	RawCompletionTokens   *int64            `json:"raw_completion_tokens"`
	ReasoningTokens       *int64            `json:"reasoning_tokens"`
	ToolPromptTokens      *int64            `json:"tool_prompt_tokens"`
	MissingTokenReasons   map[string]string `json:"missing_token_reasons"`
	BillingState          *string           `json:"billing_state"`
	ReviewReason          *string           `json:"review_reason"`
	BaseSource            *string           `json:"base_source"`
	PriceTier             *string           `json:"price_tier"`
	PriceVersion          *int64            `json:"price_version"`
	MultiplierVersion     *int64            `json:"multiplier_version"`
	SchoolMultiplier      *string           `json:"school_multiplier"`
	BaseCredits           *string           `json:"base_credits"`
	ExpectedCredits       *string           `json:"expected_credits"`
	ActualCredits         *string           `json:"actual_credits"`
	DifferenceCredits     *string           `json:"difference_credits"`
	BaseDifferenceCredits *string           `json:"base_difference_credits"`
	StudentCredits        *string           `json:"student_credits"`
	SchoolAbsorbedCredits *string           `json:"school_absorbed_credits"`
	PricingDetail         json.RawMessage   `json:"pricing_detail,omitempty"`
	ReceiptStatus         string            `json:"receipt_status"`
	ReceiptTokens         *CallTokenParts   `json:"receipt_tokens"`
}

const callReconciliationSQL = reportAttributionSQL + `,
 c.id AS call_id, c.request_id, c.outcome, c.usage_reason, c.error_category, c.reasoning_tokens,c.completion_tokens,
 c.beichen_review_reason, c.beichen_receipt_response_id, c.beichen_receipt_conflict,
 COALESCE(c.beichen_base_source,c.pricing_detail->>'base_source') AS base_source,
 COALESCE(c.beichen_base_credits,(c.pricing_detail->>'base_credits')::numeric,
   COALESCE(c.actual_credits,c.beichen_credit_exact)/NULLIF(COALESCE(c.beichen_school_multiplier,1),0)) AS base,
 COALESCE(c.student_credits,c.beichen_credit_exact) AS student,
 CASE WHEN c.student_credits IS NOT NULL THEN c.school_absorbed_credits END AS school_absorbed,
 c.price_tier,c.beichen_upstream_price_version,c.beichen_multiplier_version,c.pricing_detail
` + reportCallsFromSQL + `,
 call_page AS (
   SELECT * FROM report_calls
   WHERE ($4::timestamptz IS NULL OR (sent_at,call_id) < ($4,$5::uuid))
   ORDER BY sent_at DESC,call_id DESC LIMIT $6
 )
 SELECT a.call_id,a.request_id,a.credential_id,a.sent_at,a.model,a.protocol,a.outcome,a.usage_state,
   COALESCE(a.usage_reason,CASE a.error_category
     WHEN 'incomplete_token_breakdown' THEN 'token_breakdown_unconfirmed'
     WHEN 'stream' THEN 'response_interrupted' END),
   a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,a.total_tokens,
   CASE WHEN a.token_parts>0 OR a.total_tokens IS NOT NULL OR a.prompt_tokens IS NOT NULL THEN a.tokens END,
   a.tokens_complete,a.prompt_tokens,a.completion_tokens,a.reasoning_tokens,a.tool_prompt_tokens,a.beichen_state,a.beichen_review_reason,
   a.base_source,a.price_tier,a.beichen_upstream_price_version,a.beichen_multiplier_version,
   trim_scale(a.beichen_school_multiplier)::text,trim_scale(a.base)::text,trim_scale(amount.expected)::text,
   trim_scale(amount.actual)::text,trim_scale(amount.expected-amount.actual)::text,
   trim_scale(a.base-amount.actual)::text,trim_scale(a.student)::text,trim_scale(a.school_absorbed)::text,a.pricing_detail,
   CASE WHEN a.beichen_receipt_conflict OR claim.conflicted_at IS NOT NULL THEN 'conflict'
        WHEN r.call_id IS NOT NULL THEN 'matched'
        WHEN a.beichen_receipt_response_id IS NULL THEN 'receipt_id_missing'
        ELSE 'unmatched' END,
   r.input_tokens,r.output_tokens,r.cache_read_tokens,r.cache_write_tokens
 FROM call_page a
 LEFT JOIN beichen_receipt_response_claims claim ON claim.response_id=a.beichen_receipt_response_id
 LEFT JOIN beichen_call_receipts r ON r.call_id=a.call_id AND r.response_id=claim.response_id
   AND claim.first_call_id=a.call_id AND claim.conflicted_at IS NULL AND NOT a.beichen_receipt_conflict
   AND r.credential_id=a.credential_id AND r.model=a.model
 CROSS JOIN LATERAL (SELECT r.actual_cost*100 AS actual,
   CASE WHEN a.base_source IN ('gateway_usage_price','teacher_manual_price','receipt_usage_price') THEN a.base END AS expected
 ) amount
 ORDER BY a.sent_at DESC,a.call_id DESC`

func (s *Service) ReconciliationCalls(ctx context.Context, provider uuid.UUID, day, cursor string) (usage.Page[CallReconciliation], error) {
	page := usage.Page[CallReconciliation]{Items: []CallReconciliation{}}
	if provider == uuid.Nil || len(cursor) > 256 {
		return page, invalid("供应商或调用对账分页位置无效")
	}
	if day == "" {
		day = s.now().In(beijing).Format(dayLayout)
	}
	start, err := dayStart(day)
	if err != nil {
		return page, err
	}
	end := start.AddDate(0, 0, 1)
	pos, err := usage.DecodeCursor(cursor)
	if err != nil || (pos != nil && (pos.CreatedAt.Before(start) || !pos.CreatedAt.Before(end))) {
		return page, invalid("调用对账分页位置无效，请重新查询所选日期")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return page, unavailable("开启北辰调用对账失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return page, err
	}
	page, err = readReconciliationCalls(ctx, tx, provider, start, end, pos)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return page, unavailable("读取北辰调用对账失败", err)
	}
	return page, nil
}

func readReconciliationCalls(ctx context.Context, tx *sql.Tx, provider uuid.UUID, start, end time.Time, pos *usage.Cursor) (usage.Page[CallReconciliation], error) {
	const pageSize = 50
	var before any
	var beforeID uuid.UUID
	if pos != nil {
		before, beforeID = pos.CreatedAt, pos.ID
	}
	page := usage.Page[CallReconciliation]{Items: []CallReconciliation{}}
	rows, err := tx.QueryContext(ctx, callReconciliationSQL, provider, start, end, before, beforeID, pageSize+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == pageSize {
			last := page.Items[pageSize-1]
			next := usage.EncodeCursor(usage.Cursor{CreatedAt: last.SentAt, ID: last.ID})
			page.NextCursor = &next
			break
		}
		var row CallReconciliation
		var receipt CallTokenParts
		var complete bool
		var detail []byte
		err = rows.Scan(&row.ID, &row.RequestID, &row.CredentialID, &row.SentAt, &row.Model, &row.Protocol, &row.Outcome,
			&row.RecordedUsageState, &row.UsageReason, &row.LocalTokens.Input, &row.LocalTokens.Output, &row.LocalTokens.CacheRead,
			&row.LocalTokens.CacheWrite, &row.ReportedTotalTokens, &row.KnownTotalTokens, &complete, &row.RawPromptTokens,
			&row.RawCompletionTokens, &row.ReasoningTokens, &row.ToolPromptTokens,
			&row.BillingState, &row.ReviewReason, &row.BaseSource, &row.PriceTier, &row.PriceVersion, &row.MultiplierVersion,
			&row.SchoolMultiplier, &row.BaseCredits, &row.ExpectedCredits, &row.ActualCredits, &row.DifferenceCredits,
			&row.BaseDifferenceCredits, &row.StudentCredits, &row.SchoolAbsorbedCredits, &detail, &row.ReceiptStatus,
			&receipt.Input, &receipt.Output, &receipt.CacheRead, &receipt.CacheWrite)
		if err != nil {
			return page, err
		}
		row.PricingDetail = detail
		row.describeTokenObservation(complete)
		if row.ReceiptStatus == "matched" {
			row.ReceiptTokens = &receipt
		}
		page.Items = append(page.Items, row)
	}
	return page, rows.Err()
}

func (r *CallReconciliation) describeTokenObservation(complete bool) {
	r.MissingTokenReasons = map[string]string{}
	if complete {
		r.UsageState = "reported"
		return
	}
	switch {
	case r.RecordedUsageState != nil && *r.RecordedUsageState == "interrupted":
		r.UsageState = "interrupted"
	case r.KnownTotalTokens != nil:
		r.UsageState = "partial"
	default:
		r.UsageState = "missing"
	}
	reason := "field_not_reported"
	if r.UsageReason != nil {
		reason = *r.UsageReason
	} else if r.UsageState == "missing" {
		reason = "usage_not_reported"
	} else if r.UsageState == "interrupted" {
		reason = "response_interrupted"
	} else {
		reason = "token_breakdown_unconfirmed"
	}
	if r.UsageReason == nil {
		r.UsageReason = &reason
	}
	if reason == "cache_write_not_in_usage_metadata" || reason == "token_breakdown_unconfirmed" {
		reason = "field_not_reported"
	}
	for _, part := range []struct {
		name  string
		count *int64
	}{
		{"input_tokens", r.LocalTokens.Input}, {"output_tokens", r.LocalTokens.Output},
		{"cache_read_tokens", r.LocalTokens.CacheRead}, {"cache_write_tokens", r.LocalTokens.CacheWrite},
	} {
		if part.count == nil {
			r.MissingTokenReasons[part.name] = reason
		}
	}
	if r.LocalTokens.Input == nil && r.RawPromptTokens != nil && r.Protocol != "anthropic" {
		r.MissingTokenReasons["input_tokens"] = "input_cache_breakdown_unconfirmed"
	}
	if r.LocalTokens.CacheWrite == nil && r.Protocol == "google_gemini_v1beta" && r.KnownTotalTokens != nil {
		r.MissingTokenReasons["cache_write_tokens"] = "cache_write_not_in_usage_metadata"
	}
}
