package beichen

import (
	"context"
	"database/sql"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

// ReadStudentModelCatalog uses the same snapshot freshness check as the
// teacher directory. It reads only the channels associated with these models.
func ReadStudentModelCatalog(ctx context.Context, db *sql.DB, models []string) (map[string][]catalog.StudentChannelCatalog, error) {
	result := make(map[string][]catalog.StudentChannelCatalog)
	if len(models) == 0 {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT m.model_id::text, p.base_url, c.id, btrim(b.upstream_model_name),
       `+keyCatalogCurrentSQL+`,
       EXISTS (SELECT 1 FROM provider_model_catalog k
          WHERE k.credential_id=c.id AND k.provider_id=p.id
            AND k.model_id=btrim(b.upstream_model_name)::citext
            AND k.status='active' AND k.catalog_source='key' AND k.last_seen_at>=c.updated_at)
FROM models m
JOIN model_bindings b ON b.model_id=m.id
JOIN providers p ON p.id=b.provider_id AND p.status='active'
JOIN provider_credentials c ON c.provider_id=p.id AND c.status='active'
  AND (c.id=b.credential_id OR b.credential_id IS NULL)
LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='catalog'
WHERE m.model_id IN (SELECT name::citext FROM unnest($1::text[]) AS names(name))`, models)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var modelID, baseURL string
		var channel catalog.StudentChannelCatalog
		if err := rows.Scan(&modelID, &baseURL, &channel.CredentialID, &channel.UpstreamModel, &channel.Current, &channel.Listed); err != nil {
			return nil, err
		}
		if catalog.IsBeichenBaseURL(baseURL) {
			result[modelID] = append(result[modelID], channel)
		}
	}
	return result, rows.Err()
}
