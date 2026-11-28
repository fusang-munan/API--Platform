package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

const derivedPriceSource = "derived_from_usage"
const derivedPriceLabel = "反推价，待核对"

// A website group can be absent from channels/available while its verified
// Key still advertises its models and a current token multiplier.
func (s *Service) readBillingCatalog(ctx context.Context, job syncJob, key string) (websiteKeyCatalog, error) {
	access, err := s.loadPriceAccess(ctx, job.credential)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	return readWebsiteKey(ctx, s, access, key, func(token string, identity listedKey) (websiteKeyCatalog, error) {
		return s.fetchBillingCatalog(ctx, job, token, key, identity)
	})
}

func (s *Service) fetchBillingCatalog(ctx context.Context, job syncJob, token, key string, identity listedKey) (websiteKeyCatalog, error) {
	directory, websiteErr := s.fetchWebsiteKeyCatalog(ctx, token, identity)
	if errors.Is(websiteErr, errPanelGroupNotFound) {
		fallback, err := s.fetchKeyBillingCatalog(ctx, key, identity)
		if err != nil && errors.Is(websiteErr, errWebsiteDirectoryEmpty) && !errors.Is(err, errKeyGroupNotAllowed) {
			return directory, errWebsiteDirectoryEmpty
		}
		fallback.keyGroup, fallback.keyGroupAt, fallback.keyID = directory.keyGroup, directory.keyGroupAt, directory.keyID
		if err != nil {
			return fallback, err
		}
		if fallback.keyGroup.Rate == nil {
			fallback.keyGroup.Rate = fallback.group.Rate
		}
		fallback.prices = matchWebsitePrices(fallback.entries, directory.prices)
		directory = fallback
	} else if websiteErr != nil {
		return directory, websiteErr
	}
	rates, rateErr := s.fetchWebsiteAccountRates(ctx, token)
	if rate, ok := rates[directory.group.ID]; ok {
		directory.group.AccountRate = &rate
	}
	if directory.keyBilling != nil {
		if directory.group.AccountRate == nil {
			slog.Warn("北辰官网账户倍率未核实，使用 Key 账单确认的生效倍率", "provider_id", job.provider, "group_id", directory.group.ID)
		}
		return directory, nil
	}
	if job.kind == "billing" && directory.group.AccountRate == nil {
		info, at, err := s.fetchVerifiedKeyAccountRate(ctx, job.credential, key, identity, directory.group)
		switch {
		case err == nil:
			directory.group.setKeyAccountRate(info)
			directory.keyBillingSampleAt, directory.at = info.ObservedAt, at
		case errors.Is(err, errKeyGroupNotAllowed):
			directory.accountRateErr = err
			return directory, nil
		case !setGroupAccountRate(&directory.group, identity):
			directory.accountRateErr = err
			if rateErr != nil {
				directory.accountRateErr = errors.Join(accountRateReadFailed(websiteAccountRatesReadFailure, rateErr), err)
			}
			return directory, nil
		}
	}
	// Existing evidence can require reconciling legacy model spellings even
	// after the group is published. Quote terms still come from the website.
	var hasEvidence bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM beichen_group_price_observations
WHERE provider_id=$1 AND group_id=$2 AND source='derived_from_usage' AND observed_at<=statement_timestamp()
)`, job.provider, identity.GroupID).Scan(&hasEvidence); err != nil {
		return directory, errPanelAccountDB
	}
	if !hasEvidence {
		return directory, nil
	}
	actual, err := s.fetchKeyCatalog(ctx, key)
	if err != nil {
		return directory, keyMetadataError(err, identity.GroupID, "北辰 Key 的实际型号清单无法读取")
	}
	return directory.withActualModels(actual), nil
}

func (directory websiteKeyCatalog) withActualModels(actual []catalog.ProviderEntry) websiteKeyCatalog {
	directory.websiteModels = crawlModelIDs(directory.entries)
	directory.entries = actual
	directory.prices = matchWebsitePrices(actual, directory.prices)
	return directory
}

func matchWebsitePrices(actual []catalog.ProviderEntry, official map[string]json.RawMessage) map[string]json.RawMessage {
	pairs, _, _ := compareCrawlModelNames(crawlModelIDs(actual), slices.Sorted(maps.Keys(official)))
	prices := make(map[string]json.RawMessage, len(pairs))
	for _, pair := range pairs {
		prices[pair.KeyModel] = official[pair.ChannelModel]
	}
	return prices
}

func (s *Service) fetchKeyBillingInfo(ctx context.Context, key string, groupID int64) (billingInfo, error) {
	body, err := s.fetch(ctx, key, billingPath, nil)
	if err != nil {
		if errors.Is(err, errKeyGroupNotAllowed) {
			return billingInfo{}, deniedKeyGroup(groupID)
		}
		return billingInfo{}, keyBillingReadError{err}
	}
	return parseBilling(body)
}

func keyBillingIdentityIssue(info billingInfo, identity listedKey) string {
	if group := billingPanelGroup(info); group != 0 && group != identity.GroupID {
		return "Key 账单分组号与官网不一致"
	}
	if (info.KeyID != 0 && info.KeyID != identity.ID) || (info.APIKeyID != 0 && info.APIKeyID != identity.ID) {
		return "Key 账单中的 Key 编号与官网不一致"
	}
	if group := identity.Group; group != nil {
		if group.SubscriptionType != "" && group.SubscriptionType != "standard" {
			return "官网 Key 清单中的分组不是标准余额计费"
		}
		if group.Rate != "" && info.GroupRate != "" && !samePanelRate(group.Rate, info.GroupRate) {
			return keyBillingDefaultRateMismatch
		}
		if group.PeakEnabled != nil && info.PeakEnabled != nil && *group.PeakEnabled != *info.PeakEnabled {
			return keyBillingPeakMismatch
		}
	}
	return ""
}

type keyBillingReadError struct{ cause error }

func (keyBillingReadError) Error() string { return "北辰 Key 的当前计费倍率无法读取" }

func (s *Service) fetchKeyBillingCatalog(ctx context.Context, key string, identity listedKey) (websiteKeyCatalog, error) {
	info, err := s.fetchKeyBillingInfo(ctx, key, identity.GroupID)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	if keyBillingIdentityIssue(info, identity) != "" {
		return websiteKeyCatalog{}, errPanelRulesConflict
	}
	if group := identity.Group; group != nil && info.GroupRate == "" && websiteGroupRate(group.Rate) != nil {
		info.GroupRate = group.Rate
	}
	// A separately published Key price table needs its own official-price
	// verification. Do not select an estimate over those declared terms.
	if !websiteMissing(info.Pricing) {
		return websiteKeyCatalog{}, errors.New("北辰 Key 已提供单独的价表，需核实正式报价")
	}
	entries, err := s.fetchKeyCatalog(ctx, key)
	if err != nil {
		return websiteKeyCatalog{}, keyMetadataError(err, identity.GroupID, "北辰 Key 的实际型号清单无法读取")
	}
	at, err := s.currentKeyBillingAt(ctx, info)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	info.GroupID = &identity.GroupID
	group := directoryGroup{WebsiteGroup: WebsiteGroup{ID: identity.GroupID, PeakEnabled: info.PeakEnabled}}
	if identity.Group != nil {
		group.Name = identity.Group.Name
	}
	if info.GroupRate != "" {
		group.Rate = &info.GroupRate
	}
	if info.PeakRate != "" {
		group.PeakRate = &info.PeakRate
	}
	return websiteKeyCatalog{group: group, entries: entries, at: at, keyBilling: &info}, nil
}

var errKeyBillingSampleExpired = errors.New("北辰 Key 倍率的采样时段已结束")

func (s *Service) currentKeyBillingAt(ctx context.Context, info billingInfo) (time.Time, error) {
	at := s.now().UTC()
	// The two hosts may differ by a fraction of a second. Wait for a near
	// future sample to become current rather than changing its timestamp.
	if delay := info.ObservedAt.Sub(at); delay > 0 && delay <= time.Second {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-timer.C:
			at = s.now().UTC()
		}
	}
	if !info.ObservedAt.IsZero() && info.ObservedAt.Before(at.Add(-jobTimeout)) {
		return time.Time{}, errKeyBillingSampleExpired
	}
	expires, err := panelExpiry(info, at)
	if err != nil {
		return time.Time{}, err
	}
	if !expires.After(at) {
		return time.Time{}, errKeyBillingSampleExpired
	}
	return at, nil
}

type deniedKeyGroup int64

func (group deniedKeyGroup) Error() string { return fmt.Sprintf("账号无 group %d 权限", group) }
func (group deniedKeyGroup) Unwrap() error { return errKeyGroupNotAllowed }

func keyMetadataError(err error, groupID int64, message string) error {
	if errors.Is(err, errKeyGroupNotAllowed) {
		return deniedKeyGroup(groupID)
	}
	return errors.New(message)
}
