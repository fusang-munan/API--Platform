package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

type RebindRoute struct {
	CredentialID    *uuid.UUID `json:"credential_id"`
	CredentialLabel *string    `json:"credential_label"`
	UpstreamModel   string     `json:"upstream_model"`
	Priority        int        `json:"priority"`
	Status          string     `json:"status"`
}

type RebindChange struct {
	ModelID     uuid.UUID    `json:"model_id"`
	SchoolModel string       `json:"school_model"`
	Adapter     string       `json:"adapter"`
	BindingID   *uuid.UUID   `json:"binding_id,omitempty"`
	Action      string       `json:"action"`
	Before      *RebindRoute `json:"before"`
	After       RebindRoute  `json:"after"`
}

type RebindPlan struct {
	Preview          bool                    `json:"preview"`
	PreviewToken     string                  `json:"preview_token"`
	EvidenceVersions []RebindEvidenceVersion `json:"evidence_versions,omitempty"`
	Changes          []RebindChange          `json:"changes"`
}

type RebindEvidenceVersion struct {
	CredentialID       uuid.UUID `json:"credential_id"`
	CredentialRevision time.Time `json:"credential_revision"`
	GroupID            *int64    `json:"group_id"`
	CatalogObservedAt  time.Time `json:"catalog_observed_at"`
	EvidenceSince      time.Time `json:"evidence_since"`
}

type rebindSchool struct {
	id   uuid.UUID
	name string
}

type rebindSlot struct {
	model   uuid.UUID
	adapter string
}

type rebindCatalogEntry struct {
	explicit, catalogCurrent bool
	school                   rebindSchool
	route                    RebindRoute
	protocols                []string
	probes                   map[string]rebindProbeEvidence
}

type rebindProbeEvidence struct {
	CheckedAt time.Time `json:"checked_at"`
	Blocked   bool      `json:"blocked"`
}

type bindingRestore struct {
	schoolModelID, credentialID uuid.UUID
}

func (r bindingRestore) tokenPrefix() string {
	return "restore/" + r.schoolModelID.String() + "/" + r.credentialID.String()
}

func (s *Service) Rebind(ctx context.Context, provider, actor uuid.UUID, preview bool, token string) (RebindPlan, error) {
	if provider == uuid.Nil || actor == uuid.Nil {
		return RebindPlan{}, invalid("需要有效的供应商和教师 ID")
	}
	var restore *bindingRestore
	digest := token
	if strings.HasPrefix(token, "restore/") {
		parts := strings.SplitN(token, "/", 4)
		if preview && len(parts) != 3 || !preview && len(parts) != 4 {
			return RebindPlan{}, invalid("请指定要恢复的校内模型和接替渠道")
		}
		modelID, modelErr := uuid.Parse(parts[1])
		credentialID, keyErr := uuid.Parse(parts[2])
		if modelErr != nil || keyErr != nil || modelID == uuid.Nil || credentialID == uuid.Nil {
			return RebindPlan{}, invalid("需要有效的校内模型和接替渠道 ID")
		}
		restore = &bindingRestore{schoolModelID: modelID, credentialID: credentialID}
		if !preview {
			digest = parts[3]
		}
	}
	if !preview {
		if _, err := hex.DecodeString(digest); len(digest) != sha256.Size*2 || err != nil {
			return RebindPlan{}, invalid("请先预览绑定重建，再确认执行")
		}
	}
	if s.db == nil {
		return RebindPlan{}, unavailable("北辰绑定重建未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	plan, err := s.rebind(ctx, provider, actor, preview, token, restore)
	if !preview && activationRetryable(err) {
		return RebindPlan{}, domain.WrapError(domain.CodeInvalidTransition, "北辰目录或绑定正在更新，请重新预览", err)
	}
	return plan, err
}

func (s *Service) rebind(ctx context.Context, provider, actor uuid.UUID, preview bool, token string, restore *bindingRestore) (RebindPlan, error) {
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if !preview {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return RebindPlan{}, unavailable("开启北辰绑定重建失败", err)
	}
	defer tx.Rollback()
	if preview {
		if err := checkProvider(ctx, tx, provider); err != nil {
			return RebindPlan{}, err
		}
	} else if err := lockRebindConfirmation(ctx, tx, provider); err != nil {
		return RebindPlan{}, err
	}
	var school, credential *uuid.UUID
	if restore != nil {
		school, credential = &restore.schoolModelID, &restore.credentialID
	}
	bindings, priorities, err := readRebindBindings(ctx, tx, provider, school, !preview)
	if err != nil {
		return RebindPlan{}, unavailable("读取北辰绑定失败", err)
	}
	entries, err := readRebindCatalog(ctx, tx, provider, credential)
	if err != nil {
		return RebindPlan{}, unavailable("读取北辰重建目录失败", err)
	}
	versions, ready, err := readRebindEvidence(ctx, tx, provider, credential)
	if err != nil {
		return RebindPlan{}, unavailable("核对北辰渠道目录失败", err)
	}
	if !ready {
		return RebindPlan{}, domain.NewError(domain.CodeInvalidTransition,
			"目录或分组待复核，请核对启用渠道的分组权限并重扫目录后重新预览")
	}
	var changes []RebindChange
	if restore == nil {
		changes, err = planRebind(bindings, entries, priorities)
	} else {
		changes, err = planBindingRestore(bindings, entries, *restore)
	}
	if err != nil {
		return RebindPlan{}, err
	}
	raw, err := json.Marshal(struct {
		EvidenceVersions []RebindEvidenceVersion
		Changes          []RebindChange
		TargetProbes     []time.Time
	}{versions, changes, rebindTargetProbeTimes(entries, changes)})
	if err != nil {
		return RebindPlan{}, unavailable("生成北辰重建预览失败", err)
	}
	scope := provider.String()
	if restore != nil {
		scope += "/" + restore.tokenPrefix()
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(append([]byte(scope), raw...)))
	if restore != nil {
		fingerprint = restore.tokenPrefix() + "/" + fingerprint
	}
	view := RebindPlan{Preview: preview, PreviewToken: fingerprint, EvidenceVersions: versions, Changes: changes}
	if !preview {
		if token != fingerprint {
			return RebindPlan{}, domain.NewError(domain.CodeInvalidTransition, "凭据、分组、探活、目录、别名或绑定已变化，请重新预览")
		}
		if err := applyRebind(ctx, tx, provider, changes); err != nil {
			return RebindPlan{}, err
		}
		// A fresh statement also catches evidence committed by a writer outside
		// the application locks while the binding statements were running.
		current, ready, err := readRebindEvidence(ctx, tx, provider, credential)
		if err != nil {
			return RebindPlan{}, unavailable("重新核对北辰渠道目录失败", err)
		}
		if !ready || !reflect.DeepEqual(versions, current) {
			return RebindPlan{}, domain.NewError(domain.CodeInvalidTransition, "凭据、分组或目录已变化，请重新预览")
		}
		if err := checkRebindTargets(ctx, tx, provider, credential, changes); err != nil {
			return RebindPlan{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RebindPlan{}, unavailable("完成北辰绑定重建失败", err)
	}
	if !preview && len(changes) > 0 {
		slog.InfoContext(ctx, "北辰绑定重建", "event", "beichen_bindings_rebuilt", "actor_user_id", actor,
			"provider_id", provider, "change_count", len(changes))
	}
	return view, nil
}

func lockRebindConfirmation(ctx context.Context, tx *sql.Tx, provider uuid.UUID) error {
	locked, err := catalog.TryLockBeichenRebind(ctx, tx, provider)
	if err != nil {
		return unavailable("锁定北辰绑定重建证据失败", err)
	}
	if !locked {
		return domain.NewError(domain.CodeInvalidTransition, "北辰目录或绑定正在更新，请重新预览")
	}
	// Website Key inventory and alias writers also lock this provider row.
	return lockAliasProvider(ctx, tx, provider)
}

func checkRebindTargets(ctx context.Context, tx *sql.Tx, provider uuid.UUID, credential *uuid.UUID, changes []RebindChange) error {
	if len(changes) == 0 {
		return nil
	}
	entries, err := readRebindCatalog(ctx, tx, provider, credential)
	if err != nil {
		return unavailable("重新核对北辰接替线路失败", err)
	}
	for _, change := range changes {
		if change.After.Status == "active" && rebindTarget(entries, change) == nil {
			return domain.NewError(domain.CodeInvalidTransition, "接替线路的目录或认证状态已变化，请重新预览")
		}
	}
	return nil
}

func rebindTarget(entries []rebindCatalogEntry, change RebindChange) *rebindCatalogEntry {
	for i := range entries {
		entry := &entries[i]
		if entry.catalogCurrent && sameRouteKey(entry.route.CredentialID, change.After.CredentialID) &&
			strings.EqualFold(entry.route.UpstreamModel, strings.TrimSpace(change.After.UpstreamModel)) &&
			slices.Contains(entry.protocols, change.Adapter) {
			return entry
		}
	}
	return nil
}

func rebindTargetProbeTimes(entries []rebindCatalogEntry, changes []RebindChange) []time.Time {
	times := make([]time.Time, len(changes))
	for i, change := range changes {
		if entry := rebindTarget(entries, change); entry != nil {
			times[i] = entry.probes[change.Adapter].CheckedAt.UTC()
		}
	}
	return times
}

func readRebindEvidence(ctx context.Context, tx *sql.Tx, provider uuid.UUID, credential *uuid.UUID) ([]RebindEvidenceVersion, bool, error) {
	var ready bool
	var raw []byte
	err := tx.QueryRowContext(ctx, `
WITH `+catalog.BeichenKeyEvidenceCTE+`
SELECT p.status='active' AND ($2::uuid IS NULL OR count(c.id)=1) AND COALESCE(bool_and(`+catalog.BeichenCatalogCurrentSQL+`
  AND `+catalog.BeichenGroupAllowsRouteSQL+`) FILTER (WHERE c.id IS NOT NULL),true),
  COALESCE(jsonb_agg(jsonb_build_object(
    'credential_id', c.id, 'credential_revision', e.revision, 'group_id', e.group_id,
    'catalog_observed_at', e.catalog_at, 'evidence_since', e.evidence_since
  ) ORDER BY c.id) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb)
FROM providers p
LEFT JOIN provider_credentials c ON c.provider_id=p.id AND c.status='active' AND ($2::uuid IS NULL OR c.id=$2)
LEFT JOIN beichen_key_evidence e ON e.credential_id=c.id
WHERE p.id=$1 GROUP BY p.id`, provider, credential).Scan(&ready, &raw)
	if err != nil || !ready {
		return nil, ready, err
	}
	var versions []RebindEvidenceVersion
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, false, err
	}
	for i := range versions {
		e := &versions[i]
		e.CredentialRevision = e.CredentialRevision.UTC()
		e.CatalogObservedAt = e.CatalogObservedAt.UTC()
		e.EvidenceSince = e.EvidenceSince.UTC()
	}
	return versions, true, nil
}

// ConfirmCredentialSwitch only moves bindings from the selected original Key
// to the selected replacement. Uncovered routes stay on their original Key.
func (s *Service) ConfirmCredentialSwitch(ctx context.Context, provider, actor uuid.UUID, scope CredentialSwitch, token string) (SwitchPreview, error) {
	if provider == uuid.Nil || actor == uuid.Nil || scope.From == uuid.Nil || scope.To == uuid.Nil || scope.From == scope.To {
		return SwitchPreview{}, invalid("请选择两个不同的北辰渠道")
	}
	if _, err := hex.DecodeString(token); len(token) != sha256.Size*2 || err != nil {
		return SwitchPreview{}, invalid("请先预览渠道切换，再确认重绑")
	}
	if s.db == nil {
		return SwitchPreview{}, unavailable("北辰渠道切换未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SwitchPreview{}, unavailable("开启北辰渠道切换失败", err)
	}
	defer tx.Rollback()
	if err := lockRebindConfirmation(ctx, tx, provider); err != nil {
		return SwitchPreview{}, err
	}
	keys, _, err := readSwitchCredentials(ctx, tx, provider)
	if err != nil {
		return SwitchPreview{}, unavailable("读取北辰渠道失败", err)
	}
	from, to, err := selectSwitchCredentials(keys, scope)
	if err != nil {
		return SwitchPreview{}, err
	}
	if !switchCatalogReady(to) {
		return SwitchPreview{}, domain.NewError(domain.CodeInvalidTransition, "接替渠道的目录或同步状态已变化，请重新同步并预览")
	}
	plan, err := readCredentialRebind(ctx, tx, provider, *from, *to, true)
	if err != nil {
		return SwitchPreview{}, unavailable("重新核对北辰渠道切换失败", err)
	}
	if token != plan.PreviewToken || len(plan.Changes) == 0 {
		return SwitchPreview{}, domain.NewError(domain.CodeInvalidTransition, "渠道、报价、协议或绑定已变化，请重新预览")
	}
	if err := applyRebind(ctx, tx, provider, plan.Changes); err != nil {
		return SwitchPreview{}, err
	}
	if err := checkRebindTargets(ctx, tx, provider, &to.CredentialID, plan.Changes); err != nil {
		return SwitchPreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return SwitchPreview{}, unavailable("完成北辰渠道切换失败", err)
	}
	plan.Preview = false
	slog.InfoContext(ctx, "北辰渠道切换", "event", "beichen_credential_switched", "actor_user_id", actor, "provider_id", provider,
		"from_credential_id", scope.From, "to_credential_id", scope.To, "change_count", len(plan.Changes))
	return plan, nil
}

func readCredentialRebind(ctx context.Context, tx *sql.Tx, provider uuid.UUID, from, to SwitchCredential, lock bool) (SwitchPreview, error) {
	bindings, _, err := readRebindBindings(ctx, tx, provider, nil, lock)
	if err != nil {
		return SwitchPreview{}, err
	}
	entries, err := readRebindCatalog(ctx, tx, provider, &to.CredentialID)
	if err != nil {
		return SwitchPreview{}, err
	}
	var observed Overview
	if err := readModels(ctx, tx, provider, &observed); err != nil {
		return SwitchPreview{}, err
	}
	priced := map[string]bool{}
	for _, row := range observed.Models {
		if row.CredentialID == to.CredentialID {
			priced[row.Model] = row.activationPrice().ready()
		}
	}
	byUpstream, bySchool := map[string][]rebindCatalogEntry{}, map[uuid.UUID][]rebindCatalogEntry{}
	for _, entry := range entries {
		if !entry.catalogCurrent || *entry.route.CredentialID != to.CredentialID {
			continue
		}
		if !entry.explicit {
			name := strings.ToLower(entry.route.UpstreamModel)
			byUpstream[name] = append(byUpstream[name], entry)
		}
		if entry.school.id != uuid.Nil {
			bySchool[entry.school.id] = append(bySchool[entry.school.id], entry)
		}
	}
	plan := SwitchPreview{
		RebindPlan: RebindPlan{Preview: true, Changes: []RebindChange{}},
		Blockers:   []SwitchBlocker{},
	}
	for _, b := range bindings {
		if b.Before.Status != "active" || !sameRouteKey(b.Before.CredentialID, &from.CredentialID) {
			continue
		}
		entries := append(slices.Clone(bySchool[b.ModelID]), byUpstream[strings.ToLower(strings.TrimSpace(b.Before.UpstreamModel))]...)
		next, blocked := replacementRoute(b, entries, priced)
		if blocked != nil {
			plan.Blockers = append(plan.Blockers, *blocked)
			continue
		}
		if (from.Identity == "") != (to.Identity == "") {
			plan.Blockers = append(plan.Blockers, SwitchBlocker{b.SchoolModel, b.Adapter, "channel_mismatch",
				"原渠道和接替渠道的学校计费身份不同。请选择原 Key 后添加接替 Key。"})
			continue
		}
		if to.Identity != "" && to.Identity != "ready" {
			plan.Blockers = append(plan.Blockers, SwitchBlocker{b.SchoolModel, b.Adapter, "unverified_identity",
				"接替 Key 的官网账单归属待核对。请核对学校北辰账号中的 Key，再点一键同步。"})
			continue
		}
		b.Action, b.After = "update", next
		plan.Changes = append(plan.Changes, b)
	}
	slices.SortFunc(plan.Changes, compareRebindChanges)
	slices.SortFunc(plan.Blockers, func(a, b SwitchBlocker) int {
		if order := strings.Compare(a.SchoolModel, b.SchoolModel); order != 0 {
			return order
		}
		return strings.Compare(a.Adapter, b.Adapter)
	})
	// Include both Key revisions and the blocked routes so an old confirmation
	// cannot apply after the teacher's choices or the observed results change.
	raw, err := json.Marshal(struct {
		Plan         SwitchPreview
		TargetProbes []time.Time
	}{plan, rebindTargetProbeTimes(entries, plan.Changes)})
	if err != nil {
		return SwitchPreview{}, err
	}
	versions := fmt.Sprintf("%s/%s/%s/%s/%s:", provider, from.CredentialID, from.revision.UTC(), to.CredentialID, to.revision.UTC())
	plan.PreviewToken = fmt.Sprintf("%x", sha256.Sum256(append([]byte(versions), raw...)))
	return plan, nil
}

func replacementRoute(binding RebindChange, entries []rebindCatalogEntry, priced map[string]bool) (RebindRoute, *SwitchBlocker) {
	blocked := SwitchBlocker{SchoolModel: binding.SchoolModel, Adapter: binding.Adapter}
	blocked.Code, blocked.Message = "missing_model", "接替渠道的目录缺少此模型。请重新同步目录或核对模型别名。"
	if len(entries) > 0 {
		blocked.Code, blocked.Message = "missing_protocol", "接替渠道未登记这条线路使用的协议。请探活并核对上游支持的协议。"
	}
	var picked RebindRoute
	best := -1
	for _, entry := range entries {
		if !slices.Contains(entry.protocols, binding.Adapter) {
			continue
		}
		route := entry.route
		if !priced[route.UpstreamModel] {
			blocked.Code, blocked.Message = "missing_price", "接替渠道缺少可计算报价。请重新同步报价，或在北辰模型页设置校内价。"
			continue
		}
		rank := 0
		if entry.explicit {
			rank += 2
		}
		if strings.EqualFold(route.UpstreamModel, strings.TrimSpace(binding.Before.UpstreamModel)) {
			rank++
		}
		if rank > best {
			picked, best = route, rank
		}
	}
	if best < 0 {
		return RebindRoute{}, &blocked
	}
	picked.Priority = binding.Before.Priority
	return picked, nil
}

func readRebindBindings(ctx context.Context, tx *sql.Tx, provider uuid.UUID, school *uuid.UUID, lock bool) (map[rebindSlot]RebindChange, map[rebindSlot]int, error) {
	query := `
SELECT b.id, b.model_id, m.model_id::text, b.adapter, b.credential_id, c.label::text,
  b.upstream_model_name, b.priority, b.status
FROM model_bindings b JOIN models m ON m.id=b.model_id
LEFT JOIN provider_credentials c ON c.id=b.credential_id AND c.provider_id=b.provider_id
WHERE b.provider_id=$1 AND ($2::uuid IS NULL OR b.model_id=$2) ORDER BY b.id`
	if lock {
		query += " FOR UPDATE OF b"
	}
	rows, err := tx.QueryContext(ctx, query, provider, school)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	bindings := map[rebindSlot]RebindChange{}
	for rows.Next() {
		var change RebindChange
		var before RebindRoute
		if err := rows.Scan(&change.BindingID, &change.ModelID, &change.SchoolModel, &change.Adapter, &before.CredentialID,
			&before.CredentialLabel, &before.UpstreamModel, &before.Priority, &before.Status); err != nil {
			return nil, nil, err
		}
		change.Before = &before
		bindings[rebindSlot{change.ModelID, change.Adapter}] = change
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if school != nil {
		return bindings, nil, nil
	}
	rows, err = tx.QueryContext(ctx, `
SELECT model_id,adapter,max(priority) FROM model_bindings
WHERE provider_id<>$1 AND status='active' GROUP BY model_id,adapter`, provider)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	priorities := map[rebindSlot]int{}
	for rows.Next() {
		var slot rebindSlot
		var priority int
		if err := rows.Scan(&slot.model, &slot.adapter, &priority); err != nil {
			return nil, nil, err
		}
		priorities[slot] = priority
	}
	return bindings, priorities, rows.Err()
}

func readRebindCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID, credential *uuid.UUID) ([]rebindCatalogEntry, error) {
	rows, err := tx.QueryContext(ctx, `
WITH `+catalog.BeichenKeyEvidenceCTE+`
SELECT a.model,k.credential_id,c.label::text,k.raw_entry,to_json(k.supported_protocols),
  `+catalog.BeichenKeyListsModelSQL+` AND `+catalog.BeichenCatalogCurrentSQL+` AND `+catalog.BeichenGroupAllowsRouteSQL+`
    AND octet_length(c.credential_ciphertext)>0 AND `+catalog.BeichenCatalogAuthReadySQL+`,
  COALESCE((SELECT jsonb_object_agg(r.protocol,jsonb_build_object('checked_at',r.checked_at,
    'blocked',COALESCE(`+catalog.BeichenProbeBlocksRouteSQL+`,false)))
    FROM upstream_probes r WHERE `+catalog.BeichenCurrentCatalogProbeSQL+`),'{}'::jsonb),
  alias.school_model IS NOT NULL,m.id,m.model_id::text
FROM provider_model_catalog k
JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
JOIN beichen_key_evidence e ON e.credential_id=c.id
CROSS JOIN LATERAL (SELECT k.credential_id,k.model_id::text AS model) a
`+joinModelAliasSQL+`
LEFT JOIN models m ON m.model_id=COALESCE(alias.school_model,a.model)::citext
WHERE k.provider_id=$1 AND k.status='active' AND c.status='active'
  AND ($2::uuid IS NULL OR c.id=$2) ORDER BY k.credential_id,a.model`, provider, credential)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []rebindCatalogEntry
	for rows.Next() {
		var entry rebindCatalogEntry
		var schoolID *uuid.UUID
		var schoolName *string
		var raw, protocols, probes []byte
		if err := rows.Scan(&entry.route.UpstreamModel, &entry.route.CredentialID, &entry.route.CredentialLabel,
			&raw, &protocols, &entry.catalogCurrent, &probes, &entry.explicit, &schoolID, &schoolName); err != nil {
			return nil, err
		}
		if schoolID != nil {
			entry.school = rebindSchool{*schoolID, *schoolName}
		}
		stored, err := storedCatalogEntry(entry.route.UpstreamModel, raw, protocols)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(probes, &entry.probes); err != nil {
			return nil, err
		}
		entry.protocols = slices.DeleteFunc(catalog.RegisteredProtocols(stored), func(protocol string) bool {
			return entry.probes[protocol].Blocked
		})
		entry.route.Status = "active"
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// A provider rebuild operates on school models with an active binding.
// Explicit aliases can change those routes. Directory omissions alone leave
// the bindings in place.
func planRebind(bindings map[rebindSlot]RebindChange, entries []rebindCatalogEntry, priorities map[rebindSlot]int) ([]RebindChange, error) {
	schools := map[uuid.UUID]rebindSchool{}
	anchors := map[string][]RebindChange{}
	for _, b := range bindings {
		if b.Before.Status == "active" {
			schools[b.ModelID] = rebindSchool{b.ModelID, b.SchoolModel}
			name := strings.ToLower(strings.TrimSpace(b.Before.UpstreamModel))
			anchors[name] = append(anchors[name], b)
		}
	}
	type rankedChange struct {
		change    RebindChange
		rank      int
		ambiguous bool
	}
	chosen := map[rebindSlot]rankedChange{}
	for _, entry := range entries {
		if !entry.catalogCurrent {
			continue
		}
		targets := map[uuid.UUID]bool{}
		for _, anchor := range anchors[strings.ToLower(entry.route.UpstreamModel)] {
			if sameRouteKey(anchor.Before.CredentialID, entry.route.CredentialID) || anchor.Before.CredentialID == nil {
				targets[anchor.ModelID] = anchor.Before.CredentialID != nil || targets[anchor.ModelID]
			}
		}
		if entry.explicit && schools[entry.school.id].id != uuid.Nil {
			targets[entry.school.id] = targets[entry.school.id]
		}
		for _, adapter := range entry.protocols {
			for id, pinned := range targets {
				school := schools[id]
				slot := rebindSlot{id, adapter}
				change := bindings[slot]
				route := entry.route
				explicit := entry.explicit && entry.school.id == id
				rank := 1
				if explicit {
					rank = 4
				}
				if before := change.Before; before != nil {
					if before.Status != "active" {
						continue
					}
					change.Action = "update"
					route.Priority = before.Priority
					if strings.EqualFold(strings.TrimSpace(before.UpstreamModel), route.UpstreamModel) &&
						(before.CredentialID == nil || sameRouteKey(before.CredentialID, route.CredentialID)) {
						route, rank = *before, 3
						if explicit {
							rank = 5
						}
					} else if !explicit {
						continue
					}
				} else {
					if entry.explicit && !explicit || !pinned && !explicit {
						continue
					}
					if pinned && !explicit {
						rank = 2
					}
					priority := min(priorities[slot], math.MaxInt32-1) + 1
					route.Priority = max(catalog.DefaultBindingPriority(modelbinding.Adapter(adapter)), priority)
					change = RebindChange{ModelID: school.id, SchoolModel: school.name, Adapter: adapter, Action: "add"}
				}
				change.After = route
				if previous, exists := chosen[slot]; !exists || previous.rank < rank {
					chosen[slot] = rankedChange{change: change, rank: rank}
				} else if previous.rank == rank && !sameRebindRoute(previous.change.After, route) {
					previous.ambiguous = true
					chosen[slot] = previous
				}
			}
		}
	}
	changes := []RebindChange{}
	for _, candidate := range chosen {
		if candidate.ambiguous {
			return nil, domain.NewError(domain.CodeInvalidTransition, "重建包含无法唯一确认的线路，请按校内模型和渠道定向处理")
		}
		c := candidate.change
		if c.Before == nil || !sameRebindRoute(*c.Before, c.After) {
			changes = append(changes, c)
		}
	}
	slices.SortFunc(changes, compareRebindChanges)
	return changes, nil
}

func planBindingRestore(bindings map[rebindSlot]RebindChange, entries []rebindCatalogEntry, scope bindingRestore) ([]RebindChange, error) {
	changes := []RebindChange{}
	for _, b := range bindings {
		if b.ModelID != scope.schoolModelID || b.Before.Status != "inactive" || sameRouteKey(b.Before.CredentialID, &scope.credentialID) {
			continue
		}
		var route RebindRoute
		best, ambiguous := 0, false
		for _, entry := range entries {
			if !entry.catalogCurrent || !sameRouteKey(entry.route.CredentialID, &scope.credentialID) ||
				!slices.Contains(entry.protocols, b.Adapter) || entry.explicit && entry.school.id != b.ModelID {
				continue
			}
			rank := 0
			if entry.school.id == b.ModelID {
				rank = 1
			}
			if strings.EqualFold(strings.TrimSpace(b.Before.UpstreamModel), entry.route.UpstreamModel) {
				rank = 2
			}
			if entry.explicit {
				rank += 3
			}
			if rank > best {
				route, best, ambiguous = entry.route, rank, false
			} else if rank > 0 && rank == best && !sameRebindRoute(route, entry.route) {
				ambiguous = true
			}
		}
		if ambiguous {
			return nil, domain.NewError(domain.CodeInvalidTransition, "接替渠道有多个匹配型号，请先核对该校内模型的别名")
		}
		if best > 0 {
			route.Priority = b.Before.Priority
			b.Action, b.After = "enable", route
			changes = append(changes, b)
		}
	}
	if len(changes) == 0 {
		return nil, domain.NewError(domain.CodeInvalidTransition, "所选模型没有可在接替渠道恢复的停用线路")
	}
	slices.SortFunc(changes, compareRebindChanges)
	return changes, nil
}

func sameRebindRoute(a, b RebindRoute) bool {
	return a.UpstreamModel == b.UpstreamModel && a.Status == b.Status && sameRouteKey(a.CredentialID, b.CredentialID)
}

func compareRebindChanges(a, b RebindChange) int {
	if order := strings.Compare(strings.ToLower(a.SchoolModel), strings.ToLower(b.SchoolModel)); order != 0 {
		return order
	}
	if order := strings.Compare(a.SchoolModel, b.SchoolModel); order != 0 {
		return order
	}
	return strings.Compare(a.Adapter, b.Adapter)
}

func sameRouteKey(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func applyRebind(ctx context.Context, tx *sql.Tx, provider uuid.UUID, changes []RebindChange) error {
	for _, c := range changes {
		var result sql.Result
		var err error
		r := c.After
		if c.BindingID == nil {
			result, err = tx.ExecContext(ctx, `
INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,now(),now()) ON CONFLICT(model_id,provider_id,adapter) DO NOTHING`,
				c.ModelID, provider, r.CredentialID, r.UpstreamModel, c.Adapter, r.Priority)
		} else {
			result, err = tx.ExecContext(ctx, `
UPDATE model_bindings SET credential_id=$2,upstream_model_name=$3,status=$4,updated_at=now()
WHERE id=$1 AND provider_id=$5 AND model_id=$6 AND adapter=$7 AND status=$8
  AND credential_id IS NOT DISTINCT FROM $9::uuid AND upstream_model_name=$10 AND priority=$11`,
				*c.BindingID, r.CredentialID, r.UpstreamModel, r.Status, provider, c.ModelID, c.Adapter, c.Before.Status,
				c.Before.CredentialID, c.Before.UpstreamModel, c.Before.Priority)
		}
		if err != nil {
			return unavailable("保存北辰重建绑定失败", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return domain.NewError(domain.CodeInvalidTransition, "绑定已变化，请重新预览")
		}
	}
	return nil
}
