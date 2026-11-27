package beichen

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestCapturedWebSessionGenerationOnRefresh(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := New(nil, nil)
	s.now = func() time.Time { return now }
	const access = "fixture-reused-access"
	const refresh = "fixture-initial-refresh"
	if err := s.ConfigureWebSessionTokens("capture@school.example", access, refresh, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	saveErr := errors.New("fixture rotation write failed")
	var saves, refreshes int
	s.webAccount.save = func(_, _ string, _ time.Time) error {
		saves++
		if saves == 1 {
			return saveErr
		}
		return nil
	}
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		var fields map[string]string
		if r.URL.String() != webAuthURL+"refresh" || json.NewDecoder(r.Body).Decode(&fields) != nil {
			t.Fatal("unexpected refresh request")
		}
		refreshes++
		if refreshes <= 2 && fields["refresh_token"] != refresh ||
			refreshes == 3 && fields["refresh_token"] != "fixture-rotated-refresh" {
			t.Fatal("refresh did not use the current credential")
		}
		switch refreshes {
		case 1:
			return scanResponse(http.StatusServiceUnavailable, ""), nil
		case 2:
			return scanResponse(http.StatusOK, `{"code":0,"data":{"access_token":"fixture-reused-access","refresh_token":"fixture-rotated-refresh","expires_in":3600}}`), nil
		case 3:
			return scanResponse(http.StatusOK, `{"code":0,"data":{"access_token":"fixture-next-access","expires_in":3600}}`), nil
		default:
			return nil, errors.New("unexpected additional refresh")
		}
	})
	_, initial, err := s.webPanelAccess(ctx, false)
	if err != nil || initial == 0 {
		t.Fatalf("initial version missing: %v", err)
	}
	s.rejectCapturedWebSession(access, initial)
	for i := 0; i < 2; i++ {
		if _, _, err := s.webPanelAccess(ctx, false); !errors.Is(err, upstreamHTTPStatus(http.StatusServiceUnavailable)) {
			t.Fatalf("refresh failure was not retained: %v", err)
		}
	}
	if refreshes != 1 || s.webAccount.generation != initial {
		t.Fatal("failed or backed-off refresh changed the session version")
	}
	now = now.Add(3 * time.Minute)
	if _, _, err := s.webPanelAccess(ctx, false); !errors.Is(err, saveErr) || s.webAccount.generation != initial+1 {
		t.Fatalf("rotation was not installed exactly once before retrying persistence: %v", err)
	}
	bearer, generation, err := s.webPanelAccess(ctx, false)
	if err != nil || bearer != access || generation != initial+1 || refreshes != 2 || saves != 2 {
		t.Fatalf("retrying the rotation write changed the version or refreshed again: %v", err)
	}
	s.rejectCapturedWebSession(bearer, generation)
	bearer, generation, err = s.webPanelAccess(ctx, false)
	if err != nil || bearer != "fixture-next-access" || generation != initial+2 || refreshes != 3 || saves != 3 {
		t.Fatalf("another successful access renewal did not advance the version once: %v", err)
	}
}

func TestCapturedWebSessionConcurrentKeyUnauthorized(t *testing.T) {
	s := New(nil, nil)
	configureReviewCapture(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var reads, refreshes atomic.Int32
	started := make(chan struct{}, 2)
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshes.Add(1)
			body, err := json.Marshal(map[string]any{"code": 0, "data": webTokens{
				Access: reviewCapture, Refresh: "fixture-refresh-only-rotation", Lifetime: 3600,
			}})
			return scanResponse(http.StatusOK, string(body)), err
		case "/api/v1/keys":
			read := reads.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+reviewCapture {
				return nil, errors.New("expected the same access credential after refresh-only rotation")
			}
			if read <= 2 {
				release := releaseFirst
				if read == 2 {
					release = releaseSecond
				}
				started <- struct{}{}
				select {
				case <-release:
					return scanResponse(http.StatusUnauthorized, ""), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return scanResponse(http.StatusOK, `{"code":0,"data":{"items":[{"id":51,"user_id":7}],"total":1,"page":1,"page_size":100,"pages":1}}`), nil
		default:
			return nil, errors.New("unexpected Key read request")
		}
	})
	read := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			var version []byte
			readCtx := context.WithValue(ctx, capturedWebVersionKey{}, &version)
			observation, err := s.readWebsiteKeys(readCtx, uuid.Nil)
			if err == nil && (len(observation.keys) != 1 || observation.keys[0].ID != 51 ||
				len(version) != capturedWebVersionSize || binary.BigEndian.Uint64(version) != 2) {
				err = errors.New("successful Key retry did not capture the shared renewed version")
			}
			done <- err
		}()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("Key read did not reach upstream")
		}
		return done
	}
	first, second := read(), read()
	for i, done := range []<-chan error{first, second} {
		if i == 0 {
			close(releaseFirst)
		} else {
			close(releaseSecond)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("Key retry did not finish")
		}
	}
	if _, err := s.webPanelToken(ctx); err != nil || reads.Load() != 4 || refreshes.Load() != 1 {
		t.Fatalf("late 401 invalidated the renewed session: Key reads=%d refreshes=%d err=%v", reads.Load(), refreshes.Load(), err)
	}
}
