package db

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBeichenManualPriceSourceMigration(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0043")
	migration, err := migrationFiles.ReadFile("migrations/0043_beichen_manual_price_source.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := conn.ExecContext(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	call := uuid.New()
	_, err = conn.ExecContext(ctx, `INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,
api_key_id,model_id,organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,
multiplier_milli,credit_milli,billing_state,created_at,beichen_state,beichen_credit_exact,beichen_base_credits,
beichen_base_source,beichen_school_multiplier)
VALUES($1,'manual-migration',now(),$1,$1,$1,$1,$1,'org','project','user','key','model','openai_compatible',
'/v1/chat/completions',1000,1100,'charged',now(),'charged',1.10,1,'teacher_manual_price',1.10)`, call)
	if err != nil {
		t.Fatalf("新来源不能写入: %v", err)
	}
	for _, assignment := range []string{"beichen_base_source='invalid_source'", "beichen_base_source=NULL", "beichen_base_credits=2"} {
		if _, err := conn.ExecContext(ctx, "UPDATE gateway_calls SET "+assignment+" WHERE id=$1", call); err == nil || !strings.Contains(err.Error(), "23514") {
			t.Fatalf("原有数据约束丢失: %s, %v", assignment, err)
		}
	}
	var source string
	if err := conn.QueryRowContext(ctx, "SELECT beichen_base_source FROM gateway_calls WHERE id=$1", call).Scan(&source); err != nil || source != "teacher_manual_price" {
		t.Fatalf("拒绝的更新改变了来源: %s, %v", source, err)
	}
}
