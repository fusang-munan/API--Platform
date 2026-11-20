package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
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

func saveWebsiteCatalog(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision time.Time, quote panelQuote) error {
	if quote.websiteModels == nil {
		_, err := tx.ExecContext(ctx, `DELETE FROM beichen_website_catalogs WHERE credential_id = $1`, credentialID)
		return err
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

func readCatalogDifferences(ctx context.Context, tx *sql.Tx, provider uuid.UUID, view *Overview) error {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, c.label::text, w.group_id, w.checked_at, to_json(w.models)
FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = 'catalog'
LEFT JOIN beichen_website_catalogs w ON w.credential_id = c.id AND w.credential_revision = c.updated_at
  AND p.status = 'active' AND d.last_error IS NULL AND w.checked_at >= d.last_success
  AND NOT EXISTS (
    SELECT 1 FROM provider_model_catalog m WHERE m.credential_id = c.id AND m.synced_at > w.checked_at
  )
WHERE c.provider_id = $1 AND c.status = 'active'
ORDER BY c.label, c.id`, provider)
	if err != nil {
		return err
	}
	defer rows.Close()

	keyModels := make(map[uuid.UUID][]string)
	for _, model := range view.Models {
		keyModels[model.CredentialID] = append(keyModels[model.CredentialID], model.Model)
	}
	view.CatalogDifferences = []CatalogDifference{}
	for rows.Next() {
		diff := CatalogDifference{State: "unverified", WebsiteOnly: []string{}, KeyOnly: []string{}}
		var website []byte
		if err := rows.Scan(&diff.CredentialID, &diff.Label, &diff.GroupID, &diff.CheckedAt, &website); err != nil {
			return err
		}
		if diff.CheckedAt != nil {
			var websiteModels []string
			if err := json.Unmarshal(website, &websiteModels); err != nil {
				return err
			}
			at := diff.CheckedAt.UTC()
			diff.State, diff.CheckedAt = "compared", &at
			diff.WebsiteOnly, diff.KeyOnly = diffModelNames(websiteModels, keyModels[diff.CredentialID])
		}
		view.CatalogDifferences = append(view.CatalogDifferences, diff)
	}
	return rows.Err()
}

func diffModelNames(website, key []string) ([]string, []string) {
	index := func(names []string) map[string]string {
		result := make(map[string]string, len(names))
		for _, name := range names {
			lower := strings.ToLower(name)
			if old, ok := result[lower]; !ok || name < old {
				result[lower] = name
			}
		}
		return result
	}
	webNames, keyNames := index(website), index(key)
	only := func(left, right map[string]string) []string {
		names := []string{}
		for lower, model := range left {
			if _, ok := right[lower]; !ok {
				names = append(names, model)
			}
		}
		slices.Sort(names)
		return names
	}
	return only(webNames, keyNames), only(keyNames, webNames)
}
