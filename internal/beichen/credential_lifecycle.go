package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

type CredentialEdit struct {
	Label, GroupHint, Status *string
	Channel                  string
	Ciphertext               []byte
	Replaces                 *uuid.UUID
	Fingerprint              []byte
}

func IsSchoolChannel(channel string) bool {
	_, ok := beichenKeyAliases[channel]
	return ok
}

// SaveCredential serializes registration, status changes, and rebinds.
func (s *Service) SaveCredential(ctx context.Context, providerID, credentialID uuid.UUID, edit CredentialEdit) (*ent.ProviderCredential, error) {
	if s.db == nil {
		return nil, unavailable("北辰凭据管理未配置", nil)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, unavailable("开启北辰凭据事务失败", err)
	}
	defer tx.Rollback()
	if err := lockAliasProvider(ctx, tx, providerID); err != nil {
		return nil, err
	}
	row := &ent.ProviderCredential{ProviderID: providerID}
	values := []any{&row.ID, &row.Label, &row.GroupHint, &row.Status, &row.CreatedAt, &row.UpdatedAt, &row.CredentialCiphertext}
	creating := credentialID == uuid.Nil
	var previousStatus string
	channel := edit.Channel
	if channel != "" && (!creating || edit.Replaces != nil || !IsSchoolChannel(channel)) {
		return nil, invalid("请选择一个有效的新学校渠道：CC1、G3、GM1 或 Gemini")
	}
	if !creating && len(edit.Ciphertext) != 0 {
		if edit.Status == nil || *edit.Status != "active" {
			return nil, invalid("原地更换 Key 仅适用于重新启用停用的 Gemini 渠道")
		}
		if err := s.RestoreGeminiCredentialTx(ctx, tx, providerID, credentialID, edit); err != nil {
			return nil, err
		}
		err = tx.QueryRowContext(ctx, `
SELECT id,label,group_hint,status,created_at,updated_at,credential_ciphertext
FROM provider_credentials WHERE id=$1`, credentialID).Scan(values...)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			return nil, unavailable("保存恢复后的 Gemini 渠道失败", err)
		}
		return row, nil
	}
	if creating {
		if edit.Label == nil || edit.Status == nil || len(edit.Ciphertext) == 0 {
			return nil, invalid("请填写新渠道名称和 Key")
		}
		credentialID, err = uuid.NewV7()
		if err != nil {
			return nil, unavailable("生成北辰凭据编号失败", err)
		}
		if edit.Replaces != nil {
			channel, err = readPredecessorChannel(ctx, tx, providerID, *edit.Replaces)
			if err != nil {
				return nil, err
			}
		}
		err = tx.QueryRowContext(ctx, `
INSERT INTO provider_credentials(id,provider_id,label,group_hint,credential_ciphertext,status,created_at,updated_at,retired_at)
VALUES($1,$2,$3,NULLIF($4::text,''),$5,$6::text,statement_timestamp(),statement_timestamp(),
 CASE WHEN $6::text<>'active' THEN statement_timestamp() END)
RETURNING id,label,group_hint,status,created_at,updated_at,credential_ciphertext`,
			credentialID, providerID, edit.Label, edit.GroupHint, edit.Ciphertext, edit.Status).Scan(values...)
	} else {
		err = tx.QueryRowContext(ctx, `
SELECT c.status,COALESCE(j.channel,'') FROM provider_credentials c
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1 AND c.provider_id=$2 FOR NO KEY UPDATE OF c`,
			credentialID, providerID).Scan(&previousStatus, &channel)
		if err == nil {
			if channel != "" && previousStatus != "active" && edit.Status != nil && *edit.Status == "active" {
				if err := checkSchoolChannelAvailability(ctx, tx, providerID, channel, credentialID, nil); err != nil {
					return nil, err
				}
			}
			// The timestamp also versions the Key's price and receipt authorization.
			// Changing a display label or hint does not change the Key.
			err = tx.QueryRowContext(ctx, `
UPDATE provider_credentials SET label=COALESCE($3::citext,label),
 group_hint=CASE WHEN $4::text IS NULL THEN group_hint ELSE NULLIF($4::text,'') END,
 status=COALESCE($5::text,status),
 retired_at=CASE
   WHEN COALESCE($5::text,status)='active' THEN NULL
   WHEN status='active' THEN clock_timestamp()
   ELSE retired_at END,
 updated_at=CASE WHEN COALESCE($5::text,status)<>status THEN clock_timestamp() ELSE updated_at END
WHERE id=$1 AND provider_id=$2
RETURNING id,label,group_hint,status,created_at,updated_at,credential_ciphertext`,
				credentialID, providerID, edit.Label, edit.GroupHint, edit.Status).Scan(values...)
		}
	}
	if err != nil {
		return nil, credentialSaveError(err)
	}
	if creating && channel != "" {
		if err := RegisterSchoolCredentialTx(ctx, tx, providerID, row.ID, channel, edit.Fingerprint, true, edit.Replaces); err != nil {
			return nil, err
		}
		if edit.Replaces == nil {
			if err := EnsureProvisionActorTx(ctx, tx); err != nil {
				return nil, err
			}
		}
		// Discover the new Key's account and group independently of its predecessor.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at)
VALUES ($1,$2,$3)`, row.ID, ProvisionActorID, row.UpdatedAt); err != nil {
			return nil, unavailable("登记北辰面板授权占位失败", err)
		}
	}
	if (creating && channel != "") || (row.Status == "active" && previousStatus != "active") {
		err = QueueCredentialSyncTx(ctx, tx, row.ID)
	} else if !creating && string(row.Status) != previousStatus {
		err = RetireCredentialCatalogTx(ctx, tx, row.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, unavailable("保存北辰渠道切换状态失败", err)
	}
	return row, nil
}

func credentialSaveError(err error) error {
	var constraint *pgconn.PgError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.NewError(domain.CodeNotFound, "北辰渠道不存在或不属于该供应商")
	case errors.As(err, &constraint) && constraint.ConstraintName == "provider_credentials_provider_id_label_key":
		return domain.NewError(domain.CodeNameConflict, "该供应商下已有同名渠道")
	default:
		return unavailable("保存北辰渠道失败", err)
	}
}

func readPredecessorChannel(ctx context.Context, tx *sql.Tx, provider, previous uuid.UUID) (string, error) {
	var channel string
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(j.channel,'') FROM provider_credentials c
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1 AND c.provider_id=$2`, previous, provider).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.NewError(domain.CodeNotFound, "原渠道不属于该北辰供应商")
	}
	if err != nil {
		return "", unavailable("读取原北辰渠道失败", err)
	}
	return channel, nil
}

// An explicitly named predecessor can stay active while its replacement is
// prepared. The database keeps the registration with the existing holder.
func checkSchoolChannelAvailability(ctx context.Context, tx *sql.Tx, provider uuid.UUID, channel string, current uuid.UUID, predecessor *uuid.UUID) error {
	var occupied bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM beichen_provision_channels j JOIN provider_credentials c ON c.id=j.credential_id
  WHERE c.provider_id=$1 AND j.channel=$2 AND c.status='active' AND c.id<>$3 AND c.id IS DISTINCT FROM $4::uuid
)`, provider, channel, current, predecessor).Scan(&occupied); err != nil {
		return unavailable("核对学校渠道登记失败", err)
	}
	if occupied {
		return domain.NewError(domain.CodeNameConflict, "该供应商已有启用的 "+channel+" 学校渠道或正在准备的接替 Key")
	}
	return nil
}

// RegisterSchoolCredentialTx requires the provider lock. A credential keeps
// its school billing channel even after retirement.
func RegisterSchoolCredentialTx(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, channel string, fingerprint []byte, discover bool, predecessor *uuid.UUID) error {
	if !IsSchoolChannel(channel) || len(fingerprint) != sha256.Size {
		return invalid("学校渠道或推理 Key 摘要无效")
	}
	var previous string
	var active bool
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(j.channel,''),c.status='active' FROM provider_credentials c
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1 AND c.provider_id=$2`, credential, provider).Scan(&previous, &active)
	if err != nil {
		return credentialSaveError(err)
	}
	if previous != "" && previous != channel {
		return invalid("此凭据已登记为 " + previous + "，不能登记到其他学校渠道")
	}
	if active && previous == "" {
		if err := checkSchoolChannelAvailability(ctx, tx, provider, channel, credential, predecessor); err != nil {
			return err
		}
	}
	// A fingerprint may only identify one local credential, including archived
	// credentials. Serialize this check across providers.
	if _, err := tx.ExecContext(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended('beichen/inference/' || encode($1,'hex'), 0))`, fingerprint); err != nil {
		return unavailable("锁定学校渠道 Key 摘要失败", err)
	}
	var duplicate bool
	err = tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM beichen_provision_channels WHERE key_fingerprint=$1 AND credential_id<>$2)`,
		fingerprint, credential).Scan(&duplicate)
	if err != nil {
		return unavailable("核对学校渠道 Key 摘要失败", err)
	}
	if duplicate {
		return domain.NewError(domain.CodeNameConflict, "此 Key 已登记为学校渠道，请直接选择已有 Key")
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES ($1,$2,$3,$4) ON CONFLICT (credential_id) DO UPDATE
SET key_fingerprint=excluded.key_fingerprint,discover_ids=excluded.discover_ids,
    checked_at=NULL,upstream_user_id=NULL,discovery_error=NULL
WHERE (beichen_provision_channels.key_fingerprint,beichen_provision_channels.discover_ids)
 IS DISTINCT FROM (excluded.key_fingerprint,excluded.discover_ids)`, credential, channel, fingerprint, discover)
	if err != nil {
		return unavailable("登记学校渠道失败", err)
	}
	return nil
}

// RestoreGeminiCredentialTx reuses the retired credential's billing identity.
// Its old routes and receipts remain historical until the new Key is verified.
func (s *Service) RestoreGeminiCredentialTx(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, edit CredentialEdit) error {
	var eligible bool
	err := tx.QueryRowContext(ctx, `
SELECT c.status='inactive' AND COALESCE(j.channel='Gemini',
   c.label IN ('哈吉米','Gemini') OR COALESCE(c.group_hint='5',false)
   OR EXISTS (SELECT 1 FROM beichen_panel_access a WHERE a.credential_id=c.id AND a.upstream_key_id=12098)
   OR EXISTS (SELECT 1 FROM beichen_website_keys k
              WHERE k.credential_id=c.id AND k.provider_id=c.provider_id AND k.upstream_key_id=12098))
FROM provider_credentials c LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1 AND c.provider_id=$2 FOR NO KEY UPDATE OF c`, credential, provider).Scan(&eligible)
	if err != nil {
		return credentialSaveError(err)
	}
	if !eligible {
		return invalid("仅允许为停用的 Gemini 渠道提交新 Key，其他渠道请使用接替向导")
	}
	if err := checkSchoolChannelAvailability(ctx, tx, provider, "Gemini", credential, nil); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE provider_credentials SET label=COALESCE($3::citext,label),
 group_hint=CASE WHEN $4::text IS NULL THEN group_hint ELSE NULLIF($4::text,'') END,
 credential_ciphertext=$5,status='active',retired_at=NULL,updated_at=clock_timestamp()
WHERE id=$1 AND provider_id=$2 AND status='inactive'`, credential, provider, edit.Label, edit.GroupHint, edit.Ciphertext)
	if err != nil {
		return credentialSaveError(err)
	}
	if err := RegisterSchoolCredentialTx(ctx, tx, provider, credential, "Gemini", edit.Fingerprint, true, nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE beichen_provision_channels SET checked_at=NULL,upstream_user_id=NULL,discovery_error=NULL WHERE credential_id=$1`, credential); err != nil {
		return unavailable("重置 Gemini 渠道归属核对失败", err)
	}
	if err := EnsureProvisionActorTx(ctx, tx); err != nil {
		return err
	}
	if err := s.WritePanelAccessTx(ctx, tx, credential, uuid.MustParse(ProvisionActorID), PanelAccessInput{}); err != nil {
		return unavailable("重建 Gemini 面板授权占位失败", err)
	}
	if err := QueueCredentialSyncTx(ctx, tx, credential); err != nil {
		return unavailable("安排恢复后的 Gemini 渠道同步失败", err)
	}
	return nil
}

// EnsureProvisionActorTx reserves a disabled identity for automatic panel jobs.
func EnsureProvisionActorTx(ctx context.Context, tx *sql.Tx) error {
	const email = "beichen-provision@nebula.invalid"
	_, err := tx.ExecContext(ctx, `
INSERT INTO users (id,name,email,password_hash,role,status,created_at,updated_at)
VALUES ($1,'北辰部署审计',$2,'!','student','disabled',now(),now()) ON CONFLICT DO NOTHING`, ProvisionActorID, email)
	if err != nil {
		return unavailable("北辰部署审计身份写入失败", err)
	}
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND email=$2 AND password_hash='!'
  AND role='student' AND status='disabled'
  AND NOT EXISTS (SELECT 1 FROM mentor_profiles WHERE user_id=$1)
  AND NOT EXISTS (SELECT 1 FROM student_profiles WHERE user_id=$1) FOR SHARE`, ProvisionActorID, email).Scan(&id)
	if err != nil {
		return unavailable("北辰部署专用审计身份缺失或冲突", err)
	}
	return nil
}

// Retiring a Key makes its routes stale without discarding the old model,
// binding, price history, or receipt identity needed to repair them.
func RetireCredentialCatalogTx(ctx context.Context, tx *sql.Tx, credential uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `UPDATE provider_model_catalog SET status='inactive',updated_at=clock_timestamp()
WHERE credential_id=$1 AND status='active'`, credential)
	return err
}

// QueueCredentialSyncTx reuses the persistent scheduler. A job already holding
// a lease cannot finish with the previous revision or consume the new schedule.
func QueueCredentialSyncTx(ctx context.Context, tx *sql.Tx, credential uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets(credential_id,kind)
SELECT $1::uuid,kind FROM (VALUES ('billing'),('key_usage')) AS jobs(kind)
ON CONFLICT(credential_id,kind) DO UPDATE
SET next_attempt='-infinity',last_error=NULL,failure_count=0,lease_id=NULL,lease_until=NULL`, credential)
	return err
}
