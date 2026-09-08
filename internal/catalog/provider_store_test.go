package catalog

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestProviderCatalogUpsertStoresExtendedFields(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	providerID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	credentialID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	syncedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	entries := []ProviderEntry{{
		ID:          "gpt-5.3-codex",
		Object:      "model",
		DisplayName: "GPT 5.3 Codex",
		CreatedAt:   "2024-01-01T00:00:00Z",
		OwnedBy:     "openai",
		Description: "coding model",
		Raw: map[string]any{
			"id":           "gpt-5.3-codex",
			"type":         "model",
			"display_name": "GPT 5.3 Codex",
		},
	}}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE provider_model_catalog SET status='inactive',synced_at=$1,updated_at=$1 WHERE credential_id=$2`)).
		WithArgs(syncedAt, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO provider_model_catalog`).
		WithArgs(sqlmock.AnyArg(), providerID, credentialID, "gpt-5.3-codex", "GPT 5.3 Codex", "coding model", "openai", "model", "2024-01-01T00:00:00Z", sqlmock.AnyArg(), syncedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count, err := (&ProviderCatalogSyncer{DB: database}).UpsertCredentialEntries(context.Background(), providerID, credentialID, entries, syncedAt)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 upsert, got %d", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
