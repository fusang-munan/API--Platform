package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/labtls"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

func TestRecipientRequiresPinnedSchoolIdentity(t *testing.T) {
	id, err := schoolseal.LoadOrCreate(filepath.Join(t.TempDir(), "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := labtls.LoadOrCreate(t.TempDir(), "https://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != schoolseal.PublicPath || r.Method != http.MethodGet {
			t.Error("unexpected request")
		}
		_ = json.NewEncoder(w).Encode(id.Recipient())
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()
	got, err := readRecipient(context.Background(), server.URL, server.Certificate().Raw)
	if err != nil || got != id.Recipient() {
		t.Fatalf("verified recipient not read: %v", err)
	}
	other, err := labtls.LoadOrCreate(t.TempDir(), "https://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readRecipient(context.Background(), server.URL, other.Certificate[0]); err == nil {
		t.Fatal("wrong trust anchor accepted")
	}
}

func TestCheckValidatesSchoolPublicKeyBeforeReadingInput(t *testing.T) {
	dir := t.TempDir()
	id, err := schoolseal.LoadOrCreate(filepath.Join(dir, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := labtls.LoadOrCreate(filepath.Join(dir, "tls"), "https://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "leaf.der")
	if err := os.WriteFile(certPath, cert.Certificate[0], 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "never-written.json")
	lowOrder := [32]byte{1}
	for _, tc := range []struct {
		name, key string
		valid     bool
	}{
		{"valid", id.Recipient().PublicKey, true}, {"bad base64", "not-base64", false},
		{"wrong length", base64.StdEncoding.EncodeToString(make([]byte, 31)), false},
		{"invalid X25519 public key", base64.StdEncoding.EncodeToString(make([]byte, 32)), false},
		{"nonzero low-order X25519 point", base64.StdEncoding.EncodeToString(lowOrder[:]), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(schoolseal.Recipient{Algorithm: schoolseal.Algorithm, PublicKey: tc.key})
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			server.StartTLS()
			defer server.Close()
			if err := run(server.URL, certPath, out, true, nil); (err == nil) != tc.valid {
				t.Fatal("incorrect preflight result", err)
			}
		})
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("preflight wrote an envelope")
	}
	if _, err := readRecipient(context.Background(), "http://127.0.0.1", cert.Certificate[0]); err == nil {
		t.Fatal("accepted plaintext transport")
	}
}
