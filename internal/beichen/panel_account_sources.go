package beichen

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

// ConfigurePanelFallback sets the startup bearer independently of the legacy
// per-Key group mapping.
func (s *Service) ConfigurePanelFallback(token string) {
	s.panelFallback = strings.TrimSpace(token)
}

func (s *Service) managedPanelAuthorization(ctx context.Context, provider uuid.UUID, fallback string) (panelAuthorization, bool, error) {
	if s.localLogin != nil {
		authorization, err := s.localLoginAuthorization(ctx, "")
		return authorization, true, err
	}
	authorization, stored, err := s.storedPanelAuthorization(ctx, provider)
	if stored && errors.Is(err, errPanelAccountCleared) {
		// The cleared row still blocks the old environment token. The school
		// session delivered with a release is deployment configuration, so it
		// remains the price source even after a saved login was cleared.
		if s.WebSessionConfigured() {
			return panelAuthorization{}, false, nil
		}
		return panelAuthorization{}, true, errPanelAccountMissing
	}
	if stored || err != nil {
		return authorization, true, err
	}
	if s.sharedPanelErr != nil {
		return panelAuthorization{}, true, s.sharedPanelErr
	}
	if s.sharedPanel != nil {
		if provider != uuid.Nil && provider != s.sharedPanel.provider {
			return panelAuthorization{}, true, invalid("本机北辰授权与当前供应商不匹配")
		}
		authorization, err := s.requestSharedPanelSession(ctx, "")
		return authorization, true, err
	}
	if fallback == "" {
		fallback = s.panelFallback
	}
	if fallback == "" {
		return panelAuthorization{}, false, nil
	}
	if !validPanelBearer(fallback) {
		return panelAuthorization{}, true, invalid("BEICHEN_PANEL_TOKEN 格式无效，请提供网页会话 Token")
	}
	if expiry := webBearerExpiry(fallback); expiry != nil && !s.now().Before(*expiry) {
		return panelAuthorization{}, true, errEnvironmentPanelToken
	}
	return panelAuthorization{bearer: fallback}, true, nil
}

var errEnvironmentPanelToken = errors.New("北辰网页 Token 已失效，请更新 BEICHEN_PANEL_TOKEN")

func readWithPanelAuthorization[T any](ctx context.Context, service *Service, provider uuid.UUID,
	authorization panelAuthorization, retry bool, read func(string) (T, error)) (T, error) {
	return readWithPanelSession(ctx, service, provider, authorization, retry, func(auth panelAuthorization) (T, error) {
		return read(auth.bearer)
	})
}

func readWithPanelSession[T any](ctx context.Context, service *Service, provider uuid.UUID,
	authorization panelAuthorization, retry bool, read func(panelAuthorization) (T, error)) (T, error) {
	value, err := read(authorization)
	if err == nil {
		return value, service.notePanelVerified(ctx, provider, authorization, !retry)
	}
	if !panelAuthFailed(err) {
		return value, err
	}
	if authorization.local {
		renewed, err := service.localLoginAuthorization(ctx, authorization.bearer)
		if err != nil {
			return value, err
		}
		value, err = read(renewed)
		if panelAuthFailed(err) {
			return value, localSessionError(err)
		}
		return value, err
	}
	if authorization.shared {
		renewed, err := service.requestSharedPanelSession(ctx, panelBearerDigest(authorization.bearer))
		if err != nil {
			return value, err
		}
		if !retry || renewed.bearer == authorization.bearer {
			return value, errors.New("本次检测使用的北辰网页会话遭到拒绝，请查看学校服务器会话状态后重试")
		}
		return read(renewed)
	}
	if authorization.tokenVersion == nil {
		return value, errEnvironmentPanelToken
	}
	if err := service.markPanelRejected(ctx, provider, authorization, false); err != nil {
		return value, err
	}
	renewed, found, err := service.storedPanelAuthorization(ctx, provider)
	if err != nil {
		return value, err
	}
	if !found {
		return value, errPanelAccountMissing
	}
	if !retry {
		return value, errors.New("北辰拒绝本次检查使用的 Token，网页会话已更新，请重新运行 check")
	}
	value, err = read(renewed)
	if panelAuthFailed(err) {
		if err := service.markPanelRejected(ctx, provider, renewed, true); err != nil {
			return value, err
		}
		return value, errors.New("续期后北辰渠道页仍拒绝网页会话")
	}
	if err == nil {
		err = service.notePanelVerified(ctx, provider, renewed, false)
	}
	return value, err
}

func (s *Service) managedChannelQuote(ctx context.Context, access priceAccess, key string, group int64,
	info billingInfo, entries []catalog.ProviderEntry) (panelQuote, bool) {
	authorization, handled, err := s.managedPanelAuthorization(ctx, access.providerID, "")
	if err != nil {
		return panelQuote{groupID: group, err: err}, true
	}
	if !handled {
		return panelQuote{}, false
	}
	quote, err := readWithPanelAuthorization(ctx, s, access.providerID, authorization, true, func(token string) (panelQuote, error) {
		quote := s.fetchPanelQuote(ctx, token, key, group, access, info, entries)
		return quote, quote.err
	})
	quote.err = err
	return quote, true
}

// CheckPanelAccount reads channels/available once. It prepares a new password
// session if rejected and leaves the new directory check to the caller.
func (s *Service) CheckPanelAccount(ctx context.Context, provider uuid.UUID) (PanelCrawlSummary, error) {
	if s.db != nil {
		resolved, err := s.ResolvePanelProvider(ctx, provider)
		if err != nil {
			return PanelCrawlSummary{}, err
		}
		provider = resolved
	}
	authorization, handled, err := s.managedPanelAuthorization(ctx, provider, "")
	if err != nil {
		return PanelCrawlSummary{}, err
	}
	if !handled {
		return PanelCrawlSummary{}, errPanelAccountMissing
	}
	crawl, err := readWithPanelAuthorization(ctx, s, provider, authorization, false, func(token string) (directoryCrawl, error) {
		return s.fetchWebsiteCrawl(ctx, token)
	})
	return crawl.panelSummary(), err
}

func (s *Service) CrawlPanelDirectory(ctx context.Context, provider uuid.UUID) (PanelCrawlSummary, error) {
	provider, err := s.ResolvePanelProvider(ctx, provider)
	if err != nil {
		return PanelCrawlSummary{}, err
	}
	summary, err := s.syncWebsiteDirectory(ctx, provider, true)
	if err != nil && !errors.Is(err, errWebsiteDirectoryPartial) {
		var appErr *domain.Error
		if errors.As(err, &appErr) {
			return PanelCrawlSummary{}, domain.NewError(appErr.Code, appErr.Message)
		}
		return PanelCrawlSummary{}, domain.NewError(domain.CodeDependencyUnavailable, err.Error())
	}
	return summary, nil
}
