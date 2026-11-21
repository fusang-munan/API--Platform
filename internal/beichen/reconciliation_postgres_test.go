package beichen

import (
	"context"
	"database/sql"
	"os"
	"reflect"
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
	for _, name := range []string{"0034_beichen_panel_access.sql", "0035_beichen_receipts.sql", "0044_beichen_conditional_billing.sql", "0046_beichen_billing_checks_and_aliases.sql"} {
		migration, err := os.ReadFile("../infrastructure/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(migration))
	}
	return ctx, tx
}

func TestCatalogQualityAliasAggregationPostgres(t *testing.T) {
	ctx, tx := reconciliationTestTransaction(t)
	provider, first, second, actor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO users(id) VALUES($1)", actor)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id,provider_id,label,created_at) VALUES
 ($1,$3,'第一渠道','2026-11-20 00:00+08'),($2,$3,'第二渠道','2026-11-20 00:00+08')`, first, second, provider)
	call := reportCallFixture{provider: provider, credential: first, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		model: "historical", protocol: "anthropic", state: "charged", usage: "reported", credits: "20", base: "20", multiplier: "1",
		sent: "2026-11-20T10:00:00+08:00", prompt: 10, output: 1, read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	call.model = "Current"
	insertReportCall(t, ctx, tx, call)
	call.credential, call.model = second, "historical"
	insertReportCall(t, ctx, tx, call)
	call.model = "untouched"
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_price_versions (credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES ($1,'historical','upstream','effective','billing','2026-01-01',1000000,2000000,0,0),
($1,'Current','upstream','effective','billing','2026-01-01',1000000,2000000,0,0)`, first)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source)
VALUES($1,$2,'2026-11-20','historical',2,20,2,0,0,30,'usage'),($1,$2,'2026-11-20','current',1,20,2,0,0,NULL,'usage'),
($1,$3,'2026-11-20','historical',1,10,1,0,0,10,'usage'),($1,$3,'2026-11-20','untouched',1,10,1,0,0,0,'usage')`, provider, first, second)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	period, _ := reportPeriod("day", now)
	baseline, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, `INSERT INTO upstream_model_aliases (provider_id,credential_id,upstream_model,school_model,created_by)
VALUES ($1,$2,'HISTORICAL','school-specific',$3),($1,$2,'current','school-specific',$3),
       ($1,NULL,'historical','school-default',$3)`, provider, first, actor)
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Totals, baseline.Totals) || !reflect.DeepEqual(view.Coverage, baseline.Coverage) ||
		!reflect.DeepEqual(view.Keys, baseline.Keys) || len(view.Models) != 3 {
		t.Fatal("别名应只改变同渠道的模型分组")
	}
	for _, row := range view.Models {
		if row.MatchStatus != "matched" {
			t.Fatal("两侧没有按同一校内名称合并")
		}
		switch row.Model {
		case "school-specific":
			if row.CredentialID != first || row.Local.Requests != 2 || row.Upstream.Requests != 3 ||
				row.Local.EstimatedMicros == nil || *row.Local.EstimatedMicros != 24 || row.Upstream.BilledMicros != nil {
				t.Fatalf("渠道优先、按原始名称查价或缺失成本处理错误: %+v", row)
			}
		case "school-default", "untouched":
			if row.CredentialID != second || row.Local.Requests != 1 || row.Upstream.Requests != 1 {
				t.Fatalf("默认别名或原名回退错误: %+v", row)
			}
		default:
			t.Fatalf("意外的对账模型分组: %s", row.Model)
		}
	}
	daily := Overview{}
	start, _ := dayStart("2026-11-20")
	if err := readComparison(ctx, tx, provider, start, &daily); err != nil {
		t.Fatal(err)
	}
	if len(daily.Comparison) != 3 {
		t.Fatalf("日对账应按相同规则合并: %+v", daily.Comparison)
	}
	for _, row := range daily.Comparison {
		if row.LocalRequests == nil || row.UpstreamRequests == nil ||
			(row.Model == "school-specific" && (*row.LocalRequests != 2 || *row.UpstreamRequests != 3 || row.BilledUpstreamMicros != nil)) {
			t.Fatalf("日对账合并缺失同渠道明细或缺失成本处理错误: %+v", row)
		}
	}
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
	execTestSQL(t, ctx, tx, `UPDATE gateway_calls SET student_credits=10, actual_credits=11.000000000000000001,
 school_absorbed_credits=1.000000000000000001, beichen_base_credits=NULL,
 pricing_detail='{"base_credits":"11.000000000000000001"}'
 WHERE beichen_dispatch_credential_id=$1`, first)
	execTestSQL(t, ctx, tx, `UPDATE gateway_calls SET student_credits=4.5, actual_credits=4.5, beichen_base_credits=4.5
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
	if view.Coverage.ExpectedDays != 3 || view.Coverage.SyncedDays != 0 || view.Coverage.ComparableDays != 0 ||
		view.Coverage.CoverageKind != "none" || view.Coverage.UnpricedCalls != 0 || view.Totals.Money.MarginAvailable {
		t.Fatalf("没有确认完整账单的日期应是未同步: %+v", view.Coverage)
	}
	if len(view.Models) != 5 || len(view.Keys) != 3 || view.Totals.Local.Credits != "94.5" ||
		view.Totals.Local.ChargedTokensMissingCalls != 2 || view.Totals.Upstream.Requests != 6 {
		t.Fatalf("汇总未区分阶段 2 已扣积分与历史扣分: %+v", view)
	}
	wantCompleteness := map[string]string{"zero-tokens": "complete", "partial-local": "partial", "partial-upstream": "partial", "no-tokens": "partial"}
	for _, row := range view.Models {
		if row.MatchStatus != "matched" || row.Completeness != wantCompleteness[row.Model] {
			t.Errorf("模型记录状态和 Token 回报状态错误: %+v", row)
		}
		if row.Model == "zero-tokens" {
			wantSchool := map[uuid.UUID]string{first: "1.000000000000000001", second: "0"}[row.CredentialID]
			wantStudent := map[uuid.UUID]string{first: "0.1", second: "0.045"}[row.CredentialID]
			wantBase := map[uuid.UUID]string{first: "0.11000000000000000001", second: "0.045"}[row.CredentialID]
			if row.Local.Tokens != 0 || row.Upstream.Tokens != 0 || reportDecimal(row.SchoolAbsorbedCredits) != wantSchool ||
				row.StudentYuan != wantStudent || row.BaseYuan != wantBase {
				t.Errorf("零 Token 或触顶前的基础金额未精确保留: %+v", row)
			}
		} else if row.SchoolAbsorbedCredits != nil || row.StudentYuan == "0" {
			t.Errorf("历史记录的学校承担或学生已扣错误: %+v", row)
		}
		if row.Model == "no-tokens" && (row.Local.ChargedTokensMissingCalls != 1 || row.Local.Completeness != "partial" ||
			row.Local.Tokens != 10 || row.Local.TokensMissing != 0 || row.Upstream.Completeness != "missing") {
			t.Errorf("旧记录标记为缺失但仍保留原始分项: %+v", row)
		}
	}
	for _, row := range view.Keys {
		wantStatus := map[uuid.UUID]string{first: "complete", second: "partial", third: "partial"}[row.CredentialID]
		wantSchool := map[uuid.UUID]string{first: "1.000000000000000001", second: "0", third: "<null>"}[row.CredentialID]
		if row.Completeness != wantStatus || reportDecimal(row.SchoolAbsorbedCredits) != wantSchool {
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
		model, day, usage                       string
		receipt, claimConflict, gatewayConflict bool
		partial                                 bool
	}{
		{model: "verified", day: "2026-11-20", receipt: true},
		{model: "verified-partial", day: "2026-11-20", receipt: true, partial: true},
		{model: "verified-no-usage", day: "2026-11-20", receipt: true, usage: "missing"},
		{model: "previous-day", day: "2026-11-19", receipt: true},
		{model: "claim-without-receipt", day: "2026-11-20"},
		{model: "claim-conflict", day: "2026-11-20", receipt: true, claimConflict: true},
		{model: "gateway-conflict", day: "2026-11-20", receipt: true, gatewayConflict: true},
	} {
		call.model, call.sent = tc.model, tc.day+"T10:00:00+08:00"
		call.usage, call.read = "reported", 0
		if tc.usage != "" {
			call.usage = tc.usage
		}
		if tc.partial {
			call.read = nil
		}
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
		rangeValue            string
		wantCount, wantTokens int64
	}{{"day", 3, 1}, {"month", 4, 2}} {
		period, _ := reportPeriod(tc.rangeValue, now)
		view, err := readReconciliation(ctx, tx, provider, period, now)
		if err != nil || view.Coverage.VerifiedMatchedCalls != tc.wantCount ||
			view.Coverage.VerifiedMatchedCompleteTokenCalls != tc.wantTokens || view.Coverage.ComparableDays != 0 {
			t.Fatalf("%s 逐笔关联数或本地 Token 完整笔数错误: %+v, err=%v", tc.rangeValue, view.Coverage, err)
		}
	}
}
