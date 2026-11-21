package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CrawlKeyModelPair struct {
	KeyModel     string `json:"key_model"`
	ChannelModel string `json:"channel_model"`
	Match        string `json:"match"`
}

type CrawlKeyModelMatch struct {
	CrawlKeyModelPair
	PriceComplete bool `json:"price_complete"`
}

type CrawlKeyModelComparison struct {
	Channel     string               `json:"channel"`
	GroupID     *int64               `json:"group_id"`
	State       string               `json:"state"`
	Error       string               `json:"error,omitempty"`
	Matched     []CrawlKeyModelMatch `json:"matched"`
	OnlyKey     []string             `json:"only_key"`
	OnlyChannel []string             `json:"only_channel"`
}

func newCrawlKeyModelComparison(channel string) CrawlKeyModelComparison {
	return CrawlKeyModelComparison{Channel: channel, State: "unverified", Matched: []CrawlKeyModelMatch{},
		OnlyKey: []string{}, OnlyChannel: []string{}}
}

func (r *CrawlKeyModelComparison) compare(crawl directoryCrawl, entries []catalog.ProviderEntry, groupID int64) {
	r.GroupID = &groupID
	if !crawl.membershipComplete {
		r.Error = "渠道页型号资料不完整"
		return
	}
	group, err := selectWebsiteKeyCatalog(crawl.platforms, groupID)
	if err != nil && !errors.Is(err, errPanelGroupNotFound) {
		r.Error = "渠道页分组资料不完整或有歧义"
		return
	}
	keyNames := crawlModelIDs(entries)
	channelNames := make([]string, 0, len(group.entries))
	for _, model := range group.entries {
		channelNames = append(channelNames, model.ID)
	}
	pairs, onlyKey, onlyChannel := compareCrawlModelNames(keyNames, channelNames)
	for _, match := range pairs {
		r.Matched = append(r.Matched, CrawlKeyModelMatch{match, usage.BeichenDirectoryQuoteBlocker(group.prices[match.ChannelModel]) == ""})
	}
	r.OnlyKey, r.OnlyChannel, r.State = onlyKey, onlyChannel, "checked"
}

func crawlModelIDs(entries []catalog.ProviderEntry) []string {
	names := make([]string, 0, len(entries))
	for _, model := range entries {
		names = append(names, model.ID)
	}
	return names
}

func compareCrawlModelNames(keyNames, channelNames []string) ([]CrawlKeyModelPair, []string, []string) {
	pairs := []CrawlKeyModelPair{}
	onlyKey, onlyChannel := []string{}, []string{}
	key, channel := newCatalogNameMatcher(keyNames, nil), newCatalogNameMatcher(channelNames, nil)
	matched := make(map[string]bool, len(channelNames))
	for _, name := range channelNames {
		match := key.match(name, false)
		if match.name == "" || channel.match(match.name, false).name != name {
			onlyChannel = append(onlyChannel, name)
			continue
		}
		matched[match.name] = true
		pairs = append(pairs, CrawlKeyModelPair{KeyModel: match.name, ChannelModel: name, Match: match.kind})
	}
	for _, name := range keyNames {
		if !matched[name] {
			onlyKey = append(onlyKey, name)
		}
	}
	slices.Sort(onlyKey)
	return pairs, onlyKey, onlyChannel
}

type crawlKeyModelTarget struct {
	ciphertext []byte
	channel    string
	verified   bool
}

// The website inventory establishes which provisioned credentials belong to
// this provider's web account. Each comparison rechecks its current group.
func (s *Service) crawlKeyModels(ctx context.Context, provider uuid.UUID, crawl *directoryCrawl, panelToken string) ([]CrawlKeyModelComparison, string) {
	results := []CrawlKeyModelComparison{}
	if s.db == nil || s.crypto == nil {
		return results, ""
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.credential_ciphertext, p.channel, COALESCE(w.match_status='matched' AND inv.status='current'
    AND inv.last_seen > statement_timestamp()-interval '30 minutes', false)
FROM beichen_provision_channels p JOIN provider_credentials c ON c.id=p.credential_id
LEFT JOIN beichen_website_keys w ON w.provider_id=c.provider_id AND w.credential_id=c.id
  AND w.ciphertext_digest=sha256(c.credential_ciphertext)
LEFT JOIN beichen_website_key_sync inv ON inv.provider_id=w.provider_id
WHERE c.provider_id=$1 AND c.status='active'
ORDER BY p.channel`, provider)
	const readFailed = "读取已配置北辰渠道失败"
	if err != nil {
		return results, readFailed
	}
	var targets []crawlKeyModelTarget
	for rows.Next() {
		var target crawlKeyModelTarget
		if err := rows.Scan(&target.ciphertext, &target.channel, &target.verified); err != nil {
			rows.Close()
			return results, readFailed
		}
		targets = append(targets, target)
		results = append(results, newCrawlKeyModelComparison(target.channel))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return results, readFailed
	}
	rows.Close()
	if !slices.ContainsFunc(targets, func(target crawlKeyModelTarget) bool { return target.verified }) {
		for i := range results {
			results[i].Error = "等待官网 Key 清单核对该渠道"
		}
		return results, ""
	}
	keys, err := readWithWebsiteKeySession(ctx, s, provider, panelToken, func(auth panelAuthorization) ([]listedKey, error) {
		keys, err := s.accountKeys(ctx, auth.bearer)
		if err == nil {
			rates, rateErr := s.fetchWebsiteAccountRates(ctx, auth.bearer)
			if rateErr != nil {
				crawl.AccountRateError = "北辰官网账户倍率暂未核实"
			} else {
				crawl.applyAccountRates(rates)
			}
		}
		return keys, err
	})
	if err != nil {
		return results, "读取当前官网 Key 分组失败"
	}
	var round sync.WaitGroup
	for i, target := range targets {
		if !target.verified {
			results[i].Error = "等待官网 Key 清单核对该渠道"
			continue
		}
		round.Add(1)
		go func() {
			defer round.Done()
			s.checkCrawlKeyModels(ctx, &results[i], target, keys, *crawl)
		}()
	}
	round.Wait()
	return results, ""
}

// The website renders these logged-in account rates next to group defaults.
func (s *Service) fetchWebsiteAccountRates(ctx context.Context, bearer string) (map[int64]json.Number, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://beichenapi.com/api/v1/groups/rates", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("X-User-UI-Request", "1")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-cache")
	body, err := s.readResponse(request)
	if err != nil {
		return nil, err
	}
	data, err := panelData(body)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.Number
	var invalidRates = errors.New("北辰官网账户倍率格式无效")
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return nil, invalidRates
	}
	rates := make(map[int64]json.Number, len(raw))
	for id, rate := range raw {
		groupID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || !validWebsiteGroupID(groupID) || strconv.FormatInt(groupID, 10) != id || websiteGroupRate(rate) == nil {
			return nil, invalidRates
		}
		rates[groupID] = rate
	}
	return rates, nil
}

func (crawl *directoryCrawl) applyAccountRates(rates map[int64]json.Number) {
	for i := range crawl.platforms {
		for j := range crawl.platforms[i].Groups {
			group := &crawl.platforms[i].Groups[j]
			if rate, ok := rates[group.ID]; ok {
				group.AccountRate = &rate
			}
		}
	}
	crawl.accountRatesVerified = true
}

func (s *Service) checkCrawlKeyModels(ctx context.Context, result *CrawlKeyModelComparison,
	target crawlKeyModelTarget, keys []listedKey, crawl directoryCrawl) {
	key, err := s.crypto.DecryptCredential(target.ciphertext)
	if err != nil {
		result.Error = "北辰凭据解密失败"
		return
	}
	identity, err := identifyChannel(key, target.channel, "", keys)
	if err != nil {
		result.Error = "当前 Key 分组无法唯一确认"
		return
	}
	entries, err := s.fetchKeyCatalog(ctx, key)
	if err != nil {
		result.GroupID, result.State, result.Error = &identity.GroupID, "unavailable", err.Error()
		return
	}
	result.compare(crawl, entries, identity.GroupID)
}
