package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CatalogDifference struct {
	CredentialID uuid.UUID  `json:"credential_id"`
	Label        string     `json:"label"`
	State        string     `json:"state"`
	GroupID      *int64     `json:"group_id"`
	CheckedAt    *time.Time `json:"checked_at"`
	WebsiteOnly  []string   `json:"website_only"`
	KeyOnly      []string   `json:"key_only"`
}

type WebsiteOnlyModel struct {
	Model    string          `json:"model"`
	Channels []CredentialRef `json:"channels"`
}

type WebsiteLocation struct {
	Name                 string         `json:"name"`
	ChannelID            int64          `json:"channel_id,omitempty"`
	Channel              string         `json:"channel,omitempty"`
	Platform             string         `json:"platform,omitempty"`
	PriceScope           string         `json:"price_scope"`
	Groups               []WebsiteGroup `json:"groups"`
	ObservedAt           time.Time      `json:"observed_at"`
	HasPrice             bool           `json:"has_price"`
	PriceComplete        bool           `json:"price_complete"`
	PriceConditionsExact map[string]any `json:"price_conditions_exact,omitempty"`
	Stale                bool           `json:"stale,omitempty"`
	channelStatus        string
}

type CatalogKey struct {
	CredentialRef
	SchoolChannel  string `json:"school_channel,omitempty"`
	Replacement    bool   `json:"replacement,omitempty"`
	GroupID        *int64 `json:"group_id"`
	GroupStale     bool   `json:"group_stale,omitempty"`
	GroupDiagnosis string `json:"group_diagnosis,omitempty"`
	State          string `json:"state"`
	UpstreamModel  string `json:"upstream_model,omitempty"`
	Match          string `json:"match,omitempty"`
	Stale          bool   `json:"stale,omitempty"`
}

const keyCatalogCurrentSQL = `(d.last_error IS NULL AND COALESCE(
  d.last_success >= c.updated_at AND d.last_success > statement_timestamp() - interval '30 minutes', false))`

func saveWebsiteCatalog(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision time.Time, quote panelQuote) error {
	if quote.websiteModels == nil {
		return nil
	}
	models, err := json.Marshal(quote.websiteModels)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_website_catalogs (credential_id, credential_revision, group_id, checked_at, models)
VALUES ($1, $2, $3, clock_timestamp(), ARRAY(SELECT jsonb_array_elements_text($4::jsonb)))
ON CONFLICT (credential_id) DO UPDATE SET credential_revision = EXCLUDED.credential_revision,
  group_id = EXCLUDED.group_id, checked_at = EXCLUDED.checked_at, models = EXCLUDED.models`,
		credentialID, revision, quote.groupID, string(models))
	return err
}

type discoveryKey struct {
	CatalogKey
	hint         string
	legacyAt     *time.Time
	legacyGroup  *int64
	legacyNames  []string
	models       map[string]*Model
	staleModels  []string
	nonKeyNames  []string
	matcher      catalogNameMatcher
	groupCurrent bool
	platforms    map[string]bool
	priceError   *string
}

func readDiscoveryKeys(ctx context.Context, tx *sql.Tx, provider uuid.UUID) ([]discoveryKey, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, c.label::text, COALESCE(school.channel,''), COALESCE(c.group_hint,''),
  EXISTS (SELECT 1 FROM beichen_provision_channels prev JOIN provider_credentials old ON old.id=prev.credential_id
    WHERE prev.channel=school.channel AND old.provider_id=c.provider_id AND old.created_at<c.created_at),
  COALESCE(w.group_id,a.group_id), w.group_id, w.checked_at, to_json(w.models),
  `+keyCatalogCurrentSQL+`, d.last_error IS NOT NULL, d.last_success IS NOT NULL,
  to_json(ARRAY(SELECT m.model_id::text FROM provider_model_catalog m
    WHERE m.credential_id=c.id AND m.status='active' AND m.catalog_source<>'key'))
FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = 'catalog'
LEFT JOIN beichen_website_catalogs w ON w.credential_id = c.id AND w.credential_revision = c.updated_at
LEFT JOIN beichen_panel_access a ON a.credential_id=c.id AND a.updated_at=c.updated_at
LEFT JOIN beichen_provision_channels school ON school.credential_id=c.id
WHERE c.provider_id = $1 AND c.status = 'active' AND p.status = 'active'
ORDER BY c.label, c.id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []discoveryKey
	for rows.Next() {
		key := discoveryKey{models: map[string]*Model{}}
		var website, nonKeyModels []byte
		var scanned, failed, previouslyScanned bool
		if err := rows.Scan(&key.CredentialID, &key.Label, &key.SchoolChannel, &key.hint, &key.Replacement,
			&key.GroupID, &key.legacyGroup, &key.legacyAt, &website, &scanned, &failed, &previouslyScanned, &nonKeyModels); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(nonKeyModels, &key.nonKeyNames); err != nil {
			return nil, err
		}
		switch {
		case scanned:
			key.State = "missing"
		case failed:
			key.State, key.Stale = "failed", true
		default:
			key.State, key.Stale = "pending", previouslyScanned
		}
		if key.legacyAt != nil {
			at := key.legacyAt.UTC()
			key.legacyAt = &at
			if err := json.Unmarshal(website, &key.legacyNames); err != nil {
				return nil, err
			}
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func readCatalogDifferences(ctx context.Context, tx *sql.Tx, provider uuid.UUID, view *Overview) error {
	status, platforms, err := readWebsiteDirectory(ctx, tx, provider)
	if err != nil {
		return err
	}
	keys, err := readDiscoveryKeys(ctx, tx, provider)
	if err != nil {
		return err
	}
	aliases, err := readModelAliases(ctx, tx, provider)
	if err != nil {
		return err
	}
	view.WebsiteDirectory = status
	indexKeyGroups(view.Datasets, keys, platforms)
	indexDiscoveryKeys(view.Models, keys, aliases)
	web := map[string][]WebsiteLocation{}
	stale := status.State == "stale"
	for _, section := range platforms {
		for _, model := range section.Models {
			name := strings.ToLower(model.Name)
			location := WebsiteLocation{
				Name: model.Name, ChannelID: section.ChannelID, Channel: section.Channel, Platform: section.Platform,
				PriceScope: "platform",
				Groups:     websiteGroups(directoryModelGroups(section, model)),
				ObservedAt: model.ObservedAt, HasPrice: hasPanelPrice(model.Pricing),
				PriceComplete: usage.BeichenDirectoryQuoteBlocker(model.Pricing) == "",
				Stale:         stale, channelStatus: section.ChannelStatus,
			}
			if model.GroupID != 0 {
				location.PriceScope = "group"
			}
			if location.HasPrice {
				decoder := json.NewDecoder(bytes.NewReader(usage.BeichenPanelDisplayTerms(model.Pricing)))
				decoder.UseNumber()
				if err := decoder.Decode(&location.PriceConditionsExact); err != nil {
					return err
				}
				stringifyPriceNumbers(location.PriceConditionsExact)
			}
			web[name] = append(web[name], location)
		}
	}
	view.CatalogDifferences = make([]CatalogDifference, 0, len(keys))
	view.WebsiteOnlyStatus = "unverified"
	allKeysScanned, anyWebsite := true, len(platforms) != 0
	for _, key := range keys {
		names, checked := websiteGroupCatalog(platforms, key.GroupID)
		if len(platforms) == 0 && key.legacyAt != nil && key.GroupID != nil && *key.legacyGroup == *key.GroupID {
			anyWebsite = true
			names, checked = key.legacyNames, key.legacyAt
			for _, name := range names {
				lower := strings.ToLower(name)
				web[lower] = append(web[lower], WebsiteLocation{
					Name: name, PriceScope: "group", Groups: []WebsiteGroup{{ID: *key.GroupID}}, ObservedAt: *checked,
				})
			}
		}
		diff := CatalogDifference{CredentialID: key.CredentialID, Label: key.Label, GroupID: key.GroupID,
			CheckedAt: checked, State: "unverified", WebsiteOnly: []string{}, KeyOnly: []string{}}
		scanned := key.State == "missing"
		allKeysScanned = allKeysScanned && scanned
		if scanned && names != nil {
			diff.State = "compared"
			if stale || key.GroupStale {
				diff.State = "stale"
			}
			diff.WebsiteOnly, diff.KeyOnly = diffModelNames(names, key)
		}
		view.CatalogDifferences = append(view.CatalogDifferences, diff)
	}
	if stale {
		view.WebsiteOnlyStatus = "stale"
	} else if anyWebsite && allKeysScanned {
		view.WebsiteOnlyStatus = "compared"
	}
	mergeCatalogDiscovery(view, keys, web)
	return nil
}

func indexKeyGroups(datasets []Dataset, keys []discoveryKey, platforms []directoryPlatform) {
	index := make(map[uuid.UUID]*discoveryKey, len(keys))
	for i := range keys {
		index[keys[i].CredentialID] = &keys[i]
	}
	for _, dataset := range datasets {
		key := index[dataset.CredentialID]
		if key == nil {
			continue
		}
		switch dataset.Kind {
		case "prices":
			key.priceError = dataset.LastError
		case "billing":
			// The saved panel mapping is display evidence for legacy rows. A
			// successful live observation supersedes it even if the group is unknown.
			if dataset.ObservedAt != nil || dataset.GroupID != nil || dataset.GroupStatus != "" {
				key.GroupID = dataset.GroupID
			}
			key.GroupStale = dataset.BillingState == "stale" &&
				(dataset.GroupStatus == "" || dataset.GroupStatus == "assigned")
			key.groupCurrent = dataset.BillingState == "current" && dataset.GroupID != nil
			if !key.groupCurrent {
				continue
			}
			key.platforms = map[string]bool{}
			for _, section := range platforms {
				if namesGroup(section.Groups, *key.GroupID) {
					key.platforms[strings.ToLower(section.Platform)] = true
				}
			}
		}
	}
}

func websiteGroups(groups []directoryGroup) []WebsiteGroup {
	items := make([]WebsiteGroup, 0, len(groups))
	for _, group := range groups {
		items = append(items, group.WebsiteGroup)
	}
	return items
}

func indexDiscoveryKeys(models []Model, keys []discoveryKey, aliases []ModelAlias) {
	index := make(map[uuid.UUID]*discoveryKey, len(keys))
	for i := range keys {
		index[keys[i].CredentialID] = &keys[i]
	}
	for i := range models {
		row := &models[i]
		key := index[row.CredentialID]
		if key == nil || !row.available || !row.currentKey {
			continue
		}
		// The crawler reads only names from the same database snapshot.
		if row.CatalogSource == "" && key.nonKeyNames != nil && !slices.Contains(key.nonKeyNames, row.Model) {
			row.CatalogSource = "key"
		}
		if row.CatalogSource != "key" {
			continue
		}
		if key.State == "missing" {
			key.models[row.Model] = row
		} else if key.Stale {
			key.staleModels = append(key.staleModels, row.Model)
		}
	}
	for i := range keys {
		key := &keys[i]
		// A legacy website snapshot may have recorded catalog success.
		if key.State == "missing" && len(key.nonKeyNames) != 0 && len(key.models) == 0 {
			key.State, key.Stale = "pending", true
		}
		mapping := map[string]string{}
		// readModelAliases orders provider-wide defaults before Key overrides.
		for _, alias := range aliases {
			if alias.CredentialID == nil || *alias.CredentialID == key.CredentialID {
				mapping[strings.ToLower(alias.UpstreamModel)] = strings.ToLower(alias.SchoolModel)
			}
		}
		key.matcher = newCatalogNameMatcher(slices.Collect(maps.Keys(key.models)), mapping)
	}
}

func websiteGroupCatalog(platforms []directoryPlatform, groupID *int64) ([]string, *time.Time) {
	if groupID == nil {
		return nil, nil
	}
	var names []string
	var checked *time.Time
	for _, section := range platforms {
		if namesGroup(section.Groups, *groupID) {
			if names == nil {
				names = []string{}
			}
			for _, model := range section.Models {
				if namesGroup(directoryModelGroups(section, model), *groupID) {
					names = append(names, model.Name)
				}
			}
			if checked == nil || section.ObservedAt.After(*checked) {
				checked = &section.ObservedAt
			}
		}
	}
	return names, checked
}

func namesGroup(groups []directoryGroup, id int64) bool {
	return slices.ContainsFunc(groups, func(group directoryGroup) bool { return group.ID == id })
}

func diffModelNames(website []string, key discoveryKey) ([]string, []string) {
	websiteNames := newCatalogNameMatcher(website, nil)
	onlyWebsite, onlyKey := []string{}, []string{}
	matched, seen := map[string]bool{}, map[string]bool{}
	for _, name := range website {
		match := matchWebsiteModel(name, key.matcher, websiteNames)
		if match.name != "" {
			matched[match.name] = true
		} else if lower := strings.ToLower(name); !seen[lower] {
			onlyWebsite = append(onlyWebsite, name)
			seen[lower] = true
		}
	}
	for model := range key.models {
		if !matched[model] {
			onlyKey = append(onlyKey, model)
		}
	}
	slices.Sort(onlyWebsite)
	slices.Sort(onlyKey)
	return onlyWebsite, onlyKey
}
