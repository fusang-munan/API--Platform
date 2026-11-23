package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

const currentProbeRevisionSQL = `r.checked_at >= c.updated_at AND k.last_seen_at >= c.updated_at`

const probeEvidenceSQL = `jsonb_build_object('checked_at',r.checked_at,
 'last_passed_at',CASE WHEN r.last_passed_at >= c.updated_at THEN r.last_passed_at END,
 'status_code',r.status_code,'error_code',r.error_code)`

const publishedBeichenProviderSQL = `lower(btrim(p.base_url)) IN
 ('https://api.beichenapi.com','https://api.beichenapi.com/','https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')`

// Resolve the credential with the same Beichen ordering as
// FindActiveCatalogCredential. With an explicit binding only that Key can match.
const publishedProtocolRouteSQL = `(
 SELECT jsonb_build_object('credential_id',c.id,'ciphertext',encode(c.credential_ciphertext,'base64'),
  'upstream_model',k.model_id::text,'probe',CASE WHEN r.protocol IS NOT NULL THEN ` + probeEvidenceSQL + ` END)
 FROM provider_model_catalog k
 JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id AND c.status='active'
 JOIN providers p ON p.id=c.provider_id AND p.status='active'
 LEFT JOIN upstream_probes r ON r.provider_id=p.id AND r.credential_id=c.id
  AND r.model=k.model_id::text AND r.protocol=%[3]s AND ` + currentProbeRevisionSQL + `
  AND k.catalog_source = 'key' AND ` + publishedBeichenProviderSQL + `
 WHERE k.provider_id=%[1]s AND (%[2]s IS NULL OR c.id=%[2]s) AND k.status='active'
  AND (NOT (` + publishedBeichenProviderSQL + `) OR k.last_seen_at>=c.updated_at)
  AND lower(k.model_id::text)=lower(btrim(%[4]s))
 ORDER BY c.id LIMIT 1
) AS route_evidence`

// LoadPublishedProtocols reads stored evidence for the supplied school model IDs.
// Only routes with credentials that the gateway can decrypt are included.
func LoadPublishedProtocols(ctx context.Context, client *ent.Client, modelIDs []uuid.UUID, manager *security.Manager) (map[uuid.UUID][]ProtocolCapability, error) {
	result := make(map[uuid.UUID][]ProtocolCapability)
	if len(modelIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ModelID       uuid.UUID `sql:"model_id"`
		Adapter       string
		RouteEvidence []byte `sql:"route_evidence"`
	}
	err := client.ModelBinding.Query().Where(
		modelbinding.ModelIDIn(modelIDs...),
		modelbinding.StatusEQ(modelbinding.StatusActive),
		modelbinding.HasModelWith(entmodel.StatusEQ(entmodel.StatusActive)),
	).Select(modelbinding.FieldModelID, modelbinding.FieldAdapter).
		Aggregate(func(s *entsql.Selector) string {
			return fmt.Sprintf(publishedProtocolRouteSQL, s.C(modelbinding.FieldProviderID), s.C(modelbinding.FieldCredentialID),
				s.C(modelbinding.FieldAdapter), s.C(modelbinding.FieldUpstreamModelName))
		}).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	usable := make(map[uuid.UUID]bool)
	for _, row := range rows {
		if len(row.RouteEvidence) == 0 {
			continue
		}
		var route struct {
			CredentialID  uuid.UUID      `json:"credential_id"`
			Ciphertext    []byte         `json:"ciphertext"`
			UpstreamModel string         `json:"upstream_model"`
			Probe         *ProbeEvidence `json:"probe"`
		}
		if err := json.Unmarshal(row.RouteEvidence, &route); err != nil {
			return nil, err
		}
		valid, checked := usable[route.CredentialID]
		if !checked {
			clear, err := manager.DecryptCredential(route.Ciphertext)
			valid = err == nil && strings.TrimSpace(clear) != ""
			usable[route.CredentialID] = valid
		}
		if valid {
			result[row.ModelID] = append(result[row.ModelID], protocolCapability(row.Adapter, route.UpstreamModel, true, route.Probe))
		}
	}
	return result, nil
}
