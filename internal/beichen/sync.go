package beichen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const (
	sweepInterval = time.Minute
	sweepTimeout  = time.Minute
	jobTimeout    = 25 * time.Second
	leaseSeconds  = 90
)

type syncJob struct {
	provider   uuid.UUID
	credential uuid.UUID
	kind       string
}

type credentialRecord struct {
	ciphertext []byte
	updatedAt  time.Time
}

type fetchResult struct {
	save       func(context.Context, *sql.Tx) error
	err        error
	partialErr error
}

func (s *Service) Run(ctx context.Context) {
	if s.db == nil || s.crypto == nil {
		return
	}
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		sweep, cancel := context.WithTimeout(ctx, sweepTimeout)
		if err := s.syncJobs(sweep, nil); err != nil && ctx.Err() == nil {
			slog.Warn("北辰后台同步未完成", "code", domain.ErrorCode(err))
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sync executes a single bounded pass; running jobs are never started twice.
func (s *Service) Sync(ctx context.Context, providerID uuid.UUID) error {
	if providerID == uuid.Nil {
		return invalid("需要供应商 ID")
	}
	if s.db == nil || s.crypto == nil {
		return unavailable("北辰同步未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	if err := checkProvider(ctx, s.db, providerID); err != nil {
		return err
	}
	return s.syncJobs(ctx, &providerID)
}

func (s *Service) syncJobs(ctx context.Context, providerID *uuid.UUID) error {
	jobs, err := s.listJobs(ctx, providerID)
	if err != nil {
		return unavailable("读取北辰同步任务失败", err)
	}
	if providerID != nil && len(jobs) == 0 {
		return invalid("北辰供应商和凭据须为已启用状态")
	}
	failed, busy := 0, 0
	for len(jobs) > 0 {
		if ctx.Err() != nil {
			return unavailable("北辰同步达到时限，已保存完成的任务", ctx.Err())
		}
		batch := jobs[:min(4, len(jobs))]
		type outcome struct {
			running bool
			err     error
		}
		results := make(chan outcome, len(batch))
		for _, job := range batch {
			go func() {
				running, err := s.runJob(ctx, job, providerID != nil)
				results <- outcome{running, err}
			}()
		}
		for range batch {
			result := <-results
			if result.err != nil {
				failed++
			} else if result.running {
				busy++
			}
		}
		jobs = jobs[len(batch):]
	}
	if failed > 0 {
		return unavailable(fmt.Sprintf("有 %d 项北辰同步未完成，请查看数据状态", failed), nil)
	}
	if providerID != nil && busy > 0 {
		return domain.NewError(domain.CodeInvalidTransition, "已有北辰同步任务运行中")
	}
	return nil
}

func (s *Service) listJobs(ctx context.Context, providerID *uuid.UUID) ([]syncJob, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, c.id, job.kind
FROM providers p JOIN provider_credentials c ON c.provider_id = p.id
CROSS JOIN (VALUES ('billing', 1), ('usage', 2)) AS job(kind, priority)
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = job.kind
WHERE `+beichenProvider+` AND p.status = 'active' AND c.status = 'active'
  AND ($1::uuid IS NULL OR p.id = $1)
  AND ($1 IS NOT NULL OR (
    COALESCE(d.next_attempt, '-infinity'::timestamptz) <= clock_timestamp()
    AND (d.lease_until IS NULL OR d.lease_until <= clock_timestamp())
  ))
ORDER BY job.priority, COALESCE(d.next_attempt, '-infinity'::timestamptz), c.id
LIMIT CASE WHEN $1 IS NULL THEN 24 ELSE NULL END`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []syncJob
	for rows.Next() {
		var job syncJob
		if err := rows.Scan(&job.provider, &job.credential, &job.kind); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Service) runJob(ctx context.Context, job syncJob, manual bool) (bool, error) {
	lease := uuid.New()
	err := s.db.QueryRowContext(ctx, `
INSERT INTO beichen_datasets (credential_id, kind, lease_id, lease_until)
VALUES ($1, $2, $3, clock_timestamp() + $4::int * interval '1 second')
ON CONFLICT (credential_id, kind) DO UPDATE SET lease_id = EXCLUDED.lease_id, lease_until = EXCLUDED.lease_until
WHERE (beichen_datasets.lease_until IS NULL OR beichen_datasets.lease_until <= clock_timestamp())
  AND ($5::boolean OR beichen_datasets.next_attempt <= clock_timestamp())
RETURNING lease_id`, job.credential, job.kind, lease, leaseSeconds, manual).Scan(&lease)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var credential credentialRecord
	err = s.db.QueryRowContext(ctx, `
SELECT c.credential_ciphertext, c.updated_at
FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE c.id = $1 AND p.id = $2 AND p.status = 'active' AND c.status = 'active' AND `+beichenProvider,
		job.credential, job.provider).Scan(&credential.ciphertext, &credential.updatedAt)
	if err != nil {
		result := fetchResult{err: errors.New("读取已启用的北辰凭据失败")}
		return false, s.finishJob(ctx, job, lease, credential, result, manual)
	}
	key, err := s.crypto.DecryptCredential(credential.ciphertext)
	if err != nil {
		result := fetchResult{err: errors.New("北辰凭据解密失败")}
		return false, s.finishJob(ctx, job, lease, credential, result, manual)
	}
	deadline, cancel := context.WithTimeout(ctx, jobTimeout)
	result := s.fetchJob(deadline, job, credential, key, manual)
	cancel()
	return false, s.finishJob(ctx, job, lease, credential, result, manual)
}

func (s *Service) fetchJob(ctx context.Context, job syncJob, credential credentialRecord, key string, manual bool) fetchResult {
	switch job.kind {
	case "billing":
		return s.fetchBillingJob(ctx, job, credential.updatedAt, key)
	case "usage":
		return s.fetchUsage(ctx, job, key, manual)
	default:
		return fetchResult{err: errors.New("北辰同步任务类型无效")}
	}
}

func (s *Service) fetchBillingJob(ctx context.Context, job syncJob, revision time.Time, key string) fetchResult {
	requestedAt := s.now()
	body, billingErr := s.fetch(ctx, key, billingPath, nil)
	var info billingInfo
	if billingErr == nil {
		info, billingErr = parseBilling(body)
	}
	entries, catalogErr := s.fetchKeyCatalog(ctx, key)
	catalogAt := s.now().UTC()
	access, accessErr := s.loadPriceAccess(ctx, job.credential)
	group := billingPanelGroup(access.groupID, info)
	quote := panelQuote{groupID: group}
	switch {
	case billingErr != nil:
		quote.err = billingErr
	case catalogErr != nil:
		quote.err = catalogErr
	case accessErr != nil:
		quote.err = accessErr
	case (group != 0 || s.WebSessionConfigured()) && info.ObservedAt.Before(requestedAt.Add(-2*time.Second)):
		quote.err = errors.New("北辰 Key 账单报价早于本轮采样")
	default:
		quote = s.fetchChannelQuote(ctx, access, key, info, entries)
	}
	return fetchResult{err: billingErr, partialErr: errors.Join(quote.err, quote.verificationError), save: func(ctx context.Context, tx *sql.Tx) error {
		if catalogErr != nil {
			if err := recordDatasetError(ctx, tx, job.credential, "catalog", catalogErr.Error()); err != nil {
				return err
			}
		} else {
			if _, err := catalog.ReplaceCredentialEntries(ctx, tx, job.provider, job.credential, entries, catalogAt); err != nil {
				return err
			}
			if err := recordSuccess(ctx, tx, job.credential, "catalog"); err != nil {
				return err
			}
		}
		return saveBillingObservation(ctx, tx, job.credential, revision, info, billingErr, quote.groupID, quote)
	}}
}

func (s *Service) fetchKeyCatalog(ctx context.Context, key string) ([]catalog.ProviderEntry, error) {
	body, err := s.fetch(ctx, key, modelsPath, nil)
	if err != nil {
		return nil, err
	}
	entries, err := catalog.ParseBeichenModels(body)
	if err != nil {
		return nil, errors.New("北辰模型目录格式无效或超过 2000 条")
	}
	return entries, nil
}

func billingPanelGroup(mappedGroup int64, info billingInfo) int64 {
	if info.GroupID != nil && *info.GroupID > 0 {
		return *info.GroupID
	}
	return mappedGroup
}

func (s *Service) fetchUsage(ctx context.Context, job syncJob, key string, manual bool) fetchResult {
	today := s.now().In(beijing)
	type snapshot struct {
		day  string
		rows []usageRow
	}
	var complete []snapshot
	var failures []error
	offsets := []int{-1, -2, -3}
	if manual {
		offsets = []int{0, -1}
	}
	for _, offset := range offsets {
		day := today.AddDate(0, 0, offset).Format(dayLayout)
		query := url.Values{"start_date": {day}, "end_date": {day}, "days": {"4"}, "timezone": {"Asia/Shanghai"}}
		body, err := s.fetch(ctx, key, usagePath, query)
		if err == nil {
			var rows []usageRow
			rows, err = parseUsage(body, day)
			if err == nil {
				complete = append(complete, snapshot{day: day, rows: rows})
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s：%w", day, err))
		}
	}
	result := fetchResult{err: errors.Join(failures...)}
	if len(complete) > 0 {
		result.save = func(ctx context.Context, tx *sql.Tx) error {
			for _, day := range complete {
				if err := replaceUsageDay(ctx, tx, job, day.day, day.rows); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return result
}

func replaceUsageDay(ctx context.Context, tx *sql.Tx, job syncJob, day string, rows []usageRow) error {
	_, err := tx.ExecContext(ctx, `
DELETE FROM beichen_daily_usage
WHERE provider_id = $1 AND credential_id = $2 AND day = $3::text::date AND source IN ('usage', 'export')
`, job.provider, job.credential, day)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := saveUsage(ctx, tx, job.provider, job.credential, row, "usage", nil); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_usage_days (provider_id, credential_id, day, empty)
VALUES ($1, $2, $3::text::date, $4)
ON CONFLICT (provider_id, day, credential_id) DO UPDATE
SET empty = EXCLUDED.empty, synced_at = clock_timestamp()
`, job.provider, job.credential, day, len(rows) == 0)
	return err
}

func saveBilling(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, info billingInfo) error {
	effectiveAt, err := priceTimestamp(ctx, tx)
	if err != nil {
		return err
	}
	if err := saveMultiplier(ctx, tx, credentialID, info, effectiveAt); err != nil {
		return err
	}
	basis, prices, err := parsePricing(info.Pricing)
	if err != nil {
		return recordDatasetError(ctx, tx, credentialID, "prices", err.Error())
	}
	for _, row := range prices {
		if err := savePrice(ctx, tx, credentialID, row.Model, "upstream", basis, "billing", nil, row.Rates, effectiveAt); err != nil {
			return err
		}
	}
	return recordSuccess(ctx, tx, credentialID, "prices")
}

func saveMultiplier(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, info billingInfo, effectiveAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_multiplier_versions (credential_id, multiplier, timezone, effective_at)
SELECT $1::uuid, $2::text::numeric, NULLIF($3::text, ''), $4::timestamptz
WHERE NOT EXISTS (
  SELECT 1 FROM (
    SELECT multiplier, timezone FROM beichen_multiplier_versions WHERE credential_id = $1
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) previous
  WHERE previous.multiplier = $2::text::numeric AND previous.timezone IS NOT DISTINCT FROM NULLIF($3::text, '')
)`, credentialID, info.Multiplier.String(), info.Timezone, effectiveAt)
	return err
}

func recordDatasetError(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, kind, message string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id, kind, last_error) VALUES ($1, $2, $3)
ON CONFLICT (credential_id, kind) DO UPDATE SET last_error = EXCLUDED.last_error`, credentialID, kind, message)
	return err
}

func (s *Service) finishJob(ctx context.Context, job syncJob, lease uuid.UUID, credential credentialRecord, result fetchResult, manual bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if result.save != nil {
		var current time.Time
		err := tx.QueryRowContext(ctx, `
SELECT c.updated_at FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE p.id = $1 AND c.id = $2 AND c.status = 'active' AND p.status = 'active' AND `+beichenProvider+`
FOR SHARE OF p FOR NO KEY UPDATE OF c`, job.provider, job.credential).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err != nil || !credential.updatedAt.Equal(current) {
			result = fetchResult{err: errors.New("同步期间北辰凭据或供应商已变化")}
		}
	}
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `
SELECT lease_id = $3 AND lease_until > clock_timestamp() FROM beichen_datasets
WHERE credential_id = $1 AND kind = $2 AND lease_id IS NOT NULL FOR UPDATE`,
		job.credential, job.kind, lease).Scan(&ownsLease)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !ownsLease {
		return errors.New("北辰同步任务租约已失效")
	}
	if result.save != nil {
		if err := result.save(ctx, tx); err != nil {
			return err
		}
	}
	var message *string
	if result.err != nil {
		value := result.err.Error()
		message = &value
	}
	_, err = tx.ExecContext(ctx, `
UPDATE beichen_datasets SET
  last_success = CASE WHEN $3 THEN clock_timestamp() ELSE last_success END,
  last_error = $4, failure_count = CASE WHEN $6 THEN failure_count WHEN $4::text IS NULL THEN 0 ELSE LEAST(failure_count + 1, 6) END,
  next_attempt = CASE
    WHEN $6 THEN next_attempt
    WHEN $4 IS NULL AND kind = 'billing' THEN LEAST(
      clock_timestamp() + CASE WHEN EXISTS (
        SELECT 1 FROM beichen_datasets WHERE credential_id = $1 AND kind = 'prices' AND last_error IS NOT NULL
      ) THEN interval '2 minutes' ELSE $5::int * interval '1 minute' END,
      (SELECT CASE WHEN expires_at > clock_timestamp() THEN expires_at END
        FROM beichen_panel_snapshots WHERE credential_id = $1 ORDER BY effective_at DESC LIMIT 1))
    WHEN $4 IS NULL OR kind = 'usage' THEN clock_timestamp() + $5::int * interval '1 minute'
    ELSE clock_timestamp() + power(2, LEAST(failure_count, 6)) * interval '1 minute' END,
  lease_id = NULL, lease_until = NULL
WHERE credential_id = $1 AND kind = $2`, job.credential, job.kind,
		(result.save != nil && job.kind != "billing") || result.err == nil, message, refreshMinutes(job.kind), manual && job.kind == "usage")
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errors.Join(result.err, result.partialErr)
}

func refreshMinutes(kind string) int {
	if kind == "billing" {
		return 15
	}
	return 1440
}
