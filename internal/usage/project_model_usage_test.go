package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testProjectReceiptAttribution(t *testing.T, ctx context.Context, db *sql.DB, org, project, student, key, model uuid.UUID) {
	exec := func(query string, args ...any) {
		t.Helper()
		ledgerTestExec(t, ctx, db, query, args...)
	}
	month := Month(time.Date(2024, 2, 10, 0, 0, 0, 0, shanghai))
	second, secondKey, pendingModel, credential := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES
		($1,'Zed','zed@example.test','fixture','student',now(),now())`, second)
	exec(`INSERT INTO project_members(id,project_id,user_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, project, second)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$2,$3,'Zed key','active','second-test-key','fixture',now(),now(),now())`, secondKey, project, second)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'pending','Pending','active',1000,now(),now())`, pendingModel)
	exec(`INSERT INTO project_month_credit_buckets(id,project_id,month,quota_milli,charged_milli,charged_milli_remainder,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,10000,3300,0.00000000001,now(),now())`, project, month)
	exec(`INSERT INTO api_key_month_credit_buckets(id,api_key_id,project_id,month,quota_milli,created_at,updated_at)
		SELECT gen_random_uuid(),id,$3,$4,0,now(),now() FROM api_keys WHERE id IN ($1,$2)`, key, secondKey, project, month)
	exec(`INSERT INTO monthly_usage_cube(id,month,organization_id,project_id,user_id,api_key_id,model_id,
		organization_name,project_name,user_name,api_key_name,model_name,charged_milli,charged_milli_remainder,charged_count,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,'Org','Project','Student','Key','Old name',1300,0.00000000001,4,now(),now()),
		(gen_random_uuid(),$1,$2,$3,$7,$8,$6,'Org','Project','Zed','Zed key','New name',2000,0,1,now(),now())`,
		month, org, project, student, key, model, second, secondKey)
	exec(`INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,billed_upstream_micros,source)
		VALUES(gen_random_uuid(),$1,'2024-02-01','model',999999,'usage')`, credential)

	rowID := 1000
	addCall := func(p, user, k, m uuid.UUID, period time.Time, kind, state, actual string) uuid.UUID {
		t.Helper()
		call, response := uuid.New(), uuid.New()
		exec(`INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,
			multiplier_milli,credit_milli,beichen_dispatch_kind,beichen_state,beichen_credit_exact,
			beichen_review_reason,beichen_receipt_response_id,created_at)
			VALUES($1,'fixture',$2,$3,$4,$5,$6,$7,'School','Project','Member','Key','Model','openai_responses','/v1/responses',
			0,0,$8,$9,CASE WHEN $9='charged' THEN 1 END,CASE WHEN $9='pending_review' THEN 'fixture' END,$10,$2)`,
			call, period, org, p, user, k, m, kind, state, response)
		if actual != "" {
			rowID++
			exec(`INSERT INTO beichen_receipt_response_claims(response_id,first_call_id) VALUES($1,$2)`, response, call)
			// A receipt imported the following month still belongs to the call's ledger month.
			exec(`INSERT INTO beichen_call_receipts(row_id,call_id,credential_id,response_id,api_key_id,model,occurred_at,actual_cost,
				input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,credits_booked,applied)
				VALUES($1,$2,$3,$4,42,'upstream-alias',$5, $6,1,0,0,0,1,false)`,
				rowID, call, credential, response, month.AddDate(0, 1, 0), actual)
		}
		return call
	}
	addCall(project, student, key, model, month, "beichen", "charged", "0.00014352")
	addCall(project, student, key, model, month, "beichen", "charged", "0.00000000000000000001")
	addCall(project, student, key, model, month, "beichen", "charged", "")
	addCall(project, student, key, pendingModel, month, "beichen", "pending_review", "")
	addCall(project, student, key, model, month, "legacy", "released", "")
	addCall(project, second, secondKey, model, month, "beichen", "charged", "0")
	collision := addCall(project, second, secondKey, model, month, "beichen", "charged", "8")
	exec(`UPDATE beichen_receipt_response_claims SET conflicted_at=now(),conflicting_call_id=gen_random_uuid() WHERE first_call_id=$1`, collision)
	addCall(project, student, key, model, month.AddDate(0, 1, 0), "beichen", "charged", "1000")
	addCall(uuid.New(), student, key, model, month, "beichen", "charged", "1000")

	got, err := NewStore(db).ProjectUsage(ctx, project, month)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || len(got.Models) != 2 || got.Charged != "3.30000000000001" {
		t.Fatalf("unexpected project totals: %+v", got)
	}
	main, other := got.Members[0], got.Members[1]
	if main.UserID != student.String() || other.UserID != second.String() ||
		len(main.Keys) != 1 || len(main.Keys[0].Models) != 2 ||
		len(other.Keys) != 1 || len(other.Keys[0].Models) != 1 {
		t.Fatalf("unexpected member keys/models: %+v", got.Members)
	}
	check := func(spend BeichenSpend, expected string, missing int64) {
		t.Helper()
		actual := "<null>"
		if spend.ActualCost != nil {
			actual = *spend.ActualCost
		}
		if actual != expected || spend.UnmatchedCalls != missing {
			t.Errorf("actual=%s unmatched=%d, want %s and %d", actual, spend.UnmatchedCalls, expected, missing)
		}
	}
	const exact = "0.00014352000000000001"
	check(main.BeichenSpend, exact, 2)
	check(main.Keys[0].Models[0].BeichenSpend, exact, 1)
	check(main.Keys[0].Models[1].BeichenSpend, "<null>", 1)
	check(other.BeichenSpend, "0", 1)
	check(other.Keys[0].Models[0].BeichenSpend, "0", 1)
	check(got.Models[0].BeichenSpend, exact, 2)
	check(got.Models[1].BeichenSpend, "<null>", 1)
	if main.Credits != "1.30000000000001" || other.Credits != "2.000" || got.Models[0].Credits != got.Charged ||
		got.Models[0].ID != "model" || got.Models[0].Calls != 5 || got.Models[1].ID != "pending" || got.Models[1].Credits != "0.000" {
		t.Fatalf("incorrect school credits or models: %+v", got)
	}
}
