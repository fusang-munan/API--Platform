package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

const (
	probePollInterval      = 5 * time.Second
	probeParallelism       = 4
	probeDBTimeout         = 15 * time.Second
	probeAttemptTimeout    = 8 * time.Second
	defaultProbeDailyLimit = 500
	probeFailureRetries    = 2
	probeRetryDelay        = 2 * time.Second
)

// probeDailyLimit reads the cap on each call so a changed environment is
// picked up without rebuilding the service. Invalid values fall back to the
// default; config.Load still rejects them at server startup.
func probeDailyLimit() int {
	raw := strings.TrimSpace(os.Getenv("BEICHEN_PROBE_DAILY_LIMIT"))
	if raw == "" {
		return defaultProbeDailyLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultProbeDailyLimit
	}
	return n
}

func probeBudget(used, limit int, now time.Time) ProbeBudget {
	if limit < 1 {
		limit = defaultProbeDailyLimit
	}
	if used < 0 {
		used = 0
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return ProbeBudget{
		Date: now.In(beijing).Format(dayLayout), Used: used, Limit: limit,
		Remaining: remaining, Exhausted: remaining == 0,
	}
}

type probeJob struct {
	provider, lease uuid.UUID
	startedAt       time.Time
	force           bool
}

type probeTarget struct {
	credential uuid.UUID
	revision   time.Time
	lastCheck  time.Time
	model      string
	protocol   string
	registered string
	client     *catalog.RouteProbe
}

type probeObservation struct {
	target     probeTarget
	statusCode int
	errorCode  string
	durationMS int64
}

func (s *Service) RunProbes(ctx context.Context, automatic bool) {
	if s.db == nil || s.crypto == nil {
		return
	}
	ticker := time.NewTicker(probePollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, probeDBTimeout)
		job, err := s.claimProbeJob(claimCtx, automatic)
		cancel()
		switch {
		case err == nil:
			s.runProbeJob(ctx, job)
		case !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil:
			slog.Warn("北辰探活无法领取任务")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) claimProbeJob(ctx context.Context, automatic bool) (probeJob, error) {
	job := probeJob{lease: uuid.New()}
	err := s.db.QueryRowContext(ctx, `
WITH candidate AS (
  SELECT p.id FROM providers p LEFT JOIN beichen_probe_jobs q ON q.provider_id = p.id
  WHERE `+beichenProvider+` AND (q.lease_until IS NULL OR q.lease_until <= statement_timestamp())
    AND (q.requested_at IS NOT NULL OR ($1 AND p.status = 'active' AND `+activeProbeCatalog+`
      AND (q.finished_at IS NULL OR q.started_at <= statement_timestamp() - interval '1 hour')))
  ORDER BY q.requested_at NULLS LAST, q.started_at NULLS FIRST, p.id
  LIMIT 1
)
INSERT INTO beichen_probe_jobs AS q(provider_id, started_at, lease_id, lease_until)
SELECT id, statement_timestamp(), $2, clock_timestamp() + interval '60 seconds' FROM candidate
ON CONFLICT (provider_id) DO UPDATE
SET started_at = CASE WHEN q.finished_at IS NULL AND q.started_at IS NOT NULL THEN q.started_at ELSE EXCLUDED.started_at END,
    finished_at = NULL, last_error = NULL, lease_id = EXCLUDED.lease_id, lease_until = EXCLUDED.lease_until
WHERE (q.lease_until IS NULL OR q.lease_until <= clock_timestamp())
  AND (q.requested_at IS NOT NULL OR ($1 AND
    (q.finished_at IS NULL OR q.started_at <= clock_timestamp() - interval '1 hour')))
RETURNING provider_id, started_at, COALESCE(requested_at <= started_at, false)`, automatic, job.lease).
		Scan(&job.provider, &job.startedAt, &job.force)
	return job, err
}

func (s *Service) runProbeJob(ctx context.Context, job probeJob) {
	code, err := s.executeProbeJob(ctx, job)
	if err != nil {
		code = "incomplete"
	}
	// An interrupted sweep keeps its starting time and manual request so a
	// replacement worker can resume and skip the results already saved.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeDBTimeout)
	defer cancel()
	_, finishErr := s.db.ExecContext(finishCtx, `
UPDATE beichen_probe_jobs
SET requested_at = CASE WHEN $4 OR NOT $5 THEN requested_at END,
    finished_at = CASE WHEN NOT $4 THEN clock_timestamp() END,
    last_error = CASE WHEN NOT $4 THEN NULLIF($3, '') END, lease_id = NULL, lease_until = NULL
WHERE provider_id = $1 AND lease_id = $2`, job.provider, job.lease, code, ctx.Err() != nil, job.force)
	if finishErr != nil || (err != nil && ctx.Err() == nil) {
		slog.Warn("北辰探活任务未完成")
	}
}

func (s *Service) executeProbeJob(ctx context.Context, job probeJob) (string, error) {
	listCtx, cancel := context.WithTimeout(ctx, probeDBTimeout)
	targets, hasTargets, err := s.listProbeTargets(listCtx, job)
	cancel()
	if err != nil {
		return "", err
	}
	if !hasTargets {
		return "no_token_protocols", nil
	}
	for len(targets) > 0 {
		budgetCtx, cancel := context.WithTimeout(ctx, probeDBTimeout)
		budget, err := readProbeBudget(budgetCtx, s.db)
		cancel()
		if err != nil {
			return "", err
		}
		batch := make([]probeTarget, 0, probeParallelism)
		for len(targets) > 0 && len(batch) < probeParallelism {
			target := targets[0]
			targets = targets[1:]
			if target.lastCheck.In(beijing).Format(dayLayout) != budget.Date {
				if budget.Remaining <= 0 {
					continue
				}
				budget.Remaining--
			}
			batch = append(batch, target)
		}
		if len(batch) == 0 {
			break
		}
		leaseCtx, cancel := context.WithTimeout(ctx, probeDBTimeout)
		err = renewProbeLease(leaseCtx, s.db, job)
		cancel()
		if err != nil {
			return "", err
		}
		observations := make([]probeObservation, len(batch))
		var wg sync.WaitGroup
		for i, target := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				observations[i] = observeTarget(ctx, target)
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		retryFailedProbes(ctx, observations, probeRetryDelay)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		saveCtx, cancel := context.WithTimeout(ctx, probeDBTimeout)
		err = s.saveProbeBatch(saveCtx, job, observations)
		cancel()
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

// retryFailedProbes re-checks transient failures before they become the
// availability verdict, so a short upstream blip cannot hide models until the
// next hourly sweep. Deterministic failures (auth, missing model, validation)
// are recorded on the first attempt.
func retryFailedProbes(ctx context.Context, observations []probeObservation, delay time.Duration) {
	for attempt := 0; attempt < probeFailureRetries; attempt++ {
		pending := make([]int, 0, len(observations))
		for i := range observations {
			if probeFailureRetryable(observations[i]) {
				pending = append(pending, i)
			}
		}
		if len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay * time.Duration(attempt+1)):
		}
		if ctx.Err() != nil {
			return
		}
		var wg sync.WaitGroup
		for _, i := range pending {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				observations[i] = observeTarget(ctx, observations[i].target)
			}(i)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
	}
}

// probeFailureRetryable reports failures that can heal on their own.
func probeFailureRetryable(observation probeObservation) bool {
	status := observation.statusCode
	if observation.errorCode == "credential_unreadable" || (status >= 400 && status < 500 && status != 408 && status != 429) {
		return false
	}
	return observation.errorCode == "network_error" || observation.errorCode == "timeout" ||
		status == 408 || status == 429 || (status >= 500 && status < 600)
}

func (s *Service) listProbeTargets(ctx context.Context, job probeJob) ([]probeTarget, bool, error) {
	// A manual request only de-duplicates results saved by the same sweep
	// before its worker was interrupted. Automatic sweeps also skip recent results.
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.updated_at, c.credential_ciphertext, m.model_id::text, m.raw_entry, to_json(m.supported_protocols),
       prior.done, prior.checks
FROM providers p JOIN provider_credentials c ON c.provider_id = p.id
JOIN provider_model_catalog m ON m.provider_id = p.id AND m.credential_id = c.id
CROSS JOIN LATERAL (
  SELECT COALESCE(json_agg(r.protocol) FILTER (WHERE r.checked_at >= c.updated_at
    AND (r.checked_at >= $2::timestamptz OR (NOT $3::boolean AND r.checked_at >= statement_timestamp() - interval '1 hour'))), '[]') AS done,
    COALESCE(json_object_agg(r.protocol, r.checked_at), '{}') AS checks
  FROM upstream_probes r
  WHERE r.provider_id = p.id AND r.credential_id = c.id AND r.model = m.model_id::text
) prior
WHERE p.id = $1 AND `+beichenProvider+` AND p.status = 'active' AND c.status = 'active' AND m.status = 'active'
  AND m.catalog_source = 'key'
  AND m.last_seen_at >= c.updated_at
ORDER BY c.id, m.model_id`, job.provider, job.startedAt, job.force)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var targets []probeTarget
	var currentKey uuid.UUID
	var client *catalog.RouteProbe
	hasTargets := false
	for rows.Next() {
		var target probeTarget
		var ciphertext, raw, protocols, done, priorChecks []byte
		if err := rows.Scan(&target.credential, &target.revision, &ciphertext, &target.model, &raw, &protocols, &done, &priorChecks); err != nil {
			return nil, false, err
		}
		entry, err := storedCatalogEntry(target.model, raw, protocols)
		if err != nil {
			return nil, false, err
		}
		if !validModel(target.model) {
			continue
		}
		if currentKey != target.credential {
			currentKey = target.credential
			client = nil
			key, err := s.crypto.DecryptCredential(ciphertext)
			key = strings.TrimSpace(key)
			if err == nil && key != "" && len(key) <= 4096 && !strings.ContainsAny(key, "\r\n") {
				client = &catalog.RouteProbe{BaseURL: origin, Credential: key, Client: s.client}
			}
		}
		var checked []string
		if err := json.Unmarshal(done, &checked); err != nil {
			return nil, false, err
		}
		var lastChecks map[string]time.Time
		if err := json.Unmarshal(priorChecks, &lastChecks); err != nil {
			return nil, false, err
		}
		target.client = client
		registered := catalog.RegisteredProtocols(entry)
		if len(entry.SupportedProtocols) == 0 {
			encoded, err := json.Marshal(registered)
			if err != nil {
				return nil, false, err
			}
			target.registered = string(encoded)
		}
		for _, protocol := range registered {
			if _, ok := catalog.TokenProbeEndpoint(protocol); !ok {
				continue
			}
			hasTargets = true
			if !slices.Contains(checked, protocol) {
				target.protocol = protocol
				target.lastCheck = lastChecks[protocol]
				targets = append(targets, target)
			}
		}
	}
	return targets, hasTargets, rows.Err()
}

func storedCatalogEntry(model string, raw, protocols []byte) (catalog.ProviderEntry, error) {
	entry := catalog.ProviderEntry{ID: model}
	err := json.Unmarshal(protocols, &entry.SupportedProtocols)
	if err == nil && len(entry.SupportedProtocols) == 0 {
		err = json.Unmarshal(raw, &entry.Raw)
	}
	return entry, err
}

func observeTarget(ctx context.Context, target probeTarget) probeObservation {
	result := probeObservation{target: target}
	if target.client == nil {
		result.errorCode = "credential_unreadable"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, probeAttemptTimeout)
	defer cancel()
	step, err := target.client.ProbeTokenStep(ctx, target.model, target.protocol)
	if err != nil {
		result.errorCode = "network_error"
		return result
	}
	result.statusCode, result.errorCode, result.durationMS = step.HTTPStatus, step.FailureCode, step.DurationMS
	return result
}

func renewProbeLease(ctx context.Context, db rowQueryer, job probeJob) error {
	var owner uuid.UUID
	return db.QueryRowContext(ctx, `
UPDATE beichen_probe_jobs SET lease_until = clock_timestamp() + interval '60 seconds'
WHERE provider_id = $1 AND lease_id = $2 AND lease_until > clock_timestamp()
RETURNING provider_id`, job.provider, job.lease).Scan(&owner)
}

func (s *Service) saveProbeBatch(ctx context.Context, job probeJob, observations []probeObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := renewProbeLease(ctx, tx, job); err != nil {
		return err
	}
	for _, result := range observations {
		target := result.target
		var key uuid.UUID
		err := tx.QueryRowContext(ctx, `
SELECT c.id FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE p.id = $1 AND `+beichenProvider+` AND p.status = 'active' AND c.id = $2 AND c.status = 'active' AND c.updated_at = $3
FOR SHARE OF p FOR NO KEY UPDATE OF c`, job.provider, target.credential, target.revision).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
WITH saved AS (
  INSERT INTO upstream_probes(provider_id, credential_id, model, protocol, status_code, error_code, duration_ms, checked_at, detail, last_passed_at)
  SELECT $1, $2, m.model_id::text, $4, $5, NULLIF($6, ''), $7, statement_timestamp(), '{"max_output_tokens":1}'::jsonb,
    CASE WHEN NULLIF($6, '') IS NULL AND $5 BETWEEN 200 AND 299 THEN statement_timestamp() END
  FROM provider_model_catalog m
  WHERE m.provider_id = $1 AND m.credential_id = $2 AND m.model_id = $3::citext
    AND m.model_id::text = $3::text AND m.status = 'active' AND m.catalog_source = 'key'
  ON CONFLICT (provider_id, credential_id, model, protocol) DO UPDATE SET status_code = EXCLUDED.status_code,
    error_code = EXCLUDED.error_code, duration_ms = EXCLUDED.duration_ms, checked_at = EXCLUDED.checked_at, detail = EXCLUDED.detail,
    last_passed_at = COALESCE(EXCLUDED.last_passed_at,
      CASE WHEN upstream_probes.last_passed_at >= $9::timestamptz THEN upstream_probes.last_passed_at END)
  RETURNING error_code
)
UPDATE provider_model_catalog m SET supported_protocols = CASE
  WHEN cardinality(m.supported_protocols) = 0 AND $8::text <> ''
    THEN ARRAY(SELECT jsonb_array_elements_text(NULLIF($8::text, '')::jsonb))
  ELSE array_append(m.supported_protocols, $4) END
WHERE m.provider_id = $1 AND m.credential_id = $2 AND m.model_id = $3::citext
  AND NOT ($4 = ANY(m.supported_protocols)) AND EXISTS (SELECT 1 FROM saved WHERE error_code IS NULL)`,
			job.provider, key, target.model, target.protocol, result.statusCode, result.errorCode, result.durationMS, target.registered, target.revision); err != nil {
			return err
		}
	}
	return tx.Commit()
}
