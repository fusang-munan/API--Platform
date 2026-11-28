package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

// Both preview and publication use the same snapshot and route selection.
// Only a teacher's explicit action writes a model, bindings or governance audit.
type activationModel struct {
	name               string
	provider           uuid.UUID
	keys               []uuid.UUID
	defaults           map[string][]activationCandidate
	schools            map[uuid.UUID]*activationSchool
	multiSchoolTargets bool
	aliasConflict      bool
	diagnostics        map[string]int
}

type activationCandidate struct {
	schoolID         uuid.UUID
	fallbackSchoolID uuid.UUID
	unresolvedAlias  bool
	route            PreferredRoute
}

func (c activationCandidate) matchesSchool(id uuid.UUID) bool {
	if c.schoolID == id && !c.unresolvedAlias {
		return true
	}
	return c.fallbackSchoolID != uuid.Nil && c.fallbackSchoolID == id
}

type activationSchool struct {
	id                   uuid.UUID
	name, status         string
	multiplier           *int64
	hasActivationMapping bool
	websiteOnly          bool
	bindings             []activationBinding
}

type activationBinding struct {
	id                  uuid.UUID
	credential          *uuid.UUID
	route               PreferredRoute
	active, reachable   bool
	authFailedActiveKey bool
}

type activationWrite struct {
	id     uuid.UUID
	route  PreferredRoute
	before *PreferredRoute
}

type activationPlan struct {
	routes, northRoutes []PreferredRoute
	writes              []activationWrite
	blocker             CatalogBlocker
	ambiguous           bool
	catalogBlocked      bool
	foreignActive       bool
}

const (
	blockerNoCandidate    = "no_candidate"
	blockerMultiSchool    = "multi_school"
	blockerMultiCandidate = "multi_candidate"
	blockerAuthFailed     = "auth_failed"
	blockerPriceMissing   = "price_missing"

	ambiguousProtocolRoutes = "同一个校内模型的同一协议有多个候选线路，请先确认并绑定线路"
	ambiguousFirstRoutes    = "首次启用有多个候选上游线路，请先确认并绑定线路"
	authFailedRebindMessage = "原线路 Key 已被上游拒绝认证，请由教师显式重绑"
	noCandidateMessage      = "没有与唯一活跃校内目标匹配的北辰候选线路，请检查 Key 目录、分组和路线配置"
	multiSchoolMessage      = "该上游名称对应多个活跃校内模型，请指定校内模型 ID"
	pinnedRouteNotReady     = "原绑定线路尚未就绪，请同步该渠道或由教师明确重绑"

	diagnosticRows           = "rows"
	diagnosticAvailable      = "available"
	diagnosticCatalogCurrent = "catalog_current"
	diagnosticAliasMissing   = "alias_missing"
	diagnosticTargetMissing  = "target_missing"
	diagnosticKeyListed      = "key_listed"
	diagnosticGroupAllowed   = "group_allowed"
	diagnosticCandidateRows  = "candidate_rows"
	diagnosticMatchingSchool = "matching_school"
	diagnosticFallbackSchool = "fallback_school"
	diagnosticSchools        = "schools"
)

func newActivationDiagnostics() map[string]int {
	return map[string]int{
		diagnosticRows: 0, diagnosticAvailable: 0, diagnosticCatalogCurrent: 0,
		diagnosticAliasMissing: 0, diagnosticTargetMissing: 0, diagnosticKeyListed: 0, diagnosticGroupAllowed: 0,
		diagnosticCandidateRows: 0, diagnosticMatchingSchool: 0, diagnosticFallbackSchool: 0, diagnosticSchools: 0,
	}
}

func (m *activationModel) noCandidateDiagnostics() map[string]int {
	if m == nil || m.diagnostics == nil {
		return nil
	}
	details := make(map[string]int, len(m.diagnostics))
	for key, value := range m.diagnostics {
		details[key] = value
	}
	return details
}

func (m *activationModel) blockerDiagnostics(blocker CatalogBlocker, plan activationPlan, school *activationSchool) map[string]any {
	if blocker.Code != blockerNoCandidate {
		return nil
	}
	details := map[string]any{}
	for key, value := range m.noCandidateDiagnostics() {
		details[key] = value
	}
	details["north_ready"] = len(plan.northRoutes) > 0
	details["ambiguous"] = plan.ambiguous
	details["catalog_blocked"] = plan.catalogBlocked
	details["foreign_active"] = plan.foreignActive
	details["multiplier_set"] = school != nil && school.multiplier != nil && *school.multiplier > 0
	details["writes"] = len(plan.writes)
	details["routes"] = len(plan.routes)
	return details
}

func (m *activationModel) fallbackSchool() *activationSchool {
	if schools := m.selectableSchools(); len(schools) == 1 {
		target := schools[0]
		if !target.websiteOnly {
			return target
		}
	} else if len(schools) == 0 && len(m.schools) == 1 {
		// A sole disabled target can still repair its existing route.
		for _, school := range m.schools {
			if school.hasActivationMapping {
				return school
			}
		}
	}
	return nil
}

func (m *activationModel) applyFallbackSchools() {
	fallback := m.fallbackSchool()
	if fallback == nil {
		return
	}
	for _, candidates := range m.defaults {
		for i := range candidates {
			if candidates[i].schoolID == fallback.id {
				candidates[i].fallbackSchoolID = uuid.Nil
			} else {
				candidates[i].fallbackSchoolID = fallback.id
			}
		}
	}
}

func (m *activationModel) updateCandidateDiagnostics() {
	if m.diagnostics == nil {
		return
	}
	m.diagnostics[diagnosticCandidateRows] = 0
	m.diagnostics[diagnosticMatchingSchool] = 0
	m.diagnostics[diagnosticFallbackSchool] = 0
	m.diagnostics[diagnosticSchools] = len(m.selectableSchools())
	target := m.target("")
	for _, candidates := range m.defaults {
		for _, candidate := range candidates {
			m.diagnostics[diagnosticCandidateRows]++
			if target == nil || target.id == uuid.Nil {
				continue
			}
			if candidate.schoolID == target.id {
				m.diagnostics[diagnosticMatchingSchool]++
			}
			if candidate.fallbackSchoolID == target.id {
				m.diagnostics[diagnosticFallbackSchool]++
			}
		}
	}
}

func activationBlockerError(blocker CatalogBlocker, diagnostics map[string]any) error {
	details := map[string]any{"code": blocker.Code}
	if blocker.Code == blockerNoCandidate {
		for key, value := range diagnostics {
			details[key] = value
		}
	}
	return &domain.Error{Code: domain.CodeValidation, Message: blocker.Message, Details: details}
}

func (p *activationPlan) setBlocker(blocker CatalogBlocker) {
	if blocker.Code == blockerMultiCandidate {
		p.ambiguous = true
	}
	// An explicit rebind takes precedence over candidate selection and pricing.
	if p.blocker.Code == "" || blocker.Code == blockerAuthFailed ||
		blocker.Code == blockerMultiCandidate && p.blocker.Code == blockerNoCandidate {
		p.blocker = blocker
	}
}

func loadActivationCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID, source string) ([]*activationModel, error) {
	return readActivationCatalog(ctx, tx, provider, source, nil)
}

// Scope a school UUID request by upstream name so each name keeps all of its
// school targets and eligible candidates when deciding whether it is unique.
const activationSchoolSourcesSQL = `school_sources AS (
 SELECT k.model_id AS upstream
 FROM provider_model_catalog k
 CROSS JOIN LATERAL (SELECT k.credential_id,k.model_id::text AS model) a
 ` + joinModelAliasSQL + `
 JOIN models s ON s.id=$3 AND (s.model_id=alias.school_model::citext OR s.model_id=a.model::citext)
 WHERE k.provider_id=$1
 UNION
 SELECT btrim(b.upstream_model_name)::citext FROM model_bindings b
 WHERE b.provider_id=$1 AND b.model_id=$3 AND btrim(b.upstream_model_name)<>''
)`

const activationCatalogSourceAllowedSQL = `(k.status='active' AND (
  k.catalog_source IN ` + catalog.BeichenKeyCatalogSourcesSQL + ` OR (k.catalog_source='website' AND EXISTS (
    SELECT 1 FROM model_bindings b JOIN models s ON s.id=b.model_id
    WHERE b.provider_id=k.provider_id AND btrim(b.upstream_model_name)::citext=k.model_id
      AND (b.credential_id IS NULL OR b.credential_id=k.credential_id)
      AND b.status='active' AND s.status='active'
  ))
))`

func readActivationCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID, source string, schoolID *uuid.UUID) ([]*activationModel, error) {
	query := `
WITH ` + catalog.BeichenKeyEvidenceCTE + `, ` + activationSchoolSourcesSQL + `
SELECT k.model_id::text,k.raw_entry,p.name::text,c.id,c.label::text,k.catalog_source IN ` + catalog.BeichenKeyCatalogSourcesSQL + `,
 p.status='active' AND c.status='active' AND k.last_seen_at>=c.updated_at,
 ` + catalog.BeichenKeyListsModelSQL + `,
 ` + catalog.BeichenGroupAllowsRouteSQL + `,
 ` + catalog.BeichenKeyListsModelSQL + ` AND ` + catalog.BeichenGroupAllowsRouteSQL + ` AND octet_length(c.credential_ciphertext)>0,
 CASE WHEN billing.last_error IS NULL AND billing.last_success>=c.updated_at
   AND billing.last_success>statement_timestamp()-interval '16 minutes'
   AND billing.last_success>=e.evidence_since AND mv.stale_at IS NULL
   AND (e.group_id IS NULL OR mv.group_id=e.group_id)
 THEN mv.multiplier::text END, to_json(k.supported_protocols),
 alias_target.id, upstream_target.id, alias.school_model IS NOT NULL, COALESCE(to_json(blocked.protocols),'[]'::json)
FROM provider_model_catalog k JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
JOIN providers p ON p.id=c.provider_id
JOIN beichen_key_evidence e ON e.credential_id=c.id
CROSS JOIN LATERAL (SELECT k.credential_id,k.model_id::text AS model) a
` + joinModelAliasSQL + `
LEFT JOIN models alias_target ON alias_target.model_id=alias.school_model::citext
LEFT JOIN models upstream_target ON upstream_target.model_id=a.model::citext
LEFT JOIN LATERAL (
 SELECT array_agg(r.protocol) AS protocols FROM upstream_probes r
 WHERE r.provider_id=k.provider_id AND r.credential_id=k.credential_id AND r.model=k.model_id::text
   AND ` + catalog.BeichenCurrentProbeSQL + ` AND ` + catalog.BeichenProbeBlocksRouteSQL + `
) blocked ON true
LEFT JOIN beichen_datasets billing ON billing.credential_id=c.id AND billing.kind='billing'
LEFT JOIN LATERAL (SELECT multiplier,stale_at,group_id FROM beichen_multiplier_versions WHERE credential_id=c.id
 AND effective_at<=statement_timestamp()
 ORDER BY effective_at DESC,id DESC LIMIT 1) mv ON true
WHERE p.id=$1 AND ` + beichenProvider + ` AND ` + activationCatalogSourceAllowedSQL + `
 AND ($2::text='' OR k.model_id=$2::citext)
 AND ($3::uuid IS NULL OR k.model_id IN (SELECT upstream FROM school_sources))
ORDER BY lower(k.model_id::text),c.id`
	if source != "" || schoolID != nil {
		query += " FOR SHARE OF p,c,k"
	}
	rows, err := tx.QueryContext(ctx, query, provider, source, schoolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []*activationModel
	index := map[string]*activationModel{}
	diagnostics := map[string]map[string]int{}
	conflictTargets := map[string][][2]uuid.UUID{}
	diagnosticsFor := func(source string) map[string]int {
		name := strings.ToLower(source)
		if diagnostics[name] == nil {
			diagnostics[name] = newActivationDiagnostics()
		}
		return diagnostics[name]
	}
	modelFor := func(source string) *activationModel {
		name := strings.ToLower(source)
		if m := index[name]; m != nil {
			return m
		}
		m := &activationModel{name: source, provider: provider, defaults: map[string][]activationCandidate{}, schools: map[uuid.UUID]*activationSchool{}, diagnostics: diagnosticsFor(source)}
		index[name] = m
		models = append(models, m)
		return m
	}
	rates := map[uuid.UUID]*json.Number{}
	var legacyPublished bool
	for rows.Next() {
		var modelID string
		var raw, protocols, blocked []byte
		var providerName, label string
		var key uuid.UUID
		var aliasTargetID, upstreamTargetID *uuid.UUID
		var sourceKey, available, keyListed, groupAllowed, catalogCurrent, aliased bool
		var rate *string
		if err := rows.Scan(&modelID, &raw, &providerName, &key, &label, &sourceKey, &available, &keyListed, &groupAllowed, &catalogCurrent, &rate,
			&protocols, &aliasTargetID, &upstreamTargetID, &aliased, &blocked); err != nil {
			return nil, err
		}
		diagnostic := diagnosticsFor(modelID)
		diagnostic[diagnosticRows]++
		if available {
			diagnostic[diagnosticAvailable]++
		}
		if catalogCurrent {
			diagnostic[diagnosticCatalogCurrent]++
		}
		if keyListed {
			diagnostic[diagnosticKeyListed]++
		}
		if groupAllowed {
			diagnostic[diagnosticGroupAllowed]++
		}
		if aliased && aliasTargetID == nil {
			diagnostic[diagnosticAliasMissing]++
		}
		if aliasTargetID == nil && upstreamTargetID == nil {
			diagnostic[diagnosticTargetMissing]++
		}
		if aliasTargetID != nil && upstreamTargetID != nil && *aliasTargetID != *upstreamTargetID {
			name := strings.ToLower(modelID)
			conflictTargets[name] = append(conflictTargets[name], [2]uuid.UUID{*aliasTargetID, *upstreamTargetID})
		}
		if available && rate != nil {
			n := json.Number(*rate)
			rates[key] = &n
		}
		if !sourceKey {
			legacyPublished = legacyPublished || available
			continue
		}
		m := modelFor(modelID)
		m.keys = append(m.keys, key)
		if !keyListed || !available || !catalogCurrent {
			continue
		}
		entry, err := storedCatalogEntry(modelID, raw, protocols)
		if err != nil {
			return nil, err
		}
		var blockedProtocols []string
		if err := json.Unmarshal(blocked, &blockedProtocols); err != nil {
			return nil, err
		}
		for _, protocol := range catalog.RegisteredProtocols(entry) {
			if slices.Contains(blockedProtocols, protocol) {
				continue
			}
			keyID := key
			targetID := aliasTargetID
			if targetID == nil {
				targetID = upstreamTargetID
			}
			candidate := activationCandidate{unresolvedAlias: aliased && targetID == nil, route: PreferredRoute{UpstreamModel: modelID, ProviderName: providerName, CredentialLabel: &label,
				Adapter: protocol, ProviderID: provider, CredentialID: &keyID, Priority: catalog.DefaultBindingPriority(modelbinding.Adapter(protocol)),
				State: "candidate", EffectiveMultiplier: rates[key]}}
			if targetID != nil {
				candidate.schoolID = *targetID
			}
			m.defaults[protocol] = append(m.defaults[protocol], candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(models) == 0 && !legacyPublished && source != "" && schoolID == nil {
		return models, nil
	}
	// An unpinned binding can still display a reachable Key during transition.
	// Publication requires evidence for the first Key the gateway will pick.
	bound, err := tx.QueryContext(ctx, `
WITH `+catalog.BeichenKeyEvidenceCTE+`, `+activationSchoolSourcesSQL+`, north AS (
 SELECT DISTINCT k.model_id FROM provider_model_catalog k
 WHERE k.provider_id=$1 AND ($2::text='' OR k.model_id=$2::citext)
   AND ((k.status='active' AND k.catalog_source IN `+catalog.BeichenKeyCatalogSourcesSQL+`)
     OR ($2::text='' AND EXISTS (SELECT 1 FROM models s WHERE s.model_id=k.model_id)))
 UNION
 SELECT btrim(b.upstream_model_name)::citext FROM model_bindings b JOIN models s ON s.id = b.model_id
 WHERE b.provider_id=$1 AND s.status='active' AND btrim(b.upstream_model_name)<>''
   AND ($2::text='' OR btrim(b.upstream_model_name)::citext=$2::citext)
), school_names AS (
 SELECT n.model_id AS upstream,alias_target.id,k.status='active' AND k.catalog_source IN `+catalog.BeichenKeyCatalogSourcesSQL+` AS key_or_binding,
   k.catalog_source='website' AS website_source
 FROM north n JOIN provider_model_catalog k ON k.provider_id=$1 AND k.model_id=n.model_id
 CROSS JOIN LATERAL (SELECT k.credential_id,k.model_id::text AS model) a
 `+joinModelAliasSQL+`
 JOIN models alias_target ON alias_target.model_id=alias.school_model::citext
 WHERE (k.status='active' AND k.catalog_source IN `+catalog.BeichenKeyCatalogSourcesSQL+`) OR $2::text=''
 UNION
 SELECT n.model_id AS upstream,upstream_target.id,k.status='active' AND k.catalog_source IN `+catalog.BeichenKeyCatalogSourcesSQL+` AS key_or_binding,
   k.catalog_source='website' AS website_source
 FROM north n JOIN provider_model_catalog k ON k.provider_id=$1 AND k.model_id=n.model_id
 CROSS JOIN LATERAL (SELECT k.credential_id,k.model_id::text AS model) a
 JOIN models upstream_target ON upstream_target.model_id=a.model::citext
 WHERE (k.status='active' AND k.catalog_source IN `+catalog.BeichenKeyCatalogSourcesSQL+`) OR $2::text=''
 UNION ALL
 SELECT n.model_id,b.model_id,true,false FROM north n JOIN model_bindings b ON b.provider_id=$1 AND lower(btrim(b.upstream_model_name))=lower(n.model_id::text)
), names AS (
 SELECT upstream,id,bool_or(key_or_binding) AS key_or_binding,bool_or(website_source) AS website_source FROM school_names
 WHERE $3::uuid IS NULL OR upstream IN (SELECT upstream FROM school_sources)
 GROUP BY upstream,id
)
SELECT n.upstream::text,m.id,m.model_id::text,m.status,m.credit_multiplier_milli,n.key_or_binding,n.website_source AND NOT n.key_or_binding,b.id,b.adapter,b.upstream_model_name,
 b.provider_id,p.name::text,b.priority,b.status='active',b.credential_id,
 COALESCE(b.credential_id IS NOT NULL AND bc.status='active' AND bc.retired_at IS NULL AND (
   `+catalog.BeichenCatalogAuthFailureSQL+` OR EXISTS (
     SELECT 1 FROM upstream_probes r
     WHERE r.provider_id=b.provider_id AND r.credential_id=b.credential_id
       AND btrim(r.model)::citext=btrim(b.upstream_model_name)::citext
       AND r.protocol=b.adapter AND `+catalog.BeichenCurrentProbeSQL+`
       AND `+catalog.BeichenProbeAuthFailureSQL+`
   )
 ),false),
 p.status='active' AND COALESCE(picked.reachable AND (b.provider_id<>$1 OR picked.catalog_current),false),
 CASE WHEN picked.reachable AND (b.credential_id IS NOT NULL OR b.provider_id=$1 OR picked.count=1) THEN picked.id END,
 CASE WHEN picked.reachable AND (b.credential_id IS NOT NULL OR b.provider_id=$1 OR picked.count=1) THEN picked.label::text END
FROM names n JOIN models m ON m.id=n.id
LEFT JOIN model_bindings b ON b.model_id=m.id LEFT JOIN providers p ON p.id=b.provider_id
LEFT JOIN provider_credentials bc ON bc.id=b.credential_id AND bc.provider_id=b.provider_id
LEFT JOIN beichen_key_evidence e ON e.credential_id=b.credential_id
LEFT JOIN LATERAL (
 SELECT c.id,c.label,count(*) OVER() AS count,
   (b.provider_id<>$1 OR k.last_seen_at>=c.updated_at) AS reachable,
   (`+catalog.BeichenKeyListsModelSQL+` AND `+catalog.BeichenGroupAllowsRouteSQL+`
     AND octet_length(c.credential_ciphertext)>0 AND NOT EXISTS (
       SELECT 1 FROM upstream_probes r
       WHERE r.provider_id=k.provider_id AND r.credential_id=k.credential_id AND r.model=k.model_id::text
         AND r.protocol=b.adapter AND `+catalog.BeichenCurrentProbeSQL+` AND `+catalog.BeichenProbeBlocksRouteSQL+`
     )) AS catalog_current
 FROM provider_model_catalog k
 JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
 LEFT JOIN beichen_key_evidence e ON e.credential_id=c.id
 WHERE k.provider_id=b.provider_id AND k.model_id=btrim(b.upstream_model_name)::citext AND k.status='active' AND c.status='active'
   AND (b.credential_id IS NULL OR c.id=b.credential_id)
 ORDER BY c.id LIMIT 1
) picked ON true
ORDER BY n.upstream,m.id,b.priority,b.id`, provider, source, schoolID)
	if err != nil {
		return nil, err
	}
	defer bound.Close()
	for bound.Next() {
		var source string
		var school activationSchool
		var id, owner, key *uuid.UUID
		var adapter, upstream, providerName, label *string
		var priority *int
		var active, authFailedActiveKey, reachable *bool
		var bindingKey *uuid.UUID
		if err := bound.Scan(&source, &school.id, &school.name, &school.status, &school.multiplier, &school.hasActivationMapping, &school.websiteOnly, &id, &adapter, &upstream, &owner, &providerName,
			&priority, &active, &bindingKey, &authFailedActiveKey, &reachable, &key, &label); err != nil {
			return nil, err
		}
		m := modelFor(source)
		if m.schools[school.id] == nil {
			m.schools[school.id] = &school
		}
		if id == nil {
			continue
		}
		r := PreferredRoute{SchoolModel: school.name, UpstreamModel: *upstream, Adapter: *adapter, ProviderID: *owner, ProviderName: *providerName,
			Priority: *priority, CredentialID: key, CredentialLabel: label, State: "candidate"}
		if key != nil {
			r.EffectiveMultiplier = rates[*key]
		}
		s := m.schools[school.id]
		s.bindings = append(s.bindings, activationBinding{
			id: *id, credential: bindingKey, route: r, active: *active, reachable: *reachable,
			authFailedActiveKey: *authFailedActiveKey,
		})
	}
	if err := bound.Err(); err != nil {
		return nil, err
	}
	for _, m := range models {
		for _, pair := range conflictTargets[strings.ToLower(m.name)] {
			alias, upstream := m.schools[pair[0]], m.schools[pair[1]]
			if alias != nil && upstream != nil && alias.selectable() && upstream.selectable() {
				m.aliasConflict = true
				break
			}
		}
	}
	// A sole selectable target receives every default candidate, including
	// candidates that still point at an older school UUID.
	for _, m := range models {
		m.applyFallbackSchools()
		m.updateCandidateDiagnostics()
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].name) < strings.ToLower(models[j].name) })
	return models, nil
}

func (m *activationModel) target(name string) *activationSchool {
	if name != "" {
		if id, err := uuid.Parse(name); err == nil && m.schools[id] != nil {
			return m.schools[id]
		}
		for _, s := range m.schools {
			if strings.EqualFold(name, s.name) {
				return s
			}
		}
		if len(m.schools) != 0 || !strings.EqualFold(name, m.name) {
			return nil
		}
	}
	if live := m.selectableSchools(); len(live) == 1 {
		return live[0]
	}
	switch len(m.schools) {
	case 0:
		return &activationSchool{name: m.name, status: "not_configured"}
	case 1:
		// An explicitly disabled model with its own routes can still be enabled.
		for _, s := range m.schools {
			return s
		}
	}
	return nil
}

func (s *activationSchool) selectable() bool {
	return s.status == "active" || s.status == "pending_configuration"
}

func (m *activationModel) selectableSchools() []*activationSchool {
	schools := make([]*activationSchool, 0, len(m.schools))
	for _, school := range m.schools {
		if school.selectable() {
			schools = append(schools, school)
		}
	}
	return schools
}

func (m *activationModel) targetBlocker(selector string) CatalogBlocker {
	if selector == "" && len(m.selectableSchools()) > 1 {
		return CatalogBlocker{blockerMultiSchool, multiSchoolMessage}
	}
	return CatalogBlocker{blockerNoCandidate, noCandidateMessage}
}

func (s *activationSchool) published() bool {
	return s.status == "active" && s.multiplier != nil && *s.multiplier > 0
}

func (m *activationModel) alreadyPublished(s *activationSchool) bool {
	return s.published() && slices.ContainsFunc(s.bindings, func(b activationBinding) bool {
		return b.active && b.route.ProviderID == m.provider
	})
}

// Overview presents one directory row per confirmed school identity. Keeping
// the activation model grouped by upstream name preserves preview actions.
func (m *activationModel) catalogViews() []*activationModel {
	schools := m.selectableSchools()
	if len(schools) == 0 {
		schools = slices.Collect(maps.Values(m.schools))
	}
	if len(schools) <= 1 && len(schools) == len(m.schools) {
		return []*activationModel{m}
	}
	views := make([]*activationModel, 0, len(schools))
	slices.SortFunc(schools, func(a, b *activationSchool) int { return strings.Compare(a.name, b.name) })
	for _, school := range schools {
		view := *m
		view.schools = map[uuid.UUID]*activationSchool{school.id: school}
		view.multiSchoolTargets = len(schools) > 1
		views = append(views, &view)
	}
	return views
}

func (m *activationModel) plan(s *activationSchool) activationPlan {
	var plan activationPlan
	type choice struct {
		route PreferredRoute
		rank  int
	}
	choices := map[string]choice{}
	hasNorth := false
	lastPriority := map[string]int{}
	for _, b := range s.bindings {
		r := b.route
		north := r.ProviderID == m.provider
		if north {
			hasNorth = true
		}
		if r.Priority > lastPriority[r.Adapter] {
			lastPriority[r.Adapter] = r.Priority
		}
		if b.active && !north {
			plan.foreignActive = true
		}
		rank := 0
		if north && b.active {
			switch {
			case b.authFailedActiveKey:
				plan.setBlocker(CatalogBlocker{blockerAuthFailed, authFailedRebindMessage})
			case !b.reachable && b.credential != nil:
				next, blocker := m.repairRoute(s, r.Adapter, r.UpstreamModel)
				if blocker.Code != "" {
					plan.setBlocker(blocker)
					plan.catalogBlocked = true
				} else {
					next.Priority = r.Priority
					before := r
					before.CredentialID = b.credential
					plan.writes = append(plan.writes, activationWrite{id: b.id, route: next, before: &before})
					plan.northRoutes = append(plan.northRoutes, next)
					r, rank = next, 2
				}
			case !b.reachable && !s.published():
				// Activation would also publish this retained, unpinned binding.
				plan.setBlocker(CatalogBlocker{blockerNoCandidate, pinnedRouteNotReady})
				plan.catalogBlocked = true
			}
		}
		if b.active && b.reachable {
			rank = 2
			if s.status == "active" {
				r.State = "ready"
				rank = 3
			}
			if north {
				plan.northRoutes = append(plan.northRoutes, r)
			}
		}
		// Bindings arrive in gateway priority and ID order.
		if existing, ok := choices[r.Adapter]; !ok || rank > existing.rank {
			choices[r.Adapter] = choice{r, rank}
		}
	}
	// Configure new North routes only for this school identity. An existing
	// North binding keeps its Key and protocol until the teacher rebinds it.
	if !hasNorth {
		defaults, blocker := m.defaultRoutes(s)
		if blocker.Code != "" {
			plan.setBlocker(blocker)
		}
		for _, r := range defaults {
			adapter := r.Adapter
			if c, exists := choices[adapter]; exists {
				if lastPriority[adapter] == math.MaxInt32 {
					continue
				}
				r.Priority = max(r.Priority, lastPriority[adapter]+1)
				if c.rank == 0 {
					choices[adapter] = choice{r, 2}
				}
			} else {
				choices[adapter] = choice{r, 2}
			}
			plan.writes = append(plan.writes, activationWrite{route: r})
			plan.northRoutes = append(plan.northRoutes, r)
		}
	}
	if s.status == "inactive" && len(m.selectableSchools()) > 0 {
		plan.setBlocker(CatalogBlocker{blockerNoCandidate, "所选校内模型已停用，请选择同名的当前校内模型"})
	}
	if len(plan.northRoutes) == 0 {
		plan.setBlocker(CatalogBlocker{blockerNoCandidate, noCandidateMessage})
	}
	if plan.foreignActive && (s.multiplier == nil || *s.multiplier <= 0) {
		plan.setBlocker(CatalogBlocker{blockerPriceMissing, "校内模型的其他供应商线路缺少可计算报价，请先设置校内倍率"})
	}
	for _, adapter := range slices.Sorted(maps.Keys(choices)) {
		plan.routes = append(plan.routes, choices[adapter].route)
	}
	return plan
}

// repairRoute selects a replacement Key only when the school model has one
// distinct candidate for the protocol. The teacher still has to preview and
// submit the existing activation action; ambiguous candidates stay blocked.
func (m *activationModel) repairRoute(s *activationSchool, adapter, upstream string) (PreferredRoute, CatalogBlocker) {
	var routes []PreferredRoute
	for _, candidate := range m.defaults[adapter] {
		if !candidate.matchesSchool(s.id) || candidate.fallbackSchoolID != uuid.Nil &&
			!strings.EqualFold(candidate.route.UpstreamModel, upstream) {
			continue
		}
		routes = append(routes, candidate.route)
	}
	if len(routes) == 0 {
		return PreferredRoute{}, CatalogBlocker{blockerNoCandidate, pinnedRouteNotReady}
	}
	picked, blocker := selectActivationRoute(routes, s.name, upstream)
	if blocker.Code != "" {
		return PreferredRoute{}, blocker
	}
	picked.SchoolModel = s.name
	return picked, CatalogBlocker{}
}

func (m *activationModel) defaultRoutes(s *activationSchool) ([]PreferredRoute, CatalogBlocker) {
	var routes []PreferredRoute
	unique := uuid.Nil
	if !m.multiSchoolTargets {
		if fallback := m.fallbackSchool(); fallback != nil {
			unique = fallback.id
		}
	}
	for _, adapter := range slices.Sorted(maps.Keys(m.defaults)) {
		var candidates []PreferredRoute
		for _, candidate := range m.defaults[adapter] {
			if !(candidate.schoolID == s.id && !candidate.unresolvedAlias) &&
				!(candidate.fallbackSchoolID != uuid.Nil && candidate.fallbackSchoolID == s.id) &&
				!(candidate.schoolID == uuid.Nil && !m.multiSchoolTargets && unique != uuid.Nil && unique == s.id) {
				continue
			}
			route := candidate.route
			route.SchoolModel = s.name
			candidates = append(candidates, route)
		}
		if len(candidates) != 0 {
			selected, blocker := selectActivationRoute(candidates, s.name, "")
			if blocker.Code != "" {
				return nil, blocker
			}
			routes = append(routes, selected)
		}
	}
	// A single Key can supply several protocols for the first publication.
	// A second Key needs explicit teacher selection.
	if len(routes) > 1 && slices.ContainsFunc(routes[1:], func(route PreferredRoute) bool {
		return !sameRouteKey(route.CredentialID, routes[0].CredentialID)
	}) {
		return nil, CatalogBlocker{blockerMultiCandidate, ambiguousFirstRoutes}
	}
	return routes, CatalogBlocker{}
}

func selectActivationRoute(routes []PreferredRoute, schoolName, preferredUpstream string) (PreferredRoute, CatalogBlocker) {
	if len(routes) == 0 {
		return PreferredRoute{}, CatalogBlocker{}
	}
	for _, route := range routes[1:] {
		if !sameRouteKey(route.CredentialID, routes[0].CredentialID) {
			return PreferredRoute{}, CatalogBlocker{blockerMultiCandidate, ambiguousProtocolRoutes}
		}
	}
	for _, preferred := range []string{preferredUpstream, schoolName} {
		if preferred == "" {
			continue
		}
		matching := make([]PreferredRoute, 0, len(routes))
		for _, route := range routes {
			if strings.EqualFold(strings.TrimSpace(route.UpstreamModel), strings.TrimSpace(preferred)) {
				matching = append(matching, route)
			}
		}
		if len(matching) != 0 {
			routes = matching
			break
		}
	}
	selected := routes[0]
	for _, route := range routes[1:] {
		if route.Priority < selected.Priority {
			selected = route
		}
	}
	for _, route := range routes {
		if route.Priority == selected.Priority && !strings.EqualFold(strings.TrimSpace(route.UpstreamModel), strings.TrimSpace(selected.UpstreamModel)) {
			return PreferredRoute{}, CatalogBlocker{blockerMultiCandidate, ambiguousProtocolRoutes}
		}
	}
	return selected, CatalogBlocker{}
}

type activationPrice struct {
	blocker     *CatalogBlocker
	conditional bool
}

func (price activationPrice) ready() bool {
	return price.blocker == nil
}

func routePriceReadiness(fixed bool, terms json.RawMessage) activationPrice {
	if fixed {
		return activationPrice{}
	}
	code, message := usage.BeichenPanelQuoteBlocker(terms)
	if code == "" {
		return activationPrice{conditional: true}
	}
	return activationPrice{
		blocker:     &CatalogBlocker{Code: code, Message: message},
		conditional: conditionalPanelPricing(terms),
	}
}

// derivedPriceSuggestion returns the newest usage-derived upstream rates for the
// first usable North route. Official quotes always win; this only fills the
// "no price" gap with evidence the teacher can review and apply manually.
func derivedPriceSuggestion(ctx context.Context, tx *sql.Tx, routes []PreferredRoute) *SuggestedRates {
	for _, route := range routes {
		if route.CredentialID == nil || *route.CredentialID == uuid.Nil || strings.TrimSpace(route.UpstreamModel) == "" {
			continue
		}
		var suggestion SuggestedRates
		err := tx.QueryRowContext(ctx, `
SELECT input_rate, output_rate, COALESCE(cache_read_rate, 0), COALESCE(cache_write_rate, 0), effective_at
FROM beichen_price_versions
WHERE credential_id = $1 AND model = $2 AND track = 'upstream' AND source = '`+derivedPriceSource+`'
ORDER BY effective_at DESC, id DESC LIMIT 1`,
			*route.CredentialID, route.UpstreamModel).
			Scan(&suggestion.InputRate, &suggestion.OutputRate, &suggestion.CacheReadRate, &suggestion.CacheWriteRate, &suggestion.ObservedAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil
		}
		suggestion.Source = derivedPriceSource
		return &suggestion
	}
	return nil
}

func (model Model) activationPrice() activationPrice {
	// Overview synthesizes display terms for derived prices. They cannot settle new calls.
	if model.PriceSource == derivedPriceSource && model.Mode != "manual" {
		return routePriceReadiness(false, nil)
	}
	return model.priceReadiness
}

// Check the North routes that can actually be used after publication,
// including fallbacks hidden by a higher-priority route in the preview.
func (p activationPlan) priceReadiness(ctx context.Context, tx *sql.Tx, observed map[string]map[uuid.UUID]*Model) (activationPrice, error) {
	type routeKey struct {
		credential uuid.UUID
		model      string
	}
	seen := make(map[routeKey]bool)
	var readiness activationPrice
	for _, route := range p.northRoutes {
		key := routeKey{*route.CredentialID, route.UpstreamModel}
		if seen[key] {
			continue
		}
		seen[key] = true
		record := observed[strings.ToLower(key.model)][key.credential]
		var price activationPrice
		if record != nil && record.Model == key.model {
			price = record.activationPrice()
		} else {
			var fixed bool
			var terms []byte
			if err := tx.QueryRowContext(ctx, activationPriceSQL, route.ProviderID, key.credential, key.model).Scan(&fixed, &terms); err != nil {
				return activationPrice{}, err
			}
			price = routePriceReadiness(fixed, terms)
		}
		if !price.ready() {
			return price, nil
		}
		if price.conditional {
			readiness = price
		}
	}
	return readiness, nil
}

const activationPriceSQL = `
WITH ` + catalog.BeichenKeyEvidenceCTE + `
SELECT COALESCE(` + catalog.BeichenFixedPriceSQL + `,false),
  CASE WHEN version.source IS DISTINCT FROM '` + derivedPriceSource + `'
       AND ` + catalog.BeichenPanelQuoteCurrentSQL + `
       THEN snap.model_pricing -> ($3::text) END
FROM provider_credentials c
JOIN beichen_key_evidence e ON e.credential_id=c.id
CROSS JOIN LATERAL beichen_price_at(c.id,$3,statement_timestamp()) price
LEFT JOIN beichen_price_versions version ON version.id=price.upstream_version
LEFT JOIN LATERAL (
  SELECT effective_at,expires_at,credential_ciphertext_sha256,credential_revision,group_id,model_pricing
  FROM beichen_panel_snapshots
  WHERE credential_id=c.id AND effective_at<=statement_timestamp()
  ORDER BY effective_at DESC LIMIT 1
) snap ON true
WHERE c.id=$2`

func (m *activationModel) overview(target *activationSchool, plan activationPlan) CatalogModel {
	result := CatalogModel{Model: m.name, CredentialIDs: append([]uuid.UUID{}, m.keys...), SchoolStatus: "not_configured", PreferredRoutes: []PreferredRoute{}}
	if target != nil {
		result.SchoolStatus = target.status
		if target.id != uuid.Nil {
			result.SchoolModel = target.name
			id := target.id
			result.SchoolModelID = &id
		}
		result.CanEnable = plan.blocker.Code == "" && (!target.published() || len(plan.writes) > 0)
		result.PreferredRoutes = append(result.PreferredRoutes, plan.routes...)
	}
	for i := range result.PreferredRoutes {
		r := &result.PreferredRoutes[i]
		if r.EffectiveMultiplier != nil {
			r.EffectiveMultiplierText = r.EffectiveMultiplier.String()
		}
	}
	return result
}

type BatchEnabledModel struct {
	Model   string    `json:"model"`
	ModelID uuid.UUID `json:"model_id"`
	Message string    `json:"message,omitempty"`
}

type BatchSkippedModel struct {
	Model          string          `json:"model"`
	Reason         string          `json:"reason"`
	Code           string          `json:"code,omitempty"`
	Details        map[string]any  `json:"details,omitempty"`
	Message        string          `json:"message"`
	SuggestedRates *SuggestedRates `json:"suggested_rates,omitempty"`
}

// SuggestedRates carries a usage-derived upstream price so a missing official
// quote is actionable: the teacher can accept these numbers as a manual price.
type SuggestedRates struct {
	InputRate      int64     `json:"input_rate"`
	OutputRate     int64     `json:"output_rate"`
	CacheReadRate  int64     `json:"cache_read_rate"`
	CacheWriteRate int64     `json:"cache_write_rate"`
	ObservedAt     time.Time `json:"observed_at"`
	Source         string    `json:"source"`
}

type BatchEnableResult struct {
	Enabled []BatchEnabledModel `json:"enabled"`
	Skipped []BatchSkippedModel `json:"skipped"`
}

// EnableModelsBatch 逐校内模型提交。请求项可以是校内模型 UUID 或可唯一定位的上游名称。
func (s *Service) EnableModelsBatch(ctx context.Context, provider, actor uuid.UUID, models []string) (BatchEnableResult, error) {
	result := BatchEnableResult{Enabled: []BatchEnabledModel{}, Skipped: []BatchSkippedModel{}}
	if provider == uuid.Nil || actor == uuid.Nil || len(models) == 0 || len(models) > 200 {
		return result, invalid("需要有效的供应商、教师 ID 和 1 至 200 个模型")
	}
	unique := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if !validModel(model) {
			return result, invalid("请指定有效的北辰目录模型")
		}
		key := strings.ToLower(model)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, model)
		}
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM providers p WHERE p.id=$1 AND `+beichenProvider+`)`, provider).Scan(&exists); err != nil {
		return result, unavailable("读取北辰供应商失败", err)
	}
	if !exists {
		return result, domain.NewError(domain.CodeNotFound, "北辰供应商不存在")
	}
	seenSchools := make(map[uuid.UUID]bool, len(unique))
	for _, model := range unique {
		var enabled BatchEnabledModel
		var skipped *BatchSkippedModel
		var err error
		for attempt := 0; ; attempt++ {
			enabled, skipped, err = s.enableBatchModel(ctx, provider, actor, model, seenSchools)
			if attempt >= 2 || !activationRetryable(err) {
				break
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			message := "启用模型失败，请重试或联系管理员"
			var appErr *domain.Error
			if errors.As(err, &appErr) {
				message = appErr.Message
			}
			skipped = &BatchSkippedModel{Model: model, Reason: "enable_failed", Message: message}
		}
		if skipped != nil {
			result.Skipped = append(result.Skipped, *skipped)
		} else {
			result.Enabled = append(result.Enabled, enabled)
		}
	}
	return result, nil
}

func selectBatchActivation(ctx context.Context, tx *sql.Tx, provider uuid.UUID, selector string) (*activationModel, *activationSchool, error) {
	schoolID, parseErr := uuid.Parse(selector)
	if parseErr != nil || !strings.EqualFold(selector, schoolID.String()) {
		models, err := loadActivationCatalog(ctx, tx, provider, selector)
		if err != nil || len(models) != 1 {
			return nil, nil, err
		}
		return models[0], models[0].target(""), nil
	}

	models, err := readActivationCatalog(ctx, tx, provider, "", &schoolID)
	if err != nil {
		return nil, nil, err
	}
	var combined *activationModel
	var target *activationSchool
	for _, model := range models {
		school := model.schools[schoolID]
		if school == nil {
			continue
		}
		if combined == nil {
			combined = &activationModel{provider: provider, defaults: map[string][]activationCandidate{}, schools: map[uuid.UUID]*activationSchool{}, diagnostics: newActivationDiagnostics()}
			target = school
		}
		maps.Copy(combined.schools, model.schools)
		combined.aliasConflict = combined.aliasConflict || model.aliasConflict
		for key, value := range model.diagnostics {
			combined.diagnostics[key] += value
		}
		for protocol, routes := range model.defaults {
			combined.defaults[protocol] = append(combined.defaults[protocol], routes...)
		}
	}
	if combined == nil && len(models) == 1 {
		return models[0], nil, nil
	}
	return combined, target, nil
}

func (s *Service) enableBatchModel(ctx context.Context, provider, actor uuid.UUID, source string,
	seenSchools map[uuid.UUID]bool) (BatchEnabledModel, *BatchSkippedModel, error) {
	var enabled BatchEnabledModel
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return enabled, nil, err
	}
	defer tx.Rollback()
	m, target, err := selectBatchActivation(ctx, tx, provider, source)
	if err != nil {
		return enabled, nil, err
	}
	skip := func(reason, code, message string, diagnostic ...map[string]any) (BatchEnabledModel, *BatchSkippedModel, error) {
		if target != nil && target.id != uuid.Nil {
			seenSchools[target.id] = true
		}
		var details map[string]any
		if len(diagnostic) != 0 {
			details = diagnostic[0]
		}
		return enabled, &BatchSkippedModel{Model: source, Reason: reason, Code: code, Details: details, Message: message}, nil
	}
	if m == nil {
		return skip("not_found", "", "当前北辰目录中没有此模型")
	}
	if target == nil {
		blocker := m.targetBlocker("")
		return skip("route_not_ready", blocker.Code, blocker.Message, m.blockerDiagnostics(blocker, activationPlan{}, nil))
	}
	if m.aliasConflict {
		return skip("route_not_ready", blockerMultiSchool, multiSchoolMessage)
	}
	if seenSchools[target.id] {
		return skip("duplicate_school_model", "", "本批已处理同一个校内模型 ID")
	}
	plan := m.plan(target)
	if plan.blocker.Code != blockerAuthFailed && m.alreadyPublished(target) {
		return skip("already_active", "", "模型已启用")
	}
	if plan.blocker.Code != "" {
		return skip("route_not_ready", plan.blocker.Code, plan.blocker.Message, m.blockerDiagnostics(plan.blocker, plan, target))
	}
	price, err := plan.priceReadiness(ctx, tx, nil)
	if err != nil {
		return enabled, nil, err
	}
	if !price.ready() {
		_, skipped, _ := skip("no_price", blockerPriceMissing, price.blocker.Message)
		skipped.SuggestedRates = derivedPriceSuggestion(ctx, tx, plan.northRoutes)
		return enabled, skipped, nil
	}
	id, err := applyActivationPlan(ctx, tx, provider, actor, target, plan)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return enabled, nil, err
	}
	seenSchools[id] = true
	enabled = BatchEnabledModel{Model: source, ModelID: id}
	if price.conditional {
		enabled.Message = "按条件价自动结算"
	}
	return enabled, nil, nil
}

func activationRetryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}

// EnableModel 在事务内重新检查目录；并发首次启用使用新快照重试，保留已胜出的线路修改。
func (s *Service) EnableModel(ctx context.Context, provider, actor uuid.UUID, source, school string) (uuid.UUID, error) {
	if !validModel(source) || school != "" && !validModel(school) {
		return uuid.Nil, invalid("请指定有效的北辰目录模型")
	}
	for attempt := 0; ; attempt++ {
		id, err := s.enableModel(ctx, provider, actor, source, school)
		if attempt >= 2 || !activationRetryable(err) {
			return id, err
		}
	}
}

func (s *Service) enableModel(ctx context.Context, provider, actor uuid.UUID, source, school string) (uuid.UUID, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback()
	models, err := loadActivationCatalog(ctx, tx, provider, source)
	if err != nil {
		return uuid.Nil, err
	}
	if len(models) != 1 {
		return uuid.Nil, domain.NewError(domain.CodeNotFound, "当前北辰目录中没有此模型")
	}
	m := models[0]
	target := m.target(school)
	if target == nil {
		blocker := m.targetBlocker(school)
		return uuid.Nil, activationBlockerError(blocker, m.blockerDiagnostics(blocker, activationPlan{}, nil))
	}
	if m.aliasConflict {
		return uuid.Nil, activationBlockerError(CatalogBlocker{blockerMultiSchool, multiSchoolMessage}, nil)
	}
	plan := m.plan(target)
	if plan.blocker.Code != blockerAuthFailed && m.alreadyPublished(target) {
		return target.id, nil
	}
	if plan.blocker.Code != "" {
		return uuid.Nil, activationBlockerError(plan.blocker, m.blockerDiagnostics(plan.blocker, plan, target))
	}
	price, err := plan.priceReadiness(ctx, tx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	if !price.ready() {
		return uuid.Nil, activationBlockerError(CatalogBlocker{blockerPriceMissing, price.blocker.Message}, nil)
	}
	id, err := applyActivationPlan(ctx, tx, provider, actor, target, plan)
	if err == nil {
		err = tx.Commit()
	}
	return id, err
}

func applyActivationPlan(ctx context.Context, tx *sql.Tx, provider, actor uuid.UUID, target *activationSchool, plan activationPlan) (uuid.UUID, error) {
	var err error
	if target.id == uuid.Nil {
		err = tx.QueryRowContext(ctx, `INSERT INTO models(id,model_id,display_name,created_at,updated_at)
 VALUES(gen_random_uuid(),$1::text,$1::text,now(),now()) ON CONFLICT(model_id) DO NOTHING RETURNING id`, target.name).Scan(&target.id)
		if errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, domain.NewError(domain.CodeInvalidTransition, "校内模型已变化，请刷新后重新选择")
		}
		if err != nil {
			return uuid.Nil, err
		}
	}
	for _, write := range plan.writes {
		r := write.route
		if write.id == uuid.Nil {
			result, execErr := tx.ExecContext(ctx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
 VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,now(),now()) ON CONFLICT(model_id,provider_id,adapter) DO NOTHING`, target.id, provider, r.CredentialID, r.UpstreamModel, r.Adapter, r.Priority)
			if execErr != nil {
				return uuid.Nil, execErr
			}
			if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
				return uuid.Nil, domain.NewError(domain.CodeInvalidTransition, "绑定已变化，请刷新后重新选择")
			}
			continue
		}
		if write.before == nil {
			return uuid.Nil, domain.NewError(domain.CodeInvalidTransition, "绑定已变化，请刷新后重新选择")
		}
		result, execErr := tx.ExecContext(ctx, `
UPDATE model_bindings SET credential_id=$2,upstream_model_name=$3,updated_at=now()
WHERE id=$1 AND provider_id=$4 AND model_id=$5 AND adapter=$6 AND status='active'
  AND credential_id IS NOT DISTINCT FROM $7::uuid AND upstream_model_name=$8 AND priority=$9`,
			write.id, r.CredentialID, r.UpstreamModel, provider, target.id, r.Adapter,
			write.before.CredentialID, write.before.UpstreamModel, write.before.Priority)
		if execErr != nil {
			return uuid.Nil, execErr
		}
		if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
			return uuid.Nil, domain.NewError(domain.CodeInvalidTransition, "绑定已变化，请刷新后重新选择")
		}
	}
	// 正的中性倍率用于学生申请资格，北辰账本独立计算实际用量费用。
	if target.multiplier == nil || *target.multiplier <= 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO model_multiplier_audits(id,model_id,actor_user_id,old_multiplier_milli,new_multiplier_milli,reason,created_at)
 VALUES(gen_random_uuid(),$1,$2,$3,1000,'教师启用北辰目录模型，使用北辰基础计价',now())`, target.id, actor, target.multiplier)
		if err != nil {
			return uuid.Nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE models SET status='active',credit_multiplier_milli=CASE WHEN credit_multiplier_milli>0
 THEN credit_multiplier_milli ELSE 1000 END,updated_at=now() WHERE id=$1 AND (status<>'active' OR coalesce(credit_multiplier_milli,0)<=0)`, target.id)
	if err != nil {
		return uuid.Nil, err
	}
	return target.id, nil
}
