package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
)

// ProbeProtocolsForEndpoints returns the catalog protocols an operation needs.
func ProbeProtocolsForEndpoints(endpoints []BindingRouteProbeEndpoint) []modelbinding.Adapter {
	result := make([]modelbinding.Adapter, 0, len(endpoints))
	for _, endpoint := range endpoints {
		adapter, ok := probeEndpointProtocol(endpoint)
		if ok && !slices.Contains(result, adapter) {
			result = append(result, adapter)
		}
	}
	return result
}

func probeEndpointProtocol(endpoint BindingRouteProbeEndpoint) (modelbinding.Adapter, bool) {
	switch endpoint {
	case BindingRouteProbeChatCompletions:
		return modelbinding.AdapterOpenaiCompatible, true
	case BindingRouteProbeResponses:
		return modelbinding.AdapterOpenaiResponses, true
	case BindingRouteProbeMessages:
		return modelbinding.AdapterAnthropic, true
	case BindingRouteProbeImagesGenerations:
		return modelbinding.AdapterOpenaiImages, true
	case BindingRouteProbeGenerateContent, BindingRouteProbeStreamGenerateContent, BindingRouteProbeEmbedContent:
		return modelbinding.AdapterGoogleGeminiV1beta, true
	default:
		return "", false
	}
}

// FindProbeCatalogCredential requires one Key to list the model for every
// operation being probed. Ambiguous Key ownership returns nil for review.
func FindProbeCatalogCredential(ctx context.Context, database *sql.DB, client *ent.Client, providerID uuid.UUID, baseURL, model string, protocols ...string) (*ent.ProviderCredential, error) {
	model = strings.TrimSpace(model)
	first, second := model, "models/"+model
	if strings.HasPrefix(model, "models/") || IsGeminiFamilyModel(model) {
		nativeID := strings.TrimPrefix(model, "models/")
		first, second = nativeID, "models/"+nativeID
	}
	rows, err := database.QueryContext(ctx, `SELECT m.credential_id,array_to_json(m.supported_protocols)::text
		FROM provider_model_catalog m
		JOIN provider_credentials c ON c.id=m.credential_id AND c.provider_id=m.provider_id
		JOIN providers p ON p.id=m.provider_id
		WHERE m.provider_id=$1 AND m.status='active' AND m.catalog_source='key'
		  AND c.status='active' AND p.status='active'
		  AND (lower(m.model_id::text)=lower($2) OR lower(m.model_id::text)=lower($3))`,
		providerID, first, second)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var credentialID uuid.UUID
		var raw []byte
		if err := rows.Scan(&credentialID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var supported []string
		if json.Unmarshal(raw, &supported) != nil || !credentialListsProtocols(supported, protocols) {
			continue
		}
		owners[credentialID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	credentials := make([]*ent.ProviderCredential, 0, len(owners))
	for credentialID := range owners {
		credential, err := client.ProviderCredential.Query().Where(
			providercredential.IDEQ(credentialID),
			providercredential.ProviderIDEQ(providerID),
			providercredential.StatusEQ(providercredential.StatusActive),
			providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
		).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	if len(credentials) != 1 {
		return nil, nil
	}
	return credentials[0], nil
}

func credentialListsProtocols(supported, required []string) bool {
	for _, protocol := range required {
		found := false
		for _, value := range supported {
			if strings.EqualFold(strings.TrimSpace(value), protocol) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
