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

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

func normalizeUpstreamModelID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// BeichenProbeBlocksRouteSQL reads both historical status codes and the current
// structured classification. The caller checks the Key/group evidence version.
const BeichenProbeBlocksRouteSQL = `(r.status_code IN (401,403) OR r.error_code='credential_unreadable'
 OR r.detail->>'failure_code' IN ('GROUP_NOT_ALLOWED','credential_invalid'))`

// These predicates use the same Key evidence (e), catalog row (k), and probe
// (r) in gateway routing and teacher rebind.
const BeichenCatalogAuthReadySQL = `NOT EXISTS (
  SELECT 1 FROM maintenance_state s
  WHERE s.name='` + beichenCatalogAuthKey + `'||e.credential_id::text
    AND (s.last_error::jsonb->>'credential_revision')::timestamptz=e.revision
)`

const BeichenCurrentCatalogProbeSQL = `r.provider_id=k.provider_id AND r.credential_id=k.credential_id
  AND r.model=k.model_id::text AND k.status='active' AND k.catalog_source='key'
  AND ` + BeichenCurrentProbeSQL

// BindingRouteReady checks catalog coverage and confirmed upstream restrictions.
func BindingRouteReady(ctx context.Context, client *ent.Client, binding *ent.ModelBinding) (bool, error) {
	credential, err := bindingRouteCredential(ctx, client, binding)
	return credential != nil, err
}

func bindingRouteCredential(ctx context.Context, client *ent.Client, binding *ent.ModelBinding) (*ent.ProviderCredential, error) {
	if binding == nil || binding.Status != modelbinding.StatusActive {
		return nil, nil
	}
	upstream := strings.TrimSpace(binding.UpstreamModelName)
	if upstream == "" {
		return nil, nil
	}
	providerRow, err := client.Provider.Get(ctx, binding.ProviderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if providerRow.Status != provider.StatusActive {
		return nil, nil
	}
	credentialID := binding.CredentialID
	if credentialID == nil {
		var found bool
		credentialID, found, err = FindActiveCatalogCredential(ctx, client, binding.ProviderID, upstream, uuid.Nil)
		if err != nil || !found {
			return nil, err
		}
	}
	query := client.ProviderCredential.Query().Where(
		providercredential.IDEQ(*credentialID),
		providercredential.ProviderIDEQ(binding.ProviderID),
		providercredential.StatusEQ(providercredential.StatusActive),
		providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
		providercredential.HasModelCatalogEntriesWith(
			providermodelcatalog.ProviderIDEQ(binding.ProviderID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
			func(s *entsql.Selector) {
				s.Where(entsql.P(func(b *entsql.Builder) {
					b.WriteString("btrim(").WriteString(s.C(providermodelcatalog.FieldModelID)).
						WriteString("::text)::citext = ").Arg(upstream).WriteString("::citext")
				}))
			},
		),
	)
	if IsBeichenBaseURL(providerRow.BaseURL) {
		// Check the chosen credential and its denial evidence in one read.
		// A blocked legacy binding must not pick a different Key underneath.
		query.Where(func(s *entsql.Selector) {
			s.Where(entsql.P(func(b *entsql.Builder) {
				b.WriteString("EXISTS (SELECT 1 FROM (" + BeichenKeyEvidenceSQL + " WHERE c.id=").
					WriteString(s.C(providercredential.FieldID)).WriteString(`) e
WHERE NOT (` + BeichenGroupUnavailableSQL + `)
  AND ` + BeichenCatalogAuthReadySQL + `
  AND NOT EXISTS (
    SELECT 1 FROM provider_model_catalog k JOIN upstream_probes r ON ` + BeichenCurrentCatalogProbeSQL + `
    WHERE k.provider_id=`).Arg(binding.ProviderID).WriteString(` AND k.credential_id=e.credential_id
      AND r.protocol=`).Arg(binding.Adapter).WriteString(` AND btrim(k.model_id::text)::citext=`).Arg(upstream).WriteString(`::citext
      AND ` + BeichenProbeBlocksRouteSQL + `
  ))`)
			}))
		})
	}
	row, err := query.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return row, err
}

// ResolveBindingCredential uses the same route check before decrypting the
// credential that will actually be sent upstream.
func ResolveBindingCredential(ctx context.Context, client *ent.Client, manager *security.Manager, binding *ent.ModelBinding) (string, uuid.UUID, error) {
	row, err := bindingRouteCredential(ctx, client, binding)
	if err != nil || row == nil {
		return "", uuid.Nil, err
	}
	credential, err := manager.DecryptCredential(row.CredentialCiphertext)
	if err != nil || strings.TrimSpace(credential) == "" {
		return "", uuid.Nil, nil
	}
	return credential, row.ID, nil
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
