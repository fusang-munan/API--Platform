package schoolseal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleSession() Session {
	return Session{
		WebEmail:     "school@example.test",
		WebAccess:    "access-token-fixture",
		WebRefresh:   "refresh-token-fixture",
		WebExpiresAt: time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func TestSessionReleaseIsSeparateAndEncrypted(t *testing.T) {
	id, err := LoadOrCreate(filepath.Join(t.TempDir(), "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	session := sampleSession()
	sealed, err := SealSession(id.Recipient(), session)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{session.WebEmail, session.WebAccess, session.WebRefresh} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatal("cleartext session token in release")
		}
	}
	original := sessionRelease
	t.Cleanup(func() { sessionRelease = original })
	sessionRelease = sealed
	if !HasSessionRelease() {
		t.Fatal("session release not detected")
	}
	got, err := id.OpenSessionRelease()
	if err != nil || got == nil || got.WebAccess != session.WebAccess || got.WebRefresh != session.WebRefresh ||
		!got.WebExpiresAt.Equal(session.WebExpiresAt) {
		t.Fatal("session release did not round-trip")
	}
	other, err := LoadOrCreate(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.OpenSessionRelease(); err == nil {
		t.Fatal("wrong school accepted session release")
	}
	var e envelope
	if err := json.Unmarshal(sealed, &e); err != nil {
		t.Fatal(err)
	}
	cipher, _ := base64.StdEncoding.DecodeString(e.Ciphertext)
	cipher[len(cipher)-1] ^= 1
	e.Ciphertext = base64.StdEncoding.EncodeToString(cipher)
	bad, _ := json.Marshal(e)
	sessionRelease = bad
	if _, err := id.OpenSessionRelease(); err == nil {
		t.Fatal("tampered session release accepted")
	}
}

func TestSessionStoreFallsBackWhenRenewalCacheInvalid(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreate(filepath.Join(dir, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	session := sampleSession()
	store, err := id.SessionStore(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte("corrupt-renewal-cache"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.WebAccess != session.WebAccess || got.WebRefresh != session.WebRefresh {
		t.Fatalf("fallback failed: %+v, %v", got, err)
	}
	if len(SessionReleaseDigest()) != 64 {
		t.Fatalf("unexpected embedded session digest: %q", SessionReleaseDigest())
	}
}

func TestSessionRejectsMissingOrUnsafeTokens(t *testing.T) {
	for _, change := range []func(*Session){
		func(s *Session) { s.WebEmail = "not-an-email" },
		func(s *Session) { s.WebAccess = "" },
		func(s *Session) { s.WebRefresh = "sk-not-a-web-session" },
		func(s *Session) { s.WebAccess = "has space" },
		func(s *Session) { s.WebExpiresAt = time.Time{} },
	} {
		session := sampleSession()
		change(&session)
		if session.Validate() == nil {
			t.Fatal("unsafe session accepted")
		}
	}
}

func TestEmptySessionRelease(t *testing.T) {
	id, err := LoadOrCreate(filepath.Join(t.TempDir(), "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	original := sessionRelease
	t.Cleanup(func() { sessionRelease = original })
	for _, value := range []string{"", "null\n", "  null  "} {
		sessionRelease = []byte(value)
		got, err := id.OpenSessionRelease()
		if err != nil || got != nil || HasSessionRelease() {
			t.Fatal("empty session release must preserve old configuration")
		}
	}
}
