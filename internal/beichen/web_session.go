package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const webAuthURL = "https://beichenapi.com/api/v1/auth/"

var errWebSignIn = errors.New("北辰官网要求核实学校账号或完成登录验证")

// The school signs in through the same endpoints as the Beichen website. Only
// its running process holds the session and refresh token.
type webSession struct {
	email, password   string
	mu                sync.Mutex
	access, refresh   string
	renewAt           time.Time
	lastError         error
	humanVerification atomic.Bool
	retryAt           time.Time
}

// ConfigureWebSession runs before any workers start.
func (s *Service) ConfigureWebSession(email, password string) error {
	email = strings.TrimSpace(email)
	if email == "" && password == "" {
		s.webAccount = nil
		return nil
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || password == "" || len(password) > 4096 {
		return errors.New("北辰网页登录配置需要有效的账号邮箱与密码")
	}
	s.webAccount = &webSession{email: email, password: password}
	return nil
}

// ConfigureWebSessionTokens loads a session captured after the school account's
// normal human verification. The access token is used until its recorded
// expiry; the refresh token renews it without another password login.
func (s *Service) ConfigureWebSessionTokens(email, access, refresh string, expiresAt time.Time) error {
	email = strings.TrimSpace(email)
	access, refresh = strings.TrimSpace(access), strings.TrimSpace(refresh)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || !validPanelBearer(access) ||
		!validPanelBearer(refresh) || expiresAt.IsZero() {
		return errors.New("北辰网页会话需要有效的账号邮箱、双令牌和到期时间")
	}
	renewAt := expiresAt.Add(-time.Minute)
	if renewAt.Before(s.now()) {
		renewAt = s.now()
	}
	s.webAccount = &webSession{email: email, access: access, refresh: refresh, renewAt: renewAt}
	return nil
}

func (s *Service) WebSessionConfigured() bool { return s.webAccount != nil }

// A protected endpoint can reject a session before its advertised expiry. Only
// invalidate the bearer used by that request, never a newer concurrent login.
func (s *Service) webReadError(bearer string, err error) error {
	account := s.webAccount
	if account != nil && errors.Is(err, upstreamHTTPStatus(http.StatusUnauthorized)) {
		account.mu.Lock()
		if bearer == account.access {
			account.renewAt = time.Time{}
		}
		account.mu.Unlock()
	}
	return err
}

func (s *Service) webPanelIdentity() string {
	if s.webAccount == nil {
		return ""
	}
	return s.webAccount.email
}

type webTokens struct {
	Access   string `json:"access_token"`
	Refresh  string `json:"refresh_token"`
	Lifetime int64  `json:"expires_in"`
	Type     string `json:"token_type"`
	TwoStep  bool   `json:"requires_2fa"`
}

func (s *Service) webPanelToken(ctx context.Context) (string, error) {
	account := s.webAccount
	if account == nil {
		return "", nil
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := s.now()
	if account.access != "" && now.Before(account.renewAt) {
		return account.access, nil
	}
	if account.lastError != nil && (account.retryAt.IsZero() || now.Before(account.retryAt)) {
		return "", account.lastError
	}

	var tokens webTokens
	var err error
	if account.refresh != "" {
		tokens, err = s.requestWebTokens(ctx, "refresh", map[string]string{"refresh_token": account.refresh})
		if errors.Is(err, upstreamHTTPStatus(http.StatusUnauthorized)) {
			account.refresh = ""
		}
	}
	if account.refresh == "" {
		if account.password == "" {
			account.lastError = errWebSignIn
			account.humanVerification.Store(true)
			return "", account.lastError
		}
		tokens, err = s.requestWebTokens(ctx, "login", map[string]string{"email": account.email, "password": account.password})
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		account.lastError = err
		account.retryAt = now.Add(2 * time.Minute)
		var status upstreamHTTPStatus
		switch {
		case errors.Is(err, errWebSignIn), errors.As(err, &status) && (status == 400 || status == 401 || status == 403 || status == 422):
			// A rejected sign-in or human challenge needs account action.
			account.lastError, account.retryAt = errWebSignIn, time.Time{}
			account.humanVerification.Store(true)
		case errors.Is(err, upstreamHTTPStatus(http.StatusTooManyRequests)):
			account.retryAt = now.Add(15 * time.Minute)
		}
		return "", account.lastError
	}
	ttl := webSessionTTL(tokens.Lifetime)
	account.access, account.refresh = tokens.Access, tokens.Refresh
	account.renewAt = now.Add(ttl - min(time.Minute, ttl/5))
	account.lastError = nil
	account.humanVerification.Store(false)
	return account.access, nil
}

func webSessionTTL(seconds int64) time.Duration {
	const maxLifetime = 366 * 24 * time.Hour
	if seconds <= 0 {
		return 15 * time.Minute
	}
	if seconds > int64(maxLifetime/time.Second) {
		return maxLifetime
	}
	return time.Duration(seconds) * time.Second
}

// The only callers use fixed first-party login and refresh endpoints. Never
// include account details or the website response body in errors.
func (s *Service) requestWebTokens(ctx context.Context, path string, fields map[string]string) (webTokens, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return webTokens{}, errors.New("构建北辰网页登录请求失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webAuthURL+path, bytes.NewReader(raw))
	if err != nil {
		return webTokens{}, errors.New("构建北辰网页登录请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	body, err := s.readResponse(req)
	if err != nil {
		return webTokens{}, err
	}
	var reply struct {
		Code *int      `json:"code"`
		Data webTokens `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.Code == nil || *reply.Code != 0 {
		return webTokens{}, errors.New("北辰网页登录响应格式无效")
	}
	if reply.Data.TwoStep {
		return webTokens{}, errWebSignIn
	}
	t := reply.Data
	if !strings.EqualFold(t.Type, "Bearer") || !validPanelBearer(t.Access) || t.Lifetime < 0 ||
		(t.Refresh != "" && !validPanelBearer(t.Refresh)) {
		return webTokens{}, errors.New("北辰网页登录响应缺少有效会话")
	}
	return t, nil
}
