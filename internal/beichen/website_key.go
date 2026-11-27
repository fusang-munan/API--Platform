package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type websiteKeyCatalog struct {
	group              directoryGroup
	keyGroup           *WebsiteGroup
	keyGroupAt         time.Time
	keyID              int64
	entries            []catalog.ProviderEntry
	prices             map[string]json.RawMessage
	at                 time.Time
	keyBilling         *billingInfo // Key-only catalog when the group has no website listing.
	keyBillingSampleAt time.Time    // Sample used for a verified account-rate fallback.
	accountRateErr     error
	websiteModels      []string
}

func (s *Service) readCredentialWebsiteCatalog(ctx context.Context, credential uuid.UUID, key string) (websiteKeyCatalog, error) {
	access, err := s.loadPriceAccess(ctx, credential)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	return readWebsiteKey(ctx, s, access, key, func(token string, identity listedKey) (websiteKeyCatalog, error) {
		return s.fetchWebsiteKeyCatalog(ctx, token, identity)
	})
}

func (directory websiteKeyCatalog) catalogQuote() panelQuote {
	quote := panelQuote{groupID: directory.group.ID}
	if directory.keyBilling != nil {
		quote.websiteModels = []string{}
		return quote
	}
	if directory.websiteModels != nil {
		quote.websiteModels = directory.websiteModels
		return quote
	}
	quote.websiteModels = make([]string, 0, len(directory.entries))
	for _, model := range directory.entries {
		quote.websiteModels = append(quote.websiteModels, model.ID)
	}
	return quote
}

// The account's Key list is the only source of the Key and group identity.
// A name, a configured group hint or a public usage response cannot prove it.
func readWebsiteKey[T any](ctx context.Context, s *Service, access priceAccess, key string,
	read func(string, listedKey) (T, error)) (T, error) {
	var zero T
	if access.disabled {
		return zero, errors.New("该渠道的北辰网页读取已停用")
	}
	verified := func(token string) (T, error) {
		return readIdentifiedWebsiteKey(ctx, s, access, key, token, read)
	}
	auth, managed, err := s.managedPanelAuthorization(ctx, access.providerID, "")
	if err != nil {
		return zero, err
	}
	if managed {
		return readWithPanelAuthorization(ctx, s, access.providerID, auth, true, verified)
	}
	if err := s.checkLegacyPriceAccount(access, auth.delivered); err != nil {
		return zero, err
	}
	if !auth.delivered && access.legacyToken != "" && (access.channel == "" || !s.WebSessionConfigured()) {
		return verified(access.legacyToken)
	}
	if !s.WebSessionConfigured() {
		return zero, errPanelAccountMissing
	}
	return readWithCapturedWebSession(ctx, s, verified)
}

func readIdentifiedWebsiteKey[T any](ctx context.Context, s *Service, access priceAccess, key, token string,
	read func(string, listedKey) (T, error)) (T, error) {
	var zero T
	if !validPanelBearer(token) || token == key {
		return zero, errPanelAccountMissing
	}
	keys, err := s.accountKeys(ctx, token)
	if err != nil {
		return zero, err
	}
	match, err := identifyChannel(key, access.channel, access.label, keys)
	if err != nil && !errors.Is(err, errKeyGroupMissing) {
		return zero, err
	}
	return read(token, match)
}

func (s *Service) fetchWebsiteKeyCatalog(ctx context.Context, token string, key listedKey) (websiteKeyCatalog, error) {
	group := key.websiteGroup()
	groupAt := s.now().UTC()
	observed := websiteKeyCatalog{keyGroup: &group, keyGroupAt: groupAt, keyID: key.ID}
	if group.ID == 0 {
		return observed, errKeyGroupMissing
	}
	crawl, err := s.fetchWebsiteCrawl(ctx, token)
	if errors.Is(err, errWebsiteDirectoryEmpty) && crawl.membershipComplete && crawl.GroupCount == 0 {
		return observed, errors.Join(err, errPanelGroupNotFound)
	}
	// A bad price in another group can leave every group identifiable.
	if err != nil && !(errors.Is(err, errWebsiteDirectoryPartial) && crawl.membershipComplete) {
		return observed, err
	}
	directory, err := selectWebsiteKeyCatalog(crawl.platforms, group.ID)
	if err != nil {
		if errors.Is(err, errPanelGroupNotFound) && key.Group != nil {
			observed.prices = websiteBasePrices(crawl.platforms, key.Group.Platform)
		}
		return observed, err
	}
	if group.Rate != nil && (directory.group.Rate == nil || !samePanelRate(*group.Rate, *directory.group.Rate)) {
		return observed, errPanelRulesConflict
	}
	if group.Name == "" {
		group.Name = directory.group.Name
	}
	if group.Rate == nil {
		group.Rate = directory.group.Rate
	}
	directory.keyGroup, directory.keyGroupAt, directory.keyID = &group, groupAt, key.ID
	return directory, nil
}

// Only shared quotes from the Key group's confirmed platform are eligible.
func websiteBasePrices(platforms []directoryPlatform, platform string) map[string]json.RawMessage {
	if platform == "" || platform == "composite" {
		return nil
	}
	prices := make(map[string]json.RawMessage)
	for _, section := range platforms {
		if section.Platform != platform || (section.ChannelStatus != "" && section.ChannelStatus != "active") {
			continue
		}
		for _, model := range section.Models {
			if model.GroupID != 0 || (model.Platform != "" && model.Platform != section.Platform) {
				continue
			}
			var price json.RawMessage
			if usage.BeichenTokenQuoteUsable(model.Pricing) {
				price = model.Pricing
			}
			if previous, found := prices[model.Name]; found && !bytes.Equal(previous, price) {
				price = nil
			}
			prices[model.Name] = price
		}
	}
	return prices
}

func (key listedKey) websiteGroup() WebsiteGroup {
	view := websiteKeyView(key)
	group := WebsiteGroup{ID: key.GroupID, PeakEnabled: view.Peak.Enabled}
	if view.GroupName != nil {
		group.Name = *view.GroupName
	}
	if view.Rate != nil {
		rate := json.Number(*view.Rate)
		group.Rate = &rate
	}
	if view.Peak.Rate != nil {
		rate := json.Number(*view.Peak.Rate)
		group.PeakRate = &rate
	}
	return group
}

func selectWebsiteKeyCatalog(platforms []directoryPlatform, groupID int64) (websiteKeyCatalog, error) {
	if groupID <= 0 {
		return websiteKeyCatalog{}, errPanelGroupUnverified
	}
	directory := websiteKeyCatalog{prices: map[string]json.RawMessage{}}
	entries := map[string]catalog.ProviderEntry{}
	var channelID int64
	var channel string
	seenPlatforms := map[string]bool{}
	for _, section := range platforms {
		for _, group := range section.Groups {
			if group.ID != groupID {
				continue
			}
			if section.Incomplete || seenPlatforms[section.Platform] ||
				(section.ChannelStatus != "" && section.ChannelStatus != "active") ||
				(group.Status != "" && group.Status != "active") ||
				(group.Platform != "" && group.Platform != section.Platform && group.Platform != "composite") {
				return websiteKeyCatalog{}, errPanelGroupUnverified
			}
			if directory.group.ID != 0 && (channelID != section.ChannelID || channel != section.Channel ||
				group.Platform != "composite" || !sameWebsiteGroup(directory.group, group)) {
				return websiteKeyCatalog{}, errPanelGroupUnverified
			}
			channelID, channel = section.ChannelID, section.Channel
			directory.group, directory.at = group, section.ObservedAt
			seenPlatforms[section.Platform] = true
			for _, model := range section.Models {
				if !namesGroup(directoryModelGroups(section, model), groupID) {
					continue
				}
				name := strings.ToLower(model.Name)
				if _, duplicate := entries[name]; duplicate ||
					(model.Platform != "" && model.Platform != section.Platform) {
					return websiteKeyCatalog{}, errPanelGroupUnverified
				}
				raw := map[string]any{"id": model.Name, "source": "beichen_website_group", "group_id": groupID}
				protocols := model.Protocols
				if protocols == nil {
					protocols = []string{}
					if adapter, ok := catalog.ResolveAdapter(section.Platform); ok {
						protocols = append(protocols, string(adapter))
					}
				}
				raw["supported_protocols"] = protocols
				entries[name] = catalog.ProviderEntry{ID: model.Name, SupportedProtocols: protocols, Raw: raw}
				directory.prices[model.Name] = model.Pricing
			}
		}
	}
	if directory.group.ID == 0 {
		return websiteKeyCatalog{}, errPanelGroupNotFound
	}
	if len(entries) > maxRows {
		return websiteKeyCatalog{}, errors.New("北辰网页分组模型数量超限")
	}
	directory.entries = make([]catalog.ProviderEntry, 0, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		directory.entries = append(directory.entries, entries[name])
	}
	return directory, nil
}

func sameWebsiteGroup(left, right directoryGroup) bool {
	// OwnModels is a property of the platform projection.
	left.OwnModels, right.OwnModels = false, false
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return string(a) == string(b)
}

func (directory websiteKeyCatalog) billing() (billingInfo, panelQuote, error) {
	group := directory.group
	quote := directory.catalogQuote()
	quote.verifiedKeyModels = directory.keyBilling != nil || directory.websiteModels != nil
	if directory.accountRateErr != nil {
		return billingInfo{}, quote, directory.accountRateErr
	}
	var info billingInfo
	if directory.keyBilling != nil {
		info = *directory.keyBilling
		if info.ResolvedRate == "" && info.PeakEnabled != nil && !*info.PeakEnabled {
			info.ResolvedRate = info.Multiplier
		}
	} else {
		info = billingInfo{GroupID: &group.ID, ObservedAt: directory.at, Timezone: group.Timezone,
			PeakEnabled: group.PeakEnabled, PeakStart: group.PeakStart, PeakEnd: group.PeakEnd}
		if group.Subscription != "standard" || group.Rate == nil || group.PeakEnabled == nil {
			return info, quote, errors.New("北辰网页分组缺少可确认的余额计费规则")
		}
		if websiteGroupRate(*group.Rate) == nil {
			return info, quote, errors.New("北辰网页分组倍率无效")
		}
		info.GroupRate, info.ResolvedRate = *group.Rate, *group.Rate
		if group.PeakRate != nil {
			info.PeakRate = *group.PeakRate
		}
	}
	if group.AccountRate != nil {
		info.UserRate, info.ResolvedRate = group.AccountRate, *group.AccountRate
		info.accountRateSource = group.AccountRateSource
	}
	if !directory.keyBillingSampleAt.IsZero() {
		info.ObservedAt = directory.keyBillingSampleAt
	}
	if directory.keyBilling == nil || group.AccountRate != nil {
		if err := applyWebsiteBillingRate(&info); err != nil {
			return info, quote, err
		}
	}

	ends, err := panelExpiry(info, directory.at)
	if err != nil {
		return info, quote, err
	}
	quote.expiresAt = ends
	quote.terms = make(map[string]json.RawMessage)
	for _, model := range directory.entries {
		raw := directory.prices[model.ID]
		if hasPanelPrice(raw) {
			quote.terms[model.ID] = raw
		} else if directory.keyBilling != nil {
			quote.notListed = append(quote.notListed, model.ID)
		}
		if rates, ok := flatPanelRates(raw); ok {
			quote.rows = append(quote.rows, priceRow{Model: model.ID, Rates: rates})
		}
	}
	if len(quote.terms) == 0 {
		quote.err = errPanelEmpty
	}
	if directory.keyBilling == nil && group.AccountRate == nil {
		return info, quote, errWebsiteAccountRateUnverified
	}
	return info, quote, nil
}

func applyWebsiteBillingRate(info *billingInfo) error {
	rate := exactDecimal(info.ResolvedRate)
	if rate == nil || rate.Cmp(big.NewRat(1000, 1)) > 0 {
		return errors.New("北辰网页生效倍率无效")
	}
	info.Multiplier = info.ResolvedRate
	if info.PeakEnabled != nil && *info.PeakEnabled {
		loc, locErr := time.LoadLocation(info.Timezone)
		start, startErr := time.Parse("15:04", info.PeakStart)
		end, endErr := time.Parse("15:04", info.PeakEnd)
		factor := exactDecimal(info.PeakRate)
		if info.Timezone == "" || info.Timezone == "Local" || locErr != nil ||
			startErr != nil || endErr != nil || !start.Before(end) || factor == nil {
			return errors.New("北辰网页分组缺少可确认的高峰计费规则")
		}
		info.AppliedPeak = "1"
		if inPanelPeak(info.ObservedAt.In(loc), start, end) {
			info.AppliedPeak = info.PeakRate
			value := new(big.Rat).Mul(rate, factor)
			if value.Cmp(big.NewRat(1000, 1)) > 0 {
				return errors.New("北辰网页分组的高峰有效倍率超限")
			}
			info.Multiplier = json.Number(websiteDecimalText(value))
		}
	}
	return nil
}

func websiteDecimalText(value *big.Rat) string {
	// The website's decimal fields are bounded to 128 fractional digits and
	// a two-digit exponent. This also preserves products of two such rates.
	return strings.TrimRight(strings.TrimRight(value.FloatString(460), "0"), ".")
}
