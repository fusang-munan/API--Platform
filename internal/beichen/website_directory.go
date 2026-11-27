package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const websiteDirectoryInterval = 15 * time.Minute

var (
	errWebsiteDirectoryEmpty          = errors.New("北辰官网目录未返回可读取的模型")
	errWebsiteDirectoryPartial        = errors.New("北辰官网部分平台目录无法读取")
	errWebsiteDirectorySessionChanged = errors.New("读取官网目录期间北辰网页会话已更换，请重新同步")
)

type WebsiteDirectoryStatus struct {
	State       string                    `json:"state"`
	PriceUnit   string                    `json:"price_unit"`
	LastSuccess *time.Time                `json:"last_success"`
	LastError   *string                   `json:"last_error"`
	LastRun     *CrawlRun                 `json:"last_run"`
	Groups      []WebsiteGroupObservation `json:"groups,omitempty"`
}

type WebsiteGroupObservation struct {
	ChannelID  int64     `json:"channel_id,omitempty"`
	Platform   string    `json:"platform"`
	GroupID    int64     `json:"group_id,omitempty"`
	GroupName  string    `json:"group_name,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Retained   bool      `json:"retained"`
	Incomplete bool      `json:"incomplete,omitempty"`
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
	Platform     string     `json:"platform,omitempty"`
	Subscription string     `json:"subscription_type,omitempty"`
	PeakStart    string     `json:"peak_start,omitempty"`
	PeakEnd      string     `json:"peak_end,omitempty"`
	Timezone     string     `json:"timezone,omitempty"`
	OwnModels    bool       `json:"own_models,omitempty"`
	ObservedAt   *time.Time `json:"observed_at,omitempty"`
	Retained     bool       `json:"retained,omitempty"`
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
	Scope                string           `json:"scope"`
	ChannelID            int64            `json:"channel_id,omitempty"`
	Channel              string           `json:"channel,omitempty"`
	ChannelStatus        string           `json:"channel_status,omitempty"`
	Platform             string           `json:"platform"`
	ObservedAt           time.Time        `json:"observed_at"`
	Groups               []directoryGroup `json:"groups"`
	Models               []directoryModel `json:"models"`
	Incomplete           bool             `json:"incomplete,omitempty"`
	MembershipIncomplete bool             `json:"membership_incomplete,omitempty"`
	Retained             bool             `json:"retained,omitempty"`
}

func directoryGroupObservations(platforms []directoryPlatform, allRetained bool) []WebsiteGroupObservation {
	var items []WebsiteGroupObservation
	for _, platform := range platforms {
		for _, group := range platform.Groups {
			at := platform.ObservedAt
			if group.ObservedAt != nil {
				at = *group.ObservedAt
			}
			items = append(items, WebsiteGroupObservation{
				ChannelID: platform.ChannelID, Platform: platform.Platform, GroupID: group.ID, GroupName: group.Name,
				ObservedAt: at.UTC(), Retained: allRetained || platform.Retained || group.Retained, Incomplete: platform.Incomplete,
			})
		}
	}
	return items
}

type directoryCoverage struct {
	retain map[string]bool
	remove map[string]bool
}

// The read-only audit reports the scopes that would retain their previous data.
func (crawl *directoryCrawl) checkCoverage(previous []directoryPlatform, lastFailures []CrawlFailure, clean, advance bool) map[string]bool {
	return crawl.reviewCoverage(previous, lastFailures, clean, advance).retain
}

// Only a complete list of models and groups can advance coverage confirmation.
func (crawl *directoryCrawl) reviewCoverage(previous []directoryPlatform, lastFailures []CrawlFailure, complete, advance bool) directoryCoverage {
	current := make(map[string]directoryPlatform, len(crawl.platforms))
	for _, section := range crawl.platforms {
		current[section.Scope] = section
	}
	prior := make(map[string]CrawlFailure, len(lastFailures))
	for _, failure := range lastFailures {
		if failure.SnapshotHash != "" {
			prior[failure.Scope] = failure
		}
	}
	coverage := directoryCoverage{retain: map[string]bool{}, remove: map[string]bool{}}
	for _, old := range previous {
		section, present := current[old.Scope]
		if !present && !complete && crawl.markUnreadableScope(old) {
			coverage.retain[old.Scope] = true
			continue
		}
		if present && section.MembershipIncomplete {
			continue
		}
		reason, fingerprint := directoryCoverageDrop(old, section, present)
		if reason == "" {
			continue
		}
		rounds := 0
		if complete && advance {
			rounds = 1
			if last := prior[old.Scope]; last.SnapshotHash == fingerprint && last.ConfirmationRounds > 0 &&
				last.ConfirmationRounds < catalog.BeichenShrinkRounds {
				rounds = last.ConfirmationRounds + 1
			}
			if rounds >= catalog.BeichenShrinkRounds {
				if !present {
					coverage.remove[old.Scope] = true
				}
				continue
			}
			reason += fmt.Sprintf("，连续一致 %d/%d", rounds, catalog.BeichenShrinkRounds)
		}
		coverage.retain[old.Scope] = true
		crawl.Failures = append(crawl.Failures, CrawlFailure{
			ChannelID: old.ChannelID, Channel: old.Channel, Platform: old.Platform, Scope: old.Scope,
			Path: "coverage", Reason: reason + "，沿用上次平台目录", Retained: true,
			SnapshotHash: fingerprint, ConfirmationRounds: rounds,
		})
	}
	if len(coverage.retain) != 0 {
		crawl.membershipComplete = false
		previousNames := map[string]bool{}
		for _, section := range previous {
			if coverage.remove[section.Scope] {
				continue
			}
			for _, model := range section.Models {
				previousNames[model.Name] = true
			}
		}
		crawl.ExpectedModelCount = max(crawl.ExpectedModelCount, len(previousNames))
	}
	return coverage
}

func (crawl *directoryCrawl) markUnreadableScope(old directoryPlatform) bool {
	for i, failure := range crawl.Failures {
		if failure.Path == "coverage" {
			continue
		}
		sameChannel := old.ChannelID != 0 && old.ChannelID == failure.ChannelID ||
			old.Channel != "" && old.Channel == failure.Channel
		if sameChannel && (failure.Platform == "" || strings.EqualFold(failure.Platform, old.Platform)) {
			crawl.Failures[i].Retained = true
			return true
		}
	}
	return false
}

type directoryMembership struct {
	Models []string            `json:"models"`
	Groups map[string][]string `json:"groups"`
}

func platformMembership(section directoryPlatform) directoryMembership {
	membership := directoryMembership{Groups: map[string][]string{}}
	identifiers := map[string]bool{}
	modelsByGroup := map[string]map[string]bool{}
	groupKey := func(group directoryGroup) string {
		if group.ID != 0 {
			return fmt.Sprint(group.ID)
		}
		return "name/" + strings.ToLower(strings.TrimSpace(group.Name))
	}
	for _, group := range section.Groups {
		modelsByGroup[groupKey(group)] = map[string]bool{}
	}
	for _, model := range section.Models {
		name := strings.ToLower(model.Name)
		identifiers[name] = true
		for _, group := range directoryModelGroups(section, model) {
			modelsByGroup[groupKey(group)][name] = true
		}
	}
	for id, names := range modelsByGroup {
		membership.Groups[id] = slices.Sorted(maps.Keys(names))
	}
	membership.Models = slices.Sorted(maps.Keys(identifiers))
	return membership
}

func directoryCoverageDrop(old, current directoryPlatform, present bool) (string, string) {
	if !present {
		return "北辰官网平台本轮缺席，待核验", "absent"
	}
	before, after := platformMembership(old), platformMembership(current)
	var reasons []string
	if catalog.BeichenSnapshotShrank(len(before.Models), len(after.Models)) {
		reasons = append(reasons, fmt.Sprintf("平台型号由 %d 个降至 %d 个", len(before.Models), len(after.Models)))
	}
	for _, id := range slices.Sorted(maps.Keys(before.Groups)) {
		prior := before.Groups[id]
		names, found := after.Groups[id]
		if !found {
			reasons = append(reasons, fmt.Sprintf("分组 %s 缺席", id))
		} else if catalog.BeichenSnapshotShrank(len(prior), len(names)) {
			reasons = append(reasons, fmt.Sprintf("分组 %s 型号由 %d 个降至 %d 个", id, len(prior), len(names)))
		}
	}
	if len(reasons) == 0 {
		return "", ""
	}
	raw, _ := json.Marshal(after)
	return "北辰官网目录待核验：" + strings.Join(reasons, "，"), fmt.Sprintf("%x", sha256.Sum256(raw))
}

func mergeWebsiteDirectory(previous, current []directoryPlatform) []directoryPlatform {
	return mergePartialWebsiteDirectory(previous, current, nil)
}

// Preserve unreadable and unconfirmed scopes while applying confirmed removals.
func mergePartialWebsiteDirectory(previous, current []directoryPlatform, removed map[string]bool) []directoryPlatform {
	seen := make(map[string]int, len(current))
	for i, section := range current {
		seen[section.Scope] = i
	}
	for _, old := range previous {
		if removed[old.Scope] {
			continue
		}
		i, found := seen[old.Scope]
		if !found {
			old.Retained = true
			current = append(current, old)
			continue
		}
		section := &current[i]
		if !section.Incomplete {
			continue
		}
		section.Groups = slices.Clone(section.Groups)
		section.Models = slices.Clone(section.Models)
		if section.MembershipIncomplete {
			for _, group := range old.Groups {
				if !slices.ContainsFunc(section.Groups, func(candidate directoryGroup) bool {
					return group.ID == candidate.ID && (group.ID != 0 || group.Name == candidate.Name)
				}) {
					group.Retained = true
					if group.ObservedAt == nil {
						at := old.ObservedAt
						group.ObservedAt = &at
					}
					section.Groups = append(section.Groups, group)
				}
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
		invalidPrices := map[modelScope]bool{}
		section.Models = slices.DeleteFunc(section.Models, func(model directoryModel) bool {
			id := modelScope{model.GroupID, model.Name}
			if model.priceBlocker == crawlPriceInvalid && oldModels[id] {
				invalidPrices[id] = true
				return true
			}
			return false
		})
		models := make(map[modelScope]bool, len(section.Models))
		for _, model := range section.Models {
			models[modelScope{model.GroupID, model.Name}] = true
		}
		for _, model := range old.Models {
			id := modelScope{model.GroupID, model.Name}
			if !models[id] && (section.MembershipIncomplete || invalidPrices[id]) {
				section.Models = append(section.Models, model)
				for _, affected := range directoryModelGroups(*section, model) {
					for j := range section.Groups {
						group := &section.Groups[j]
						if group.ID == affected.ID && (group.ID != 0 || group.Name == affected.Name) {
							group.Retained = true
							if group.ObservedAt == nil || model.ObservedAt.Before(*group.ObservedAt) {
								at := model.ObservedAt
								group.ObservedAt = &at
							}
						}
					}
				}
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
	var previous, previousFailures []byte
	err := s.db.QueryRowContext(ctx, `
INSERT INTO beichen_website_directories(provider_id,lease_id,lease_until)
VALUES ($1,$2,clock_timestamp()+$3::int*interval '1 second')
ON CONFLICT(provider_id) DO UPDATE SET lease_id=EXCLUDED.lease_id,lease_until=EXCLUDED.lease_until
WHERE (beichen_website_directories.lease_until IS NULL OR beichen_website_directories.lease_until<=clock_timestamp())
  AND ($4 OR beichen_website_directories.next_attempt<=clock_timestamp())
RETURNING platforms, (SELECT failures FROM beichen_crawl_runs WHERE provider_id=$1)`,
		provider, lease, leaseSeconds, manual).Scan(&previous, &previousFailures)
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
	start := time.Now()
	var tokenVersion, capturedVersion []byte
	readCtx := context.WithValue(fetch, capturedWebVersionKey{}, &capturedVersion)
	crawl, observedErr := readWithWebsiteKeySession(readCtx, s, provider, "", func(session panelAuthorization) (directoryCrawl, error) {
		tokenVersion = session.tokenVersion
		return s.fetchWebsiteCrawl(fetch, session.bearer)
	})
	if tokenVersion == nil {
		tokenVersion = capturedVersion
	}
	if observedErr != nil && !errors.Is(observedErr, errWebsiteDirectoryPartial) && !errors.Is(observedErr, errWebsiteDirectoryEmpty) {
		crawl, _ = failedDirectoryCrawl(observedErr)
	}
	crawl.StartedAt, crawl.DurationMS = start.UTC(), time.Since(start).Milliseconds()
	var old []directoryPlatform
	var lastFailures []CrawlFailure
	if json.Unmarshal(previous, &old) != nil || (len(previousFailures) != 0 && json.Unmarshal(previousFailures, &lastFailures) != nil) {
		cancel()
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	var coverage directoryCoverage
	if observedErr == nil || errors.Is(observedErr, errWebsiteDirectoryPartial) {
		coverage = crawl.reviewCoverage(old, lastFailures, crawl.membershipComplete, true)
		if len(coverage.retain) != 0 {
			crawl.platforms = slices.DeleteFunc(crawl.platforms, func(section directoryPlatform) bool { return coverage.retain[section.Scope] })
			observedErr = errWebsiteDirectoryPartial
		}
		for _, section := range crawl.platforms {
			if section.Incomplete && slices.ContainsFunc(old, func(prior directoryPlatform) bool { return prior.Scope == section.Scope }) {
				for j := range crawl.Failures {
					if crawl.Failures[j].ChannelID == section.ChannelID && crawl.Failures[j].Platform == section.Platform {
						crawl.Failures[j].Retained = true
					}
				}
			}
		}
	} else if len(old) != 0 {
		for i := range crawl.Failures {
			crawl.Failures[i].Retained = true
		}
	}
	if crawl.membershipComplete && crawl.ModelCount != 0 {
		crawl.KeyModelComparisons, crawl.KeyModelError = s.crawlKeyModels(fetch, provider, &crawl, "")
		crawl.DurationMS = time.Since(crawl.StartedAt).Milliseconds()
	}
	cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	defer tx.Rollback()
	if tokenVersion != nil && crawl.rawResponse != nil {
		current, unlock, err := s.lockWebsiteSessionVersion(ctx, tx, provider, tokenVersion)
		if err != nil {
			return PanelCrawlSummary{}, errPanelAccountDB
		}
		if unlock != nil {
			defer unlock()
		}
		if !current {
			observedErr = errWebsiteDirectorySessionChanged
			crawl = directoryCrawl{CrawlRun: CrawlRun{
				StartedAt: crawl.StartedAt, DurationMS: crawl.DurationMS,
				Failures: []CrawlFailure{{Path: "session", Reason: observedErr.Error(), Retained: len(old) != 0}},
			}}
		}
	}
	// Capture fresh records before restoring older data in a partial merge.
	blockedGroups := map[int64]bool{}
	for _, section := range old {
		if coverage.retain[section.Scope] {
			for _, group := range section.Groups {
				blockedGroups[group.ID] = true
			}
		}
	}
	observations := slices.DeleteFunc(websitePriceObservations(crawl), func(quote groupPriceObservation) bool {
		return blockedGroups[quote.GroupID]
	})
	prices, err := json.Marshal(observations)
	if err != nil {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	platforms := crawl.platforms
	if errors.Is(observedErr, errWebsiteDirectoryPartial) {
		platforms = mergePartialWebsiteDirectory(old, platforms, coverage.remove)
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
	err = tx.QueryRowContext(ctx, `
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
	if err := tx.Commit(); err != nil {
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
	allRetained := status.LastError != nil && (status.LastRun == nil || status.LastRun.ModelCount == 0)
	status.Groups = directoryGroupObservations(platforms, allRetained)
	return status, platforms, nil
}
