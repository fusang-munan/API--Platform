package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ProvisionActorID = "01953422-b1f7-7c50-81b1-734f142827bb"

type PanelAccessInput struct {
	Enabled       bool   `json:"enabled"`
	GroupID       int64  `json:"group_id"`
	Token         string `json:"token"`
	UpstreamKeyID *int64 `json:"upstream_key_id,omitempty"`
	webAccount    string
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
	if in.GroupID <= 0 || in.GroupID > 9007199254740991 || (in.webAccount == "" && !validPanelBearer(in.Token)) {
		return invalid("请输入北辰分组 ID 和独立的面板读取凭据")
	}
	if in.UpstreamKeyID != nil && (*in.UpstreamKeyID <= 0 || *in.UpstreamKeyID > 9007199254740991) {
		return invalid("北辰 Key ID 须为可精确表示的正整数")
	}
	return nil
}

// DeploymentPanelAccess pins the configured school account, never its short-lived session.
// The existing bearer configuration remains available to older deployments.
func (s *Service) DeploymentPanelAccess(group, keyID int64, legacyToken string) (PanelAccessInput, error) {
	if group <= 0 || group > 9007199254740991 || keyID <= 0 || keyID > 9007199254740991 {
		return PanelAccessInput{}, errors.New("北辰分组、上游 Key ID 须为可精确表示的正整数")
	}
	in := PanelAccessInput{Enabled: true, GroupID: group, UpstreamKeyID: &keyID, Token: legacyToken}
	if identity := s.webPanelIdentity(); identity != "" {
		in.Token, in.webAccount = "", accountDigest(identity)
	}
	if in.Token == "" && in.webAccount == "" {
		// The inference key alone is sufficient for catalog discovery.
		return PanelAccessInput{}, nil
	}
	return in, in.validate()
}

func accountDigest(identity string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identity))))
	return hex.EncodeToString(digest[:])
}

func providerPanelAccountDigest(provider uuid.UUID) string {
	return accountDigest("beichen-provider:" + provider.String())
}

type priceAccess struct {
	providerID  uuid.UUID
	groupID     int64
	channel     string
	label       string
	legacyToken string
	legacyErr   error
	webAccount  string
	disabled    bool
}

func (s *Service) loadPriceAccess(ctx context.Context, credentialID uuid.UUID) (priceAccess, error) {
	var access priceAccess
	var group sql.NullInt64
	var actor uuid.NullUUID
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `
SELECT a.group_id,a.token_ciphertext,a.updated_by,COALESCE(j.channel,''),c.label::text,c.provider_id
FROM provider_credentials c
LEFT JOIN beichen_panel_access a ON a.credential_id=c.id
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE c.id=$1`, credentialID).Scan(&group, &encrypted, &actor, &access.channel, &access.label, &access.providerID)
	if err != nil {
		return priceAccess{}, errors.New("读取北辰采价配置失败")
	}
	if !actor.Valid {
		access.groupID = s.panelGroups[credentialID]
		if access.groupID > 0 {
			access.legacyToken = s.panelToken
		}
		return access, nil
	}
	if !group.Valid {
		// 自动预配的空行表示身份尚待发现，教师主动停用的空行才撤销采价。
		access.disabled = actor.UUID.String() != ProvisionActorID
		return access, nil
	}
	source, err := s.storedPanelAccess(credentialID, encrypted)
	if err != nil {
		access.legacyErr = err
		return access, nil
	}
	access.groupID, access.legacyToken, access.webAccount = group.Int64, source.Token, source.webAccount
	return access, nil
}

func (s *Service) checkLegacyPriceAccount(access priceAccess, allowProviderBinding bool) error {
	if access.legacyErr != nil {
		return access.legacyErr
	}
	return s.checkWebsiteAccount(access.providerID, access.webAccount, allowProviderBinding)
}

func (s *Service) checkWebsiteAccount(provider uuid.UUID, account string, allowProviderBinding bool) error {
	if account == "" {
		return nil
	}
	if !s.WebSessionConfigured() {
		return errors.New("未配置北辰学校网页会话")
	}
	// The provider marker means the caller must prove Key ownership with the
	// selected session. An explicit account identity is still pinned.
	if allowProviderBinding && account == providerPanelAccountDigest(provider) {
		return nil
	}
	if account != accountDigest(s.webPanelIdentity()) {
		return errors.New("北辰绑定的学校网页登录身份与本机配置不一致")
	}
	return nil
}

// PanelUsageAccess returns the current explicit per-key usage binding. The
// caller must match both ID and revision against the values pinned at dispatch.
// A missing, disabled, or outdated binding returns zero values.
func (s *Service) PanelUsageAccess(ctx context.Context, credentialID uuid.UUID) (token string, upstreamKeyID int64, revision time.Time, err error) {
	binding, err := s.panelUsageAccess(ctx, credentialID)
	return binding.auth.bearer, binding.key, binding.revision, err
}

type panelUsageBinding struct {
	auth                panelAuthorization
	managed             bool
	provider            uuid.UUID
	key, group, owner   int64
	channel, label      string
	inferenceCiphertext []byte
	revision            time.Time
}

func (s *Service) panelUsageAccess(ctx context.Context, credentialID uuid.UUID) (panelUsageBinding, error) {
	var binding panelUsageBinding
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `
SELECT a.token_ciphertext,a.upstream_key_id,a.updated_at,c.provider_id,a.group_id,
       COALESCE(j.upstream_user_id,0),COALESCE(j.channel,''),c.label::text,c.credential_ciphertext
FROM beichen_panel_access a
JOIN provider_credentials c ON c.id = a.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE a.credential_id = $1 AND a.upstream_key_id IS NOT NULL
  AND a.updated_at = c.updated_at AND c.status = 'active' AND p.status = 'active'
  AND `+beichenProvider, credentialID).Scan(&encrypted, &binding.key, &binding.revision, &binding.provider,
		&binding.group, &binding.owner, &binding.channel, &binding.label, &binding.inferenceCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return panelUsageBinding{}, nil
	}
	if err != nil {
		return panelUsageBinding{}, errors.New("读取北辰逐笔查账配置失败")
	}
	auth, managed, err := s.managedPanelAuthorization(ctx, binding.provider, "")
	if err != nil {
		return panelUsageBinding{}, err
	}
	if !managed && !auth.delivered {
		binding.auth.bearer, err = s.resolvePanelToken(ctx, credentialID, encrypted)
		return binding, err
	}
	if auth.delivered {
		source, err := s.storedPanelAccess(credentialID, encrypted)
		if err != nil {
			return panelUsageBinding{}, err
		}
		if err := s.checkWebsiteAccount(binding.provider, source.webAccount, true); err != nil {
			return panelUsageBinding{}, err
		}
	}
	// A legacy binding explicitly authorized with this same environment
	// bearer has already assigned the Key to that session.
	if !auth.delivered && auth.tokenVersion == nil && !auth.shared {
		if legacy, err := s.storedPanelAccess(credentialID, encrypted); err == nil && legacy.Token == auth.bearer {
			binding.auth, binding.managed = auth, true
			return binding, nil
		}
	}
	binding.auth, err = readWithPanelSession(ctx, s, binding.provider, auth, true,
		func(candidate panelAuthorization) (panelAuthorization, error) {
			return candidate, s.confirmPanelUsageBinding(ctx, binding, candidate.bearer)
		})
	if err != nil {
		return panelUsageBinding{}, err
	}
	binding.managed = true
	return binding, nil
}

func (s *Service) confirmPanelUsageBinding(ctx context.Context, binding panelUsageBinding, token string) error {
	keys, err := s.accountKeys(ctx, token)
	if err != nil {
		return err
	}
	if binding.owner != 0 {
		for _, key := range keys {
			if key.ID == binding.key && key.UserID == binding.owner && (key.Status == "" || key.Status == "active") {
				return nil
			}
		}
		return errKeyMatch
	}
	secret, err := s.crypto.DecryptCredential(binding.inferenceCiphertext)
	if err != nil {
		return errors.New("无法核对北辰网页会话与渠道的归属")
	}
	key, err := identifyChannel(secret, binding.channel, binding.label, keys)
	if err != nil || key.ID != binding.key {
		return errKeyMatch
	}
	return nil
}

func readPanelUsage[T any](ctx context.Context, s *Service, binding *panelUsageBinding, read func(string) (T, error)) (T, error) {
	if !binding.managed {
		return read(binding.auth.bearer)
	}
	return readWithPanelSession(ctx, s, binding.provider, binding.auth, true, func(auth panelAuthorization) (T, error) {
		if auth.bearer != binding.auth.bearer {
			if err := s.confirmPanelUsageBinding(ctx, *binding, auth.bearer); err != nil {
				var zero T
				return zero, err
			}
			binding.auth = auth
		}
		return read(auth.bearer)
	})
}

func (s *Service) storedPanelAccess(credentialID uuid.UUID, encrypted []byte) (PanelAccessInput, error) {
	stored, err := s.crypto.DecryptCredential(encrypted)
	if err == nil {
		if token, ok := strings.CutPrefix(stored, panelCredentialPrefix(credentialID)); ok && validPanelBearer(token) {
			return PanelAccessInput{Token: token}, nil
		}
		if id, ok := strings.CutPrefix(stored, webAccountPrefix(credentialID)); ok && len(id) == sha256.Size*2 {
			if _, err := hex.DecodeString(id); err == nil {
				return PanelAccessInput{webAccount: id}, nil
			}
		}
	}
	return PanelAccessInput{}, errors.New("解密北辰采价配置失败")
}

func (s *Service) resolvePanelToken(ctx context.Context, credentialID uuid.UUID, encrypted []byte) (string, error) {
	source, err := s.storedPanelAccess(credentialID, encrypted)
	if err != nil || source.webAccount == "" {
		return source.Token, err
	}
	identity := s.webPanelIdentity()
	if identity == "" || source.webAccount != accountDigest(identity) {
		return "", errors.New("北辰绑定的学校网页登录身份与本机配置不一致")
	}
	return s.webPanelToken(ctx)
}

func webAccountPrefix(credentialID uuid.UUID) string {
	return "beichen-web-v1:" + credentialID.String() + ":"
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
	return s.write(ctx, provider, credential, func(tx *sql.Tx) error {
		return s.WritePanelAccessTx(ctx, tx, credential, actor, input)
	})
}

// PanelSourceMatches compares a stored source without logging in while holding DB locks.
func (s *Service) PanelSourceMatches(credential uuid.UUID, ciphertext []byte, expected PanelAccessInput) (bool, error) {
	stored, err := s.storedPanelAccess(credential, ciphertext)
	return stored.Token == expected.Token && stored.webAccount == expected.webAccount, err
}

// WritePanelAccessTx requires validated input, a verified actor and the credential lock in tx.
func (s *Service) WritePanelAccessTx(ctx context.Context, tx *sql.Tx, credential, actor uuid.UUID, input PanelAccessInput) error {
	var group *int64
	var ciphertext []byte
	if input.Enabled {
		var err error
		value := panelCredentialPrefix(credential) + input.Token
		if input.webAccount != "" {
			value = webAccountPrefix(credential) + input.webAccount
		}
		ciphertext, err = s.crypto.EncryptCredential(value)
		if err != nil {
			return unavailable("加密北辰采价凭据失败", err)
		}
		group = &input.GroupID
	}
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
	if !input.Enabled && actor.String() != ProvisionActorID {
		if err := invalidatePanelQuote(ctx, tx, credential, version, version); err != nil {
			return err
		}
	}
	if input.Enabled {
		if err := invalidateChangedPanelQuote(ctx, tx, credential, version, version, input.GroupID, false); err != nil {
			return err
		}
	}
	return schedulePanelRefresh(ctx, tx, credential)
}
