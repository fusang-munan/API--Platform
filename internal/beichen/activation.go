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
	routes, northRoutes       []PreferredRoute
	writes                    []activationBinding
	northReady, foreignActive bool
}

func loadActivationCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID, source string) ([]*activationModel, error) {
	query := `
SELECT k.model_id::text,k.raw_entry,p.name::text,c.id,c.label::text,
 p.status='active' AND c.status='active',
 CASE WHEN d.last_error IS NULL AND d.last_success>=c.updated_at
   AND d.last_success>statement_timestamp()-interval '16 minutes'
   AND mv.stale_at IS NULL
 THEN mv.multiplier::text END, to_json(k.supported_protocols)
FROM provider_model_catalog k JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
JOIN providers p ON p.id=c.provider_id
LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='billing'
LEFT JOIN LATERAL (SELECT multiplier,stale_at FROM beichen_multiplier_versions WHERE credential_id=c.id
 AND effective_at<=statement_timestamp()
 ORDER BY effective_at DESC,id DESC LIMIT 1) mv ON true
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
		var modelID string
		var raw, protocols []byte
		var providerName, label string
		var key uuid.UUID
		var available bool
		var rate *string
		if err := rows.Scan(&modelID, &raw, &providerName, &key, &label, &available, &rate, &protocols); err != nil {
			return nil, err
		}
		name := strings.ToLower(modelID)
		m := index[name]
		if m == nil {
			m = &activationModel{name: modelID, provider: provider, defaults: map[string]PreferredRoute{}, schools: map[string]*activationSchool{}}
			index[name] = m
			models = append(models, m)
		}
		m.keys = append(m.keys, key)
		if !available {
			continue
		}
		entry, err := storedCatalogEntry(modelID, raw, protocols)
		if err != nil {
			return nil, err
		}
		if rate != nil {
			n := json.Number(*rate)
			rates[key] = &n
		}
		for _, protocol := range catalog.RegisteredProtocols(entry) {
			if _, ok := m.defaults[protocol]; ok {
				continue
			}
			m.defaults[protocol] = PreferredRoute{SchoolModel: m.name, UpstreamModel: modelID, ProviderName: providerName, CredentialLabel: &label,
				Adapter: protocol, ProviderID: provider, CredentialID: &key, Priority: catalog.DefaultBindingPriority(modelbinding.Adapter(protocol)), State: "candidate", EffectiveMultiplier: rates[key]}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(models) == 0 && source != "" {
		return models, nil
	}
	bound, err := tx.QueryContext(ctx, `
WITH north AS (
 SELECT DISTINCT model_id FROM provider_model_catalog WHERE provider_id=$1 AND status='active' AND ($2::text='' OR model_id=$2::citext)
 UNION
 SELECT btrim(b.upstream_model_name)::citext FROM model_bindings b JOIN models s ON s.id = b.model_id
 WHERE b.provider_id=$1 AND $2::text='' AND s.status='active' AND btrim(b.upstream_model_name)<>''
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
		group := strings.ToLower(source)
		m := index[group]
		if m == nil {
			m = &activationModel{name: source, provider: provider, defaults: map[string]PreferredRoute{}, schools: map[string]*activationSchool{}}
			index[group] = m
			models = append(models, m)
		}
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
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].name) < strings.ToLower(models[j].name) })
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
		for _, adapter := range slices.Sorted(maps.Keys(m.defaults)) {
			r := m.defaults[adapter]
			r.SchoolModel = s.name
			plan.writes = append(plan.writes, activationBinding{route: r})
			plan.routes = append(plan.routes, r)
		}
		plan.northRoutes = plan.routes
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
		if rank > 0 && r.ProviderID == m.provider {
			plan.northRoutes = append(plan.northRoutes, r)
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
		for _, adapter := range slices.Sorted(maps.Keys(m.defaults)) {
			r := m.defaults[adapter]
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
			plan.northRoutes = append(plan.northRoutes, r)
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
			price = record.priceReadiness
		} else {
			var fixed bool
			var terms []byte
			if err := tx.QueryRowContext(ctx, activationPriceSQL, key.credential, key.model).Scan(&fixed, &terms); err != nil {
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

// Read the original terms without converting any prices to floating point.
const activationFixedSchoolPriceSQL = `p.school_input IS NOT NULL AND p.school_output IS NOT NULL
  AND (p.mode = 'manual' OR (p.school_cache_read IS NOT NULL AND p.school_cache_write IS NOT NULL))`

const activationPriceSQL = `
SELECT ` + activationFixedSchoolPriceSQL + `,
  CASE WHEN snap.expires_at > statement_timestamp()
       AND COALESCE(snap.credential_ciphertext_sha256 = sha256(c.credential_ciphertext), snap.credential_revision = c.updated_at)
       THEN snap.model_pricing -> ($2::text) END
FROM provider_credentials c
CROSS JOIN LATERAL beichen_price_at(c.id,$2,statement_timestamp()) p
LEFT JOIN LATERAL (
  SELECT expires_at,credential_ciphertext_sha256,credential_revision,model_pricing
  FROM beichen_panel_snapshots
  WHERE credential_id=c.id AND effective_at<=statement_timestamp()
  ORDER BY effective_at DESC LIMIT 1
) snap ON true
WHERE c.id=$1`

func (m *activationModel) overview() CatalogModel {
	result := CatalogModel{Model: m.name, CredentialIDs: append([]uuid.UUID{}, m.keys...), SchoolStatus: "not_configured", PreferredRoutes: []PreferredRoute{}}
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

type BatchEnabledModel struct {
	Model   string    `json:"model"`
	ModelID uuid.UUID `json:"model_id"`
	Message string    `json:"message,omitempty"`
}

type BatchSkippedModel struct {
	Model   string `json:"model"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type BatchEnableResult struct {
	Enabled []BatchEnabledModel `json:"enabled"`
	Skipped []BatchSkippedModel `json:"skipped"`
}

// EnableModelsBatch 逐模型提交，单项失败不会撤销同批已经成功的启用。
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
	for _, model := range unique {
		var enabled BatchEnabledModel
		var skipped *BatchSkippedModel
		var err error
		for attempt := 0; ; attempt++ {
			enabled, skipped, err = s.enableBatchModel(ctx, provider, actor, model)
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

func (s *Service) enableBatchModel(ctx context.Context, provider, actor uuid.UUID, source string) (BatchEnabledModel, *BatchSkippedModel, error) {
	var enabled BatchEnabledModel
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return enabled, nil, err
	}
	defer tx.Rollback()
	skip := func(reason, message string) (BatchEnabledModel, *BatchSkippedModel, error) {
		return enabled, &BatchSkippedModel{Model: source, Reason: reason, Message: message}, nil
	}
	models, err := loadActivationCatalog(ctx, tx, provider, source)
	if err != nil {
		return enabled, nil, err
	}
	if len(models) != 1 {
		return skip("not_found", "当前北辰目录中没有此模型")
	}
	m := models[0]
	target := m.target("")
	if target == nil {
		return skip("route_not_ready", "存在多个校内模型，请先明确该模型的线路")
	}
	plan := m.plan(target)
	if !plan.allowed(target) {
		return skip("route_not_ready", "当前北辰凭据、已配置的线路或其他供应商计价尚未就绪")
	}
	if target.status == "active" && len(plan.writes) == 0 && target.multiplier != nil && *target.multiplier > 0 {
		return skip("already_active", "模型已启用")
	}
	price, err := plan.priceReadiness(ctx, tx, nil)
	if err != nil {
		return enabled, nil, err
	}
	if !price.ready() {
		return skip("no_price", price.blocker.Message)
	}
	id, err := applyActivationPlan(ctx, tx, provider, actor, target, plan)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return enabled, nil, err
	}
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
		return uuid.Nil, invalid("请指定与该北辰目录对应的校内模型")
	}
	plan := m.plan(target)
	if !plan.allowed(target) {
		return uuid.Nil, invalid("当前北辰凭据、已配置的路线或其他供应商计价不支持直接启用")
	}
	if target.status == "active" && len(plan.writes) == 0 && target.multiplier != nil && *target.multiplier > 0 {
		return target.id, nil
	}
	price, err := plan.priceReadiness(ctx, tx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	if !price.ready() {
		return uuid.Nil, invalid(price.blocker.Message)
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
