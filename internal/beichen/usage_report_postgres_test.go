package beichen

import (
	"context"
	"database/sql"
	"math"
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
	ctx, _, tx := reportTestSetup(t)
	return ctx, tx
}

func reportTestSetup(t *testing.T) (context.Context, *sql.DB, *sql.Tx) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("需要隔离的本机 nebula_test PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || (config.Database != "nebula_test" && config.Database != "nebula_test_rev_b") ||
		(config.User != "nebula_qa" && config.User != "nebula_test") || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("仅允许本机隔离的 nebula_test 数据库、测试用户及 sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := pgx.Identifier{"beichen_reports_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, db, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config.RuntimeParams["search_path"] = schema + ",public"
	isolated := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = isolated.Close() })
	tx, err := isolated.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	execTestSQL(t, ctx, tx, `
CREATE EXTENSION IF NOT EXISTS citext;
CREATE TABLE providers(id uuid PRIMARY KEY, base_url text NOT NULL);
CREATE TABLE provider_credentials(id uuid PRIMARY KEY, provider_id uuid NOT NULL, label text, created_at timestamptz NOT NULL);
CREATE TABLE users(id uuid PRIMARY KEY);
CREATE TABLE models(id uuid PRIMARY KEY, model_id citext NOT NULL UNIQUE);
CREATE TABLE gateway_calls(
 provider_id uuid, provider_credential_id uuid, binding_id uuid, upstream_model_name text,
 created_at timestamptz, sent_at timestamptz, outcome text, protocol text, usage_state text,
 prompt_tokens int, completion_tokens int, total_tokens int, cache_hit_tokens bigint, cache_miss_tokens bigint, cache_write_tokens bigint,
 reasoning_tokens bigint, tool_prompt_tokens bigint, usage_reason text,
 beichen_state text, beichen_credit_exact numeric, beichen_dispatch_kind text, beichen_dispatch_at timestamptz,
 beichen_dispatch_credential_id uuid, beichen_dispatch_model text, beichen_price_at timestamptz,
 project_id uuid, project_name text, user_id uuid, user_name text, api_key_id uuid, api_key_name text,
 beichen_base_credits numeric, beichen_school_multiplier numeric,
 model_id uuid, billing_state text, credit_milli bigint, unresolved_credit_milli bigint NOT NULL DEFAULT 0
)`)
	applyBeichenTestMigrations(t, ctx, tx, "0030_beichen.sql", "0046_beichen_billing_checks_and_aliases.sql")
	return ctx, isolated, tx
}

type reportCallFixture struct {
	provider, credential, project, user, key                       uuid.UUID
	model, protocol, state, usage, credits, base, multiplier, sent string
	prompt, output                                                 int64
	read, miss, write, total                                       any
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
 beichen_base_credits, beichen_school_multiplier, total_tokens)
VALUES($1,$2,$3,$4,$5::text::timestamptz - interval '1 day',$5::text::timestamptz - CASE WHEN $10::text='beichen' THEN interval '1 day' ELSE interval '0' END,
 $6,$7,$8,$11,$12,$13,$14,$15,$9,$16::text::numeric,$10,$5::text::timestamptz,$2,$4,$5::text::timestamptz,
 $17::uuid,'项目 ' || $17::uuid::text,$18::uuid,'学生 ' || $18::uuid::text,$19::uuid,'Key ' || $19::uuid::text,$20::text::numeric,$21::text::numeric,$22)`,
		provider, call.credential, binding, call.model, call.sent, outcome, call.protocol, call.usage, call.state, kind,
		call.prompt, call.output, call.read, call.miss, call.write, call.credits, call.project, call.user, call.key, base, call.multiplier, call.total)
}

func TestModelUsageCompletenessPostgres(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider, credential, project, first, second, key, secondKey := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'测试渠道','2026-11-19 00:00+08')", credential, provider)
	call := reportCallFixture{provider: provider, credential: credential, project: project, user: first, key: key,
		model: "ready", protocol: "anthropic", state: "charged", usage: "reported", credits: "1", base: "1", multiplier: "1",
		sent: "2026-11-20T00:00:00+08:00", prompt: 10, output: 2, read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	call.user, call.key, call.model, call.protocol = second, secondKey, "mixed", "openai_responses"
	call.prompt, call.output, call.read, call.write = 100, 20, 25, 5
	insertReportCall(t, ctx, tx, call)
	call.state = "pending_review"
	insertReportCall(t, ctx, tx, call)
	call.model, call.usage, call.state = "interrupted", "interrupted", "charged"
	call.prompt, call.output, call.read, call.write, call.total = 50, 4, 10, nil, 73
	insertReportCall(t, ctx, tx, call)
	call.model, call.usage, call.protocol = "incomplete", "reported", "anthropic"
	call.prompt, call.output, call.read, call.write, call.total = 9, 2, nil, 4, 18
	insertReportCall(t, ctx, tx, call)
	call.model, call.usage, call.state = "unknown", "missing", "pending_review"
	call.write, call.total = nil, nil
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, "UPDATE gateway_calls SET prompt_tokens=NULL,completion_tokens=NULL WHERE beichen_dispatch_model='unknown'")

	period, _ := reportPeriod("month", time.Date(2026, 11, 20, 4, 0, 0, 0, beijing))
	report, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil {
		t.Fatal(err)
	}
	check := func(label string, got ModelUsageMetrics, tokens, missing int64, completeness string, rate *float64) {
		t.Helper()
		if got.Tokens != tokens || got.TokensMissing != missing || got.Completeness != completeness {
			t.Errorf("%s 的 Token 或完整度有误: %+v", label, got)
		}
		if rate == nil {
			if got.CacheHitRate != nil {
				t.Errorf("%s 的缓存命中率应为空: %v", label, *got.CacheHitRate)
			}
		} else if got.CacheHitRate == nil {
			t.Errorf("%s 的缓存命中率为空，期望 %v", label, *rate)
		} else if math.Abs(*got.CacheHitRate-*rate) > 1e-12 {
			t.Errorf("%s 的缓存命中率有误: got=%v, want=%v", label, *got.CacheHitRate, *rate)
		}
	}
	totalRate, mixedRate, zero := 50.0/200, 25.0/95, 0.0
	check("总计", report.Totals, 343, 62, "partial", &totalRate)
	if got := report.Totals; got.InputTokens != 159 || got.OutputTokens != 48 ||
		got.CacheReadTokens != 60 || got.CacheWriteTokens != 14 || got.ChargedTokensMissingCalls != 2 || got.TokenMissingCalls != 1 {
		t.Errorf("已回报的分项累计或缺项笔数有误: %+v", got)
	}
	models := make(map[string]ModelUsageMetrics)
	for _, row := range report.Models {
		models[row.Model] = row.ModelUsageMetrics
	}
	check("完整", models["ready"], 12, 0, "complete", &zero)
	check("账款待核但 Token 已回报", models["mixed"], 240, 0, "complete", &mixedRate)
	check("上游有总数", models["interrupted"], 73, 59, "partial", nil)
	check("只有部分分项", models["incomplete"], 18, 3, "partial", nil)
	check("上游未回报", models["unknown"], 0, 0, "missing", nil)
	if len(report.Students) != 2 {
		t.Fatalf("应包含两个学生: %+v", report.Students)
	}
	for _, row := range report.Students {
		if row.UserID == first {
			check("第一个学生", row.ModelUsageMetrics, 12, 0, "complete", &zero)
		} else {
			check("第二个学生", row.ModelUsageMetrics, 331, 62, "partial", &mixedRate)
			for _, detail := range row.Models {
				check("第二个学生的 "+detail.Model, detail.ModelUsageMetrics, models[detail.Model].Tokens,
					models[detail.Model].TokensMissing, models[detail.Model].Completeness, models[detail.Model].CacheHitRate)
			}
		}
	}
	for _, row := range report.Keys {
		if row.APIKeyID == secondKey {
			check("第二个 Key", row.ModelUsageMetrics, 331, 62, "partial", &mixedRate)
		}
	}
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

func TestModelUsageLegacySettlementPostgres(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	provider, credential := uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'旧渠道','2026-10-01 00:00+08')", credential, provider)
	call := reportCallFixture{provider: provider, credential: credential, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		model: "legacy-free", protocol: "anthropic", state: "charged", usage: "reported", credits: "0", multiplier: "1", legacy: true,
		sent: "2026-11-20T00:00:00+08:00", read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	call.model = "legacy-paid"
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `UPDATE gateway_calls SET beichen_state=NULL,beichen_credit_exact=NULL,billing_state='charged',
 credit_milli=CASE WHEN upstream_model_name='legacy-free' THEN 0 ELSE 400 END WHERE upstream_model_name LIKE 'legacy-%'`)
	call.model, call.state, call.credits = "current-exact", "charged", "2.5"
	insertReportCall(t, ctx, tx, call)
	// 精确扣分优先；即使旧千分积分字段不同，也不得再把两列相加。
	execTestSQL(t, ctx, tx, "UPDATE gateway_calls SET credit_milli=9999 WHERE upstream_model_name='current-exact'")
	period, _ := reportPeriod("month", time.Date(2026, 11, 20, 4, 0, 0, 0, beijing))
	report, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil {
		t.Fatal(err)
	}
	got := report.Totals
	if got.DispatchedCalls != 3 || got.SettledCalls != 3 || got.ZeroCostCalls != 1 || got.Credits != "2.9" ||
		got.TokenMissingCalls != 0 || got.Completeness != "complete" {
		t.Fatalf("旧账本状态或精确积分兼容口径错误: %+v", got)
	}
	for _, model := range report.Models {
		want := map[string]string{"legacy-free": "0", "legacy-paid": "0.4", "current-exact": "2.5"}[model.Model]
		if model.Credits != want || model.SettledCalls != 1 {
			t.Fatalf("旧调用的逐型号扣分错误: %+v", model)
		}
		if model.Model == "legacy-paid" {
			gotCredits, ok := new(big.Rat).SetString(model.Credits)
			if !ok || gotCredits.Cmp(new(big.Rat).SetFrac64(400, 1000)) != 0 {
				t.Fatalf("400 千分积分应等于 0.400，实际为 %s", model.Credits)
			}
		}
	}
}

func TestReportsPostgresModelsStudentsKeysAndReconciliation(t *testing.T) {
	ctx, tx := reconciliationTestTransaction(t)
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
	call.model, call.protocol, call.usage, call.credits, call.base = "one-sided", "openai_responses", "reported", "5.5", "5"
	call.prompt, call.output, call.read, call.miss, call.write = 50, 2, 10, 40, nil
	insertReportCall(t, ctx, tx, call)
	call.user, call.key, call.model, call.legacy = student, thirdKey, "matched", true
	call.protocol, call.credits, call.base, call.multiplier = "openai_realtime", "1", "1", "1"
	call.prompt, call.output, call.read, call.miss, call.write = 10, 3, nil, 10, 0
	insertReportCall(t, ctx, tx, call)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
 VALUES($1,'matched','upstream','effective','billing','2026-01-01',1000000,2000000,1000000,1000000),
 ($2,'matched','upstream','effective','billing','2026-01-01',1000000,2000000,1000000,1000000)`, first, second)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source,actor_id)
 VALUES($1,$2,'2026-11-20','matched',2,150,20,40,10,330000,'csv',$4),
 ($1,$3,'2026-11-20','matched',1,10,3,0,0,10000,'csv',$4),
 ($1,$2,'2026-11-20','one-sided',1,40,0,0,0,40000,'csv',$4),
 ($1,$2,'2026-11-19','prior',1,1,1,0,0,500000,'csv',$4)`, provider, first, second, reportImportActor)
	now := time.Date(2026, 11, 20, 4, 0, 0, 0, beijing)
	seedVerifiedKeyDay(t, ctx, tx, first, 41, "2026-11-18", now, "unknown",
		`{"requests":0,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"actual_cost":0}`, `[]`)
	seedVerifiedKeyDay(t, ctx, tx, first, 41, "2026-11-20", now, "CNY",
		`{"requests":3,"actual_cost":0.37}`, `[{"model":"matched","actual_cost":0.33},{"model":"one-sided","actual_cost":0.04}]`)
	period, _ := reportPeriod("day", now)
	usage, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Totals.Credits != "40.60000000000000000001" || usage.Totals.Tokens != 505 || usage.Totals.Calls != 6 ||
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
	local, upstream := view.Totals.Local, view.Totals.Upstream
	if local.Tokens != usage.Totals.Tokens || local.Requests != usage.Totals.Calls ||
		local.InputTokens != 350 || local.OutputTokens != 45 || local.CacheReadTokens != 90 || local.CacheWriteTokens != 20 ||
		local.TokensMissing != 0 || local.ChargedTokensMissingCalls != 1 || local.Completeness != "partial" ||
		upstream.Tokens != 273 || upstream.Requests != 4 ||
		view.Totals.Money.StudentYuan != "0.4060000000000000000001" || view.Totals.Money.BaseYuan != "0.37" ||
		reportDecimal(view.Totals.Money.ActualYuan) != "0.38" || view.Totals.Money.MarginAvailable ||
		view.Totals.Money.MarginYuan != nil || view.Totals.Money.MarginRate != nil {
		t.Fatalf("金额或 Token 汇总错误: %+v", view.Totals)
	}
	if view.Coverage.ExpectedDays != 2 || view.Coverage.SyncedDays != 1 || view.Coverage.ComparableDays != 0 ||
		strings.Join(view.Coverage.MissingDays, ",") != "2026-11-20" ||
		view.Coverage.PendingCalls != 1 || view.Coverage.UnpricedCalls != 1 || !view.Coverage.MultiplierMixed ||
		view.Coverage.CoverageKind != "partial" || view.Coverage.VerifiedMatchedCalls != 0 {
		t.Fatalf("覆盖情况错误: %+v", view.Coverage)
	}
	matchedChannels := make(map[uuid.UUID]bool)
	for _, row := range view.Models {
		if row.MarginAvailable || row.MarginYuan != nil || row.MarginRate != nil {
			t.Errorf("区间缺账日时模型差额须为空: %+v", row)
		}
		switch row.Model {
		case "matched":
			wantTokens := map[uuid.UUID]int64{first: 220, second: 13}[row.CredentialID]
			wantEstimate := map[uuid.UUID]int64{first: 240, second: 16}[row.CredentialID]
			if row.MatchStatus != "matched" || row.Completeness != "complete" || row.Label == nil || wantTokens == 0 ||
				row.Local.Tokens != wantTokens || row.Upstream.Tokens != wantTokens ||
				row.Local.EstimatedMicros == nil || *row.Local.EstimatedMicros != wantEstimate {
				t.Errorf("两边均有记录的模型归一化错误: %+v", row)
			}
			matchedChannels[row.CredentialID] = true
			checkReportRatio(t, row.LocalShare, wantTokens, 505)
			checkReportRatio(t, row.UpstreamShare, wantTokens, 273)
		case "pending", "missing":
			if row.MatchStatus != "local_only" || row.ActualYuan != nil ||
				(row.Model == "missing" && (row.Local.Tokens != 110 || row.Local.ChargedTokensMissingCalls != 1 ||
					row.Local.Completeness != "partial")) {
				t.Errorf("本地模型状态错误: %+v", row)
			}
		case "one-sided":
			switch row.CredentialID {
			case first:
				if row.MatchStatus != "upstream_only" || row.LocalShare != "0" || reportDecimal(row.ActualYuan) != "0.04" {
					t.Errorf("上游独有模型错误: %+v", row)
				}
			case second:
				if row.MatchStatus != "local_only" || row.ActualYuan != nil || row.Local.Tokens != 52 {
					t.Errorf("本地独有模型错误: %+v", row)
				}
			default:
				t.Errorf("同名模型归属错误: %+v", row)
			}
		default:
			t.Errorf("跨日混入模型: %+v", row)
		}
	}
	if len(view.Models) != 6 || len(matchedChannels) != 2 || len(view.Keys) != 2 || view.Totals.Local.EstimatedMicros != nil {
		t.Fatalf("模型、渠道或缺价估算错误: %+v", view)
	}
	for _, row := range view.Keys {
		if row.Label == nil || row.MarginAvailable || row.MarginYuan != nil || row.MarginRate != nil ||
			(row.CredentialID == first && row.Local.Tokens != 440) || (row.CredentialID == second && row.Local.Tokens != 65) {
			t.Errorf("渠道聚合错误: %+v", row)
		}
		if row.CredentialID == first && reportDecimal(row.ActualYuan) != "0.37" || row.CredentialID == second && reportDecimal(row.ActualYuan) != "0.01" {
			t.Errorf("渠道扣款重复累计: %+v", row)
		}
	}
	for _, value := range []string{"month", "all"} {
		period, _ = reportPeriod(value, now)
		view, err = readReconciliation(ctx, tx, provider, period, now)
		if err != nil || view.Coverage.ExpectedDays != 4 || view.Coverage.SyncedDays != 2 || view.Coverage.ComparableDays != 1 ||
			view.Totals.Money.MarginAvailable ||
			strings.Join(view.Coverage.MissingDays, ",") != "2026-11-19,2026-11-20" || reportDecimal(view.Totals.Money.ActualYuan) != "0.88" {
			t.Fatalf("%s 覆盖情况错误: %+v, err=%v", value, view, err)
		}
	}
	seedVerifiedKeyDay(t, ctx, tx, first, 41, "2026-11-19", now, "CNY",
		`{"requests":1,"actual_cost":0.5}`, `[{"model":"prior","actual_cost":0.5}]`)
	seedVerifiedKeyDay(t, ctx, tx, second, 42, "2026-11-20", now, "CNY",
		`{"requests":1,"actual_cost":0.01}`, `[{"model":"matched","actual_cost":0.01}]`)
	view, err = readReconciliation(ctx, tx, provider, period, now)
	if err != nil || view.Coverage.ComparableDays != 3 || len(view.Coverage.MissingDays) != 0 ||
		!view.Totals.Money.MarginAvailable || reportDecimal(view.Totals.Money.MarginYuan) != "-0.4739999999999999999999" ||
		view.Totals.Money.MarginRate == nil {
		t.Fatalf("补齐账日后应恢复差额: %+v, err=%v", view, err)
	}
	// 凭据删除不删除已归档账单，本地存在 provider_id 的调用也仍可追溯。
	execTestSQL(t, ctx, tx, "DELETE FROM provider_credentials WHERE id=$1", second)
	period, _ = reportPeriod("day", now)
	view, err = readReconciliation(ctx, tx, provider, period, now)
	if err != nil || len(view.Keys) != 2 || view.Coverage.ComparableDays != 0 ||
		strings.Join(view.Coverage.MissingDays, ",") != "2026-11-20" || view.Totals.Money.MarginAvailable {
		t.Fatalf("历史凭据记录丢失: %+v, err=%v", view, err)
	}
}

func TestReportsPostgresEmptyResults(t *testing.T) {
	ctx, tx := reconciliationTestTransaction(t)
	provider := uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, beijing)
	period, _ := reportPeriod("all", now)
	usage, err := readModelUsage(ctx, tx, &provider, period)
	if err != nil || usage.Totals.Credits != "0" || usage.Totals.PendingReviewCredits != "0" ||
		usage.Models == nil || usage.Students == nil || usage.Keys == nil || usage.Totals.CacheHitRate != nil ||
		usage.Totals.TokensMissing != 0 || usage.Totals.TokenMissingCalls != 0 || usage.Totals.Completeness != "missing" {
		t.Fatalf("空用量应返回零和空数组: %+v, err=%v", usage, err)
	}
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil || view.Totals.Money.MarginAvailable || view.Totals.Money.ActualYuan != nil ||
		view.Totals.Money.MarginRate != nil || view.Totals.Money.MarginYuan != nil || view.Coverage.ComparableDays != 0 ||
		view.Models == nil || view.Keys == nil || view.Coverage.MissingDays == nil ||
		view.Coverage.Credentials == nil || view.Totals.Local.EstimatedMicros != nil ||
		view.Coverage.CoverageKind != "none" || view.Coverage.VerifiedMatchedCalls != 0 {
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
	ctx, tx := reconciliationTestTransaction(t)
	provider, first, second, zero := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials VALUES
 ($1,$4,'已知渠道','2026-11-20 00:00+08'),($2,$4,'未知渠道','2026-11-20 00:00+08'),($3,$4,'零扣款渠道','2026-11-20 00:00+08')`, first, second, zero, provider)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros,source,actor_id)
 VALUES($1,$2,'2026-11-20','partial',1,10,1,0,0,5000,'csv',$5),
 ($1,$3,'2026-11-20','partial',1,10,1,0,0,NULL,'csv',$5),
 ($1,$3,'2026-11-20','unknown',1,10,1,0,0,NULL,'csv',$5),
 ($1,$4,'2026-11-20','zero',1,0,0,0,0,0,'csv',$5)`, provider, first, second, zero, reportImportActor)
	call := reportCallFixture{provider: provider, credential: first, project: uuid.New(), user: uuid.New(), key: uuid.New(),
		model: "partial", protocol: "anthropic", state: "charged", usage: "reported", credits: "11", base: "10", multiplier: "1.1",
		sent: "2026-11-20T00:10:00+08:00", prompt: 10, output: 1, read: 0, write: 0}
	insertReportCall(t, ctx, tx, call)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	seedVerifiedKeyDay(t, ctx, tx, first, 41, "2026-11-20", now, "CNY",
		`{"requests":1,"actual_cost":0.005}`, `[{"model":"partial","actual_cost":0.005}]`)
	seedVerifiedKeyDay(t, ctx, tx, second, 42, "2026-11-20", now, "CNY",
		`{"requests":2,"actual_cost":null}`, `[{"model":"partial"},{"model":"unknown"}]`)
	seedVerifiedKeyDay(t, ctx, tx, zero, 43, "2026-11-20", now, "CNY",
		`{"requests":1,"actual_cost":0}`, `[{"model":"zero","actual_cost":0}]`)
	period, _ := reportPeriod("day", now)
	view, err := readReconciliation(ctx, tx, provider, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Coverage.ExpectedDays != 3 || view.Coverage.SyncedDays != 3 || view.Coverage.ComparableDays != 1 ||
		view.Coverage.CoverageKind != "full" || view.Totals.Upstream.BilledMicros != nil || view.Totals.Money.MarginAvailable ||
		view.Totals.Money.ActualYuan != nil || view.Totals.Money.MarginYuan != nil || view.Totals.Money.MarginRate != nil {
		t.Fatalf("覆盖完整但成本缺失时不能伪造零实扣或利润: %+v", view)
	}
	for _, row := range view.Models {
		if row.Model == "zero" {
			if !row.MarginAvailable || reportDecimal(row.ActualYuan) != "0" || reportDecimal(row.MarginYuan) != "0" || reportDecimal(row.MarginRate) != "0" {
				t.Errorf("明确零实扣必须保留: %+v", row)
			}
		} else if row.CredentialID == first {
			if !row.MarginAvailable || reportDecimal(row.ActualYuan) != "0.005" || reportDecimal(row.MarginYuan) != "0.105" {
				t.Errorf("已知金额的渠道模型应可比较: %+v", row)
			}
		} else if row.Upstream.BilledMicros != nil || row.ActualYuan != nil || row.MarginAvailable || row.MarginYuan != nil || row.MarginRate != nil {
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
