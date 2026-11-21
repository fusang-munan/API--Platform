package beichen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const webAuthURL = "https://beichenapi.com/api/v1/auth/"

var errWebAuthResponse = errors.New("北辰网页登录接口响应格式无效")
var errExpiredWebSession = errors.New("北辰返回的网页会话已过期，请核对服务器时间")

type webRefreshCredential struct {
	Token      string `json:"token"`
	CookieName string `json:"cookie_name,omitempty"`
}

type webTokens struct {
	Access   string          `json:"access_token"`
	Refresh  string          `json:"refresh_token"`
	Lifetime int64           `json:"expires_in"`
	Expiry   json.RawMessage `json:"expires_at"`
	Type     string          `json:"token_type"`
	TwoStep  bool            `json:"requires_2fa"`
	Cookie   string          `json:"-"`
}

func (s *Service) requestWebTokens(ctx context.Context, path string, fields map[string]string, cookies ...*http.Cookie) (webTokens, error) {
	body, received, err := s.requestWebAuth(ctx, http.MethodPost, path, "", fields, cookies...)
	if err != nil {
		return webTokens{}, err
	}
	data, err := webAuthData(body)
	if err != nil {
		return webTokens{}, err
	}
	var tokens webTokens
	if json.Unmarshal(data, &tokens) != nil {
		return webTokens{}, errWebAuthResponse
	}
	if tokens.TwoStep {
		return webTokens{}, errWebSignIn
	}
	if (tokens.Type != "" && !strings.EqualFold(tokens.Type, "Bearer")) || !validPanelBearer(tokens.Access) || tokens.Lifetime < 0 {
		return webTokens{}, errWebAuthResponse
	}
	if tokens.Refresh == "" {
		for _, cookie := range received {
			if validRefreshCookieName(cookie.Name) {
				tokens.Cookie = cookie.Name
				if cookie.MaxAge >= 0 && (cookie.Expires.IsZero() || cookie.Expires.After(s.now())) {
					tokens.Refresh = cookie.Value
				}
				break
			}
		}
	}
	if tokens.Refresh != "" && !validPanelBearer(tokens.Refresh) {
		return webTokens{}, errWebAuthResponse
	}
	now := s.now()
	if !now.Before(tokens.expiresAt(now)) {
		return webTokens{}, errExpiredWebSession
	}
	return tokens, nil
}

func (tokens webTokens) expiresAt(now time.Time) time.Time {
	var expiry *time.Time
	for _, hint := range []*time.Time{parseWebExpiry(tokens.Expiry), webBearerExpiry(tokens.Access)} {
		if hint != nil {
			expiry = earliestPanelExpiry(expiry, *hint)
		}
	}
	if expiry == nil || tokens.Lifetime > 0 {
		expiry = earliestPanelExpiry(expiry, now.UTC().Add(webSessionTTL(tokens.Lifetime)))
	}
	return *expiry
}

func validRefreshCookieName(name string) bool {
	switch strings.ToLower(name) {
	case "refresh_token", "refreshtoken", "__host-refresh_token", "__secure-refresh_token":
		return true
	default:
		return false
	}
}

func (s *Service) refreshWebTokens(ctx context.Context, refresh webRefreshCredential) (webTokens, error) {
	if refresh.CookieName != "" {
		return s.requestWebTokens(ctx, "refresh", map[string]string{}, &http.Cookie{Name: refresh.CookieName, Value: refresh.Token})
	}
	return s.requestWebTokens(ctx, "refresh", map[string]string{"refresh_token": refresh.Token})
}

func (s *Service) verifyWebTokens(ctx context.Context, bearer string) (*time.Time, error) {
	expires, supported, err := s.verifyWebSessionEndpoint(ctx, bearer)
	if err != nil || supported {
		return expires, err
	}
	return nil, s.verifyWebChannels(ctx, bearer)
}

func (s *Service) verifyWebSessionEndpoint(ctx context.Context, bearer string) (*time.Time, bool, error) {
	body, _, err := s.requestWebAuth(ctx, http.MethodGet, "session", bearer, nil)
	var status upstreamHTTPStatus
	if errors.As(err, &status) && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented) {
		return nil, false, nil
	}
	if err != nil || body == nil {
		return nil, true, err
	}
	data, err := webAuthData(body)
	if err != nil {
		return nil, true, err
	}
	var session struct {
		Authenticated *bool           `json:"authenticated"`
		User          json.RawMessage `json:"user"`
		Session       json.RawMessage `json:"session"`
		Expiry        json.RawMessage `json:"expires_at"`
		Lifetime      int64           `json:"expires_in"`
	}
	if json.Unmarshal(data, &session) != nil || len(data) == 0 || data[0] != '{' {
		return nil, true, errWebAuthResponse
	}
	if session.Authenticated != nil && !*session.Authenticated {
		return nil, true, upstreamHTTPStatus(http.StatusUnauthorized)
	}
	if session.Authenticated == nil && !hasWebSessionData(session.User) && !hasWebSessionData(session.Session) {
		return nil, false, nil
	}
	expiry := parseWebExpiry(session.Expiry)
	if session.Lifetime > 0 {
		expiry = earliestPanelExpiry(expiry, s.now().UTC().Add(webSessionTTL(session.Lifetime)))
	}
	if expiry != nil && !s.now().Before(*expiry) {
		return nil, true, upstreamHTTPStatus(http.StatusUnauthorized)
	}
	return expiry, true, nil
}

func hasWebSessionData(raw json.RawMessage) bool {
	return len(raw) != 0 && raw[0] == '{' && !bytes.Equal(raw, []byte("{}"))
}

func (s *Service) verifyWebChannels(ctx context.Context, bearer string) error {
	body, err := s.fetchPanelBody(ctx, bearer)
	if err != nil {
		return err
	}
	// An empty array is valid for an account with no available channels.
	data := bytes.TrimSpace(body)
	if len(data) == 0 || data[0] != '[' {
		data, err = panelData(body)
		if err != nil {
			return err
		}
	}
	var channels []json.RawMessage
	if json.Unmarshal(data, &channels) != nil || channels == nil {
		return errWebAuthResponse
	}
	return nil
}

func (s *Service) requestWebAuth(ctx context.Context, method, path, bearer string, fields map[string]string, cookies ...*http.Cookie) ([]byte, []*http.Cookie, error) {
	response, err := s.sendWebAuth(ctx, method, path, bearer, fields, cookies...)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent && (path == "logout" || path == "session") {
		return nil, nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, upstreamHTTPStatus(response.StatusCode)
	}
	body, err := readWebAuthBody(response)
	if err != nil {
		return nil, nil, err
	}
	return body, response.Cookies(), nil
}

func (s *Service) sendWebAuth(ctx context.Context, method, path, bearer string, fields map[string]string, cookies ...*http.Cookie) (*http.Response, error) {
	var raw []byte
	if fields != nil {
		raw, _ = json.Marshal(fields)
	}
	req, err := http.NewRequestWithContext(ctx, method, webAuthURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("构建北辰网页登录请求失败")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	if fields != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("连接北辰网页登录接口失败或超时")
	}
	return response, nil
}

func readWebAuthBody(response *http.Response) ([]byte, error) {
	const maxAuthBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAuthBytes+1))
	if err != nil || len(body) > maxAuthBytes {
		return nil, errWebAuthResponse
	}
	return body, nil
}

func webAuthData(body []byte) (json.RawMessage, error) {
	var reply struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return nil, errWebAuthResponse
	}
	switch string(reply.Code) {
	case "":
		return bytes.TrimSpace(body), nil
	case "0", `"0"`:
		return bytes.TrimSpace(reply.Data), nil
	case "400", `"400"`:
		return nil, upstreamHTTPStatus(http.StatusBadRequest)
	case "401", `"401"`, `"UNAUTHORIZED"`, `"TOKEN_EXPIRED"`:
		return nil, upstreamHTTPStatus(http.StatusUnauthorized)
	case "403", `"403"`, `"FORBIDDEN"`:
		return nil, upstreamHTTPStatus(http.StatusForbidden)
	case "422", `"422"`:
		return nil, upstreamHTTPStatus(http.StatusUnprocessableEntity)
	case "429", `"429"`:
		return nil, upstreamHTTPStatus(http.StatusTooManyRequests)
	default:
		return nil, errWebAuthResponse
	}
}

// Read a JWT's expiry as a hint only. Authentication is checked by Beichen.
func webBearerExpiry(bearer string) *time.Time {
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return nil
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var envelope struct {
		Expiry json.RawMessage `json:"exp"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	return parseWebExpiry(envelope.Expiry)
}

func parseWebExpiry(value json.RawMessage) *time.Time {
	if seconds, err := strconv.ParseInt(string(value), 10, 64); err == nil && seconds > 0 && seconds <= 253402300799 {
		at := time.Unix(seconds, 0).UTC()
		return &at
	}
	var at time.Time
	if json.Unmarshal(value, &at) == nil && !at.IsZero() {
		utc := at.UTC()
		return &utc
	}
	return nil
}

func earliestPanelExpiry(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.Before(*current) {
		return &candidate
	}
	return current
}
