package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestImportedWebSessionUsesAccessUntilExpiry(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	s := New(nil, nil)
	s.now = func() time.Time { return now }
	if err := s.ConfigureWebSessionTokens("school@example.test", "access-fixture", "refresh-fixture", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network must not be used before expiry")
	})
	token, err := s.webPanelToken(context.Background())
	if err != nil || token != "access-fixture" || calls != 0 {
		t.Fatal("imported access token was not used", token, err, calls)
	}
	now = now.Add(2 * time.Hour)
	calls = 0
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != webAuthURL+"refresh" || r.Method != http.MethodPost {
			t.Fatal("unexpected session request")
		}
		var fields map[string]string
		if json.NewDecoder(r.Body).Decode(&fields) != nil || fields["refresh_token"] != "refresh-fixture" {
			t.Fatal("wrong refresh token")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"code":0,"data":{"access_token":"access-two","refresh_token":"refresh-two","token_type":"Bearer","expires_in":3600}}`))}, nil
	})
	token, err = s.webPanelToken(context.Background())
	if err != nil || token != "access-two" || calls != 1 {
		t.Fatal("imported refresh token was not used", token, err, calls)
	}
}

func TestImportedWebSessionNeedsReauthenticationWhenRefreshFails(t *testing.T) {
	now := time.Now()
	s := New(nil, nil)
	s.now = func() time.Time { return now }
	if err := s.ConfigureWebSessionTokens("school@example.test", "expired-access", "expired-refresh", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	requests := 0
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != webAuthURL+"refresh" {
			t.Fatal("token-only session must not attempt a password login")
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := s.webPanelToken(context.Background()); !errors.Is(err, errWebSignIn) {
		t.Fatalf("expected reauthentication error, got %v", err)
	}
	if requests != 1 || !s.webAccount.humanVerification.Load() {
		t.Fatal("failed refresh did not request human verification")
	}
}
