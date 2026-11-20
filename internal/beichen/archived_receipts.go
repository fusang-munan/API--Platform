package beichen

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// The school values are taken from the already-booked call. The supplier may
// correct its own debit after that booking.
type ArchivedReceipt struct {
	OccurredAt        time.Time `json:"occurred_at"`
	Model             string    `json:"model"`
	StudentName       *string   `json:"student_name"`
	BaseCredits       *string   `json:"base_credits"`
	SchoolMultiplier  *string   `json:"school_multiplier"`
	ChargedCredits    *string   `json:"charged_credits"`
	ActualCost        *string   `json:"actual_cost"`
	DifferenceCredits *string   `json:"difference_credits"`
	Matched           bool      `json:"matched"`
}

type ArchivedReceiptPage struct {
	Items      []ArchivedReceipt `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

// Row numbers are unique within one upstream account. All three sort keys
// are necessary when a page ends on a timestamp shared by multiple receipts.
type archivedReceiptCursor struct {
	Provider uuid.UUID `json:"provider"`
	Time     time.Time `json:"at"`
	Row      int64     `json:"row"`
	Owner    int64     `json:"owner"`
}

const archivedReceiptPageSize = 50

func archivedReceiptPosition(raw string, provider uuid.UUID, start, end time.Time) (archivedReceiptCursor, error) {
	if raw == "" {
		return archivedReceiptCursor{Provider: provider, Time: end}, nil
	}
	var pos archivedReceiptCursor
	if len(raw) <= 256 {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err == nil && json.Unmarshal(data, &pos) == nil && pos.Provider == provider &&
			pos.Row > 0 && pos.Owner > 0 && !pos.Time.Before(start) && pos.Time.Before(end) {
			return pos, nil
		}
	}
	return archivedReceiptCursor{}, invalid("逐笔对账分页位置无效，请重新查询所选日期")
}

// ArchivedReceipts reads the school's archive only. Opening a teacher page
// neither contacts the provider nor applies a charge.
func (s *Service) ArchivedReceipts(ctx context.Context, provider uuid.UUID, day, cursor string) (ArchivedReceiptPage, error) {
	if provider == uuid.Nil {
		return ArchivedReceiptPage{}, invalid("需要供应商 ID")
	}
	if day == "" {
		day = s.now().In(beijing).Format(dayLayout)
	}
	start, err := dayStart(day)
	if err != nil {
		return ArchivedReceiptPage{}, err
	}
	end := start.AddDate(0, 0, 1)
	pos, err := archivedReceiptPosition(cursor, provider, start, end)
	if err != nil {
		return ArchivedReceiptPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ArchivedReceiptPage{}, unavailable("开启北辰逐笔对账查询失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return ArchivedReceiptPage{}, err
	}

	rows, err := tx.QueryContext(ctx, `
SELECT u.occurred_at,u.model,CASE WHEN student.id IS NOT NULL THEN c.user_name END,
       trim_scale(coalesce(c.beichen_base_credits,c.beichen_credit_exact))::text,
       CASE WHEN c.id IS NOT NULL THEN coalesce(c.beichen_school_multiplier,1)::text END,
       trim_scale(c.beichen_credit_exact)::text,trim_scale(debit.actual)::text,
       trim_scale(c.beichen_credit_exact-debit.actual*100)::text,c.id IS NOT NULL,
       u.row_id,u.upstream_user_id`+archivedMatchesSQL+`
 LEFT JOIN users student ON student.id=c.user_id AND student.role='student'
 CROSS JOIN LATERAL (SELECT `+archivedDebitSQL+` AS actual) debit`+archivedDaySQL+`
 AND (u.occurred_at,u.row_id,u.upstream_user_id) < ($4,$5,$6)
 ORDER BY u.occurred_at DESC,u.row_id DESC,u.upstream_user_id DESC LIMIT $7`,
		provider, start, end, pos.Time, pos.Row, pos.Owner, archivedReceiptPageSize+1)
	if err != nil {
		return ArchivedReceiptPage{}, unavailable("查询北辰逐笔对账失败", err)
	}
	defer rows.Close()
	page := ArchivedReceiptPage{Items: []ArchivedReceipt{}}
	for rows.Next() {
		if len(page.Items) == archivedReceiptPageSize {
			data, err := json.Marshal(pos)
			if err != nil {
				return ArchivedReceiptPage{}, unavailable("生成北辰逐笔对账分页位置失败", err)
			}
			next := base64.RawURLEncoding.EncodeToString(data)
			page.NextCursor = &next
			break
		}
		var row ArchivedReceipt
		err := rows.Scan(&row.OccurredAt, &row.Model, &row.StudentName, &row.BaseCredits, &row.SchoolMultiplier,
			&row.ChargedCredits, &row.ActualCost, &row.DifferenceCredits, &row.Matched, &pos.Row, &pos.Owner)
		if err != nil {
			return ArchivedReceiptPage{}, unavailable("读取北辰逐笔对账失败", err)
		}
		pos.Time = row.OccurredAt.UTC()
		page.Items = append(page.Items, row)
	}
	if err := rows.Err(); err != nil {
		return ArchivedReceiptPage{}, unavailable("读取北辰逐笔对账失败", err)
	}
	if err := rows.Close(); err != nil {
		return ArchivedReceiptPage{}, unavailable("关闭北辰逐笔对账查询失败", err)
	}
	if err := tx.Commit(); err != nil {
		return ArchivedReceiptPage{}, unavailable("读取北辰逐笔对账失败", err)
	}
	return page, nil
}
