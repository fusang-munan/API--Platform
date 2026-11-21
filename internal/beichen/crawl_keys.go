package beichen

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type CrawlKey struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	KeyPrefix  *string        `json:"key_prefix"`
	GroupID    *int64         `json:"group_id"`
	Group      *string        `json:"group"`
	Rate       *string        `json:"rate"`
	Peak       WebsiteKeyPeak `json:"peak"`
	ObservedAt *time.Time     `json:"observed_at"`
	ChangedAt  *time.Time     `json:"changed_at"`
	Status     string         `json:"status"`
	GroupCheck string         `json:"group_check"`
	Models     []string       `json:"models"`
	Message    string         `json:"message,omitempty"`
	*CrawlKeyModels
}

type CrawlKeyModels struct {
	Check            string              `json:"model_check"`
	Message          string              `json:"model_message,omitempty"`
	ActualModelCount *int                `json:"actual_model_count"`
	ActualModels     []string            `json:"actual_models"`
	Matched          []CrawlKeyModelPair `json:"matched"`
	OnlyKey          []string            `json:"only_key"`
	OnlyGroup        []string            `json:"only_group"`
}

type crawlWebsiteKeys struct {
	keys      []WebsiteKey
	models    []CrawlKeyModels
	readErr   error
	modelsErr error
	saveErr   error
	saved     bool
}

var errCrawlKeyModelsPartial = errors.New("部分北辰 Key 的实际模型清单无法读取")

func (s *Service) crawlDirectoryAndKeys(ctx context.Context, provider uuid.UUID, options CrawlOptions) (directoryCrawl, error, crawlWebsiteKeys) {
	start := time.Now()
	var crawl directoryCrawl
	var dirErr error
	var result crawlWebsiteKeys
	var modelKeys map[int64]string
	read := func(ctx context.Context) (websiteKeyObservation, error) {
		observation, err := readWithWebsiteKeySession(ctx, s, provider, options.PanelToken, func(auth panelAuthorization) (websiteKeyObservation, error) {
			crawl, dirErr = s.fetchWebsiteCrawl(ctx, auth.bearer)
			if panelAuthFailed(dirErr) {
				return websiteKeyObservation{}, dirErr
			}
			if options.IncludeKeys {
				modelKeys = make(map[int64]string)
			}
			keys, err := s.readAccountKeys(ctx, auth.bearer, modelKeys)
			observation := websiteKeyObservation{keys: keys, tokenVersion: auth.tokenVersion}
			if err == nil {
				if missing := websiteKeysMissingGroups(keys); len(missing) != 0 {
					err = supplementWebsiteKeyGroups(keys, missing, crawl.rawResponse)
				}
			}
			return observation, err
		})
		result.keys = make([]WebsiteKey, 0, len(observation.keys))
		for _, key := range observation.keys {
			result.keys = append(result.keys, websiteKeyView(key))
		}
		result.readErr = err
		return observation, err
	}
	if options.SaveKeys {
		saved, err := s.refreshWebsiteKeysWith(ctx, provider, true, read)
		switch {
		case err != nil:
			// A database error can contain connection or statement details.
			result.saveErr = errors.New("无法保存北辰 Key 清单，请检查数据库、迁移和同步状态")
		case saved != nil:
			result.saved, result.keys = true, saved
		case result.readErr == nil:
			result.saveErr = errWebsiteKeySessionChanged
		}
	} else {
		_, _ = read(ctx)
	}
	if failure := errors.Join(result.readErr, result.saveErr); failure != nil && dirErr == nil && crawl.rawResponse == nil {
		crawl, _ = failedDirectoryCrawl(failure)
	}
	if options.IncludeKeys {
		result.models = s.readCrawlKeyModelLists(ctx, result.keys, modelKeys)
		if slices.ContainsFunc(result.models, func(view CrawlKeyModels) bool { return view.ActualModelCount == nil }) {
			result.modelsErr = errCrawlKeyModelsPartial
		}
	}
	crawl.StartedAt, crawl.DurationMS = start.UTC(), time.Since(start).Milliseconds()
	return crawl, dirErr, result
}

func (s *Service) readCrawlKeyModelLists(ctx context.Context, keys []WebsiteKey, modelKeys map[int64]string) []CrawlKeyModels {
	views := make([]CrawlKeyModels, len(keys))
	const concurrency = 4
	slots := make(chan struct{}, concurrency)
	var running sync.WaitGroup
	for i, key := range keys {
		secret := modelKeys[key.ID]
		if secret == "" {
			views[i] = CrawlKeyModels{Check: "unverified", Message: "官网未提供可用于查询实际型号的完整 Key"}
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			views[i] = CrawlKeyModels{Check: "unavailable", Message: "读取实际型号超时或已取消"}
			continue
		}
		running.Add(1)
		go func() {
			defer running.Done()
			defer func() { <-slots }()
			views[i] = s.readCrawlKeyModelList(ctx, secret)
		}()
	}
	running.Wait()
	return views
}

func (s *Service) readCrawlKeyModelList(ctx context.Context, secret string) CrawlKeyModels {
	entries, err := s.fetchKeyCatalog(ctx, secret)
	if err != nil {
		return CrawlKeyModels{Check: "unavailable", Message: err.Error()}
	}
	models := crawlModelIDs(entries)
	for _, name := range models {
		if !validModel(name) || strings.Contains(name, secret) {
			return CrawlKeyModels{Check: "unavailable", Message: "北辰模型目录含无效型号"}
		}
	}
	slices.Sort(models)
	count := len(models)
	view := CrawlKeyModels{Check: "checked", ActualModelCount: &count, ActualModels: models}
	if count == 0 {
		view.Check, view.Message = "empty", "此 Key 的 /v1/models 返回空清单，当前无可用模型"
	}
	return view
}

func (crawl directoryCrawl) checkedKeys(keys []WebsiteKey, modelLists []CrawlKeyModels) []CrawlKey {
	groupModels := map[int64]map[string]struct{}{}
	for _, platform := range crawl.platforms {
		for _, group := range platform.Groups {
			if group.ID > 0 && groupModels[group.ID] == nil {
				groupModels[group.ID] = map[string]struct{}{}
			}
		}
		for _, model := range platform.Models {
			for _, group := range directoryModelGroups(platform, model) {
				if models := groupModels[group.ID]; models != nil {
					models[model.Name] = struct{}{}
				}
			}
		}
	}
	rows := make([]CrawlKey, 0, len(keys))
	for i, key := range keys {
		row := CrawlKey{
			ID: key.ID, Name: key.Name, KeyPrefix: key.KeyPrefix, GroupID: key.GroupID,
			Group: key.GroupName, Rate: key.Rate, Peak: key.Peak, Status: key.Status, Models: []string{},
			ObservedAt: key.ObservedAt, ChangedAt: key.ChangedAt,
		}
		if row.GroupID == nil {
			row.GroupCheck, row.Message = "unknown_group", "Key 清单没有可核实的分组 ID"
		} else {
			models, listed := groupModels[*row.GroupID]
			switch {
			case len(models) != 0:
				row.GroupCheck, row.Models = "listed", slices.Sorted(maps.Keys(models))
			case !crawl.membershipComplete:
				row.GroupCheck, row.Message = "unverified", "渠道页的分组或模型资料不完整，暂无法确认"
			case listed:
				row.GroupCheck, row.Message = "no_models", "该分组未分配模型"
			default:
				row.GroupCheck, row.Message = "missing_group", "该分组未分配模型"
			}
		}
		if modelLists != nil {
			row.CrawlKeyModels = &modelLists[i]
			if row.ActualModelCount != nil && row.GroupID != nil && crawl.membershipComplete {
				row.Matched, row.OnlyKey, row.OnlyGroup = compareCrawlModelNames(row.ActualModels, row.Models)
			}
		}
		rows = append(rows, row)
	}
	return rows
}
