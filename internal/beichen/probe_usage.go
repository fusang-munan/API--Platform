package beichen

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/google/uuid"
)

// reserveProbeAttempts persists a slot before dispatch. A worker that dies between
// reservation and recording the response leaves an identifiable unknown outcome.
// The transaction lock covers all providers; it is released before network I/O.
func (s *Service) reserveProbeAttempts(ctx context.Context, job probeJob, targets []probeTarget) ([]uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(ctx, probeDBTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(660066, 1)"); err != nil {
		return nil, err
	}
	if err := renewProbeLease(ctx, tx, job); err != nil {
		return nil, err
	}
	var now time.Time
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT statement_timestamp(), count(*) FROM beichen_probe_usage WHERE job_id = $1", job.id).Scan(&now, &used); err != nil {
		return nil, err
	}
	budget, err := readProbeBudget(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	count := min(len(targets), budget.Remaining, max(0, probeSweepLimit-used))
	ids := make([]uuid.UUID, count)
	for i, target := range targets[:count] {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		ids[i] = id
		if _, err := tx.ExecContext(ctx, `
INSERT INTO beichen_probe_usage(id, provider_id, credential_id, model, protocol, job_id, checked_at, error_code)
VALUES ($1,$2,$3,$4,$5,$6,$7,'outcome_unknown')`, id, job.provider, target.credential, target.model, target.protocol, job.id, now); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// Each retry enters here as a new attempt. Finishing a row is independent of the
// sweep lease, key revision and parent cancellation: those only govern availability.
func (s *Service) runProbeRound(ctx context.Context, job probeJob, targets []probeTarget) ([]probeObservation, error) {
	ids, err := s.reserveProbeAttempts(ctx, job, targets)
	if err != nil {
		return nil, err
	}
	results := make([]probeObservation, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := observeTarget(ctx, targets[i])
			results[i] = result
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeDBTimeout)
			defer cancel()
			_, errs[i] = s.db.ExecContext(saveCtx, `
UPDATE beichen_probe_usage SET status_code=NULLIF($2::integer,0), error_code=NULLIF($3,''),
  input_tokens=$4, output_tokens=$5, cache_read_tokens=$6, cache_write_tokens=$7 WHERE id=$1`,
				id, result.statusCode, result.errorCode, result.usage.InputTokens, result.usage.OutputTokens,
				result.usage.CacheReadTokens, result.usage.CacheWriteTokens)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// ProbeTokenTotal sums reported counts only. Reported counts rows with an explicit
// counter (including zero); Tokens is nil when none were reported.
type ProbeTokenTotal struct {
	Tokens   *int64
	Reported int64
}

type ProbeUsageDay struct {
	Attempts                             int64
	Input, Output, CacheRead, CacheWrite ProbeTokenTotal
}

// ProbeUsageForDay reads the provider's independent bucket for a Shanghai calendar day.
// Input is the protocol-native input count and may already include cache hits.
func (s *Service) ProbeUsageForDay(ctx context.Context, providerID uuid.UUID, day string) (ProbeUsageDay, error) {
	start, err := dayStart(day)
	if err != nil {
		return ProbeUsageDay{}, err
	}
	if s.db == nil {
		return ProbeUsageDay{}, unavailable("北辰探活未配置", nil)
	}
	var result ProbeUsageDay
	err = s.db.QueryRowContext(ctx, `
SELECT count(*), sum(input_tokens), count(input_tokens), sum(output_tokens), count(output_tokens),
       sum(cache_read_tokens), count(cache_read_tokens), sum(cache_write_tokens), count(cache_write_tokens)
FROM beichen_probe_usage WHERE provider_id=$1 AND checked_at >= $2 AND checked_at < $3`,
		providerID, start, start.AddDate(0, 0, 1)).Scan(&result.Attempts, &result.Input.Tokens, &result.Input.Reported,
		&result.Output.Tokens, &result.Output.Reported, &result.CacheRead.Tokens, &result.CacheRead.Reported,
		&result.CacheWrite.Tokens, &result.CacheWrite.Reported)
	return result, err
}
