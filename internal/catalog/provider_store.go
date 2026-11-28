package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ProviderCatalogSyncer struct {
	DB *sql.DB
}

type ProviderCatalogSyncResult struct {
	ProviderID   uuid.UUID
	CredentialID uuid.UUID
	Synced       int
}

func (s *ProviderCatalogSyncer) UpsertCredentialEntries(ctx context.Context, providerID, credentialID uuid.UUID, entries []ProviderEntry, syncedAt time.Time, revision *time.Time) (int, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("catalog database is unavailable")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if revision != nil {
		var baseURL string
		checkErr := tx.QueryRowContext(ctx, `
SELECT p.base_url FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE c.id = $1 AND p.id = $2 AND c.updated_at = $3 AND c.status = 'active' AND p.status = 'active'
FOR SHARE OF p FOR NO KEY UPDATE OF c`, credentialID, providerID, *revision).Scan(&baseURL)
		if checkErr != nil || !IsBeichenBaseURL(baseURL) {
			return 0, fmt.Errorf("北辰目录写入时无法确认供应商或凭据版本")
		}
		// Callers without completeness evidence may safely add observed rows.
		count, err = upsertCredentialEntries(ctx, tx, providerID, credentialID, entries, syncedAt)
	} else {
		count, err = ReplaceCredentialEntries(ctx, tx, providerID, credentialID, entries, syncedAt)
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// ReplaceCredentialEntries applies a complete per-key snapshot in the caller's transaction.
func ReplaceCredentialEntries(ctx context.Context, tx *sql.Tx, providerID, credentialID uuid.UUID, entries []ProviderEntry, syncedAt time.Time) (int, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE provider_model_catalog SET status='inactive',synced_at=$1,updated_at=$1
WHERE credential_id=$2 AND provider_id=$3`, syncedAt, credentialID, providerID); err != nil {
		return 0, err
	}
	return upsertCredentialEntries(ctx, tx, providerID, credentialID, entries, syncedAt)
}

func upsertCredentialEntries(ctx context.Context, tx *sql.Tx, providerID, credentialID uuid.UUID, entries []ProviderEntry, syncedAt time.Time) (int, error) {
	count := 0
	for _, entry := range entries {
		raw, _ := json.Marshal(entry.Raw)
		if entry.Raw == nil {
			raw = []byte("{}")
		}
		protocols, err := json.Marshal(RegisteredProtocols(entry))
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO provider_model_catalog(
  id, provider_id, credential_id, model_id, display_name, description, owned_by, upstream_object, upstream_created_at,
  raw_entry, catalog_source, status, last_seen_at, synced_at, created_at, updated_at, supported_protocols
) VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10::jsonb,'key','active',$11,$11,$11,$11,
  ARRAY(SELECT jsonb_array_elements_text($12::jsonb)))
ON CONFLICT(credential_id, model_id) DO UPDATE SET
  display_name=EXCLUDED.display_name,
  description=EXCLUDED.description,
  owned_by=EXCLUDED.owned_by,
  upstream_object=EXCLUDED.upstream_object,
  upstream_created_at=EXCLUDED.upstream_created_at,
  raw_entry=EXCLUDED.raw_entry,
  catalog_source=EXCLUDED.catalog_source,
  supported_protocols=EXCLUDED.supported_protocols || ARRAY(
    SELECT probe.protocol FROM upstream_probes probe
    JOIN provider_credentials c ON c.id = probe.credential_id AND c.provider_id = probe.provider_id
    WHERE probe.provider_id = $2 AND probe.credential_id = $3 AND probe.model = EXCLUDED.model_id::text
      AND probe.checked_at >= c.updated_at AND probe.error_code IS NULL
      AND NOT (probe.protocol = ANY(EXCLUDED.supported_protocols))
    ORDER BY probe.protocol),
  status='active',
  last_seen_at=$11,
  synced_at=$11,
  updated_at=$11`,
			uuid.Must(uuid.NewV7()), providerID, credentialID, entry.ID,
			entry.DisplayName, entry.Description, entry.OwnedBy, entry.Object, entry.CreatedAt,
			string(raw), syncedAt, string(protocols),
		)
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
