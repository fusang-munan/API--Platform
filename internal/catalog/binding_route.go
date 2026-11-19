package catalog

// Route layer: bindings mirror upstream catalog ownership.
//
// Catalog sync owns provider_model_catalog. Materialize creates platform models and bindings.
// ReconcileProviderBindingRoutes keeps binding status aligned with whether a key currently
// owns the upstream model. It never changes model governance status (pending/active/inactive);
// that is teacher-controlled except during Reset Catalog.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

func normalizeUpstreamModelID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// BindingRouteReady reports whether a binding can reach upstream through an active provider key catalog entry.
func BindingRouteReady(ctx context.Context, client *ent.Client, binding *ent.ModelBinding) (bool, error) {
	if binding == nil || binding.Status != modelbinding.StatusActive {
		return false, nil
	}
	providerRow, err := client.Provider.Get(ctx, binding.ProviderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if providerRow.Status != provider.StatusActive {
		return false, nil
	}
	upstream := normalizeUpstreamModelID(binding.UpstreamModelName)
	if upstream == "" {
		return false, nil
	}
	if binding.CredentialID != nil {
		return credentialOwnsCatalogModel(ctx, client, *binding.CredentialID, upstream)
	}
	_, found, err := FindActiveCatalogCredential(ctx, client, binding.ProviderID, upstream, uuid.Nil)
	return found, err
}

// ModelHasActiveRoute reports whether a model has at least one routable binding.
func ModelHasActiveRoute(ctx context.Context, client *ent.Client, modelID uuid.UUID) (bool, error) {
	bindings, err := client.ModelBinding.Query().
		Where(
			modelbinding.ModelIDEQ(modelID),
			modelbinding.StatusEQ(modelbinding.StatusActive),
		).
		All(ctx)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		ready, err := BindingRouteReady(ctx, client, binding)
		if err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
	}
	return false, nil
}

// ModelHasActiveRouteForAdapter reports whether a model has a routable binding for a specific adapter.
func ModelHasActiveRouteForAdapter(ctx context.Context, client *ent.Client, modelID uuid.UUID, adapter modelbinding.Adapter) (bool, error) {
	bindings, err := client.ModelBinding.Query().
		Where(
			modelbinding.ModelIDEQ(modelID),
			modelbinding.AdapterEQ(adapter),
			modelbinding.StatusEQ(modelbinding.StatusActive),
		).
		All(ctx)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		ready, err := BindingRouteReady(ctx, client, binding)
		if err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
	}
	return false, nil
}

// CredentialLineHasActiveRoute reports whether a model has a routable binding on a specific Key line.
func CredentialLineHasActiveRoute(ctx context.Context, client *ent.Client, modelID, credentialID uuid.UUID) (bool, error) {
	bindings, err := client.ModelBinding.Query().
		Where(
			modelbinding.ModelIDEQ(modelID),
			modelbinding.CredentialIDEQ(credentialID),
			modelbinding.StatusEQ(modelbinding.StatusActive),
		).
		All(ctx)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		ready, err := BindingRouteReady(ctx, client, binding)
		if err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
	}
	return false, nil
}

// FindActiveCatalogCredential returns an active key that currently owns the upstream model in catalog.
func FindActiveCatalogCredential(
	ctx context.Context,
	client *ent.Client,
	providerID uuid.UUID,
	upstreamModel string,
	excludeCredentialID uuid.UUID,
) (*uuid.UUID, bool, error) {
	upstreamModel = normalizeUpstreamModelID(upstreamModel)
	if upstreamModel == "" {
		return nil, false, nil
	}
	query := client.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.ProviderIDEQ(providerID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
			providermodelcatalog.HasCredentialWith(
				providercredential.StatusEQ(providercredential.StatusActive),
				providercredential.ProviderIDEQ(providerID),
				providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
			),
		)
	owner, err := client.Provider.Get(ctx, providerID)
	if ent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if IsBeichenBaseURL(owner.BaseURL) {
		// Match the preview when a legacy binding leaves the key unspecified.
		query.Order(ent.Asc(providermodelcatalog.FieldCredentialID))
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, row := range rows {
		if excludeCredentialID != uuid.Nil && row.CredentialID == excludeCredentialID {
			continue
		}
		if normalizeUpstreamModelID(row.ModelID) == upstreamModel {
			id := row.CredentialID
			return &id, true, nil
		}
	}
	return nil, false, nil
}

func credentialOwnsCatalogModel(ctx context.Context, client *ent.Client, credentialID uuid.UUID, upstreamModel string) (bool, error) {
	cred, err := client.ProviderCredential.Get(ctx, credentialID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if cred.Status != providercredential.StatusActive {
		return false, nil
	}
	providerRow, err := client.Provider.Get(ctx, cred.ProviderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if providerRow.Status != provider.StatusActive {
		return false, nil
	}
	exists, err := client.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.CredentialIDEQ(credentialID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
		).
		All(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range exists {
		if normalizeUpstreamModelID(row.ModelID) == upstreamModel {
			return true, nil
		}
	}
	return false, nil
}

// DeactivateProviderResources marks all routes and catalog rows for a provider inactive.
func DeactivateProviderResources(ctx context.Context, client *ent.Client, providerID uuid.UUID) error {
	if _, err := client.ModelBinding.Update().
		Where(
			modelbinding.ProviderIDEQ(providerID),
			modelbinding.StatusEQ(modelbinding.StatusActive),
		).
		SetStatus(modelbinding.StatusInactive).
		Save(ctx); err != nil {
		return err
	}
	_, err := client.ProviderModelCatalog.Update().
		Where(providermodelcatalog.ProviderIDEQ(providerID)).
		SetStatus(providermodelcatalog.StatusInactive).
		Save(ctx)
	return err
}

// DeactivateCredentialCatalog marks all catalog rows for a key inactive.
func DeactivateCredentialCatalog(ctx context.Context, client *ent.Client, credentialID uuid.UUID) error {
	_, err := client.ProviderModelCatalog.Update().
		Where(providermodelcatalog.CredentialIDEQ(credentialID)).
		SetStatus(providermodelcatalog.StatusInactive).
		Save(ctx)
	return err
}

// ReconcileProviderBindingRoutes aligns binding status with current catalog ownership.
func ReconcileProviderBindingRoutes(ctx context.Context, client *ent.Client, providerID uuid.UUID) error {
	bindings, err := client.ModelBinding.Query().
		Where(modelbinding.ProviderIDEQ(providerID)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		owned, err := syncBindingCredentialFromCatalog(ctx, client, binding)
		if err != nil {
			return err
		}
		switch {
		case owned && binding.Status != modelbinding.StatusActive:
			if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusActive).Exec(ctx); err != nil {
				return err
			}
		case !owned && binding.Status == modelbinding.StatusActive:
			if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusInactive).Exec(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncBindingCredentialFromCatalog(ctx context.Context, client *ent.Client, binding *ent.ModelBinding) (bool, error) {
	if binding == nil {
		return false, nil
	}
	providerRow, err := client.Provider.Get(ctx, binding.ProviderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if providerRow.Status != provider.StatusActive {
		return false, nil
	}
	upstream := normalizeUpstreamModelID(binding.UpstreamModelName)
	if upstream == "" {
		return false, nil
	}
	if binding.CredentialID != nil {
		return credentialOwnsCatalogModel(ctx, client, *binding.CredentialID, upstream)
	}
	credentialID, found, err := FindActiveCatalogCredential(ctx, client, binding.ProviderID, upstream, uuid.Nil)
	if err != nil {
		return false, err
	}
	if !found || credentialID == nil {
		return false, nil
	}
	if binding.CredentialID == nil || *binding.CredentialID != *credentialID {
		if err := client.ModelBinding.UpdateOne(binding).SetCredentialID(*credentialID).Exec(ctx); err != nil {
			return false, err
		}
		binding.CredentialID = credentialID
	}
	return true, nil
}
