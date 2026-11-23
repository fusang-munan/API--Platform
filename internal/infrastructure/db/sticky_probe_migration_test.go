package db

import (
	"testing"

	"github.com/google/uuid"
)

func TestStickyProbeMigrationBackfillsCurrentSuccessPostgres(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0064")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var provider uuid.UUID
	if err := conn.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'fixture','fixture',now(),timestamptz '2026-11-23 00:54:00+08')`, key, provider)
	exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
VALUES($1,$2,'current-success','openai_compatible',200,NULL,1,'2026-11-22 17:00:00+00'),
      ($1,$2,'failure','openai_compatible',503,'http_error',1,'2026-11-22 17:00:00+00'),
      ($1,$2,'invalid-body','openai_compatible',200,'invalid_response',1,'2026-11-22 17:00:00+00'),
      ($1,$2,'old-key-success','openai_compatible',200,NULL,1,'2026-11-22 16:30:00+00')`, provider, key)
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var total, exactBackfill, keepNull int
	err := conn.QueryRowContext(ctx, `SELECT count(*),
  count(*) FILTER (WHERE model='current-success' AND last_passed_at=checked_at),
  count(*) FILTER (WHERE model<>'current-success' AND last_passed_at IS NULL)
FROM upstream_probes WHERE credential_id=$1`, key).Scan(&total, &exactBackfill, &keepNull)
	if err != nil || total != 4 || exactBackfill != 1 || keepNull != 3 {
		t.Fatalf("migration changed probe history or backfilled the wrong rows: total=%d, backfilled=%d, untouched=%d (%v)",
			total, exactBackfill, keepNull, err)
	}
}
