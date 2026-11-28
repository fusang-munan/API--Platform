package catalog

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
)

func LoadActiveCatalogEntries(ctx context.Context, db *sql.DB, credentialID uuid.UUID) ([]ProviderEntry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT model_id::text, COALESCE(upstream_object, ''), COALESCE(display_name, ''),
       COALESCE(upstream_created_at, ''), COALESCE(owned_by, ''), COALESCE(description, ''),
       raw_entry, to_json(supported_protocols)
FROM provider_model_catalog
WHERE credential_id=$1 AND status='active'
ORDER BY model_id`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ProviderEntry, 0)
	for rows.Next() {
		var entry ProviderEntry
		var raw, protocols []byte
		if err := rows.Scan(&entry.ID, &entry.Object, &entry.DisplayName, &entry.CreatedAt,
			&entry.OwnedBy, &entry.Description, &raw, &protocols); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &entry.Raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(protocols, &entry.SupportedProtocols); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
