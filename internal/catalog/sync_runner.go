package catalog

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
)

type ProviderSyncRunner struct {
	DB      *sql.DB
	Ent     *ent.Client
	Decrypt func([]byte) (string, error)
	Now     func() time.Time
}

type activeCredentialRow struct {
	ID                   uuid.UUID
	ProviderID           uuid.UUID
	BaseURL              string
	Label                string
	GroupHint            sql.NullString
	CredentialCiphertext []byte
	UpdatedAt            time.Time
}

func (r *ProviderSyncRunner) SyncActiveProviders(ctx context.Context) (int, error) {
	if r.DB == nil || r.Ent == nil || r.Decrypt == nil {
		return 0, nil
	}
	now := r.now().UTC()
	rows, err := r.DB.QueryContext(ctx, `
SELECT pc.id, pc.provider_id, p.base_url, pc.label, pc.group_hint, pc.credential_ciphertext, pc.updated_at
FROM provider_credentials pc
JOIN providers p ON p.id = pc.provider_id
WHERE p.status='active' AND pc.status='active'
ORDER BY lower(p.name), p.name, lower(pc.label), pc.label`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	synced := 0
	syncer := &ProviderCatalogSyncer{DB: r.DB}
	for rows.Next() {
		var row activeCredentialRow
		if err := rows.Scan(&row.ID, &row.ProviderID, &row.BaseURL, &row.Label, &row.GroupHint, &row.CredentialCiphertext, &row.UpdatedAt); err != nil {
			return synced, err
		}
		credential, err := r.Decrypt(row.CredentialCiphertext)
		if err != nil {
			continue
		}
		entries, err := FetchProviderModels(ctx, row.BaseURL, credential, nil)
		if err != nil {
			continue
		}
		var groupHint *string
		if row.GroupHint.Valid {
			hint := row.GroupHint.String
			groupHint = &hint
		}
		entries = FilterProviderEntriesByCredential(row.Label, groupHint, row.BaseURL, entries)
		var revision *time.Time
		if IsBeichenBaseURL(row.BaseURL) {
			revision = &row.UpdatedAt
		}
		if _, err := syncer.UpsertCredentialEntries(ctx, row.ProviderID, row.ID, entries, now, revision); err != nil {
			return synced, err
		}
		synced++
	}
	return synced, rows.Err()
}

func (r *ProviderSyncRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func ActiveCredentials(ctx context.Context, client *ent.Client, providerID *uuid.UUID) ([]*ent.ProviderCredential, error) {
	query := client.ProviderCredential.Query().
		Where(providercredential.StatusEQ(providercredential.StatusActive)).
		Where(providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive))).
		WithProvider().
		Order(
			ent.Asc(providercredential.FieldLabel),
			ent.Asc(providercredential.FieldID),
		)
	if providerID != nil {
		query = query.Where(providercredential.ProviderIDEQ(*providerID))
	}
	return query.All(ctx)
}
