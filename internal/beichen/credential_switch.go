package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type CredentialSwitch struct {
	From uuid.UUID `json:"from_credential_id"`
	To   uuid.UUID `json:"to_credential_id"`
}

type SwitchCredential struct {
	CredentialRef
	Status        string            `json:"status"`
	Sync          map[string]string `json:"sync"`
	Identity      string            `json:"identity,omitempty"`
	SchoolChannel string            `json:"school_channel,omitempty"`
	revision      time.Time
}

type SwitchRoute struct {
	Adapter string     `json:"adapter"`
	Ready   bool       `json:"ready"`
	Stale   bool       `json:"stale"`
	KeyID   *uuid.UUID `json:"credential_id"`
	Label   *string    `json:"credential_label"`
	Model   string     `json:"upstream_model"`
}

type SwitchModel struct {
	Model                string        `json:"model"`
	Routes               []SwitchRoute `json:"routes"`
	ReadyCount           int           `json:"ready_count"`
	LosingRouteCount     int           `json:"losing_route_count"`
	LosesLastRoute       bool          `json:"loses_last_route"`
	readyAfterRetirement bool
}

type SwitchCounts struct {
	EnabledKeys       int `json:"enabled_keys"`
	EnabledModels     int `json:"enabled_models"`
	MaintenanceModels int `json:"maintenance_models"`
	StaleRoutes       int `json:"stale_routes"`
}

type SwitchBlocker struct {
	SchoolModel string `json:"school_model"`
	Adapter     string `json:"adapter"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

type SwitchPreview struct {
	RebindPlan
	Blockers []SwitchBlocker `json:"blockers"`
}

type ChannelSwitchView struct {
	Credentials []SwitchCredential `json:"credentials"`
	Counts      SwitchCounts       `json:"counts"`
	Models      []SwitchModel      `json:"models"`
	Preview     *SwitchPreview     `json:"preview"`
}

// ChannelSwitch derives the stale and maintenance states from the current
// bindings and catalogs. Its preview is rebuilt from a single read snapshot.
func (s *Service) ChannelSwitch(ctx context.Context, provider uuid.UUID, scope CredentialSwitch) (ChannelSwitchView, error) {
	if s.db == nil {
		return ChannelSwitchView{}, unavailable("北辰渠道切换未配置", nil)
	}
	if scope.From != uuid.Nil && scope.From == scope.To {
		return ChannelSwitchView{}, invalid("原渠道和接替渠道须为不同的 Key")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ChannelSwitchView{}, unavailable("读取北辰渠道切换状态失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return ChannelSwitchView{}, err
	}
	keys, enabled, err := readSwitchCredentials(ctx, tx, provider)
	if err != nil {
		return ChannelSwitchView{}, unavailable("读取北辰渠道同步进度失败", err)
	}
	from, to, err := selectSwitchCredentials(keys, scope)
	if err != nil {
		return ChannelSwitchView{}, err
	}
	view := ChannelSwitchView{Credentials: keys}
	view.Counts.EnabledKeys = enabled
	view.Models, err = readSwitchModels(ctx, tx, provider, scope.From, &view.Counts)
	if err != nil {
		return ChannelSwitchView{}, unavailable("读取北辰线路影响失败", err)
	}
	if from != nil && to != nil && switchCatalogReady(to) {
		plan, err := readCredentialRebind(ctx, tx, provider, *from, *to, false)
		if err != nil {
			return ChannelSwitchView{}, unavailable("生成北辰渠道切换预览失败", err)
		}
		view.Preview = &plan
	}
	if err := tx.Commit(); err != nil {
		return ChannelSwitchView{}, unavailable("读取北辰渠道切换状态失败", err)
	}
	return view, nil
}

func selectSwitchCredentials(keys []SwitchCredential, scope CredentialSwitch) (from, to *SwitchCredential, err error) {
	for i := range keys {
		key := &keys[i]
		if key.CredentialID == scope.From {
			from = key
		}
		if key.CredentialID == scope.To {
			to = key
		}
	}
	if (scope.From != uuid.Nil && from == nil) || (scope.To != uuid.Nil && to == nil) {
		err = domain.NewError(domain.CodeNotFound, "所选渠道不属于该北辰供应商")
	}
	return
}

func switchCatalogReady(key *SwitchCredential) bool {
	return key.Sync["catalog"] == "ready" && key.Sync["billing"] != "pending" &&
		key.Sync["billing"] != "syncing" && key.Sync["usage"] != "pending" && key.Sync["usage"] != "syncing"
}

func readSwitchCredentials(ctx context.Context, tx *sql.Tx, provider uuid.UUID) ([]SwitchCredential, int, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id,c.label::text,c.status,c.updated_at,p.status='active' AND c.status='active',COALESCE(j.channel,''),
  CASE WHEN j.credential_id IS NULL THEN ''
    WHEN c.status<>'active' OR p.status<>'active' THEN 'inactive'
    WHEN EXISTS (SELECT 1 FROM beichen_receipt_active_bindings a WHERE a.credential_id=c.id) THEN 'ready'
    WHEN j.discovery_error IS NOT NULL THEN 'failed'
    ELSE 'pending' END,
  jsonb_object_agg(job.kind, CASE
    WHEN p.status<>'active' OR c.status<>'active' THEN 'inactive'
    WHEN d.lease_until > statement_timestamp() AND (d.last_success IS NULL OR d.last_success < c.updated_at OR d.last_error IS NOT NULL) THEN 'syncing'
    WHEN d.last_error IS NOT NULL THEN 'failed'
    WHEN d.last_success >= c.updated_at THEN 'ready'
    ELSE 'pending' END)
FROM provider_credentials c JOIN providers p ON p.id=c.provider_id
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
CROSS JOIN (VALUES ('catalog'),('prices'),('billing'),('usage')) job(kind)
LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind=job.kind
WHERE c.provider_id=$1
GROUP BY c.id,p.status,j.credential_id,j.channel,j.discovery_error ORDER BY c.label,c.id`, provider)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	keys := []SwitchCredential{}
	enabled := 0
	for rows.Next() {
		var key SwitchCredential
		var active bool
		var sync []byte
		if err := rows.Scan(&key.CredentialID, &key.Label, &key.Status, &key.revision, &active, &key.SchoolChannel, &key.Identity, &sync); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(sync, &key.Sync); err != nil {
			return nil, 0, err
		}
		if active {
			enabled++
		}
		keys = append(keys, key)
	}
	return keys, enabled, rows.Err()
}

func readSwitchModels(ctx context.Context, tx *sql.Tx, provider, retiring uuid.UUID, counts *SwitchCounts) ([]SwitchModel, error) {
	// Match the existing routing rule, including legacy bindings that select
	// the first available Key and fallbacks from another provider.
	rows, err := tx.QueryContext(ctx, `
WITH published AS (
  SELECT m.id,m.model_id::text AS name FROM models m WHERE m.status='active'
  AND EXISTS (SELECT 1 FROM model_bindings b WHERE b.model_id=m.id AND b.provider_id=$1)
)
SELECT school.name,b.provider_id,b.credential_id,c.label::text,b.upstream_model_name,b.adapter,
  b.status='active',p.status='active' AND picked.ready,p.status='active' AND picked.other_key
FROM published school
JOIN model_bindings b ON b.model_id=school.id
JOIN providers p ON p.id=b.provider_id
LEFT JOIN provider_credentials c ON c.id=b.credential_id AND c.provider_id=b.provider_id
JOIN LATERAL (
  SELECT count(*)>0 AS ready,count(*) FILTER (WHERE k.credential_id<>$2)>0 AS other_key
  FROM provider_model_catalog k
  JOIN provider_credentials owner ON owner.id=k.credential_id AND owner.provider_id=k.provider_id AND owner.status='active'
  WHERE k.provider_id=b.provider_id AND k.model_id=btrim(b.upstream_model_name)::citext
    AND k.status='active' AND (b.credential_id IS NULL OR k.credential_id=b.credential_id)
) picked ON true
ORDER BY school.name,b.provider_id,b.adapter`, provider, retiring)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []SwitchModel{}
	for rows.Next() {
		var model string
		var route SwitchRoute
		var owner uuid.UUID
		var active, catalogReady, otherReady bool
		if err := rows.Scan(&model, &owner, &route.KeyID, &route.Label, &route.Model, &route.Adapter, &active, &catalogReady, &otherReady); err != nil {
			return nil, err
		}
		if len(models) == 0 || models[len(models)-1].Model != model {
			models = append(models, SwitchModel{Model: model, Routes: []SwitchRoute{}})
		}
		entry := &models[len(models)-1]
		route.Ready = active && catalogReady
		if route.Ready {
			entry.ReadyCount++
		}
		entry.readyAfterRetirement = entry.readyAfterRetirement || active && otherReady
		if owner == provider {
			route.Stale = active && !catalogReady
			if route.Stale {
				counts.StaleRoutes++
			}
			if route.Ready && !otherReady {
				entry.LosingRouteCount++
			}
			entry.Routes = append(entry.Routes, route)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts.EnabledModels = len(models)
	affected := make([]SwitchModel, 0, len(models))
	for _, model := range models {
		if model.ReadyCount == 0 {
			counts.MaintenanceModels++
		}
		model.LosesLastRoute = model.ReadyCount > 0 && !model.readyAfterRetirement
		if model.ReadyCount == 0 || model.LosingRouteCount > 0 || includesSwitchRoute(model.Routes, retiring) {
			affected = append(affected, model)
		}
	}
	return affected, nil
}

func includesSwitchRoute(routes []SwitchRoute, selected uuid.UUID) bool {
	for _, route := range routes {
		if route.Stale || (route.KeyID != nil && *route.KeyID == selected) {
			return true
		}
	}
	return false
}

// UseActiveCredentialHealth keeps retired Keys in the switch view while
// reporting only enabled Keys in the teacher model coverage.
func (view *Overview) UseActiveCredentialHealth() {
	credentials := make([]CredentialSummary, 0, len(view.Credentials))
	for _, key := range view.Credentials {
		if key.Status == "active" {
			if key.Health.Pricing.State == "available" && key.PricedModelCount < key.ModelCount {
				key.Health.Pricing.State = "pending"
				key.Health.AvailableDimensions--
			}
			credentials = append(credentials, key)
		}
	}
	view.Credentials = credentials
	for i := range view.ModelCatalog {
		model := &view.ModelCatalog[i]
		if model.BindingIssue != nil && model.ReadyCount == 0 {
			model.BindingIssue.Message = "维护中。请在供应商管理打开「渠道切换向导」，同步接替 Key、核对报价和协议，再确认重绑。"
		}
	}
}
