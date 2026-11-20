package controlplane

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

type catalogTestTransport func(*http.Request) (*http.Response, error)

func (f catalogTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBeichenRefreshStoresOnlyActualCatalog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seed := bytes.Repeat([]byte{3}, 32)
	manager, err := security.NewManager(seed, seed, seed, seed, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := manager.EncryptCredential("synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	provider := &ent.Provider{ID: uuid.New(), BaseURL: "https://API.BEICHENAPI.COM/v1/", Status: "active"}
	hint := "Claude"
	key := &ent.ProviderCredential{ID: uuid.New(), ProviderID: provider.ID, Label: "Gemini", GroupHint: &hint,
		Status: "active", CredentialCiphertext: ciphertext, UpdatedAt: time.Now().UTC()}
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	calls := 0
	http.DefaultTransport = catalogTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != "https://api.beichenapi.com/v1/models" ||
			req.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Fatal("unexpected catalog request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gemini-test"},{"id":"other-family"}]}`))}, nil
	})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT p.base_url FROM provider_credentials").WithArgs(key.ID, provider.ID, key.UpdatedAt).
		WillReturnRows(sqlmock.NewRows([]string{"base_url"}).AddRow(provider.BaseURL))
	mock.ExpectExec("UPDATE provider_model_catalog").WillReturnResult(sqlmock.NewResult(0, 0))
	for _, id := range []string{"gemini-test", "other-family"} {
		mock.ExpectExec("INSERT INTO provider_model_catalog").WithArgs(sqlmock.AnyArg(), provider.ID, key.ID, id,
			"", "", "", "", "", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	s := &Service{sqlDB: db, security: manager, now: time.Now}
	count, err := s.refreshCredentialCatalog(context.Background(), provider, key)
	if err != nil || count != 2 || calls != 1 {
		t.Fatalf("refresh got %d models, %d HTTP requests: %v", count, calls, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBeichenBaseURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.beichenapi.com":     "https://api.beichenapi.com",
		"https://API.BEICHENAPI.COM/v1/": "https://api.beichenapi.com",
		"https://other.example.test/v1":  "https://other.example.test/v1",
	} {
		got, err := ValidateBaseURL(input)
		if err != nil || got != want {
			t.Errorf("ValidateBaseURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"http://api.beichenapi.com",
		"https://api.beichenapi.com:8443",
		"https://api.beichenapi.com/v1/usage",
	} {
		if _, err := ValidateBaseURL(input); err == nil {
			t.Errorf("ValidateBaseURL(%q) accepted an unsupported address", input)
		}
	}
}
