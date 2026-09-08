package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func resolveTurn(ctx context.Context, tx *sql.Tx, keyID uuid.UUID, correlation CallCorrelation, _ string) (uuid.UUID, int, bool, error) {
	if correlation.ExplicitTurnID != uuid.Nil {
		var maxIndex int
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(call_index), 0) FROM gateway_calls WHERE turn_id=$1 AND api_key_id=$2`, correlation.ExplicitTurnID, keyID).Scan(&maxIndex)
		if err != nil {
			return uuid.Nil, 0, false, err
		}
		if maxIndex > 0 {
			return correlation.ExplicitTurnID, maxIndex + 1, false, nil
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_call_turns WHERE id=$1)`, correlation.ExplicitTurnID).Scan(&exists); err != nil {
			return uuid.Nil, 0, false, err
		}
		return correlation.ExplicitTurnID, 1, !exists, nil
	}
	if previous := strings.TrimSpace(correlation.PreviousResponseID); previous != "" {
		var turnID uuid.UUID
		var callIndex int
		err := tx.QueryRowContext(ctx, `SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`, previous, keyID).Scan(&turnID, &callIndex)
		if err == nil && turnID != uuid.Nil {
			return turnID, callIndex + 1, false, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, 0, false, err
		}
	}
	if taskKey := strings.TrimSpace(correlation.TaskKey); taskKey != "" {
		var turnID uuid.UUID
		var maxIndex int
		err := tx.QueryRowContext(ctx, `SELECT c.turn_id, MAX(c.call_index)
FROM gateway_calls c
WHERE c.api_key_id=$1 AND c.task_key=$2 AND c.turn_id IS NOT NULL
  AND c.created_at > now() - interval '30 minutes'
GROUP BY c.turn_id
ORDER BY MAX(c.created_at) DESC
LIMIT 1`, keyID, taskKey).Scan(&turnID, &maxIndex)
		if err == nil && turnID != uuid.Nil {
			return turnID, maxIndex + 1, false, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, 0, false, err
		}
	}
	return uuid.Must(uuid.NewV7()), 1, true, nil
}

func applyTurnBilling(ctx context.Context, tx *sql.Tx, turnID uuid.UUID, callIndex int, success bool, outcome string, credit int64) error {
	if turnID == uuid.Nil {
		return nil
	}
	chargedCredit := int64(0)
	if success {
		chargedCredit = credit
	}
	_, err := tx.ExecContext(ctx, `UPDATE gateway_call_turns SET
call_count=GREATEST(call_count,$2),
credit_milli=credit_milli+$3,
outcome=CASE WHEN $4<>'' AND $4<>'succeeded' THEN $4 WHEN outcome='pending' THEN $4 ELSE outcome END,
finished_at=CASE WHEN $5 THEN now() ELSE finished_at END,
updated_at=now()
WHERE id=$1`, turnID, callIndex, chargedCredit, outcome, success)
	return err
}

func applyTurnTokenMetrics(ctx context.Context, tx *sql.Tx, turnID uuid.UUID, metrics CallMetrics) error {
	if turnID == uuid.Nil || !metrics.HasTokens() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE gateway_call_turns SET
prompt_tokens=prompt_tokens+$2,
completion_tokens=completion_tokens+$3,
total_tokens=total_tokens+$4,
updated_at=now()
WHERE id=$1`, turnID, metrics.PromptTokens, metrics.CompletionTokens, metrics.TotalTokens)
	return err
}

func previewPrompt(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func scanOptionalInt(value sql.NullInt64) *int {
	if !value.Valid || value.Int64 <= 0 {
		return nil
	}
	v := int(value.Int64)
	return &v
}

func (s *Store) MentorCallTurns(ctx context.Context, mentorID uuid.UUID, f LogFilter) (Page[CallTurn], error) {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 50
	}
	args := []any{mentorID}
	where := []string{`EXISTS(SELECT 1 FROM project_members pm WHERE pm.project_id=t.project_id AND pm.user_id=$1)`}
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if f.ProjectID != nil {
		add("t.project_id=$%d", *f.ProjectID)
	}
	if f.UserID != nil {
		add("t.user_id=$%d", *f.UserID)
	}
	if f.APIKeyID != nil {
		add("t.api_key_id=$%d", *f.APIKeyID)
	}
	if f.ModelID != nil {
		add("EXISTS(SELECT 1 FROM models filter_model WHERE filter_model.id=t.model_id AND filter_model.model_id=$%d)", *f.ModelID)
	}
	if f.Status != "" {
		add("t.outcome=$%d", f.Status)
	}
	if f.Start != nil {
		add("t.started_at>=$%d", *f.Start)
	}
	if f.End != nil {
		add("t.started_at<$%d", *f.End)
	}
	if f.Cursor != nil {
		args = append(args, f.Cursor.CreatedAt, f.Cursor.ID)
		where = append(where, fmt.Sprintf("(t.started_at,t.id)<($%d,$%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	query := `SELECT t.id,t.project_id,t.project_name,t.user_id,t.user_name,t.api_key_id,t.api_key_name,m.model_id,t.model_name,COALESCE(t.task_key,''),t.root_prompt,t.root_prompt_bytes,t.call_count,t.credit_milli,t.prompt_tokens,t.completion_tokens,t.total_tokens,t.outcome,t.started_at,t.finished_at FROM gateway_call_turns t JOIN models m ON m.id=t.model_id WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY t.started_at DESC,t.id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[CallTurn]{}, err
	}
	defer rows.Close()
	page := Page[CallTurn]{Items: []CallTurn{}}
	for rows.Next() {
		var ids [4]uuid.UUID
		var creditMilli int64
		var taskKey string
		var rootPrompt string
		var finishedAt sql.NullTime
		var item CallTurn
		if err := rows.Scan(&ids[0], &ids[1], &item.ProjectName, &ids[2], &item.UserName, &ids[3], &item.APIKeyName, &item.ModelID, &item.ModelName, &taskKey, &rootPrompt, &item.RootPromptBytes, &item.CallCount, &creditMilli, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.Outcome, &item.StartedAt, &finishedAt); err != nil {
			return page, err
		}
		item.ID = ids[0].String()
		item.ProjectID = ids[1].String()
		item.UserID = ids[2].String()
		item.APIKeyID = ids[3].String()
		item.TaskKey = taskKey
		item.RootPrompt = rootPrompt
		item.RootPromptPreview = previewPrompt(rootPrompt, 200)
		item.Credits = domain.FormatCredits(creditMilli)
		if finishedAt.Valid {
			item.FinishedAt = &finishedAt.Time
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > f.Limit {
		last := page.Items[f.Limit-1]
		id, _ := uuid.Parse(last.ID)
		cursor := EncodeCursor(Cursor{CreatedAt: last.StartedAt, ID: id})
		page.NextCursor = &cursor
		page.Items = page.Items[:f.Limit]
	}
	return page, rows.Err()
}

func (s *Store) MentorCallTurn(ctx context.Context, mentorID, turnID uuid.UUID) (CallTurn, error) {
	var ids [4]uuid.UUID
	var creditMilli int64
	var taskKey, rootPrompt string
	var finishedAt sql.NullTime
	var item CallTurn
	err := s.db.QueryRowContext(ctx, `SELECT t.id,t.project_id,t.project_name,t.user_id,t.user_name,t.api_key_id,t.api_key_name,m.model_id,t.model_name,COALESCE(t.task_key,''),t.root_prompt,t.root_prompt_bytes,t.call_count,t.credit_milli,t.prompt_tokens,t.completion_tokens,t.total_tokens,t.outcome,t.started_at,t.finished_at FROM gateway_call_turns t JOIN models m ON m.id=t.model_id WHERE t.id=$1 AND EXISTS(SELECT 1 FROM project_members pm WHERE pm.project_id=t.project_id AND pm.user_id=$2)`, turnID, mentorID).Scan(&ids[0], &ids[1], &item.ProjectName, &ids[2], &item.UserName, &ids[3], &item.APIKeyName, &item.ModelID, &item.ModelName, &taskKey, &rootPrompt, &item.RootPromptBytes, &item.CallCount, &creditMilli, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.Outcome, &item.StartedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CallTurn{}, domain.NewError(domain.CodeNotFound, "call turn not found")
	}
	if err != nil {
		return CallTurn{}, err
	}
	item.ID = ids[0].String()
	item.ProjectID = ids[1].String()
	item.UserID = ids[2].String()
	item.APIKeyID = ids[3].String()
	item.TaskKey = taskKey
	item.RootPrompt = rootPrompt
	item.RootPromptPreview = previewPrompt(rootPrompt, 200)
	item.Credits = domain.FormatCredits(creditMilli)
	if finishedAt.Valid {
		item.FinishedAt = &finishedAt.Time
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.request_id,c.turn_id,c.call_index,c.project_id,c.project_name,c.user_id,c.user_name,c.api_key_id,c.api_key_name,m.model_id,c.model_name,COALESCE(c.provider_name,''),c.protocol,c.request_path,c.multiplier_milli,c.credit_milli,c.prompt_tokens,c.completion_tokens,c.total_tokens,c.billing_state,c.outcome,COALESCE(c.error_category,''),COALESCE(c.error_message,''),c.created_at FROM gateway_calls c JOIN models m ON m.id=c.model_id WHERE c.turn_id=$1 ORDER BY c.call_index,c.created_at`, turnID)
	if err != nil {
		return CallTurn{}, err
	}
	defer rows.Close()
	for rows.Next() {
		call, err := scanCallLogRow(rows)
		if err != nil {
			return CallTurn{}, err
		}
		item.Calls = append(item.Calls, call)
	}
	return item, rows.Err()
}

func scanCallLogRow(rows *sql.Rows) (CallLog, error) {
	var ids [4]uuid.UUID
	var turnID uuid.NullUUID
	var multiplier, credits int64
	var promptTokens, completionTokens, totalTokens sql.NullInt64
	var item CallLog
	if err := rows.Scan(&ids[0], &item.RequestID, &turnID, &item.CallIndex, &ids[1], &item.ProjectName, &ids[2], &item.UserName, &ids[3], &item.APIKeyName, &item.ModelID, &item.ModelName, &item.ProviderName, &item.Protocol, &item.Path, &multiplier, &credits, &promptTokens, &completionTokens, &totalTokens, &item.BillingState, &item.Outcome, &item.ErrorCategory, &item.ErrorMessage, &item.CreatedAt); err != nil {
		return CallLog{}, err
	}
	item.ID = ids[0].String()
	item.ProjectID = ids[1].String()
	item.UserID = ids[2].String()
	item.APIKeyID = ids[3].String()
	if turnID.Valid {
		item.TurnID = turnID.UUID.String()
	}
	item.Multiplier = domain.FormatCredits(multiplier)
	item.Credits = domain.FormatCredits(credits)
	item.PromptTokens = scanOptionalInt(promptTokens)
	item.CompletionTokens = scanOptionalInt(completionTokens)
	item.TotalTokens = scanOptionalInt(totalTokens)
	return item, nil
}
