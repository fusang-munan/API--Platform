package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CrawlRun struct {
	StartedAt          time.Time      `json:"started_at"`
	DurationMS         int64          `json:"duration_ms"`
	PlatformCount      int            `json:"platform_count"`
	GroupCount         int            `json:"group_count"`
	ModelCount         int            `json:"model_count"`
	ExpectedModelCount int            `json:"expected_model_count"`
	Failures           []CrawlFailure `json:"failures"`
}

type CrawlFailure struct {
	ChannelID int64  `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
}

func (run CrawlRun) panelSummary() PanelCrawlSummary {
	var startedAt *time.Time
	if !run.StartedAt.IsZero() {
		startedAt = &run.StartedAt
	}
	count := len(run.Failures)
	return PanelCrawlSummary{LastRunAt: startedAt, Platforms: &run.PlatformCount, Models: &run.ModelCount,
		Failures: &count, FailureDetails: run.Failures}
}

type directoryCrawl struct {
	CrawlRun
	platforms   []directoryPlatform
	rawResponse []byte
}

func (crawl *directoryCrawl) addFailure(path string, section directoryPlatform, reason string) {
	crawl.Failures = append(crawl.Failures, CrawlFailure{
		ChannelID: section.ChannelID, Channel: section.Channel, Platform: section.Platform, Path: path, Reason: reason,
	})
}

func failedDirectoryCrawl(err error) (directoryCrawl, error) {
	var crawl directoryCrawl
	crawl.addFailure("response", directoryPlatform{}, err.Error())
	return crawl, err
}

func decodeWebsiteDirectory(body []byte, observedAt time.Time) (directoryCrawl, error) {
	rawResponse := body
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	data := body
	if len(body) == 0 || body[0] != '[' {
		var err error
		data, err = panelData(body)
		if err != nil {
			return failedDirectoryCrawl(err)
		}
	}
	var channels []json.RawMessage
	if json.Unmarshal(data, &channels) != nil || channels == nil {
		return failedDirectoryCrawl(errors.New("北辰官网响应的 data 不是渠道数组"))
	}

	crawl := directoryCrawl{CrawlRun: CrawlRun{Failures: []CrawlFailure{}}, rawResponse: rawResponse}
	sourceNames := map[string]struct{}{}
	for i, raw := range channels {
		var channel struct {
			ID        int64             `json:"id"`
			Name      string            `json:"name"`
			Status    string            `json:"status"`
			Platforms []json.RawMessage `json:"platforms"`
		}
		err := json.Unmarshal(raw, &channel)
		section := directoryPlatform{
			ChannelID: channel.ID, Channel: strings.TrimSpace(channel.Name), ChannelStatus: channel.Status, ObservedAt: observedAt,
		}
		channelPath := fmt.Sprintf("data[%d]", i)
		if err != nil || channel.Platforms == nil {
			crawl.addFailure(channelPath, section, "渠道资料无效或缺少 platforms 数组")
		}
		for j, rawPlatform := range channel.Platforms {
			path := fmt.Sprintf("%s.platforms[%d]", channelPath, j)
			platform, sourceModels, problems, readable := decodeDirectoryPlatform(rawPlatform, section)
			for _, name := range sourceModels {
				sourceNames[name] = struct{}{}
			}
			if len(problems) != 0 {
				crawl.addFailure(path, platform, strings.Join(problems, "，"))
			}
			if readable {
				platform.Scope = directoryScope(platform, i)
				platform.Incomplete = len(problems) != 0
				crawl.platforms = append(crawl.platforms, platform)
			}
		}
	}
	crawl.countModelsAndGroups()
	crawl.ExpectedModelCount = len(sourceNames)
	if crawl.ModelCount == 0 {
		if len(crawl.Failures) == 0 {
			crawl.addFailure("response", directoryPlatform{}, errWebsiteDirectoryEmpty.Error())
		}
		return crawl, errWebsiteDirectoryEmpty
	}
	if crawl.ExpectedModelCount != crawl.ModelCount && len(crawl.Failures) == 0 {
		crawl.addFailure("response", directoryPlatform{}, "报告模型数与响应中去重后的模型数不一致")
	}
	if len(crawl.Failures) != 0 {
		return crawl, errWebsiteDirectoryPartial
	}
	return crawl, nil
}

type websiteModelLists struct {
	Supported json.RawMessage `json:"supported_models"`
	Models    json.RawMessage `json:"models"`
}

func decodeDirectoryPlatform(raw json.RawMessage, section directoryPlatform) (directoryPlatform, []string, []string, bool) {
	var input struct {
		websiteModelLists
		Platform string          `json:"platform"`
		Groups   json.RawMessage `json:"groups"`
	}
	err := json.Unmarshal(raw, &input)
	var problems, sourceNames []string
	platform := strings.TrimSpace(input.Platform)
	hasPlatform := err == nil && validModel(platform)
	if hasPlatform {
		section.Platform = platform
	} else {
		problems = append(problems, "platform 名称缺失或格式无效")
	}
	section.Groups = []directoryGroup{}
	section.Models = []directoryModel{}
	hasModels := readDirectoryModels(&section, input.websiteModelLists, 0, "", &sourceNames, &problems)
	var groups []json.RawMessage
	if len(input.Groups) != 0 && json.Unmarshal(input.Groups, &groups) != nil {
		problems = append(problems, "groups 不是分组数组")
	}
	for i, item := range groups {
		var group struct {
			directoryGroup
			websiteModelLists
		}
		if json.Unmarshal(item, &group) != nil || group.ID < 0 || group.ID > 9007199254740991 ||
			(group.ID == 0 && strings.TrimSpace(group.Name) == "") {
			problems = append(problems, fmt.Sprintf("groups[%d] 分组资料无效", i))
			continue
		}
		if group.ID > 0 {
			group.OwnModels = readDirectoryModels(&section, group.websiteModelLists, group.ID,
				fmt.Sprintf("groups[%d].", i), &sourceNames, &problems)
			hasModels = hasModels || group.OwnModels
		}
		section.Groups = append(section.Groups, group.directoryGroup)
	}
	section.Models = slices.DeleteFunc(section.Models, func(model directoryModel) bool {
		if model.GroupID == 0 || namesGroup(section.Groups, model.GroupID) {
			return false
		}
		problems = append(problems, "模型的 group_id 未在本平台的 groups 中登记")
		return true
	})
	if !hasModels {
		problems = append(problems, "没有可读取的 models 或 supported_models 数组")
	}
	return section, sourceNames, problems, hasPlatform && hasModels
}

func readDirectoryModels(section *directoryPlatform, input websiteModelLists, groupID int64, prefix string,
	sourceNames, problems *[]string) bool {
	hasModelList := false
	for _, list := range []struct {
		name string
		raw  json.RawMessage
	}{{"supported_models", input.Supported}, {"models", input.Models}} {
		if websiteMissing(list.raw) {
			continue
		}
		var rows []json.RawMessage
		if json.Unmarshal(list.raw, &rows) != nil {
			*problems = append(*problems, prefix+list.name+" 不是模型数组")
			continue
		}
		hasModelList = true
		for i, item := range rows {
			model, problem := decodeDirectoryModel(item, section.ObservedAt)
			if model.Name != "" {
				*sourceNames = append(*sourceNames, model.Name)
			}
			if groupID != 0 && model.GroupID != 0 && model.GroupID != groupID {
				*problems = append(*problems, fmt.Sprintf("%s%s[%d] 的 group_id 与所属分组冲突", prefix, list.name, i))
				continue
			}
			if groupID != 0 {
				model.GroupID = groupID
			}
			if validModel(model.Name) {
				section.Models = append(section.Models, model)
			}
			if problem != "" {
				*problems = append(*problems, fmt.Sprintf("%s%s[%d] %s", prefix, list.name, i, problem))
			}
		}
	}
	return hasModelList
}

const crawlPriceInvalid = "price_conditions_invalid"

func decodeDirectoryModel(raw json.RawMessage, at time.Time) (directoryModel, string) {
	var input struct {
		Name      string          `json:"name"`
		ID        json.RawMessage `json:"id"`
		Platform  string          `json:"platform"`
		GroupID   json.RawMessage `json:"group_id"`
		Protocols []string        `json:"supported_protocols"`
		Pricing   json.RawMessage `json:"pricing"`
	}
	err := json.Unmarshal(raw, &input)
	if len(raw) != 0 && raw[0] == '"' {
		err = json.Unmarshal(raw, &input.Name)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		var id string
		if json.Unmarshal(input.ID, &id) == nil {
			name = strings.TrimSpace(id)
		}
	}
	if !validModel(name) {
		return directoryModel{Name: name}, "缺少有效模型名称"
	}
	var groupID int64
	if !websiteMissing(input.GroupID) {
		var number json.Number
		if json.Unmarshal(input.GroupID, &number) != nil {
			return directoryModel{}, "group_id 格式无效"
		}
		var groupErr error
		groupID, groupErr = number.Int64()
		if groupErr != nil || groupID < 0 || groupID > 9007199254740991 {
			return directoryModel{}, "group_id 格式无效"
		}
	}
	pricing := normalizeWebsitePricing(input.Pricing)
	var number json.Number
	if json.Unmarshal(pricing, &number) == nil {
		if amount := exactDecimal(number); amount != nil && amount.Sign() == 0 {
			pricing = nil
		}
	}
	blocker, _ := usage.BeichenTokenQuoteBlocker(pricing)
	model := directoryModel{Name: name, Platform: input.Platform, GroupID: groupID, Protocols: input.Protocols,
		Pricing: pricing, ObservedAt: at, priceBlocker: blocker}
	if blocker == crawlPriceInvalid {
		return model, "pricing 报价格式无效"
	}
	if err != nil {
		return model, "模型资料格式无效"
	}
	return model, ""
}

func (crawl *directoryCrawl) countModelsAndGroups() {
	models := map[string]struct{}{}
	groups := map[string]struct{}{}
	for _, platform := range crawl.platforms {
		for _, model := range platform.Models {
			models[model.Name] = struct{}{}
		}
		for _, group := range platform.Groups {
			key := fmt.Sprint(group.ID)
			if group.ID == 0 {
				key = fmt.Sprintf("%s/%q", platform.Scope, strings.TrimSpace(group.Name))
			}
			groups[key] = struct{}{}
		}
	}
	crawl.PlatformCount, crawl.GroupCount, crawl.ModelCount = len(crawl.platforms), len(groups), len(models)
}

func directoryScope(section directoryPlatform, index int) string {
	platform := strings.ToLower(section.Platform)
	if section.ChannelID > 0 {
		return fmt.Sprintf("%d/%s", section.ChannelID, platform)
	}
	if section.Channel != "" {
		return fmt.Sprintf("%q/%s", strings.ToLower(section.Channel), platform)
	}
	groups := make([]int64, 0, len(section.Groups))
	for _, group := range section.Groups {
		if group.ID > 0 {
			groups = append(groups, group.ID)
		}
	}
	if len(groups) != 0 {
		slices.Sort(groups)
		return fmt.Sprintf("%v/%s", groups, platform)
	}
	return fmt.Sprintf("channel-%d/%s", index, platform)
}

func (s *Service) crawlWebsiteDirectory(ctx context.Context, provider uuid.UUID, panelToken string) (directoryCrawl, error) {
	start := time.Now()
	crawl, err := s.readWebsiteCrawl(ctx, provider, panelToken)
	crawl.StartedAt, crawl.DurationMS = start.UTC(), time.Since(start).Milliseconds()
	return crawl, err
}

func (s *Service) fetchWebsiteCrawl(ctx context.Context, bearer string) (directoryCrawl, error) {
	body, err := s.fetchPanelBody(ctx, bearer)
	if err != nil {
		return failedDirectoryCrawl(err)
	}
	return decodeWebsiteDirectory(body, s.now().UTC())
}

func (s *Service) readWebsiteCrawl(ctx context.Context, provider uuid.UUID, panelToken string) (directoryCrawl, error) {
	authorization, managed, err := s.managedPanelAuthorization(ctx, provider, panelToken)
	if err != nil {
		return failedDirectoryCrawl(err)
	}
	if managed {
		crawl, err := readWithPanelAuthorization(ctx, s, provider, authorization, true, func(token string) (directoryCrawl, error) {
			return s.fetchWebsiteCrawl(ctx, token)
		})
		if err != nil && !errors.Is(err, errWebsiteDirectoryPartial) && !errors.Is(err, errWebsiteDirectoryEmpty) {
			return failedDirectoryCrawl(err)
		}
		return crawl, err
	}
	if !s.WebSessionConfigured() {
		return failedDirectoryCrawl(errors.New("未提供真实凭据，仅 fixture"))
	}
	token, err := s.webPanelToken(ctx)
	if err != nil {
		return failedDirectoryCrawl(errors.New("北辰官网目录会话不可用，请核对网页凭据或登录验证"))
	}
	crawl, err := s.fetchWebsiteCrawl(ctx, token)
	if !panelAuthFailed(err) {
		return crawl, err
	}
	s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
	token, err = s.webPanelToken(ctx)
	if err != nil {
		return failedDirectoryCrawl(errors.New("北辰官网目录会话续期失败"))
	}
	crawl, err = s.fetchWebsiteCrawl(ctx, token)
	if panelAuthFailed(err) {
		s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
	}
	return crawl, err
}
