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
	if revision != nil {
		var baseURL string
		err := tx.QueryRowContext(ctx, `
SELECT p.base_url FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE c.id = $1 AND p.id = $2 AND c.updated_at = $3 AND c.status = 'active' AND p.status = 'active'
FOR SHARE OF p FOR NO KEY UPDATE OF c`, credentialID, providerID, *revision).Scan(&baseURL)
		if err != nil || !IsBeichenBaseURL(baseURL) {
			return 0, fmt.Errorf("北辰目录写入时无法确认供应商或凭据版本")
		}
	}
	count, err := ReplaceCredentialEntries(ctx, tx, providerID, credentialID, entries, syncedAt)
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
	if _, err := tx.ExecContext(ctx, `UPDATE provider_model_catalog SET status='inactive',synced_at=$1,updated_at=$1 WHERE credential_id=$2`, syncedAt, credentialID); err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		raw, _ := json.Marshal(entry.Raw)
		if entry.Raw == nil {
			raw = []byte("{}")
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO provider_model_catalog(
  id, provider_id, credential_id, model_id, display_name, description, owned_by, upstream_object, upstream_created_at,
  raw_entry, status, last_seen_at, synced_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10::jsonb,'active',$11,$11,$11,$11)
ON CONFLICT(credential_id, model_id) DO UPDATE SET
  display_name=EXCLUDED.display_name,
  description=EXCLUDED.description,
  owned_by=EXCLUDED.owned_by,
  upstream_object=EXCLUDED.upstream_object,
  upstream_created_at=EXCLUDED.upstream_created_at,
  raw_entry=EXCLUDED.raw_entry,
  status='active',
  last_seen_at=$11,
  synced_at=$11,
  updated_at=$11`,
			uuid.Must(uuid.NewV7()), providerID, credentialID, entry.ID,
			entry.DisplayName, entry.Description, entry.OwnedBy, entry.Object, entry.CreatedAt,
			string(raw), syncedAt,
		)
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
