package beichen

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPanelPostgresPriceLifecycle(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || config.Database != "nebula_test" ||
		config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("panel test requires a loopback-only nebula_test database with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := stdlib.OpenDB(*config)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("could not open disposable PostgreSQL")
	}
	defer tx.Rollback()
	schema := pgx.Identifier{"beichen_panel_test_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema)
	execTestSQL(t, ctx, tx, `
CREATE TABLE users (id uuid PRIMARY KEY);
CREATE TABLE provider_credentials (id uuid PRIMARY KEY, updated_at timestamptz NOT NULL, credential_ciphertext bytea NOT NULL);
CREATE TABLE gateway_calls (provider_id uuid, provider_credential_id uuid, upstream_model_name text, sent_at timestamptz)`)
	apply := func(name string) {
		t.Helper()
		migration, err := os.ReadFile("../infrastructure/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(migration))
	}
	for _, name := range []string{"0030_beichen.sql", "0033_beichen_panel_prices.sql", "0040_beichen_panel_price_conditions.sql"} {
		apply(name)
	}
	id, obsolete := uuid.New(), uuid.New()
	revision, err := priceTimestamp(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := revision.Add(time.Hour)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES ($1,$3,'fixture'),($2,$3,'obsolete fixture')", id, obsolete, revision)
	execTestSQL(t, ctx, tx, `
WITH rates AS (
  INSERT INTO beichen_multiplier_versions (credential_id,effective_at,multiplier)
  SELECT id,$3,0.125 FROM provider_credentials WHERE id IN ($1,$2)
  RETURNING credential_id,id
)
INSERT INTO beichen_panel_snapshots (credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing)
SELECT credential_id,$3,CASE WHEN credential_id=$1 THEN $3::timestamptz ELSE $3::timestamptz-interval '1 second' END,1,$4,id,
       '{"shared":{"input_price":0.00000000001,"output_price":0.00000000001}}'
FROM rates`, id, obsolete, revision, expiresAt)
	execTestSQL(t, ctx, tx, `
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,effective_at)
SELECT id,'shared','upstream','base','panel',10,10,$3 FROM provider_credentials WHERE id IN ($1,$2)`, id, obsolete, revision)
	check := func(credential uuid.UUID, model string, at time.Time, expected string) {
		t.Helper()
		var rate sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT trim_scale(upstream_input)::text FROM beichen_price_at($1,$2,$3)`,
			credential, model, at).Scan(&rate)
		if err != nil || rate.Valid != (expected != "") || rate.String != expected {
			t.Fatalf("lookup %s at %s: rate %v, error %v, want %q", model, at, rate, err, expected)
		}
	}
	check(id, "shared", revision, "1.25")
	check(obsolete, "shared", revision, "")
	apply("0042_beichen_price_credential_identity.sql")
	var good, bad bool
	if err := tx.QueryRowContext(ctx, `
SELECT (SELECT s.credential_ciphertext_sha256=sha256(c.credential_ciphertext)
        FROM beichen_panel_snapshots s JOIN provider_credentials c ON c.id=s.credential_id WHERE s.credential_id=$1),
       (SELECT credential_ciphertext_sha256 IS NULL FROM beichen_panel_snapshots WHERE credential_id=$2)`, id, obsolete).Scan(&good, &bad); err != nil || !good || !bad {
		t.Fatalf("0042 must backfill the active matching snapshot only: %v", err)
	}
	check(id, "shared", revision, "1.25")
	check(obsolete, "shared", revision, "")
	check(id, "removed", revision, "")
	check(id, "shared", expiresAt, "")
	later := revision.Add(time.Second)
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET updated_at=$2 WHERE id=$1", id, later)
	check(id, "shared", later, "1.25")
	later = later.Add(time.Second)
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET updated_at=$2,credential_ciphertext='different fixture' WHERE id=$1", id, later)
	check(id, "shared", later, "")
	check(id, "shared", revision, "1.25")
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET credential_ciphertext='fixture' WHERE id=$1", id)
	if err := invalidatePanelQuote(ctx, tx, id, later, later); err != nil {
		t.Fatal(err)
	}
	check(id, "shared", revision, "1.25")
	check(id, "shared", later, "")
	t.Log("0042 upgraded existing snapshots and preserved current and historical price validity")
}
