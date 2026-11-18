package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

func TestBeichenMigrationUpgrade(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL URL")
	}
	if config.Host != "127.0.0.1" || config.Database != "nebula_test" || len(config.Fallbacks) != 0 {
		t.Fatal("migration test requires a loopback-only nebula_test database with sslmode=disable")
	}
	conn, err := sql.Open(DriverName, dsn)
	if err != nil {
		t.Fatal("could not open test database")
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	empty, err := databaseIsEmpty(ctx, conn)
	if err != nil || !empty {
		t.Fatalf("expected a new empty test database, empty=%v, err=%v", empty, err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE schema_migrations(version varchar(128) PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name >= "0029" {
			continue
		}
		migration, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply baseline %s: %v", name, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", name); err != nil {
			t.Fatal(err)
		}
	}

	const callID = "9f2c01a6-a2c0-4b8c-9008-de2e90bfb8bb"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO gateway_calls(id, request_id, month, organization_id, project_id, user_id,
  api_key_id, model_id, organization_name, project_name, user_name, api_key_name,
  model_name, protocol, request_path, multiplier_milli, credit_milli, billing_state, created_at)
VALUES ($1, 'legacy-call', now(), $1, $1, $1, $1, $1,
  'org', 'project', 'user', 'key', 'model', 'openai', '/v1/chat/completions', 1000, 1000, 'charged', now())`, callID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatalf("apply upgrade %d: %v", i+1, err)
		}
	}
	var credits int64
	var cacheHits sql.NullInt64
	var credential sql.NullString
	if err := conn.QueryRowContext(ctx, `
SELECT credit_milli, cache_hit_tokens, provider_credential_id
FROM gateway_calls WHERE id=$1`, callID).Scan(&credits, &cacheHits, &credential); err != nil {
		t.Fatal(err)
	}
	if credits != 1000 || cacheHits.Valid || credential.Valid {
		t.Fatalf("old call changed: credit=%d cache=%v credential=%v", credits, cacheHits, credential)
	}

	providerID, credentialID := uuid.New(), uuid.New()
	if _, err := conn.ExecContext(ctx, `
INSERT INTO providers(id, name, base_url, created_at, updated_at)
VALUES($1, 'North test', 'https://api.beichenapi.com', now(), now())`, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO provider_credentials(id, provider_id, label, credential_ciphertext, created_at, updated_at)
VALUES($1, $2, 'test', decode('01', 'hex'), now(), now())`, credentialID, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO beichen_price_versions(credential_id, model, track, basis, source, effective_at, input_rate, output_rate, cache_read_rate)
VALUES($1, 'model', 'upstream', 'effective', 'billing', '2026-11-18 00:00:00+08', 2000000, 3000000, 500000)`, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE gateway_calls SET provider_id=$2, provider_credential_id=$3, binding_id=$4, upstream_model_name='model',
  protocol='openai_compatible', outcome='succeeded', usage_state='reported', prompt_tokens=12000,
  completion_tokens=1000, total_tokens=13000, cache_hit_tokens=2000,
  created_at='2026-11-17 23:59:59+08', sent_at='2026-11-18 00:00:01+08'
WHERE id=$1`, callID, providerID, credentialID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO beichen_daily_usage(provider_id, credential_id, day, model, requests, input_tokens,
   output_tokens, cache_read_tokens, billed_upstream_micros, source)
VALUES($1, $2, '2026-11-18', 'model', 1, 10000, 1000, 2000, 24000, 'usage')`, providerID, credentialID); err != nil {
		t.Fatal(err)
	}

	overview, err := beichen.New(conn, nil).Overview(ctx, providerID, "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Comparison) != 1 {
		t.Fatalf("comparison rows = %d, want 1", len(overview.Comparison))
	}
	row := overview.Comparison[0]
	for _, want := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"local requests", row.LocalRequests, 1},
		{"upstream requests", row.UpstreamRequests, 1},
		{"uncached input tokens", row.LocalInputTokens, 10000},
		{"cached input tokens", row.LocalCacheReadTokens, 2000},
		{"upstream estimate", row.EstimatedUpstreamMicros, 24000},
		{"upstream billed", row.BilledUpstreamMicros, 24000},
		{"trial credits", row.TrialCreditMilli, 24},
		{"unpriced calls", row.TrialUnpricedCalls, 0},
	} {
		if want.got == nil {
			t.Errorf("%s is missing, want %d", want.name, want.want)
		} else if *want.got != want.want {
			t.Errorf("%s = %d, want %d", want.name, *want.got, want.want)
		}
	}
}
