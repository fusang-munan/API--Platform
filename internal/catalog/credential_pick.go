package catalog

import (
	"context"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

// FindProbeCatalogCredential uses only a credential that lists the model.
// Beichen website-only catalog entries do not confirm what a Key can access.
func FindProbeCatalogCredential(ctx context.Context, client *ent.Client, providerID uuid.UUID, baseURL, model string) (*ent.ProviderCredential, error) {
	model = strings.TrimSpace(model)
	matches := providermodelcatalog.ModelIDEqualFold(model)
	if strings.HasPrefix(model, "models/") || IsGeminiFamilyModel(model) {
		nativeID := strings.TrimPrefix(model, "models/")
		matches = providermodelcatalog.Or(
			providermodelcatalog.ModelIDEqualFold(nativeID),
			providermodelcatalog.ModelIDEqualFold("models/"+nativeID),
		)
	}
	query := client.ProviderModelCatalog.Query().Where(
		providermodelcatalog.ProviderIDEQ(providerID),
		providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
		matches,
	)
	if IsBeichenBaseURL(baseURL) {
		query.Where(func(selector *entsql.Selector) {
			selector.Where(entsql.EQ(selector.C("catalog_source"), "key"))
		})
	}
	row, err := query.QueryCredential().Where(
		providercredential.ProviderIDEQ(providerID),
		providercredential.StatusEQ(providercredential.StatusActive),
		providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
	).Order(ent.Asc(providercredential.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return row, err
}
