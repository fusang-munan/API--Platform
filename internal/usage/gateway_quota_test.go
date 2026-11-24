package usage

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestGatewayQuotaPostgres(t *testing.T) {
	ctx, db := newBeichenLedgerTestDB(t)
	t.Setenv("NEBULA_STUDENT_CONCURRENCY_LIMIT", "")
	if err := os.Unsetenv("NEBULA_STUDENT_CONCURRENCY_LIMIT"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "")
	t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "")
	store := NewStore(db)
	exec := func(stmt string, args ...any) {
		t.Helper()
		ledgerTestExec(t, ctx, db, stmt, args...)
	}
	student, org, model := uuid.New(), uuid.New(), uuid.New()
	project, otherProject, key, otherKey := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Quota student','quota-student@example.test','fixture','student',now(),now())`, student)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at)
		VALUES($1,'Quota organization',now(),now())`, org)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
		VALUES($1,$3,'Quota project',1500,now(),now()),($2,$3,'Other quota project',1500,now(),now())`, project, otherProject, org)
	const addMember = `INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
		VALUES(gen_random_uuid(),$1,$3,1000,now()),(gen_random_uuid(),$2,$3,1000,now()) ON CONFLICT DO NOTHING`
	exec(addMember, project, otherProject, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$3,$5,'Quota key','active','quota-hash','fixture',now(),now(),now()),
		      ($2,$4,$5,'Other key','active','other-quota-hash','fixture',now(),now(),now())`, key, otherKey, project, otherProject, student)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'quota-model','Quota model','active',1000,now(),now())`, model)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
		VALUES(gen_random_uuid(),$1,$3,now()),(gen_random_uuid(),$2,$3,now())`, key, otherKey, model)

	route := CallRoute{Beichen: true, ProviderID: uuid.New(), BindingID: uuid.New(), CredentialID: uuid.New(), UpstreamModelName: "quota-model"}
	reserve := func(keyID uuid.UUID) (Reservation, error) {
		return store.Reserve(ctx, keyID, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", CallCorrelation{}, route)
	}
	mustReserve := func(t *testing.T, keyID uuid.UUID) Reservation {
		t.Helper()
		reservation, err := reserve(keyID)
		if err != nil {
			t.Fatal(err)
		}
		return reservation
	}
	release := func(t *testing.T, r Reservation) {
		t.Helper()
		if err := store.Finalize(ctx, r, false, "upstream_failed", "", "", CallRoute{DefinitelyNotSent: true}, CallMetrics{}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("inactive access", func(t *testing.T) {
		for _, tt := range []struct {
			name, disable, restore string
			id                     uuid.UUID
		}{
			{"key", "UPDATE api_keys SET status='inactive',revoked_at=now() WHERE id=$1", "UPDATE api_keys SET status='active',revoked_at=NULL WHERE id=$1", key},
			{"model", "UPDATE models SET status='inactive' WHERE id=$1", "UPDATE models SET status='active' WHERE id=$1", model},
			{"student", "UPDATE users SET status='disabled' WHERE id=$1", "UPDATE users SET status='active' WHERE id=$1", student},
			{"project", "UPDATE projects SET status='inactive' WHERE id=$1", "UPDATE projects SET status='active' WHERE id=$1", project},
			{"organization", "UPDATE organizations SET status='inactive' WHERE id=$1", "UPDATE organizations SET status='active' WHERE id=$1", org},
		} {
			t.Run(tt.name, func(t *testing.T) {
				exec(tt.disable, tt.id)
				defer exec(tt.restore, tt.id)
				if _, err := reserve(key); domain.ErrorCode(err) != domain.CodeNotFound {
					t.Fatalf("inactive %s: %v", tt.name, err)
				}
			})
		}
		exec("DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", project, student)
		defer exec(addMember, project, otherProject, student)
		if _, err := reserve(key); domain.ErrorCode(err) != domain.CodeNotFound {
			t.Fatalf("removed membership: %v", err)
		}
		var calls int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM gateway_calls WHERE user_id=$1", student).Scan(&calls); err != nil || calls != 0 {
			t.Fatalf("rejected access wrote %d calls: %v", calls, err)
		}
	})

	t.Run("balance before the new hold", func(t *testing.T) {
		first := mustReserve(t, key)
		second := mustReserve(t, key)
		if first.MemberAvailableMilli != 1000 || first.ProjectAvailableMilli != 1500 ||
			second.MemberAvailableMilli != 999 || second.ProjectAvailableMilli != 1499 {
			t.Fatalf("available member/project: first=%d/%d second=%d/%d",
				first.MemberAvailableMilli, first.ProjectAvailableMilli, second.MemberAvailableMilli, second.ProjectAvailableMilli)
		}
		var admissionOnly bool
		if err := db.QueryRowContext(ctx, "SELECT credit_milli=1 AND student_cap_milli IS NULL FROM gateway_calls WHERE id=$1", first.CallID).Scan(&admissionOnly); err != nil || !admissionOnly {
			t.Fatalf("expected a one milli-credit hold with no settlement cap: %v", err)
		}
		release(t, first)
		release(t, second)
		exec("UPDATE project_member_month_credit_buckets SET charged_milli=999, charged_milli_remainder=0.5 WHERE project_id=$1 AND user_id=$2", project, student)
		last := mustReserve(t, key)
		if last.MemberAvailableMilli != 0 {
			t.Fatalf("fractional remaining balance exposed as %d milli-credits", last.MemberAvailableMilli)
		}
		release(t, last)
		exec("UPDATE project_member_month_credit_buckets SET charged_milli=0, charged_milli_remainder=0 WHERE project_id=$1 AND user_id=$2", project, student)
	})

	t.Run("membership checked again before dispatch", func(t *testing.T) {
		request := mustReserve(t, key)
		exec("DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", project, student)
		defer exec(addMember, project, otherProject, student)
		defer release(t, request)
		if _, err := store.StartAttempt(ctx, request, route, 0); domain.ErrorCode(err) != domain.CodeNotFound {
			t.Fatalf("dispatch after membership removal: %v", err)
		}
	})

	t.Run("effective output price", func(t *testing.T) {
		check := func(want string) {
			t.Helper()
			got, err := store.BeichenOutputPrice(ctx, route)
			if err != nil || got != want {
				t.Fatalf("output price=%q, want %q: %v", got, want, err)
			}
		}
		check("")
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate)
			VALUES ($1,$2,'upstream','effective','billing',1000000,8000000)`, route.CredentialID, route.UpstreamModelName)
		check("8")
		exec(`WITH multiplier AS (
			INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier,group_id)
			VALUES($1,statement_timestamp(),0.5,16) RETURNING id,effective_at
		), snapshot AS (
			INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version)
			SELECT $1,effective_at,effective_at,16,effective_at+interval '15 minutes',id FROM multiplier
			RETURNING effective_at
		)
		INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,group_id,observed_at,effective_at)
		SELECT $1,$2,'upstream','base','derived_from_usage',1000000,8000000,16,effective_at,effective_at FROM snapshot`,
			route.CredentialID, route.UpstreamModelName)
		var inferredOutput string
		if err := db.QueryRowContext(ctx, `SELECT trim_scale(school_output)::text
			FROM beichen_price_at($1,$2,statement_timestamp())`, route.CredentialID, route.UpstreamModelName).
			Scan(&inferredOutput); err != nil || inferredOutput != "4000000" {
			t.Fatalf("fixture must select the inferred price: %q (%v)", inferredOutput, err)
		}
		check("")
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate)
			VALUES ($1,$2,'school','manual','teacher',$3,100000000,600000000)`, route.CredentialID, route.UpstreamModelName, student)
		check("6")
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id)
			VALUES ($1,$2,'school','follow','teacher',$3)`, route.CredentialID, route.UpstreamModelName, student)
		check("")
	})

	t.Run("default allows five dispatches", func(t *testing.T) {
		var requests [6]Reservation
		for i := range requests {
			requests[i] = mustReserve(t, key)
			defer release(t, requests[i])
		}
		for i, request := range requests {
			_, err := store.StartAttempt(ctx, request, route, 0)
			if i == 5 {
				expectQuotaRejection(t, err, StudentConcurrencyExceeded)
			} else if err != nil {
				t.Fatalf("dispatch %d: %v", i+1, err)
			}
		}
	})

	t.Run("dispatch shares a limit across keys and projects", func(t *testing.T) {
		var requests []Reservation
		for range 3 {
			requests = append(requests, mustReserve(t, key), mustReserve(t, otherKey))
		}
		results := make(chan error, len(requests))
		for _, request := range requests {
			go func() {
				_, err := store.StartAttempt(ctx, request, route, 0)
				results <- err
			}()
		}
		rejected := 0
		for range requests {
			if err := <-results; err != nil {
				expectQuotaRejection(t, err, StudentConcurrencyExceeded)
				rejected++
			}
		}
		if rejected != 1 {
			t.Fatalf("rejected %d dispatches out of %d, want 1", rejected, len(requests))
		}
		_, err := reserve(otherKey)
		expectQuotaRejection(t, err, StudentConcurrencyExceeded)
		for _, request := range requests {
			release(t, request)
		}
	})

	t.Run("configured limit of one", func(t *testing.T) {
		t.Setenv("NEBULA_STUDENT_CONCURRENCY_LIMIT", "1")
		inFlight := mustReserve(t, otherKey)
		if _, err := store.StartAttempt(ctx, inFlight, route, 0); err != nil {
			t.Fatal(err)
		}
		_, err := reserve(key)
		expectQuotaRejection(t, err, StudentConcurrencyExceeded)
		release(t, inFlight)
		release(t, mustReserve(t, key))
	})

	t.Run("pending review uses a student credit budget", func(t *testing.T) {
		reviewRoute := route
		reviewRoute.CredentialID = uuid.New()
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate)
			VALUES ($1,$2,'upstream','effective','billing',1000000,8000000)`, reviewRoute.CredentialID, reviewRoute.UpstreamModelName)
		exec(`INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,multiplier_milli,
			credit_milli,billing_state,beichen_state,beichen_review_reason,unresolved_credit_milli,created_at)
			SELECT gen_random_uuid(),i::text,now(),$1,$2,$3,$4,$5,'org','project','student','key','model',
			'openai_responses','/v1/responses',1000,0,'failed','pending_review','missing_terminal_usage',10,now()
			FROM generate_series(1,98) i`, org, project, student, key, model)
		makePending := func(request Reservation) CallRoute {
			t.Helper()
			if _, err := store.StartAttempt(ctx, request, reviewRoute, 0); err != nil {
				t.Fatal(err)
			}
			if err := store.PrepareDispatch(ctx, request.CallID, reviewRoute); err != nil {
				t.Fatal(err)
			}
			sent := reviewRoute
			sent.SentAt = time.Now()
			if err := store.Finalize(ctx, request, true, "succeeded", "", "", sent, CallMetrics{}); err != nil {
				t.Fatal(err)
			}
			return sent
		}
		waiting := mustReserve(t, otherKey)
		defer release(t, waiting)
		last := mustReserve(t, key)
		sent := makePending(last)
		_, err := store.StartAttempt(ctx, waiting, reviewRoute, 0)
		expectQuotaRejection(t, err, UnresolvedExposureLimit) // 99 × 10 + 10 = 1000
		_, err = reserve(otherKey)
		expectQuotaRejection(t, err, UnresolvedExposureLimit)

		student2, key2 := uuid.New(), uuid.New()
		exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
			VALUES($1,'Student 2','quota-student-2@example.test','fixture','student',now(),now())`, student2)
		exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
			VALUES(gen_random_uuid(),$1,$2,100,now())`, project, student2)
		exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
			VALUES($1,$2,$3,'Independent key','active','independent-hash','fixture',now(),now(),now())`, key2, project, student2)
		exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, key2, model)
		release(t, mustReserve(t, key2))

		t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "9")
		if err := store.Finalize(ctx, last, true, "succeeded", "", "", sent, CallMetrics{}); err != nil {
			t.Fatal(err)
		}
		checkExposure := func(id uuid.UUID, state string, credits int) {
			t.Helper()
			var match bool
			err := db.QueryRowContext(ctx, `SELECT beichen_state=$2 AND unresolved_credit_milli=$3
				FROM gateway_calls WHERE id=$1`, id, state, credits).Scan(&match)
			if err != nil || !match {
				t.Fatalf("call %s: want %s with %d unresolved credits: %v", id, state, credits, err)
			}
		}
		checkExposure(last.CallID, "pending_review", 10)
		small := mustReserve(t, otherKey) // 99 existing reviews, 990 + 9 < 1000
		rejectedRoute := makePending(small)
		checkExposure(small.CallID, "pending_review", 9)
		_, err = reserve(key)
		expectQuotaRejection(t, err, UnresolvedExposureLimit) // 999 + 9 > 1000

		t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "1010")
		extra := mustReserve(t, key)
		extraRoute := makePending(extra)
		t.Setenv("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", "")
		_, err = reserve(otherKey)
		expectQuotaRejection(t, err, UnresolvedExposureLimit) // already 1008 outstanding
		extraRoute.UpstreamStatus = 400
		if err := store.Finalize(ctx, extra, false, "upstream_failed", "", "", extraRoute, CallMetrics{}); err != nil {
			t.Fatal(err)
		}
		rejectedRoute.UpstreamStatus = 400
		for range 2 {
			if err := store.Finalize(ctx, small, false, "upstream_failed", "", "", rejectedRoute, CallMetrics{}); err != nil {
				t.Fatal(err)
			}
		}
		checkExposure(small.CallID, "released", 0)
		t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "")
		input, output, zero := int64(33267), int64(8), int64(0)
		report := CallMetrics{State: UsageReported, PromptTokens: &input, CompletionTokens: &output, CacheHitTokens: &zero, CacheWriteTokens: &zero}
		for range 2 {
			if err := store.CompleteCallMetrics(ctx, last.CallID, report); err != nil {
				t.Fatal(err)
			}
		}
		var settled bool
		checkExposure(last.CallID, "charged", 0)
		err = db.QueryRowContext(ctx, `SELECT beichen_state='charged' AND beichen_review_reason IS NULL
			AND beichen_credit_exact=3.3331 FROM gateway_calls WHERE id=$1`, last.CallID).Scan(&settled)
		if err != nil || !settled {
			t.Fatalf("late verified counters should settle and release the pending credits: %v", err)
		}
		release(t, mustReserve(t, otherKey))

		// An older, smaller reserve leaves room for a default-size call even with 99 reviews.
		t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "1")
		makePending(mustReserve(t, otherKey))
		t.Setenv("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", "")
		makePending(mustReserve(t, otherKey)) // 98 × 10 + 1 + 10 = 991
	})
}

func expectQuotaRejection(t *testing.T, err error, code string) {
	t.Helper()
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.Code != code {
		t.Fatalf("quota rejection=%v, want %s", err, code)
	}
}
