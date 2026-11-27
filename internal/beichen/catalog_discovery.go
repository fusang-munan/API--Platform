package beichen

import (
	"context"
	"database/sql"
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
		if model.published() {
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
			if i, ok := rows[strings.ToLower(name)]; ok {
				setCatalogKey(&view.ModelCatalog[i], key.listed(name, ""))
			}
		}
		for _, name := range key.staleModels {
			if i, ok := rows[strings.ToLower(name)]; ok {
				setCatalogKey(&view.ModelCatalog[i], key.listed(name, ""))
			}
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
		row.CurrentKeyCount, row.StaleKeyCount = 0, 0
		protocols := map[string]bool{}
		for _, key := range row.CatalogKeys {
			switch {
			case key.State != "listed":
				continue
			case key.Stale:
				row.StaleKeyCount++
			default:
				row.CurrentKeyCount++
				for _, protocol := range byKey[key.CredentialID][key.UpstreamModel].protocols {
					protocols[protocol] = true
				}
			}
		}
		row.KeyCount = row.CurrentKeyCount
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
		case row.published():
			row.CatalogState = "published_unavailable"
			if len(row.WebsiteLocations) != 0 {
				row.Source = "website"
			}
			if row.BindingIssue == nil {
				row.BindingIssue = &CatalogBlocker{"catalog_missing", "当前 Key 目录里已找不到校内模型，请重扫目录并核查供货渠道。"}
				if row.StaleKeyCount != 0 {
					row.BindingIssue = &CatalogBlocker{"catalog_stale", "该模型的 Key 目录记录待刷新，请重扫渠道目录。"}
				}
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

func (row CatalogModel) published() bool {
	return row.SchoolStatus == "active" || row.SchoolModel != "" || row.BindingIssue != nil
}

// Use stored channel relationships even when a scan stopped listing the model.
// Key rotation can leave an enabled binding on an old Key so the teacher
// can diagnose and move it. Only disabled bindings count as retired evidence.
func markRetiredCatalogModels(ctx context.Context, tx *sql.Tx, provider uuid.UUID, models []CatalogModel) error {
	if len(models) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
WITH north_bindings AS (
  SELECT lower(btrim(b.upstream_model_name)) AS model, b.model_id,
    b.status = 'inactive' AND (p.status <> 'active' OR COALESCE(c.status <> 'active', false)) AS retired
  FROM model_bindings b
  JOIN providers p ON p.id = b.provider_id
  LEFT JOIN provider_credentials c ON c.id = b.credential_id AND c.provider_id = b.provider_id
  WHERE b.provider_id = $1
), channel_links AS (
  SELECT lower(k.model_id::text) AS model, p.status <> 'active' OR c.status <> 'active' AS retired
  FROM provider_model_catalog k
  JOIN providers p ON p.id = k.provider_id
  JOIN provider_credentials c ON c.id = k.credential_id AND c.provider_id = k.provider_id
  WHERE k.provider_id = $1
  UNION ALL
  SELECT model, retired FROM north_bindings
), historical_models AS (
  SELECT model FROM channel_links GROUP BY model HAVING bool_and(retired)
), school_models AS (
  SELECT h.model, m.id AS model_id
  FROM historical_models h JOIN models m ON m.model_id = h.model::citext
  UNION
  SELECT h.model, b.model_id FROM historical_models h JOIN north_bindings b USING (model)
)
SELECT h.model FROM historical_models h
WHERE NOT EXISTS (
  SELECT 1 FROM school_models s JOIN model_bindings b ON b.model_id = s.model_id
  JOIN providers p ON p.id = b.provider_id
  WHERE s.model = h.model AND b.status = 'active' AND p.status = 'active'
)`, provider)
	if err != nil {
		return err
	}
	defer rows.Close()
	historical := make(map[string]bool)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return err
		}
		historical[model] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range models {
		row := &models[i]
		// Keep alias matches and the existing diagnosis for a replacement Key
		// associated with this model during catalog comparison.
		hasActiveKey := slices.ContainsFunc(row.CatalogKeys, func(key CatalogKey) bool {
			return key.State == "listed" || key.Replacement
		})
		if row.CatalogState != "website_only" && historical[strings.ToLower(row.Model)] && !hasActiveKey {
			row.Retired = true
			row.BindingIssue = nil
		}
	}
	return nil
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
