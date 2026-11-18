package db

import (
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestSeededProviderWaitsForActiveCredential(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, conn)))
	service := controlplane.NewService(client, conn, nil, nil, nil, controlplane.Config{})
	checkEmpty := func() {
		t.Helper()
		models, err := service.TeacherModels(ctx)
		if err != nil || len(models) != 0 {
			t.Fatalf("model list with no usable credential: %v, %v", models, err)
		}
		if _, err := service.SyncActiveProviderCatalog(ctx); domain.ErrorCode(err) != domain.CodeNotFound {
			t.Fatalf("unexpected catalog sync with no usable credential: %v", err)
		}
	}
	checkEmpty()

	if _, err := conn.ExecContext(ctx, `INSERT INTO provider_credentials
		(id, provider_id, label, credential_ciphertext, status, created_at, updated_at)
		SELECT gen_random_uuid(), id, 'fixture', decode('01','hex'), 'inactive', now(), now()
		FROM providers WHERE name='北辰'`); err != nil {
		t.Fatal(err)
	}
	checkEmpty()
	if _, err := conn.ExecContext(ctx, "UPDATE provider_credentials SET status='active' WHERE label='fixture'"); err != nil {
		t.Fatal(err)
	}
	p, err := service.ActiveProvider(ctx)
	if err != nil || p.Name != "北辰" {
		t.Fatalf("expected configured provider to be selectable, got %v, %v", p, err)
	}
}
