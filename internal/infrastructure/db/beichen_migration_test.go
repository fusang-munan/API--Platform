package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

func TestBeichenMigrationUpgrade(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0029")

	const callID = "9f2c01a6-a2c0-4b8c-9008-de2e90bfb8bb"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO gateway_calls(id, request_id, month, organization_id, project_id, user_id,
  api_key_id, model_id, organization_name, project_name, user_name, api_key_name,
  model_name, protocol, request_path, multiplier_milli, credit_milli, billing_state, created_at)
VALUES ($1, 'legacy-call', now(), $1, $1, $1, $1, $1,
  'org', 'project', 'user', 'key', 'model', 'openai', '/v1/chat/completions', 1000, 1000, 'charged', now())`, callID); err != nil {
		t.Fatal(err)
	}
	verifyBeichenMigrationUpgrade(t, ctx, conn, callID)
}

func TestBeichenMigrationFrom0051PreservesPriceAndUsage(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0052")

	provider, credential := uuid.New(), uuid.New()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
VALUES($1,'Upgrade fixture','https://api.beichenapi.com',now(),now())`, provider)
	seed(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'fixture Key','fixture'::bytea,now(),'2026-11-11 00:00+08')`, credential, provider)
	seed(`
WITH multiplier AS (
 INSERT INTO beichen_multiplier_versions(credential_id,multiplier,effective_at)
 VALUES($1,0.3,'2026-11-12 00:00+08') RETURNING id,effective_at
)
INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version)
SELECT $1,effective_at,'2026-11-11 00:00+08',301,'2026-11-13 00:00+08',id FROM multiplier`, credential)
	seed(`
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,effective_at)
VALUES($1,'fixture-model','upstream','base','panel',1000,2000,'2026-11-12 00:00+08')`, credential)
	seed(`
INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,billed_upstream_micros,source)
VALUES($1,$2,'2026-11-12','public-history',1,42,'usage')`, provider, credential)

	for attempt := 1; attempt <= 2; attempt++ {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatalf("apply 0052–0055, attempt %d: %v", attempt, err)
		}
	}
	var migrations int
	var group, micros int64
	if err := conn.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM schema_migrations WHERE split_part(version,'_',1) BETWEEN '0052' AND '0055'),
       (SELECT group_id FROM beichen_price_versions WHERE credential_id=$1 AND model='fixture-model'),
       (SELECT billed_upstream_micros FROM beichen_daily_usage WHERE credential_id=$1 AND model='public-history')`,
		credential).Scan(&migrations, &group, &micros); err != nil || migrations != 4 || group != 301 || micros != 42 {
		t.Fatalf("migration records, pricing group or historical usage changed: %d, %d, %d (%v)", migrations, group, micros, err)
	}
}

func TestBeichenMigration0070AccountRateSource(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0070")
	credential := uuid.New()
	if _, err := conn.ExecContext(ctx, `
INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,group_rate,peak_enabled)
VALUES($1,0.6,16,0.6,true)`, credential); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var versions, applied int
	var preserved bool
	err := conn.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM schema_migrations WHERE left(version,4)='0070'),
       (SELECT count(*) FROM schema_migrations WHERE version='0070_beichen_account_rate_source.sql'),
       (SELECT multiplier=0.6 AND group_id=16 AND group_rate=0.6 AND peak_enabled IS TRUE
          AND account_rate_source IS NULL FROM beichen_multiplier_versions WHERE credential_id=$1)`,
		credential).Scan(&versions, &applied, &preserved)
	if err != nil || versions != 1 || applied != 1 || !preserved {
		t.Fatalf("unexpected migration history or legacy rates: %d %d %t (%v)", versions, applied, preserved, err)
	}

	insert := `
INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,group_rate,peak_enabled,account_rate_source)
VALUES($1,0.6,16,0.6,$2,$3)`
	peakOn, peakOff := true, false
	for _, test := range []struct {
		peak   *bool
		source string
	}{
		{&peakOn, "key_billing"},
		{nil, "key_billing"},
		{&peakOff, "unknown"},
	} {
		_, err := conn.ExecContext(ctx, insert, credential, test.peak, test.source)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("invalid source or peak flag accepted: %v", err)
		}
	}
	if _, err := conn.ExecContext(ctx, insert, credential, false, "key_billing"); err != nil {
		t.Fatalf("could not persist a non-peak Key billing source: %v", err)
	}
}

func TestBeichenMigration0071GroupRateSource(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0071")
	const insert = `
INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,group_rate,peak_enabled,account_rate_source)
VALUES($1,0.6,16,0.6,$2,$3)`
	previous := uuid.New()
	if _, err := conn.ExecContext(ctx, insert, previous, false, "key_billing"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var versions, applied, predecessor int
	var preserved bool
	err := conn.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM schema_migrations WHERE left(version,4)='0071'),
       (SELECT count(*) FROM schema_migrations WHERE version='0071_beichen_group_rate_source.sql'),
       (SELECT count(*) FROM schema_migrations WHERE version='0070_beichen_account_rate_source.sql'),
       (SELECT multiplier=0.6 AND group_id=16 AND group_rate=0.6 AND peak_enabled IS FALSE AND account_rate_source='key_billing'
        FROM beichen_multiplier_versions WHERE credential_id=$1)`, previous).Scan(&versions, &applied, &predecessor, &preserved)
	if err != nil || versions != 1 || applied != 1 || predecessor != 1 || !preserved {
		t.Fatalf("unexpected migration history or prior Key evidence: 0071=%d full=%d 0070=%d preserved=%t (%v)",
			versions, applied, predecessor, preserved, err)
	}

	for _, peak := range []bool{true, false} {
		if _, err := conn.ExecContext(ctx, insert, uuid.New(), peak, "group_rate"); err != nil {
			t.Fatalf("group_rate with peak_enabled=%t should be accepted: %v", peak, err)
		}
	}
	peakOn, peakOff := true, false
	for _, test := range []struct {
		name   string
		peak   *bool
		source string
	}{
		{name: "group rate with unknown peak", source: "group_rate"},
		{name: "Key billing with peak enabled", peak: &peakOn, source: "key_billing"},
		{name: "Key billing with unknown peak", source: "key_billing"},
		{name: "unknown source with peak disabled", peak: &peakOff, source: "unknown"},
		{name: "unknown source with unknown peak", source: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := conn.ExecContext(ctx, insert, uuid.New(), test.peak, test.source)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
				pgErr.ConstraintName != "beichen_multiplier_versions_rate_source_peak_check" {
				t.Fatalf("expected the new rate source check to reject the row: %v", err)
			}
		})
	}
}

func TestBeichenMigration0062ExpiresPre026Prices(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0062")

	provider, legacy, panel := uuid.New(), uuid.New(), uuid.New()
	const model = "pre-026-model"
	var historical time.Time
	if err := conn.QueryRowContext(ctx, "SELECT now() - interval '1 hour'").Scan(&historical); err != nil {
		t.Fatal(err)
	}
	older := historical.Add(-2 * time.Hour)
	ended := historical.Add(-time.Hour)
	unavailable := historical.Add(-30 * time.Minute)
	originalExpiry := historical.Add(2 * time.Hour)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
VALUES($1,'Price migration fixture','https://api.beichenapi.com',now(),now())`, provider)
	seed(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$3,'Legacy pricing','fixture'::bytea,$4,$4), ($2,$3,'Panel pricing','fixture'::bytea,$4,$4)`,
		legacy, panel, provider, older)
	seed(`INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier,group_id,stale_at)
VALUES($1,$3,0.2,NULL,$5), ($1,$4,0.15,NULL,NULL), ($2,$3,0.2,15,NULL), ($2,$4,0.15,15,$6)`,
		legacy, panel, older, historical, ended, originalExpiry)
	seed(`INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version)
SELECT $1,effective_at,$2,group_id,CASE WHEN effective_at=$2 THEN $3::timestamptz ELSE $4 END,id
FROM beichen_multiplier_versions WHERE credential_id=$1`, panel, older, ended, originalExpiry)
	seed(`INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision) VALUES($1,$2,$3)`,
		panel, unavailable, older)
	seed(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,effective_at,group_id)
VALUES($1,$3,'upstream','base','billing',1000,2000,$4,NULL), ($2,$3,'upstream','base','panel',1000,2000,$4,15)`,
		legacy, panel, model, historical)

	checkPrice := func(credential uuid.UUID, at time.Time, hasVersion, usable bool) {
		t.Helper()
		var priceVersion, multiplierVersion bool
		var input sql.NullString
		want := sql.NullString{String: "150", Valid: true}
		if !usable {
			want = sql.NullString{}
		}
		err := conn.QueryRowContext(ctx, `
SELECT upstream_version IS NOT NULL, multiplier_version IS NOT NULL, trim_scale(upstream_input)::text
FROM beichen_price_at($1,$2,$3)`, credential, model, at).Scan(&priceVersion, &multiplierVersion, &input)
		if err != nil || priceVersion != hasVersion || multiplierVersion != usable || input != want {
			t.Fatalf("price at %s for %s: price version=%t, multiplier version=%t, input=%+v, want %t/%t/%+v (%v)",
				at, credential, priceVersion, multiplierVersion, input, hasVersion, usable, want, err)
		}
	}
	checkPrice(legacy, historical.Add(time.Hour), true, true)
	checkPrice(panel, historical.Add(time.Hour), true, true)

	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var cutoff time.Time
	if err := conn.QueryRowContext(ctx, `
SELECT applied_at FROM schema_migrations WHERE version='0062_expire_pre_026_prices.sql'`).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	checkPrice(legacy, cutoff, true, false) // The legacy price row still exists, but its multiplier has expired.
	checkPrice(panel, cutoff, false, false)
	checkPrice(legacy, historical, true, true)
	checkPrice(panel, historical, true, true)

	var multiplierBoundaries, panelBoundaries bool
	err := conn.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM beichen_multiplier_versions
        WHERE credential_id IN ($1,$2) AND stale_at=$3)=3
       AND (SELECT stale_at FROM beichen_multiplier_versions WHERE credential_id=$1 AND effective_at=$4)=$5,
       (SELECT expires_at FROM beichen_panel_snapshots WHERE credential_id=$2 AND effective_at=$6)=$3
       AND (SELECT expires_at FROM beichen_panel_snapshots WHERE credential_id=$2 AND effective_at=$4)=$5
       AND (SELECT expires_at IS NULL FROM beichen_panel_snapshots WHERE credential_id=$2 AND effective_at=$7)`,
		legacy, panel, cutoff, older, ended, historical, unavailable).Scan(&multiplierBoundaries, &panelBoundaries)
	if err != nil || !multiplierBoundaries || !panelBoundaries {
		t.Fatalf("expiry boundaries after migration: multipliers=%t, panels=%t (%v)", multiplierBoundaries, panelBoundaries, err)
	}
}

func openMigrationTestDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, conn := openIsolatedMigrationSchema(t)
	empty, err := databaseIsEmpty(ctx, conn)
	if err != nil || !empty {
		t.Fatalf("expected a new empty test database, empty=%v, err=%v", empty, err)
	}
	return ctx, conn
}

func openIsolatedMigrationSchema(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL URL")
	}
	if config.Host != "127.0.0.1" || (config.Database != "nebula_test" && config.Database != "nebula_test_rev_b") ||
		len(config.Fallbacks) != 0 {
		t.Fatal("migration test requires a loopback-only nebula_test database with sslmode=disable")
	}
	conn, err := sql.Open(DriverName, dsn)
	if err != nil {
		t.Fatal("could not open test database")
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	schema := pgx.Identifier{"migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop isolated schema: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx, "SET search_path TO "+schema+", public"); err != nil {
		t.Fatal(err)
	}
	return ctx, conn
}

func applyMigrationTestBaseline(t *testing.T, ctx context.Context, conn *sql.DB, before string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "CREATE TABLE schema_migrations(version varchar(128) PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name >= before {
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
}

func verifyBeichenMigrationUpgrade(t *testing.T, ctx context.Context, conn *sql.DB, callID string) {
	t.Helper()
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
	actor := uuid.New()
	if _, err := conn.ExecContext(ctx, `INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'Migration teacher','migration@example.test','fixture','teacher',now(),now())`, actor); err != nil {
		t.Fatal(err)
	}
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
  completion_tokens=1000, total_tokens=13000, cache_hit_tokens=2000, cache_write_tokens=0,
  created_at='2026-11-17 23:59:59+08', sent_at='2026-11-18 00:00:01+08'
WHERE id=$1`, callID, providerID, credentialID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO beichen_daily_usage(provider_id, credential_id, day, model, requests, input_tokens,
   output_tokens, cache_read_tokens, billed_upstream_micros, source, actor_id)
VALUES($1, $2, '2026-11-18', 'model', 1, 10000, 1000, 2000, 24000, 'csv', $3),
      ($1, $2, '2026-11-18', 'old-public-model', 9, 90, 0, 0, 90000, 'usage', NULL)`, providerID, credentialID, actor); err != nil {
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
		{"trial credits (milli)", row.TrialCreditMilli, 2400},
		{"unpriced calls", row.TrialUnpricedCalls, 0},
	} {
		if want.got == nil {
			t.Errorf("%s is missing, want %d", want.name, want.want)
		} else if *want.got != want.want {
			t.Errorf("%s = %d, want %d", want.name, *want.got, want.want)
		}
	}
}

func TestIndependentReview0071Scope(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0071")
	var oldName, oldDefinition string
	err := conn.QueryRowContext(ctx, `SELECT c.conname, pg_get_constraintdef(c.oid) FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
		WHERE c.conrelid='beichen_multiplier_versions'::regclass AND c.contype='c'
		AND a.attname='account_rate_source'`).Scan(&oldName, &oldDefinition)
	if err != nil {
		t.Fatal("could not identify the original constraint")
	}
	_, err = conn.ExecContext(ctx, `ALTER TABLE beichen_multiplier_versions
		ADD CONSTRAINT qa_keep_group_id_for_tagged_rate CHECK (account_rate_source IS NULL OR group_id IS NOT NULL)`)
	if err != nil {
		t.Fatal("could not add the unrelated test constraint")
	}
	script, err := migrationFiles.ReadFile("migrations/0071_beichen_group_rate_source.sql")
	if err != nil {
		t.Fatal("could not open the migration")
	}
	for range 2 {
		if _, err := conn.ExecContext(ctx, string(script)); err != nil {
			t.Fatal("raw migration was not repeatable")
		}
	}
	var kept bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint
		WHERE conrelid='beichen_multiplier_versions'::regclass AND conname='qa_keep_group_id_for_tagged_rate')`).Scan(&kept)
	if err != nil {
		t.Fatal("could not check the unrelated test constraint")
	}
	t.Logf("0070 constraint name=%s; definition=%s; raw SQL ran twice; unrelated check retained=%t", oldName, oldDefinition, kept)
	if !kept {
		t.Fatal("0071 removed an unrelated check referencing account_rate_source")
	}
}
