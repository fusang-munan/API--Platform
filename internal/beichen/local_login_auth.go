package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

type localAuthReply struct {
	webTokens
	Authenticated *bool           `json:"authenticated"`
	Email         string          `json:"email"`
	User          json.RawMessage `json:"user"`
	Session       json.RawMessage `json:"session"`
}

func (reply localAuthReply) expiry(now time.Time) *time.Time {
	expires := parseWebExpiry(reply.Expiry)
	if reply.Lifetime > 0 {
		expires = earliestPanelExpiry(expires, now.Add(webSessionTTL(reply.Lifetime)))
	}
	return expires
}

func (s *Service) requestLocalAuth(ctx context.Context, method, path string, session *localPanelLogin, fields map[string]string) (localAuthReply, error) {
	var bearer string
	if path == "session" {
		bearer = session.Access
	}
	now := s.now().UTC()
	response, err := s.sendWebAuth(ctx, method, path, bearer, fields, session.authCookies(path, now)...)
	if err != nil {
		return localAuthReply{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent && path == "session" {
		yes := true
		session.updateAuthCookies(response.Cookies(), now)
		return localAuthReply{Authenticated: &yes}, nil
	}
	body, readErr := readWebAuthBody(response)
	defer clear(body)
	data, dataErr := webAuthData(body)
	if dataErr == nil && len(data) == 0 {
		dataErr = errWebAuthResponse
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	// Accept a data envelope with no code, in addition to a flat response.
	if bytes.Equal(bytes.TrimSpace(body), data) && json.Unmarshal(data, &envelope) == nil && len(envelope.Data) != 0 {
		data = bytes.TrimSpace(envelope.Data)
	}
	if readErr != nil || dataErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		cause := dataErr
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			cause = upstreamHTTPStatus(response.StatusCode)
		}
		return localAuthReply{}, localAuthError(cause, body, response, session, fields)
	}
	var reply localAuthReply
	if json.Unmarshal(data, &reply) != nil || len(data) == 0 || data[0] != '{' {
		return localAuthReply{}, errWebAuthResponse
	}
	if reply.Authenticated != nil && !*reply.Authenticated {
		return localAuthReply{}, localAuthError(upstreamHTTPStatus(http.StatusUnauthorized), body, response, session, fields)
	}
	if reply.TwoStep || path != "session" && reply.Access == "" {
		return localAuthReply{}, localAuthError(errWebAuthResponse, body, response, session, fields)
	}
	session.updateAuthCookies(response.Cookies(), now)
	return reply, nil
}

func localAuthError(cause error, body []byte, response *http.Response, session *localPanelLogin, fields map[string]string) error {
	if cause == nil {
		cause = errWebAuthResponse
	}
	type details struct {
		Code        json.RawMessage `json:"code"`
		Message     string          `json:"message"`
		Msg         string          `json:"msg"`
		Description string          `json:"error_description"`
		Error       json.RawMessage `json:"error"`
		Data        json.RawMessage `json:"data"`
		Access      string          `json:"access_token"`
		Refresh     string          `json:"refresh_token"`
		TwoStep     bool            `json:"requires_2fa"`
		Captcha     bool            `json:"requires_captcha"`
		CaptchaAlt  bool            `json:"captcha_required"`
	}
	var top, nested, nestedError details
	_ = json.Unmarshal(body, &top)
	_ = json.Unmarshal(top.Data, &nested)
	_ = json.Unmarshal(top.Error, &nestedError)
	challenge := false
	var message string
	secrets := []string{fields["password"], fields["code"], session.Password, session.Access, session.Refresh}
	for _, detail := range []details{top, nested, nestedError} {
		var simpleError string
		_ = json.Unmarshal(detail.Error, &simpleError)
		for _, text := range []string{detail.Message, detail.Msg, detail.Description, simpleError} {
			if message == "" && strings.TrimSpace(text) != "" {
				message = text
			}
			challenge = challenge || needsLocalVerification(text)
		}
		challenge = challenge || detail.TwoStep || detail.Captcha || detail.CaptchaAlt || needsLocalVerification(string(detail.Code))
		secrets = append(secrets, detail.Access, detail.Refresh)
	}
	if message == "" && strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		message = string(body)
		challenge = challenge || needsLocalVerification(message)
	}
	for _, cookie := range session.Cookies {
		secrets = append(secrets, cookie.Value)
	}
	for _, cookie := range response.Cookies() {
		secrets = append(secrets, cookie.Value)
	}
	message = safeLocalAuthText(message, secrets...)
	if challenge {
		cause = errWebSignIn
		hint := "请填写验证码后重试，或在北辰网站完成验证后使用 Token 模式。"
		if message == "" {
			return fmt.Errorf("%w。%s", cause, hint)
		}
		return fmt.Errorf("%w。北辰返回：%s。%s", cause, message, hint)
	}
	if message == "" || message == cause.Error() {
		return cause
	}
	return fmt.Errorf("%w。北辰返回：%s", cause, message)
}

func needsLocalVerification(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"验证码", "二次验证", "人机验证", "captcha", "2fa", "two_factor", "two-factor", "verification code", "verification_required", "mfa_required", "otp_required"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func safeLocalAuthText(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[已隐藏]")
			message = strings.ReplaceAll(message, url.QueryEscape(secret), "[已隐藏]")
			quoted, _ := json.Marshal(secret)
			message = strings.ReplaceAll(message, string(quoted[1:len(quoted)-1]), "[已隐藏]")
		}
	}
	message = strings.Join(strings.FieldsFunc(message, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	}), " ")
	if runes := []rune(message); len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return message
}

func (s *Service) verifyLocalAuth(ctx context.Context, session *localPanelLogin) error {
	reply, err := s.requestLocalAuth(ctx, http.MethodGet, "session", session, nil)
	if err != nil {
		return err
	}
	var identity struct {
		Email    string          `json:"email"`
		Username string          `json:"username"`
		User     json.RawMessage `json:"user"`
		Expiry   json.RawMessage `json:"expires_at"`
	}
	_ = json.Unmarshal(reply.Session, &identity)
	if reply.Authenticated == nil && reply.Email == "" && !hasWebSessionData(reply.User) && !hasWebSessionData(reply.Session) {
		return errWebAuthResponse
	}
	account := firstLocalAccount(reply.Email, identity.Email, identity.Username)
	for _, raw := range []json.RawMessage{reply.User, identity.User} {
		var user struct {
			Email    string `json:"email"`
			Username string `json:"username"`
		}
		if json.Unmarshal(raw, &user) == nil {
			account = firstLocalAccount(user.Email, user.Username, account)
		}
	}
	secrets := []string{session.Access, session.Refresh, session.Password}
	for _, cookie := range session.Cookies {
		secrets = append(secrets, cookie.Value)
	}
	if account = safeLocalAuthText(account, secrets...); account != "" {
		session.Account = account
	}
	now := s.now().UTC()
	for _, hint := range []*time.Time{reply.expiry(now), parseWebExpiry(identity.Expiry)} {
		if hint != nil {
			session.ExpiresAt = earliestPanelExpiry(session.ExpiresAt, *hint)
		}
	}
	if session.expired(now) {
		return upstreamHTTPStatus(http.StatusUnauthorized)
	}
	session.LastVerifiedAt = &now
	session.NeedsVerification = false
	return nil
}

func firstLocalAccount(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Service) receiveLocalTokens(ctx context.Context, method string, session *localPanelLogin, fields map[string]string) error {
	reply, err := s.requestLocalAuth(ctx, http.MethodPost, method, session, fields)
	if err != nil {
		return err
	}
	if !validPanelBearer(reply.Access) || reply.Refresh != "" && !validPanelBearer(reply.Refresh) ||
		reply.Type != "" && !strings.EqualFold(reply.Type, "Bearer") || reply.Lifetime < 0 {
		return errWebAuthResponse
	}
	session.Access = reply.Access
	if reply.Refresh != "" {
		session.Refresh = reply.Refresh
	}
	now := s.now().UTC()
	session.ExpiresAt = reply.expiry(now)
	if hint := webBearerExpiry(session.Access); hint != nil {
		session.ExpiresAt = earliestPanelExpiry(session.ExpiresAt, *hint)
	}
	if session.expired(now) {
		return errExpiredWebSession
	}
	session.NeedsVerification = true
	return nil
}

type localAuthCookie struct {
	Name    string    `json:"name"`
	Value   string    `json:"value"`
	Path    string    `json:"path"`
	Expires time.Time `json:"expires,omitempty"`
}

const localAuthPath = "/api/v1/auth/"

func (c localAuthCookie) appliesTo(path string) bool {
	return path == c.Path || strings.HasPrefix(path, strings.TrimSuffix(c.Path, "/")+"/")
}

func (session *localPanelLogin) authCookies(path string, now time.Time) []*http.Cookie {
	var cookies []*http.Cookie
	for _, cookie := range session.Cookies {
		if cookie.appliesTo(localAuthPath+path) && (cookie.Expires.IsZero() || cookie.Expires.After(now)) {
			cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	return cookies
}

func (session *localPanelLogin) updateAuthCookies(received []*http.Cookie, now time.Time) {
	for _, next := range received {
		domain := strings.TrimPrefix(strings.ToLower(next.Domain), ".")
		if domain != "" && domain != "beichenapi.com" {
			continue
		}
		path := next.Path
		if !strings.HasPrefix(path, "/") {
			path = strings.TrimSuffix(localAuthPath, "/")
		}
		cookie := localAuthCookie{Name: next.Name, Value: next.Value, Path: path, Expires: next.Expires}
		if !cookie.appliesTo(localAuthPath+"refresh") && !cookie.appliesTo(localAuthPath+"session") {
			continue
		}
		if next.MaxAge > 0 {
			const tenYears = 10 * 366 * 24 * 3600
			cookie.Expires = now.Add(time.Duration(min(next.MaxAge, tenYears)) * time.Second)
		}
		for i := 0; i < len(session.Cookies); i++ {
			old := session.Cookies[i]
			if old.Name == cookie.Name && old.Path == cookie.Path || !old.Expires.IsZero() && !old.Expires.After(now) {
				session.Cookies = append(session.Cookies[:i], session.Cookies[i+1:]...)
				i--
			}
		}
		if next.MaxAge >= 0 && (cookie.Expires.IsZero() || cookie.Expires.After(now)) &&
			next.Value != "" && len(next.Value) <= 4096 && len(session.Cookies) < 32 {
			session.Cookies = append(session.Cookies, cookie)
		}
	}
	sort.SliceStable(session.Cookies, func(i, j int) bool { return len(session.Cookies[i].Path) > len(session.Cookies[j].Path) })
}
