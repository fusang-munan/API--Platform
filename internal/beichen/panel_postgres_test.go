package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
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
	for _, name := range []string{"0030_beichen.sql", "0033_beichen_panel_prices.sql", "0040_beichen_panel_price_conditions.sql", "0042_beichen_price_credential_identity.sql"} {
		migration, err := os.ReadFile("../infrastructure/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(migration))
	}
	id := uuid.New()
	revision, err := priceTimestamp(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES ($1,$2,'fixture')", id, revision)
	rate := int64(10)
	info := billingInfo{Multiplier: json.Number("0.125")}
	quote := panelQuote{
		rows:      []priceRow{{Model: "shared", Rates: Rates{Input: &rate, Output: &rate}}},
		expiresAt: revision.Add(time.Minute),
	}
	if err := saveBillingObservation(ctx, tx, id, revision, info, nil, 1, quote); err != nil {
		t.Fatal(err)
	}
	effectiveAt, err := priceTimestamp(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	check := func(model string, at time.Time, expected string) {
		t.Helper()
		var rate sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT trim_scale(upstream_input)::text FROM beichen_price_at($1,$2,$3)`,
			id, model, at).Scan(&rate)
		if err != nil || rate.Valid != (expected != "") || rate.String != expected {
			t.Fatalf("lookup %s at %s: rate %v, error %v, want %q", model, at, rate, err, expected)
		}
	}
	check("shared", effectiveAt, "1.25")
	check("removed", effectiveAt, "")
	check("shared", quote.expiresAt, "")
	later := effectiveAt.Add(time.Second)
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET updated_at=$2 WHERE id=$1", id, later)
	check("shared", later, "1.25")
	later = later.Add(time.Second)
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET updated_at=$2,credential_ciphertext='different fixture' WHERE id=$1", id, later)
	check("shared", later, "")
	check("shared", effectiveAt, "1.25")
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET credential_ciphertext='fixture' WHERE id=$1", id)
	if err := invalidatePanelQuote(ctx, tx, id, later, later); err != nil {
		t.Fatal(err)
	}
	check("shared", effectiveAt, "1.25")
	check("shared", later, "")
}
