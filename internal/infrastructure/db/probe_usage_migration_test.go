package db

import (
	"strings"
	"testing"
)

func TestProbeUsageMigration0066Postgres(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0066")
	if _, err := conn.ExecContext(ctx, `INSERT INTO beichen_probe_jobs(provider_id) SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var jobs, rows, versions int
	var providerIndex, credentialIndex string
	err := conn.QueryRowContext(ctx, `SELECT (SELECT count(job_id) FROM beichen_probe_jobs),
		(SELECT count(*) FROM beichen_probe_usage),
		(SELECT count(*) FROM schema_migrations WHERE version='0066_beichen_probe_usage.sql'),
		pg_get_indexdef('beichen_probe_usage_provider_time'::regclass),
		pg_get_indexdef('beichen_probe_usage_credential_time'::regclass)`).Scan(
		&jobs, &rows, &versions, &providerIndex, &credentialIndex)
	if err != nil || jobs != 1 || rows != 0 || versions != 1 ||
		!strings.Contains(providerIndex, "(provider_id, checked_at)") ||
		!strings.Contains(credentialIndex, "(credential_id, checked_at)") {
		t.Fatalf("jobs=%d rows=%d versions=%d providerIndex=%q credentialIndex=%q err=%v",
			jobs, rows, versions, providerIndex, credentialIndex, err)
	}
}
