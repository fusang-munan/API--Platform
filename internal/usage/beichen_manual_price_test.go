package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBeichenManualBilling(t *testing.T, ctx context.Context, db *sql.DB, org, student, model, provider, credential uuid.UUID) {
	t.Helper()
	exec := func(q string, args ...any) { t.Helper(); ledgerTestExec(t, ctx, db, q, args...) }
	actor, project, key := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'手动价教师','manual-billing@example.test','fixture','teacher',now(),now())`, actor)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
VALUES($1,$2,'手动价测试',2000000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
VALUES(gen_random_uuid(),$1,$2,2000000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
VALUES($1,$2,$3,'手动价 Key','active','manual-test-hash','fixture',now(),now(),now())`, key, project, student)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, key, model)
	for _, name := range []string{"manual-priced", "manual-follow", "manual-later", "manual-fallback"} {
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,$2,'upstream','effective','billing',now()-interval '1 hour',1000000,2000000,500000,250000)`, credential, name)
	}
	manual := func(name string, free, missingWrite bool) {
		t.Helper()
		input, output, read, write := int64(200000000), int64(400000000), int64(50000000), int64(25000000)
		if free {
			input, output, read, write = 0, 0, 0, 0
		}
		var cacheWrite *int64 = &write
		if missingWrite {
			cacheWrite = nil
		}
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,$2,'school','manual','teacher',$3,$4,$5,$6,$7)`, credential, name, actor, input, output, read, cacheWrite)
	}
	manual("manual-priced", false, false)
	manual("manual-only", false, false)
	manual("manual-free", true, false)
	manual("manual-fallback", false, true)
	store := NewStore(db)
	reserve := func(name, protocol string, row int64) (Reservation, CallRoute, BeichenReceipt) {
		t.Helper()
		route := CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: name}
		r, err := store.Reserve(ctx, key, model, uuid.NewString(), protocol, "/v1/responses", "", "", CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, r.CallID, route); err != nil {
			t.Fatal(err)
		}
		response := uuid.New()
		if err := store.RecordBeichenReceiptID(ctx, r.CallID, credential, response); err != nil {
			t.Fatal(err)
		}
		route.SentAt = time.Now().UTC()
		return r, route, BeichenReceipt{ResponseID: response, RowID: row, APIKeyID: 92, Model: name, ActualCost: "0.9", OccurredAt: route.SentAt,
			InputTokens: 90000, OutputTokens: 10000, CacheReadTokens: 20000, CacheWriteTokens: 10000}
	}
	finish := func(r Reservation, route CallRoute, metrics CallMetrics) {
		t.Helper()
		if err := store.Finalize(ctx, r, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
	}
	check := func(r Reservation, state, base, source, charged string) {
		t.Helper()
		var gotState, gotBase, gotSource, gotCharged string
		err := db.QueryRowContext(ctx, `SELECT beichen_state,coalesce(trim_scale(beichen_base_credits)::text,''),
coalesce(beichen_base_source,''),coalesce(trim_scale(beichen_credit_exact)::text,'') FROM gateway_calls WHERE id=$1`, r.CallID).
			Scan(&gotState, &gotBase, &gotSource, &gotCharged)
		if err != nil || gotState != state || gotBase != base || gotSource != source || gotCharged != charged {
			t.Fatalf("账目错误: state=%s base=%s source=%s charged=%s; want %s/%s/%s/%s, %v", gotState, gotBase, gotSource, gotCharged, state, base, source, charged, err)
		}
	}
	input, output, read, write := int64(120000), int64(10000), int64(20000), int64(10000)
	report := CallMetrics{State: UsageReported, PromptTokens: &input, CompletionTokens: &output, CacheHitTokens: &read, CacheWriteTokens: &write}
	for i, tc := range []struct{ name, protocol, base, source, charge string }{
		{"manual-follow", "openai_responses", "12.25", "gateway_usage_price", "13.475"},
		{"manual-priced", "openai_responses", "23.25", "teacher_manual_price", "25.575"},
		{"manual-only", "openai_responses", "23.25", "teacher_manual_price", "25.575"},
		{"manual-free", "openai_responses", "0", "teacher_manual_price", "0"},
		{"manual-fallback", "openai_responses", "12.25", "teacher_manual_price", "13.475"},
		{"manual-priced", "anthropic", "23.25", "teacher_manual_price", "25.575"},
	} {
		t.Run(tc.name+"/"+tc.protocol, func(t *testing.T) {
			r, route, _ := reserve(tc.name, tc.protocol, int64(800+i))
			if tc.protocol == "anthropic" {
				input = 90000
			} else {
				input = 120000
			}
			finish(r, route, report)
			finish(r, route, report)
			check(r, "charged", tc.base, tc.source, tc.charge)
			if tc.name == "manual-follow" {
				var same bool
				err := db.QueryRowContext(ctx, `SELECT c.beichen_base_credits=beichen_token_credits(c.protocol,c.prompt_tokens,c.completion_tokens,
c.cache_hit_tokens,c.cache_miss_tokens,c.cache_write_tokens,p.upstream_input,p.upstream_output,p.upstream_cache_read,p.upstream_cache_write)
FROM gateway_calls c CROSS JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,c.beichen_price_at) p WHERE c.id=$1`, r.CallID).Scan(&same)
				if err != nil || !same {
					t.Fatalf("跟随模式与旧公式不一致: %t, %v", same, err)
				}
			}
		})
	}
	input = 120000
	missing, route, _ := reserve("manual-no-price", "openai_responses", 810)
	finish(missing, route, report)
	check(missing, "pending_review", "", "", "")
	var reason string
	if err := db.QueryRowContext(ctx, "SELECT beichen_review_reason FROM gateway_calls WHERE id=$1", missing.CallID).Scan(&reason); err != nil || reason != "missing_upstream_price" {
		t.Fatalf("缺价未保持待核账语义: %s, %v", reason, err)
	}
	for i, name := range []string{"manual-priced", "manual-only"} {
		t.Run("回执/"+name, func(t *testing.T) {
			r, route, bill := reserve(name, "openai_responses", int64(820+i))
			finish(r, route, CallMetrics{})
			applied, err := store.ApplyBeichenReceipt(ctx, credential, bill)
			if err != nil || !applied {
				t.Fatalf("回执补记失败: %t, %v", applied, err)
			}
			check(r, "charged", "23.25", "teacher_manual_price", "25.575")
			if applied, err := store.ApplyBeichenReceipt(ctx, credential, bill); err != nil || applied {
				t.Fatalf("回执重复扣费: %t, %v", applied, err)
			}
		})
	}
	t.Run("回执优先使用完整网关用量", func(t *testing.T) {
		r, route, bill := reserve("manual-only", "openai_responses", 830)
		finish(r, route, CallMetrics{})
		exec(`UPDATE gateway_calls SET usage_state='reported',prompt_tokens=120000,completion_tokens=10000,
cache_hit_tokens=20000,cache_write_tokens=10000 WHERE id=$1`, r.CallID)
		bill.InputTokens, bill.OutputTokens, bill.CacheReadTokens, bill.CacheWriteTokens = 1, 0, 0, 0
		if applied, err := store.ApplyBeichenReceipt(ctx, credential, bill); err != nil || !applied {
			t.Fatalf("回执补记失败: %t, %v", applied, err)
		}
		check(r, "charged", "23.25", "teacher_manual_price", "25.575")
	})
	t.Run("手动价格不追溯早先派发及已经入账", func(t *testing.T) {
		old, oldRoute, bill := reserve("manual-later", "openai_responses", 840)
		manual("manual-later", false, false)
		finish(old, oldRoute, report)
		check(old, "charged", "12.25", "gateway_usage_price", "13.475")
		fresh, freshRoute, _ := reserve("manual-later", "openai_responses", 841)
		finish(fresh, freshRoute, report)
		check(fresh, "charged", "23.25", "teacher_manual_price", "25.575")
		manual("manual-later", true, false)
		finish(fresh, freshRoute, report)
		check(fresh, "charged", "23.25", "teacher_manual_price", "25.575")
		if applied, err := store.ApplyBeichenReceipt(ctx, credential, bill); err != nil || applied {
			t.Fatalf("历史回执不应重新扣费: %t, %v", applied, err)
		}
		check(old, "charged", "12.25", "gateway_usage_price", "13.475")
	})
	var balanced bool
	err := db.QueryRowContext(ctx, `SELECT (SELECT sum(beichen_credit_exact) FROM gateway_calls WHERE project_id=$1)=
(SELECT (charged_milli+charged_milli_remainder)*0.001 FROM project_month_credit_buckets WHERE project_id=$1)`, project).Scan(&balanced)
	if err != nil || !balanced {
		t.Fatalf("逐笔扣费与月桶不一致: %t, %v", balanced, err)
	}
}
