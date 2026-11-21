package beichen

import (
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
)

type websiteKeyCatalog struct {
	group   directoryGroup
	entries []catalog.ProviderEntry
	prices  map[string]json.RawMessage
	at      time.Time
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
	quote := panelQuote{groupID: directory.group.ID, websiteModels: make([]string, 0, len(directory.entries))}
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
		if !validPanelBearer(token) || token == key {
			return zero, errPanelAccountMissing
		}
		keys, err := s.accountKeys(ctx, token)
		if err != nil {
			return zero, err
		}
		match, err := identifyChannel(key, access.channel, access.label, keys)
		if err != nil {
			return zero, err
		}
		return read(token, match)
	}
	auth, managed, err := s.managedPanelAuthorization(ctx, access.providerID, "")
	if err != nil {
		return zero, err
	}
	if managed {
		return readWithPanelAuthorization(ctx, s, access.providerID, auth, true, verified)
	}
	if err := s.checkLegacyPriceAccount(access); err != nil {
		return zero, err
	}
	if access.legacyToken != "" && (access.channel == "" || !s.WebSessionConfigured()) {
		return verified(access.legacyToken)
	}
	if !s.WebSessionConfigured() {
		return zero, errPanelAccountMissing
	}
	return readWithCapturedWebSession(ctx, s, verified)
}

func (s *Service) fetchWebsiteKeyCatalog(ctx context.Context, token string, key listedKey) (websiteKeyCatalog, error) {
	crawl, err := s.fetchWebsiteCrawl(ctx, token)
	if err != nil {
		return websiteKeyCatalog{}, err
	}
	catalog, err := selectWebsiteKeyCatalog(crawl.platforms, key.GroupID)
	if err != nil {
		return catalog, err
	}
	if key.Group != nil && key.Group.Rate != "" &&
		(catalog.group.Rate == nil || !samePanelRate(key.Group.Rate, *catalog.group.Rate)) {
		return websiteKeyCatalog{}, errPanelRulesConflict
	}
	return catalog, nil
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
	info := billingInfo{GroupID: &group.ID, ObservedAt: directory.at, Timezone: group.Timezone,
		PeakEnabled: group.PeakEnabled, PeakStart: group.PeakStart, PeakEnd: group.PeakEnd}
	quote := panelQuote{groupID: group.ID}
	if group.Subscription != "standard" || group.Rate == nil || group.PeakEnabled == nil {
		return info, quote, errors.New("北辰网页分组缺少可确认的余额计费规则")
	}
	rate := exactDecimal(*group.Rate)
	if rate == nil || rate.Cmp(big.NewRat(1000, 1)) > 0 {
		return info, quote, errors.New("北辰网页分组倍率无效")
	}
	info.GroupRate, info.ResolvedRate, info.Multiplier = *group.Rate, *group.Rate, *group.Rate
	if group.PeakRate != nil {
		info.PeakRate = *group.PeakRate
	}
	if *group.PeakEnabled {
		loc, locErr := time.LoadLocation(group.Timezone)
		start, startErr := time.Parse("15:04", group.PeakStart)
		end, endErr := time.Parse("15:04", group.PeakEnd)
		factor := exactDecimal(info.PeakRate)
		if group.Timezone == "" || group.Timezone == "Local" || locErr != nil ||
			startErr != nil || endErr != nil || !start.Before(end) || factor == nil {
			return info, quote, errors.New("北辰网页分组缺少可确认的高峰计费规则")
		}
		info.AppliedPeak = "1"
		if inPanelPeak(directory.at.In(loc), start, end) {
			info.AppliedPeak = info.PeakRate
			value := new(big.Rat).Mul(rate, factor)
			if value.Cmp(big.NewRat(1000, 1)) > 0 {
				return info, quote, errors.New("北辰网页分组的高峰有效倍率超限")
			}
			info.Multiplier = json.Number(websiteDecimalText(value))
		}
	}
	ends, err := panelExpiry(info, directory.at)
	if err != nil {
		return info, quote, err
	}
	quote.expiresAt = ends
	quote.terms = make(map[string]json.RawMessage)
	quote.websiteModels = make([]string, 0, len(directory.entries))
	for _, model := range directory.entries {
		raw := directory.prices[model.ID]
		quote.websiteModels = append(quote.websiteModels, model.ID)
		if hasPanelPrice(raw) {
			quote.terms[model.ID] = raw
		}
		if rates, ok := flatPanelRates(raw); ok {
			quote.rows = append(quote.rows, priceRow{Model: model.ID, Rates: rates})
		}
	}
	if len(quote.terms) == 0 {
		quote.err = errPanelEmpty
	}
	return info, quote, nil
}

func websiteDecimalText(value *big.Rat) string {
	// The website's decimal fields are bounded to 128 fractional digits and
	// a two-digit exponent. This also preserves products of two such rates.
	return strings.TrimRight(strings.TrimRight(value.FloatString(460), "0"), ".")
}
