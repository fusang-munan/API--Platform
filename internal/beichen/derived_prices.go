package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
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
		return s.fetchBillingCatalog(ctx, job.provider, token, key, identity)
	})
}

func (s *Service) fetchBillingCatalog(ctx context.Context, provider uuid.UUID, token, key string, identity listedKey) (websiteKeyCatalog, error) {
	directory, websiteErr := s.fetchWebsiteKeyCatalog(ctx, token, identity)
	if websiteErr != nil && !errors.Is(websiteErr, errPanelGroupNotFound) {
		return directory, websiteErr
	}
	var hasEvidence bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM beichen_group_price_observations
WHERE provider_id=$1 AND group_id=$2 AND source='derived_from_usage' AND observed_at<=statement_timestamp()
)`, provider, identity.GroupID).Scan(&hasEvidence); err != nil {
		return websiteKeyCatalog{}, errPanelAccountDB
	}
	if !hasEvidence {
		return directory, websiteErr
	}
	if websiteErr != nil {
		return s.fetchDerivedBillingCatalog(ctx, key, identity)
	}
	actual, err := s.fetchKeyCatalog(ctx, key)
	if err != nil {
		return websiteKeyCatalog{}, errors.New("北辰 Key 的实际型号清单无法读取")
	}
	return directory.withActualModels(actual), nil
}

func (directory websiteKeyCatalog) withActualModels(actual []catalog.ProviderEntry) websiteKeyCatalog {
	website := crawlModelIDs(directory.entries)
	pairs, _, _ := compareCrawlModelNames(crawlModelIDs(actual), website)
	prices := make(map[string]json.RawMessage, len(pairs))
	for _, pair := range pairs {
		prices[pair.KeyModel] = directory.prices[pair.ChannelModel]
	}
	directory.websiteModels, directory.entries, directory.prices = website, actual, prices
	return directory
}

func (s *Service) fetchDerivedBillingCatalog(ctx context.Context, key string, identity listedKey) (websiteKeyCatalog, error) {
	body, err := s.fetch(ctx, key, billingPath, nil)
	if err != nil {
		return websiteKeyCatalog{}, errors.New("北辰 Key 的当前计费倍率无法读取")
	}
	info, err := parseBilling(body)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	if group := billingPanelGroup(info); group != 0 && group != identity.GroupID {
		return websiteKeyCatalog{}, errPanelRulesConflict
	}
	if (info.KeyID != 0 && info.KeyID != identity.ID) || (info.APIKeyID != 0 && info.APIKeyID != identity.ID) {
		return websiteKeyCatalog{}, errPanelRulesConflict
	}
	if group := identity.Group; group != nil {
		if group.SubscriptionType != "" && group.SubscriptionType != "standard" {
			return websiteKeyCatalog{}, errPanelRulesConflict
		}
		if (group.Rate != "" && info.GroupRate != "" && !samePanelRate(group.Rate, info.GroupRate)) ||
			(group.PeakEnabled != nil && info.PeakEnabled != nil && *group.PeakEnabled != *info.PeakEnabled) {
			return websiteKeyCatalog{}, errPanelRulesConflict
		}
	}
	// A separately published Key price table needs its own official-price
	// verification. Do not select an estimate over those declared terms.
	if !websiteMissing(info.Pricing) {
		return websiteKeyCatalog{}, errors.New("北辰 Key 已提供单独的价表，需核实正式报价")
	}
	entries, err := s.fetchKeyCatalog(ctx, key)
	if err != nil {
		return websiteKeyCatalog{}, errors.New("北辰 Key 的实际型号清单无法读取")
	}
	info.GroupID = &identity.GroupID
	at := s.now().UTC()
	// The two hosts may differ by a fraction of a second. Wait for a near
	// future sample to become current rather than changing its timestamp.
	if delay := info.ObservedAt.Sub(at); delay > 0 && delay <= time.Second {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return websiteKeyCatalog{}, ctx.Err()
		case <-timer.C:
			at = s.now().UTC()
		}
	}
	if _, err := panelExpiry(info, at); err != nil {
		return websiteKeyCatalog{}, err
	}
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

type derivedPriceRow struct {
	priceRow
	observedAt time.Time
}

func readDerivedPrices(ctx context.Context, tx *sql.Tx, credential uuid.UUID, group int64, at time.Time,
	official map[string]json.RawMessage) ([]derivedPriceRow, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT ON (o.model) o.model,o.pricing,o.observed_at
FROM provider_credentials c
JOIN beichen_group_price_observations o ON o.provider_id=c.provider_id
JOIN provider_model_catalog m ON m.credential_id=c.id AND m.provider_id=c.provider_id
  AND m.model_id::text=o.model AND m.status='active' AND m.last_seen_at>=c.updated_at
WHERE c.id=$1 AND o.group_id=$2 AND o.source='derived_from_usage' AND o.observed_at<=$3
ORDER BY o.model,o.observed_at DESC`, credential, group, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prices []derivedPriceRow
	for rows.Next() {
		var row derivedPriceRow
		var raw json.RawMessage
		if err := rows.Scan(&row.Model, &raw, &row.observedAt); err != nil {
			return nil, err
		}
		if !websiteMissing(official[row.Model]) {
			continue
		}
		if rates, ok := flatPanelRates(raw); ok {
			row.Rates = rates
			prices = append(prices, row)
		}
	}
	return prices, rows.Err()
}
