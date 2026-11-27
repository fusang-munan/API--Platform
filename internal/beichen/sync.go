package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
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
	sweepInterval        = time.Minute
	sweepTimeout         = time.Minute
	jobTimeout           = 25 * time.Second
	leaseSeconds         = 90
	billingAlertFailures = 3
)

type syncJob struct {
	provider       uuid.UUID
	credential     uuid.UUID
	kind           string
	target         KeyUsageTarget
	websiteCatalog bool
}

func (j syncJob) leaseKind() string {
	if j.kind == "catalog" {
		return "billing"
	}
	return j.kind
}

type credentialRecord struct {
	ciphertext []byte
	updatedAt  time.Time
}

type fetchResult struct {
	save        func(context.Context, *sql.Tx) error
	err         error
	partialErr  error
	accountRate *billingAccountRateObservation
}

var errBillingKeyBindingChanged = errors.New("同步期间北辰官网 Key 或分组已变化")

func (s *Service) Run(ctx context.Context) {
	if s.db == nil || s.crypto == nil {
		return
	}
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		sweep, cancel := context.WithTimeout(ctx, sweepTimeout)
		if err := s.syncWithWebsite(sweep, nil); err != nil && ctx.Err() == nil {
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
	return s.syncWithWebsite(ctx, &providerID)
}

func (s *Service) syncWithWebsite(ctx context.Context, provider *uuid.UUID) error {
	website := make(chan error, 1)
	go func() { website <- s.syncWebsiteDirectories(ctx, provider) }()
	return errors.Join(s.syncJobs(ctx, provider), <-website)
}

// SyncCredential pulls this Key's catalog, pricing, multiplier and recent
// usage using the same leases as the scheduled jobs.
func (s *Service) SyncCredential(ctx context.Context, providerID, credentialID uuid.UUID) error {
	if providerID == uuid.Nil || credentialID == uuid.Nil {
		return invalid("需要有效的供应商和渠道 ID")
	}
	if s.db == nil || s.crypto == nil {
		return unavailable("北辰同步未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	if err := checkActiveSyncCredential(ctx, s.db, providerID, credentialID); err != nil {
		return err
	}
	discovery, finish := context.WithTimeout(ctx, receiptKeyTimeout)
	discoveryErr := s.discoverChannelKeys(discovery, &credentialID)
	finish()
	if err := s.executeSyncJobs(ctx, []syncJob{
		{provider: providerID, credential: credentialID, kind: "billing"},
		{provider: providerID, credential: credentialID, kind: "key_usage"},
	}, true); err != nil {
		return err
	}
	if discoveryErr != nil {
		return unavailable("渠道已同步，但北辰官网的 Key 身份待确认", discoveryErr)
	}
	return nil
}

func (s *Service) syncJobs(ctx context.Context, providerID *uuid.UUID) error {
	jobs, err := s.listJobs(ctx, providerID)
	if err != nil {
		return unavailable("读取北辰同步任务失败", err)
	}
	if providerID != nil && len(jobs) == 0 {
		return invalid("北辰供应商和凭据须为已启用状态")
	}
	return s.executeSyncJobs(ctx, jobs, providerID != nil)
}

func (s *Service) executeSyncJobs(ctx context.Context, jobs []syncJob, manual bool) error {
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
				running, err := s.runJob(ctx, job, manual)
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
	if manual && busy > 0 {
		return domain.NewError(domain.CodeInvalidTransition, "已有北辰同步任务运行中")
	}
	return nil
}

func (s *Service) listJobs(ctx context.Context, providerID *uuid.UUID) ([]syncJob, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, c.id, job.kind
FROM providers p JOIN provider_credentials c ON c.provider_id = p.id
CROSS JOIN (VALUES ('billing', 1), ('key_usage', 2)) AS job(kind, priority)
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
RETURNING lease_id`, job.credential, job.leaseKind(), lease, leaseSeconds, manual).Scan(&lease)
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
		if job.kind == "billing" || job.kind == "catalog" {
			result.save = func(ctx context.Context, tx *sql.Tx) error {
				if err := recordDatasetError(ctx, tx, job.credential, "catalog", result.err.Error()); err != nil {
					return err
				}
				if job.kind == "billing" {
					return saveBillingObservation(ctx, tx, job.credential, credential.updatedAt, billingInfo{}, result.err, 0, panelQuote{})
				}
				return nil
			}
		}
		if job.kind == "key_usage" {
			result.save = func(ctx context.Context, tx *sql.Tx) error {
				digest := sha256.Sum256(credential.ciphertext)
				_, err := tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET models_error=$3,usage_error=$3 WHERE credential_id=$1 AND key_digest=$2`,
					job.credential, digest[:], result.err.Error())
				return err
			}
		}
		return false, s.finishJob(ctx, job, lease, credential, result, manual)
	}
	deadline, cancel := context.WithTimeout(ctx, jobTimeout)
	result := s.fetchJob(deadline, job, credential, key, manual)
	cancel()
	return false, s.finishJob(ctx, job, lease, credential, result, manual)
}

func (s *Service) fetchJob(ctx context.Context, job syncJob, credential credentialRecord, key string, manual bool) fetchResult {
	switch job.kind {
	case "catalog":
		result := s.fetchKeyCatalogJob(ctx, job, key)
		if !job.websiteCatalog {
			return result
		}
		directory, websiteErr := s.readCredentialWebsiteCatalog(ctx, job.credential, key)
		return fetchResult{err: errors.Join(result.err, websiteErr), save: func(ctx context.Context, tx *sql.Tx) error {
			if err := result.save(ctx, tx); err != nil {
				return err
			}
			if websiteErr != nil {
				return nil
			}
			return saveWebsiteCatalog(ctx, tx, job.credential, credential.updatedAt, directory.catalogQuote())
		}}
	case "billing":
		return s.fetchBillingJob(ctx, job, credential.updatedAt, key)
	case "key_usage":
		return s.fetchWebsiteKeyUsage(ctx, job, credential, key)
	default:
		return fetchResult{err: errors.New("北辰同步任务类型无效")}
	}
}

func (s *Service) fetchBillingJob(ctx context.Context, job syncJob, revision time.Time, key string) fetchResult {
	startedAt := s.now().UTC()
	keyResults := make(chan fetchResult, 1)
	go func() { keyResults <- s.fetchKeyCatalogJob(ctx, job, key) }()
	directory, websiteErr := s.readBillingCatalog(ctx, job, key)
	var info billingInfo
	var quote panelQuote
	billingErr := websiteErr
	if websiteErr == nil {
		info, quote, billingErr = directory.billing()
	}
	partialErr := quote.err
	if directory.keyBilling != nil && billingErr == nil {
		// A successful Key read establishes the billing rule. The official
		// website quote remains unavailable and is reported separately.
		partialErr = nil
	}
	var evidence *billingAccountRateObservation
	if group := directory.group; billingErr == nil && group.AccountRate != nil {
		evidence = &billingAccountRateObservation{
			GroupID: group.ID, KeyID: group.AccountRateKeyID, Rate: *group.AccountRate,
			Source: group.AccountRateSource, KeyChecked: directory.keyGroupAt, Revision: revision,
		}
	}
	keyResult := <-keyResults
	return fetchResult{err: billingErr, partialErr: errors.Join(partialErr, keyResult.err), accountRate: evidence, save: func(ctx context.Context, tx *sql.Tx) error {
		if evidence != nil && evidence.Source == keyBillingRateSource {
			changed, err := personalRateBindingChanged(ctx, tx, job.credential, evidence)
			if err != nil {
				return err
			}
			if changed {
				if err := saveBillingObservation(ctx, tx, job.credential, revision, info, errBillingKeyBindingChanged, quote.groupID, quote); err != nil {
					return err
				}
				return errBillingKeyBindingChanged
			}
		}
		if err := saveBillingGroup(ctx, tx, job.credential, revision, startedAt, directory, billingErr); err != nil {
			return err
		}
		if err := keyResult.save(ctx, tx); err != nil {
			return err
		}
		if websiteErr == nil {
			if err := saveWebsiteCatalog(ctx, tx, job.credential, revision, directory.catalogQuote()); err != nil {
				return err
			}
		}
		return saveBillingObservation(ctx, tx, job.credential, revision, info, billingErr, quote.groupID, quote)
	}}
}

func (s *Service) fetchKeyCatalogJob(ctx context.Context, job syncJob, key string) fetchResult {
	entries, err := s.fetchKeyCatalog(ctx, key)
	at := s.now().UTC()
	return fetchResult{err: err, save: func(ctx context.Context, tx *sql.Tx) error {
		if err != nil {
			return recordDatasetError(ctx, tx, job.credential, "catalog", err.Error())
		}
		return saveKeyCatalog(ctx, tx, job, entries, at)
	}}
}

func saveKeyCatalog(ctx context.Context, tx *sql.Tx, job syncJob, entries []catalog.ProviderEntry, at time.Time) error {
	if _, err := catalog.ReplaceCredentialEntries(ctx, tx, job.provider, job.credential, entries, at); err != nil {
		return err
	}
	return recordSuccess(ctx, tx, job.credential, "catalog")
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

func billingPanelGroup(info billingInfo) int64 {
	if info.GroupID != nil && *info.GroupID > 0 && *info.GroupID <= 9007199254740991 {
		return *info.GroupID
	}
	return 0
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
	var observedAt *time.Time
	if !info.ObservedAt.IsZero() {
		observedAt = &info.ObservedAt
	}
	_, err := tx.ExecContext(ctx, `
WITH observation AS (
  SELECT $1::uuid AS credential_id, $2::text::numeric AS multiplier, NULLIF($3::text, '') AS timezone,
    $4::timestamptz AS effective_at, NULLIF($5::bigint, 0) AS group_id,
    NULLIF($6::text, '')::numeric AS group_rate, $7::boolean AS peak_enabled,
    NULLIF($8::text, '')::numeric AS peak_rate, NULLIF($9::text, '') AS peak_start,
    NULLIF($10::text, '') AS peak_end, $11::timestamptz AS observed_at,
    NULLIF($12::text, '') AS account_rate_source
), reused AS (
  UPDATE beichen_multiplier_versions previous
  SET observed_at = GREATEST(previous.observed_at, observation.observed_at)
  FROM observation
  WHERE previous.id = (
    SELECT id FROM beichen_multiplier_versions WHERE credential_id = observation.credential_id
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) AND previous.stale_at IS NULL AND
    ROW(previous.multiplier, previous.timezone, previous.group_id, previous.group_rate, previous.peak_enabled,
        previous.peak_rate, previous.peak_start, previous.peak_end, previous.account_rate_source)
    IS NOT DISTINCT FROM
    ROW(observation.multiplier, observation.timezone, observation.group_id, observation.group_rate, observation.peak_enabled,
        observation.peak_rate, observation.peak_start, observation.peak_end, observation.account_rate_source)
  RETURNING previous.id
)
INSERT INTO beichen_multiplier_versions
  (credential_id, multiplier, timezone, effective_at, group_id, group_rate, peak_enabled, peak_rate, peak_start, peak_end, observed_at,
   account_rate_source)
SELECT observation.* FROM observation
WHERE NOT EXISTS (SELECT 1 FROM reused)`, credentialID, info.Multiplier.String(), info.Timezone, effectiveAt, billingPanelGroup(info),
		info.GroupRate.String(), info.PeakEnabled, info.PeakRate.String(), info.PeakStart, info.PeakEnd, observedAt, info.accountRateSource)
	return err
}

func markMultiplierStale(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, effectiveAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
UPDATE beichen_multiplier_versions SET stale_at = $2
WHERE id = (SELECT id FROM beichen_multiplier_versions WHERE credential_id = $1 AND effective_at <= $2
  ORDER BY effective_at DESC, id DESC LIMIT 1) AND stale_at IS NULL`, credentialID, effectiveAt)
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
	var current time.Time
	err = tx.QueryRowContext(ctx, `
SELECT c.updated_at FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE p.id = $1 AND c.id = $2 AND c.status = 'active' AND p.status = 'active' AND `+beichenProvider+`
FOR SHARE OF p FOR NO KEY UPDATE OF c`, job.provider, job.credential).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	obsolete := err != nil || !credential.updatedAt.Equal(current)
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `
SELECT lease_id = $3 AND lease_until > clock_timestamp() FROM beichen_datasets
WHERE credential_id = $1 AND kind = $2 AND lease_id IS NOT NULL FOR UPDATE`,
		job.credential, job.leaseKind(), lease).Scan(&ownsLease)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !ownsLease {
		return errors.New("北辰同步任务租约已失效")
	}
	if obsolete {
		_, err := tx.ExecContext(ctx, `UPDATE beichen_datasets SET lease_id=NULL, lease_until=NULL
WHERE credential_id=$1 AND kind=$2`, job.credential, job.leaseKind())
		if err == nil {
			err = tx.Commit()
		}
		return errors.Join(err, errors.New("同步期间北辰凭据或供应商已变化"))
	}
	if result.save != nil {
		if err := result.save(ctx, tx); err != nil {
			if !errors.Is(err, errBillingKeyBindingChanged) {
				return err
			}
			result.err, result.accountRate = err, nil
		}
	}
	if job.kind == "catalog" {
		// The Key catalog shares the regular billing job's lease. A targeted
		// scan leaves billing and price results and their schedules intact.
		_, err := tx.ExecContext(ctx, `UPDATE beichen_datasets SET lease_id=NULL, lease_until=NULL
WHERE credential_id=$1 AND kind='billing'`, job.credential)
		if err == nil {
			err = tx.Commit()
		}
		return errors.Join(err, result.err)
	}
	var message *string
	if result.err != nil {
		value := result.err.Error()
		message = &value
	}
	var accountRate *string
	if result.accountRate != nil {
		raw, err := json.Marshal(result.accountRate)
		if err != nil {
			return err
		}
		value := string(raw)
		accountRate = &value
	}
	var failures int
	err = tx.QueryRowContext(ctx, `
WITH completed AS (SELECT clock_timestamp() AS at)
UPDATE beichen_datasets SET
  last_success = CASE WHEN $3 THEN completed.at ELSE last_success END,
  website_group_observation = CASE
    WHEN kind <> 'billing' THEN website_group_observation
    WHEN $7::jsonb IS NULL THEN website_group_observation - 'billing_account_rate'
    ELSE jsonb_set(COALESCE(website_group_observation,'{}'),'{billing_account_rate}',
      $7::jsonb || jsonb_build_object('last_success',completed.at)) END,
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
FROM completed
WHERE credential_id = $1 AND kind = $2
RETURNING failure_count`, job.credential, job.kind,
		(result.save != nil && job.kind == "usage") || result.err == nil, message, refreshMinutes(job.kind), manual && job.kind == "usage",
		accountRate).Scan(&failures)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if job.kind == "billing" && failures == billingAlertFailures {
		slog.Warn("北辰渠道倍率连续读取失败", "provider_id", job.provider, "credential_id", job.credential, "failures", failures)
	}
	return errors.Join(result.err, result.partialErr)
}

func refreshMinutes(kind string) int {
	if kind == "billing" || kind == "key_usage" {
		return 15
	}
	return 1440
}
