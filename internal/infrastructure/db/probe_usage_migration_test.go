package db

import "testing"

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
	var jobs, rows, indexes, versions int
	err := conn.QueryRowContext(ctx, `SELECT (SELECT count(job_id) FROM beichen_probe_jobs),
		(SELECT count(*) FROM beichen_probe_usage),
		(SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='beichen_probe_usage'
		 AND (indexdef LIKE '%(provider_id, checked_at)' OR indexdef LIKE '%(credential_id, checked_at)')),
		(SELECT count(*) FROM schema_migrations WHERE version='0066_beichen_probe_usage.sql')`).Scan(&jobs, &rows, &indexes, &versions)
	if err != nil || jobs != 1 || rows != 0 || indexes != 2 || versions != 1 {
		t.Fatalf("jobs=%d rows=%d indexes=%d versions=%d err=%v", jobs, rows, indexes, versions, err)
	}
}
