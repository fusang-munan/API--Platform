package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
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
	SupportedProtocols  []string             `json:"supported_protocols"`
	AvailableProtocols  []string             `json:"available_protocols"`
	RecommendedProtocol *string              `json:"recommended_protocol"`
	ProtocolDetails     []ProtocolCapability `json:"protocol_details"`
	CatalogState        string               `json:"catalog_state"`
	Availability        string               `json:"availability"`
	AvailabilityReason  string               `json:"availability_reason,omitempty"`
	PriceState          string               `json:"price_state"`
	PriceBlocker        *CapabilityIssue     `json:"price_blocker"`
	MultiplierState     string               `json:"multiplier_state"`
	ProbeState          string               `json:"probe_state"`
	BindingState        string               `json:"binding_state"`
	ActionHint          string               `json:"action_hint"`
}

type BeichenCapabilities struct {
	Capabilities []ModelCapability `json:"capabilities"`
	Gaps         []CapabilityGap   `json:"gaps"`
}

// Look up model names in every current Key catalog and website snapshot.
// Include historical bindings so a loss of catalog coverage stays visible.
const readBeichenCapabilitiesSQL = `
WITH entries AS (
 SELECT k.*,lower(k.model_id::text) AS name_key
 FROM provider_model_catalog k WHERE k.provider_id=$1 AND k.status='active'
), website AS (
 SELECT w.credential_id,lower(btrim(n.model)) AS name_key,min(btrim(n.model)) AS model
 FROM beichen_website_catalogs w
 JOIN provider_credentials c ON c.id=w.credential_id AND w.credential_revision=c.updated_at AND c.status='active'
 CROSS JOIN LATERAL unnest(w.models) n(model)
 WHERE c.provider_id=$1 AND btrim(n.model)<>''
 GROUP BY w.credential_id,lower(btrim(n.model))
), bindings AS (
 SELECT COALESCE(b.credential_id,picked.credential_id) AS credential_id,
  lower(btrim(b.upstream_model_name)) AS name_key,btrim(b.upstream_model_name) AS model,
  b.adapter,b.status='active' AND s.status='active' AS published,s.status='active' AS active_school
 FROM model_bindings b JOIN models s ON s.id=b.model_id
 LEFT JOIN LATERAL (
  SELECT k.credential_id FROM entries k
  JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id AND c.status='active'
  WHERE b.credential_id IS NULL AND k.name_key=lower(btrim(b.upstream_model_name))
  ORDER BY k.credential_id LIMIT 1
 ) picked ON true
 WHERE b.provider_id=$1 AND btrim(b.upstream_model_name)<>''
), grouped_bindings AS (
 SELECT credential_id,name_key,min(model) AS model,bool_or(active_school) AS active_school,
  to_jsonb(COALESCE(array_agg(DISTINCT adapter ORDER BY adapter) FILTER (WHERE published),ARRAY[]::text[])) AS protocols
 FROM bindings GROUP BY credential_id,name_key
), names AS (
 SELECT credential_id,name_key FROM entries
 UNION SELECT credential_id,name_key FROM website
 UNION SELECT credential_id,name_key FROM grouped_bindings
)
SELECT COALESCE(k.model_id::text,w.model,b.model),n.credential_id,COALESCE(c.label::text,''),
 COALESCE(p.status='active' AND c.status='active' AND octet_length(c.credential_ciphertext)>0,false),
 COALESCE(k.last_seen_at>=c.updated_at,false),
 w.model IS NOT NULL,
 COALESCE(d.last_error IS NULL AND d.last_success>=c.updated_at,false),
 COALESCE(k.raw_entry,'{}'::jsonb),COALESCE(to_jsonb(k.supported_protocols),'[]'::jsonb),
 COALESCE(b.active_school,false) OR EXISTS (
  SELECT 1 FROM models s WHERE s.status='active' AND s.model_id=n.name_key::citext
   AND NOT EXISTS (SELECT 1 FROM model_bindings other WHERE other.model_id=s.id)
 ),
 COALESCE(b.protocols,'[]'::jsonb),probes.observations,
 price.mode,price.school_input IS NOT NULL AND price.school_output IS NOT NULL
  AND (price.mode='manual' OR (price.school_cache_read IS NOT NULL AND price.school_cache_write IS NOT NULL)),
 CASE WHEN snap.expires_at>statement_timestamp()
        AND COALESCE(snap.credential_ciphertext_sha256=sha256(c.credential_ciphertext),snap.credential_revision=c.updated_at)
      THEN snap.model_pricing->k.model_id::text END,
 COALESCE(snap.expires_at<=statement_timestamp() AND snap.model_pricing ? k.model_id::text,false),
 CASE WHEN c.id IS NULL THEN 'not_applicable'
  WHEN billing.last_error IS NOT NULL THEN 'sync_failed'
  WHEN billing.last_success IS NULL OR NOT EXISTS (
   SELECT 1 FROM beichen_multiplier_versions m WHERE m.credential_id=c.id AND m.effective_at<=statement_timestamp()
  ) THEN 'missing'
  WHEN billing.last_success<c.updated_at OR billing.last_success<=statement_timestamp()-interval '16 minutes'
    OR snap.expires_at<=statement_timestamp() THEN 'expired'
  ELSE 'current' END
FROM names n JOIN providers p ON p.id=$1
LEFT JOIN provider_credentials c ON c.id=n.credential_id AND c.provider_id=p.id
LEFT JOIN entries k ON k.credential_id=n.credential_id AND k.name_key=n.name_key
LEFT JOIN website w ON w.credential_id=n.credential_id AND w.name_key=n.name_key
LEFT JOIN grouped_bindings b ON b.credential_id IS NOT DISTINCT FROM n.credential_id AND b.name_key=n.name_key
LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='catalog'
LEFT JOIN beichen_datasets billing ON billing.credential_id=c.id AND billing.kind='billing'
LEFT JOIN LATERAL (
 SELECT expires_at,credential_revision,credential_ciphertext_sha256,model_pricing FROM beichen_panel_snapshots
 WHERE credential_id=c.id AND effective_at<=statement_timestamp() ORDER BY effective_at DESC LIMIT 1
) snap ON true
CROSS JOIN LATERAL beichen_price_at(c.id,k.model_id::text,statement_timestamp()) price
CROSS JOIN LATERAL (
 SELECT COALESCE(jsonb_object_agg(r.protocol,` + probeEvidenceSQL + `),'{}'::jsonb) AS observations
 FROM upstream_probes r
 WHERE r.provider_id=p.id AND r.credential_id=c.id AND r.model=k.model_id::text AND ` + currentProbeRevisionSQL + `
  AND p.status='active' AND c.status='active'
) probes
ORDER BY n.name_key,c.label,c.id`

type capabilityRow struct {
	ModelCapability
	keyEnabled, keyListed, websiteListed, catalogCurrent, activeSchool bool
	fixedPrice, priceExpired                                           bool
	priceMode                                                          string
	priceTerms                                                         []byte
	entryRaw, declaredJSON, publishedJSON, probesJSON                  []byte
}

func ReadBeichenCapabilities(ctx context.Context, db *sql.DB, providerID uuid.UUID) (BeichenCapabilities, error) {
	view := BeichenCapabilities{Capabilities: []ModelCapability{}, Gaps: []CapabilityGap{}}
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
		err = readCapabilityRows(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return BeichenCapabilities{}, domain.WrapError(domain.CodeDependencyUnavailable, "读取北辰能力目录失败", err)
	}
	return view, nil
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
			&row.keyEnabled, &row.keyListed, &row.websiteListed, &row.catalogCurrent,
			&row.entryRaw, &row.declaredJSON, &row.activeSchool, &row.publishedJSON, &row.probesJSON,
			&row.priceMode, &row.fixedPrice, &row.priceTerms, &row.priceExpired, &row.MultiplierState); err != nil {
			return err
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
	var published []string
	var probes map[string]*ProbeEvidence
	for _, pair := range []struct {
		raw []byte
		dst any
	}{
		{row.entryRaw, &entry.Raw}, {row.declaredJSON, &entry.SupportedProtocols},
		{row.publishedJSON, &published}, {row.probesJSON, &probes},
	} {
		if err := json.Unmarshal(pair.raw, pair.dst); err != nil {
			return err
		}
	}

	row.CatalogState = "key_listed"
	rescan := "在供应商管理中重扫「" + row.CredentialLabel + "」Key 的模型目录。"
	switch {
	case row.websiteListed && !row.keyListed && row.catalogCurrent && row.keyEnabled:
		row.CatalogState = "website_only"
		row.addGap(view, "catalog", "website_only", "官网分组列出了该模型，当前这把 Key 的目录没有返回它。", rescan)
	case !row.keyListed && row.CredentialID != nil && !row.catalogCurrent && row.keyEnabled:
		row.CatalogState = "catalog_unverified"
		row.addGap(view, "catalog", "catalog_unverified", "当前 Key 缺少成功的目录扫描。", rescan)
	case !row.keyListed:
		row.CatalogState = "catalog_missing"
	}

	row.BindingState = "not_published"
	switch {
	case len(published) > 0 && row.keyListed && row.keyEnabled:
		row.BindingState = "ready"
	case row.activeSchool:
		row.BindingState = "invalid"
		action := "检查渠道 Key 与模型协议，再预览并确认重绑。"
		switch {
		case row.CredentialID == nil || !row.keyEnabled:
			action = "启用或配置有效的渠道 Key，重扫目录后再重绑。"
		case !row.keyListed:
			action = rescan + "模型重新出现后预览并确认重绑。"
		}
		row.addGap(view, "binding", "binding_invalid", "校内模型已发布，这条绑定无法使用。", action)
	}

	row.ProtocolDetails = []ProtocolCapability{}
	var bound []ProtocolCapability
	if row.keyListed {
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
	whole, routed := SummarizeProtocols(row.Model, row.ProtocolDetails), SummarizeProtocols(row.Model, bound)
	row.SupportedProtocols, row.AvailableProtocols = whole.Protocols, routed.AvailableProtocols
	row.ProbeState, row.RecommendedProtocol = whole.ProbeState, routed.RecommendedProtocol
	if row.BindingState == "ready" {
		row.ProbeState = routed.ProbeState
	}
	if row.BindingState == "not_published" && row.keyEnabled {
		row.RecommendedProtocol = whole.RecommendedProtocol
	}
	row.Availability = "not_published"
	switch {
	case routed.ProbeState == probeFailed || whole.ProbeState == probeFailed:
		row.Availability = "maintenance"
		row.AvailabilityReason = "维护中。当前线路的协议最近一次探活均失败。"
	case row.BindingState == "invalid":
		row.Availability = "maintenance"
		row.AvailabilityReason = "维护中。校内模型已发布，这条绑定无法使用。"
	case len(routed.AvailableProtocols) > 0:
		row.Availability = "available"
	case row.BindingState == "ready":
		row.Availability = "unverified"
	case !row.keyListed || !row.keyEnabled:
		row.Availability = "unavailable"
	}
	row.reportProbeGaps(view, whole)
	row.reportPriceGaps(view)
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

func (row *capabilityRow) reportPriceGaps(view *BeichenCapabilities) {
	switch {
	case row.fixedPrice && row.priceMode == "manual":
		row.PriceState = "manual"
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
	switch row.MultiplierState {
	case "expired":
		row.addGap(view, "multiplier", "multiplier_expired", "渠道倍率已超过有效期或 Key 已换版。", "请重新同步该 Key 的账单倍率。")
	case "missing":
		row.addGap(view, "multiplier", "multiplier_missing", "当前渠道尚无有效倍率记录。", "请检查 Key 所属分组并同步账单倍率。")
	case "sync_failed":
		row.addGap(view, "multiplier", "multiplier_sync_failed", "最近一次账单同步失败，渠道倍率需要核对。", "请查看该 Key 的账单同步错误，恢复渠道后重新同步。")
	}
}
