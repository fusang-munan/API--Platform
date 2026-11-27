package usage

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestTeacherStudentUsagePostgres(t *testing.T) {
	ctx, db := newBeichenLedgerTestDB(t)
	store := NewStore(db)
	exec := func(query string, args ...any) {
		t.Helper()
		ledgerTestExec(t, ctx, db, query, args...)
	}
	month := Month(time.Date(2025, 1, 10, 0, 0, 0, 0, shanghai))
	next := month.In(shanghai).AddDate(0, 1, 0)
	late := next.Add(time.Second)
	students := []uuid.UUID{uuid.New(), uuid.New()}
	projects := []uuid.UUID{uuid.New(), uuid.New()}
	models := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	org := uuid.New()
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'School',now(),now())`, org)
	for i, id := range students {
		exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
			VALUES($1,'同名同学',$2,'fixture','student',now(),now())`, id, fmt.Sprintf("usage+%d@example.test", i))
		exec(`INSERT INTO student_profiles(id,user_id,created_at,updated_at) VALUES(gen_random_uuid(),$1,now(),now())`, id)
	}
	for i, id := range projects {
		exec(`INSERT INTO projects(id,organization_id,name,created_at,updated_at) VALUES($1,$2,$3,now(),now())`, id, org, fmt.Sprintf("项目 %d", i))
	}
	for i, id := range models {
		exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
			VALUES($1,$2::text,$2::text,'active',0,now(),now())`, id, fmt.Sprintf("model-%d", i))
	}

	type scope struct {
		student, project, key uuid.UUID
		keyName               string
	}
	addCube := func(s scope, model uuid.UUID, milli, paid, zero int, remainder string) {
		t.Helper()
		exec(`INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_milli_remainder,
			charged_count,zero_cost_count,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,'School','原项目名','原姓名',$7,'历史模型名',$8,$9::numeric,$10,$11,$1,$1)`,
			month, org, s.project, s.student, s.key, model, s.keyName, milli, remainder, paid, zero)
	}
	addCall := func(s scope, model uuid.UUID, ledgerMonth, createdAt time.Time, billing, outcome, state string,
		prompt, output, total int, hit, miss, write any, usageState string, pending int) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,
			multiplier_milli,credit_milli,billing_state,outcome,beichen_state,beichen_review_reason,created_at,
			prompt_tokens,completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,cache_write_tokens,
			usage_state,unresolved_credit_milli)
			VALUES($1,$1::uuid::text,$2,$3,$4,$5,$6,$7,'School','原项目名','原姓名',$8,'历史模型名',
				'openai_responses','/fixture',0,0,$9,$10,NULLIF($11::text,''),
				CASE WHEN $11='pending_review' THEN 'fixture' END,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			id, ledgerMonth, org, s.project, s.student, s.key, model, s.keyName,
			billing, outcome, state, createdAt, prompt, output, total, hit, miss, write, usageState, pending)
		return id
	}

	var first scope
	var receiptCall, conflictCall uuid.UUID
	var historicalKey uuid.UUID
	for i, student := range students {
		for j, project := range projects {
			exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
				VALUES(gen_random_uuid(),$1,$2,100000,now())`, project, student)
			for k := 0; k < 2; k++ {
				s := scope{student: student, project: project, key: uuid.New(), keyName: fmt.Sprintf("Key %d%d%d", i, j, k)}
				if k == 0 {
					status := "active"
					if i == 0 {
						status = "inactive"
					}
					exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,revoked_at,created_at,updated_at)
						VALUES($1,$2,$3,$4,$5::text,digest($1::uuid::text,'sha256'),'fixture',now(),
							CASE WHEN $5::text='inactive' THEN now() END,now(),now())`, s.key, project, student, s.keyName, status)
				}
				// The second Key is historical. Its report survives missing live metadata.
				if i == 0 && j == 0 && k == 0 {
					first = s
				}
				if i == 0 && j == 1 && k == 1 {
					historicalKey = s.key
				}
				for m, model := range models[:2] {
					firstLeaf := j == 0 && k == 0 && m == 0
					created := month.Add(time.Hour)
					if i == 0 && firstLeaf {
						addCube(s, model, 0, 0, 1, "0")
						receiptCall = addCall(s, model, month, late, "charged", "succeeded", "", 100, 40, 140, 20, 70, 10, "reported", 0)
					} else if i == 0 {
						addCube(s, model, 100, 1, 0, "0.00000000000001")
						if j != 1 || k != 1 || m != 1 { // Keep one ledger entry after raw-call retention.
							addCall(s, model, month, created, "charged", "succeeded", "", 100, 40, 140, 20, 70, 10, "reported", 0)
						}
					} else {
						paid, remainder := 1, "0.000000000000001"
						if firstLeaf {
							paid, remainder = 2, "0.000000000000002"
							conflictCall = addCall(s, model, month, created, "charged", "succeeded", "", 2, 1, 3, 0, 2, 0, "reported", 0)
						}
						addCube(s, model, 50*paid, paid, 0, remainder)
						addCall(s, model, month, created, "charged", "succeeded", "", 2, 1, 3, 0, 2, 0, "reported", 0)
					}
				}
			}
		}
	}
	pending := addCall(first, models[2], month, month, "pending", "succeeded", "pending_review", 100, 4, 130, nil, nil, nil, "partial", 7)
	addCall(first, models[2], month, late.Add(time.Hour), "pending", "pending", "reserved", 100, 40, 140, 20, 70, 10, "reported", 0)
	addCall(first, models[2], next, next, "pending", "succeeded", "pending_review", 100, 40, 140, 20, 70, 10, "reported", 30)
	exec(`UPDATE users SET status='disabled' WHERE id=$1`, students[0])
	exec(`UPDATE student_profiles SET status='disabled' WHERE user_id=$1`, students[0])
	exec(`UPDATE users SET role='mentor' WHERE id=$1`, students[1])
	exec(`DELETE FROM project_members WHERE user_id=$1 AND project_id=$2`, students[0], projects[1])

	credential, provider := uuid.New(), uuid.New()
	responses := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	exec(`INSERT INTO beichen_receipt_response_claims(response_id,first_call_id) VALUES($1,$2),($3,$4)`,
		responses[0], pending, responses[1], receiptCall)
	exec(`INSERT INTO beichen_receipt_response_claims(response_id,first_call_id,conflicting_call_id,conflicted_at)
		VALUES($1,$2,$3,now())`, responses[2], conflictCall, uuid.New())
	for i, call := range []uuid.UUID{pending, receiptCall, conflictCall} {
		exec(`UPDATE gateway_calls SET beichen_receipt_response_id=$2 WHERE id=$1`, call, responses[i])
	}
	exec(`UPDATE gateway_calls SET beichen_dispatch_kind='beichen',beichen_dispatch_credential_id=$2,
		beichen_dispatch_model='upstream-model',beichen_receipt_api_key_id=42,sent_at=$3,finalized_at=$3
		WHERE id IN ($1,$4)`, pending, credential, month, conflictCall)
	exec(`INSERT INTO beichen_call_receipts(row_id,call_id,credential_id,response_id,api_key_id,model,occurred_at,
		actual_cost,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,credits_booked,applied)
		VALUES(4,$1,$2,$3,42,'upstream-model',$4,1,70,40,20,10,0,false)`, receiptCall, credential, responses[1], month)
	archive := func(rowID int, requestID, model string, occurred time.Time, billingType int, amount string) {
		t.Helper()
		exec(`INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,
			channel,request_id,model,occurred_at,billing_type,actual_cost,cost_details)
			VALUES(1,$1,42,$2,$3,'Gemini',$4,$5,$6,$7,$8::numeric,'{}'::jsonb)`,
			rowID, provider, credential, requestID, model, occurred, billingType, amount)
	}
	archive(1, "website-request", "upstream-model", month, 0, "0.000000000000000003")
	archive(2, "client:invalid", "upstream-model", month, 1, "2")
	archive(3, "client:"+responses[0].String(), "upstream-model", month, 0, "1")
	archive(4, "client:"+responses[1].String(), "upstream-model", month, 0, "1")
	archive(5, "client:"+responses[2].String(), "upstream-model", month, 0, "0")
	archive(6, "client:"+responses[0].String(), "different-model", month, 0, "0")
	archive(7, "next-month", "upstream-model", next, 0, "100")

	scopeFilter := TeacherStudentUsageScope{Month: month}
	query := TeacherStudentUsageQuery{TeacherStudentUsageScope: scopeFilter, Page: 1, PageSize: 20}
	list := func(q TeacherStudentUsageQuery) TeacherStudentUsagePage {
		t.Helper()
		page, err := store.ListTeacherStudentUsage(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := list(query)
	if page.Month != "2025-01" || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("expected two students in January: %+v", page)
	}
	for i, row := range page.Items {
		wantCalls, wantCredits, wantTokens, wantComplete := int64(8), "0.70000000000000007", "1110", "partial"
		if i == 1 {
			wantCalls, wantCredits, wantTokens, wantComplete = 9, "0.450000000000000009", "27", "complete"
		}
		checkStudentUsageFields(t, studentUsageJSON(t, row), map[string]any{
			"user_id": students[i].String(), "user_name": "同名同学", "user_email": fmt.Sprintf("usage+%d@example.test", i),
			"project_count": 2.0, "key_count": 4.0, "credits": wantCredits, "calls": float64(wantCalls),
			"total_tokens": wantTokens, "completeness": wantComplete,
		})
		detail, err := store.TeacherStudentUsage(ctx, row.UserID, scopeFilter)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Projects) != 2 || detail.Totals.Credits != row.Credits || detail.Totals.Calls != row.Calls ||
			!reflect.DeepEqual(detail.Totals.Tokens.Total, row.TotalTokens) || detail.Totals.Completeness != row.Completeness ||
			detail.Totals.PendingReviewCalls != row.PendingReviewCalls || detail.Totals.PendingReviewCredits != row.PendingReviewCredits ||
			detail.Totals.ZeroCostCalls != row.ZeroCostCalls {
			t.Fatalf("list/detail mismatch: %+v / %+v", row, detail.Totals)
		}
		assertTeacherUsageTree(t, detail)
		if i == 0 {
			a := studentUsageJSON(t, detail)
			projectA := studentUsageRow(t, a["projects"], "project_id", projects[0].String())
			projectB := studentUsageRow(t, a["projects"], "project_id", projects[1].String())
			keyA := studentUsageRow(t, projectA["keys"], "id", first.key.String())
			oldKey := studentUsageRow(t, projectB["keys"], "id", historicalKey.String())
			checkStudentUsageFields(t, projectA, map[string]any{"used": "0.30000000000000003", "zero_cost_calls": 1.0, "pending_review_credits": "7.000"})
			checkStudentUsageFields(t, projectB, map[string]any{"used": "0.40000000000000004", "zero_cost_calls": 0.0})
			checkStudentUsageFields(t, keyA, map[string]any{"used": "0.10000000000000001", "status": "inactive"})
			checkStudentUsageFields(t, oldKey, map[string]any{"name": "Key 011", "status": "unknown"})
			free := studentUsageRow(t, keyA["models"], "id", "model-0")
			checkStudentUsageFields(t, free, map[string]any{"credits": "0.000", "calls": 1.0, "zero_cost_calls": 1.0, "cache_hit_coverage": map[string]any{"sample_calls": 1.0, "cache_read_tokens": "20", "input_with_cache_read_tokens": "90"}})
			pendingModel := studentUsageRow(t, keyA["models"], "id", "model-2")
			checkStudentUsageFields(t, pendingModel, map[string]any{"credits": "0.000", "completeness": "partial", "pending_review_calls": 1.0, "pending_review_credits": "7.000"})
		}
	}
	rowA := page.Items[0]
	if rowA.LastCallAt == nil || !rowA.LastCallAt.Equal(late) || rowA.PendingReviewCalls != 1 ||
		rowA.PendingReviewCredits != "7.000" || rowA.ZeroCostCalls != 1 {
		t.Fatalf("unexpected latest call or settlement split: %+v", rowA)
	}
	checkStudentUsageFields(t, studentUsageJSON(t, page.UnattributedUpstream), map[string]any{
		"rows": 4.0, "debit_rows": 3.0, "balance_debit": "0.000000000000000003",
	})

	query.PageSize = 1
	query.Sort, query.Page = "calls", 1
	if got := list(query); got.Total != 2 || len(got.Items) != 1 || got.Items[0].UserID != students[1] {
		t.Fatalf("sort/page 1 by calls: %+v", got)
	}
	query.Page = 2
	if got := list(query); len(got.Items) != 1 || got.Items[0].UserID != students[0] {
		t.Fatalf("page 2: %+v", got)
	}
	query.Page = 99
	if got := list(query); got.Total != 2 || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("past the last page: %+v", got)
	}
	query.Page, query.Sort = 1, "tokens"
	if got := list(query); got.Items[0].UserID != students[0] {
		t.Fatalf("descending token sort: %+v", got)
	}
	query.Order = "asc"
	if got := list(query); got.Items[0].UserID != students[1] {
		t.Fatalf("ascending token sort: %+v", got)
	}
	query.Search, query.ProjectID = "USAGE+0@", uuid.NullUUID{UUID: projects[0], Valid: true}
	filtered := list(query)
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].UserID != students[0] ||
		filtered.Items[0].ProjectCount != 1 || filtered.Items[0].KeyCount != 2 || filtered.Items[0].Credits != "0.30000000000000003" ||
		!reflect.DeepEqual(filtered.UnattributedUpstream, page.UnattributedUpstream) {
		t.Fatalf("project and case-insensitive email scope: %+v", filtered)
	}
	detail, err := store.TeacherStudentUsage(ctx, students[0], query.TeacherStudentUsageScope)
	if err != nil || len(detail.Projects) != 1 || detail.Projects[0].ProjectID != projects[0].String() ||
		detail.Totals.Credits != filtered.Items[0].Credits {
		t.Fatalf("scoped detail: %+v, %v", detail, err)
	}
	query.Search = "%"
	if got := list(query); got.Total != 0 || len(got.Items) != 0 || got.Items == nil {
		t.Fatalf("search should treat %% as text: %+v", got)
	}
	query.Search, query.Month = "", next
	if got := list(query); got.Total != 1 || got.Items[0].Calls != 0 || got.Items[0].PendingReviewCredits != "30.000" ||
		got.UnattributedUpstream.Rows != 1 {
		t.Fatalf("separate February scope (pending only): %+v", got)
	}
	query.ProjectID.UUID = uuid.New()
	if _, err := store.TeacherStudentUsage(ctx, students[0], query.TeacherStudentUsageScope); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatalf("expected no detail for a different project: %v", err)
	}
}

type teacherUsageTestSums struct {
	credits, pending, tokens big.Rat
	known                    bool
	calls, zero, review      int64
}

func (s *teacherUsageTestSums) add(t *testing.T, credits string, calls, zero int64, m StudentUsageMetrics) {
	t.Helper()
	for target, value := range map[*big.Rat]string{&s.credits: credits, &s.pending: m.PendingReviewCredits} {
		item, ok := new(big.Rat).SetString(value)
		if !ok {
			t.Fatalf("invalid decimal %q", value)
		}
		target.Add(target, item)
	}
	if m.Tokens.Total != nil {
		tokens, ok := new(big.Rat).SetString(*m.Tokens.Total)
		if !ok {
			t.Fatalf("invalid token total %q", *m.Tokens.Total)
		}
		s.tokens.Add(&s.tokens, tokens)
		s.known = true
	}
	s.calls += calls
	s.zero += zero
	s.review += m.PendingReviewCalls
}

func (s *teacherUsageTestSums) check(t *testing.T, label, credits string, calls, zero int64, m StudentUsageMetrics) {
	t.Helper()
	var want teacherUsageTestSums
	want.add(t, credits, calls, zero, m)
	if s.credits.Cmp(&want.credits) != 0 || s.pending.Cmp(&want.pending) != 0 || s.tokens.Cmp(&want.tokens) != 0 ||
		s.known != want.known || s.calls != want.calls || s.zero != want.zero || s.review != want.review {
		t.Errorf("%s children do not sum to parent: credits=%s, pending=%s, tokens=%s, calls=%d, zero=%d, review=%d",
			label, s.credits.String(), s.pending.String(), s.tokens.String(), s.calls, s.zero, s.review)
	}
}

func assertTeacherUsageTree(t *testing.T, detail TeacherStudentUsageDetail) {
	t.Helper()
	var projects, overallModels teacherUsageTestSums
	for _, project := range detail.Projects {
		if len(project.Keys) != 2 {
			t.Fatalf("expected two historical Key IDs in %s, got %+v", project.ProjectID, project.Keys)
		}
		var keys teacherUsageTestSums
		for _, key := range project.Keys {
			if len(key.Models) < 2 {
				t.Fatalf("expected multiple models in Key %s, got %+v", key.ID, key.Models)
			}
			var models teacherUsageTestSums
			for _, model := range key.Models {
				models.add(t, model.Credits, model.Calls, model.ZeroCostCalls, model.StudentUsageMetrics)
			}
			models.check(t, key.Name, key.Used, key.Calls, key.ZeroCostCalls, key.StudentUsageMetrics)
			keys.add(t, key.Used, key.Calls, key.ZeroCostCalls, key.StudentUsageMetrics)
		}
		keys.check(t, project.ProjectName, project.Used, project.Calls, project.ZeroCostCalls, project.StudentUsageMetrics)
		projects.add(t, project.Used, project.Calls, project.ZeroCostCalls, project.StudentUsageMetrics)
	}
	for _, model := range detail.Models {
		overallModels.add(t, model.Credits, model.Calls, model.ZeroCostCalls, model.StudentUsageMetrics)
	}
	total := detail.Totals
	projects.check(t, "student projects", total.Credits, total.Calls, total.ZeroCostCalls, total.StudentUsageMetrics)
	overallModels.check(t, "student models", total.Credits, total.Calls, total.ZeroCostCalls, total.StudentUsageMetrics)
}
