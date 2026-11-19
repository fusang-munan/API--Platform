package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

// Both preview and publication use the same snapshot and route selection.
// Only a teacher's explicit action writes a model, bindings or governance audit.
type activationModel struct {
	name     string
	provider uuid.UUID
	keys     []uuid.UUID
	defaults map[string]PreferredRoute
	schools  map[string]*activationSchool
}

type activationSchool struct {
	id           uuid.UUID
	name, status string
	multiplier   *int64
	bindings     []activationBinding
}

type activationBinding struct {
	id                uuid.UUID
	route             PreferredRoute
	active, reachable bool
}

type activationPlan struct {
	routes                    []PreferredRoute
	writes                    []activationBinding
	northReady, foreignActive bool
}

func loadActivationCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID, source string) ([]*activationModel, error) {
	query := `
SELECT k.model_id::text,k.raw_entry,p.name::text,c.id,c.label::text,
 p.status='active' AND c.status='active',
 CASE WHEN d.last_error IS NULL AND d.last_success>=c.updated_at
   AND d.last_success>statement_timestamp()-interval '16 minutes'
   AND (snap.expires_at IS NULL OR snap.expires_at>statement_timestamp())
 THEN mv.multiplier::text END
FROM provider_model_catalog k JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
JOIN providers p ON p.id=c.provider_id
LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='billing'
LEFT JOIN LATERAL (SELECT multiplier FROM beichen_multiplier_versions WHERE credential_id=c.id
 ORDER BY effective_at DESC,id DESC LIMIT 1) mv ON true
LEFT JOIN LATERAL (SELECT expires_at FROM beichen_panel_snapshots WHERE credential_id=c.id
 ORDER BY effective_at DESC LIMIT 1) snap ON true
WHERE p.id=$1 AND ` + beichenProvider + ` AND k.status='active' AND ($2::text='' OR k.model_id=$2::citext)
ORDER BY lower(k.model_id::text),c.id`
	if source != "" {
		query += " FOR SHARE OF p,c,k"
	}
	rows, err := tx.QueryContext(ctx, query, provider, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []*activationModel
	index := map[string]*activationModel{}
	rates := map[uuid.UUID]*json.Number{}
	for rows.Next() {
		var e catalog.ProviderEntry
		var raw []byte
		var providerName, label string
		var key uuid.UUID
		var available bool
		var rate *string
		if err := rows.Scan(&e.ID, &raw, &providerName, &key, &label, &available, &rate); err != nil {
			return nil, err
		}
		name := strings.ToLower(e.ID)
		m := index[name]
		if m == nil {
			m = &activationModel{name: e.ID, provider: provider, defaults: map[string]PreferredRoute{}, schools: map[string]*activationSchool{}}
			index[name] = m
			models = append(models, m)
		}
		m.keys = append(m.keys, key)
		if !available {
			continue
		}
		if err := json.Unmarshal(raw, &e.Raw); err != nil {
			return nil, err
		}
		if rate != nil {
			n := json.Number(*rate)
			rates[key] = &n
		}
		for _, adapter := range catalog.AdaptersFromUpstream(e) {
			if _, ok := m.defaults[string(adapter)]; ok {
				continue
			}
			m.defaults[string(adapter)] = PreferredRoute{SchoolModel: m.name, UpstreamModel: e.ID, ProviderName: providerName, CredentialLabel: &label,
				Adapter: string(adapter), ProviderID: provider, CredentialID: &key, Priority: catalog.DefaultBindingPriority(adapter), State: "candidate", EffectiveMultiplier: rates[key]}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return models, nil
	}
	bound, err := tx.QueryContext(ctx, `
WITH north AS (
 SELECT DISTINCT model_id FROM provider_model_catalog WHERE provider_id=$1 AND status='active' AND ($2::text='' OR model_id=$2::citext)
), names AS (
 SELECT n.model_id AS upstream,m.id FROM north n JOIN models m ON m.model_id=n.model_id
 UNION
 SELECT n.model_id,b.model_id FROM north n JOIN model_bindings b ON b.provider_id=$1 AND lower(btrim(b.upstream_model_name))=lower(n.model_id::text)
)
SELECT n.upstream::text,m.id,m.model_id::text,m.status,m.credit_multiplier_milli,b.id,b.adapter,b.upstream_model_name,
 b.provider_id,p.name::text,b.priority,b.status='active',p.status='active' AND picked.id IS NOT NULL,
 CASE WHEN b.credential_id IS NOT NULL OR b.provider_id=$1 OR picked.count=1 THEN picked.id END,
 CASE WHEN b.credential_id IS NOT NULL OR b.provider_id=$1 OR picked.count=1 THEN picked.label::text END
FROM names n JOIN models m ON m.id=n.id
LEFT JOIN model_bindings b ON b.model_id=m.id LEFT JOIN providers p ON p.id=b.provider_id
LEFT JOIN LATERAL (
 SELECT c.id,c.label,count(*) OVER() AS count FROM provider_model_catalog k
 JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
 WHERE k.provider_id=b.provider_id AND k.model_id=btrim(b.upstream_model_name)::citext AND k.status='active' AND c.status='active'
 AND (b.credential_id IS NULL OR c.id=b.credential_id) ORDER BY c.id LIMIT 1
) picked ON true
ORDER BY n.upstream,m.id,b.priority,b.id`, provider, source)
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
		var active, reachable *bool
		if err := bound.Scan(&source, &school.id, &school.name, &school.status, &school.multiplier, &id, &adapter, &upstream, &owner, &providerName,
			&priority, &active, &reachable, &key, &label); err != nil {
			return nil, err
		}
		m := index[strings.ToLower(source)]
		name := strings.ToLower(school.name)
		if m.schools[name] == nil {
			m.schools[name] = &school
		}
		if id == nil {
			continue
		}
		r := PreferredRoute{SchoolModel: school.name, UpstreamModel: *upstream, Adapter: *adapter, ProviderID: *owner, ProviderName: *providerName,
			Priority: *priority, CredentialID: key, CredentialLabel: label, State: "candidate"}
		if key != nil {
			r.EffectiveMultiplier = rates[*key]
		}
		s := m.schools[name]
		s.bindings = append(s.bindings, activationBinding{id: *id, route: r, active: *active, reachable: *reachable})
	}
	return models, bound.Err()
}

func (m *activationModel) target(name string) *activationSchool {
	if name != "" {
		if s := m.schools[strings.ToLower(name)]; s != nil {
			return s
		}
		if !strings.EqualFold(name, m.name) {
			return nil
		}
	}
	if s := m.schools[strings.ToLower(m.name)]; s != nil {
		return s
	}
	switch len(m.schools) {
	case 0:
		return &activationSchool{name: m.name, status: "not_configured"}
	case 1:
		for _, s := range m.schools {
			return s
		}
	}
	return nil
}

func (m *activationModel) plan(s *activationSchool) activationPlan {
	var plan activationPlan
	// The first publication uses the same default protocols shown in the preview.
	if len(s.bindings) == 0 {
		for _, r := range m.defaults {
			r.SchoolModel = s.name
			plan.writes = append(plan.writes, activationBinding{route: r})
			plan.routes = append(plan.routes, r)
		}
		plan.northReady = len(plan.routes) > 0
		return plan
	}
	type choice struct {
		route PreferredRoute
		rank  int
	}
	choices := map[string]choice{}
	hasNorth := false
	lastPriority := map[string]int{}
	for _, b := range s.bindings {
		r := b.route
		if r.ProviderID == m.provider {
			hasNorth = true
		}
		if r.Priority > lastPriority[r.Adapter] {
			lastPriority[r.Adapter] = r.Priority
		}
		ours := r.ProviderID == m.provider && strings.EqualFold(strings.TrimSpace(r.UpstreamModel), m.name)
		if b.active && r.ProviderID != m.provider {
			plan.foreignActive = true
		}
		rank := 0
		switch {
		case b.active && b.reachable:
			rank = 2
			if s.status == "active" {
				r.State = "ready"
				rank = 3
			}
			if ours {
				plan.northReady = true
			}
		case ours && b.active:
			if next, ok := m.defaults[r.Adapter]; ok {
				next.SchoolModel = s.name
				next.Priority = r.Priority
				r = next
				plan.writes = append(plan.writes, activationBinding{id: b.id, route: r})
				plan.northReady = true
				rank = 2
			}
		}
		// Bindings arrive in gateway priority and ID order.
		if existing, ok := choices[r.Adapter]; !ok || rank > existing.rank {
			choices[r.Adapter] = choice{r, rank}
		}
	}
	// For a same-name model owned solely by other suppliers, add North behind
	// their configured routes. Once North has bindings, respect its exact
	// protocol list, including every manually disabled binding.
	if !hasNorth && strings.EqualFold(s.name, m.name) {
		for adapter, r := range m.defaults {
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
			plan.writes = append(plan.writes, activationBinding{route: r})
			plan.northReady = true
		}
	}
	for _, v := range choices {
		plan.routes = append(plan.routes, v.route)
	}
	return plan
}

func (p activationPlan) allowed(s *activationSchool) bool {
	return p.northReady && (!p.foreignActive || s.multiplier != nil && *s.multiplier > 0)
}

func (m *activationModel) overview() CatalogModel {
	result := CatalogModel{Model: m.name, CredentialIDs: m.keys, SchoolStatus: "not_configured", PreferredRoutes: []PreferredRoute{}}
	target := m.target("")
	if target != nil {
		plan := m.plan(target)
		result.SchoolStatus = target.status
		result.SchoolModel = target.name
		result.CanEnable = plan.allowed(target) && (target.status != "active" || len(plan.writes) > 0 || target.multiplier == nil || *target.multiplier <= 0)
		if len(m.schools) == 0 {
			result.PreferredRoutes = append(result.PreferredRoutes, plan.routes...)
		}
	}
	for _, s := range m.schools {
		result.PreferredRoutes = append(result.PreferredRoutes, m.plan(s).routes...)
	}
	for i := range result.PreferredRoutes {
		r := &result.PreferredRoutes[i]
		if r.EffectiveMultiplier != nil {
			r.EffectiveMultiplierText = r.EffectiveMultiplier.String()
		}
	}
	sort.Slice(result.PreferredRoutes, func(i, j int) bool {
		a, b := result.PreferredRoutes[i], result.PreferredRoutes[j]
		if a.SchoolModel != b.SchoolModel {
			return a.SchoolModel < b.SchoolModel
		}
		return a.Adapter < b.Adapter
	})
	return result
}

// EnableModel rechecks the snapshot under a transaction before applying it.
// Concurrent first clicks may both see no model. Retry a serialization conflict
// with a fresh snapshot, preserving any route or governance change that won.
func (s *Service) EnableModel(ctx context.Context, provider, actor uuid.UUID, source, school string) (uuid.UUID, error) {
	if !validModel(source) || school != "" && !validModel(school) {
		return uuid.Nil, invalid("请指定有效的北辰目录模型")
	}
	for attempt := 0; ; attempt++ {
		id, err := s.enableModel(ctx, provider, actor, source, school)
		var pg *pgconn.PgError
		if attempt >= 2 || !errors.As(err, &pg) || pg.Code != "40001" && pg.Code != "40P01" {
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
		return uuid.Nil, invalid("请指定与该北辰目录对应的校内模型")
	}
	plan := m.plan(target)
	if !plan.allowed(target) {
		return uuid.Nil, invalid("当前北辰凭据、已配置的路线或其他供应商计价不支持直接启用")
	}
	if target.id == uuid.Nil {
		err = tx.QueryRowContext(ctx, `INSERT INTO models(id,model_id,display_name,created_at,updated_at)
 VALUES(gen_random_uuid(),$1::text,$1::text,now(),now()) ON CONFLICT(model_id) DO UPDATE SET model_id=models.model_id RETURNING id`, target.name).Scan(&target.id)
		if err != nil {
			return uuid.Nil, err
		}
	}
	for _, write := range plan.writes {
		r := write.route
		if write.id == uuid.Nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
 VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,now(),now()) ON CONFLICT(model_id,provider_id,adapter) DO NOTHING`, target.id, provider, r.CredentialID, r.UpstreamModel, r.Adapter, r.Priority)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE model_bindings SET credential_id=$2,upstream_model_name=$3,updated_at=now() WHERE id=$1`, write.id, r.CredentialID, r.UpstreamModel)
		}
		if err != nil {
			return uuid.Nil, err
		}
	}
	// A positive neutral governance value makes the model eligible for student
	// applications. The Beichen ledger prices actual usage independently.
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
	return target.id, tx.Commit()
}
