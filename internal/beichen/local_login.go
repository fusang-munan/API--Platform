package beichen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
)

var errLocalLoginMissing = errors.New("请先运行 go run ./cmd/beichen-session login --window 登录北辰")
var errLocalReauthenticate = errors.New("北辰网页会话需要重新登录，请打开本机登录窗口")

type LocalLoginInput struct {
	PanelAccountLogin
	Code     string `json:"code"`
	Remember bool   `json:"remember"`
}

type LocalLoginStatus struct {
	Account        string     `json:"account"`
	ExpiresAt      *time.Time `json:"expires_at"`
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	Expired        bool       `json:"expired"`
	CanRefresh     bool       `json:"can_refresh"`
}

type localPanelLogin struct {
	Account           string            `json:"account"`
	Email             string            `json:"email,omitempty"`
	Password          string            `json:"password,omitempty"`
	Access            string            `json:"access_token"`
	Refresh           string            `json:"refresh_token,omitempty"`
	Cookies           []localAuthCookie `json:"cookies,omitempty"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	LastVerifiedAt    *time.Time        `json:"last_verified_at,omitempty"`
	NeedsVerification bool              `json:"needs_verification,omitempty"`
}

func (session localPanelLogin) expired(now time.Time) bool {
	return session.ExpiresAt != nil && !now.Before(*session.ExpiresAt)
}

func (session localPanelLogin) status(now time.Time) *LocalLoginStatus {
	return &LocalLoginStatus{
		Account: session.Account, ExpiresAt: session.ExpiresAt, LastVerifiedAt: session.LastVerifiedAt,
		Expired: session.expired(now), CanRefresh: session.Password != "" || session.Refresh != "" || len(session.authCookies("refresh", now)) != 0,
	}
}

func (s *Service) UseLocalLogin(store *LocalLoginStore) {
	s.localLogin = store
}

func (s *Service) ReadLocalLogin(ctx context.Context) (*LocalLoginStatus, error) {
	if s.localLogin == nil {
		return nil, errLocalLoginMissing
	}
	exists, err := s.localLogin.Exists()
	if err != nil || !exists {
		return nil, err
	}
	var session localPanelLogin
	err = s.localLogin.withLock(ctx, func() error {
		var err error
		session, err = s.localLogin.load()
		return err
	})
	if err != nil {
		return nil, err
	}
	return session.status(s.now()), nil
}

func (s *Service) LoginLocally(ctx context.Context, input LocalLoginInput) (*LocalLoginStatus, error) {
	if s.localLogin == nil {
		return nil, errLocalLoginMissing
	}
	if err := input.validate(); err != nil {
		return nil, err
	}
	input.Code = strings.TrimSpace(input.Code)
	if len(input.Code) > 128 || strings.ContainsFunc(input.Code, unicode.IsControl) ||
		input.Mode == panelModeToken && (input.Code != "" || input.Remember) {
		return nil, errors.New("验证码格式不正确，请核对后重试")
	}
	session := localPanelLogin{Access: input.Token, Account: input.Email, Email: input.Email, Password: input.Password}
	err := s.localLogin.withLock(ctx, func() error {
		if input.Mode == panelModePassword {
			fields := map[string]string{"email": input.Email, "password": input.Password}
			if input.Code != "" {
				fields["code"] = input.Code
			}
			if err := s.receiveLocalTokens(ctx, "login", &session, fields); err != nil {
				return err
			}
		} else {
			session.ExpiresAt = webBearerExpiry(session.Access)
		}
		if err := s.verifyLocalAuth(ctx, &session); err != nil {
			return localSessionError(err)
		}
		if !input.Remember {
			session.Email, session.Password = "", ""
		}
		return s.localLogin.save(session)
	})
	if err != nil {
		return nil, err
	}
	return session.status(s.now()), nil
}

func (s *Service) RefreshLocalLogin(ctx context.Context) (*LocalLoginStatus, error) {
	session, err := s.currentLocalLogin(ctx, true, true, "")
	if err != nil {
		return nil, err
	}
	return session.status(s.now()), nil
}

func (s *Service) VerifyLocalLogin(ctx context.Context) (*LocalLoginStatus, error) {
	session, err := s.currentLocalLogin(ctx, true, false, "")
	if err != nil {
		return nil, err
	}
	return session.status(s.now()), nil
}

func (s *Service) localLoginAuthorization(ctx context.Context, rejected string) (panelAuthorization, error) {
	session, err := s.currentLocalLogin(ctx, false, false, rejected)
	if err != nil {
		return panelAuthorization{}, err
	}
	return panelAuthorization{bearer: session.Access, local: true}, nil
}

func (s *Service) currentLocalLogin(ctx context.Context, verify, force bool, rejected string) (localPanelLogin, error) {
	var session localPanelLogin
	if s.localLogin == nil {
		return session, errLocalLoginMissing
	}
	err := s.localLogin.withLock(ctx, func() error {
		var err error
		session, err = s.localLogin.load()
		if errors.Is(err, os.ErrNotExist) {
			return errLocalLoginMissing
		}
		if err != nil {
			return err
		}
		if force || session.expired(s.now()) || rejected != "" && session.Access == rejected {
			return s.renewLocalLogin(ctx, &session)
		}
		if !verify && !session.NeedsVerification {
			return nil
		}
		if err := s.verifyLocalAuth(ctx, &session); panelAuthFailed(err) {
			return s.renewLocalLogin(ctx, &session)
		} else if err != nil {
			return err
		}
		return s.localLogin.save(session)
	})
	return session, err
}

func (s *Service) renewLocalLogin(ctx context.Context, session *localPanelLogin) error {
	err := errLocalReauthenticate
	if session.Refresh != "" || len(session.authCookies("refresh", s.now())) != 0 {
		fields := map[string]string{}
		if session.Refresh != "" {
			fields["refresh_token"] = session.Refresh
		}
		err = s.receiveLocalTokens(ctx, "refresh", session, fields)
	}
	if session.Password != "" && (errors.Is(err, errLocalReauthenticate) || panelSignInRejected(err)) {
		next := localPanelLogin{Account: session.Account, Email: session.Email, Password: session.Password}
		err = s.receiveLocalTokens(ctx, "login", &next, map[string]string{"email": next.Email, "password": next.Password})
		if err == nil {
			*session = next
		}
	}
	if err != nil {
		return localSessionError(err)
	}
	// Retain a rotated credential even if verifying the new bearer times out.
	// Crawlers verify the pending session before using it.
	if err := s.localLogin.save(*session); err != nil {
		return err
	}
	if err := s.verifyLocalAuth(ctx, session); err != nil {
		return localSessionError(err)
	}
	return s.localLogin.save(*session)
}

func localSessionError(err error) error {
	if panelSignInRejected(err) && !errors.Is(err, errWebSignIn) || errors.Is(err, errExpiredWebSession) {
		return fmt.Errorf("%w。%s", errLocalReauthenticate, err)
	}
	return err
}
