package db

import "testing"

func TestUnresolvedExposureMigration0069Postgres(t *testing.T) {
	ctx, conn := openIsolatedMigrationSchema(t)
	applyMigrationTestBaseline(t, ctx, conn, "0069")
	_, err := conn.ExecContext(ctx, `INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,
		api_key_id,model_id,organization_name,project_name,user_name,api_key_name,model_name,protocol,
		request_path,multiplier_milli,credit_milli,billing_state,beichen_state,beichen_review_reason,created_at)
		SELECT gen_random_uuid(),state,now(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
		gen_random_uuid(),gen_random_uuid(),'org','project','student','key','model','openai_responses',
		'/v1/responses',1000,0,'failed',state,CASE WHEN state='pending_review' THEN 'unconfirmed_dispatch' END,now()
		FROM (VALUES ('pending_review'),('released')) states(state)`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var pending, released int
	err = conn.QueryRowContext(ctx, `SELECT
		(SELECT unresolved_credit_milli FROM gateway_calls WHERE request_id='pending_review'),
		(SELECT unresolved_credit_milli FROM gateway_calls WHERE request_id='released')`).Scan(&pending, &released)
	if err != nil || pending != 10 || released != 0 {
		t.Fatalf("after migration: pending=%d released=%d err=%v", pending, released, err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE gateway_calls SET unresolved_credit_milli=-1"); err == nil {
		t.Fatal("migration accepted negative exposure")
	}
}
