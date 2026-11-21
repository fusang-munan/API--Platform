package beichen

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const localGrantPrefix = "nblbc_"
const localConfigLimit = 8 << 10

var errLocalPanelConfig = errors.New("北辰本机配置无效，请从学校服务器重新导出")

// LocalPanelConfig delegates renewal to a single school server. The file
// carries a revocable grant, allowing the server to keep the refresh secret.
type LocalPanelConfig struct {
	origin   string
	provider uuid.UUID
	grant    string
}

func (config LocalPanelConfig) Env() string {
	return "BEICHEN_SESSION_SERVER=" + config.origin + "\nBEICHEN_SESSION_PROVIDER_ID=" +
		config.provider.String() + "\nBEICHEN_SESSION_GRANT=" + config.grant + "\n"
}

func localPanelOrigin(raw string) (string, error) {
	origin, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.RawPath != "" ||
		origin.Path != "" && origin.Path != "/" {
		return "", errors.New("请配置学校服务器的 HTTPS 地址")
	}
	return "https://" + origin.Host, nil
}

func panelGrantHash(grant string) ([]byte, bool) {
	raw, ok := strings.CutPrefix(grant, localGrantPrefix)
	if !ok || len(raw) != 64 {
		return nil, false
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return nil, false
	}
	hash := sha256.Sum256([]byte(grant))
	return hash[:], true
}

func ReadLocalPanelConfig(path string) (LocalPanelConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return LocalPanelConfig{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, localConfigLimit+1))
	if err != nil || len(raw) > localConfigLimit {
		return LocalPanelConfig{}, errLocalPanelConfig
	}
	values := make(map[string]string, 3)
	for _, line := range strings.Split(strings.TrimPrefix(string(raw), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if _, exists := values[key]; !ok || exists {
			return LocalPanelConfig{}, errLocalPanelConfig
		}
		values[key] = value
	}
	origin, err := localPanelOrigin(values["BEICHEN_SESSION_SERVER"])
	provider, providerErr := uuid.Parse(values["BEICHEN_SESSION_PROVIDER_ID"])
	grant := values["BEICHEN_SESSION_GRANT"]
	_, valid := panelGrantHash(grant)
	if err != nil || providerErr != nil || provider == uuid.Nil || !valid || len(values) != 3 {
		return LocalPanelConfig{}, errLocalPanelConfig
	}
	return LocalPanelConfig{origin: origin, provider: provider, grant: grant}, nil
}

// ConfigureImportedPanelSession runs once during startup. The same import can
// supply the CLI and an independently running local backend.
func (s *Service) ConfigureImportedPanelSession() error {
	path := strings.TrimSpace(os.Getenv("BEICHEN_SESSION_LOCAL_FILE"))
	useDefault := path == ""
	if useDefault {
		path = filepath.Join("deploy", "beichen-session.local.env")
	}
	config, err := ReadLocalPanelConfig(path)
	if useDefault && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.sharedPanelErr = errLocalPanelConfig
		return errLocalPanelConfig
	}
	s.sharedPanel = &config
	return nil
}

func (s *Service) CheckSharedPanelConnection(ctx context.Context, provider uuid.UUID) error {
	if s.sharedPanel == nil {
		return errLocalLoginMissing
	}
	_, _, err := s.managedPanelAuthorization(ctx, provider, "")
	return err
}

func (s *Service) ExportPanelConfiguration(ctx context.Context, provider, actor uuid.UUID, rawOrigin string) (LocalPanelConfig, error) {
	origin, err := localPanelOrigin(rawOrigin)
	if err != nil {
		return LocalPanelConfig{}, invalid(err.Error())
	}
	provider, err = s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return LocalPanelConfig{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return LocalPanelConfig{}, domain.NewError(domain.CodeDependencyUnavailable, "无法生成北辰本机访问授权")
	}
	config := LocalPanelConfig{origin: origin, provider: provider, grant: localGrantPrefix + hex.EncodeToString(secret[:])}
	hash, _ := panelGrantHash(config.grant)
	err = s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		if err := checkPanelAccountActor(ctx, tx, actor); err != nil {
			return err
		}
		var saved uuid.UUID
		err := tx.QueryRowContext(ctx, `
UPDATE beichen_panel_accounts SET local_grant_hash=$2,local_granted_by=$3,local_granted_at=clock_timestamp()
WHERE provider_id=$1 AND (token_ciphertext IS NOT NULL OR password_ciphertext IS NOT NULL)
RETURNING provider_id`, provider, hash, actor).Scan(&saved)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid(errPanelAccountMissing.Error())
		}
		if err != nil {
			return errPanelAccountDB
		}
		return nil
	})
	if err != nil {
		return LocalPanelConfig{}, err
	}
	return config, nil
}

var errLocalGrantRejected = domain.NewError(domain.CodeForbidden, "北辰本机授权已失效，请重新导出并导入")

type LocalPanelSession struct {
	Token string `json:"access_token"`
}

func (s *Service) ReadSharedPanelSession(ctx context.Context, provider uuid.UUID, grant, rejected string) (LocalPanelSession, error) {
	hash, valid := panelGrantHash(grant)
	if s.db == nil || !valid {
		return LocalPanelSession{}, errLocalGrantRejected
	}
	var authorized bool
	const check = `
SELECT EXISTS (SELECT 1 FROM beichen_panel_accounts a JOIN providers p ON p.id=a.provider_id
JOIN users u ON u.id=a.local_granted_by
WHERE a.provider_id=$1 AND a.local_grant_hash=$2 AND ($3::bytea IS NULL OR a.token_ciphertext=$3)
AND p.status='active' AND u.status='active' AND u.role='teacher')`
	if err := s.db.QueryRowContext(ctx, check, provider, hash, nil).Scan(&authorized); err != nil {
		return LocalPanelSession{}, errPanelAccountDB
	}
	if !authorized {
		return LocalPanelSession{}, errLocalGrantRejected
	}
	authorization, _, err := s.storedPanelAuthorization(ctx, provider)
	if err != nil {
		return LocalPanelSession{}, domain.NewError(domain.CodeDependencyUnavailable, err.Error())
	}
	if rejected != "" && rejected == panelBearerDigest(authorization.bearer) {
		// A CLI check makes one directory request. Confirm rejection through
		// auth/session, or rely on the authorized client for older upstreams.
		_, supported, verifyErr := s.verifyWebSessionEndpoint(ctx, authorization.bearer)
		if !supported || panelAuthFailed(verifyErr) {
			if err := s.markPanelRejected(ctx, provider, authorization, false); err != nil {
				return LocalPanelSession{}, err
			}
			authorization, _, err = s.storedPanelAuthorization(ctx, provider)
			if err != nil {
				return LocalPanelSession{}, domain.NewError(domain.CodeDependencyUnavailable, err.Error())
			}
		} else if verifyErr != nil {
			return LocalPanelSession{}, domain.NewError(domain.CodeDependencyUnavailable, verifyErr.Error())
		}
	}
	if err := s.db.QueryRowContext(ctx, check, provider, hash, authorization.tokenVersion).Scan(&authorized); err != nil {
		return LocalPanelSession{}, errPanelAccountDB
	}
	if !authorized || authorization.bearer == "" {
		return LocalPanelSession{}, errLocalGrantRejected
	}
	return LocalPanelSession{Token: authorization.bearer}, nil
}

func panelBearerDigest(bearer string) string {
	digest := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(digest[:])
}

func (s *Service) requestSharedPanelSession(ctx context.Context, rejected string) (panelAuthorization, error) {
	config := s.sharedPanel
	body, _ := json.Marshal(map[string]string{"rejected_token_sha256": rejected})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		config.origin+"/api/v1/beichen/local-session/"+config.provider.String(), bytes.NewReader(body))
	if err != nil {
		return panelAuthorization{}, errLocalPanelConfig
	}
	req.Header.Set("Authorization", "Bearer "+config.grant)
	req.Header.Set("Origin", config.origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	client := *s.client
	client.Timeout = 32 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return panelAuthorization{}, errors.New("无法连接学校服务器的北辰会话服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return panelAuthorization{}, errLocalGrantRejected
	}
	var reply struct {
		Data LocalPanelSession `json:"data"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, localConfigLimit)).Decode(&reply) != nil || !validPanelBearer(reply.Data.Token) {
		return panelAuthorization{}, errors.New("学校服务器的北辰网页会话不可用，请在教师页查看状态")
	}
	return panelAuthorization{bearer: reply.Data.Token, shared: true}, nil
}
