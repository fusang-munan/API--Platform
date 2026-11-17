package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

type RepairTurnStats struct {
	TaskKeysBackfilled int
	PromptsFixed       int
	TurnsMerged        int
	TurnsRemoved       int
}

const repairTurnGap = 30 * time.Minute

func (s *Store) RepairCallTurnHistory(ctx context.Context, dryRun bool) (RepairTurnStats, error) {
	var stats RepairTurnStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer func() { _ = tx.Rollback() }()

	backfilled, err := backfillTurnTaskKeys(ctx, tx)
	if err != nil {
		return stats, err
	}
	stats.TaskKeysBackfilled = backfilled

	fixed, err := repairTurnPrompts(ctx, tx)
	if err != nil {
		return stats, err
	}
	stats.PromptsFixed = fixed

	merged, removed, err := mergeTurnsByTaskKey(ctx, tx)
	if err != nil {
		return stats, err
	}
	stats.TurnsMerged = merged
	stats.TurnsRemoved = removed

	fixedAfterMerge, err := repairTurnPrompts(ctx, tx)
	if err != nil {
		return stats, err
	}
	stats.PromptsFixed += fixedAfterMerge

	if dryRun {
		return stats, nil
	}
	return stats, tx.Commit()
}

func backfillTurnTaskKeys(ctx context.Context, tx *sql.Tx) (int, error) {
	result, err := tx.ExecContext(ctx, `
UPDATE gateway_call_turns t
SET task_key = c.task_key, updated_at = now()
FROM (
  SELECT DISTINCT ON (turn_id) turn_id, task_key
  FROM gateway_calls
  WHERE turn_id IS NOT NULL AND COALESCE(task_key, '') <> ''
  ORDER BY turn_id, call_index, created_at
) c
WHERE t.id = c.turn_id AND COALESCE(t.task_key, '') = ''`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func repairTurnPrompts(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM gateway_call_turns ORDER BY started_at`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	fixed := 0
	for rows.Next() {
		var turnID uuid.UUID
		if err := rows.Scan(&turnID); err != nil {
			return fixed, err
		}
		best, err := bestPromptForTurn(ctx, tx, turnID)
		if err != nil {
			return fixed, err
		}
		if best == "" {
			continue
		}
		result, err := tx.ExecContext(ctx, `
UPDATE gateway_call_turns
SET root_prompt = $1, root_prompt_bytes = $2, updated_at = now()
WHERE id = $3 AND (root_prompt = '' OR root_prompt LIKE 'You are Codex%' OR root_prompt LIKE 'You are a coding agent%' OR root_prompt_bytes > 1024)`,
			best, len([]byte(best)), turnID)
		if err != nil {
			return fixed, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			fixed++
		}
	}
	return fixed, rows.Err()
}

func bestPromptForTurn(ctx context.Context, tx *sql.Tx, turnID uuid.UUID) (string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT mi.content, mi.source
FROM monitored_inputs mi
JOIN gateway_calls c ON c.id = mi.call_id
WHERE c.turn_id = $1
ORDER BY c.call_index, c.created_at`, turnID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	bestText := ""
	bestScore := -1
	for rows.Next() {
		var content, source string
		if err := rows.Scan(&content, &source); err != nil {
			return "", err
		}
		score := scoreStoredPrompt(content, source)
		if score > bestScore {
			bestScore = score
			bestText = normalizeStoredUserPrompt(content)
		}
	}
	return bestText, rows.Err()
}

type turnRow struct {
	ID        uuid.UUID
	APIKeyID  uuid.UUID
	TaskKey   string
	StartedAt time.Time
}

func mergeTurnsByTaskKey(ctx context.Context, tx *sql.Tx) (mergedGroups, removedTurns int, err error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, api_key_id, COALESCE(task_key, ''), started_at
FROM gateway_call_turns
WHERE COALESCE(task_key, '') <> ''
ORDER BY api_key_id, task_key, started_at, id`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var batch []turnRow
	var currentKey string
	var currentAPI uuid.UUID
	flush := func() error {
		if len(batch) < 2 {
			batch = batch[:0]
			return nil
		}
		groups := clusterTurnRows(batch)
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			if err := mergeTurnGroup(ctx, tx, group); err != nil {
				return err
			}
			mergedGroups++
			removedTurns += len(group) - 1
		}
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		var row turnRow
		if err := rows.Scan(&row.ID, &row.APIKeyID, &row.TaskKey, &row.StartedAt); err != nil {
			return mergedGroups, removedTurns, err
		}
		key := row.APIKeyID.String() + "\x00" + row.TaskKey
		if currentKey != "" && key != currentKey {
			if err := flush(); err != nil {
				return mergedGroups, removedTurns, err
			}
		}
		currentKey = key
		currentAPI = row.APIKeyID
		_ = currentAPI
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return mergedGroups, removedTurns, err
	}
	if err := flush(); err != nil {
		return mergedGroups, removedTurns, err
	}
	return mergedGroups, removedTurns, nil
}

func clusterTurnRows(rows []turnRow) [][]turnRow {
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].ID.String() < rows[j].ID.String()
		}
		return rows[i].StartedAt.Before(rows[j].StartedAt)
	})
	var groups [][]turnRow
	current := []turnRow{rows[0]}
	for i := 1; i < len(rows); i++ {
		gap := rows[i].StartedAt.Sub(current[len(current)-1].StartedAt)
		if gap > repairTurnGap {
			groups = append(groups, current)
			current = []turnRow{rows[i]}
			continue
		}
		current = append(current, rows[i])
	}
	groups = append(groups, current)
	return groups
}

func mergeTurnGroup(ctx context.Context, tx *sql.Tx, group []turnRow) error {
	sort.Slice(group, func(i, j int) bool {
		if group[i].StartedAt.Equal(group[j].StartedAt) {
			return group[i].ID.String() < group[j].ID.String()
		}
		return group[i].StartedAt.Before(group[j].StartedAt)
	})
	canonical := group[0].ID
	for _, duplicate := range group[1:] {
		if err := reassignCalls(ctx, tx, duplicate.ID, canonical); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_call_turns WHERE id = $1`, duplicate.ID); err != nil {
			return err
		}
	}
	return recomputeTurn(ctx, tx, canonical)
}

func reassignCalls(ctx context.Context, tx *sql.Tx, fromTurn, toTurn uuid.UUID) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM gateway_calls WHERE turn_id = $1 ORDER BY created_at, call_index`, fromTurn)
	if err != nil {
		return err
	}
	defer rows.Close()
	var maxIndex int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(call_index), 0) FROM gateway_calls WHERE turn_id = $1`, toTurn).Scan(&maxIndex); err != nil {
		return err
	}
	for rows.Next() {
		var callID uuid.UUID
		if err := rows.Scan(&callID); err != nil {
			return err
		}
		maxIndex++
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_calls SET turn_id = $1, call_index = $2 WHERE id = $3`, toTurn, maxIndex, callID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func recomputeTurn(ctx context.Context, tx *sql.Tx, turnID uuid.UUID) error {
	var (
		callCount     int
		creditMilli   int64
		promptTokens  int64
		completionTok int64
		totalTokens   int64
		startedAt     time.Time
		finishedAt    sql.NullTime
		outcome       string
	)
	err := tx.QueryRowContext(ctx, `
SELECT
  COALESCE(MAX(call_index), 0),
  COALESCE(SUM(credit_milli), 0),
  COALESCE(SUM(COALESCE(prompt_tokens, 0)), 0),
  COALESCE(SUM(COALESCE(completion_tokens, 0)), 0),
  COALESCE(SUM(COALESCE(total_tokens, 0)), 0),
  MIN(created_at),
  MAX(finalized_at)
FROM gateway_calls WHERE turn_id = $1`, turnID).Scan(
		&callCount, &creditMilli, &promptTokens, &completionTok, &totalTokens, &startedAt, &finishedAt,
	)
	if err != nil {
		return err
	}
	if callCount == 0 {
		return fmt.Errorf("turn %s has no calls after merge", turnID)
	}
	outcome, err = resolveTurnOutcome(ctx, tx, turnID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE gateway_call_turns SET
  call_count = $2,
  credit_milli = $3,
  prompt_tokens = $4,
  completion_tokens = $5,
  total_tokens = $6,
  started_at = $7,
  finished_at = $8,
  outcome = $9,
  updated_at = now()
WHERE id = $1`,
		turnID, callCount, creditMilli, promptTokens, completionTok, totalTokens, startedAt, finishedAt, outcome,
	)
	return err
}
