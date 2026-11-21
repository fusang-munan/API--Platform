package beichen

import (
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// The merged directory is a teacher read model. Raw Key IDs and each row's
// existing publication plan remain the authority for routing and mutations.
func mergeCatalogDiscovery(view *Overview, keys []discoveryKey, web map[string][]WebsiteLocation) {
	rows := make(map[string]int, len(view.ModelCatalog)+len(web))
	var published []string
	for i, model := range view.ModelCatalog {
		rows[strings.ToLower(model.Model)] = i
		if model.SchoolStatus == "active" || model.BindingIssue != nil {
			published = append(published, model.Model)
		}
	}
	publishedNames := newCatalogNameMatcher(published, nil)
	names := make([]string, 0, len(web))
	for _, locations := range web {
		names = append(names, locations[0].Name)
	}
	websiteNames := newCatalogNameMatcher(names, nil)
	for _, key := range keys {
		for name := range key.models {
			row := &view.ModelCatalog[rows[strings.ToLower(name)]]
			setCatalogKey(row, key.listed(name, ""))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(web)) {
		locations := web[name]
		targets, evidence := catalogWebsiteMatches(keys, websiteNames, locations)
		if published := matchWebsiteModel(locations[0].Name, publishedNames, websiteNames).name; published != "" {
			targets[strings.ToLower(published)] = true
		}
		if _, exists := rows[name]; exists || len(targets) == 0 {
			targets[name] = true
		}
		for target := range targets {
			i, exists := rows[target]
			if !exists {
				i = len(view.ModelCatalog)
				rows[target] = i
				view.ModelCatalog = append(view.ModelCatalog, CatalogModel{
					Model: locations[0].Name, SchoolStatus: "not_configured", Activation: "blocked",
					CredentialIDs: []uuid.UUID{}, PreferredRoutes: []PreferredRoute{}, Protocols: []string{},
					PriceState: "missing", ProbeState: "unprobed",
					Blockers: []CatalogBlocker{{Code: "website_only", Message: "官网已列出此模型，请核对分组和当前渠道 Key 的目录。"}},
				})
			}
			row := &view.ModelCatalog[i]
			row.WebsiteLocations = append(row.WebsiteLocations, locations...)
			for _, item := range evidence {
				setCatalogKey(row, item)
			}
		}
	}

	byKey := make(map[uuid.UUID]map[string]*Model, len(keys))
	for _, key := range keys {
		byKey[key.CredentialID] = key.models
	}
	view.WebsiteOnlyModels = []WebsiteOnlyModel{}
	for i := range view.ModelCatalog {
		row := &view.ModelCatalog[i]
		row.KeyCount = 0
		protocols := map[string]bool{}
		for _, key := range row.CatalogKeys {
			if key.State == "listed" {
				row.KeyCount++
				for _, protocol := range byKey[key.CredentialID][key.UpstreamModel].protocols {
					protocols[protocol] = true
				}
			}
		}
		row.Protocols = slices.Sorted(maps.Keys(protocols))
		if row.Protocols == nil {
			row.Protocols = []string{}
		}
		switch {
		case row.KeyCount > 0:
			row.Source, row.CatalogState = "key", "key_listed"
			if len(row.WebsiteLocations) != 0 {
				row.Source = "both"
			}
		case len(row.WebsiteLocations) != 0:
			row.Source, row.CatalogState = "website", "website_only"
			only := WebsiteOnlyModel{Model: row.Model, Channels: []CredentialRef{}}
			for _, key := range row.CatalogKeys {
				only.Channels = append(only.Channels, key.CredentialRef)
			}
			view.WebsiteOnlyModels = append(view.WebsiteOnlyModels, only)
		default:
			row.CatalogState = "catalog_missing"
		}
	}
	slices.SortFunc(view.ModelCatalog, func(a, b CatalogModel) int {
		return strings.Compare(strings.ToLower(a.Model), strings.ToLower(b.Model))
	})
	slices.SortFunc(view.WebsiteOnlyModels, func(a, b WebsiteOnlyModel) int {
		return strings.Compare(strings.ToLower(a.Model), strings.ToLower(b.Model))
	})
}

func catalogWebsiteMatches(keys []discoveryKey, websiteNames catalogNameMatcher, locations []WebsiteLocation) (map[string]bool, []CatalogKey) {
	targets := map[string]bool{}
	var evidence []CatalogKey
	for _, key := range keys {
		match := matchWebsiteModel(locations[0].Name, key.matcher, websiteNames)
		if match.name != "" {
			targets[strings.ToLower(match.name)] = true
			item := key.listed(match.name, match.kind)
			if row := key.models[match.name]; row != nil && row.PriceStatus != "confirmed" && row.PriceStatus != "conditional" && row.PriceStatus != "derived" {
				item.GroupDiagnosis = key.diagnoseGroup(locations, false)
			}
			evidence = append(evidence, item)
		} else if key.couldCover(locations) || match.kind == "ambiguous" {
			item := key.CatalogKey
			if item.State == "missing" && match.kind == "ambiguous" {
				item.State = "ambiguous"
			}
			if item.State == "missing" {
				item.GroupDiagnosis = key.diagnoseGroup(locations, true)
			}
			evidence = append(evidence, item)
		}
	}
	return targets, evidence
}

func (key discoveryKey) diagnoseGroup(locations []WebsiteLocation, missingModel bool) string {
	if !key.groupCurrent {
		return ""
	}
	hasOtherGroup, hasCurrentGroup, currentPrice := false, false, false
	for _, location := range locations {
		if location.Stale || location.channelStatus != "" && location.channelStatus != "active" {
			continue
		}
		if len(key.platforms) != 0 && !key.platforms[strings.ToLower(location.Platform)] {
			continue
		}
		for _, group := range location.Groups {
			if group.ID == 0 || group.Status != "" && group.Status != "active" {
				continue
			}
			if group.ID == *key.GroupID {
				hasCurrentGroup = true
				currentPrice = currentPrice || location.HasPrice
			} else {
				hasOtherGroup = hasOtherGroup || (missingModel || location.HasPrice)
			}
		}
	}
	if missingModel && hasOtherGroup && !hasCurrentGroup {
		return "model_outside_key_group"
	}
	if !missingModel && hasCurrentGroup && key.priceError != nil && *key.priceError == errPanelRulesConflict.Error() {
		return "group_rate_mismatch"
	}
	if !missingModel && hasOtherGroup && !currentPrice {
		return "price_outside_key_group"
	}
	return ""
}

func (key discoveryKey) listed(name, kind string) CatalogKey {
	item := key.CatalogKey
	item.State, item.UpstreamModel, item.Match = "listed", name, kind
	return item
}

func setCatalogKey(row *CatalogModel, key CatalogKey) {
	i := slices.IndexFunc(row.CatalogKeys, func(existing CatalogKey) bool {
		return existing.CredentialID == key.CredentialID
	})
	if i == -1 {
		row.CatalogKeys = append(row.CatalogKeys, key)
	} else if key.State == "listed" && (row.CatalogKeys[i].State != "listed" ||
		row.CatalogKeys[i].Match == "" && row.CatalogKeys[i].UpstreamModel == key.UpstreamModel) {
		row.CatalogKeys[i] = key
	}
}

func (key discoveryKey) couldCover(locations []WebsiteLocation) bool {
	labelMatches := func(name string) bool {
		name = strings.TrimSpace(name)
		return name != "" && (strings.EqualFold(name, key.Label) || strings.EqualFold(name, key.SchoolChannel) ||
			strings.EqualFold(name, strings.TrimSpace(key.hint)))
	}
	channel := key.SchoolChannel
	if channel == "" {
		channel = key.Label
	}
	for _, location := range locations {
		if location.channelStatus != "" && location.channelStatus != "active" {
			continue
		}
		for _, group := range location.Groups {
			if group.Status != "" && group.Status != "active" {
				continue
			}
			if key.GroupID != nil && group.ID == *key.GroupID || labelMatches(group.Name) {
				return true
			}
		}
		if labelMatches(location.Channel) {
			return true
		}
		platform := strings.ToLower(location.Platform)
		if key.platforms[platform] {
			return true
		}
		switch strings.ToLower(channel) {
		case "cc1":
			if platform == "anthropic" || platform == "claude" {
				return true
			}
		case "g3":
			if platform == "openai" {
				return true
			}
		case "gemini":
			if platform == "gemini" || platform == "google" {
				return true
			}
		}
	}
	return false
}
