package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const (
	panelModePassword = "password"
	panelModeToken    = "token"
	panelStateNone    = "none"
	panelStateActive  = "active"
	panelStateExpired = "expired"
	panelStateError   = "error"
)

var (
	errPanelAccountMissing = errors.New("请先配置北辰网页账号或 Token")
	errPanelTokenExpired   = errors.New("北辰网页 Token 已过期，请粘贴新的 Token")
	errPanelAccountDB      = domain.NewError(domain.CodeDependencyUnavailable, "读写北辰网页登录配置失败，请检查数据库连接和迁移")
	errPanelAccountCrypto  = domain.NewError(domain.CodeDependencyUnavailable, "北辰网页登录凭证加解密失败，请核对服务器的凭据加密密钥")
)

type PanelAccountStatus struct {
	Configured     bool              `json:"configured"`
	Mode           string            `json:"mode"`
	Email          string            `json:"email"`
	Status         string            `json:"status"`
	ExpiresAt      *time.Time        `json:"expires_at"`
	LastVerifiedAt *time.Time        `json:"last_verified_at"`
	LastError      *string           `json:"last_error"`
	FailureCount   int               `json:"failure_count"`
	Crawl          PanelCrawlSummary `json:"crawl"`
}

type PanelCrawlSummary struct {
	LastRunAt      *time.Time     `json:"last_run_at"`
	Platforms      *int           `json:"platforms"`
	Models         *int           `json:"models"`
	Failures       *int           `json:"failures"`
	FailureDetails []CrawlFailure `json:"failure_details,omitempty"`
}

type panelAccount struct {
	provider                        uuid.UUID
	mode, status, lastError         string
	email, password, token, refresh []byte
	expiresAt, lastVerifiedAt       *time.Time
	failureCount                    int
	updatedAt                       time.Time
}

type panelAccountReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readPanelAccount(ctx context.Context, db panelAccountReader, provider uuid.UUID) (panelAccount, bool, error) {
	account := panelAccount{provider: provider, mode: panelModePassword, status: panelStateNone}
	err := db.QueryRowContext(ctx, `
SELECT mode,status,email_ciphertext,password_ciphertext,token_ciphertext,refresh_token_ciphertext,
       expires_at,last_verified_at,COALESCE(last_error,''),failure_count,updated_at
FROM beichen_panel_accounts WHERE provider_id=$1`, provider).Scan(
		&account.mode, &account.status, &account.email, &account.password, &account.token, &account.refresh,
		&account.expiresAt, &account.lastVerifiedAt, &account.lastError, &account.failureCount, &account.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return account, false, nil
	}
	if err != nil {
		return panelAccount{}, false, errPanelAccountDB
	}
	return account, true, nil
}

func readPanelCrawlSummary(ctx context.Context, db panelAccountReader, provider uuid.UUID) (PanelCrawlSummary, error) {
	var view PanelCrawlSummary
	var failures []byte
	err := db.QueryRowContext(ctx, `
SELECT started_at,platform_count,model_count,failures
FROM beichen_crawl_runs WHERE provider_id=$1`, provider).
		Scan(&view.LastRunAt, &view.Platforms, &view.Models, &failures)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PanelCrawlSummary{}, errPanelAccountDB
	}
	if err == nil {
		if json.Unmarshal(failures, &view.FailureDetails) != nil {
			return PanelCrawlSummary{}, errPanelAccountDB
		}
		count := len(view.FailureDetails)
		view.Failures = &count
	}
	return view, nil
}

func (s *Service) savePanelAccount(ctx context.Context, tx *sql.Tx, account panelAccount, actor uuid.UUID) error {
	// A nil actor updates session state only. Teachers are attributed when
	// providing or clearing the credentials.
	if actor == uuid.Nil {
		_, err := tx.ExecContext(ctx, `
UPDATE beichen_panel_accounts SET token_ciphertext=$2,refresh_token_ciphertext=$3,expires_at=$4,
    last_verified_at=$5,status=$6,last_error=NULLIF($7,''),failure_count=$8,updated_at=clock_timestamp()
WHERE provider_id=$1`, account.provider, account.token, account.refresh, account.expiresAt,
			account.lastVerifiedAt, account.status, account.lastError, account.failureCount)
		if err != nil {
			return errPanelAccountDB
		}
		return nil
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_panel_accounts(provider_id,mode,email_ciphertext,password_ciphertext,token_ciphertext,
    refresh_token_ciphertext,expires_at,last_verified_at,status,last_error,failure_count,created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12)
ON CONFLICT(provider_id) DO UPDATE SET mode=excluded.mode,email_ciphertext=excluded.email_ciphertext,
    password_ciphertext=excluded.password_ciphertext,token_ciphertext=excluded.token_ciphertext,
    refresh_token_ciphertext=excluded.refresh_token_ciphertext,expires_at=excluded.expires_at,
    last_verified_at=excluded.last_verified_at,status=excluded.status,last_error=excluded.last_error,
    failure_count=excluded.failure_count,created_by=excluded.created_by,updated_at=clock_timestamp(),
    local_grant_hash=NULL,local_granted_by=NULL,local_granted_at=NULL`,
		account.provider, account.mode, account.email, account.password, account.token, account.refresh,
		account.expiresAt, account.lastVerifiedAt, account.status, account.lastError, account.failureCount, actor)
	if err != nil {
		return errPanelAccountDB
	}
	return nil
}

func (s *Service) withPanelAccountLock(ctx context.Context, provider uuid.UUID, action func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errPanelAccountDB
	}
	defer tx.Rollback()
	// CLI and server workers share this lock when they share a database.
	// Keep it independent of the provider locks used for billing writes.
	if _, err := tx.ExecContext(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended('beichen_panel_accounts'::regclass::oid::text || ':' || $1::uuid::text, 0))`, provider); err != nil {
		return errPanelAccountDB
	}
	if err := action(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errPanelAccountDB
	}
	return nil
}

func (s *Service) encryptPanelSecret(provider uuid.UUID, field, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if s.crypto == nil {
		return nil, errPanelAccountCrypto
	}
	ciphertext, err := s.crypto.EncryptCredential(panelSecretPrefix(provider, field) + value)
	if err != nil {
		return nil, errPanelAccountCrypto
	}
	return ciphertext, nil
}

func (s *Service) decryptPanelSecret(provider uuid.UUID, field string, ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if s.crypto == nil {
		return "", errPanelAccountCrypto
	}
	plaintext, err := s.crypto.DecryptCredential(ciphertext)
	value, match := strings.CutPrefix(plaintext, panelSecretPrefix(provider, field))
	if err != nil || !match || value == "" {
		return "", errPanelAccountCrypto
	}
	return value, nil
}

func panelSecretPrefix(provider uuid.UUID, field string) string {
	return "beichen-panel-account-v1:" + provider.String() + ":" + field + ":"
}
