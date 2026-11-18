package beichen

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PanelAccessInput struct {
	Enabled       bool   `json:"enabled"`
	GroupID       int64  `json:"group_id"`
	Token         string `json:"token"`
	UpstreamKeyID *int64 `json:"upstream_key_id,omitempty"`
}

type PanelAccessStatus struct {
	Configured    bool   `json:"configured"`
	GroupID       int64  `json:"group_id,omitempty"`
	UpstreamKeyID *int64 `json:"upstream_key_id,omitempty"`
}

func (in PanelAccessInput) validate() error {
	if !in.Enabled {
		if in.GroupID != 0 || in.Token != "" || in.UpstreamKeyID != nil {
			return invalid("停用采价时请省略分组、授权凭据和北辰 Key ID")
		}
		return nil
	}
	if in.GroupID <= 0 || in.GroupID > 9007199254740991 || !validPanelBearer(in.Token) {
		return invalid("请输入北辰分组 ID 和独立的面板读取凭据")
	}
	if in.UpstreamKeyID != nil && (*in.UpstreamKeyID <= 0 || *in.UpstreamKeyID > 9007199254740991) {
		return invalid("北辰 Key ID 须为可精确表示的正整数")
	}
	return nil
}

// A disabled database row overrides deployment settings. Rotation never
// exposes a stored token through the control plane.
func (s *Service) panelAccess(ctx context.Context, credentialID uuid.UUID) (string, int64, error) {
	var group sql.NullInt64
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT group_id,token_ciphertext FROM beichen_panel_access WHERE credential_id=$1`, credentialID).Scan(&group, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		id := s.panelGroups[credentialID]
		if id == 0 {
			return "", 0, nil
		}
		return s.panelToken, id, nil
	}
	if err != nil {
		return "", 0, errors.New("读取北辰采价配置失败")
	}
	if !group.Valid {
		return "", 0, nil
	}
	token, err := s.decryptPanelToken(credentialID, encrypted)
	if err != nil {
		return "", 0, err
	}
	return token, group.Int64, nil
}

// PanelUsageAccess returns the current explicit per-key usage binding. The
// caller must match both ID and revision against the values pinned at dispatch.
// A missing, disabled, or outdated binding returns zero values.
func (s *Service) PanelUsageAccess(ctx context.Context, credentialID uuid.UUID) (token string, upstreamKeyID int64, revision time.Time, err error) {
	var encrypted []byte
	err = s.db.QueryRowContext(ctx, `
SELECT a.token_ciphertext, a.upstream_key_id, a.updated_at
FROM beichen_panel_access a
JOIN provider_credentials c ON c.id = a.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE a.credential_id = $1 AND a.upstream_key_id IS NOT NULL
  AND a.updated_at = c.updated_at AND c.status = 'active' AND p.status = 'active'
  AND `+beichenProvider, credentialID).Scan(&encrypted, &upstreamKeyID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, time.Time{}, nil
	}
	if err != nil {
		return "", 0, time.Time{}, errors.New("读取北辰逐笔查账配置失败")
	}
	token, err = s.decryptPanelToken(credentialID, encrypted)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	return token, upstreamKeyID, revision, nil
}

func (s *Service) decryptPanelToken(credentialID uuid.UUID, encrypted []byte) (string, error) {
	stored, err := s.crypto.DecryptCredential(encrypted)
	token, bound := strings.CutPrefix(stored, panelCredentialPrefix(credentialID))
	if err != nil || !bound || !validPanelBearer(token) {
		return "", errors.New("解密北辰采价配置失败")
	}
	return token, nil
}

func panelCredentialPrefix(credentialID uuid.UUID) string {
	return "beichen-panel-v1:" + credentialID.String() + ":"
}

func (s *Service) PanelStatus(ctx context.Context, provider, credential uuid.UUID) (PanelAccessStatus, error) {
	var group sql.NullInt64
	var keyID *int64
	var overridden bool
	err := s.db.QueryRowContext(ctx, `
SELECT a.credential_id IS NOT NULL, a.group_id, a.upstream_key_id
FROM provider_credentials c JOIN providers p ON p.id=c.provider_id
LEFT JOIN beichen_panel_access a ON a.credential_id=c.id
WHERE p.id=$1 AND c.id=$2 AND `+beichenProvider, provider, credential).Scan(&overridden, &group, &keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return PanelAccessStatus{}, invalid("该上游凭据不属于此北辰供应商")
	}
	if err != nil {
		return PanelAccessStatus{}, unavailable("读取北辰采价配置失败", err)
	}
	id := group.Int64
	if !overridden {
		id = s.panelGroups[credential]
	}
	return PanelAccessStatus{Configured: id > 0, GroupID: id, UpstreamKeyID: keyID}, nil
}

func (s *Service) SetPanelAccess(ctx context.Context, provider, credential, actor uuid.UUID, input PanelAccessInput) error {
	if err := input.validate(); err != nil {
		return err
	}
	if actor == uuid.Nil {
		return invalid("需要有效的教师 ID")
	}
	var group *int64
	var ciphertext []byte
	if input.Enabled {
		var err error
		ciphertext, err = s.crypto.EncryptCredential(panelCredentialPrefix(credential) + input.Token)
		if err != nil {
			return unavailable("加密北辰采价凭据失败", err)
		}
		group = &input.GroupID
	}
	return s.write(ctx, provider, credential, func(tx *sql.Tx) error {
		version, err := priceTimestamp(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO beichen_panel_access (credential_id,group_id,token_ciphertext,updated_by,updated_at,upstream_key_id)
VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (credential_id) DO UPDATE
SET group_id=excluded.group_id, token_ciphertext=excluded.token_ciphertext,
    updated_by=excluded.updated_by, updated_at=excluded.updated_at,
    upstream_key_id=excluded.upstream_key_id`, credential, group, ciphertext, actor, version, input.UpstreamKeyID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE provider_credentials SET updated_at=$2 WHERE id=$1`, credential, version); err != nil {
			return err
		}
		return s.InvalidatePanelQuote(ctx, tx, credential, version)
	})
}
