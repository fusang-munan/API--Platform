package catalog

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestFindProbeCatalogCredentialRequiresBeichenKeyEvidence(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	defer db.Close()
	providerID := uuid.New()
	owner := uuid.New()
	find := func(model string) (*ent.ProviderCredential, error) {
		return FindProbeCatalogCredential(context.Background(), db, providerID, "https://api.beichenapi.com/v1", model)
	}
	const query = `FROM "provider_credentials".*"provider_model_catalog".*"model_id" ILIKE.*"catalog_source" =.*ORDER BY "provider_credentials"."id"`
	mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(owner))
	picked, err := find("models/gemini-pro")
	if err != nil || picked == nil || picked.ID != owner {
		t.Fatalf("did not pick the catalog owner: %v", err)
	}
	mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if picked, err := find("gemini-unlisted"); err != nil || picked != nil {
		t.Fatalf("unlisted model got a credential or error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
