package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

type CredentialEdit struct {
	Label, GroupHint, Status *string
	Ciphertext               []byte
	Replaces                 *uuid.UUID
	Fingerprint              []byte
}

// SaveCredential serializes changes with rebind and sync. The credential ID
// remains the billing identity of one upstream Key for its entire lifetime.
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
	if creating {
		if edit.Label == nil || edit.Status == nil || len(edit.Ciphertext) == 0 {
			return nil, invalid("请填写新渠道名称和 Key")
		}
		credentialID, err = uuid.NewV7()
		if err != nil {
			return nil, unavailable("生成北辰凭据编号失败", err)
		}
		err = tx.QueryRowContext(ctx, `
INSERT INTO provider_credentials(id,provider_id,label,group_hint,credential_ciphertext,status,created_at,updated_at,retired_at)
VALUES($1,$2,$3,NULLIF($4::text,''),$5,$6::text,statement_timestamp(),statement_timestamp(),
 CASE WHEN $6::text<>'active' THEN statement_timestamp() END)
RETURNING id,label,group_hint,status,created_at,updated_at,credential_ciphertext`,
			credentialID, providerID, edit.Label, edit.GroupHint, edit.Ciphertext, edit.Status).Scan(values...)
	} else {
		if len(edit.Ciphertext) != 0 {
			return nil, invalid("更换北辰 Key 请在渠道切换向导添加新 Key")
		}
		err = tx.QueryRowContext(ctx, `
SELECT status FROM provider_credentials WHERE id=$1 AND provider_id=$2 FOR NO KEY UPDATE`,
			credentialID, providerID).Scan(&previousStatus)
		if err == nil {
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
		var constraint *pgconn.PgError
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, domain.NewError(domain.CodeNotFound, "北辰渠道不存在或不属于该供应商")
		case errors.As(err, &constraint) && constraint.Code == "23505":
			return nil, domain.NewError(domain.CodeNameConflict, "该供应商下已有同名渠道")
		default:
			return nil, unavailable("保存北辰渠道失败", err)
		}
	}
	if creating && edit.Replaces != nil {
		if err := recordCredentialSuccession(ctx, tx, providerID, row.ID, *edit.Replaces, edit.Fingerprint, row.UpdatedAt); err != nil {
			return nil, err
		}
	}
	if string(row.Status) != previousStatus {
		if row.Status == "active" {
			err = QueueCredentialSyncTx(ctx, tx, row.ID)
		} else if !creating {
			err = RetireCredentialCatalogTx(ctx, tx, row.ID)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, unavailable("保存北辰渠道切换状态失败", err)
	}
	return row, nil
}

func recordCredentialSuccession(ctx context.Context, tx *sql.Tx, provider, next, previous uuid.UUID, fingerprint []byte, revision time.Time) error {
	var channel string
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(j.channel,'') FROM provider_credentials c
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1 AND c.provider_id=$2`, previous, provider).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "原渠道不属于该北辰供应商")
	}
	if err != nil {
		return unavailable("读取原北辰渠道失败", err)
	}
	if channel == "" {
		return nil
	}
	if len(fingerprint) != sha256.Size {
		return invalid("需要有效的接替 Key")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM beichen_provision_channels WHERE key_fingerprint=$1)`, fingerprint).Scan(&exists); err != nil {
		return unavailable("核对接替渠道失败", err)
	}
	if exists {
		return invalid("此 Key 已配置为学校渠道，请直接选择已有 Key")
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES ($1,$2,$3,true)`, next, channel, fingerprint)
	if err == nil {
		// Only the school billing channel is inherited. Account discovery must
		// verify the replacement's own Key ID, group, and receipt access.
		_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at)
VALUES ($1,$2,$3)`, next, ProvisionActorID, revision)
	}
	if err != nil {
		return unavailable("登记接替渠道失败", err)
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
ON CONFLICT(credential_id,kind) DO UPDATE SET next_attempt='-infinity',last_error=NULL,failure_count=0`, credential)
	return err
}
