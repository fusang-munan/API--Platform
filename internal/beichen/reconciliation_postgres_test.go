package beichen

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func reconciliationTestTransaction(t *testing.T) (context.Context, *sql.Tx) {
	t.Helper()
	ctx, tx := reportTestTransaction(t)
	execTestSQL(t, ctx, tx, `
ALTER TABLE providers ADD COLUMN status text;
ALTER TABLE provider_credentials ADD COLUMN status text, ADD COLUMN updated_at timestamptz;
ALTER TABLE gateway_calls ADD COLUMN id uuid PRIMARY KEY DEFAULT gen_random_uuid();`)
	for _, name := range []string{"0034_beichen_panel_access.sql", "0035_beichen_receipts.sql", "0044_beichen_conditional_billing.sql"} {
		migration, err := os.ReadFile("../infrastructure/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(migration))
	}
	return ctx, tx
}

func TestReconciliationPostgresStudentAndSchoolChargesAndTokenCompleteness(t *testing.T) {
	ctx, tx := reconciliationTestTransaction(t)
	provider, first, second, third := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id, provider_id, label, created_at) VALUES
 ($1,$4,'第一渠道','2026-11-20 00:00+08'),($2,$4,'第二渠道','2026-11-20 00:00+08'),($3,$4,'第三渠道','2026-11-20 00:00+08')`, first, second, third, provider)
	call := reportCallFixture{provider: provider, credential: first, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		model: "zero-tokens", protocol: "anthropic", state: "charged", usage: "reported", credits: "20", base: "20", multiplier: "1",
		sent: "2026-11-20T10:00:00+08:00", prompt: 0, output: 0, read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	call.credential = second
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `UPDATE gateway_calls SET student_credits=10, school_absorbed_credits=1.000000000000000001
 WHERE beichen_dispatch_credential_id=$1`, first)
	execTestSQL(t, ctx, tx, `UPDATE gateway_calls SET student_credits=4.5, school_absorbed_credits=4.000000000000000003
 WHERE beichen_dispatch_credential_id=$1`, second)
	call.model, call.prompt = "partial-local", 10
	insertReportCall(t, ctx, tx, call)
	call.usage = "missing"
	insertReportCall(t, ctx, tx, call)
	call.model, call.usage = "partial-upstream", "reported"
	insertReportCall(t, ctx, tx, call)
	call.model, call.credential, call.usage = "no-tokens", third, "missing"
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source)
 VALUES($1,$2,'2026-11-20','zero-tokens',1,0,0,0,0,10,'usage'),
 ($1,$3,'2026-11-20','zero-tokens',1,0,0,0,0,20,'usage'),
 ($1,$3,'2026-11-20','partial-local',2,10,0,0,0,0,'usage'),
 ($1,$3,'2026-11-20','partial-upstream',1,0,0,NULL,0,0,'usage'),
 ($1,$4,'2026-11-20','no-tokens',1,NULL,NULL,NULL,NULL,0,'usage')`, provider, first, second, third)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	period, _ := reportPeriod("day", now)
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Coverage.ExpectedDays != 3 || view.Coverage.SyncedDays != 0 || view.Coverage.CoverageKind != "none" {
		t.Fatalf("没有确认完整账单的日期应是未同步: %+v", view.Coverage)
	}
	if len(view.Models) != 4 || len(view.Keys) != 3 || view.Totals.Local.Credits != "94.5" {
		t.Fatalf("汇总未区分阶段 2 已扣积分与历史扣分: %+v", view)
	}
	wantCompleteness := map[string]string{"zero-tokens": "complete", "partial-local": "partial", "partial-upstream": "partial", "no-tokens": "missing"}
	for _, row := range view.Models {
		if row.MatchStatus != "matched" || row.Completeness != wantCompleteness[row.Model] {
			t.Errorf("模型记录状态和 Token 回报状态错误: %+v", row)
		}
		if row.Model == "zero-tokens" {
			if row.Local.Tokens != 0 || row.Upstream.Tokens != 0 || row.SchoolAbsorbedCredits != "5.000000000000000004" || row.StudentYuan != "0.145" {
				t.Errorf("零 Token 或跨渠道金额未精确保留: %+v", row)
			}
		} else if row.SchoolAbsorbedCredits != "0" || row.StudentYuan == "0" {
			t.Errorf("历史记录的学校承担或学生已扣错误: %+v", row)
		}
	}
	for _, row := range view.Keys {
		wantStatus := map[uuid.UUID]string{first: "complete", second: "partial", third: "missing"}[row.CredentialID]
		wantSchool := map[uuid.UUID]string{first: "1.000000000000000001", second: "4.000000000000000003", third: "0"}[row.CredentialID]
		if row.Completeness != wantStatus || row.SchoolAbsorbedCredits != wantSchool {
			t.Errorf("渠道账本汇总错误: %+v", row)
		}
	}
}

func TestReconciliationPostgresVerifiedCallsRequireUnconflictedReceipt(t *testing.T) {
	ctx, tx := reconciliationTestTransaction(t)
	provider, credential := uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'测试渠道','2026-11-19 00:00+08')", credential, provider)
	call := reportCallFixture{provider: provider, credential: credential, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		protocol: "anthropic", state: "charged", usage: "reported", credits: "1", base: "1", multiplier: "1",
		prompt: 10, output: 0, read: 0, write: 0}
	for index, tc := range []struct {
		model, day                              string
		receipt, claimConflict, gatewayConflict bool
	}{
		{model: "verified", day: "2026-11-20", receipt: true},
		{model: "previous-day", day: "2026-11-19", receipt: true},
		{model: "claim-without-receipt", day: "2026-11-20"},
		{model: "claim-conflict", day: "2026-11-20", receipt: true, claimConflict: true},
		{model: "gateway-conflict", day: "2026-11-20", receipt: true, gatewayConflict: true},
	} {
		call.model, call.sent = tc.model, tc.day+"T10:00:00+08:00"
		insertReportCall(t, ctx, tx, call)
		responseID := uuid.New()
		var callID uuid.UUID
		if err := tx.QueryRowContext(ctx, `UPDATE gateway_calls SET beichen_receipt_response_id=$1, beichen_receipt_conflict=$2
 WHERE beichen_dispatch_model=$3 RETURNING id`, responseID, tc.gatewayConflict, call.model).Scan(&callID); err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, `INSERT INTO beichen_receipt_response_claims VALUES($1,$2,
 CASE WHEN $3::boolean THEN gen_random_uuid() END, CASE WHEN $3::boolean THEN now() END)`, responseID, callID, tc.claimConflict)
		if tc.receipt {
			execTestSQL(t, ctx, tx, `INSERT INTO beichen_call_receipts(row_id,call_id,credential_id,response_id,api_key_id,model,occurred_at,
 actual_cost,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,credits_booked,applied)
 VALUES($1,$2,$3,$4,1,$5,$6::text::timestamptz,0.01,10,0,0,0,1,false)`, index+1, callID, credential, responseID, call.model, call.sent)
		}
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	for _, tc := range []struct {
		rangeValue string
		wantCount  int64
	}{{"day", 1}, {"month", 2}} {
		period, _ := reportPeriod(tc.rangeValue, now)
		view, err := readReconciliation(ctx, tx, provider, period, now)
		if err != nil || view.Coverage.VerifiedMatchedCalls != tc.wantCount {
			t.Fatalf("%s 逐笔关联数 = %d，期望 %d，错误 %v", tc.rangeValue, view.Coverage.VerifiedMatchedCalls, tc.wantCount, err)
		}
	}
}
