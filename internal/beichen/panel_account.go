package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

type PanelAccountLogin struct {
	Mode     string `json:"mode"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

func (input *PanelAccountLogin) validate() error {
	input.Email = strings.TrimSpace(input.Email)
	input.Token = strings.TrimSpace(input.Token)
	switch input.Mode {
	case panelModePassword:
		addr, err := mail.ParseAddress(input.Email)
		if err != nil || addr.Address != input.Email || len(input.Email) > 254 ||
			input.Password == "" || len(input.Password) > 4096 || input.Token != "" {
			return invalid("请填写有效的北辰账号邮箱和密码")
		}
	case panelModeToken:
		if !validPanelBearer(input.Token) || input.Email != "" || input.Password != "" {
			return invalid("请粘贴有效的北辰网页会话 Token")
		}
	default:
		return invalid("请选择账号密码或网页 Token 登录方式")
	}
	return nil
}

func (s *Service) ResolvePanelProvider(ctx context.Context, provider uuid.UUID) (uuid.UUID, error) {
	if s.db == nil {
		return uuid.Nil, errPanelAccountDB
	}
	if provider == uuid.Nil {
		return findCrawlProvider(ctx, s.db)
	}
	err := checkProvider(ctx, s.db, provider)
	if domain.ErrorCode(err) == domain.CodeDependencyUnavailable {
		return uuid.Nil, errPanelAccountDB
	}
	return provider, err
}

func (s *Service) PanelAccountStatus(ctx context.Context, provider uuid.UUID) (PanelAccountStatus, error) {
	provider, err := s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	account, _, err := readPanelAccount(ctx, s.db, provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	return s.describePanelAccount(ctx, account)
}

func (s *Service) describePanelAccount(ctx context.Context, account panelAccount) (PanelAccountStatus, error) {
	email, err := s.decryptPanelSecret(account.provider, "email", account.email)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	crawl, err := readPanelCrawlSummary(ctx, s.db, account.provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	view := PanelAccountStatus{
		Configured: len(account.token) != 0 || len(account.password) != 0,
		Mode:       account.mode, Email: email, Status: account.status, ExpiresAt: account.expiresAt,
		LastVerifiedAt: account.lastVerifiedAt, FailureCount: account.failureCount, Crawl: crawl,
		WebSessionConfigured: s.WebSessionConfigured(), WebSessionError: s.WebSessionError(),
		SessionReleaseSHA256: schoolseal.SessionReleaseDigest(),
	}
	if view.Status == panelStateActive && account.hasExpired(s.now()) {
		view.Status = panelStateExpired
		if account.mode == panelModeToken {
			account.lastError = errPanelTokenExpired.Error()
		}
	}
	if account.lastError != "" {
		view.LastError = &account.lastError
	}
	return view, nil
}

func (s *Service) LoginPanelAccount(ctx context.Context, provider, actor uuid.UUID, input PanelAccountLogin) (PanelAccountStatus, error) {
	if err := input.validate(); err != nil {
		return PanelAccountStatus{}, err
	}
	provider, err := s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	account := panelAccount{provider: provider, mode: input.Mode, status: panelStateActive}
	err = s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		if err := checkPanelAccountActor(ctx, tx, actor); err != nil {
			return err
		}
		if input.Mode == panelModeToken {
			account.token, err = s.encryptPanelSecret(provider, "token", input.Token)
			if err != nil {
				return err
			}
			account.expiresAt = webBearerExpiry(input.Token)
			if account.hasExpired(s.now()) {
				account.fail(panelStateExpired, errPanelTokenExpired.Error())
			}
		} else {
			account.email, err = s.encryptPanelSecret(provider, "email", input.Email)
			if err != nil {
				return err
			}
			tokens, loginErr := s.signInPanelAccount(ctx, input.Email, input.Password)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if loginErr != nil {
				previous, _, err := readPanelAccount(ctx, tx, provider)
				if err != nil {
					return err
				}
				account.failureCount = previous.failureCount
				account.fail(panelStateError, panelSignInMessage(loginErr))
			} else {
				account.password, err = s.encryptPanelSecret(provider, "password", input.Password)
				if err != nil {
					return err
				}
				if err := s.saveWebTokens(&account, tokens); err != nil {
					return err
				}
			}
		}
		return s.savePanelAccount(ctx, tx, account, actor)
	})
	if err != nil {
		return PanelAccountStatus{}, err
	}
	return s.describePanelAccount(ctx, account)
}

func checkPanelAccountActor(ctx context.Context, tx *sql.Tx, actor uuid.UUID) error {
	var allowed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id=$1 AND role='teacher' AND status='active')", actor).Scan(&allowed); err != nil {
		return errPanelAccountDB
	}
	if !allowed {
		return domain.NewError(domain.CodeForbidden, "需要在职教师账号才能保存或清除北辰凭证")
	}
	return nil
}

func (s *Service) VerifyPanelAccount(ctx context.Context, provider uuid.UUID) (PanelAccountStatus, error) {
	provider, err := s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	account, sessionErr, err := s.updatePanelSession(ctx, provider, true)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	if errors.Is(sessionErr, errPanelAccountMissing) {
		return PanelAccountStatus{}, invalid(sessionErr.Error())
	}
	return s.describePanelAccount(ctx, account)
}

func (s *Service) ClearPanelAccount(ctx context.Context, provider, actor uuid.UUID) (PanelAccountStatus, error) {
	provider, err := s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return PanelAccountStatus{}, err
	}
	cleared := panelAccount{provider: provider, mode: panelModePassword, status: panelStateNone}
	var previous panelAccount
	err = s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		if err := checkPanelAccountActor(ctx, tx, actor); err != nil {
			return err
		}
		previous, _, err = readPanelAccount(ctx, tx, provider)
		if err != nil {
			return err
		}
		// Keep an empty row so clearing a login does not resume the old
		// environment or sealed session for this provider.
		return s.savePanelAccount(ctx, tx, cleared, actor)
	})
	if err != nil {
		return PanelAccountStatus{}, err
	}
	if len(previous.token) != 0 {
		if err := s.signOutPanelAccount(ctx, previous); err != nil {
			cleared.lastError = "本地凭证已清除，北辰网站会话退出未获确认"
		}
	}
	return s.describePanelAccount(ctx, cleared)
}

func (s *Service) signOutPanelAccount(ctx context.Context, account panelAccount) error {
	bearer, err := s.decryptPanelSecret(account.provider, "token", account.token)
	if err != nil {
		return err
	}
	refresh, err := s.panelRefreshCredential(account)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	var cookies []*http.Cookie
	if refresh.CookieName != "" {
		cookies = append(cookies, &http.Cookie{Name: refresh.CookieName, Value: refresh.Token})
	} else if refresh.Token != "" {
		fields["refresh_token"] = refresh.Token
	}
	body, _, err := s.requestWebAuth(ctx, http.MethodPost, "logout", bearer, fields, cookies...)
	if err == nil && len(bytes.TrimSpace(body)) != 0 {
		_, err = webAuthData(body)
	}
	return err
}

func (account panelAccount) hasExpired(now time.Time) bool {
	return account.expiresAt != nil && !now.Before(*account.expiresAt)
}

func (account *panelAccount) verified(now time.Time) {
	at := now.UTC()
	account.lastVerifiedAt = &at
	account.status, account.lastError, account.failureCount = panelStateActive, "", 0
}

func (account *panelAccount) fail(state, message string) {
	account.status, account.lastError = state, message
	account.failureCount = min(account.failureCount+1, 2147483647)
}

func (s *Service) signInPanelAccount(ctx context.Context, email, password string) (webTokens, error) {
	return s.requestWebTokens(ctx, "login", map[string]string{"email": email, "password": password})
}

func panelSignInRejected(err error) bool {
	var status upstreamHTTPStatus
	return errors.Is(err, errWebSignIn) || errors.As(err, &status) &&
		(status == 400 || status == 401 || status == 403 || status == 422)
}

func panelSignInMessage(err error) string {
	switch {
	case errors.Is(err, errWebSignIn):
		return "北辰网页登录需要人工验证，请先在北辰网页完成验证，或使用网页 Token"
	case panelSignInRejected(err):
		return "北辰拒绝网页登录，请核对账号、密码或网页验证"
	case errors.Is(err, upstreamHTTPStatus(http.StatusTooManyRequests)):
		return "北辰网页登录请求过多，请稍后再试"
	case errors.Is(err, errExpiredWebSession):
		return errExpiredWebSession.Error()
	default:
		return "北辰网页登录或续期失败，请稍后重试"
	}
}

func (s *Service) panelRefreshCredential(account panelAccount) (webRefreshCredential, error) {
	value, err := s.decryptPanelSecret(account.provider, "refresh", account.refresh)
	var credential webRefreshCredential
	if err != nil || value != "" && (json.Unmarshal([]byte(value), &credential) != nil ||
		!validPanelBearer(credential.Token) || credential.CookieName != "" && !validRefreshCookieName(credential.CookieName)) {
		return webRefreshCredential{}, errPanelAccountCrypto
	}
	return credential, nil
}

func (s *Service) saveWebTokens(account *panelAccount, tokens webTokens) error {
	now := s.now().UTC()
	expiry := tokens.expiresAt(now)
	if !now.Before(expiry) {
		return errExpiredWebSession
	}
	token, err := s.encryptPanelSecret(account.provider, "token", tokens.Access)
	if err != nil {
		return err
	}
	var refresh []byte
	if tokens.Refresh != "" {
		raw, _ := json.Marshal(webRefreshCredential{Token: tokens.Refresh, CookieName: tokens.Cookie})
		refresh, err = s.encryptPanelSecret(account.provider, "refresh", string(raw))
		if err != nil {
			return err
		}
	}
	account.token, account.refresh, account.expiresAt = token, refresh, &expiry
	account.verified(now)
	return nil
}
