package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

// A disabled audit principal with an invalid password hash and no login profiles.
// Its UUID is reserved so a matching email alone never attributes writes to a person.
var provisionActor = uuid.MustParse(beichen.ProvisionActorID)

var beichenAliases = map[string]string{"Gemini": "哈吉米", "GM1": "国模", "G3": "gpt", "CC1": "cc不满血"}

type provisionChannel struct {
	Channel       string `json:"channel"`
	Status        string `json:"status,omitempty"`
	APIKey        string `json:"api_key"`
	GroupID       int64  `json:"group_id"`
	UpstreamKeyID int64  `json:"upstream_key_id"`
	access        beichen.PanelAccessInput
}

// A duplicate member in this secret could silently pair a key with the wrong account.
func (c *provisionChannel) UnmarshalJSON(raw []byte) error {
	bad := errors.New("invalid channel object")
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return bad
	}
	fields := map[string]any{"channel": &c.Channel, "status": &c.Status, "api_key": &c.APIKey,
		"group_id": &c.GroupID, "upstream_key_id": &c.UpstreamKeyID}
	for d.More() {
		field, err := d.Token()
		if err != nil {
			return bad
		}
		key, ok := field.(string)
		if !ok || fields[key] == nil || d.Decode(fields[key]) != nil {
			return bad
		}
		delete(fields, key)
	}
	groupMissing, idMissing := fields["group_id"] != nil, fields["upstream_key_id"] != nil
	if end, err := d.Token(); err != nil || end != json.Delim('}') || fields["channel"] != nil ||
		(!c.skip() && fields["api_key"] != nil) ||
		groupMissing != idMissing || (!groupMissing && (c.GroupID <= 0 || c.UpstreamKeyID <= 0)) {
		return bad
	}
	return nil
}

func (c provisionChannel) skip() bool {
	return c.Status == "disabled" || c.Status == "retired"
}

func (s *Service) parseProvisionChannels(raw, token string) ([]provisionChannel, error) {
	var channels []provisionChannel
	if len(raw) > 64*1024 || json.Unmarshal([]byte(raw), &channels) != nil || len(channels) < 3 || len(channels) > len(beichenAliases) {
		return nil, errors.New("BEICHEN_CHANNELS_JSON 须配置 GM1、G3、CC1，Gemini 可省略")
	}
	names, keys, ids := map[string]bool{}, map[string]bool{}, map[int64]bool{}
	for i := range channels {
		c := &channels[i]
		if _, ok := beichenAliases[c.Channel]; !ok || names[c.Channel] {
			return nil, errors.New("仅支持 Gemini、GM1、G3、CC1，每个渠道最多配置一次")
		}
		names[c.Channel] = true
		if c.skip() {
			continue
		}
		if c.Status != "" && c.Status != "active" {
			return nil, fmt.Errorf("%s：状态须为 active、disabled 或 retired", c.Channel)
		}
		if c.UpstreamKeyID != 0 {
			var err error
			c.access, err = s.beichen.DeploymentPanelAccess(c.GroupID, c.UpstreamKeyID, token)
			if err != nil {
				return nil, fmt.Errorf("%s：%w", c.Channel, err)
			}
		}
		if c.APIKey == "" || len(c.APIKey) > 4096 || strings.IndexFunc(c.APIKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 ||
			c.APIKey == token || keys[c.APIKey] || (c.UpstreamKeyID != 0 && ids[c.UpstreamKeyID]) {
			return nil, fmt.Errorf("%s：推理 Key 或上游 Key ID 无效、重复或与面板令牌相同", c.Channel)
		}
		keys[c.APIKey], ids[c.UpstreamKeyID] = true, true
	}
	for _, required := range []string{"GM1", "G3", "CC1"} {
		if !names[required] {
			return nil, fmt.Errorf("BEICHEN_CHANNELS_JSON 缺少 %s 渠道", required)
		}
	}
	return channels, nil
}

type BeichenProvisionResult struct {
	Bound          int
	Disabled       []string
	AwaitingAccess []string
}

// ProvisionBeichenChannels is called once before starting either upstream worker.
// A configured deployment can retry the entire transaction after any error.
func (s *Service) ProvisionBeichenChannels(ctx context.Context, raw, token string) (BeichenProvisionResult, error) {
	var result BeichenProvisionResult
	channels, err := s.parseProvisionChannels(raw, token)
	if err != nil {
		return result, err
	}
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return result, errors.New("北辰预配置无法开启事务")
	}
	defer tx.Rollback()
	// Serialize bootstraps and provider creation, including multiple URL spellings.
	// The selected provider row lock also fences FK inserts and panel/sync writers.
	if _, err := tx.ExecContext(ctx, "LOCK TABLE providers IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return result, errors.New("北辰预配置无法锁定供应商")
	}
	provider, active, err := provisionProvider(ctx, tx)
	if err != nil {
		return result, err
	}
	if !active {
		for _, c := range channels {
			result.Disabled = append(result.Disabled, c.Channel)
		}
		return result, nil
	}
	if err := ensureProvisionActor(ctx, tx); err != nil {
		return result, err
	}
	existing, err := s.provisionCredentials(ctx, tx, provider)
	if err != nil {
		return result, err
	}
	for _, c := range channels {
		if c.skip() {
			result.Disabled = append(result.Disabled, c.Channel)
			continue
		}
		disabled, err := s.provisionChannel(ctx, tx, provider, existing, &c)
		if err != nil {
			return BeichenProvisionResult{}, fmt.Errorf("%s：%w", c.Channel, err)
		}
		if disabled {
			result.Disabled = append(result.Disabled, c.Channel)
		} else if !c.access.Enabled {
			result.AwaitingAccess = append(result.AwaitingAccess, c.Channel)
		} else {
			result.Bound++
		}
	}
	if err := tx.Commit(); err != nil {
		return BeichenProvisionResult{}, errors.New("北辰预配置事务提交未确认")
	}
	return result, nil
}

func provisionProvider(ctx context.Context, tx *sql.Tx) (uuid.UUID, bool, error) {
	// Match the canonical host and the case-sensitive path accepted by migration 0037.
	rows, err := tx.QueryContext(ctx, `
SELECT id,status FROM providers WHERE regexp_replace(btrim(base_url),
  '^https://api[.]beichenapi[.]com(?=/|$)', 'https://api.beichenapi.com','i')
  IN ('https://api.beichenapi.com','https://api.beichenapi.com/',
      'https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
ORDER BY id LIMIT 2 FOR UPDATE`)
	if err != nil {
		return uuid.Nil, false, errors.New("读取北辰供应商失败")
	}
	defer rows.Close()
	var id uuid.UUID
	var status string
	for rows.Next() {
		if id != uuid.Nil {
			return uuid.Nil, false, errors.New("多个供应商使用北辰地址，请先确认唯一供应商")
		}
		if err := rows.Scan(&id, &status); err != nil {
			return uuid.Nil, false, errors.New("读取北辰供应商失败")
		}
	}
	if rows.Err() != nil || id == uuid.Nil {
		return uuid.Nil, false, errors.New("未找到可用的北辰供应商记录，请检查迁移 0037 和地址")
	}
	return id, status == "active", nil
}

func ensureProvisionActor(ctx context.Context, tx *sql.Tx) error {
	const email = "beichen-provision@nebula.invalid"
	_, err := tx.ExecContext(ctx, `
INSERT INTO users (id,name,email,password_hash,role,status,created_at,updated_at)
VALUES ($1,'北辰部署审计',$2,'!','student','disabled',now(),now()) ON CONFLICT DO NOTHING`, provisionActor, email)
	if err != nil {
		return errors.New("北辰部署审计身份写入失败")
	}
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND email=$2 AND password_hash='!'
  AND role='student' AND status='disabled'
  AND NOT EXISTS (SELECT 1 FROM mentor_profiles WHERE user_id=$1)
  AND NOT EXISTS (SELECT 1 FROM student_profiles WHERE user_id=$1) FOR UPDATE`, provisionActor, email).Scan(&id)
	if err != nil {
		return errors.New("北辰部署专用审计身份缺失或冲突")
	}
	return nil
}

type provisionCredential struct {
	id                     uuid.UUID
	label, status, channel string
	encrypted, fingerprint []byte
	revision               time.Time
	actor                  uuid.NullUUID
	group, upstreamID      sql.NullInt64
	panelToken             []byte
	panelRevision          sql.NullTime
}

func (s *Service) provisionCredentials(ctx context.Context, tx *sql.Tx, provider uuid.UUID) ([]provisionCredential, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id,c.label,c.credential_ciphertext,c.status,c.updated_at,
       a.updated_by,a.group_id,a.upstream_key_id,a.token_ciphertext,a.updated_at,j.key_fingerprint,COALESCE(j.channel,'')
FROM provider_credentials c LEFT JOIN beichen_panel_access a ON a.credential_id=c.id
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.provider_id=$1 ORDER BY c.id FOR UPDATE OF c`, provider)
	if err != nil {
		return nil, errors.New("读取北辰现有凭据失败")
	}
	defer rows.Close()
	var records []provisionCredential
	for rows.Next() {
		var row provisionCredential
		if err := rows.Scan(&row.id, &row.label, &row.encrypted, &row.status, &row.revision, &row.actor, &row.group,
			&row.upstreamID, &row.panelToken, &row.panelRevision, &row.fingerprint, &row.channel); err != nil {
			return nil, errors.New("读取北辰现有凭据失败")
		}
		records = append(records, row)
	}
	if rows.Err() != nil {
		return nil, errors.New("读取北辰现有凭据失败")
	}
	return records, nil
}

func (s *Service) provisionChannel(ctx context.Context, tx *sql.Tx, provider uuid.UUID, records []provisionCredential, c *provisionChannel) (disabled bool, err error) {
	fingerprint := sha256.Sum256([]byte(c.APIKey))
	var current *provisionCredential
	archivedKey := false
	for i := range records {
		r := &records[i]
		label := strings.TrimSpace(r.label)
		if strings.EqualFold(label, c.Channel) || strings.EqualFold(label, beichenAliases[c.Channel]) {
			if current != nil {
				return false, errors.New("渠道名称与历史标签命中多条凭据")
			}
			current = r
		} else if r.status != "active" && r.channel == c.Channel && bytes.Equal(r.fingerprint, fingerprint[:]) {
			archivedKey = true
		} else if bytes.Equal(r.fingerprint, fingerprint[:]) || (c.UpstreamKeyID != 0 && r.upstreamID.Int64 == c.UpstreamKeyID &&
			!(r.status == "inactive" && r.actor.UUID == provisionActor && label == c.archivedLabel(r.id))) {
			return false, errors.New("此推理 Key 或上游 Key ID 已绑定到其他凭据")
		}
	}
	if archivedKey {
		if current == nil || current.status != "active" {
			return true, nil
		}
		return false, errors.New("旧部署配置引用已归档 Key，请保留当前渠道凭据")
	}
	if current != nil {
		if current.status != "active" || (current.actor.Valid && current.actor.UUID != provisionActor && !current.group.Valid) {
			return true, nil
		}
		key, err := s.security.DecryptCredential(current.encrypted)
		if err != nil {
			return false, errors.New("无法校验此渠道已有凭据，请检查原加密主密钥")
		}
		if key == c.APIKey {
			return false, s.bindProvisionPanel(ctx, tx, *current, c)
		}
		if current.actor.UUID != provisionActor {
			return false, errors.New("已有渠道推理 Key 不同，请核对教师配置")
		}
		// New upstream credentials need their own UUID for historical usage, prices
		// and receipts. Explicit model bindings retain their original owner.
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_credentials
SET label=$2,status='inactive',updated_at=clock_timestamp(),retired_at=clock_timestamp()
WHERE id=$1`,
			current.id, c.archivedLabel(current.id)); err != nil {
			return false, errors.New("旧凭据归档失败，请检查标签冲突")
		}
		if err := beichen.RetireCredentialCatalogTx(ctx, tx, current.id); err != nil {
			return false, errors.New("旧凭据目录停用失败")
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return false, errors.New("生成凭据编号失败")
	}
	ciphertext, err := s.security.EncryptCredential(c.APIKey)
	if err != nil {
		return false, errors.New("加密推理凭据失败")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_credentials (id,provider_id,label,credential_ciphertext,status,created_at,updated_at)
VALUES ($1,$2,$3,$4,'active',clock_timestamp(),clock_timestamp())`, id, provider, c.Channel, ciphertext)
	if err != nil {
		return false, errors.New("新增北辰凭据失败")
	}
	if err := s.bindProvisionPanel(ctx, tx, provisionCredential{id: id}, c); err != nil {
		return false, err
	}
	if err := beichen.QueueCredentialSyncTx(ctx, tx, id); err != nil {
		return false, errors.New("安排新北辰凭据同步失败")
	}
	return false, nil
}

func (c provisionChannel) archivedLabel(id uuid.UUID) string {
	return c.Channel + " (archived " + id.String() + ")"
}

func (s *Service) bindProvisionPanel(ctx context.Context, tx *sql.Tx, r provisionCredential, c *provisionChannel) error {
	fingerprint := sha256.Sum256([]byte(c.APIKey))
	_, err := tx.ExecContext(ctx, `INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES ($1,$2,$3,$4) ON CONFLICT (credential_id) DO UPDATE SET
channel=excluded.channel,key_fingerprint=excluded.key_fingerprint,discover_ids=excluded.discover_ids,
checked_at=NULL,upstream_user_id=NULL,discovery_error=NULL
WHERE (beichen_provision_channels.channel,beichen_provision_channels.key_fingerprint,beichen_provision_channels.discover_ids)
 IS DISTINCT FROM (excluded.channel,excluded.key_fingerprint,excluded.discover_ids)`, r.id, c.Channel, fingerprint[:], c.UpstreamKeyID == 0)
	if err != nil {
		return errors.New("保存渠道采集配置失败，请检查迁移 0041")
	}
	if c.UpstreamKeyID == 0 && r.group.Valid && r.upstreamID.Valid {
		if !s.beichen.WebSessionConfigured() {
			return nil
		}
		desired, err := s.beichen.DeploymentPanelAccess(r.group.Int64, r.upstreamID.Int64, "")
		if err != nil {
			return err
		}
		matches, err := s.beichen.PanelSourceMatches(r.id, r.panelToken, desired)
		if err != nil {
			return errors.New("无法校验已有网站账单绑定")
		}
		if matches && r.revision.Equal(r.panelRevision.Time) {
			c.access = desired
			return nil
		}
		if r.actor.UUID != provisionActor {
			return errors.New("已有教师授权不同，请先核对")
		}
		// Never carry another account's numeric IDs into automatic discovery.
		return s.beichen.WritePanelAccessTx(ctx, tx, r.id, provisionActor, beichen.PanelAccessInput{})
	}
	if r.actor.Valid && !c.access.Enabled {
		if c.UpstreamKeyID != 0 && (r.group.Valid && r.group.Int64 != c.GroupID || r.upstreamID.Valid && r.upstreamID.Int64 != c.UpstreamKeyID) {
			return errors.New("现有授权与渠道的分组或上游 Key ID 不符")
		}
		return nil
	}
	// A deployment-owned empty row marks a catalog-only key. Teacher disabled
	// rows are handled by the caller and never promoted.
	if r.group.Valid {
		sameToken, err := s.beichen.PanelSourceMatches(r.id, r.panelToken, c.access)
		if err != nil {
			return errors.New("无法校验已有面板授权")
		}
		sameAccess := sameToken && r.group.Int64 == c.GroupID
		if sameAccess && r.upstreamID.Int64 == c.UpstreamKeyID && r.revision.Equal(r.panelRevision.Time) {
			return nil
		}
		// Complete a teacher's matching price-only binding, but never replace
		// a different explicit teacher authorization with deployment defaults.
		if r.actor.UUID != provisionActor && !(sameAccess && !r.upstreamID.Valid && r.revision.Equal(r.panelRevision.Time)) {
			return errors.New("已有教师面板授权或版本不同，请核对后再部署")
		}
	}
	if err := s.beichen.WritePanelAccessTx(ctx, tx, r.id, provisionActor, c.access); err != nil {
		return errors.New("保存北辰面板授权与逐笔绑定失败")
	}
	return nil
}
