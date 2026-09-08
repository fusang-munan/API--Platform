package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

func LoadActiveCatalogEntries(ctx context.Context, client *ent.Client, credentialID uuid.UUID) ([]ProviderEntry, error) {
	rows, err := client.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.CredentialIDEQ(credentialID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
		).
		Order(ent.Asc(providermodelcatalog.FieldModelID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ProviderEntry, 0, len(rows))
	for _, row := range rows {
		entry := ProviderEntry{
			ID:          row.ModelID,
			Object:      stringValue(row.UpstreamObject),
			DisplayName: stringValue(row.DisplayName),
			CreatedAt:   stringValue(row.UpstreamCreatedAt),
			OwnedBy:     stringValue(row.OwnedBy),
			Description: stringValue(row.Description),
			Raw:         row.RawEntry,
		}
		result = append(result, entry)
	}
	return result, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
