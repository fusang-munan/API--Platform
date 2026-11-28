package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type ProbeRun struct {
	State      string       `json:"state"`
	StartedAt  *time.Time   `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at"`
	ErrorCode  *string      `json:"error_code"`
	Budget     *ProbeBudget `json:"budget,omitempty"`
}

// ProbeBudget counts attempts reserved for dispatch across providers, including retries.
type ProbeBudget struct {
	Date      string `json:"date"`
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	Exhausted bool   `json:"exhausted"`
}

type ProbeResult struct {
	ProviderID   uuid.UUID       `json:"provider_id"`
	CredentialID uuid.UUID       `json:"credential_id"`
	Model        string          `json:"model"`
	Protocol     string          `json:"protocol"`
	StatusCode   int             `json:"status_code"`
	ErrorCode    *string         `json:"error_code"`
	DurationMS   int64           `json:"duration_ms"`
	CheckedAt    time.Time       `json:"checked_at"`
	LastPassedAt *time.Time      `json:"last_passed_at,omitempty"`
	Detail       json.RawMessage `json:"detail"`
}

type ProbesView struct {
	Run     ProbeRun      `json:"run"`
	Results []ProbeResult `json:"results"`
}

const activeProbeCatalog = `EXISTS (
  SELECT 1 FROM provider_model_catalog m JOIN provider_credentials c
    ON c.id = m.credential_id AND c.provider_id = m.provider_id
  WHERE m.provider_id = p.id AND m.status = 'active' AND c.status = 'active'
    AND m.catalog_source = 'key' AND m.last_seen_at >= c.updated_at
)`

// RequestProbes uses requested_at as the durable marker for a forced sweep.
// Requests join an existing manual sweep or queue behind an automatic sweep.
func (s *Service) RequestProbes(ctx context.Context, providerID uuid.UUID) (ProbeRun, error) {
	if s.db == nil || s.crypto == nil {
		return ProbeRun{}, unavailable("北辰探活未配置", nil)
	}
	if err := checkProvider(ctx, s.db, providerID); err != nil {
		return ProbeRun{}, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO beichen_probe_jobs AS job(provider_id, requested_at)
SELECT p.id, statement_timestamp() FROM providers p
WHERE p.id = $1 AND `+beichenProvider+` AND p.status = 'active' AND `+activeProbeCatalog+`
ON CONFLICT (provider_id) DO UPDATE SET requested_at = COALESCE(job.requested_at, clock_timestamp())`, providerID)
	if err != nil {
		return ProbeRun{}, unavailable("提交北辰探活失败", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ProbeRun{}, unavailable("提交北辰探活失败", err)
	}
	if count == 0 {
		return ProbeRun{}, invalid("请先启用北辰和渠道 Key，并成功同步该 Key 的模型目录")
	}
	run, err := readProbeRun(ctx, s.db, providerID)
	if err != nil {
		return ProbeRun{}, unavailable("读取北辰探活进度失败", err)
	}
	return run, nil
}

func (s *Service) LatestProbes(ctx context.Context, providerID uuid.UUID) (ProbesView, error) {
	if s.db == nil {
		return ProbesView{}, unavailable("北辰探活未配置", nil)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ProbesView{}, unavailable("读取北辰探活失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, providerID); err != nil {
		return ProbesView{}, err
	}
	view := ProbesView{Results: []ProbeResult{}}
	view.Run, err = readProbeRun(ctx, tx, providerID)
	if err == nil {
		view.Results, err = readRecentProbes(ctx, tx, providerID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ProbesView{}, unavailable("读取北辰探活失败", err)
	}
	return view, nil
}

func readProbeRun(ctx context.Context, db rowQueryer, providerID uuid.UUID) (ProbeRun, error) {
	var run ProbeRun
	err := db.QueryRowContext(ctx, `
SELECT CASE WHEN lease_until > statement_timestamp() THEN 'running'
            WHEN requested_at IS NOT NULL THEN 'queued'
            WHEN last_error IS NOT NULL THEN 'failed'
            WHEN finished_at IS NOT NULL THEN 'completed'
            ELSE 'idle' END,
       started_at, finished_at, last_error
FROM beichen_probe_jobs WHERE provider_id = $1`, providerID).Scan(&run.State, &run.StartedAt, &run.FinishedAt, &run.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		run = ProbeRun{State: "idle"}
		err = nil
	}
	if err != nil {
		return ProbeRun{}, err
	}
	budget, err := readProbeBudget(ctx, db, time.Now())
	if err != nil {
		return ProbeRun{}, err
	}
	run.Budget = &budget
	return run, nil
}

const probeBudgetUsedSQL = `SELECT count(*) FROM beichen_probe_usage
WHERE checked_at >= date_trunc('day', $1::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'
  AND checked_at < (date_trunc('day', $1::timestamptz AT TIME ZONE 'Asia/Shanghai') + interval '1 day') AT TIME ZONE 'Asia/Shanghai'`

func readProbeBudget(ctx context.Context, db rowQueryer, now time.Time) (ProbeBudget, error) {
	limit := probeDailyLimit()
	var used int
	if err := db.QueryRowContext(ctx, probeBudgetUsedSQL, now).Scan(&used); err != nil {
		return ProbeBudget{}, err
	}
	return probeBudget(used, limit, now), nil
}

func readRecentProbes(ctx context.Context, tx *sql.Tx, providerID uuid.UUID) ([]ProbeResult, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT r.provider_id, r.credential_id, r.model, r.protocol, r.status_code,
       COALESCE(r.detail->>'failure_code',r.error_code), r.duration_ms, r.checked_at,
       CASE WHEN r.last_passed_at >= c.updated_at THEN r.last_passed_at END, r.detail
FROM upstream_probes r
JOIN providers p ON p.id = r.provider_id AND p.status = 'active'
JOIN provider_credentials c ON c.id = r.credential_id AND c.provider_id = p.id AND c.status = 'active'
JOIN provider_model_catalog m ON m.provider_id = p.id AND m.credential_id = c.id
  AND m.model_id = r.model::citext AND m.model_id::text = r.model AND m.status = 'active'
WHERE r.provider_id = $1 AND r.checked_at >= c.updated_at AND m.catalog_source = 'key' AND m.last_seen_at >= c.updated_at
  AND (cardinality(m.supported_protocols) = 0 OR r.protocol = ANY(m.supported_protocols))
ORDER BY r.checked_at DESC, r.credential_id, r.model, r.protocol
LIMIT 100`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []ProbeResult{}
	for rows.Next() {
		var result ProbeResult
		if err := rows.Scan(&result.ProviderID, &result.CredentialID, &result.Model, &result.Protocol, &result.StatusCode,
			&result.ErrorCode, &result.DurationMS, &result.CheckedAt, &result.LastPassedAt, &result.Detail); err != nil {
			return nil, err
		}
		result.CheckedAt = result.CheckedAt.UTC()
		if result.ErrorCode != nil {
			code := classifiedProbeFailure(result.StatusCode, *result.ErrorCode)
			result.ErrorCode = &code
		}
		if result.LastPassedAt != nil {
			passedAt := result.LastPassedAt.UTC()
			result.LastPassedAt = &passedAt
		}
		results = append(results, result)
	}
	return results, rows.Err()
}
