package usage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func testSchoolBilling(t *testing.T, ctx context.Context, db *sql.DB, org, student, model, provider, credential uuid.UUID) {
	store := NewStore(db)
	exec := func(q string, args ...any) { t.Helper(); ledgerTestExec(t, ctx, db, q, args...) }
	teacher, project, key := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
		VALUES($1,'GM1',decode(repeat('12',32),'hex'),false)`, credential)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Teacher','school-teacher@example.test','fixture','teacher',now(),now())`, teacher)
	exec(`INSERT INTO student_profiles(id,user_id,created_at,updated_at) VALUES(gen_random_uuid(),$1,now(),now())`, student)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
		VALUES($1,$2,'Billing lab',200000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, project, student)
	if err := store.UpdateStudentQuotaAsTeacher(ctx, student, project, student, 100000, "fixture"); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatalf("student set quota: %v", err)
	}
	if err := store.UpdateStudentQuotaAsTeacher(ctx, teacher, project, teacher, 100000, "fixture"); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatalf("changed a non-student quota: %v", err)
	}
	if err := store.UpdateStudentQuotaAsTeacher(ctx, teacher, project, student, 201000, "fixture"); domain.ErrorCode(err) != domain.CodeProjectQuotaAllocation {
		t.Fatalf("exceeded project allocation: %v", err)
	}
	for range 2 {
		if err := store.UpdateStudentQuotaAsTeacher(ctx, teacher, project, student, 100000, "course"); err != nil {
			t.Fatal(err)
		}
	}
	var audits int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM quota_adjustment_audits WHERE project_id=$1 AND actor_user_id=$2
		AND owner_type='member' AND owner_id=$3 AND old_quota_milli=0 AND new_quota_milli=100000 AND reason='course'`, project, teacher, student).Scan(&audits)
	if err != nil || audits != 1 {
		t.Fatalf("quota audits=%d: %v", audits, err)
	}
	view, err := store.ProjectUsage(ctx, project, time.Now())
	if err != nil || view.Allocated != "100.000" || len(view.Members) != 1 || !view.Members[0].IsStudent || view.Members[0].Available != "100.000" {
		t.Fatalf("allocation before first key: %+v, %v", view, err)
	}

	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$2,$3,'Course key','active','school-billing-test','fixture',now(),now(),now())`, key, project, student)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, key, model)
	const priced = "school-billing-model"
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
		VALUES($1,$2,'upstream','effective','billing',now()-interval '1 hour',1000000,2000000,500000,0)`, credential, priced)
	reserve := func(upstream string, row int64) (Reservation, CallRoute, BeichenReceipt) {
		t.Helper()
		route := CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: upstream}
		r, err := store.Reserve(ctx, key, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, r.CallID, route); err != nil {
			t.Fatal(err)
		}
		id := uuid.New()
		if err := store.RecordBeichenReceiptID(ctx, r.CallID, credential, id); err != nil {
			t.Fatal(err)
		}
		route.SentAt = time.Now().UTC()
		return r, route, BeichenReceipt{ResponseID: id, RowID: row, APIKeyID: 92, Model: upstream, ActualCost: "0.1", InputTokens: 100000, OccurredAt: route.SentAt}
	}
	finish := func(r Reservation, route CallRoute, metrics CallMetrics) {
		t.Helper()
		if err := store.Finalize(ctx, r, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(bill BeichenReceipt, want bool) {
		t.Helper()
		applied, err := store.ApplyBeichenReceipt(ctx, credential, bill)
		if err != nil || applied != want {
			t.Fatalf("applied=%t want %t: %v", applied, want, err)
		}
	}
	check := func(r Reservation, base, source, charged, cost string) {
		t.Helper()
		got, err := store.StudentCallReceipt(ctx, student, r.RequestID)
		value := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		if err != nil || value(got.BaseCredits) != base || value(got.BaseSource) != source || value(got.SchoolMultiplier) != "1.10" ||
			value(got.ChargedCredits) != charged || value(got.ActualCost) != cost {
			t.Fatalf("base=%s source=%s factor=%s booked=%s actual=%s: %v", value(got.BaseCredits), value(got.BaseSource), value(got.SchoolMultiplier), value(got.ChargedCredits), value(got.ActualCost), err)
		}
	}
	available := func(want string) {
		t.Helper()
		v, err := store.StudentUsage(ctx, student, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range v.Projects {
			if p.ProjectID == project.String() {
				if p.Available != want {
					t.Fatalf("available=%s want %s", p.Available, want)
				}
				return
			}
		}
		t.Fatal("student project missing")
	}
	r, route, bill := reserve(priced, 710)
	zero, n := int64(0), int64(100000)
	report := CallMetrics{State: UsageReported, PromptTokens: &n, CompletionTokens: &zero, CacheHitTokens: &zero, CacheWriteTokens: &zero}
	finish(r, route, report)
	check(r, "10", "gateway_usage_price", "11", "")
	available("89.000")
	apply(bill, false)
	apply(bill, false)
	finish(r, route, report)
	check(r, "10", "gateway_usage_price", "11", "0.1")
	available("89.000")

	r, route, bill = reserve(priced, 711)
	finish(r, route, CallMetrics{})
	bill.ActualCost = "0.2" // The fixed quote would produce only 10 base credits.
	apply(bill, true)
	apply(bill, false)
	check(r, "20", "receipt_actual_cost", "22", "0.2")
	r, route, bill = reserve("no-price", 712)
	finish(r, route, CallMetrics{})
	check(r, "", "", "", "")
	bill.ActualCost = "0.000000000000001"
	apply(bill, true)
	check(r, "0.0000000000001", "receipt_actual_cost", "0.00000000000011", "0.000000000000001")
	if err := store.UpdateStudentQuotaAsTeacher(ctx, teacher, project, student, 33000, "too low"); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatalf("discarded fractional charge: %v", err)
	}
	view, err = store.ProjectUsage(ctx, project, time.Now())
	if err != nil || len(view.Models) != 1 || view.Models[0].BaseSource == nil || *view.Models[0].BaseSource != "mixed" ||
		view.Models[0].ChargedCredits == nil || *view.Models[0].ChargedCredits != "33.00000000000011" {
		t.Fatalf("report: %+v, %v", view, err)
	}

	// Both requests were admitted before the balance was exhausted.
	r, route, _ = reserve(priced, 713)
	late, lateRoute, _ := reserve(priced, 714)
	n = 1000000
	finish(r, route, report)
	available("0.000")
	_, err = store.Reserve(ctx, key, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", CallCorrelation{}, route)
	var qerr *QuotaError
	if !errors.As(err, &qerr) || qerr.Code != "member_quota_exceeded" {
		t.Fatalf("new request after exhaustion: %v", err)
	}
	n = 100000
	finish(late, lateRoute, report)
	check(late, "10", "gateway_usage_price", "11", "")

	t.Run("same label on another account and dispatch before adoption", func(t *testing.T) {
		if err := store.UpdateStudentQuotaAsTeacher(ctx, teacher, project, student, 199000, "ownership fixture"); err != nil {
			t.Fatal(err)
		}
		otherProvider, schoolKey, otherKey := uuid.New(), uuid.New(), uuid.New()
		exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
			VALUES($1,'Second account','https://api.beichenapi.com/v1',now(),now())`, otherProvider)
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
			VALUES($1,$2,'CC1','school-fixture',now(),now()),($3,$4,'CC1','foreign-fixture',now(),now())`,
			schoolKey, provider, otherKey, otherProvider)
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
			SELECT id,$3,'upstream','effective','billing',now()-interval '1 hour',1000000,2000000,500000,0
			FROM provider_credentials WHERE id IN ($1,$2)`, schoolKey, otherKey, priced)
		dispatch := func(p, c uuid.UUID) (Reservation, CallRoute) {
			t.Helper()
			route := CallRoute{Beichen: true, ProviderID: p, CredentialID: c, UpstreamModelName: priced}
			r, err := store.Reserve(ctx, key, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", CallCorrelation{}, route)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PrepareDispatch(ctx, r.CallID, route); err != nil {
				t.Fatal(err)
			}
			route.SentAt = time.Now().UTC()
			return r, route
		}
		before, beforeRoute := dispatch(provider, schoolKey)
		exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
			VALUES($1,'CC1',decode(repeat('56',32),'hex'),true)`, schoolKey)
		foreign, foreignRoute := dispatch(otherProvider, otherKey)
		exec(`UPDATE provider_credentials SET label='Renamed school credential' WHERE id=$1`, schoolKey)
		owned, ownedRoute := dispatch(provider, schoolKey)
		for _, tc := range []struct {
			r              Reservation
			route          CallRoute
			factor, charge string
		}{
			{before, beforeRoute, "1", "10"}, {foreign, foreignRoute, "1", "10"}, {owned, ownedRoute, "1.10", "11"},
		} {
			if err := store.Finalize(ctx, tc.r, true, "succeeded", "", "", tc.route, report); err != nil {
				t.Fatal(err)
			}
			got, err := store.StudentCallReceipt(ctx, student, tc.r.RequestID)
			if err != nil || got.SchoolMultiplier == nil || *got.SchoolMultiplier != tc.factor || got.ChargedCredits == nil || *got.ChargedCredits != tc.charge {
				t.Fatalf("expected factor %s, charge %s: %+v, %v", tc.factor, tc.charge, got, err)
			}
		}
	})
}
