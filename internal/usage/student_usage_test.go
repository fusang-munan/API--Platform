package usage

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStudentUsageContractPostgres(t *testing.T) {
	ctx, db := newBeichenLedgerTestDB(t)
	store := NewStore(db)
	exec := func(query string, args ...any) {
		t.Helper()
		ledgerTestExec(t, ctx, db, query, args...)
	}
	student, otherStudent, org := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES
		($1,'Student','usage-student@example.test','fixture','student',now(),now()),
		($2,'Other','usage-other@example.test','fixture','student',now(),now())`, student, otherStudent)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Usage school',now(),now())`, org)
	month := Month(time.Date(2024, 2, 10, 0, 0, 0, 0, shanghai))
	nextMonth := time.Date(2024, 2, 29, 16, 0, 0, 0, time.UTC) // March 1 at midnight in Beijing

	type scope struct{ student, project, key uuid.UUID }
	makeKey := func(student, project uuid.UUID, name, status string, pending int64) scope {
		t.Helper()
		key := uuid.New()
		exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
			VALUES($1,$2,$3,200000,now(),now()) ON CONFLICT(id) DO NOTHING`, project, org, name)
		exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
			VALUES(gen_random_uuid(),$1,$2,100000,now())`, project, student)
		exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,revoked_at,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5::text,digest($1::uuid::text,'sha256'),'fixture',now(),
			    CASE WHEN $5::text='inactive' THEN now() END,now(),now())`, key, project, student, name, status)
		exec(`INSERT INTO project_month_credit_buckets(id,project_id,month,quota_milli,pending_milli,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,200000,$3,now(),now()) ON CONFLICT(project_id,month) DO NOTHING`, project, month, pending)
		exec(`INSERT INTO project_member_month_credit_buckets(id,project_id,user_id,month,quota_milli,pending_milli,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,100000,$4,now(),now())`, project, student, month, pending)
		exec(`INSERT INTO api_key_month_credit_buckets(id,api_key_id,project_id,month,quota_milli,pending_milli,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,100000,$4,now(),now())`, key, project, month, pending)
		return scope{student, project, key}
	}
	active := makeKey(student, uuid.New(), "A", "active", 250)
	second := makeKey(student, uuid.New(), "B", "active", 0)
	inactive := makeKey(student, uuid.New(), "C", "inactive", 0)
	other := makeKey(otherStudent, active.project, "Other", "active", 0)

	makeModel := func(name string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
			VALUES($1,$2::text,$2::text,'active',0,now(),now())`, id, name)
		return id
	}
	textModel, partialModel, freeModel, archivedModel := makeModel("text"), makeModel("partial"), makeModel("free"), makeModel("archived")
	type observation struct {
		prompt, completion, total, read, miss, write, reasoning, tool any
		state, reason                                                 string
	}
	addCall := func(owner scope, model uuid.UUID, protocol, billing, outcome, beichenState string, exposure int64, ledgerMonth, createdAt time.Time, o observation) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,
			multiplier_milli,credit_milli,billing_state,outcome,unresolved_credit_milli,
			beichen_dispatch_kind,beichen_state,beichen_review_reason,created_at,prompt_tokens,
			completion_tokens,total_tokens,cache_hit_tokens,cache_miss_tokens,cache_write_tokens,reasoning_tokens,
			tool_prompt_tokens,usage_state,usage_reason)
			VALUES($1,$1::uuid::text,$2,$3,$4,$5,$6,$7,'School','Project','Student','Key','Historical name',$8,'/fixture',
			    0,0,$9,$10,$11,CASE WHEN $12::text<>'' THEN 'beichen' END,NULLIF($12,''),
			    CASE WHEN $12='pending_review' THEN 'fixture' END,$13,$14,$15,$16,$17,$18,$19,$20,$21,
			    NULLIF($22,''),NULLIF($23,''))`,
			id, ledgerMonth, org, owner.project, owner.student, owner.key, model, protocol, billing, outcome, exposure,
			beichenState, createdAt, o.prompt, o.completion, o.total, o.read, o.miss, o.write, o.reasoning, o.tool, o.state, o.reason)
		return id
	}
	book := func(call uuid.UUID, credits string) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := bookBeichenCharge(ctx, tx, call, beichenCharge{base: credits, source: "receipt_actual_cost"}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	addFreeHistory := func(owner scope, model uuid.UUID) {
		t.Helper()
		exec(`INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,zero_cost_count,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,'School','Project','Student','Key','Historical name',1,$1,$1)`,
			month, org, owner.project, owner.student, owner.key, model)
	}
	openAI := observation{prompt: 100, completion: 40, total: 140, read: 20, miss: 70, write: 10, state: "reported"}
	anthropic := observation{prompt: 30, completion: 10, total: 100, read: 40, write: 20, state: "reported"}
	// The first call was admitted in February and persisted just over the boundary.
	book(addCall(active, textModel, "openai_responses", "pending", "succeeded", "pending_review", 0, month, nextMonth, openAI),
		"0.00040000000000001")
	book(addCall(second, textModel, "anthropic", "pending", "succeeded", "pending_review", 0, month, month, anthropic),
		"0.00040000000000002")
	pending := addCall(active, partialModel, "openai_compatible", "pending", "succeeded", "pending_review", 10, month, month,
		observation{prompt: 100, completion: 4, total: 130, state: "partial", reason: "incomplete_token_breakdown"})
	addCall(inactive, freeModel, "google_gemini_v1beta", "charged", "succeeded", "", 0, month, month,
		observation{prompt: 100, completion: 20, reasoning: 4, tool: 3, read: 30, total: 127, state: "partial"})
	addFreeHistory(inactive, freeModel)
	addFreeHistory(second, archivedModel) // The cube survives raw-call retention.
	addCall(active, textModel, "openai_responses", "pending", "pending", "reserved", 0, month, month, openAI)
	addCall(active, textModel, "anthropic", "pending", "succeeded", "pending_review", 100, nextMonth, nextMonth, anthropic)
	addCall(other, textModel, "anthropic", "pending", "succeeded", "pending_review", 100, month, month, anthropic)

	read := func() map[string]any {
		t.Helper()
		result, err := store.StudentUsage(ctx, student, month)
		if err != nil {
			t.Fatal(err)
		}
		return studentUsageJSON(t, result)
	}
	before := read()
	checkStudentUsageFields(t, before, map[string]any{"month": "2024-02"})
	projects := before["projects"]
	projectA := studentUsageRow(t, projects, "project_id", active.project.String())
	keyA := studentUsageRow(t, projectA["keys"], "id", active.key.String())
	for _, row := range []map[string]any{projectA, keyA} {
		checkStudentUsageFields(t, row, map[string]any{
			"used": "0.00040000000000001", "available": "99.74959999999999999",
			"pending": "0.250", "in_flight_credits": "0.250", "pending_review": true,
			"calls": 1.0, "pending_review_calls": 1.0, "pending_review_credits": "10.000",
		})
	}
	checkStudentUsageFields(t, keyA, map[string]any{"pending_calls": 1.0})
	modelA := studentUsageRow(t, keyA["models"], "id", "text")
	checkStudentUsageFields(t, modelA, map[string]any{
		"completeness": "complete", "calls": 1.0,
		"tokens": map[string]any{"input": "70", "output": "40", "cache_read": "20", "cache_write": "10", "total": "140"},
	})
	partial := studentUsageRow(t, keyA["models"], "id", "partial")
	checkStudentUsageFields(t, partial, map[string]any{
		"credits": "0.000", "completeness": "partial", "cache_hit_rate": nil,
		"pending_review_calls": 1.0, "pending_review_credits": "10.000",
		"tokens":         map[string]any{"input": nil, "output": "4", "cache_read": nil, "cache_write": nil, "total": "130"},
		"token_coverage": map[string]any{"calls": 1.0, "known_calls": 1.0, "complete_calls": 0.0, "unclassified_tokens": "126"},
	})
	if _, exists := partial["calls"]; exists {
		t.Fatalf("pending-only calls included in the legacy settled count: %#v", partial)
	}
	projectC := studentUsageRow(t, projects, "project_id", inactive.project.String())
	keyC := studentUsageRow(t, projectC["keys"], "id", inactive.key.String())
	checkStudentUsageFields(t, keyC, map[string]any{"status": "inactive", "calls": 1.0, "used": "0.000", "completeness": "partial"})
	free := studentUsageRow(t, keyC["models"], "id", "free")
	checkStudentUsageFields(t, free, map[string]any{
		"credits": "0.000", "calls": 1.0, "completeness": "partial",
		"tokens":             map[string]any{"input": "73", "output": "24", "cache_read": "30", "cache_write": nil, "total": "127"},
		"cache_hit_coverage": map[string]any{"sample_calls": 1.0, "cache_read_tokens": "30", "input_with_cache_read_tokens": "103"},
	})
	archived := studentUsageRow(t, before["models"], "id", "archived")
	checkStudentUsageFields(t, archived, map[string]any{
		"credits": "0.000", "calls": 1.0, "completeness": "unknown", "cache_hit_rate": nil,
		"tokens":         map[string]any{"input": nil, "output": nil, "cache_read": nil, "cache_write": nil, "total": nil},
		"token_coverage": map[string]any{"calls": 1.0, "known_calls": 0.0, "complete_calls": 0.0, "unclassified_tokens": nil},
	})

	text := studentUsageRow(t, before["models"], "id", "text")
	checkStudentUsageFields(t, text, map[string]any{
		"credits": "0.00080000000000003", "calls": 2.0, "completeness": "complete", "cache_hit_rate": 0.375,
		"tokens":             map[string]any{"input": "100", "output": "50", "cache_read": "60", "cache_write": "30", "total": "240"},
		"cache_hit_coverage": map[string]any{"sample_calls": 2.0, "cache_read_tokens": "60", "input_with_cache_read_tokens": "160"},
	})
	totals := before["totals"].(map[string]any)
	checkStudentUsageFields(t, totals, map[string]any{
		"credits": "0.00080000000000003", "calls": 4.0, "pending_review_calls": 1.0, "pending_review_credits": "10.000",
		"tokens":         map[string]any{"input": "173", "output": "78", "cache_read": "90", "cache_write": "30", "total": "497"},
		"token_coverage": map[string]any{"calls": 5.0, "known_calls": 4.0, "complete_calls": 2.0, "unclassified_tokens": "126"},
	})
	receipt, err := store.StudentCallReceipt(ctx, student, pending.String())
	if err != nil {
		t.Fatal(err)
	}
	checkStudentUsageFields(t, studentUsageJSON(t, receipt), map[string]any{
		"billing_state": "pending_review", "model_id": "partial", "model_name": "Historical name",
		"created_at": month.Format(time.RFC3339), "input_tokens": nil, "output_tokens": "4", "total_tokens": "130",
		"usage_reason": "incomplete_token_breakdown",
	})

	book(pending, "0.00040000000000001")
	after := read()
	updatedTotals := after["totals"].(map[string]any)
	checkStudentUsageFields(t, updatedTotals, map[string]any{
		"credits": "0.00120000000000004", "calls": 5.0, "pending_review_calls": 0.0, "pending_review_credits": "0.000",
		"tokens": totals["tokens"], "token_coverage": totals["token_coverage"],
	})
	updatedProject := studentUsageRow(t, after["projects"], "project_id", active.project.String())
	updatedKey := studentUsageRow(t, updatedProject["keys"], "id", active.key.String())
	checkStudentUsageFields(t, updatedKey, map[string]any{
		"used": "0.00080000000000002", "pending": "0.250", "in_flight_credits": "0.250",
		"pending_calls": 0.0, "pending_review_calls": 0.0, "pending_review_credits": "0.000",
	})
	if _, exists := updatedKey["pending_review"]; exists {
		t.Fatalf("legacy review flag survived settlement: %#v", updatedKey)
	}
	checkStudentUsageFields(t, studentUsageRow(t, updatedKey["models"], "id", "partial"), map[string]any{
		"credits": "0.00040000000000001", "calls": 1.0, "pending_review_calls": 0.0, "pending_review_credits": "0.000",
	})

	empty, err := store.AggregateStudentUsage(ctx, uuid.New(), month)
	if err != nil {
		t.Fatal(err)
	}
	checkStudentUsageFields(t, studentUsageJSON(t, empty), map[string]any{"projects": []any{}, "models": []any{}})
	checkStudentUsageFields(t, studentUsageJSON(t, empty.Totals), map[string]any{"credits": "0.000", "calls": 0.0, "completeness": "unknown"})
}

func studentUsageJSON(t *testing.T, result any) map[string]any {
	t.Helper()
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func studentUsageRow(t *testing.T, value any, field, id string) map[string]any {
	t.Helper()
	rows, ok := value.([]any)
	if !ok {
		t.Fatalf("expected an array for %s=%s, got %#v", field, id, value)
	}
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row[field] == id {
			return row
		}
	}
	t.Fatalf("missing %s=%s in %#v", field, id, rows)
	return nil
}

func checkStudentUsageFields(t *testing.T, got, want map[string]any) {
	t.Helper()
	for name, value := range want {
		if actual, exists := got[name]; !exists || !reflect.DeepEqual(value, actual) {
			t.Errorf("%s = %#v (present %t), want %#v; row=%#v", name, actual, exists, value, got)
		}
	}
}
