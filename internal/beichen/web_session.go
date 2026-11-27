package beichen

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

var errWebSignIn = errors.New("北辰官网要求核实学校账号或完成登录验证")

// The school signs in through the same endpoints as the Beichen website.
type webSession struct {
	email, password   string
	mu                sync.Mutex
	access, refresh   string
	generation        uint64
	renewAt           time.Time
	legacyRejected    bool
	lastError         error
	humanVerification atomic.Bool
	retryAt           time.Time
	expiresAt         time.Time
	save              func(access, refresh string, expiresAt time.Time) error
	savePending       bool
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
	return s.configureWebSessionTokens(email, access, refresh, expiresAt, nil)
}

func (s *Service) configureWebSessionTokens(email, access, refresh string, expiresAt time.Time,
	save func(access, refresh string, expiresAt time.Time) error) error {
	email = strings.TrimSpace(email)
	access, refresh = strings.TrimSpace(access), strings.TrimSpace(refresh)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || !validPanelBearer(access) ||
		!validPanelBearer(refresh) || expiresAt.IsZero() {
		return errors.New("北辰网页会话需要有效的账号邮箱、双令牌和到期时间")
	}
	renewAt := expiresAt.Add(-time.Minute)
	if now := s.now(); renewAt.Before(now) {
		renewAt = now
	}
	account := s.webAccount
	if account == nil {
		// Install the first session before starting workers. Replacements
		// retain this object and serialize with refresh and snapshot commits.
		s.webAccount = &webSession{email: email, access: access, refresh: refresh,
			generation: 1, renewAt: renewAt, expiresAt: expiresAt, save: save}
		return nil
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	account.email, account.password = email, ""
	account.save, account.savePending = save, false
	account.installTokens(access, refresh, expiresAt, renewAt)
	return nil
}

// ConfigureSavedWebSession installs the delivered capture or its saved rotation.
func (s *Service) ConfigureSavedWebSession(store *schoolseal.SessionStore) error {
	session, err := store.Load()
	if err != nil {
		return err
	}
	return s.configureWebSessionTokens(session.WebEmail, session.WebAccess, session.WebRefresh, session.WebExpiresAt, store.Save)
}

func (s *Service) WebSessionConfigured() bool { return s.webAccount != nil }

func (s *Service) deliveredWebSessionConfigured() bool {
	account := s.webAccount
	if account == nil {
		return false
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	// Only ConfigureSavedWebSession installs a rotation store for a release capture.
	return account.save != nil
}

func (s *Service) SetWebSessionError(err error) { s.webSessionErr = err }

func (s *Service) WebSessionError() string {
	if s.webSessionErr == nil {
		return ""
	}
	return s.webSessionErr.Error()
}

// The bearer-only callback used by the fallback path can report the version
// of its actual read to the snapshot writer, including after a 401 retry.
type capturedWebVersionKey struct{}

func readWithCapturedWebSession[T any](ctx context.Context, s *Service, read func(string) (T, error)) (T, error) {
	version, _ := ctx.Value(capturedWebVersionKey{}).(*[]byte)
	return readWithCapturedPanelSession(ctx, s, true, func(auth panelAuthorization) (T, error) {
		if version != nil {
			*version = auth.tokenVersion
		}
		return read(auth.bearer)
	})
}

// Captured sessions use an 8-byte generation in panelAuthorization.tokenVersion.
// A successfully decrypted database version is AES-GCM ciphertext (at least
// 28 bytes for its nonce and tag), so the representations cannot overlap.
const capturedWebVersionSize = 8

func readWithCapturedPanelSession[T any](ctx context.Context, s *Service, retry bool,
	read func(panelAuthorization) (T, error)) (T, error) {
	for {
		bearer, generation, err := s.webPanelAccess(ctx, false)
		if err != nil {
			var zero T
			return zero, err
		}
		auth := panelAuthorization{bearer: bearer}
		if generation != 0 {
			auth.tokenVersion = binary.BigEndian.AppendUint64(nil, generation)
		}
		value, err := read(auth)
		if !panelAuthFailed(err) {
			return value, err
		}
		s.rejectCapturedWebSession(bearer, generation)
		if !retry {
			return value, err
		}
		retry = false
	}
}

func (s *Service) rejectCapturedWebSession(bearer string, generation uint64) {
	account := s.webAccount
	if account == nil {
		return
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	if bearer == account.access {
		// Key reads may also report a bearer-only rejection. Resolve it here
		// using the generation actually sent with the request.
		account.legacyRejected = false
		if generation == account.generation {
			account.renewAt = time.Time{}
		}
	}
}

// A protected endpoint can reject a session before its advertised expiry. Only
// invalidate the bearer used by that request, never a newer concurrent login.
func (s *Service) webReadError(bearer string, err error) error {
	account := s.webAccount
	if account != nil && errors.Is(err, upstreamHTTPStatus(http.StatusUnauthorized)) {
		account.mu.Lock()
		if bearer == account.access {
			// Bearer-only readers can request renewal for their next attempt.
			// Versioned readers handle rejections using their own generation.
			account.legacyRejected = true
		}
		account.mu.Unlock()
	}
	return err
}

func (s *Service) webPanelIdentity() string {
	account := s.webAccount
	if account == nil {
		return ""
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	return account.email
}

func (s *Service) webPanelToken(ctx context.Context) (string, error) {
	bearer, _, err := s.webPanelAccess(ctx, true)
	return bearer, err
}

func (s *Service) webPanelAccess(ctx context.Context, useLegacyRejection bool) (string, uint64, error) {
	account := s.webAccount
	if account == nil {
		return "", 0, nil
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := account.saveRotation(); err != nil {
		return "", 0, err
	}
	now := s.now()
	if account.access != "" && now.Before(account.renewAt) && (!useLegacyRejection || !account.legacyRejected) {
		return account.access, account.generation, nil
	}
	if account.lastError != nil && (account.retryAt.IsZero() || now.Before(account.retryAt)) {
		return "", 0, account.lastError
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
			return "", 0, account.lastError
		}
		tokens, err = s.requestWebTokens(ctx, "login", map[string]string{"email": account.email, "password": account.password})
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
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
		return "", 0, account.lastError
	}
	ttl := webSessionTTL(tokens.Lifetime)
	if tokens.Refresh == "" {
		tokens.Refresh = account.refresh
	}
	account.installTokens(tokens.Access, tokens.Refresh, now.Add(ttl), now.Add(ttl-min(time.Minute, ttl/5)))
	account.savePending = account.save != nil
	if err := account.saveRotation(); err != nil {
		return "", 0, err
	}
	return account.access, account.generation, nil
}

// Called under mu after a successful login, refresh or explicit replacement.
// A refresh may rotate only its refresh credential, so bearer equality alone
// is insufficient. Retrying a failed request or durable write never calls this.
func (account *webSession) installTokens(access, refresh string, expiresAt, renewAt time.Time) {
	account.access, account.refresh = access, refresh
	account.expiresAt, account.renewAt = expiresAt, renewAt
	account.generation++
	account.lastError, account.retryAt = nil, time.Time{}
	account.legacyRejected = false
	account.humanVerification.Store(false)
}

func (account *webSession) saveRotation() error {
	if account.savePending {
		// Retry the durable write before using or refreshing the new pair. The
		// upstream may already have revoked the previous refresh credential.
		if err := account.save(account.access, account.refresh, account.expiresAt); err != nil {
			return err
		}
		account.savePending = false
	}
	return nil
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
