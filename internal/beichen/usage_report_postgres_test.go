package beichen

import (
	"context"
	"database/sql"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func reportTestTransaction(t *testing.T) (context.Context, *sql.Tx) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("需要隔离的本机 nebula_test PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || config.Database != "nebula_test" ||
		(config.User != "nebula_qa" && config.User != "nebula_test") || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("仅允许本机隔离的 nebula_test 数据库、测试用户及 sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	schema := pgx.Identifier{"beichen_reports_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema)
	execTestSQL(t, ctx, tx, `
CREATE TABLE providers(id uuid PRIMARY KEY, base_url text NOT NULL);
CREATE TABLE provider_credentials(id uuid PRIMARY KEY, provider_id uuid NOT NULL, label text, created_at timestamptz NOT NULL);
CREATE TABLE users(id uuid PRIMARY KEY);
CREATE TABLE gateway_calls(
 provider_id uuid, provider_credential_id uuid, binding_id uuid, upstream_model_name text,
 created_at timestamptz, sent_at timestamptz, outcome text, protocol text, usage_state text,
 prompt_tokens int, completion_tokens int, cache_hit_tokens bigint, cache_miss_tokens bigint, cache_write_tokens bigint,
 beichen_state text, beichen_credit_exact numeric, beichen_dispatch_kind text, beichen_dispatch_at timestamptz,
 beichen_dispatch_credential_id uuid, beichen_dispatch_model text, beichen_price_at timestamptz,
 project_id uuid, project_name text, user_id uuid, user_name text, api_key_id uuid, api_key_name text,
 beichen_base_credits numeric, beichen_school_multiplier numeric
)`)
	migration, err := os.ReadFile("../infrastructure/db/migrations/0030_beichen.sql")
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, string(migration))
	return ctx, tx
}

type reportCallFixture struct {
	provider, credential, project, user, key                       uuid.UUID
	model, protocol, state, usage, credits, base, multiplier, sent string
	prompt, output                                                 int64
	read, miss, write                                              any
	legacy, unfinalized                                            bool
}

func insertReportCall(t *testing.T, ctx context.Context, tx *sql.Tx, call reportCallFixture) {
	t.Helper()
	var provider, binding, base any
	kind, outcome := "beichen", "succeeded"
	if call.legacy {
		provider, binding, kind = call.provider, uuid.New(), "legacy"
	}
	if call.unfinalized {
		outcome = "pending"
	}
	if call.base != "" {
		base = call.base
	}
	execTestSQL(t, ctx, tx, `INSERT INTO gateway_calls(
 provider_id, provider_credential_id, binding_id, upstream_model_name, created_at, sent_at, outcome, protocol, usage_state,
 prompt_tokens, completion_tokens, cache_hit_tokens, cache_miss_tokens, cache_write_tokens,
 beichen_state, beichen_credit_exact, beichen_dispatch_kind, beichen_dispatch_at, beichen_dispatch_credential_id,
 beichen_dispatch_model, beichen_price_at, project_id, project_name, user_id, user_name, api_key_id, api_key_name,
 beichen_base_credits, beichen_school_multiplier)
VALUES($1,$2,$3,$4,$5::text::timestamptz - interval '1 day',$5::text::timestamptz - CASE WHEN $10::text='beichen' THEN interval '1 day' ELSE interval '0' END,
 $6,$7,$8,$11,$12,$13,$14,$15,$9,$16::text::numeric,$10,$5::text::timestamptz,$2,$4,$5::text::timestamptz,
 $17::uuid,'项目 ' || $17::uuid::text,$18::uuid,'学生 ' || $18::uuid::text,$19::uuid,'Key ' || $19::uuid::text,$20::text::numeric,$21::text::numeric)`,
		provider, call.credential, binding, call.model, call.sent, outcome, call.protocol, call.usage, call.state, kind,
		call.prompt, call.output, call.read, call.miss, call.write, call.credits, call.project, call.user, call.key, base, call.multiplier)
}

func TestModelUsagePostgresRangeAndAttribution(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider, credential, project, user, key := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'测试渠道','2026-10-01 00:00+08')", credential, provider)
	call := reportCallFixture{provider: provider, credential: credential, project: project, user: user, key: key,
		model: "boundary", protocol: "openai_responses", state: "charged", usage: "reported", credits: "1", base: "1", multiplier: "1",
		prompt: 100, output: 10, read: 20, write: 5}
	for _, sent := range []string{
		"2026-10-31T23:59:59+08:00", "2026-11-01T00:00:00+08:00", "2026-11-19T23:59:59+08:00",
		"2026-11-20T00:00:00+08:00", "2026-11-20T00:29:59+08:00", "2026-11-20T00:30:00+08:00",
		"2026-11-21T00:00:00+08:00", "2026-12-01T00:00:00+08:00",
	} {
		call.sent = sent
		insertReportCall(t, ctx, tx, call)
	}
	other, otherCredential := uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,'https://other.invalid')", other)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'其他','2026-10-01 00:00+08')", otherCredential, other)
	call.provider, call.credential, call.sent = other, otherCredential, "2026-11-20T00:00:00+08:00"
	insertReportCall(t, ctx, tx, call)
	call.provider, call.credential, call.unfinalized = provider, credential, true
	insertReportCall(t, ctx, tx, call)
	now := time.Date(2026, 11, 20, 0, 30, 0, 0, beijing)
	for _, tc := range []struct {
		value string
		calls int64
	}{{"day", 3}, {"month", 6}, {"all", 5}} {
		period, _ := reportPeriod(tc.value, now)
		for _, selected := range []*uuid.UUID{nil, &provider} {
			got, err := readModelUsage(ctx, tx, selected, period)
			if err != nil {
				t.Fatal(err)
			}
			if got.Totals.Calls != tc.calls || got.Totals.Tokens != tc.calls*110 || got.Totals.Credits != strconv.FormatInt(tc.calls, 10) ||
				len(got.Models) != 1 || len(got.Students) != 1 || len(got.Keys) != 1 {
				t.Fatalf("%s: %+v", tc.value, got)
			}
		}
	}
}

func TestReportsPostgresModelsStudentsKeysAndReconciliation(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider, first, second, future := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	project, otherProject, student, otherStudent, key, otherKey, thirdKey := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials VALUES
 ($1,$4,'第一渠道','2026-11-18 12:00+08'),($2,$4,'第二渠道','2026-11-20 00:00+08'),($3,$4,'未来渠道','2026-11-22 00:00+08')`, first, second, future, provider)
	call := reportCallFixture{provider: provider, credential: first, project: project, user: student, key: key,
		model: "matched", protocol: "openai_compatible", state: "charged", usage: "reported", credits: "11.00000000000000000001", base: "10", multiplier: "1.1",
		sent: "2026-11-20T00:00:00+08:00", prompt: 100, output: 10, read: 20, write: 5}
	insertReportCall(t, ctx, tx, call)
	call.protocol, call.prompt, call.credits, call.base = "anthropic", 75, "22", "20"
	insertReportCall(t, ctx, tx, call)
	call.model, call.state, call.credits, call.base = "pending", "pending_review", "999", ""
	insertReportCall(t, ctx, tx, call)
	call.model, call.state, call.usage, call.credits = "missing", "charged", "missing", "1.1"
	insertReportCall(t, ctx, tx, call)
	call.credential, call.project, call.user, call.key = second, otherProject, otherStudent, otherKey
	call.model, call.protocol, call.usage, call.credits, call.base = "local", "openai_responses", "reported", "5.5", "5"
	call.prompt, call.output, call.read, call.miss, call.write = 50, 2, 10, 40, nil
	insertReportCall(t, ctx, tx, call)
	call.user, call.key, call.model, call.legacy = student, thirdKey, "matched", true
	call.protocol, call.credits, call.base, call.multiplier = "openai_realtime", "1", "1", "1"
	call.prompt, call.output, call.read, call.miss, call.write = 10, 3, nil, 10, 0
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
 VALUES($1,'matched','upstream','effective','billing','2026-01-01',1000000,2000000,1000000,1000000),
 ($2,'matched','upstream','effective','billing','2026-01-01',1000000,2000000,1000000,1000000)`, first, second)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source)
 VALUES($1,$2,'2026-11-20','matched',2,150,20,40,10,330000,'usage'),
 ($1,$3,'2026-11-20','matched',1,10,3,0,0,10000,'usage'),
 ($1,$2,'2026-11-20','upstream-only',1,40,0,0,0,40000,'usage'),
 ($1,$2,'2026-11-19','prior',1,1,1,0,0,500000,'usage')`, provider, first, second)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_usage_days(provider_id,credential_id,day,empty) VALUES
 ($1,$2,'2026-11-18',true),($1,$2,'2026-11-20',false)`, provider, first)
	now := time.Date(2026, 11, 20, 4, 0, 0, 0, beijing)
	period, _ := reportPeriod("day", now)
	usage, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Totals.Credits != "40.60000000000000000001" || usage.Totals.Tokens != 285 || usage.Totals.Calls != 6 ||
		usage.Totals.PendingCalls != 1 || usage.Totals.Students != 2 || usage.Totals.Keys != 3 || len(usage.Students) != 3 || len(usage.Keys) != 3 {
		t.Fatalf("用量总计错误: %+v", usage)
	}
	for _, row := range usage.Students {
		if row.Models == nil || row.ProjectName == "" || row.UserName == "" {
			t.Fatalf("成员缺少项目或模型明细: %+v", row)
		}
	}
	for _, row := range usage.Keys {
		if row.Models == nil || row.APIKeyName == "" || row.ProjectName == "" {
			t.Fatalf("Key 缺少明细: %+v", row)
		}
	}
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Totals.Local.Tokens != usage.Totals.Tokens || view.Totals.Local.Requests != usage.Totals.Calls || view.Totals.Upstream.Tokens != 273 ||
		view.Totals.Money.StudentYuan != "0.4060000000000000000001" || view.Totals.Money.BaseYuan != "0.37" ||
		reportDecimal(view.Totals.Money.ActualYuan) != "0.38" || reportDecimal(view.Totals.Money.MarginYuan) != "0.0260000000000000000001" {
		t.Fatalf("金额或 Token 汇总错误: %+v", view.Totals)
	}
	if view.Coverage.ExpectedDays != 2 || view.Coverage.SyncedDays != 1 || strings.Join(view.Coverage.MissingDays, ",") != "2026-11-20" ||
		view.Coverage.PendingCalls != 1 || view.Coverage.UnpricedCalls != 1 || !view.Coverage.MultiplierMixed {
		t.Fatalf("覆盖情况错误: %+v", view.Coverage)
	}
	for _, row := range view.Models {
		switch row.Model {
		case "matched":
			if row.MatchStatus != "matched" || row.Local.Tokens != 233 || row.Upstream.Tokens != 233 || row.Local.InputTokens != 160 ||
				row.Local.EstimatedMicros == nil || *row.Local.EstimatedMicros != 256 || row.LocalShare == "0" || row.UpstreamShare == "0" {
				t.Errorf("已匹配模型归一化错误: %+v", row)
			}
			checkReportRatio(t, row.LocalShare, 233, 285)
			checkReportRatio(t, row.UpstreamShare, 233, 273)
		case "local", "pending", "missing":
			if row.MatchStatus != "local_only" || row.ActualYuan != nil || row.MarginYuan != nil || row.MarginRate != nil ||
				((row.Model == "pending" || row.Model == "missing") && row.Local.Tokens != 0) {
				t.Errorf("本地模型状态错误: %+v", row)
			}
		case "upstream-only":
			if row.MatchStatus != "upstream_only" || row.LocalShare != "0" || reportDecimal(row.MarginYuan) != "-0.04" || reportDecimal(row.MarginRate) != "0" {
				t.Errorf("上游独有模型错误: %+v", row)
			}
		default:
			t.Errorf("跨日混入模型: %+v", row)
		}
	}
	if len(view.Models) != 5 || len(view.Keys) != 2 || view.Totals.Local.EstimatedMicros != nil {
		t.Fatalf("模型、渠道或缺价估算错误: %+v", view)
	}
	for _, row := range view.Keys {
		if row.Label == nil || (row.CredentialID == first && row.Local.Tokens != 220) || (row.CredentialID == second && row.Local.Tokens != 65) {
			t.Errorf("渠道聚合错误: %+v", row)
		}
		if row.CredentialID == first && reportDecimal(row.ActualYuan) != "0.37" || row.CredentialID == second && reportDecimal(row.ActualYuan) != "0.01" {
			t.Errorf("渠道扣款重复累计: %+v", row)
		}
	}
	for _, value := range []string{"month", "all"} {
		period, _ = reportPeriod(value, now)
		view, err = readReconciliation(ctx, tx, provider, period, now)
		if err != nil || view.Coverage.ExpectedDays != 4 || view.Coverage.SyncedDays != 2 ||
			strings.Join(view.Coverage.MissingDays, ",") != "2026-11-19,2026-11-20" || reportDecimal(view.Totals.Money.ActualYuan) != "0.88" {
			t.Fatalf("%s 覆盖情况错误: %+v, err=%v", value, view, err)
		}
	}
	// 凭据删除不删除已归档账单，本地存在 provider_id 的调用也仍可追溯。
	execTestSQL(t, ctx, tx, "DELETE FROM provider_credentials WHERE id=$1", second)
	period, _ = reportPeriod("day", now)
	view, err = readReconciliation(ctx, tx, provider, period, now)
	if err != nil || len(view.Keys) != 2 {
		t.Fatalf("历史凭据记录丢失: %+v, err=%v", view, err)
	}
}

func TestReportsPostgresEmptyResults(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider := uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, beijing)
	period, _ := reportPeriod("all", now)
	usage, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil || usage.Totals.Credits != "0" || usage.Models == nil || usage.Students == nil || usage.Keys == nil {
		t.Fatalf("空用量应返回零和空数组: %+v, err=%v", usage, err)
	}
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil || view.Totals.Money.ActualYuan != nil || view.Totals.Money.MarginRate != nil || view.Totals.Money.MarginYuan != nil ||
		view.Models == nil || view.Keys == nil || view.Coverage.MissingDays == nil ||
		view.Coverage.Credentials == nil || view.Totals.Local.EstimatedMicros != nil {
		t.Fatalf("空对账应返回零和空数组: %+v, err=%v", view, err)
	}
}

func reportDecimal(value *string) string {
	if value == nil {
		return "<null>"
	}
	return *value
}

func checkReportRatio(t *testing.T, value string, numerator, denominator int64) {
	t.Helper()
	got, ok := new(big.Rat).SetString(value)
	if !ok {
		t.Fatalf("占比不是精确数字字符串: %q", value)
	}
	difference := new(big.Rat).Sub(got, big.NewRat(numerator, denominator))
	if difference.Abs(difference).Cmp(big.NewRat(1, 1_000_000_000_000)) >= 0 {
		t.Errorf("占比 %s 不等于 %d/%d", value, numerator, denominator)
	}
}

func TestReconciliationPostgresMissingCostRemainsUnknown(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider, first, second, zero := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials VALUES
 ($1,$4,'已知渠道','2026-11-20 00:00+08'),($2,$4,'未知渠道','2026-11-20 00:00+08'),($3,$4,'零扣款渠道','2026-11-20 00:00+08')`, first, second, zero, provider)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source)
 VALUES($1,$2,'2026-11-20','partial',1,10,1,0,0,5000,'usage'),
 ($1,$3,'2026-11-20','partial',1,10,1,0,0,NULL,'usage'),
 ($1,$3,'2026-11-20','unknown',1,10,1,0,0,NULL,'usage'),
 ($1,$4,'2026-11-20','zero',1,0,0,0,0,0,'usage')`, provider, first, second, zero)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_usage_days(provider_id,credential_id,day,empty)
 VALUES($1,$2,'2026-11-20',false),($1,$3,'2026-11-20',false),($1,$4,'2026-11-20',false)`, provider, first, second, zero)
	call := reportCallFixture{provider: provider, credential: first, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		model: "partial", protocol: "anthropic", state: "charged", usage: "reported", credits: "11", base: "10", multiplier: "1.1",
		sent: "2026-11-20T00:10:00+08:00", prompt: 10, output: 1, read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	period, _ := reportPeriod("day", now)
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Coverage.ExpectedDays != 3 || view.Coverage.SyncedDays != 3 || view.Totals.Upstream.BilledMicros != nil ||
		view.Totals.Money.ActualYuan != nil || view.Totals.Money.MarginYuan != nil || view.Totals.Money.MarginRate != nil {
		t.Fatalf("覆盖完整但成本缺失时不能伪造零实扣或利润: %+v", view)
	}
	for _, row := range view.Models {
		if row.Model == "zero" {
			if reportDecimal(row.ActualYuan) != "0" || reportDecimal(row.MarginYuan) != "0" || reportDecimal(row.MarginRate) != "0" {
				t.Errorf("明确零实扣必须保留: %+v", row)
			}
		} else if row.Upstream.BilledMicros != nil || row.ActualYuan != nil || row.MarginYuan != nil || row.MarginRate != nil {
			t.Errorf("模型的全部或部分成本未知时应返回 null: %+v", row)
		}
	}
	for _, row := range view.Keys {
		if row.CredentialID == second {
			if row.Upstream.BilledMicros != nil || row.ActualYuan != nil || row.MarginYuan != nil || row.MarginRate != nil {
				t.Errorf("未知渠道的成本或差额变成零: %+v", row)
			}
		} else if row.CredentialID == first && reportDecimal(row.ActualYuan) != "0.005" {
			t.Errorf("已知渠道成本丢失: %+v", row)
		}
	}
}
