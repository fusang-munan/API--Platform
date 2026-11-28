package usage

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/testutil"
)

func newBeichenLedgerTestDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || !testutil.IsIsolatedPostgresDatabase(config.Database) ||
		config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("requires a disposable loopback ledger test database with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := pgx.Identifier{"beichen_ledger_" + uuid.NewString()}.Sanitize()
	ledgerTestExec(t, ctx, db, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config.RuntimeParams["search_path"] = schema + ",public"
	isolated := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = isolated.Close() })
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("locate migrations: %v", err)
	}
	for _, path := range paths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ledgerTestExec(t, ctx, isolated, string(migration))
	}
	return ctx, isolated
}

func TestBeichenLedgerPostgres(t *testing.T) {
	ctx, isolated := newBeichenLedgerTestDB(t)
	student, org, project, key, model := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ledgerTestExec(t, ctx, isolated, `INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Student','student@example.test','fixture','student',now(),now())`, student)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO organizations(id,name,created_at,updated_at)
		VALUES($1,'Org',now(),now())`, org)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
		VALUES($1,$2,'Project',10000,now(),now())`, project, org)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
		VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$2,$3,'Key','active','test-hash','fixture',now(),now(),now())`, key, project, student)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'model','Model','active',1250,now(),now())`, model)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
		VALUES(gen_random_uuid(),$1,$2,now())`, key, model)

	route := CallRoute{Beichen: true, CredentialID: uuid.New(), UpstreamModelName: "actual-model"}
	other := CallRoute{Beichen: true, CredentialID: uuid.New(), UpstreamModelName: "wrong-model"}
	ledgerTestExec(t, ctx, isolated, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,
		input_rate,output_rate,cache_read_rate,cache_write_rate)
		VALUES($1,$2,'upstream','base','billing',now()-interval '1 hour',2000000,4000000,500000,0),
		($3,$4,'upstream','effective','billing',now()-interval '1 hour',900000000,900000000,0,0),
		($1,$2,'upstream','effective','billing',now()+interval '1 hour',900000000,900000000,0,0)`,
		route.CredentialID, route.UpstreamModelName, other.CredentialID, other.UpstreamModelName)
	ledgerTestExec(t, ctx, isolated, `INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier)
		VALUES($1,now()-interval '1 hour',2.5),($1,now()+interval '1 hour',9)`, route.CredentialID)
	store := NewStore(isolated)
	reserve := func() Reservation {
		t.Helper()
		r, err := store.Reserve(ctx, key, model, uuid.NewString(), "openai_compatible", "/v1/chat/completions",
			"", "", CallCorrelation{}, other, route)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	prepare := func(r Reservation, candidate CallRoute) CallRoute {
		t.Helper()
		if err := store.PrepareDispatch(ctx, r.CallID, candidate); err != nil {
			t.Fatal(err)
		}
		candidate.SentAt = time.Now()
		return candidate
	}
	finalize := func(r Reservation, candidate CallRoute, metrics CallMetrics) {
		t.Helper()
		if err := store.Finalize(ctx, r, !candidate.DefinitelyNotSent, "succeeded", "", "", candidate, metrics); err != nil {
			t.Fatal(err)
		}
	}
	check := func(id uuid.UUID, wantState, wantExact string) {
		t.Helper()
		var state, amount, reason string
		err := isolated.QueryRowContext(ctx, `SELECT beichen_state,COALESCE(trim_scale(beichen_credit_exact)::text,''),
			COALESCE(beichen_review_reason,'') FROM gateway_calls WHERE id=$1`, id).Scan(&state, &amount, &reason)
		if err != nil || state != wantState || amount != wantExact {
			t.Fatalf("ledger state=%s credits=%s reason=%s want %s %q: %v", state, amount, reason, wantState, wantExact, err)
		}
	}
	zero, one := int64(0), int64(1)
	report := CallMetrics{State: UsageReported, PromptTokens: &one, CompletionTokens: &zero, CacheHitTokens: &zero, CacheWriteTokens: &zero}
	for i := 0; i < 2; i++ {
		r := reserve()
		prepare(r, other)
		final := prepare(r, route)
		finalize(r, final, CallMetrics{})
		check(r.CallID, "pending_review", "")
		replays := make(chan error, 4)
		for range cap(replays) {
			go func() { replays <- store.CompleteCallMetrics(ctx, r.CallID, report) }()
		}
		for range cap(replays) {
			if err := <-replays; err != nil {
				t.Fatal(err)
			}
		}
		finalize(r, final, report)
		check(r.CallID, "charged", "0.0005")
	}
	unsent := reserve()
	finalize(unsent, CallRoute{DefinitelyNotSent: true}, CallMetrics{})
	check(unsent.CallID, "released", "")
	rejected := reserve()
	rejectedRoute := prepare(rejected, route)
	rejectedRoute.UpstreamStatus = http.StatusBadRequest
	if err := store.Finalize(ctx, rejected, false, "upstream_failed", "http", "HTTP 400", rejectedRoute,
		CallMetrics{State: UsageMissing}); err != nil {
		t.Fatal(err)
	}
	check(rejected.CallID, "released", "")
	unknown := reserve()
	prepare(unknown, route)
	ledgerTestExec(t, ctx, isolated, `UPDATE gateway_calls SET lease_expires_at=now()-interval '1 hour' WHERE id=$1`, unknown.CallID)
	if err := store.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	check(unknown.CallID, "pending_review", "")
	missing := reserve()
	finalize(missing, prepare(missing, CallRoute{Beichen: true, CredentialID: uuid.New(), UpstreamModelName: "missing"}), report)
	check(missing.CallID, "pending_review", "")
	revokedBeforeSend, revokedAfterDispatch := reserve(), reserve()
	prepare(revokedAfterDispatch, route)
	if _, err := store.DeactivateKey(ctx, student, key, "fixture"); err != nil {
		t.Fatal(err)
	}
	check(revokedBeforeSend.CallID, "released", "")
	check(revokedAfterDispatch.CallID, "reserved", "")
	// The already dispatched call reaches its ordinary settlement path.
	route.SentAt = time.Now()
	finalize(revokedAfterDispatch, route, CallMetrics{})
	check(revokedAfterDispatch.CallID, "pending_review", "")
	if err := store.PrepareDispatch(ctx, revokedBeforeSend.CallID, route); err == nil {
		t.Fatal("dispatch permitted after key deactivation")
	}
	for _, table := range []string{"project_month_credit_buckets", "project_member_month_credit_buckets", "api_key_month_credit_buckets", "monthly_usage_cube"} {
		var charged, pending int64
		pendingColumn := "pending_milli"
		if table == "monthly_usage_cube" {
			pendingColumn = "0"
		}
		var remainder string
		err := isolated.QueryRowContext(ctx, "SELECT charged_milli,"+pendingColumn+",trim_scale(charged_milli_remainder)::text FROM "+table).
			Scan(&charged, &pending, &remainder)
		if err != nil || charged != 1 || pending != 0 || remainder != "0" {
			t.Fatalf("%s charged=%d pending=%d remainder=%s: %v", table, charged, pending, remainder, err)
		}
	}
	for _, tc := range []struct {
		name              string
		read, miss, write *int64
		want              string
	}{
		{"missing-write-with-zero-price", &zero, nil, nil, ""},
		{"missing-write-proven-zero", &zero, &one, nil, "0.0002"},
		{"missing-read-proven-zero", nil, &one, &zero, "0.0002"},
		{"inconsistent-explicit-counts", &one, &one, &zero, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var amount string
			err := isolated.QueryRowContext(ctx, `SELECT COALESCE(trim_scale(beichen_token_credits(
				'openai_responses', 1, 0, $1, $2, $3, 2000000, 4000000, 500000, 0))::text,'')`,
				tc.read, tc.miss, tc.write).Scan(&amount)
			if err != nil || amount != tc.want {
				t.Fatalf("credits=%q want %q: %v", amount, tc.want, err)
			}
		})
	}
	t.Run("external-receipts", func(t *testing.T) {
		// The school pricing fixture keeps three calls in flight to verify
		// dispatch-time ownership. Dispatch limits have their own tests.
		t.Setenv("NEBULA_STUDENT_CONCURRENCY_LIMIT", "3")
		if _, err := store.ActivateKey(ctx, student, key); err != nil {
			t.Fatal(err)
		}
		provider := uuid.New()
		ledgerTestExec(t, ctx, isolated, `INSERT INTO providers(id,name,base_url,created_at,updated_at)
			VALUES ($1,'Beichen','https://api.beichenapi.com',now(),now())`, provider)
		ledgerTestExec(t, ctx, isolated, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
			VALUES ($1,$3,'G3',decode('01','hex'),now(),now()),($2,$3,'GM1',decode('02','hex'),now(),now())`,
			route.CredentialID, other.CredentialID, provider)
		ledgerTestExec(t, ctx, isolated, `INSERT INTO beichen_panel_access
			(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
			SELECT id,1,CASE WHEN id=$1 THEN 91 ELSE 92 END,decode('03','hex'),$3,updated_at
			FROM provider_credentials WHERE id IN ($1,$2)`, route.CredentialID, other.CredentialID, student)

		makeReceipt := func(candidate CallRoute, response uuid.UUID, row, apiKey int64) (Reservation, BeichenReceipt) {
			t.Helper()
			r := reserve()
			final := prepare(r, candidate)
			if err := store.RecordBeichenReceiptID(ctx, r.CallID, candidate.CredentialID, response); err != nil {
				t.Fatal(err)
			}
			original := int64(4225)
			finalize(r, final, CallMetrics{State: UsageMissing, PromptTokens: &original})
			return r, BeichenReceipt{ResponseID: response, RowID: row, APIKeyID: apiKey,
				Model: candidate.UpstreamModelName, OccurredAt: final.SentAt, ActualCost: "0.00014352",
				InputTokens: 129, OutputTokens: 5, CacheReadTokens: 4096}
		}
		apply := func(credential uuid.UUID, receipt BeichenReceipt, expected bool) {
			t.Helper()
			booked, err := store.ApplyBeichenReceipt(ctx, credential, receipt)
			if err != nil || booked != expected {
				t.Fatalf("booked=%t want %t: %v", booked, expected, err)
			}
		}
		g3, bill := makeReceipt(route, uuid.New(), 501, 91)
		assertStudentReceipt := func(requestID, state, credits, cost string, applied *bool) {
			t.Helper()
			got, err := store.StudentCallReceipt(ctx, student, requestID)
			if err != nil || got.RequestID != requestID || got.ModelID != "model" ||
				got.Channel != "北辰" || got.BillingState != state {
				t.Fatalf("receipt identity/state = %+v, err %v", got, err)
			}
			checkDecimal := func(field string, got *string, want string) {
				t.Helper()
				if (want == "" && got != nil) || (want != "" && (got == nil || *got != want)) {
					t.Errorf("%s: want %q (empty means null), got %v", field, want, got)
				}
			}
			checkDecimal("charged_credits", got.ChargedCredits, credits)
			checkDecimal("actual_cost", got.ActualCost, cost)
			if (applied == nil && got.ReceiptApplied != nil) ||
				(applied != nil && (got.ReceiptApplied == nil || *got.ReceiptApplied != *applied)) {
				t.Errorf("unexpected receipt_applied: %v", got.ReceiptApplied)
			}
		}
		assertNotFound := func(userID uuid.UUID, requestID string) {
			t.Helper()
			_, err := store.StudentCallReceipt(ctx, userID, requestID)
			if domain.ErrorCode(err) != domain.CodeNotFound || err.Error() != "call receipt not found" {
				t.Fatalf("expected uniform not found, got %v", err)
			}
		}
		assertStudentReceipt(g3.RequestID, "pending_review", "", "", nil)
		assertNotFound(uuid.New(), g3.RequestID)
		assertNotFound(student, uuid.NewString())
		inconsistent := reserve()
		assertNotFound(student, inconsistent.RequestID)
		otherStudent := uuid.New()
		ledgerTestExec(t, ctx, isolated, `UPDATE gateway_calls SET request_id=$2, user_id=$3,
			beichen_dispatch_kind='beichen' WHERE id=$1`, inconsistent.CallID, g3.RequestID, otherStudent)
		assertNotFound(otherStudent, g3.RequestID)
		assertStudentReceipt(g3.RequestID, "pending_review", "", "", nil)
		ledgerTestExec(t, ctx, isolated, `UPDATE gateway_calls SET user_id=$2 WHERE id=$1`, inconsistent.CallID, student)
		assertNotFound(student, g3.RequestID)
		ledgerTestExec(t, ctx, isolated, `UPDATE gateway_calls SET request_id=$2 WHERE id=$1`,
			inconsistent.CallID, inconsistent.RequestID)
		t.Run("student receipt uses lookup index", func(t *testing.T) {
			tx, err := isolated.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// The small fixture otherwise makes a sequential scan cheaper.
			if _, err := tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				t.Fatal(err)
			}
			var plan string
			err = tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+studentCallReceiptSQL,
				student, g3.RequestID).Scan(&plan)
			if err != nil || !strings.Contains(plan, "gateway_calls_student_receipt_lookup") {
				t.Fatalf("expected receipt lookup index: %v\n%s", err, plan)
			}
		})
		wrong := bill
		wrong.APIKeyID = 92
		apply(route.CredentialID, wrong, false)
		apply(other.CredentialID, bill, false)
		wrong = bill
		wrong.Model = other.UpstreamModelName
		apply(route.CredentialID, wrong, false)
		wrong = bill
		wrong.OccurredAt = bill.OccurredAt.Add(-time.Hour)
		apply(route.CredentialID, wrong, false)
		numbers := make(chan bool, 4)
		errs := make(chan error, 4)
		for range cap(numbers) {
			go func() {
				booked, err := store.ApplyBeichenReceipt(ctx, route.CredentialID, bill)
				numbers <- booked
				errs <- err
			}()
		}
		bookings := 0
		for range cap(numbers) {
			if <-numbers {
				bookings++
			}
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if bookings != 1 {
			t.Fatalf("expected one booking from concurrent replays, got %d", bookings)
		}
		check(g3.CallID, "charged", "0.014352")
		applied := true
		assertStudentReceipt(g3.RequestID, "charged", "0.014352", "0.00014352", &applied)
		gm1, second := makeReceipt(other, uuid.New(), bill.RowID, 92)
		second.ActualCost, second.InputTokens, second.OutputTokens, second.CacheReadTokens = "0.00001136", 59, 3, 0
		apply(other.CredentialID, second, false)
		second.RowID++
		apply(other.CredentialID, second, true)
		check(gm1.CallID, "charged", "0.001136")
		var exactReceipts string
		var keptOriginal bool
		err := isolated.QueryRowContext(ctx, `SELECT trim_scale(sum(r.credits_booked))::text,
			bool_and(c.prompt_tokens=4225 AND c.cache_hit_tokens IS NULL)
			FROM beichen_call_receipts r JOIN gateway_calls c ON c.id=r.call_id`).Scan(&exactReceipts, &keptOriginal)
		if err != nil || exactReceipts != "0.015488" || !keptOriginal {
			t.Fatalf("receipts=%s kept original=%t: %v", exactReceipts, keptOriginal, err)
		}
		for _, table := range []string{"project_month_credit_buckets", "project_member_month_credit_buckets", "api_key_month_credit_buckets", "monthly_usage_cube"} {
			var total string
			err := isolated.QueryRowContext(ctx, `SELECT trim_scale(sum(charged_milli+charged_milli_remainder)*0.001)::text FROM `+table).Scan(&total)
			if err != nil || total != "0.016488" {
				t.Fatalf("%s: old charges plus both receipts=%s: %v", table, total, err)
			}
		}
		ambiguous, duplicate := makeReceipt(route, uuid.New(), 503, 91)
		conflicting := reserve()
		final := prepare(conflicting, route)
		if err := store.RecordBeichenReceiptID(ctx, conflicting.CallID, route.CredentialID, duplicate.ResponseID); err == nil {
			t.Fatal("duplicate response UUID accepted on another call")
		}
		finalize(conflicting, final, CallMetrics{})
		apply(route.CredentialID, duplicate, false)
		check(ambiguous.CallID, "pending_review", "")
		check(conflicting.CallID, "pending_review", "")

		estimated, audited := makeReceipt(route, uuid.New(), 505, 91)
		if err := store.CompleteCallMetrics(ctx, estimated.CallID, report); err != nil {
			t.Fatal(err)
		}
		// A settled row from before 0039 has its original exact debit and no
		// policy snapshot. A later receipt must not reprice that debit.
		ledgerTestExec(t, ctx, isolated, `UPDATE gateway_calls SET beichen_school_multiplier=NULL,
			beichen_base_credits=NULL,beichen_base_source=NULL WHERE id=$1`, estimated.CallID)
		apply(route.CredentialID, audited, false)
		var recordedDifference bool
		err = isolated.QueryRowContext(ctx, `SELECT NOT applied AND credits_booked=0.0005 AND actual_cost=0.00014352
			FROM beichen_call_receipts WHERE row_id=505`).Scan(&recordedDifference)
		if err != nil || !recordedDifference {
			t.Fatalf("already-charged difference was not archived: %v", err)
		}
		check(estimated.CallID, "charged", "0.0005")
		applied = false
		assertStudentReceipt(estimated.RequestID, "charged", "0.0005", "0.00014352", &applied)
		legacy, err := store.StudentCallReceipt(ctx, student, estimated.RequestID)
		if err != nil || legacy.BaseCredits == nil || *legacy.BaseCredits != "0.0005" || legacy.BaseSource == nil ||
			*legacy.BaseSource != "legacy" || legacy.SchoolMultiplier == nil || *legacy.SchoolMultiplier != "1" {
			t.Fatalf("legacy receipt lost original basis: %+v, %v", legacy, err)
		}

		stale, old := makeReceipt(route, uuid.New(), 504, 91)
		ledgerTestExec(t, ctx, isolated, `UPDATE provider_credentials SET updated_at=updated_at+interval '1 second' WHERE id=$1`, route.CredentialID)
		apply(route.CredentialID, old, false)
		check(stale.CallID, "pending_review", "")
		t.Run("school billing and teacher allocation", func(t *testing.T) {
			testSchoolBilling(t, ctx, isolated, org, student, model, provider, other.CredentialID)
		})
		t.Run("教师手动价实际扣费", func(t *testing.T) {
			testBeichenManualBilling(t, ctx, isolated, org, student, model, provider, other.CredentialID)
		})
	})
	t.Run("project receipt attribution", func(t *testing.T) {
		testProjectReceiptAttribution(t, ctx, isolated, org, project, student, key, model)
	})
}

func ledgerTestExec(t *testing.T, ctx context.Context, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		t.Fatal(err)
	}
}
