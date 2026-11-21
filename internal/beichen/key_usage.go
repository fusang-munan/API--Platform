package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type KeyUsageTarget struct {
	// nil preserves the saved target; an empty string clears it.
	Multiplier   *string
	ExpectClaude *bool
}

// RefreshKeyUsage verifies Key identity and reads its group and dated usage from the website.
func (s *Service) RefreshKeyUsage(ctx context.Context, provider, credential uuid.UUID, target KeyUsageTarget) error {
	if provider == uuid.Nil || credential == uuid.Nil {
		return invalid("需要有效的供应商和渠道 ID")
	}
	if target.Multiplier != nil && *target.Multiplier != "" {
		rate := exactDecimal(json.Number(*target.Multiplier))
		if rate == nil || rate.Cmp(big.NewRat(1000, 1)) > 0 {
			return invalid("目标分组倍率须为 0 至 1000 的十进制数")
		}
	}
	if s.db == nil || s.crypto == nil {
		return unavailable("北辰同步未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, jobTimeout+5*time.Second)
	defer cancel()
	if err := checkActiveSyncCredential(ctx, s.db, provider, credential); err != nil {
		return err
	}
	busy, err := s.runJob(ctx, syncJob{provider: provider, credential: credential, kind: "key_usage", target: target}, true)
	if err != nil {
		return unavailable("Key 能力或用量读取未完成，请查看该 Key 的数据状态", err)
	}
	if busy {
		return domain.NewError(domain.CodeInvalidTransition, "所选渠道正在读取网页数据，请稍后重试")
	}
	return nil
}

type websiteKeyCheck struct {
	key        listedKey
	catalog    websiteKeyCatalog
	catalogErr error
	days       []websiteUsageDay
	usageErr   error
}

func (s *Service) fetchWebsiteKeyUsage(ctx context.Context, job syncJob, credential credentialRecord, key string) fetchResult {
	var check websiteKeyCheck
	access, err := s.loadPriceAccess(ctx, job.credential)
	now := s.now().UTC()
	today := now.In(beijing).Format(dayLayout)
	if err == nil {
		check, err = readWebsiteKey(ctx, s, access, key, func(bearer string, identity listedKey) (websiteKeyCheck, error) {
			check := websiteKeyCheck{key: identity}
			check.catalog, check.catalogErr = s.fetchBillingCatalog(ctx, job.provider, bearer, key, identity)
			if panelAuthFailed(check.catalogErr) || errors.Is(check.catalogErr, errKeyGroupMissing) {
				return check, check.catalogErr
			}
			// Also refresh the most recent completed day for reconciliation.
			for _, day := range []string{today, now.In(beijing).AddDate(0, 0, -1).Format(dayLayout)} {
				result, err := s.fetchWebsiteUsageDay(ctx, bearer, identity, day)
				if err != nil {
					check.usageErr = errors.Join(check.usageErr, fmt.Errorf("%s：%w", day, err))
					if panelAuthFailed(err) {
						break
					}
					continue
				}
				check.days = append(check.days, result)
			}
			return check, errors.Join(check.catalogErr, check.usageErr)
		})
	}
	if err != nil && check.catalogErr == nil && check.usageErr == nil {
		check.catalogErr, check.usageErr = err, err
	}
	if check.key.ID == 0 {
		check.catalogErr, check.usageErr = err, err
	}
	if (panelAuthFailed(check.catalogErr) || errors.Is(check.catalogErr, errKeyGroupMissing)) &&
		len(check.days) == 0 && check.usageErr == nil {
		check.usageErr = check.catalogErr
	}
	return fetchResult{err: err, save: func(ctx context.Context, tx *sql.Tx) error {
		return s.saveWebsiteKeyCheck(ctx, tx, job, credential, check, now, today)
	}}
}

func (s *Service) saveWebsiteKeyCheck(ctx context.Context, tx *sql.Tx, job syncJob, credential credentialRecord,
	check websiteKeyCheck, at time.Time, today string) error {
	digest := sha256.Sum256(credential.ciphertext)
	id := job.credential
	if err := saveBillingGroup(ctx, tx, id, credential.updatedAt, at, check.catalog, check.catalogErr); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, initKeyObservationSQL, id, digest[:], job.target.Multiplier, job.target.ExpectClaude); err != nil {
		return err
	}
	if check.key.ID > 0 {
		// A different upstream ID must not inherit this credential's last
		// model list or current-day aggregates.
		_, err := tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET website_key_id=$3, website_group=NULL, models=NULL, models_fetched_at=NULL,
 balance=NULL, unit=NULL, usage_fetched_at=NULL, model_stats=NULL, model_day=NULL,
 baseline_models=NULL, baseline_multiplier=NULL, models_unchanged=false
WHERE credential_id=$1 AND key_digest=$2 AND website_key_id IS DISTINCT FROM $3`, id, digest[:], check.key.ID)
		if err != nil {
			return err
		}
	}
	if check.catalog.keyGroup != nil {
		var group []byte
		if check.catalog.keyGroup.ID != 0 {
			metadata := check.catalog.group.WebsiteGroup
			if metadata.ID == 0 {
				metadata = *check.catalog.keyGroup
			}
			metadata.Name, metadata.Rate = check.catalog.keyGroup.Name, check.catalog.keyGroup.Rate
			var err error
			group, err = json.Marshal(metadata)
			if err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET website_group=$3::jsonb,
 models=CASE WHEN website_group->>'id'=$3::jsonb->>'id' THEN models END,
 models_fetched_at=CASE WHEN website_group->>'id'=$3::jsonb->>'id' THEN models_fetched_at END
WHERE credential_id=$1 AND key_digest=$2`, id, digest[:], group)
		if err != nil {
			return err
		}
	}
	if check.catalogErr == nil && check.catalog.group.ID > 0 {
		models := make([]string, 0, len(check.catalog.entries))
		for _, model := range check.catalog.entries {
			models = append(models, model.ID)
		}
		list, err := json.Marshal(models)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET models=$3::jsonb,models_fetched_at=$4,models_error=NULL
WHERE credential_id=$1 AND key_digest=$2`, id, digest[:], string(list), check.catalog.at)
		if err != nil {
			return err
		}
	}
	for _, day := range check.days {
		if err := s.saveWebsiteUsageDay(ctx, tx, id, digest[:], check.key.ID, day, at); err != nil {
			return err
		}
		if day.daily.Date == today {
			_, err := tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET balance=NULL,unit=$3,usage_fetched_at=$4,usage_error=NULL
WHERE credential_id=$1 AND key_digest=$2`, id, digest[:], day.unit, at)
			if err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `
UPDATE beichen_key_observations SET models_error=$3,usage_error=$4
WHERE credential_id=$1 AND key_digest=$2`, id, digest[:], websiteErrorText(check.catalogErr), websiteErrorText(check.usageErr))
	return err
}

func websiteErrorText(err error) *string {
	if err == nil {
		return nil
	}
	message := err.Error()
	return &message
}

func (s *Service) saveWebsiteUsageDay(ctx context.Context, tx *sql.Tx, id uuid.UUID, digest []byte, key int64,
	day websiteUsageDay, at time.Time) error {
	daily, err := json.Marshal(day.daily)
	if err != nil {
		return err
	}
	models, err := json.Marshal(day.models)
	if err != nil {
		return err
	}
	// The page envelope may include account metadata. Encrypt the original
	// bytes before storing them and only expose the normalized metrics.
	raw, err := json.Marshal(day.pages)
	if err != nil {
		return err
	}
	pages, err := s.crypto.EncryptCredential(string(raw))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_key_usage_days AS prior (credential_id,key_digest,day,unit,daily_usage,model_stats,
 model_fetched_at,fetched_at,website_key_id,raw_website_pages)
VALUES ($1,$2,$3::text::date,$4,$5::jsonb,$6::jsonb,$7,$7,$8,$9)
ON CONFLICT (credential_id,key_digest,day) DO UPDATE SET unit=excluded.unit,daily_usage=excluded.daily_usage,
 model_stats=excluded.model_stats,model_fetched_at=excluded.model_fetched_at,fetched_at=excluded.fetched_at,
 website_key_id=excluded.website_key_id,raw_website_pages=excluded.raw_website_pages
WHERE prior.fetched_at <= excluded.fetched_at OR prior.website_key_id IS DISTINCT FROM excluded.website_key_id`,
		id, digest, day.daily.Date, day.unit, string(daily), string(models), at, key, pages)
	return err
}

const initKeyObservationSQL = `
INSERT INTO beichen_key_observations AS old (credential_id, key_digest, target_multiplier, expect_claude)
VALUES ($1,$2,NULLIF($3::text,'')::numeric,COALESCE($4,false))
ON CONFLICT (credential_id,key_digest) DO UPDATE SET
  target_multiplier=CASE WHEN $3::text IS NULL THEN old.target_multiplier ELSE excluded.target_multiplier END,
  expect_claude=COALESCE($4,old.expect_claude)
WHERE ($3::text IS NOT NULL AND old.target_multiplier IS DISTINCT FROM excluded.target_multiplier)
   OR ($4::boolean IS NOT NULL AND old.expect_claude IS DISTINCT FROM excluded.expect_claude)`
