package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const websiteDirectoryInterval = 15 * time.Minute

var (
	errWebsiteDirectoryEmpty   = errors.New("北辰官网目录未返回可读取的模型")
	errWebsiteDirectoryPartial = errors.New("北辰官网部分平台目录无法读取")
)

type WebsiteDirectoryStatus struct {
	State       string     `json:"state"`
	PriceUnit   string     `json:"price_unit"`
	LastSuccess *time.Time `json:"last_success"`
	LastError   *string    `json:"last_error"`
	LastRun     *CrawlRun  `json:"last_run"`
}

type WebsiteGroup struct {
	ID                int64        `json:"id,omitempty"`
	Name              string       `json:"name,omitempty"`
	Status            string       `json:"status,omitempty"`
	Rate              *json.Number `json:"rate_multiplier,omitempty"`
	AccountRate       *json.Number `json:"account_rate_multiplier,omitempty"`
	AccountRateSource string       `json:"account_rate_source,omitempty"`
	AccountRateKeyID  int64        `json:"account_rate_key_id,omitempty"`
	PeakRate          *json.Number `json:"peak_rate_multiplier,omitempty"`
	PeakEnabled       *bool        `json:"peak_rate_enabled,omitempty"`
}

type directoryGroup struct {
	WebsiteGroup
	Platform     string `json:"platform,omitempty"`
	Subscription string `json:"subscription_type,omitempty"`
	PeakStart    string `json:"peak_start,omitempty"`
	PeakEnd      string `json:"peak_end,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	OwnModels    bool   `json:"own_models,omitempty"`
}

type directoryModel struct {
	Name         string          `json:"name"`
	Platform     string          `json:"platform,omitempty"`
	GroupID      int64           `json:"group_id,omitempty"`
	Protocols    []string        `json:"supported_protocols,omitempty"`
	Pricing      json.RawMessage `json:"pricing"`
	ObservedAt   time.Time       `json:"observed_at"`
	priceBlocker string
}

type directoryPlatform struct {
	Scope         string           `json:"scope"`
	ChannelID     int64            `json:"channel_id,omitempty"`
	Channel       string           `json:"channel,omitempty"`
	ChannelStatus string           `json:"channel_status,omitempty"`
	Platform      string           `json:"platform"`
	ObservedAt    time.Time        `json:"observed_at"`
	Groups        []directoryGroup `json:"groups"`
	Models        []directoryModel `json:"models"`
	Incomplete    bool             `json:"incomplete,omitempty"`
}

// On partial reads, replace complete scopes and preserve observations that were
// obscured by a bad channel, platform or model record.
func mergeWebsiteDirectory(previous, current []directoryPlatform) []directoryPlatform {
	seen := make(map[string]int, len(current))
	for i, section := range current {
		seen[section.Scope] = i
	}
	for _, old := range previous {
		i, found := seen[old.Scope]
		if !found {
			current = append(current, old)
			continue
		}
		section := &current[i]
		if !section.Incomplete {
			continue
		}
		for _, group := range old.Groups {
			if !slices.ContainsFunc(section.Groups, func(candidate directoryGroup) bool {
				return group.ID == candidate.ID && (group.ID != 0 || group.Name == candidate.Name)
			}) {
				section.Groups = append(section.Groups, group)
			}
		}
		type modelScope struct {
			group int64
			name  string
		}
		oldModels := make(map[modelScope]bool, len(old.Models))
		for _, model := range old.Models {
			oldModels[modelScope{model.GroupID, model.Name}] = true
		}
		section.Models = slices.DeleteFunc(section.Models, func(model directoryModel) bool {
			return model.priceBlocker == crawlPriceInvalid && oldModels[modelScope{model.GroupID, model.Name}]
		})
		models := make(map[modelScope]bool, len(section.Models))
		for _, model := range section.Models {
			models[modelScope{model.GroupID, model.Name}] = true
		}
		for _, model := range old.Models {
			if !models[modelScope{model.GroupID, model.Name}] {
				section.Models = append(section.Models, model)
			}
		}
	}
	return current
}

func (s *Service) syncWebsiteDirectories(ctx context.Context, provider *uuid.UUID) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id FROM providers p
LEFT JOIN beichen_website_directories d ON d.provider_id=p.id
LEFT JOIN beichen_panel_accounts a ON a.provider_id=p.id
WHERE `+beichenProvider+` AND p.status='active' AND ($1::uuid IS NULL OR p.id=$1)
  AND (COALESCE(a.status <> 'none', $2) OR (a.status = 'none' AND $3))
  AND ($1 IS NOT NULL OR COALESCE(d.next_attempt, '-infinity'::timestamptz)<=statement_timestamp())
ORDER BY COALESCE(d.next_attempt, '-infinity'::timestamptz), p.id`, provider,
		s.WebSessionConfigured() || s.panelFallback != "" || s.sharedPanel != nil || s.sharedPanelErr != nil,
		s.deliveredWebSessionConfigured())
	if err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return unavailable("读取北辰官网目录同步任务失败", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	if err := rows.Err(); err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	var failed error
	for _, id := range ids {
		_, err := s.syncWebsiteDirectory(ctx, id, provider != nil)
		failed = errors.Join(failed, err)
	}
	if failed != nil {
		return unavailable("北辰官网目录未完整刷新，请查看模型页的目录状态", failed)
	}
	return nil
}

func (s *Service) syncWebsiteDirectory(ctx context.Context, provider uuid.UUID, manual bool) (PanelCrawlSummary, error) {
	lease := uuid.New()
	var previous []byte
	err := s.db.QueryRowContext(ctx, `
INSERT INTO beichen_website_directories(provider_id,lease_id,lease_until)
VALUES ($1,$2,clock_timestamp()+$3::int*interval '1 second')
ON CONFLICT(provider_id) DO UPDATE SET lease_id=EXCLUDED.lease_id,lease_until=EXCLUDED.lease_until
WHERE (beichen_website_directories.lease_until IS NULL OR beichen_website_directories.lease_until<=clock_timestamp())
  AND ($4 OR beichen_website_directories.next_attempt<=clock_timestamp())
RETURNING platforms`, provider, lease, leaseSeconds, manual).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		if manual {
			return PanelCrawlSummary{}, domain.NewError(domain.CodeInvalidTransition, "北辰官网目录正在抓取，请稍后刷新状态")
		}
		return PanelCrawlSummary{}, nil
	}
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	fetch, cancel := context.WithTimeout(ctx, jobTimeout)
	crawl, observedErr := s.crawlWebsiteDirectory(fetch, provider, "")
	if crawl.membershipComplete {
		crawl.KeyModelComparisons, crawl.KeyModelError = s.crawlKeyModels(fetch, provider, &crawl, "")
		crawl.DurationMS = time.Since(crawl.StartedAt).Milliseconds()
	}
	cancel()
	// Capture fresh records before restoring older data in a partial merge.
	prices, err := json.Marshal(websitePriceObservations(crawl))
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	platforms := crawl.platforms
	if errors.Is(observedErr, errWebsiteDirectoryPartial) {
		var old []directoryPlatform
		if err := json.Unmarshal(previous, &old); err != nil {
			return PanelCrawlSummary{}, errPanelAccountDB
		}
		platforms = mergeWebsiteDirectory(old, platforms)
	} else if observedErr != nil {
		platforms = nil
	}

	var snapshot, message *string
	if len(platforms) != 0 {
		raw, err := json.Marshal(platforms)
		if err != nil {
			return PanelCrawlSummary{}, errPanelAccountDB
		}
		if len(raw) <= 4*maxResponseBytes {
			body := string(raw)
			snapshot = &body
		} else {
			observedErr = errors.New("北辰官网目录超过存储上限")
			crawl.addFailure("snapshot", directoryPlatform{}, observedErr.Error())
		}
	}
	if observedErr != nil {
		safeMessage := observedErr.Error()
		message = &safeMessage
	}
	failures, err := json.Marshal(crawl.Failures)
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	comparisons := []byte("[]")
	if crawl.KeyModelComparisons != nil {
		comparisons, err = json.Marshal(crawl.KeyModelComparisons)
		if err != nil {
			return PanelCrawlSummary{}, errPanelAccountDB
		}
	}
	var saved uuid.UUID
	err = s.db.QueryRowContext(ctx, `
WITH saved AS (
  UPDATE beichen_website_directories SET platforms=COALESCE($3::jsonb,platforms),
    last_success=CASE WHEN $4::text IS NULL THEN clock_timestamp() ELSE last_success END,
    last_error=$4,
    failure_count=CASE WHEN $4 IS NULL THEN 0 ELSE LEAST(failure_count+1,6) END,
    next_attempt=clock_timestamp()+CASE WHEN $4 IS NULL THEN $5::int*interval '1 minute'
      ELSE power(2,LEAST(failure_count,5))*interval '1 minute' END,
    lease_id=NULL,lease_until=NULL
  WHERE provider_id=$1 AND lease_id=$2 AND lease_until>clock_timestamp()
  RETURNING provider_id
), price_history AS (
  INSERT INTO beichen_group_price_observations (provider_id, group_id, model, observed_at, group_rules, pricing)
  SELECT saved.provider_id, quote.group_id, quote.model, quote.observed_at, quote.group_rules, quote.pricing
  FROM saved CROSS JOIN jsonb_to_recordset($14::jsonb)
    AS quote(group_id bigint, model text, observed_at timestamptz, group_rules jsonb, pricing jsonb, account_rates_verified boolean)
  LEFT JOIN LATERAL (
    SELECT observed_at, group_rules, pricing FROM beichen_group_price_observations
    WHERE provider_id=saved.provider_id AND group_id=quote.group_id AND model=quote.model AND source='website'
    ORDER BY observed_at DESC LIMIT 1
  ) previous ON true
  WHERE (quote.account_rates_verified OR previous.group_rules->'account_rate_multiplier' IS NULL)
    AND (previous.observed_at IS NULL OR (previous.observed_at < quote.observed_at
      AND (previous.group_rules, previous.pricing) IS DISTINCT FROM (quote.group_rules, quote.pricing)))
  ON CONFLICT DO NOTHING
)
INSERT INTO beichen_crawl_runs (provider_id, started_at, duration_ms, platform_count, group_count, model_count, expected_model_count,
  failures, raw_website_response, key_model_comparisons, key_model_error, account_rate_error)
SELECT provider_id,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$15::jsonb,NULLIF($16,''),NULLIF($17,'') FROM saved
ON CONFLICT (provider_id) DO UPDATE SET
  started_at=EXCLUDED.started_at, duration_ms=EXCLUDED.duration_ms, platform_count=EXCLUDED.platform_count,
  group_count=EXCLUDED.group_count, model_count=EXCLUDED.model_count,
  expected_model_count=EXCLUDED.expected_model_count, failures=EXCLUDED.failures,
  raw_website_response=COALESCE(EXCLUDED.raw_website_response,beichen_crawl_runs.raw_website_response),
  key_model_comparisons=EXCLUDED.key_model_comparisons, key_model_error=EXCLUDED.key_model_error,
  account_rate_error=EXCLUDED.account_rate_error
RETURNING provider_id`, provider, lease, snapshot, message, int(websiteDirectoryInterval/time.Minute),
		crawl.StartedAt, crawl.DurationMS, crawl.PlatformCount, crawl.GroupCount, crawl.ModelCount,
		crawl.ExpectedModelCount, string(failures), crawl.rawResponse, string(prices), string(comparisons), crawl.KeyModelError, crawl.AccountRateError).Scan(&saved)
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	return crawl.panelSummary(), observedErr
}

func readWebsiteDirectory(ctx context.Context, tx *sql.Tx, provider uuid.UUID) (WebsiteDirectoryStatus, []directoryPlatform, error) {
	status := WebsiteDirectoryStatus{State: "unverified", PriceUnit: websitePriceUnit}
	var raw, lastRun []byte
	var stale bool
	err := tx.QueryRowContext(ctx, `
SELECT d.platforms,d.last_success,d.last_error,d.last_error IS NOT NULL OR
  COALESCE(d.last_success < statement_timestamp()-$2::int*interval '1 minute',false),
  to_jsonb(r)-'provider_id'
FROM beichen_website_directories d LEFT JOIN beichen_crawl_runs r USING (provider_id)
WHERE d.provider_id=$1`, provider, int(2*websiteDirectoryInterval/time.Minute)).
		Scan(&raw, &status.LastSuccess, &status.LastError, &stale, &lastRun)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil, nil
	}
	if err != nil {
		return status, nil, err
	}
	if len(lastRun) != 0 {
		if err := json.Unmarshal(lastRun, &status.LastRun); err != nil {
			return status, nil, err
		}
	}
	if status.LastSuccess != nil {
		at := status.LastSuccess.UTC()
		status.LastSuccess = &at
		status.State = "fresh"
	}
	var platforms []directoryPlatform
	if err := json.Unmarshal(raw, &platforms); err != nil {
		return status, nil, err
	}
	if len(platforms) != 0 && stale {
		status.State = "stale"
	}
	return status, platforms, nil
}
