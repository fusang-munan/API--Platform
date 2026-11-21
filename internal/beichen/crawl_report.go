package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CrawlOptions struct {
	Fixture       []byte
	PanelToken    string
	MatchDB       bool
	IncludeKeys   bool
	SaveKeys      bool
	CompareModels bool
	ProviderID    uuid.UUID
}

type CrawlReport struct {
	CrawlRun
	Source         string       `json:"source"`
	PriceUnit      string       `json:"price_unit"`
	Complete       bool         `json:"complete"`
	KeyMatching    string       `json:"key_matching"`
	KeyMatchError  string       `json:"key_match_error,omitempty"`
	Keys           *[]CrawlKey  `json:"keys,omitempty"`
	KeyReadError   string       `json:"key_read_error,omitempty"`
	KeyModelsError string       `json:"key_models_error,omitempty"`
	KeyStorage     string       `json:"key_storage,omitempty"`
	KeySaveError   string       `json:"key_save_error,omitempty"`
	Models         []CrawlModel `json:"models"`
}

type CrawlModel struct {
	Name       string        `json:"name"`
	Sources    []CrawlSource `json:"sources"`
	KeyMatches []CatalogKey  `json:"key_matches"`
}

type CrawlSource struct {
	ChannelID                 int64             `json:"channel_id,omitempty"`
	Channel                   string            `json:"channel,omitempty"`
	Platform                  string            `json:"platform"`
	Groups                    []WebsiteGroup    `json:"groups"`
	ObservedAt                time.Time         `json:"observed_at"`
	PriceComplete             bool              `json:"price_complete"`
	PriceBlocker              string            `json:"price_blocker,omitempty"`
	Pricing                   json.RawMessage   `json:"pricing"`
	BaseTokenPricesPerMillion map[string]string `json:"base_token_prices_per_million,omitempty"`
	MissingFields             []string          `json:"missing_price_fields"`
	IgnoredFields             []string          `json:"ignored_fields"`
}

// CrawlDirectory uses the scheduled website parser. The optional inventory uses
// the same website session and joins model membership by numeric group ID.
func (s *Service) CrawlDirectory(ctx context.Context, options CrawlOptions) (CrawlReport, error) {
	includeKeys := options.IncludeKeys || options.SaveKeys
	if options.Fixture != nil && includeKeys {
		return CrawlReport{}, errors.New("离线 fixture 不读取或保存官网 Key 清单")
	}
	if options.CompareModels && (options.Fixture != nil || s.db == nil || s.crypto == nil) {
		return CrawlReport{}, errors.New("实时核对型号需要官网会话、数据库和凭据加密密钥")
	}
	if options.SaveKeys && (s.db == nil || s.crypto == nil) {
		return CrawlReport{}, errors.New("保存官网 Key 清单需要数据库和凭据加密密钥")
	}
	source := "website"
	var crawl directoryCrawl
	var keyCrawl crawlWebsiteKeys
	var err error
	if options.Fixture != nil {
		source = "fixture"
		start := time.Now()
		crawl, err = decodeWebsiteDirectory(options.Fixture, start.UTC())
		crawl.StartedAt, crawl.DurationMS = start.UTC(), time.Since(start).Milliseconds()
	} else {
		provider := options.ProviderID
		if options.SaveKeys || options.CompareModels || provider == uuid.Nil && s.db != nil && s.crypto != nil {
			provider, err = s.ResolvePanelProvider(ctx, provider)
		}
		if err == nil {
			if includeKeys {
				crawl, err, keyCrawl = s.crawlDirectoryAndKeys(ctx, provider, options)
			} else {
				crawl, err = s.crawlWebsiteDirectory(ctx, provider, options.PanelToken)
			}
			if options.CompareModels && crawl.membershipComplete {
				crawl.KeyModelComparisons, crawl.KeyModelError = s.crawlKeyModels(ctx, provider, &crawl, options.PanelToken)
				crawl.DurationMS = time.Since(crawl.StartedAt).Milliseconds()
			}
		}
	}
	report := CrawlReport{
		CrawlRun: crawl.CrawlRun, Source: source, PriceUnit: websitePriceUnit, Complete: err == nil,
		KeyMatching: "not_checked", Models: make([]CrawlModel, 0, crawl.ModelCount),
	}
	if includeKeys {
		keys := crawl.checkedKeys(keyCrawl.keys, keyCrawl.models)
		report.Keys = &keys
		if keyCrawl.readErr != nil {
			report.KeyReadError = keyCrawl.readErr.Error()
		}
		if keyCrawl.modelsErr != nil {
			report.KeyModelsError = keyCrawl.modelsErr.Error()
		}
		if options.SaveKeys {
			report.KeyStorage = "failed"
			if keyCrawl.saved {
				report.KeyStorage = "saved"
			}
			if keyCrawl.saveErr != nil {
				report.KeySaveError = keyCrawl.saveErr.Error()
			}
		}
		err = errors.Join(err, keyCrawl.readErr, keyCrawl.modelsErr, keyCrawl.saveErr)
		report.Complete = err == nil
	}
	var keys []discoveryKey
	if options.MatchDB && crawl.ModelCount != 0 {
		var matchErr error
		keys, matchErr = s.readCrawlKeys(ctx, options.ProviderID, crawl.platforms)
		if matchErr != nil {
			report.KeyMatching, report.KeyMatchError = "failed", matchErr.Error()
			err = errors.Join(err, matchErr)
		} else {
			report.KeyMatching = "checked"
		}
	}

	models := map[string]*CrawlModel{}
	locations := map[string][]WebsiteLocation{}
	for _, section := range crawl.platforms {
		for _, observation := range section.Models {
			groups := websiteGroups(directoryModelGroups(section, observation))
			name := observation.Name
			if models[name] == nil {
				models[name] = &CrawlModel{Name: name, KeyMatches: []CatalogKey{}}
			}
			row := models[name]
			row.Sources = append(row.Sources, CrawlSource{
				ChannelID: section.ChannelID, Channel: section.Channel, Platform: section.Platform, Groups: groups,
				ObservedAt: observation.ObservedAt, MissingFields: websiteMissingPriceFields(observation.Pricing),
				PriceComplete: observation.priceBlocker == "", PriceBlocker: observation.priceBlocker, Pricing: observation.Pricing,
				BaseTokenPricesPerMillion: baseTokenPricesPerMillion(observation.Pricing),
				IgnoredFields:             append([]string{}, usage.BeichenPanelIgnoredFields(observation.Pricing)...),
			})
			locations[name] = append(locations[name], WebsiteLocation{
				Name: name, ChannelID: section.ChannelID, Channel: section.Channel, Platform: section.Platform,
				Groups: groups, channelStatus: section.ChannelStatus,
				HasPrice: observation.priceBlocker == "",
			})
		}
	}
	names := slices.Sorted(maps.Keys(models))
	websiteNames := newCatalogNameMatcher(names, nil)
	for _, name := range names {
		model := models[name]
		_, matches := catalogWebsiteMatches(keys, websiteNames, locations[name])
		model.KeyMatches = append(model.KeyMatches, matches...)
		report.Models = append(report.Models, *model)
	}
	return report, err
}

func (s *Service) readCrawlKeys(ctx context.Context, provider uuid.UUID, platforms []directoryPlatform) ([]discoveryKey, error) {
	if s.db == nil {
		return nil, errors.New("读取已有 Key 目录需要 DATABASE_URL")
	}
	readFailed := errors.New("读取已有北辰 Key 目录或别名失败，请检查数据库连接和迁移状态")
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, readFailed
	}
	defer tx.Rollback()
	if provider == uuid.Nil {
		provider, err = findCrawlProvider(ctx, tx)
		if err != nil {
			return nil, err
		}
	} else if err := checkProvider(ctx, tx, provider); err != nil {
		return nil, errors.New("无法读取指定的北辰供应商，请检查 --provider 和数据库连接")
	}
	keys, err := readDiscoveryKeys(ctx, tx, provider)
	if err != nil {
		return nil, readFailed
	}
	aliases, err := readModelAliases(ctx, tx, provider)
	if err != nil {
		return nil, readFailed
	}
	var view Overview
	if err := readDatasets(ctx, tx, provider, &view); err != nil {
		return nil, readFailed
	}
	rows, err := tx.QueryContext(ctx, `
SELECT m.credential_id, m.model_id::text
FROM provider_model_catalog m
JOIN provider_credentials c ON c.id=m.credential_id AND c.provider_id=m.provider_id
JOIN providers p ON p.id=c.provider_id
WHERE m.provider_id=$1 AND m.status='active' AND c.status='active' AND p.status='active'
  AND m.last_seen_at>=c.updated_at`, provider)
	if err != nil {
		return nil, readFailed
	}
	defer rows.Close()
	var models []Model
	for rows.Next() {
		model := Model{available: true, currentKey: true}
		if err := rows.Scan(&model.CredentialID, &model.Model); err != nil {
			return nil, readFailed
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed
	}
	if err := tx.Commit(); err != nil {
		return nil, readFailed
	}
	indexKeyGroups(view.Datasets, keys, platforms)
	indexDiscoveryKeys(models, keys, aliases)
	return keys, nil
}

func findCrawlProvider(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (uuid.UUID, error) {
	rows, err := db.QueryContext(ctx, "SELECT p.id FROM providers p WHERE p.status='active' AND "+beichenProvider+" LIMIT 2")
	if err != nil {
		return uuid.Nil, errors.New("查询北辰供应商失败，请检查数据库连接")
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, errors.New("读取北辰供应商 ID 失败")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, errors.New("读取北辰供应商 ID 失败")
	}
	if len(ids) != 1 {
		return uuid.Nil, errors.New("数据库中启用的北辰供应商数量不是 1，请用 --provider 指定北辰供应商 UUID")
	}
	return ids[0], nil
}
