package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func resolveTurn(ctx context.Context, tx *sql.Tx, keyID uuid.UUID, correlation CallCorrelation, prompt string) (uuid.UUID, int, bool, error) {
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
	if previous := strings.TrimSpace(correlation.PreviousResponseID); previous != "" && correlation.QuestionState != QuestionNew {
		var turnID uuid.UUID
		var callIndex int
		err := tx.QueryRowContext(ctx, `SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`, previous, keyID).Scan(&turnID, &callIndex)
		if err == nil && turnID != uuid.Nil {
			if same, sameErr := continuesSameUserTurn(ctx, tx, turnID, prompt); sameErr != nil {
				return uuid.Nil, 0, false, sameErr
			} else if same {
				return turnID, callIndex + 1, false, nil
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, 0, false, err
		}
		var maxIndex int
		err = tx.QueryRowContext(ctx, `SELECT c.turn_id, MAX(c.call_index)
FROM gateway_calls c
WHERE c.api_key_id=$1 AND c.previous_response_id=$2 AND c.turn_id IS NOT NULL
  AND c.created_at > now() - interval '30 minutes'
GROUP BY c.turn_id
ORDER BY MAX(c.created_at) DESC
LIMIT 1`, keyID, previous).Scan(&turnID, &maxIndex)
		if err == nil && turnID != uuid.Nil {
			if same, sameErr := continuesSameUserTurn(ctx, tx, turnID, prompt); sameErr != nil {
				return uuid.Nil, 0, false, sameErr
			} else if same {
				return turnID, maxIndex + 1, false, nil
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, 0, false, err
		}
	}
	if taskKey := strings.TrimSpace(correlation.TaskKey); taskKey != "" {
		if turnID, callIndex, ok, err := resolveTurnByTaskKey(ctx, tx, keyID, correlation.ModelID, taskKey, prompt, correlation.QuestionState); err != nil {
			return uuid.Nil, 0, false, err
		} else if ok {
			return turnID, callIndex, false, nil
		}
	}
	return uuid.Must(uuid.NewV7()), 1, true, nil
}

func resolveTurnByTaskKey(ctx context.Context, tx *sql.Tx, keyID, modelID uuid.UUID, taskKey, prompt, questionState string) (uuid.UUID, int, bool, error) {
	// A finished reply starts a new row even when the wording matches a recent row.
	if questionState != QuestionNew {
		if turnID, callIndex, ok, err := resolveTurnByExactTaskKey(ctx, tx, keyID, modelID, taskKey); err != nil || ok {
			return turnID, callIndex, ok, err
		}
	}
	// Tool replay stays on the latest row. Prompt-only keys (p:) are not threads.
	if scope, ok := threadScope(taskKey); ok {
		return resolveClaudeSessionTurn(ctx, tx, keyID, modelID, scope, prompt, questionState)
	}
	return uuid.Nil, 0, false, nil
}

func threadScope(taskKey string) (string, bool) {
	taskKey = strings.TrimSpace(taskKey)
	if taskKey == "" || strings.HasPrefix(taskKey, "p:") {
		return "", false
	}
	if scope, ok := claudeSessionScope(taskKey); ok {
		return scope, true
	}
	if colon := strings.LastIndex(taskKey, ":"); colon > 0 && isPromptDigest(taskKey[colon+1:]) {
		return taskKey[:colon], true
	}
	if !strings.Contains(taskKey, ":") {
		return taskKey, true
	}
	return "", false
}

func isPromptDigest(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func claudeSessionScope(taskKey string) (string, bool) {
	taskKey = strings.TrimSpace(taskKey)
	if !strings.HasPrefix(taskKey, "cc:") {
		return "", false
	}
	rest := taskKey[len("cc:"):]
	if colon := strings.Index(rest, ":"); colon >= 0 {
		return taskKey[:len("cc:")+colon], true
	}
	return taskKey, true
}

func resolveClaudeSessionTurn(ctx context.Context, tx *sql.Tx, keyID, modelID uuid.UUID, sessionScope, prompt, questionState string) (uuid.UUID, int, bool, error) {
	// No new human text stays on the user-prompt row. A model switch does not
	// open a blank row. An empty title is filled later.
	if strings.TrimSpace(prompt) == "" || questionState == QuestionContinue {
		return joinLatestThreadTurn(ctx, tx, keyID, modelID, sessionScope)
	}
	turnID, maxIndex, root, replyClosed, idle, ok, err := latestQuestionTurn(ctx, tx, keyID, sessionScope, &modelID)
	if err != nil {
		return uuid.Nil, 0, false, err
	}
	if ok && !separatesQuestion(questionState, prompt, root, replyClosed, idle) {
		return turnID, maxIndex + 1, true, nil
	}
	if ok {
		return uuid.Nil, 0, false, nil
	}
	// This model has no row yet. Stay on the prompt row unless the text is a new question.
	turnID, maxIndex, root, replyClosed, idle, ok, err = latestQuestionTurn(ctx, tx, keyID, sessionScope, nil)
	if err != nil || !ok || separatesQuestion(questionState, prompt, root, replyClosed, idle) {
		return uuid.Nil, 0, false, nil
	}
	return turnID, maxIndex + 1, true, nil
}

func joinLatestThreadTurn(ctx context.Context, tx *sql.Tx, keyID, modelID uuid.UUID, sessionScope string) (uuid.UUID, int, bool, error) {
	turnID, maxIndex, ok, err := latestCallTurn(ctx, tx, keyID, sessionScope, &modelID)
	if err != nil || ok {
		if !ok {
			return uuid.Nil, 0, false, err
		}
		return turnID, maxIndex + 1, true, nil
	}
	turnID, maxIndex, ok, err = latestCallTurn(ctx, tx, keyID, sessionScope, nil)
	if err != nil || !ok {
		return uuid.Nil, 0, false, err
	}
	return turnID, maxIndex + 1, true, nil
}

func latestCallTurn(ctx context.Context, tx *sql.Tx, keyID uuid.UUID, sessionScope string, modelID *uuid.UUID) (uuid.UUID, int, bool, error) {
	query := `SELECT t.id, COALESCE(MAX(c.call_index), 0)
FROM gateway_call_turns t
LEFT JOIN gateway_calls c ON c.turn_id = t.id
WHERE t.api_key_id=$1
  AND (t.task_key = $2 OR t.task_key LIKE $2 || ':%')`
	args := []any{keyID, sessionScope}
	if modelID != nil {
		query += `
  AND (t.model_id = $3 OR EXISTS (
    SELECT 1 FROM gateway_calls same_model
    WHERE same_model.turn_id = t.id AND same_model.model_id = $3))`
		args = append(args, *modelID)
	}
	query += `
GROUP BY t.id
ORDER BY GREATEST(t.updated_at, COALESCE(MAX(c.created_at), t.started_at)) DESC
LIMIT 1`
	var turnID uuid.UUID
	var maxIndex int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&turnID, &maxIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, 0, false, nil
	}
	if err != nil {
		return uuid.Nil, 0, false, err
	}
	if turnID == uuid.Nil {
		return uuid.Nil, 0, false, nil
	}
	return turnID, maxIndex, true, nil
}

func latestQuestionTurn(ctx context.Context, tx *sql.Tx, keyID uuid.UUID, sessionScope string, modelID *uuid.UUID) (uuid.UUID, int, string, sql.NullBool, bool, bool, error) {
	query := `SELECT t.id, COALESCE(MAX(c.call_index), 0), COALESCE(t.root_prompt, ''),
(SELECT c2.reply_closed FROM gateway_calls c2 WHERE c2.turn_id = t.id ORDER BY c2.created_at DESC LIMIT 1),
(GREATEST(t.updated_at, COALESCE(MAX(c.created_at), t.started_at)) < now() - interval '3 minutes')
FROM gateway_call_turns t
LEFT JOIN gateway_calls c ON c.turn_id = t.id
WHERE t.api_key_id=$1
  AND (t.task_key = $2 OR t.task_key LIKE $2 || ':%')`
	args := []any{keyID, sessionScope}
	if modelID != nil {
		query += `
  AND t.model_id = $3`
		args = append(args, *modelID)
	}
	query += `
GROUP BY t.id
ORDER BY GREATEST(t.updated_at, COALESCE(MAX(c.created_at), t.started_at)) DESC
LIMIT 1`
	var turnID uuid.UUID
	var maxIndex int
	var root string
	var replyClosed sql.NullBool
	var idle bool
	err := tx.QueryRowContext(ctx, query, args...).Scan(&turnID, &maxIndex, &root, &replyClosed, &idle)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, 0, "", sql.NullBool{}, false, false, nil
	}
	if err != nil {
		return uuid.Nil, 0, "", sql.NullBool{}, false, false, err
	}
	if turnID == uuid.Nil {
		return uuid.Nil, 0, "", sql.NullBool{}, false, false, nil
	}
	return turnID, maxIndex, root, replyClosed, idle, true, nil
}

// separatesQuestion reports that this human text starts another review row.
// A finished reply is enough. The three-minute idle gap is only the fallback
// when the body has no assistant reply and the stored finish flag is missing.
// The same wording after a long idle stays put, so a retry does not duplicate.
func separatesQuestion(state, prompt, root string, replyClosed sql.NullBool, idle bool) bool {
	prompt = strings.TrimSpace(prompt)
	root = strings.TrimSpace(root)
	if prompt == "" || state == QuestionContinue || root == "" {
		return false
	}
	if state == QuestionNew {
		return true
	}
	if replyClosed.Valid {
		return replyClosed.Bool
	}
	return idle && root != prompt
}

func resolveTurnByExactTaskKey(ctx context.Context, tx *sql.Tx, keyID, modelID uuid.UUID, taskKey string) (uuid.UUID, int, bool, error) {
	var turnID uuid.UUID
	var maxIndex int
	err := tx.QueryRowContext(ctx, `SELECT t.id, COALESCE(MAX(c.call_index), 0)
FROM gateway_call_turns t
LEFT JOIN gateway_calls c ON c.turn_id = t.id
WHERE t.api_key_id=$1 AND t.task_key=$2 AND t.model_id=$3
  AND t.updated_at > now() - interval '30 minutes'
GROUP BY t.id
ORDER BY GREATEST(t.updated_at, COALESCE(MAX(c.created_at), t.started_at)) DESC
LIMIT 1`, keyID, taskKey, modelID).Scan(&turnID, &maxIndex)
	if err == nil && turnID != uuid.Nil {
		return turnID, maxIndex + 1, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, 0, false, nil
	}
	if err != nil {
		return uuid.Nil, 0, false, err
	}
	return uuid.Nil, 0, false, nil
}

func continuesSameUserTurn(ctx context.Context, tx *sql.Tx, turnID uuid.UUID, prompt string) (bool, error) {
	current := strings.TrimSpace(prompt)
	if current == "" {
		return true, nil
	}
	var root sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT root_prompt FROM gateway_call_turns WHERE id=$1`, turnID).Scan(&root); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	if !root.Valid || strings.TrimSpace(root.String) == "" {
		return true, nil
	}
	return strings.TrimSpace(root.String) == current, nil
}

func applyTurnBilling(ctx context.Context, tx *sql.Tx, turnID uuid.UUID, callIndex int, success bool, _ string, credit int64) error {
	if turnID == uuid.Nil {
		return nil
	}
	chargedCredit := int64(0)
	if success {
		chargedCredit = credit
	}
	outcome, err := resolveTurnOutcome(ctx, tx, turnID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE gateway_call_turns SET
call_count=GREATEST(call_count,$2),
credit_milli=credit_milli+$3,
outcome=$4,
finished_at=CASE WHEN $5 THEN now() ELSE finished_at END,
updated_at=now()
WHERE id=$1`, turnID, callIndex, chargedCredit, outcome, success)
	return err
}

const (
	// turnFailedCallRatio is the share of finished calls that marks the task
	// failed. Occasional stream drops under this stay on the call only.
	turnFailedCallRatio = 0.20
	// turnOutrageousLatency is one call's create-to-finish time. Past this the
	// task is treated as stuck, even when other calls succeeded.
	turnOutrageousLatency = 10 * time.Minute
)

func turnShowsFailure(succeeded, failed int, maxSeconds float64, stuck bool) bool {
	finished := succeeded + failed
	if finished > 0 && float64(failed)/float64(finished) > turnFailedCallRatio {
		return true
	}
	return stuck || maxSeconds >= turnOutrageousLatency.Seconds()
}

func resolveTurnOutcome(ctx context.Context, tx *sql.Tx, turnID uuid.UUID) (string, error) {
	var succeeded, failed int
	var maxSeconds sql.NullFloat64
	var stuck bool
	err := tx.QueryRowContext(ctx, `SELECT
count(*) FILTER (WHERE outcome = 'succeeded'),
count(*) FILTER (WHERE outcome NOT IN ('succeeded', 'pending')),
max(EXTRACT(EPOCH FROM (finalized_at - created_at))) FILTER (WHERE finalized_at IS NOT NULL),
COALESCE(bool_or(outcome = 'pending' AND created_at < now() - interval '10 minutes'), false)
FROM gateway_calls WHERE turn_id = $1`, turnID).Scan(&succeeded, &failed, &maxSeconds, &stuck)
	if err != nil {
		return "", err
	}
	slowest := 0.0
	if maxSeconds.Valid {
		slowest = maxSeconds.Float64
	}
	if turnShowsFailure(succeeded, failed, slowest, stuck) {
		return latestUnsuccessfulOutcome(ctx, tx, turnID)
	}
	if succeeded > 0 {
		return "succeeded", nil
	}
	if failed > 0 {
		return latestUnsuccessfulOutcome(ctx, tx, turnID)
	}
	return "pending", nil
}

func latestUnsuccessfulOutcome(ctx context.Context, tx *sql.Tx, turnID uuid.UUID) (string, error) {
	var outcome string
	err := tx.QueryRowContext(ctx, `SELECT outcome FROM gateway_calls
WHERE turn_id = $1 AND outcome NOT IN ('succeeded', 'pending')
ORDER BY created_at DESC LIMIT 1`, turnID).Scan(&outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return "upstream_failed", nil
	}
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func applyTurnTokenDelta(ctx context.Context, tx *sql.Tx, turnID uuid.UUID, prompt, completion, total int64) error {
	if turnID == uuid.Nil || (prompt == 0 && completion == 0 && total == 0) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE gateway_call_turns SET
prompt_tokens=prompt_tokens+$2,
completion_tokens=completion_tokens+$3,
total_tokens=total_tokens+$4,
updated_at=now()
WHERE id=$1`, turnID, prompt, completion, total)
	return err
}

// turnModelNamesSQL lists the models actually called for this user prompt,
// in the order they were first used. A model switch stays visible on the same row.
const turnModelNamesSQL = `COALESCE((SELECT string_agg(names.model_name, '、' ORDER BY names.first_at) FROM (SELECT c.model_name, MIN(c.created_at) AS first_at FROM gateway_calls c WHERE c.turn_id=t.id AND c.model_name<>'' GROUP BY c.model_name) names), t.model_name)`

func previewPrompt(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func scanOptionalInt(value sql.NullInt64) *int {
	if !value.Valid {
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
	where := []string{
		`EXISTS(SELECT 1 FROM project_members pm WHERE pm.project_id=t.project_id AND pm.user_id=$1)`,
		callReviewPromptSQL("t.root_prompt"),
	}
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
		args = append(args, *f.ModelID)
		n := len(args)
		where = append(where, fmt.Sprintf(`(EXISTS(SELECT 1 FROM models filter_model WHERE filter_model.id=t.model_id AND filter_model.model_id=$%d) OR EXISTS(SELECT 1 FROM gateway_calls filter_call JOIN models filter_model ON filter_model.id=filter_call.model_id WHERE filter_call.turn_id=t.id AND filter_model.model_id=$%d))`, n, n))
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
	query := `SELECT t.id,t.project_id,t.project_name,t.user_id,t.user_name,t.api_key_id,t.api_key_name,m.model_id,` + turnModelNamesSQL + `,COALESCE(t.task_key,''),t.root_prompt,t.root_prompt_bytes,t.call_count,t.credit_milli,t.prompt_tokens,t.completion_tokens,t.total_tokens,t.outcome,t.started_at,t.finished_at FROM gateway_call_turns t JOIN models m ON m.id=t.model_id WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY t.started_at DESC,t.id DESC LIMIT $%d", len(args))
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
	err := s.db.QueryRowContext(ctx, `SELECT t.id,t.project_id,t.project_name,t.user_id,t.user_name,t.api_key_id,t.api_key_name,m.model_id,`+turnModelNamesSQL+`,COALESCE(t.task_key,''),t.root_prompt,t.root_prompt_bytes,t.call_count,t.credit_milli,t.prompt_tokens,t.completion_tokens,t.total_tokens,t.outcome,t.started_at,t.finished_at FROM gateway_call_turns t JOIN models m ON m.id=t.model_id WHERE t.id=$1 AND EXISTS(SELECT 1 FROM project_members pm WHERE pm.project_id=t.project_id AND pm.user_id=$2)`, turnID, mentorID).Scan(&ids[0], &ids[1], &item.ProjectName, &ids[2], &item.UserName, &ids[3], &item.APIKeyName, &item.ModelID, &item.ModelName, &taskKey, &rootPrompt, &item.RootPromptBytes, &item.CallCount, &creditMilli, &item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.Outcome, &item.StartedAt, &finishedAt)
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
