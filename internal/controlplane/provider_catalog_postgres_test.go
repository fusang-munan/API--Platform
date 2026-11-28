package controlplane

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	postgres "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/testutil"
)

func newCatalogMaterializationService(t *testing.T) (context.Context, *Service) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES opts in to catalog materialization tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || !testutil.IsIsolatedPostgresDatabase(cfg.Database) ||
		cfg.User != "nebula_qa" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires a whitelisted loopback test database as nebula_qa with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	schema := pgx.Identifier{"beichen_materialize_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { database.Close() })
	if err := postgres.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	return ctx, &Service{db: ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, database))), sqlDB: database}
}

func TestScheduledBeichenMaterializationKeepsPublishedBindingsPostgres(t *testing.T) {
	ctx, service := newCatalogMaterializationService(t)
	database := service.sqlDB
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var provider uuid.UUID
	if err := database.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	chosenKey, addedKey := uuid.New(), uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$3,'A teacher route','fixture',now(),now()),($2,$3,'Z added route','fixture',now(),now())`, chosenKey, addedKey, provider)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES(gen_random_uuid(),'shared','teacher label','active',1000,now(),now()),
      (gen_random_uuid(),'missing','teacher label','active',1000,now(),now()),
      (gen_random_uuid(),'paused','teacher label','inactive',1000,now(),now())`)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,model_id::text,'openai_responses',CASE WHEN model_id='paused' THEN 'inactive' ELSE 'active' END,73,now(),now()
FROM models WHERE model_id IN ('shared','missing','paused')`, provider, chosenKey)
	store := catalog.ProviderCatalogSyncer{DB: database}
	shared := catalog.ProviderEntry{ID: "shared", DisplayName: "other label", SupportedProtocols: []string{"openai_responses"}}
	for range 2 {
		if _, err := store.UpsertCredentialEntries(ctx, provider, chosenKey, []catalog.ProviderEntry{
			shared, {ID: "paused", SupportedProtocols: []string{"openai_responses"}},
		}, time.Now().UTC(), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertCredentialEntries(ctx, provider, addedKey, []catalog.ProviderEntry{
			shared, {ID: "new-candidate", SupportedProtocols: []string{"openai_responses"}},
		}, time.Now().UTC(), nil); err != nil {
			t.Fatal(err)
		}
		if err := service.MaterializeBeichenCatalogs(ctx); err != nil {
			t.Fatal(err)
		}
		var kept bool
		err := database.QueryRowContext(ctx, `SELECT
  (SELECT count(*)=3 FROM model_bindings WHERE provider_id=$1)
  AND (SELECT count(*)=3 FROM model_bindings b JOIN models m ON m.id=b.model_id
    WHERE b.provider_id=$1 AND b.credential_id=$2 AND b.adapter='openai_responses'
      AND b.priority=73 AND m.display_name='teacher label'
      AND b.status=CASE WHEN m.model_id='paused' THEN 'inactive' ELSE 'active' END)
  AND EXISTS (SELECT 1 FROM models WHERE model_id='new-candidate' AND status='pending_configuration')`,
			provider, chosenKey).Scan(&kept)
		if err != nil || !kept {
			t.Fatalf("scheduled catalog refresh changed teacher-selected lines or published a candidate: %v", err)
		}
	}
	if _, err := service.ResetProviderCatalog(ctx, provider); domain.ErrorCode(err) != domain.CodeInvalidTransition {
		t.Fatalf("legacy reset can bypass explicit Beichen route changes: %v", err)
	}
}

func TestGeminiCatalogMaterializationPreservesChatAndResponsesPostgres(t *testing.T) {
	ctx, service := newCatalogMaterializationService(t)
	database := service.sqlDB
	var provider uuid.UUID
	if err := database.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	credential := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'Gemini fixture','fixture',now(),now())`, credential, provider); err != nil {
		t.Fatal(err)
	}

	const model = "gemini-materialization"
	store := catalog.ProviderCatalogSyncer{DB: database}
	writeCatalog := func(protocols []string) {
		t.Helper()
		entries := []catalog.ProviderEntry{{ID: model, SupportedProtocols: protocols}}
		if _, err := store.UpsertCredentialEntries(ctx, provider, credential, entries, time.Now().UTC(), nil); err != nil {
			t.Fatal(err)
		}
	}
	checkBindings := func(wantNative int) {
		t.Helper()
		var compatibility, native int
		err := database.QueryRowContext(ctx, `SELECT
  count(*) FILTER (WHERE adapter IN ('openai_compatible','openai_responses')),
  count(*) FILTER (WHERE adapter='google_gemini_v1beta')
FROM model_bindings
WHERE provider_id=$1 AND credential_id=$2 AND upstream_model_name=$3 AND status='active'`,
			provider, credential, model).Scan(&compatibility, &native)
		if err != nil || compatibility != 2 || native != wantNative {
			t.Fatalf("Gemini bindings: Chat/Responses=%d, native=%d want %d, error=%v", compatibility, native, wantNative, err)
		}
	}
	writeCatalog(nil)
	if err := service.materializeStoredProviderCatalog(ctx, provider, false, true); err != nil {
		t.Fatal(err)
	}
	checkBindings(0)
	if _, err := database.ExecContext(ctx, "UPDATE models SET status='active' WHERE model_id=$1", model); err != nil {
		t.Fatal(err)
	}

	// A stored declaration makes native eligible without publishing it during
	// the scheduled candidate refresh or deactivating compatibility bindings.
	writeCatalog([]string{"google_gemini_v1beta"})
	if err := service.MaterializeBeichenCatalogs(ctx); err != nil {
		t.Fatal(err)
	}
	checkBindings(0)
	if err := service.materializeStoredProviderCatalog(ctx, provider, false, true); err != nil {
		t.Fatal(err)
	}
	checkBindings(1)
}
