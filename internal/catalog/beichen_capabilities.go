package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type CapabilityIssue struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	ActionHint string `json:"action_hint"`
}

type CapabilityGap struct {
	Model           string     `json:"model"`
	CredentialID    *uuid.UUID `json:"credential_id"`
	CredentialLabel string     `json:"credential_label"`
	Category        string     `json:"category"`
	CapabilityIssue
}

// ModelCapability describes an upstream model on one specific Key.
type ModelCapability struct {
	Model               string               `json:"model"`
	CredentialID        *uuid.UUID           `json:"credential_id"`
	CredentialLabel     string               `json:"credential_label"`
	GroupID             *int64               `json:"group_id"`
	GroupStatus         string               `json:"group_status,omitempty"`
	GroupCheckedAt      *time.Time           `json:"group_checked_at,omitempty"`
	KeyEnabled          bool                 `json:"key_enabled"`
	Bindings            []BeichenBinding     `json:"bindings"`
	SupportedProtocols  []string             `json:"supported_protocols"`
	AvailableProtocols  []string             `json:"available_protocols"`
	RecommendedProtocol *string              `json:"recommended_protocol"`
	ProtocolDetails     []ProtocolCapability `json:"protocol_details"`
	CatalogState        string               `json:"catalog_state"`
	Availability        string               `json:"availability"`
	AvailabilityReason  string               `json:"availability_reason,omitempty"`
	PriceState          string               `json:"price_state"`
	PriceCovered        bool                 `json:"price_covered"`
	PriceBlocker        *CapabilityIssue     `json:"price_blocker"`
	MultiplierState     string               `json:"multiplier_state"`
	ProbeState          string               `json:"probe_state"`
	BindingState        string               `json:"binding_state"`
	ResponsesReady      bool                 `json:"responses_ready"`
	ActionHint          string               `json:"action_hint"`
}

// BeichenBinding links the catalog's raw upstream ID to a school model.
// Published describes configuration; BindingReady also requires current Key evidence.
type BeichenBinding struct {
	SchoolModel string `json:"school_model"`
	Priority    int    `json:"priority"`
	Published   bool   `json:"published"`
	ProtocolCapability
}

type BeichenCapabilities struct {
	Capabilities  []ModelCapability              `json:"capabilities"`
	Gaps          []CapabilityGap                `json:"gaps"`
	RetiredModels map[string]RetiredBeichenModel `json:"-"`
}

type RetiredBeichenModel struct {
	Model    string
	Channels []string
}

// BeichenKeyEvidenceSQL is shared by model catalogs and activation preview.
// A credential revision or an observed group transition invalidates earlier
// catalog, pricing and probe evidence. Repeated reads of the same group do
// not move that boundary.
const BeichenKeyEvidenceSQL = `SELECT c.id AS credential_id, c.updated_at AS revision,
   cat.last_success AS catalog_at, cat.last_error AS catalog_error,
   GREATEST(c.updated_at, changed.group_at, history.group_at,
     CASE WHEN identity.key_id IS NOT NULL AND
          (observed.key_id <> identity.key_id OR access.upstream_key_id <> identity.key_id)
       THEN identity.checked_at END,
     CASE WHEN COALESCE(inventory.group_id, identity.group_id)>0 AND
          COALESCE(w.group_id, mv.group_id, access.group_id) <>
            COALESCE(inventory.group_id, identity.group_id)
       THEN COALESCE(history.group_at, changed.group_at, GREATEST(identity.checked_at,inventory.last_seen)) END
   ) AS evidence_since,
   NULLIF(known.group_id,0) AS group_id,
   COALESCE(GREATEST(identity.checked_at,inventory.last_seen),observed.checked_at,
     w.checked_at,mv.effective_at,access.updated_at) AS group_checked_at,
   CASE
     WHEN known.group_id=0 THEN 'not_found'
     WHEN inventory.enabled=false AND inventory.last_seen>=COALESCE(identity.checked_at,c.updated_at) THEN 'not_allowed'
     WHEN observed.state='not_allowed' AND observed.group_id IS NOT DISTINCT FROM
          known.group_id THEN observed.state
     WHEN identity.key_id IS NOT NULL AND COALESCE(inventory.group_id,identity.group_id) IS NULL THEN 'pending'
     WHEN observed.state IN ('pending','not_found') AND
          COALESCE(identity.group_id,inventory.group_id) IS NULL THEN observed.state
     WHEN known.group_id>0
       THEN 'assigned'
     ELSE '' END AS group_status,
   CASE WHEN billing.last_error IS NOT NULL THEN 'sync_failed'
     WHEN mv.id IS NULL OR billing.last_success IS NULL THEN 'missing'
     WHEN mv.stale_at IS NOT NULL OR billing.last_success<c.updated_at
       OR billing.last_success<=statement_timestamp()-interval '16 minutes' THEN 'expired'
     ELSE 'current' END AS multiplier_state
 FROM provider_credentials c
 LEFT JOIN beichen_datasets cat ON cat.credential_id=c.id AND cat.kind='catalog'
 LEFT JOIN beichen_datasets billing ON billing.credential_id=c.id AND billing.kind='billing'
 LEFT JOIN beichen_panel_access access ON access.credential_id=c.id AND access.updated_at=c.updated_at
 LEFT JOIN beichen_website_catalogs w ON w.credential_id=c.id AND w.credential_revision=c.updated_at
 LEFT JOIN LATERAL (
   SELECT (b->>'key_id')::bigint AS key_id,(b->>'group_id')::bigint AS group_id,(b->>'checked_at')::timestamptz AS checked_at
   FROM jsonb_extract_path(billing.website_group_observation,'confirmed_key_binding') b
   WHERE (b->>'credential_revision')::timestamptz=c.updated_at
 ) identity ON true
 LEFT JOIN LATERAL (
   SELECT (o->>'website_key_id')::bigint AS key_id,(o->>'id')::bigint AS group_id,
     (o->>'checked_at')::timestamptz AS checked_at,o->>'group_status' AS state
   FROM (SELECT billing.website_group_observation AS o) data
   WHERE (o->>'credential_revision')::timestamptz=c.updated_at
 ) observed ON true
 LEFT JOIN LATERAL (
   SELECT i.upstream_key_id,i.group_id,i.enabled,s.last_seen
   FROM beichen_website_keys i JOIN beichen_website_key_sync s ON s.provider_id=i.provider_id
   WHERE i.credential_id=c.id AND i.provider_id=c.provider_id AND i.match_status='matched'
     AND i.ciphertext_digest=sha256(c.credential_ciphertext)
     AND s.last_seen>=COALESCE(identity.checked_at,c.updated_at)
 ) inventory ON true
 LEFT JOIN LATERAL (
   SELECT id,group_id,stale_at,effective_at FROM beichen_multiplier_versions
   WHERE credential_id=c.id AND effective_at<=statement_timestamp()
   ORDER BY effective_at DESC,id DESC LIMIT 1
 ) mv ON true
 LEFT JOIN LATERAL (
   SELECT max(effective_at) AS group_at FROM (
     SELECT effective_at,group_id,lag(group_id) OVER (ORDER BY effective_at,id) AS prior_group
     FROM beichen_multiplier_versions
     WHERE credential_id=c.id AND group_id IS NOT NULL AND effective_at<=statement_timestamp()
   ) versions WHERE prior_group<>group_id
 ) changed ON true
 LEFT JOIN LATERAL (
   SELECT max(observed_at) AS group_at FROM (
     SELECT observed_at,group_id,lag(group_id) OVER (ORDER BY id) AS prior_group
     FROM beichen_website_key_rate_versions
     WHERE provider_id=c.provider_id AND upstream_key_id=COALESCE(inventory.upstream_key_id,identity.key_id)
       AND group_id IS NOT NULL
   ) versions WHERE prior_group<>group_id
 ) history ON true
 CROSS JOIN LATERAL (
   SELECT COALESCE(inventory.group_id,identity.group_id,observed.group_id,w.group_id,mv.group_id,access.group_id) AS group_id
 ) known`

const BeichenKeyEvidenceCTE = `beichen_key_evidence AS (
 ` + BeichenKeyEvidenceSQL + ` WHERE c.provider_id=$1
)`

const BeichenCatalogCurrentSQL = `(e.catalog_error IS NULL AND
 COALESCE(e.catalog_at>=e.evidence_since AND e.catalog_at>statement_timestamp()-interval '30 minutes',false))`

// A dated raw Key list also supplies evidence for older imports that predate the
// catalog dataset marker. Website entries never establish a Key's coverage.
const BeichenKeyListsModelSQL = `(k.catalog_source='key' AND k.last_seen_at>=e.evidence_since AND
 e.catalog_error IS NULL AND COALESCE(e.catalog_at,k.last_seen_at)>=e.evidence_since AND
 COALESCE(e.catalog_at,k.last_seen_at)>statement_timestamp()-interval '30 minutes')`

const BeichenGroupAllowsRouteSQL = `e.group_status NOT IN ('not_allowed','not_found','pending')`

// A sweep saves its outcome after dispatch. When an attempt reservation is
// present, also require its dispatch time to belong to the current group.
const beichenProbeAttemptSQL = `
 SELECT a.checked_at FROM beichen_probe_usage a
 WHERE a.provider_id=r.provider_id AND a.credential_id=r.credential_id
   AND a.model=r.model AND a.protocol=r.protocol AND a.checked_at<=`

const BeichenCurrentProbeSQL = `(r.checked_at>=e.evidence_since AND (e.evidence_since=e.revision OR
 COALESCE((` + beichenProbeAttemptSQL + `r.checked_at ORDER BY a.checked_at DESC LIMIT 1),r.checked_at)>=e.evidence_since))`

const beichenCurrentLastPassSQL = `(r.last_passed_at>=e.evidence_since AND (e.evidence_since=e.revision OR
 COALESCE((` + beichenProbeAttemptSQL + `r.last_passed_at ORDER BY a.checked_at DESC LIMIT 1),r.last_passed_at)>=e.evidence_since))`

const BeichenPanelQuoteCurrentSQL = `(snap.expires_at>statement_timestamp()
 AND COALESCE(snap.credential_ciphertext_sha256=sha256(c.credential_ciphertext),snap.credential_revision=c.updated_at)
 AND (e.group_id IS NULL OR snap.group_id=e.group_id)
 AND (e.evidence_since=e.revision OR snap.effective_at>=e.evidence_since))`

const BeichenFollowPriceCurrentSQL = `((version.source NOT IN ('panel','derived_from_usage') OR ` + BeichenPanelQuoteCurrentSQL + `)
 AND (e.evidence_since=e.revision OR version.effective_at>=e.evidence_since))`

const BeichenFixedPriceSQL = `(price.school_input IS NOT NULL AND price.school_output IS NOT NULL
 AND (price.mode='manual' OR (
   version.source IS DISTINCT FROM 'derived_from_usage'
   AND price.school_cache_read IS NOT NULL AND price.school_cache_write IS NOT NULL
   AND ` + BeichenFollowPriceCurrentSQL + `
 )))`

// Catalog and binding join on the exact upstream ID used by the gateway
// (trimmed and case insensitive). A website spelling variant can explain a
// directory match but cannot make up for a missing binding or Key entry.
const readBeichenCapabilitiesSQL = `
WITH ` + BeichenKeyEvidenceCTE + `, entries AS (
 SELECT k.*,lower(k.model_id::text) AS name_key
 FROM provider_model_catalog k WHERE k.provider_id=$1 AND k.status='active'
), website_names AS (
 SELECT w.credential_id,lower(btrim(n.model)) AS name_key,btrim(n.model) AS model
 FROM beichen_website_catalogs w
 JOIN provider_credentials c ON c.id=w.credential_id AND w.credential_revision=c.updated_at AND c.status='active'
 JOIN beichen_key_evidence e ON e.credential_id=c.id
 CROSS JOIN LATERAL unnest(w.models) n(model)
 WHERE c.provider_id=$1 AND btrim(n.model)<>'' AND w.checked_at>=e.evidence_since
   AND (e.group_id IS NULL OR e.group_id=w.group_id)
), bindings AS (
 SELECT COALESCE(b.credential_id,picked.credential_id) AS credential_id,
  lower(btrim(b.upstream_model_name)) AS name_key,btrim(b.upstream_model_name) AS model,
  s.model_id::text AS school_model,b.adapter,b.priority,b.status='active' AS enabled,s.status='active' AS published
 FROM model_bindings b JOIN models s ON s.id=b.model_id
 LEFT JOIN LATERAL (
  SELECT k.credential_id FROM entries k
  JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id AND c.status='active'
  WHERE b.credential_id IS NULL AND k.name_key=lower(btrim(b.upstream_model_name))
  ORDER BY k.credential_id LIMIT 1
 ) picked ON true
 WHERE b.provider_id=$1 AND btrim(b.upstream_model_name)<>''
), grouped_bindings AS (
 SELECT credential_id,name_key,min(model) AS model,bool_or(published) AS active_school,
  COALESCE(jsonb_agg(jsonb_build_object('school_model',school_model,'protocol',adapter,'priority',priority,
    'published',published) ORDER BY priority, school_model, adapter) FILTER (WHERE enabled),'[]'::jsonb) AS routes
 FROM bindings GROUP BY credential_id,name_key
), website AS (
 SELECT w.credential_id,COALESCE(matched.name_key,w.name_key) AS name_key,min(w.model) AS model
 FROM website_names w
 LEFT JOIN LATERAL (
   SELECT lower(btrim(a.school_model)) AS name_key FROM upstream_model_aliases a
   WHERE a.provider_id=$1 AND (a.credential_id=w.credential_id OR a.credential_id IS NULL)
     AND lower(btrim(a.upstream_model))=w.name_key
   ORDER BY a.credential_id NULLS LAST,a.created_at DESC,a.id DESC LIMIT 1
 ) web_alias ON true
 LEFT JOIN LATERAL (
   SELECT CASE WHEN count(*)=1 THEN min(k.name_key) END AS name_key FROM entries k
   LEFT JOIN LATERAL (
     SELECT lower(btrim(a.school_model)) AS name_key FROM upstream_model_aliases a
     WHERE a.provider_id=$1 AND (a.credential_id=k.credential_id OR a.credential_id IS NULL)
       AND lower(btrim(a.upstream_model))=k.name_key
     ORDER BY a.credential_id NULLS LAST,a.created_at DESC,a.id DESC LIMIT 1
   ) key_alias ON true
   WHERE k.credential_id=w.credential_id AND k.catalog_source='key'
     AND (translate(k.name_key,'.','-')=translate(w.name_key,'.','-')
       OR COALESCE(key_alias.name_key,k.name_key)=COALESCE(web_alias.name_key,w.name_key))
     AND NOT EXISTS (
       SELECT 1 FROM entries exact WHERE exact.credential_id=w.credential_id
         AND exact.name_key=w.name_key AND exact.catalog_source='key'
     )
 ) matched ON true
 GROUP BY w.credential_id,COALESCE(matched.name_key,w.name_key)
), names AS (
 SELECT credential_id,name_key FROM entries
 UNION SELECT credential_id,name_key FROM website
 UNION SELECT credential_id,name_key FROM grouped_bindings
)
SELECT COALESCE(k.model_id::text,b.model,w.model),n.credential_id,COALESCE(c.label::text,''),
 COALESCE(p.status='active' AND c.status='active',false),COALESCE(octet_length(c.credential_ciphertext)>0,false),
 e.group_id,COALESCE(e.group_status,''),e.group_checked_at,
 COALESCE(` + BeichenKeyListsModelSQL + `,false),w.model IS NOT NULL,
 COALESCE(` + BeichenCatalogCurrentSQL + ` AND
   (k.model_id IS NULL OR (k.catalog_source='key' AND k.last_seen_at>=e.evidence_since)),false),
 COALESCE(k.catalog_source='key' OR e.catalog_error IS NOT NULL
   OR (e.catalog_at IS NOT NULL AND k.catalog_source IS DISTINCT FROM 'website'),false),
 COALESCE(k.raw_entry,'{}'::jsonb),COALESCE(to_jsonb(k.supported_protocols),'[]'::jsonb),
 COALESCE(b.active_school,false) OR EXISTS (
  SELECT 1 FROM models s WHERE s.status='active' AND s.model_id=n.name_key::citext
   AND NOT EXISTS (SELECT 1 FROM model_bindings other WHERE other.model_id=s.id)
 ),
 COALESCE(b.routes,'[]'::jsonb),probes.observations,
 price.mode,COALESCE(` + BeichenFixedPriceSQL + `,false),
 COALESCE(version.source='derived_from_usage' AND ` + BeichenPanelQuoteCurrentSQL + `,false),
 CASE WHEN ` + BeichenPanelQuoteCurrentSQL + ` THEN snap.model_pricing->k.model_id::text END,
 COALESCE(snap.expires_at<=statement_timestamp() AND snap.model_pricing ? k.model_id::text,false),
 COALESCE(e.multiplier_state,'not_applicable')
FROM names n JOIN providers p ON p.id=$1
LEFT JOIN provider_credentials c ON c.id=n.credential_id AND c.provider_id=p.id
LEFT JOIN beichen_key_evidence e ON e.credential_id=c.id
LEFT JOIN entries k ON k.credential_id=n.credential_id AND k.name_key=n.name_key
LEFT JOIN website w ON w.credential_id=n.credential_id AND w.name_key=n.name_key
LEFT JOIN grouped_bindings b ON b.credential_id IS NOT DISTINCT FROM n.credential_id AND b.name_key=n.name_key
LEFT JOIN LATERAL (
 SELECT effective_at,expires_at,credential_revision,credential_ciphertext_sha256,group_id,model_pricing FROM beichen_panel_snapshots
 WHERE credential_id=c.id AND effective_at<=statement_timestamp() ORDER BY effective_at DESC LIMIT 1
) snap ON true
CROSS JOIN LATERAL beichen_price_at(c.id,k.model_id::text,statement_timestamp()) price
LEFT JOIN beichen_price_versions version ON version.id=price.upstream_version
CROSS JOIN LATERAL (
 SELECT COALESCE(jsonb_object_agg(r.protocol,jsonb_build_object('checked_at',r.checked_at,
   'last_passed_at',CASE WHEN ` + beichenCurrentLastPassSQL + ` THEN r.last_passed_at END,
   'status_code',r.status_code,'error_code',r.error_code)),'{}'::jsonb) AS observations
 FROM upstream_probes r
 WHERE r.provider_id=p.id AND r.credential_id=c.id AND r.model=k.model_id::text
  AND ` + BeichenCurrentProbeSQL + ` AND ` + BeichenKeyListsModelSQL + ` AND ` + BeichenGroupAllowsRouteSQL + `
  AND p.status='active' AND c.status='active'
) probes
ORDER BY n.name_key,c.label,c.id`

type capabilityRow struct {
	ModelCapability
	hasCredential, keyListed, websiteListed, catalogCurrent, hadCatalog, activeSchool bool
	fixedPrice, derivedPrice, priceExpired                                            bool
	priceMode                                                                         string
	priceTerms                                                                        []byte
	entryRaw, declaredJSON, bindingsJSON, probesJSON                                  []byte
}

func ReadBeichenCapabilities(ctx context.Context, db *sql.DB, providerID uuid.UUID) (BeichenCapabilities, error) {
	var view BeichenCapabilities
	if db == nil {
		return view, domain.NewError(domain.CodeDependencyUnavailable, "北辰能力目录数据库不可用")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return view, domain.WrapError(domain.CodeDependencyUnavailable, "读取北辰能力目录失败", err)
	}
	defer tx.Rollback()
	var baseURL string
	err = tx.QueryRowContext(ctx, "SELECT base_url FROM providers WHERE id=$1", providerID).Scan(&baseURL)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !IsBeichenBaseURL(baseURL) {
		return view, domain.NewError(domain.CodeNotFound, "北辰供应商不存在")
	}
	if err == nil {
		view, err = ReadBeichenCapabilitySnapshot(ctx, tx, providerID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return BeichenCapabilities{}, domain.WrapError(domain.CodeDependencyUnavailable, "读取北辰能力目录失败", err)
	}
	return view, nil
}

// ReadBeichenCapabilitySnapshot is also used by the overview so it can reuse
// the same route evidence and retirement decisions in its read transaction.
func ReadBeichenCapabilitySnapshot(ctx context.Context, tx *sql.Tx, providerID uuid.UUID) (BeichenCapabilities, error) {
	view := BeichenCapabilities{Capabilities: []ModelCapability{}, Gaps: []CapabilityGap{}}
	retired, err := readRetiredBeichenModels(ctx, tx, providerID)
	if err != nil {
		return view, err
	}
	view.RetiredModels = retired
	return view, readCapabilityRows(ctx, tx, providerID, &view)
}

// An enabled legacy binding on a disabled Key remains a repair case. A
// historical name can also survive through another alias, Key or provider.
func readRetiredBeichenModels(ctx context.Context, tx *sql.Tx, providerID uuid.UUID) (map[string]RetiredBeichenModel, error) {
	rows, err := tx.QueryContext(ctx, `
WITH links AS (
 SELECT k.model_id::text AS name,c.id AS credential_id,NULL::uuid AS school_id,
   (p.status<>'active' OR c.status<>'active') AS retired
 FROM provider_model_catalog k JOIN providers p ON p.id=k.provider_id
 JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=p.id
 WHERE k.provider_id=$1
 UNION ALL
 SELECT btrim(b.upstream_model_name),b.credential_id,b.model_id,
   b.status='inactive' AND (p.status<>'active' OR COALESCE(c.status<>'active',false))
 FROM model_bindings b JOIN providers p ON p.id=b.provider_id
 LEFT JOIN provider_credentials c ON c.id=b.credential_id AND c.provider_id=p.id
 WHERE b.provider_id=$1 AND btrim(b.upstream_model_name)<>''
), historical AS (
 SELECT lower(l.name) AS name_key,min(l.name) AS name,
   COALESCE(jsonb_agg(DISTINCT COALESCE(pc.channel,c.label::text)) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb) AS channels
 FROM links l LEFT JOIN provider_credentials c ON c.id=l.credential_id
 LEFT JOIN beichen_provision_channels pc ON pc.credential_id=c.id
 GROUP BY lower(l.name) HAVING bool_and(l.retired)
), school_models AS (
 SELECT h.name_key,m.id FROM historical h JOIN models m ON m.model_id=h.name_key::citext
 UNION
 SELECT lower(name),school_id FROM links WHERE school_id IS NOT NULL
 UNION
 SELECT lower(btrim(a.upstream_model)),m.id FROM upstream_model_aliases a
 JOIN models m ON m.model_id=btrim(a.school_model)::citext
 WHERE a.provider_id=$1
)
SELECT h.name_key,h.name,h.channels
FROM historical h
WHERE NOT EXISTS (
 SELECT 1 FROM model_bindings b JOIN providers p ON p.id=b.provider_id AND p.status='active'
 WHERE b.status='active' AND (lower(btrim(b.upstream_model_name))=h.name_key OR EXISTS (
   SELECT 1 FROM school_models m WHERE m.name_key=h.name_key AND m.id=b.model_id
 ))
)
AND NOT EXISTS (
 SELECT 1 FROM upstream_model_aliases a
 JOIN provider_model_catalog k ON k.provider_id=a.provider_id AND k.status='active' AND k.catalog_source='key'
   AND k.model_id=btrim(a.upstream_model)::citext AND (a.credential_id IS NULL OR a.credential_id=k.credential_id)
 JOIN provider_credentials c ON c.id=k.credential_id AND c.status='active'
 JOIN providers p ON p.id=k.provider_id AND p.status='active'
 WHERE a.provider_id=$1 AND (lower(btrim(a.school_model))=h.name_key OR EXISTS (
   SELECT 1 FROM school_models s JOIN models m ON m.id=s.id
   WHERE s.name_key=h.name_key AND m.model_id=btrim(a.school_model)::citext
 ))
)
AND NOT EXISTS (
 SELECT 1 FROM links l
 JOIN beichen_provision_channels old_channel ON old_channel.credential_id=l.credential_id
 JOIN provider_credentials old ON old.id=l.credential_id
 JOIN beichen_provision_channels new_channel ON new_channel.channel=old_channel.channel
 JOIN provider_credentials c ON c.id=new_channel.credential_id AND c.provider_id=$1 AND c.status='active'
   AND c.created_at>old.created_at
 JOIN providers p ON p.id=c.provider_id AND p.status='active'
 LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='catalog'
 WHERE lower(l.name)=h.name_key AND (d.last_success IS NULL OR d.last_success<c.updated_at)
)`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	retired := make(map[string]RetiredBeichenModel)
	for rows.Next() {
		var key string
		var row RetiredBeichenModel
		var channels []byte
		if err := rows.Scan(&key, &row.Model, &channels); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(channels, &row.Channels); err != nil {
			return nil, err
		}
		slices.Sort(row.Channels)
		retired[key] = row
	}
	return retired, rows.Err()
}

func readCapabilityRows(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *BeichenCapabilities) error {
	rows, err := tx.QueryContext(ctx, readBeichenCapabilitiesSQL, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row capabilityRow
		if err := rows.Scan(&row.Model, &row.CredentialID, &row.CredentialLabel,
			&row.KeyEnabled, &row.hasCredential, &row.GroupID, &row.GroupStatus, &row.GroupCheckedAt,
			&row.keyListed, &row.websiteListed, &row.catalogCurrent, &row.hadCatalog,
			&row.entryRaw, &row.declaredJSON, &row.activeSchool, &row.bindingsJSON, &row.probesJSON,
			&row.priceMode, &row.fixedPrice, &row.derivedPrice, &row.priceTerms, &row.priceExpired, &row.MultiplierState); err != nil {
			return err
		}
		if _, retired := view.RetiredModels[strings.ToLower(row.Model)]; retired {
			continue
		}
		if err := row.evaluate(view); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (row *capabilityRow) addGap(view *BeichenCapabilities, category, code, message, action string) *CapabilityIssue {
	issue := CapabilityIssue{Code: code, Message: message, ActionHint: action}
	view.Gaps = append(view.Gaps, CapabilityGap{row.Model, row.CredentialID, row.CredentialLabel, category, issue})
	if row.ActionHint == "" {
		row.ActionHint = action
	}
	return &issue
}

func (row *capabilityRow) evaluate(view *BeichenCapabilities) error {
	entry := ProviderEntry{ID: row.Model}
	var probes map[string]*ProbeEvidence
	for _, pair := range []struct {
		raw []byte
		dst any
	}{
		{row.entryRaw, &entry.Raw}, {row.declaredJSON, &entry.SupportedProtocols},
		{row.bindingsJSON, &row.Bindings}, {row.probesJSON, &probes},
	} {
		if err := json.Unmarshal(pair.raw, pair.dst); err != nil {
			return err
		}
	}

	rescan := "在供应商管理中重扫「" + row.CredentialLabel + "」Key 的模型目录。"
	switch {
	case row.keyListed && row.KeyEnabled:
		row.CatalogState = "key_listed"
	case row.KeyEnabled && !row.catalogCurrent:
		row.CatalogState = "catalog_unverified"
		if row.hadCatalog {
			row.CatalogState = "catalog_stale"
		}
		row.addGap(view, "catalog", row.CatalogState, "当前 Key 的目录待复核。", rescan)
	case row.websiteListed && row.KeyEnabled:
		row.CatalogState = "website_only"
		row.addGap(view, "catalog", "website_only", "官网分组列出了该模型，当前这把 Key 的目录没有返回它。", rescan)
	default:
		row.CatalogState = "catalog_missing"
	}

	groupAllowed := row.GroupStatus != "not_found" && row.GroupStatus != "not_allowed" && row.GroupStatus != "pending"
	routeCurrent := row.keyListed && row.KeyEnabled && row.hasCredential && groupAllowed
	published := make([]string, 0, len(row.Bindings))
	for i := range row.Bindings {
		b := &row.Bindings[i]
		b.ProtocolCapability = protocolCapability(b.Protocol, row.Model, routeCurrent && b.Published, probes[b.Protocol])
		if b.Published {
			published = append(published, b.Protocol)
		}
	}
	row.BindingState = "not_published"
	switch {
	case len(published) > 0 && routeCurrent:
		row.BindingState = "ready"
	case len(published) > 0 && row.KeyEnabled && (row.CatalogState == "catalog_unverified" ||
		row.CatalogState == "catalog_stale" || row.GroupStatus == "pending"):
		row.BindingState = "unverified"
		if row.ActionHint == "" {
			row.addGap(view, "binding", "binding_unverified", "绑定 Key 的分组待核实。", rescan)
		}
	case row.activeSchool:
		row.BindingState = "invalid"
		action := "检查渠道 Key 与模型协议，再预览并确认重绑。"
		switch {
		case row.CredentialID == nil || !row.KeyEnabled || !row.hasCredential:
			action = "启用或配置有效的渠道 Key，重扫目录后再重绑。"
		case !row.keyListed:
			action = rescan + "模型重新出现后预览并确认重绑。"
		}
		row.addGap(view, "binding", "binding_invalid", "校内模型已发布，这条绑定无法使用。", action)
	}

	row.ProtocolDetails = []ProtocolCapability{}
	var bound []ProtocolCapability
	if row.keyListed && row.KeyEnabled {
		registered := RegisteredProtocols(entry)
		if len(registered) == 0 {
			row.addGap(view, "protocol", "protocol_missing", "当前 Key 没有登记网关可识别的协议。", rescan+" 核对官网支持的协议后再探活。")
		}
		protocols := append(append([]string{}, registered...), published...)
		slices.Sort(protocols)
		for _, protocol := range slices.Compact(protocols) {
			ready := row.BindingState == "ready" && slices.Contains(published, protocol)
			option := protocolCapability(protocol, row.Model, ready, probes[protocol])
			row.ProtocolDetails = append(row.ProtocolDetails, option)
			if ready {
				bound = append(bound, option)
			}
		}
	}
	whole := SummarizeProtocols(row.Model, row.ProtocolDetails)
	routed := SummarizeModelAvailability(row.Model, entmodel.StatusActive, bound)
	row.SupportedProtocols, row.AvailableProtocols = whole.Protocols, routed.AvailableProtocols
	row.ResponsesReady = slices.Contains(routed.Protocols, "openai_responses")
	row.ProbeState, row.RecommendedProtocol = whole.ProbeState, routed.RecommendedProtocol
	if row.BindingState == "ready" {
		row.ProbeState = routed.ProbeState
	}
	if row.BindingState == "not_published" && routeCurrent {
		row.RecommendedProtocol = whole.RecommendedProtocol
	}
	row.Availability = "not_published"
	switch {
	case row.BindingState == "ready":
		row.Availability, row.AvailabilityReason = routed.Availability, routed.AvailabilityReason
	case row.BindingState == "unverified":
		row.Availability = AvailabilityUnverified
		row.AvailabilityReason = "目录或分组待复核，请用当前 Key 重扫目录并探活。"
	case row.BindingState == "invalid":
		row.Availability = AvailabilityMaintenance
		row.AvailabilityReason = "维护中。校内模型已发布，这条绑定无法使用。"
	case whole.ProbeState == probeFailed:
		row.Availability = AvailabilityMaintenance
		row.AvailabilityReason = "维护中。当前线路的协议最近一次探活均失败。"
	case !row.keyListed || !row.KeyEnabled:
		row.Availability = "unavailable"
	}
	row.reportProbeGaps(view, whole)
	row.reportPriceGaps(view, routeCurrent)
	if row.ActionHint == "" {
		switch row.Availability {
		case "available":
			row.ActionHint = "可使用推荐协议接入。"
		case "unverified":
			row.ActionHint = "线路已绑定。请发起探活，确认推荐协议可以接通。"
		case "not_published":
			row.ActionHint = "请核对协议和报价后启用校内模型。"
		default:
			row.ActionHint = "检查渠道是否启用，使用当前 Key 重扫目录。"
		}
	}
	view.Capabilities = append(view.Capabilities, row.ModelCapability)
	return nil
}

func (row *capabilityRow) reportProbeGaps(view *BeichenCapabilities, registered ProtocolSummary) {
	var failed []string
	for _, protocol := range row.ProtocolDetails {
		if protocol.ProbeState == probeFailed {
			reason := protocol.Probe.ErrorCode
			switch reason {
			case "credential_unreadable":
				reason = "Key 无法解密"
			case "timeout":
				reason = "请求超时"
			case "network_error":
				reason = "网络请求失败"
			case "invalid_response":
				reason = "上游返回格式与协议不符"
			default:
				reason = fmt.Sprintf("HTTP %d", protocol.Probe.StatusCode)
			}
			failed = append(failed, EndpointForProtocol(protocol.Protocol).Label+"（"+reason+"）")
		}
	}
	if len(failed) == 0 {
		return
	}
	message := strings.Join(failed, "、") + " 最近一次探活失败。"
	action := "请检查所选 Key 的状态与对应协议，再重新探活。"
	if row.RecommendedProtocol != nil && slices.Contains(row.AvailableProtocols, *row.RecommendedProtocol) {
		action = "请先使用 " + EndpointForProtocol(*row.RecommendedProtocol).Label + "，再检查失败线路并重新探活。"
	} else if row.Availability == "maintenance" {
		message = "维护中。" + message
		if alternate := registered.RecommendedProtocol; alternate != nil && slices.Contains(registered.AvailableProtocols, *alternate) {
			action = EndpointForProtocol(*alternate).Label + " 已通过探活。请为校内模型绑定该协议，再检查失败线路。"
		}
	}
	row.addGap(view, "probe", "probe_failed", message, action)
}

func (row *capabilityRow) reportPriceGaps(view *BeichenCapabilities, routeCurrent bool) {
	switch {
	case !routeCurrent:
		row.PriceState = "unverified"
	case row.fixedPrice && row.priceMode == "manual":
		row.PriceState = "manual"
	case row.derivedPrice:
		row.PriceState = "derived"
		row.PriceBlocker = row.addGap(view, "price", "derived_price_requires_review", "历史反推价待核对。", "请核对正式官网报价或设置手动价。")
	case row.fixedPrice:
		row.PriceState = "confirmed"
	default:
		code, message := usage.BeichenPanelQuoteBlocker(row.priceTerms)
		switch {
		case code == "":
			row.PriceState = "conditional"
		default:
			row.PriceState = "missing"
			if row.priceExpired {
				code, message = "price_snapshot_expired", "该模型的官网报价快照已过期。"
			}
			row.PriceBlocker = row.addGap(view, "price", code, message, "请同步该 Key 的官网报价，或在模型管理中设置手动价。")
		}
	}
	row.PriceCovered = row.PriceState == "confirmed" || row.PriceState == "manual" || row.PriceState == "conditional"
	switch row.MultiplierState {
	case "expired":
		row.addGap(view, "multiplier", "multiplier_expired", "渠道倍率已超过有效期或 Key 已换版。", "请重新同步该 Key 的账单倍率。")
	case "missing":
		row.addGap(view, "multiplier", "multiplier_missing", "当前渠道尚无有效倍率记录。", "请检查 Key 所属分组并同步账单倍率。")
	case "sync_failed":
		row.addGap(view, "multiplier", "multiplier_sync_failed", "最近一次账单同步失败，渠道倍率需要核对。", "请查看该 Key 的账单同步错误，恢复渠道后重新同步。")
	}
}
