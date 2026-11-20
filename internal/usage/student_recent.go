package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

// StudentRecentCall is one row of the student's own call history. Token fields
// stay nullable because older calls or failed requests may not have usage.
type StudentRecentCall struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"request_id"`
	ModelID          string    `json:"model_id"`
	ModelName        string    `json:"model_name"`
	ProjectName      string    `json:"project_name"`
	InputTokens      *int      `json:"input_tokens"`
	OutputTokens     *int      `json:"output_tokens"`
	CacheReadTokens  *int64    `json:"cache_read_tokens"`
	CacheWriteTokens *int64    `json:"cache_write_tokens"`
	Credits          string    `json:"credits"`
	Status           string    `json:"status"`
	BillingState     string    `json:"billing_state"`
	CreatedAt        time.Time `json:"created_at"`
}

// StudentRecentStart maps the public range values to a Shanghai-based lower
// bound. "all" intentionally has no bound.
func StudentRecentStart(value string, now time.Time) (*time.Time, error) {
	local := now.In(shanghai)
	switch strings.TrimSpace(value) {
	case "today":
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghai).UTC()
		return &start, nil
	case "month":
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, shanghai).UTC()
		return &start, nil
	case "all":
		return nil, nil
	default:
		return nil, domain.NewError(domain.CodeValidation, "range must be today, month or all")
	}
}

// StudentRecentCalls only reads rows recorded for the requesting student. The
// gateway writes user_id from the authenticated key owner, so a historical key
// row (including a withdrawn one) does not hide the student's own history.
func (s *Store) StudentRecentCalls(ctx context.Context, userID uuid.UUID, start *time.Time, cursor *Cursor, limit int) (Page[StudentRecentCall], error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	args := []any{userID}
	where := []string{`c.user_id=$1`}
	if start != nil {
		args = append(args, *start)
		where = append(where, fmt.Sprintf(`c.created_at>=$%d`, len(args)))
	}
	if cursor != nil {
		args = append(args, cursor.CreatedAt, cursor.ID)
		where = append(where, fmt.Sprintf(`(c.created_at,c.id)<($%d,$%d)`, len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	query := `SELECT c.id,c.request_id,m.model_id,c.model_name,c.project_name,
 c.prompt_tokens,c.completion_tokens,c.cache_hit_tokens,c.cache_write_tokens,
 ` + callDisplayCreditsSQL + `,` + callDisplayBillingSQL + `,` + callDisplayOutcomeSQL + `,c.created_at
 FROM gateway_calls c
 JOIN models m ON m.id=c.model_id
 WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(` ORDER BY c.created_at DESC,c.id DESC LIMIT $%d`, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[StudentRecentCall]{}, err
	}
	defer rows.Close()
	page := Page[StudentRecentCall]{Items: []StudentRecentCall{}}
	for rows.Next() {
		var id uuid.UUID
		var item StudentRecentCall
		if err := rows.Scan(&id, &item.RequestID, &item.ModelID, &item.ModelName, &item.ProjectName,
			&item.InputTokens, &item.OutputTokens, &item.CacheReadTokens, &item.CacheWriteTokens,
			&item.Credits, &item.BillingState, &item.Status, &item.CreatedAt); err != nil {
			return page, err
		}
		item.ID = id.String()
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		id, _ := uuid.Parse(last.ID)
		next := EncodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: id})
		page.NextCursor = &next
		page.Items = page.Items[:limit]
	}
	return page, nil
}
