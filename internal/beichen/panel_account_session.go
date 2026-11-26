package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Each authorization retains the version of the encrypted bearer used for a
// request. A late 401 cannot invalidate a concurrently rotated session.
type panelAuthorization struct {
	bearer       string
	tokenVersion []byte
	verified     bool
	shared       bool
	local        bool
}

// A cleared login keeps an empty row so the previous environment token or an
// older saved session cannot resume. It is not a usable account, so callers
// must decide whether the deployment-delivered school session may still read.
var errPanelAccountCleared = fmt.Errorf("%w（网页登录已清除）", errPanelAccountMissing)

func (s *Service) storedPanelAuthorization(ctx context.Context, provider uuid.UUID) (panelAuthorization, bool, error) {
	if s.db == nil || provider == uuid.Nil {
		return panelAuthorization{}, false, nil
	}
	account, found, err := readPanelAccount(ctx, s.db, provider)
	if err != nil || !found {
		return panelAuthorization{}, found, err
	}
	if panelAccountCleared(account) {
		return panelAuthorization{}, true, errPanelAccountCleared
	}
	if account.status != panelStateActive || account.hasExpired(s.now()) {
		var sessionErr error
		account, sessionErr, err = s.updatePanelSession(ctx, provider, false)
		if err != nil || sessionErr != nil {
			return panelAuthorization{}, true, errors.Join(err, sessionErr)
		}
	}
	bearer, err := s.decryptPanelSecret(provider, "token", account.token)
	if err != nil || !validPanelBearer(bearer) {
		return panelAuthorization{}, true, errPanelAccountCrypto
	}
	return panelAuthorization{bearer: bearer, tokenVersion: account.token, verified: account.lastVerifiedAt != nil}, true, nil
}

func panelAccountCleared(account panelAccount) bool {
	return account.status == panelStateNone && len(account.token) == 0 && len(account.password) == 0
}

func (s *Service) updatePanelSession(ctx context.Context, provider uuid.UUID, verify bool) (panelAccount, error, error) {
	var account panelAccount
	var sessionErr error
	err := s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		var err error
		account, _, err = readPanelAccount(ctx, tx, provider)
		if err != nil {
			return err
		}
		var changed bool
		changed, sessionErr = s.maintainPanelSession(ctx, &account, verify)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if changed {
			return s.savePanelAccount(ctx, tx, account, uuid.Nil)
		}
		return nil
	})
	return account, sessionErr, err
}

func (s *Service) maintainPanelSession(ctx context.Context, account *panelAccount, verify bool) (bool, error) {
	if account.status == panelStateNone {
		return false, errPanelAccountMissing
	}
	if len(account.token) == 0 && len(account.password) == 0 {
		return false, errors.New(account.lastError)
	}
	if account.mode == panelModeToken && (account.status == panelStateExpired || account.hasExpired(s.now())) {
		if account.status == panelStateExpired {
			return false, errPanelTokenExpired
		}
		account.fail(panelStateExpired, errPanelTokenExpired.Error())
		return true, errPanelTokenExpired
	}
	if !verify && account.status == panelStateError &&
		(account.mode == panelModeToken || s.now().Before(account.updatedAt.Add(2*time.Minute))) {
		return false, errors.New(account.lastError)
	}
	renewed := account.mode == panelModePassword && (account.status != panelStateActive || account.hasExpired(s.now()))
	if renewed {
		if err := s.renewPanelSession(ctx, account); err != nil {
			account.fail(panelStateError, panelRenewalMessage(err))
			return true, errors.New(account.lastError)
		}
	}
	if !verify {
		return renewed, nil
	}
	expiry, err := s.verifyPanelSession(ctx, *account)
	if panelAuthFailed(err) && account.mode == panelModePassword && !renewed {
		if err := s.renewPanelSession(ctx, account); err != nil {
			account.fail(panelStateError, panelRenewalMessage(err))
			return true, errors.New(account.lastError)
		}
		expiry, err = s.verifyPanelSession(ctx, *account)
	}
	if err != nil {
		if account.mode == panelModeToken && panelAuthFailed(err) {
			account.fail(panelStateExpired, errPanelTokenExpired.Error())
			return true, errPanelTokenExpired
		}
		message := "验证北辰网页会话失败，请稍后重试"
		if panelAuthFailed(err) {
			message = "北辰拒绝续期后的网页会话，请检查账号权限"
		} else if errors.Is(err, errPanelAccountCrypto) {
			message = errPanelAccountCrypto.Message
		}
		account.fail(panelStateError, message)
		return true, errors.New(message)
	}
	if expiry != nil {
		account.expiresAt = earliestPanelExpiry(account.expiresAt, *expiry)
	}
	account.verified(s.now())
	return true, nil
}

func (s *Service) verifyPanelSession(ctx context.Context, account panelAccount) (*time.Time, error) {
	bearer, err := s.decryptPanelSecret(account.provider, "token", account.token)
	if err != nil || !validPanelBearer(bearer) {
		return nil, errPanelAccountCrypto
	}
	return s.verifyWebTokens(ctx, bearer)
}

func (s *Service) renewPanelSession(ctx context.Context, account *panelAccount) error {
	refresh, err := s.panelRefreshCredential(*account)
	if err != nil {
		return err
	}
	if refresh.Token != "" {
		tokens, err := s.refreshWebTokens(ctx, refresh)
		if err == nil {
			if tokens.Refresh == "" && tokens.Cookie == "" {
				tokens.Refresh, tokens.Cookie = refresh.Token, refresh.CookieName
			}
			return s.saveWebTokens(account, tokens)
		}
		if !panelSignInRejected(err) {
			return err
		}
		account.refresh = nil
	}
	email, err := s.decryptPanelSecret(account.provider, "email", account.email)
	if err != nil {
		return err
	}
	password, err := s.decryptPanelSecret(account.provider, "password", account.password)
	if err != nil {
		return err
	}
	tokens, err := s.signInPanelAccount(ctx, email, password)
	if err != nil {
		return err
	}
	return s.saveWebTokens(account, tokens)
}

func panelRenewalMessage(err error) string {
	if errors.Is(err, errPanelAccountCrypto) {
		return errPanelAccountCrypto.Message
	}
	return panelSignInMessage(err)
}

func (s *Service) markPanelRejected(ctx context.Context, provider uuid.UUID, authorization panelAuthorization, final bool) error {
	return s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		account, found, err := readPanelAccount(ctx, tx, provider)
		if err != nil || !found || !bytes.Equal(account.token, authorization.tokenVersion) || account.status == panelStateExpired {
			return err
		}
		switch {
		case account.mode == panelModeToken:
			account.fail(panelStateExpired, errPanelTokenExpired.Error())
		case final:
			account.fail(panelStateError, "北辰拒绝续期后的网页会话，请检查账号权限")
		default:
			account.fail(panelStateExpired, "北辰拒绝当前网页会话，需续期")
		}
		return s.savePanelAccount(ctx, tx, account, uuid.Nil)
	})
}

func (s *Service) notePanelVerified(ctx context.Context, provider uuid.UUID, authorization panelAuthorization, force bool) error {
	if authorization.tokenVersion == nil || authorization.verified && !force {
		return nil
	}
	return s.withPanelAccountLock(ctx, provider, func(tx *sql.Tx) error {
		account, found, err := readPanelAccount(ctx, tx, provider)
		if err != nil || !found || !bytes.Equal(authorization.tokenVersion, account.token) || account.status != panelStateActive {
			return err
		}
		account.verified(s.now())
		return s.savePanelAccount(ctx, tx, account, uuid.Nil)
	})
}
