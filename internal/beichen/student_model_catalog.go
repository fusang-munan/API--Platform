package beichen

import (
	"context"
	"database/sql"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

// ReadStudentModelCatalog checks the current scan and group evidence for the
// channels associated with these models.
func ReadStudentModelCatalog(ctx context.Context, db *sql.DB, models []string) (map[string][]catalog.StudentChannelCatalog, error) {
	result := make(map[string][]catalog.StudentChannelCatalog)
	if len(models) == 0 {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `
WITH student_model_channels AS (
SELECT DISTINCT m.model_id::text AS model_id, p.id AS provider_id, p.base_url,
       c.id AS credential_id, btrim(b.upstream_model_name) AS upstream_model
FROM models m
JOIN model_bindings b ON b.model_id=m.id
JOIN providers p ON p.id=b.provider_id AND p.status='active'
JOIN provider_credentials c ON c.provider_id=p.id AND c.status='active'
  AND (c.id=b.credential_id OR b.credential_id IS NULL)
WHERE m.model_id IN (SELECT name::citext FROM unnest($1::text[]) AS names(name))
), beichen_key_evidence AS (
  `+catalog.BeichenKeyEvidenceSQL+`
  WHERE c.id IN (SELECT credential_id FROM student_model_channels)
)
SELECT b.model_id, b.base_url, b.credential_id, b.upstream_model,
       `+catalog.BeichenCatalogCurrentSQL+` OR observed.listed, observed.listed
FROM student_model_channels b
JOIN beichen_key_evidence e ON e.credential_id=b.credential_id
LEFT JOIN provider_model_catalog k ON k.credential_id=b.credential_id AND k.provider_id=b.provider_id
  AND k.model_id=b.upstream_model::citext AND k.status='active'
CROSS JOIN LATERAL (SELECT COALESCE(`+catalog.BeichenKeyListsModelSQL+`,false) AS listed) observed`, models)
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
