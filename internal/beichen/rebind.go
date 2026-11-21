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
	Preview      bool           `json:"preview"`
	PreviewToken string         `json:"preview_token"`
	Changes      []RebindChange `json:"changes"`
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
	explicit  bool
	school    rebindSchool
	route     RebindRoute
	protocols []string
}

func (s *Service) Rebind(ctx context.Context, provider, actor uuid.UUID, preview bool, token string) (RebindPlan, error) {
	if provider == uuid.Nil || actor == uuid.Nil {
		return RebindPlan{}, invalid("需要有效的供应商和教师 ID")
	}
	if !preview {
		if _, err := hex.DecodeString(token); len(token) != sha256.Size*2 || err != nil {
			return RebindPlan{}, invalid("请先预览绑定重建，再确认执行")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if !preview {
		options = nil
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
	} else if err := lockAliasProvider(ctx, tx, provider); err != nil {
		return RebindPlan{}, err
	}
	var ready bool
	err = tx.QueryRowContext(ctx, `
SELECT p.status='active' AND NOT EXISTS (
  SELECT 1 FROM provider_credentials c
  LEFT JOIN beichen_datasets d ON d.credential_id=c.id AND d.kind='catalog'
  WHERE c.provider_id=p.id AND c.status='active' AND NOT `+keyCatalogCurrentSQL+`
) FROM providers p WHERE p.id=$1`, provider).Scan(&ready)
	if err != nil {
		return RebindPlan{}, unavailable("核对北辰渠道目录失败", err)
	}
	if !ready {
		return RebindPlan{}, domain.NewError(domain.CodeInvalidTransition, "请启用北辰供应商并成功同步所有已启用渠道的目录，再重建绑定")
	}
	bindings, priorities, err := readRebindBindings(ctx, tx, provider, !preview)
	if err != nil {
		return RebindPlan{}, unavailable("读取北辰绑定失败", err)
	}
	entries, err := readRebindCatalog(ctx, tx, provider)
	if err != nil {
		return RebindPlan{}, unavailable("读取北辰重建目录失败", err)
	}
	changes := planRebind(bindings, entries, priorities)
	raw, err := json.Marshal(changes)
	if err != nil {
		return RebindPlan{}, unavailable("生成北辰重建预览失败", err)
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(append([]byte(provider.String()), raw...)))
	view := RebindPlan{Preview: preview, PreviewToken: fingerprint, Changes: changes}
	if !preview {
		if token != fingerprint {
			return RebindPlan{}, domain.NewError(domain.CodeInvalidTransition, "目录、别名或绑定已变化，请重新预览")
		}
		if err := applyRebind(ctx, tx, provider, changes); err != nil {
			return RebindPlan{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RebindPlan{}, unavailable("完成北辰绑定重建失败", err)
	}
	if !preview && len(changes) > 0 {
		slog.InfoContext(ctx, "北辰绑定重建", "event", "beichen_bindings_rebuilt", "actor_user_id", actor,
			"provider_id", provider, "change_count", len(changes), "preview_token", fingerprint, "changes", string(raw))
	}
	return view, nil
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
	if err := lockAliasProvider(ctx, tx, provider); err != nil {
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
	if err := tx.Commit(); err != nil {
		return SwitchPreview{}, unavailable("完成北辰渠道切换失败", err)
	}
	plan.Preview = false
	slog.InfoContext(ctx, "北辰渠道切换", "event", "beichen_credential_switched", "actor_user_id", actor, "provider_id", provider,
		"from_credential_id", scope.From, "to_credential_id", scope.To, "preview_token", token, "change_count", len(plan.Changes))
	return plan, nil
}

func readCredentialRebind(ctx context.Context, tx *sql.Tx, provider uuid.UUID, from, to SwitchCredential, lock bool) (SwitchPreview, error) {
	bindings, _, err := readRebindBindings(ctx, tx, provider, lock)
	if err != nil {
		return SwitchPreview{}, err
	}
	entries, err := readRebindCatalog(ctx, tx, provider)
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
			priced[row.Model] = row.PriceCovered
		}
	}
	byUpstream, bySchool := map[string][]rebindCatalogEntry{}, map[uuid.UUID][]rebindCatalogEntry{}
	for _, entry := range entries {
		if *entry.route.CredentialID != to.CredentialID {
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
	raw, err := json.Marshal(plan)
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

func readRebindBindings(ctx context.Context, tx *sql.Tx, provider uuid.UUID, lock bool) (map[rebindSlot]RebindChange, map[rebindSlot]int, error) {
	query := `
SELECT b.id, b.model_id, m.model_id::text, b.adapter, b.credential_id, c.label::text,
  b.upstream_model_name, b.priority, b.status
FROM model_bindings b JOIN models m ON m.id=b.model_id
LEFT JOIN provider_credentials c ON c.id=b.credential_id AND c.provider_id=b.provider_id
WHERE b.provider_id=$1 ORDER BY b.id`
	if lock {
		query += " FOR UPDATE OF b"
	}
	rows, err := tx.QueryContext(ctx, query, provider)
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

func readRebindCatalog(ctx context.Context, tx *sql.Tx, provider uuid.UUID) ([]rebindCatalogEntry, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT a.model,a.credential_id,c.label::text,a.raw_entry,to_json(a.supported_protocols),
  alias.school_model IS NOT NULL,m.id,m.model_id::text
FROM (SELECT k.model_id::text AS model,k.* FROM provider_model_catalog k WHERE k.provider_id=$1 AND k.status='active') a
JOIN provider_credentials c ON c.id=a.credential_id AND c.provider_id=a.provider_id
`+joinModelAliasSQL+`
LEFT JOIN models m ON m.model_id=COALESCE(alias.school_model,a.model)::citext
WHERE c.status='active' ORDER BY a.credential_id,a.model`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []rebindCatalogEntry
	for rows.Next() {
		var entry rebindCatalogEntry
		var schoolID *uuid.UUID
		var schoolName *string
		var raw, protocols []byte
		if err := rows.Scan(&entry.route.UpstreamModel, &entry.route.CredentialID, &entry.route.CredentialLabel,
			&raw, &protocols, &entry.explicit, &schoolID, &schoolName); err != nil {
			return nil, err
		}
		if schoolID != nil {
			entry.school = rebindSchool{*schoolID, *schoolName}
		}
		stored, err := storedCatalogEntry(entry.route.UpstreamModel, raw, protocols)
		if err != nil {
			return nil, err
		}
		entry.protocols = catalog.RegisteredProtocols(stored)
		entry.route.Status = "active"
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func planRebind(bindings map[rebindSlot]RebindChange, entries []rebindCatalogEntry, priorities map[rebindSlot]int) []RebindChange {
	type upstreamProtocol struct {
		model, adapter string
	}
	// Preserve an older hand-configured name for the protocol it actually
	// bound. Only an alias or an exact school name may add new protocols.
	legacy := map[upstreamProtocol][]rebindSchool{}
	for _, b := range bindings {
		model := strings.ToLower(strings.TrimSpace(b.Before.UpstreamModel))
		slot := upstreamProtocol{model, b.Adapter}
		legacy[slot] = append(legacy[slot], rebindSchool{b.ModelID, b.SchoolModel})
	}
	type rankedChange struct {
		change RebindChange
		rank   int
	}
	chosen := map[rebindSlot]rankedChange{}
	for _, entry := range entries {
		for _, adapter := range entry.protocols {
			var targets []rebindSchool
			if entry.school.id != uuid.Nil {
				targets = append(targets, entry.school)
			}
			if !entry.explicit {
				targets = append(targets, legacy[upstreamProtocol{strings.ToLower(entry.route.UpstreamModel), adapter}]...)
			}
			for _, school := range targets {
				slot := rebindSlot{school.id, adapter}
				change := bindings[slot]
				route := entry.route
				rank := 0
				if entry.explicit {
					rank += 4
				}
				if change.Before != nil {
					route.Priority = change.Before.Priority
					if strings.EqualFold(strings.TrimSpace(change.Before.UpstreamModel), route.UpstreamModel) {
						rank++
						if sameRouteKey(change.Before.CredentialID, route.CredentialID) {
							rank++
						}
					}
					change.Action = "update"
					if change.Before.Status != "active" {
						change.Action = "enable"
					}
				} else {
					priority := min(priorities[slot], math.MaxInt32-1) + 1
					route.Priority = max(catalog.DefaultBindingPriority(modelbinding.Adapter(adapter)), priority)
					change = RebindChange{ModelID: school.id, SchoolModel: school.name, Adapter: adapter, Action: "add"}
				}
				change.After = route
				if previous, exists := chosen[slot]; !exists || previous.rank < rank {
					chosen[slot] = rankedChange{change, rank}
				}
			}
		}
	}
	changes := []RebindChange{}
	for _, candidate := range chosen {
		c := candidate.change
		if c.Before == nil || c.Before.Status != c.After.Status || c.Before.UpstreamModel != c.After.UpstreamModel ||
			!sameRouteKey(c.Before.CredentialID, c.After.CredentialID) {
			changes = append(changes, c)
		}
	}
	for slot, change := range bindings {
		if _, kept := chosen[slot]; !kept && change.Before.Status == "active" {
			change.Action, change.After = "disable", *change.Before
			change.After.Status = "inactive"
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, compareRebindChanges)
	return changes
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
WHERE id=$1 AND provider_id=$5`, *c.BindingID, r.CredentialID, r.UpstreamModel, r.Status, provider)
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
