package beichen

import (
	"context"
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type modelUsagePostgresFixture struct {
	ctx                                      context.Context
	db                                       *sql.DB
	provider, actor, org, project, user, key uuid.UUID
	credentials                              []uuid.UUID
}

func newModelUsagePostgresFixture(t *testing.T) modelUsagePostgresFixture {
	t.Helper()
	ctx, db, provider, actor, credentials := newActionsPostgres(t)
	f := modelUsagePostgresFixture{ctx: ctx, db: db, provider: provider, actor: actor, credentials: credentials,
		org: uuid.New(), project: uuid.New(), user: uuid.New(), key: uuid.New()}
	f.exec(t, `INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'学生','usage@example.test','fixture','student',now(),now())`, f.user)
	f.exec(t, `INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'学校',now(),now())`, f.org)
	f.exec(t, `INSERT INTO projects(id,organization_id,name,created_at,updated_at) VALUES($1,$2,'项目',now(),now())`, f.project, f.org)
	f.exec(t, `INSERT INTO project_members(id,project_id,user_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, f.project, f.user)
	f.exec(t, `INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
VALUES($1,$2,$3,'Key','active',digest($1::uuid::text,'sha256'),'fixture',now(),now(),now())`, f.key, f.project, f.user)
	return f
}

func (f modelUsagePostgresFixture) exec(t *testing.T, stmt string, args ...any) {
	t.Helper()
	execTestSQL(t, f.ctx, f.db, stmt, args...)
}

func (f modelUsagePostgresFixture) schoolModel(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(t, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2,'校内显示名','active',0,now(),now())`, id, name)
	return id
}

type modelUsageFact struct {
	schoolModel, provider, credential uuid.UUID
	upstreamModel, state, credits     string
	admitted, dispatched              time.Time
	input                             *int
	reservedCredits                   int64
}

func (f modelUsagePostgresFixture) addCalls(t *testing.T, count int, call modelUsageFact) {
	t.Helper()
	f.exec(t, `WITH fact AS (
 SELECT $11::text AS state, NULLIF($12,'')::numeric AS credits, $13::int AS input, $8::timestamptz AS dispatched
)
INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
 organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,multiplier_milli,
 credit_milli,billing_state,outcome,created_at,sent_at,finalized_at,
 provider_id,beichen_dispatch_kind,beichen_dispatch_at,beichen_dispatch_credential_id,beichen_dispatch_model,
 beichen_price_at,beichen_state,beichen_credit_exact,beichen_review_reason,unresolved_credit_milli,
 usage_state,prompt_tokens,completion_tokens,cache_hit_tokens,cache_write_tokens)
SELECT gen_random_uuid(),gen_random_uuid()::text,$1,$2,$3,$4,$5,$6,
 '学校','项目','学生','Key','派发时的显示名','anthropic','/v1/messages',0,
 COALESCE(trunc(credits * 1000),0),
 CASE state WHEN 'charged' THEN 'charged' WHEN 'released' THEN 'failed' ELSE 'pending' END,
 CASE WHEN state = 'released' THEN 'upstream_failed' ELSE 'succeeded' END,
 $7,dispatched,dispatched,$9,'beichen',dispatched,$10,$14,
 dispatched,state,credits,CASE WHEN state = 'pending_review' THEN 'missing_price' END,$15,
 CASE WHEN input IS NULL THEN 'missing' ELSE 'reported' END,input,
 CASE WHEN input IS NOT NULL THEN 0 END,CASE WHEN input IS NOT NULL THEN 0 END,CASE WHEN input IS NOT NULL THEN 0 END
FROM fact CROSS JOIN generate_series(1,$16::int)`,
		usage.Month(call.admitted), f.org, f.project, f.user, f.key, call.schoolModel, call.admitted, call.dispatched,
		call.provider, call.credential, call.state, call.credits, call.input, call.upstreamModel, call.reservedCredits, count)
}

func (f modelUsagePostgresFixture) report(t *testing.T, provider *uuid.UUID, now time.Time) ModelUsageReport {
	t.Helper()
	report, err := New(f.db, nil).ModelUsage(f.ctx, provider, "month", now)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestModelUsageSettlementAndLedgerPostgres(t *testing.T) {
	f := newModelUsagePostgresFixture(t)
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, beijing)
	dispatch := november.Add(12 * time.Hour)
	zero := 0
	activeModels := []struct {
		name, rate, credits string
		settled, input      int
		zeroCost, missing   int
		tokens              int64
	}{
		{"claude-fable-5.1", "0.125", "1.75", 15, 10, 1, 0, 140},
		{"claude-opus-5-5", "0.25", "3.75", 15, 11, 0, 1, 154},
		{"deepseek-v4.1-flash", "0.375", "5.625", 15, 12, 0, 0, 180},
		{"gemini-3.8-flash", "0.5", "7.5", 15, 13, 0, 0, 195},
		{"glm-5.3-flash", "0.625", "9.375", 15, 14, 0, 0, 210},
		{"gpt-6.1-terra", "0.75", "11.25", 15, 15, 0, 0, 225},
		{"gpt-6.2-astra", "0.875", "13.125", 15, 16, 0, 0, 240},
		{"gpt-6.2-sol", "1", "17", 17, 17, 0, 0, 289},
	}
	schoolIDs := make(map[string]uuid.UUID, len(activeModels))
	for _, model := range activeModels {
		id := f.schoolModel(t, model.name)
		schoolIDs[model.name] = id
		call := modelUsageFact{schoolModel: id, provider: f.provider, credential: f.credentials[0],
			upstreamModel: model.name, state: "charged", credits: model.rate, admitted: dispatch, dispatched: dispatch, input: &model.input}
		f.addCalls(t, model.settled-model.zeroCost-model.missing, call)
		if model.zeroCost != 0 {
			free := call
			free.credits, free.input = "0", &zero
			f.addCalls(t, model.zeroCost, free)
		}
		if model.missing != 0 {
			call.input = nil
			f.addCalls(t, model.missing, call)
		}
		f.exec(t, `INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
 organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_count,zero_cost_count,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,'学校','项目','学生','Key',$7,$8::text::numeric*1000,$9,$10,$1,$1)`,
			usage.Month(november), f.org, f.project, f.user, f.key, id, model.name, model.credits, model.settled-model.zeroCost, model.zeroCost)
	}
	released := modelUsageFact{schoolModel: schoolIDs[activeModels[0].name], provider: f.provider, credential: f.credentials[0],
		upstreamModel: activeModels[0].name, state: "released", admitted: dispatch, dispatched: dispatch}
	f.addCalls(t, 4, released)
	pending := released
	pending.state, pending.input, pending.reservedCredits = "pending_review", &zero, 17
	f.addCalls(t, 1, pending)

	checkTotals := func(pendingCalls int64, pendingCredits string) ModelUsageReport {
		t.Helper()
		report := f.report(t, &f.provider, dispatch)
		if len(report.Students) != 1 || len(report.Keys) != 1 || len(report.Models) != len(activeModels) ||
			!reflect.DeepEqual(report.Students[0].Models, report.Models) || !reflect.DeepEqual(report.Keys[0].Models, report.Models) {
			t.Fatalf("模型、学生和 Key 汇总不一致: %+v", report)
		}
		for _, got := range []ModelUsageMetrics{report.Totals, report.Students[0].ModelUsageMetrics, report.Keys[0].ModelUsageMetrics} {
			if got.Calls != 127 || got.DispatchedCalls != 127 || got.SettledCalls != 122 || got.ZeroCostCalls != 1 ||
				got.PendingCalls != pendingCalls || got.PendingReviewCalls != pendingCalls || got.PendingReviewCredits != pendingCredits ||
				got.Credits != "69.375" || got.Tokens != 1633 || got.TokensMissing != 0 || got.TokenMissingCalls != 5 ||
				got.ChargedTokensMissingCalls != 1 || got.Completeness != "partial" {
				t.Fatalf("汇总口径错误: %+v", got)
			}
		}
		return report
	}
	checkTotals(1, "17")
	f.exec(t, `UPDATE gateway_calls SET beichen_state='released',billing_state='failed',outcome='upstream_failed',
 beichen_review_reason=NULL,unresolved_credit_milli=0 WHERE beichen_state='pending_review'`)
	report := checkTotals(0, "0")
	for i, model := range activeModels {
		got := report.Models[i]
		dispatched := int64(model.settled)
		if i == 0 {
			dispatched += 5
		}
		if got.Model != model.name || got.SchoolModelID == nil || *got.SchoolModelID != schoolIDs[model.name] ||
			got.Calls != dispatched || got.DispatchedCalls != dispatched ||
			got.SettledCalls != int64(model.settled) || got.ZeroCostCalls != int64(model.zeroCost) ||
			got.Credits != model.credits || got.Tokens != model.tokens {
			t.Errorf("%s 的积分、Token 或结算次数发生变化: %+v", model.name, got)
		}
	}

	store := usage.NewStore(f.db)
	student, err := store.AggregateStudentUsage(f.ctx, f.user, november)
	if err != nil || student.Totals.Calls != 122 || student.Totals.Credits != report.Totals.Credits ||
		student.Totals.Tokens.Total == nil || *student.Totals.Tokens.Total != "1633" || student.Totals.PendingReviewCalls != 0 {
		t.Fatalf("同月学生账本未与教师的已结算次数和用量对齐: %+v, %v", student.Totals, err)
	}

	// 受理在旧月，派发恰好在新月 00:00。结算继续更新 11 月账本。
	december := november.AddDate(0, 1, 0)
	crossMonth := released
	crossMonth.state, crossMonth.credits, crossMonth.input = "charged", "0.125", &zero
	crossMonth.admitted, crossMonth.dispatched = december.Add(-time.Second), december
	f.addCalls(t, 1, crossMonth)
	f.exec(t, `UPDATE monthly_usage_cube SET charged_count=charged_count+1,charged_milli=charged_milli+125
WHERE month=$1 AND model_id=$2`, usage.Month(november), crossMonth.schoolModel)
	checkTotals(0, "0")
	decemberReport := f.report(t, &f.provider, december)
	if got := decemberReport.Totals; got.DispatchedCalls != 1 || got.SettledCalls != 1 || got.Credits != "0.125" ||
		got.Tokens != 0 || got.TokenMissingCalls != 0 || got.Completeness != "complete" {
		t.Fatalf("北京时间边界或观测到零 Token 的口径错误: %+v", got)
	}
	for _, month := range []struct {
		start   time.Time
		settled int64
	}{{november, 123}, {december, 0}} {
		student, err := store.AggregateStudentUsage(f.ctx, f.user, month.start)
		if err != nil || student.Totals.Calls != month.settled {
			t.Fatalf("受理账月 %s: %+v, %v", month.start, student.Totals, err)
		}
	}
}

func TestModelUsageIdentityPostgres(t *testing.T) {
	f := newModelUsagePostgresFixture(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	const dotted, dashed = "claude-fable-5.1", "claude-fable-5-1"
	current, retired, orphan := f.schoolModel(t, dotted), f.schoolModel(t, dashed), uuid.New()
	oldKey, newKey := f.credentials[0], f.credentials[1]
	binding := uuid.New()
	f.exec(t, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,'anthropic',now(),now())`, binding, current, f.provider, oldKey, dashed)
	add := func(modelID, provider, credential uuid.UUID, name string, sent time.Time, tokens int) {
		t.Helper()
		f.addCalls(t, 1, modelUsageFact{schoolModel: modelID, provider: provider, credential: credential, upstreamModel: name,
			state: "charged", credits: "1", admitted: sent, dispatched: sent, input: &tokens})
	}
	add(current, f.provider, oldKey, dashed, now.Add(-2*time.Hour), 11)
	add(retired, f.provider, oldKey, dashed, now.Add(-time.Hour), 12)
	f.exec(t, `UPDATE model_bindings SET credential_id=$2,upstream_model_name=$3 WHERE id=$1`, binding, newKey, dotted)
	f.exec(t, `UPDATE models SET status='inactive' WHERE id=$1`, retired)
	f.exec(t, `UPDATE provider_credentials SET status='inactive',retired_at=$2 WHERE id=$1`, oldKey, now)
	add(current, f.provider, newKey, dotted, now, 13)
	f.exec(t, `UPDATE gateway_calls SET binding_id=$1 WHERE model_id=$2`, binding, current)
	add(orphan, f.provider, oldKey, "retired-no-catalog", now, 14)
	add(orphan, f.provider, oldKey, "retired-no-catalog-alias", now, 15)

	f.exec(t, `INSERT INTO upstream_model_aliases(provider_id,credential_id,upstream_model,school_model,created_by)
VALUES($1,$2,$4,$5,$6),($1,$3,'legacy-name',$5,$6),($1,NULL,'legacy-name',$4,$6)`,
		f.provider, oldKey, newKey, dashed, dotted, f.actor)
	add(uuid.Nil, f.provider, oldKey, "legacy-name", now.Add(-3*time.Hour), 16)
	add(uuid.Nil, f.provider, newKey, "LEGACY-NAME", now.Add(-3*time.Hour), 17)
	f.exec(t, `UPDATE gateway_calls SET provider_id=NULL WHERE beichen_dispatch_model='LEGACY-NAME'`)
	add(uuid.Nil, f.provider, newKey, dashed, now, 18)
	add(uuid.Nil, f.provider, newKey, dotted, now, 19)
	add(uuid.Nil, f.provider, newKey, "claude-fable-5.2", now, 20)

	otherProvider, otherKey := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO providers(id,name,base_url,created_at,updated_at)
VALUES($1,'北辰外部渠道','https://api.beichenapi.com/v1',now(),now())`, otherProvider)
	f.exec(t, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'外部 Key','fixture',now(),now())`, otherKey, otherProvider)
	add(uuid.Nil, otherProvider, otherKey, "legacy-name", now, 21)
	add(uuid.Nil, otherProvider, otherKey, dotted, now, 22)

	fallbackKey := func(provider, credential uuid.UUID, name string) string {
		return "upstream:" + provider.String() + ":" + credential.String() + ":" + name
	}
	type expectedModel struct {
		key, label, schoolName string
		id                     uuid.UUID
		tokens, calls          int64
		sources, upstream      []string
	}
	want := []expectedModel{
		{"school:" + current.String(), dotted, dotted, current, 41, 3, []string{"call_model_id", "configured_alias"}, []string{dashed, dotted, "LEGACY-NAME"}},
		{"school:" + retired.String(), dashed, dashed, retired, 28, 2, []string{"call_model_id", "configured_alias"}, []string{dashed, "legacy-name"}},
		{"school:" + orphan.String(), "retired-no-catalog", "", orphan, 29, 2, []string{"call_model_id"}, []string{"retired-no-catalog", "retired-no-catalog-alias"}},
		{fallbackKey(f.provider, newKey, dashed), dashed, "", uuid.Nil, 18, 1, []string{"upstream_name"}, []string{dashed}},
		{fallbackKey(f.provider, newKey, dotted), dotted, "", uuid.Nil, 19, 1, []string{"upstream_name"}, []string{dotted}},
		{fallbackKey(f.provider, newKey, "claude-fable-5.2"), "claude-fable-5.2", "", uuid.Nil, 20, 1, []string{"upstream_name"}, []string{"claude-fable-5.2"}},
	}
	all := append(slices.Clone(want),
		expectedModel{fallbackKey(otherProvider, otherKey, "legacy-name"), "legacy-name", "", uuid.Nil, 21, 1, []string{"upstream_name"}, []string{"legacy-name"}},
		expectedModel{fallbackKey(otherProvider, otherKey, dotted), dotted, "", uuid.Nil, 22, 1, []string{"upstream_name"}, []string{dotted}})
	for _, scope := range []struct {
		provider *uuid.UUID
		want     []expectedModel
	}{{&f.provider, want}, {nil, all}} {
		report := f.report(t, scope.provider, now)
		modelRows := make(map[string]ModelUsageItem)
		for _, row := range report.Models {
			modelRows[row.ModelKey] = row
		}
		if len(report.Models) != len(scope.want) || len(modelRows) != len(scope.want) ||
			len(report.Students) != 1 || len(report.Keys) != 1 || !reflect.DeepEqual(report.Models, report.Students[0].Models) ||
			!reflect.DeepEqual(report.Models, report.Keys[0].Models) {
			t.Fatalf("身份分组丢失、重复或明细口径不一致: %+v", report)
		}
		for _, expected := range scope.want {
			row := modelRows[expected.key]
			// PostgreSQL 的区域设置可以影响原名的排序。
			if row.Model != expected.label || row.Tokens != expected.tokens || row.Calls != expected.calls ||
				!slices.Equal(row.IdentitySources, expected.sources) ||
				!slices.Equal(slices.Sorted(slices.Values(row.UpstreamModels)), slices.Sorted(slices.Values(expected.upstream))) {
				t.Errorf("身份 %s 的派发证据或用量错误: %+v", expected.key, row)
			}
			if expected.id == uuid.Nil && row.SchoolModelID != nil ||
				expected.id != uuid.Nil && (row.SchoolModelID == nil || *row.SchoolModelID != expected.id) ||
				expected.schoolName == "" && row.SchoolModel != nil ||
				expected.schoolName != "" && (row.SchoolModel == nil || *row.SchoolModel != expected.schoolName) {
				t.Errorf("身份 %s 的校内模型证据错误: %+v", expected.key, row.ModelUsageIdentity)
			}
		}
	}
}

func TestModelUsageFallbackSeparatesChannelsPostgres(t *testing.T) {
	f := newModelUsagePostgresFixture(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	tokens := 10
	for _, call := range []struct {
		credential uuid.UUID
		credits    string
		tokens     int
	}{
		{f.credentials[0], "1", 10},
		{f.credentials[1], "2", 20},
	} {
		tokens = call.tokens
		f.addCalls(t, 1, modelUsageFact{schoolModel: uuid.Nil, provider: f.provider, credential: call.credential,
			upstreamModel: "same-upstream", state: "charged", credits: call.credits,
			admitted: now, dispatched: now, input: &tokens})
	}
	// 0032 之前的旧账没有北辰精确扣分，只有已结算的千分积分。
	f.exec(t, `UPDATE gateway_calls SET beichen_state=NULL,beichen_credit_exact=NULL,billing_state='charged'
WHERE beichen_dispatch_model='same-upstream'`)
	report := f.report(t, &f.provider, now)
	if report.Totals.DispatchedCalls != 2 || report.Totals.SettledCalls != 2 ||
		report.Totals.Credits != "3" || report.Totals.Tokens != 30 || len(report.Models) != 2 {
		t.Fatalf("不同渠道的同名旧调用被错误合并: %+v", report)
	}
	want := map[uuid.UUID]struct {
		credits string
		tokens  int64
	}{
		f.credentials[0]: {"1", 10},
		f.credentials[1]: {"2", 20},
	}
	for _, model := range report.Models {
		parts := strings.SplitN(model.ModelKey, ":", 4)
		if len(parts) != 4 || parts[0] != "upstream" {
			t.Fatalf("旧记录回退键缺少稳定渠道身份: %s", model.ModelKey)
		}
		credential, err := uuid.Parse(parts[2])
		if err != nil || model.Model != "same-upstream" || model.Calls != 1 || model.SettledCalls != 1 ||
			model.Credits != want[credential].credits || model.Tokens != want[credential].tokens {
			t.Fatalf("逐渠道行错误: %+v, %v", model, err)
		}
		delete(want, credential)
	}
	if len(want) != 0 {
		t.Fatalf("缺少渠道行: %v", want)
	}
}
