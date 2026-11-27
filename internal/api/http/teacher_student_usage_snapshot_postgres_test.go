package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestTeacherStudentUsageSnapshotHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "teacher")
	const month = "2025-01-01 00:00:00+08"
	f.exec(t, `INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,multiplier_milli,credit_milli,
		billing_state,outcome,created_at,prompt_tokens,completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,
		cache_write_tokens,usage_state)
		SELECT gen_random_uuid(),'fixture-'||n,$1,md5('org')::uuid,md5('project-'||(u%24))::uuid,md5('student-'||u)::uuid,
			md5('key-'||u)::uuid,md5('model')::uuid,'School','Project','Student '||u,'Key','Model',
			'openai_responses','/fixture',1000,100,'charged','succeeded',$1::timestamptz + n*interval '1 second',
			100,25,125,20,80,0,'reported'
		FROM generate_series(0,99999) n CROSS JOIN LATERAL (SELECT n%1500+1 AS u) students`, month)
	f.exec(t, `INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_milli_remainder,
		charged_count,created_at,updated_at)
		SELECT gen_random_uuid(),month,organization_id,project_id,user_id,api_key_id,model_id,
			max(organization_name),max(project_name),max(user_name),max(api_key_name),max(model_name),sum(credit_milli),
			CASE user_id WHEN md5('student-1')::uuid THEN 0.0000000000001
				WHEN md5('student-2')::uuid THEN 0.0000000000002 ELSE 0 END,
			count(*),month,month
		FROM gateway_calls GROUP BY month,organization_id,project_id,user_id,api_key_id,model_id`)
	f.exec(t, `INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,
		channel,request_id,model,occurred_at,billing_type,actual_cost,cost_details)
		SELECT 1,n,42,md5('provider')::uuid,md5('credential')::uuid,'Gemini','archive-'||n,'Model',$1,0,0.001,'{}'::jsonb
		FROM generate_series(1,50000) n`, month)
	f.exec(t, `ANALYZE gateway_calls, monthly_usage_cube, beichen_upstream_usage_rows`)

	const route = "/api/v1/teacher/usage/students?month=2025-01"
	get := func(query string) (usage.TeacherStudentUsagePage, int, time.Duration) {
		t.Helper()
		f.trace.take()
		start := time.Now()
		w := f.get(route+query, token)
		elapsed := time.Since(start)
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("unexpected HTTP cache policy: %v", w.Header())
		}
		reportQueries := 0
		for _, statement := range f.trace.take() {
			if strings.Contains(statement, "WITH ledger AS") || strings.Contains(statement, "FROM beichen_upstream_usage_rows") {
				reportQueries++
			}
		}
		return readUsageHTTPData[usage.TeacherStudentUsagePage](t, w), reportQueries, elapsed
	}
	first, coldReads, coldTime := get("&page_size=1")
	if coldReads != 2 || first.Total != 1500 || len(first.Items) != 1 || first.Items[0].UserName != "Student 2" ||
		first.Items[0].Credits != "6.7000000000000002" || first.Items[0].TotalTokens == nil || *first.Items[0].TotalTokens != "8375" ||
		first.UnattributedUpstream.Rows != 50000 || first.UnattributedUpstream.BalanceDebit == nil ||
		*first.UnattributedUpstream.BalanceDebit != "50" {
		t.Fatalf("cold snapshot mismatch (%d report queries): %+v", coldReads, first)
	}
	f.exec(t, `UPDATE monthly_usage_cube SET charged_milli=charged_milli+1000 WHERE user_id=md5('student-1')::uuid`)
	f.exec(t, `UPDATE beichen_upstream_usage_rows SET actual_cost=2.001 WHERE row_id=1`)
	second, secondReads, secondTime := get("&page_size=1&page=2")
	if secondReads != 0 || second.Total != 1500 || len(second.Items) != 1 || second.Items[0].UserName != "Student 1" ||
		second.Items[0].Credits != "6.7000000000000001" || second.UnattributedUpstream.BalanceDebit == nil ||
		*second.UnattributedUpstream.BalanceDebit != "50" {
		t.Fatalf("page 2 did not use the same snapshot (%d report queries): %+v", secondReads, second)
	}
	pastEnd, pastEndReads, pastEndTime := get("&page_size=100&page=2147483647")
	if pastEndReads != 0 || pastEnd.Total != 1500 || pastEnd.Items == nil || len(pastEnd.Items) != 0 ||
		pastEnd.UnattributedUpstream.BalanceDebit == nil || *pastEnd.UnattributedUpstream.BalanceDebit != "50" {
		t.Fatalf("out-of-range page mismatch (%d report queries): %+v", pastEndReads, pastEnd)
	}
	t.Logf("100,000 calls / 50,000 archived rows: cold=%s (%d report reads), page 2=%s (%d), beyond last page=%s (%d)",
		coldTime, coldReads, secondTime, secondReads, pastEndTime, pastEndReads)

	deadline := time.Now().Add(7 * time.Second)
	for {
		refreshed, reads, _ := get("&page_size=1&search=STUDENT+1")
		if reads != 0 {
			if reads != 2 || len(refreshed.Items) != 1 || refreshed.Items[0].UserName != "Student 1" ||
				refreshed.Items[0].Credits != "7.7000000000000001" || refreshed.UnattributedUpstream.BalanceDebit == nil ||
				*refreshed.UnattributedUpstream.BalanceDebit != "52" {
				t.Fatalf("expired snapshot did not refresh both datasets: %+v", refreshed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the snapshot did not expire")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTeacherStudentUsageTimeoutHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "teacher")
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(f.ctx, `LOCK TABLE gateway_calls IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	const route = "/api/v1/teacher/usage/students?month=2025-01"
	// Stop the test itself if the report does not enforce a shorter deadline.
	requestCtx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	blocked := *f
	blocked.ctx = requestCtx
	w := blocked.get(route, token)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) ||
		requestCtx.Err() != nil {
		t.Fatalf("report should time out before the HTTP test deadline: status=%d body=%s", w.Code, w.Body)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	page := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route, token))
	if page.Total != 0 || page.Items == nil || len(page.Items) != 0 || page.UnattributedUpstream.Rows != 0 {
		t.Fatalf("report did not recover after releasing the lock: %+v", page)
	}
}

func TestTeacherStudentUsageOverCacheLimitHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "teacher")
	f.exec(t, `INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_count,created_at,updated_at)
		SELECT gen_random_uuid(),'2025-01-01 00:00:00+08',md5('org')::uuid,md5('project')::uuid,
			md5('student-'||n)::uuid,md5('key-'||n)::uuid,md5('model')::uuid,
			'School','Project','Student '||n,'Key','Model',n,1,now(),now()
		FROM generate_series(1,10001) n`)
	const route = "/api/v1/teacher/usage/students?month=2025-01"
	first := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route+"&page_size=1", token))
	if first.Total != 10001 || len(first.Items) != 1 || first.Items[0].Credits != "10.001" {
		t.Fatalf("cache budget must not truncate the report: %+v", first)
	}
	filtered := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route+"&search=STUDENT+10001&page=2", token))
	if filtered.Total != 1 || filtered.Items == nil || len(filtered.Items) != 0 {
		t.Fatalf("filtered total beyond last page: %+v", filtered)
	}
}
