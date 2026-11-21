package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestStudentKeyModelRemovalMigrationPostgres(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0060")

	student, org, project, key, model, audit := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Student','s-upgrade@example.test','fixture','student',now(),now())`, student)
	seed(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	seed(`INSERT INTO projects(id,organization_id,name,created_at,updated_at)
		VALUES($1,$2,'Course',now(),now())`, project, org)
	seed(`INSERT INTO api_keys(id,owner_user_id,project_id,name,created_at,updated_at)
		VALUES($1,$2,$3,'Old key',now(),now())`, key, student, project)
	seed(`INSERT INTO models(id,model_id,display_name,status,created_at,updated_at)
		VALUES($1,'old-model','Old model','inactive',now(),now())`, model)
	seed(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, key, model)
	seed(`INSERT INTO api_key_audits(id,api_key_id,actor_user_id,action,comment,created_at)
		VALUES($1,$2,$3,'student_model_added','old-model',now())`, audit, key, student)

	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var linkPreserved, auditPreserved bool
	if err := conn.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM api_key_models WHERE api_key_id=$1 AND model_id=$2),
		EXISTS(SELECT 1 FROM api_key_audits WHERE id=$3 AND action='student_model_added' AND comment='old-model')`,
		key, model, audit).Scan(&linkPreserved, &auditPreserved); err != nil || !linkPreserved || !auditPreserved {
		t.Fatalf("upgrade changed legacy authorization or audit: %t %t (%v)", linkPreserved, auditPreserved, err)
	}
	const insert = `INSERT INTO api_key_audits(id,api_key_id,actor_user_id,action,comment,created_at)
		VALUES(gen_random_uuid(),$1,$2,'student_model_removed',$3,now())`
	seed(insert, key, student, "old-model")
	_, err := conn.ExecContext(ctx, insert, key, student, " ")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "api_key_audit_model_required" {
		t.Fatalf("empty model ID was not rejected by the audit constraint: %v", err)
	}
}
