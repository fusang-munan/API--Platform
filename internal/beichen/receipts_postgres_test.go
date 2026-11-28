package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestReceiptSweepPostgresG3(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || (config.Database != "nebula_test" && config.Database != "nebula_test_rev_b") ||
		(config.User != "nebula_qa" && config.User != "nebula_test") || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("requires the loopback nebula_test database with a test role and sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	admin := stdlib.OpenDB(*config)
	defer admin.Close()
	schema := pgx.Identifier{"beichen_receipt_sweep_" + uuid.NewString()}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("locate migrations: %v", err)
	}
	for _, path := range paths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		exec(string(migration))
	}
	student, teacher, organization, project, key, model, provider, credential := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES
		($1,'Student','student@example.test','fixture','student',now(),now()),
		($2,'Teacher','teacher@example.test','fixture','teacher',now(),now())`, student, teacher)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'School',now(),now())`, organization)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
		VALUES($1,$2,'Project',10000,now(),now())`, project, organization)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
		VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$2,$3,'Key','active','fixture','fixture',now(),now(),now())`, key, project, student)
	const modelName = "gpt-6.1-sol"
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,$2,'G3','active',1000,now(),now())`, model, modelName)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
		VALUES(gen_random_uuid(),$1,$2,now())`, key, model)
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
		VALUES($1,'Beichen',$2,now(),now())`, provider, origin)
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,'G3','fixture',now(),now())`, credential, provider)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
		VALUES($1,'G3',decode(repeat('34',32),'hex'),false)`, credential)

	secret := bytes.Repeat([]byte{7}, 32)
	manager, err := security.NewManager(secret, secret, secret, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s, store := New(db, manager), usage.NewStore(db)
	upstreamKeyID := int64(42)
	if err := s.SetPanelAccess(ctx, provider, credential, teacher, PanelAccessInput{
		Enabled: true, GroupID: 3, UpstreamKeyID: &upstreamKeyID, Token: "panel-fixture",
	}); err != nil {
		t.Fatal(err)
	}
	route := usage.CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: modelName}
	call, err := store.Reserve(ctx, key, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", usage.CallCorrelation{}, route)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareDispatch(ctx, call.CallID, route); err != nil {
		t.Fatal(err)
	}
	response := uuid.New()
	route.SentAt = time.Now().UTC().Truncate(time.Microsecond)
	if err := store.RecordBeichenReceiptID(ctx, call.CallID, credential, response); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(ctx, call, true, "succeeded", "", "", route, usage.CallMetrics{}); err != nil {
		t.Fatal(err)
	}

	const total = 402
	var requests int
	s.client.Transport = receiptTestTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		q := req.URL.Query()
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "beichenapi.com" ||
			req.URL.Path != "/api/v1/usage" || req.Header.Get("Authorization") != "Bearer panel-fixture" ||
			q.Get("api_key_id") != "42" || q.Get("model") != modelName || q.Get("sort_by") != "id" || q.Get("sort_order") != "asc" {
			t.Fatal("unexpected panel request")
		}
		page, err := strconv.Atoi(q.Get("page"))
		if err != nil || page < 1 || page > 5 {
			t.Fatalf("unexpected page: %q", q.Get("page"))
		}
		rows := make([]receiptRow, 0, receiptPageSize)
		for id := (page-1)*receiptPageSize + 1; id <= min(page*receiptPageSize, total); id++ {
			row := receiptFixture(route.SentAt, int64(id), "")
			row.Model = modelName
			if id == total {
				row.RequestID = "client:" + response.String()
				row.ActualCost = json.RawMessage("0.00014352")
				cacheRead := int64(4096)
				row.CacheRead = &cacheRead
			}
			rows = append(rows, row)
		}
		raw, err := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
			"items": rows, "total": total, "page": page, "page_size": receiptPageSize, "pages": 5,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	assertLedger := func(wantState, wantMilli string, wantRows int) {
		t.Helper()
		var state, cost string
		var count int
		err := db.QueryRowContext(ctx, `SELECT c.beichen_state,
			COALESCE(trim_scale(r.actual_cost)::text,''),(SELECT count(*) FROM beichen_call_receipts)
			FROM gateway_calls c LEFT JOIN beichen_call_receipts r ON r.call_id=c.id WHERE c.id=$1`, call.CallID).
			Scan(&state, &cost, &count)
		if err != nil || state != wantState || count != wantRows || (wantRows == 1 && cost != "0.00014352") {
			t.Fatalf("state=%s cost=%s receipts=%d: %v", state, cost, count, err)
		}
		for _, bucket := range []string{"project_month_credit_buckets", "project_member_month_credit_buckets", "api_key_month_credit_buckets"} {
			var milli string
			if err := db.QueryRowContext(ctx, `SELECT trim_scale(charged_milli+charged_milli_remainder)::text FROM `+bucket).
				Scan(&milli); err != nil || milli != wantMilli {
				t.Fatalf("%s charged milli=%s, want %s: %v", bucket, milli, wantMilli, err)
			}
		}
	}
	cursor, progress := uuid.Nil, make(map[uuid.UUID]*receiptProgress)
	sweep := func() {
		t.Helper()
		if err := s.sweepReceipts(ctx, store, &cursor, progress); err != nil {
			t.Fatal(err)
		}
	}
	sweep()
	assertLedger("pending_review", "0", 0)
	sweep()
	assertLedger("charged", "15.7872", 1)
	sweep()
	assertLedger("charged", "15.7872", 1)
	if requests != 6 {
		t.Fatalf("got %d HTTP requests, want four pages, boundary recheck and final page only", requests)
	}
	t.Run("already charged is linked once in the bounded window", func(t *testing.T) {
		if err := s.ConfigureWebSession("school@example.test", "fixture-password"); err != nil {
			t.Fatal(err)
		}
		binding, err := s.DeploymentPanelAccess(3, upstreamKeyID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetPanelAccess(ctx, provider, credential, teacher, binding); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var revision time.Time
		if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM provider_credentials WHERE id=$1`, credential).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		rate, zero := int64(2000000), int64(0)
		quote := panelQuote{rows: []priceRow{{Model: modelName, Rates: Rates{Input: &rate, Output: &rate, CacheRead: &zero, CacheWrite: &zero}}}, expiresAt: revision.Add(time.Hour)}
		if err := saveBillingObservation(ctx, tx, credential, revision, billingInfo{Multiplier: "1"}, nil, 3, quote); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		booked, err := store.Reserve(ctx, key, model, uuid.NewString(), "openai_responses", "/v1/responses", "", "", usage.CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, booked.CallID, route); err != nil {
			t.Fatal(err)
		}
		responseID := uuid.New()
		if err := store.RecordBeichenReceiptID(ctx, booked.CallID, credential, responseID); err != nil {
			t.Fatal(err)
		}
		route.SentAt = time.Now().UTC().Truncate(time.Microsecond)
		one := int64(1)
		if err := store.Finalize(ctx, booked, true, "succeeded", "", "", route, usage.CallMetrics{
			State: usage.UsageReported, PromptTokens: &one, CompletionTokens: &zero, CacheHitTokens: &zero, CacheWriteTokens: &zero,
		}); err != nil {
			t.Fatal(err)
		}
		var lookups, logins int
		var bookedState string
		if err := db.QueryRowContext(ctx, `SELECT beichen_state FROM gateway_calls WHERE id=$1`, booked.CallID).Scan(&bookedState); err != nil || bookedState != "charged" {
			t.Fatalf("expected local charge before receipt scan, state=%s: %v", bookedState, err)
		}
		s.client.Transport = receiptTestTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme != "https" || req.URL.Host != "beichenapi.com" {
				t.Fatal("unexpected site for the web session fixture")
			}
			if req.Method == http.MethodPost && req.URL.Path == "/api/v1/auth/login" {
				logins++
				raw := `{"code":0,"data":{"access_token":"school-web-fixture","token_type":"Bearer","expires_in":3600,"user":{"id":17,"email":"school@example.test"}}}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(raw))}, nil
			}
			if req.Method != http.MethodGet || req.URL.Path != "/api/v1/usage" ||
				req.URL.Query().Get("api_key_id") != "42" || req.Header.Get("Authorization") != "Bearer school-web-fixture" {
				t.Fatal("usage lookup did not use the bound numeric key and website session")
			}
			lookups++
			row := receiptFixture(route.SentAt, 503, "client:"+responseID.String())
			row.Model = modelName
			raw, err := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
				"items": []receiptRow{row}, "total": 1, "page": 1, "page_size": receiptPageSize, "pages": 1,
			}})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})
		exec(`UPDATE gateway_calls SET created_at=now()-interval '8 days' WHERE id=$1`, booked.CallID)
		sweep()
		if lookups != 0 || logins != 0 {
			t.Fatal("scanned a charged call outside the backfill window")
		}
		exec(`UPDATE gateway_calls SET created_at=sent_at WHERE id=$1`, booked.CallID)
		sweep()
		sweep()
		var applied bool
		var actual, credits, total string
		err = db.QueryRowContext(ctx, `SELECT applied,trim_scale(actual_cost)::text,trim_scale(credits_booked)::text,
			(SELECT trim_scale(charged_milli+charged_milli_remainder)::text FROM project_month_credit_buckets)
			FROM beichen_call_receipts WHERE call_id=$1`, booked.CallID).Scan(&applied, &actual, &credits, &total)
		if err != nil || logins != 1 || lookups != 1 || applied || actual != "0.0001435200000000001" || credits != "0.00022" || total != "16.0072" {
			t.Fatalf("logins=%d lookups=%d applied=%t actual=%s credits=%s project milli=%s: %v", logins, lookups, applied, actual, credits, total, err)
		}
		// A corrected website charge changes the comparison only.
		exec(`INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,channel,
			request_id,model,occurred_at,actual_cost,billing_type,cost_details)
			SELECT 17,row_id,api_key_id,$2,credential_id,'G3','client:'||response_id::text,
			model,occurred_at,0.00001,0,'{}'::jsonb FROM beichen_call_receipts WHERE call_id=$1`, booked.CallID, provider)
		var stamp time.Time
		if err := db.QueryRowContext(ctx, "SELECT occurred_at FROM beichen_call_receipts WHERE call_id=$1", booked.CallID).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		view, err := s.Overview(ctx, provider, stamp.In(beijing).Format(dayLayout))
		if err != nil || len(view.ArchivedUsage) != 1 {
			t.Fatalf("read observed amount: %v, %v", view.ArchivedUsage, err)
		}
		group := view.ArchivedUsage[0]
		if group.UnmatchedRows != 0 || group.MatchedChargedCredits == nil || *group.MatchedChargedCredits != credits ||
			group.MarginCredits == nil || *group.MarginCredits != "-0.00078" {
			t.Fatalf("unexpected corrected comparison: %+v", group)
		}
		page, err := s.ArchivedReceipts(ctx, provider, stamp.In(beijing).Format(dayLayout), "")
		if err != nil || len(page.Items) != 1 || page.NextCursor != nil {
			t.Fatalf("read the corrected receipt: got %d rows, error %v", len(page.Items), err)
		}
		item := page.Items[0]
		if !item.Matched || item.StudentName == nil || *item.StudentName != "Student" ||
			item.ActualCost == nil || *item.ActualCost != "0.00001" ||
			item.BaseCredits == nil || *item.BaseCredits != "0.0002" ||
			item.SchoolMultiplier == nil || *item.SchoolMultiplier != "1.10" ||
			item.ChargedCredits == nil || *item.ChargedCredits != credits ||
			item.DifferenceCredits == nil || *item.DifferenceCredits != *group.MarginCredits {
			t.Fatalf("receipt differs from its original student booking or the archive: %+v", item)
		}
	})
	t.Run("archive pages keep equal timestamps and unknown debits", func(t *testing.T) {
		const day = "2020-06-12"
		start, err := dayStart(day)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,
			channel,model,occurred_at,actual_cost,billing_type,cost_details)
			SELECT 41,1000+n,82,$1,$2,'G3','model-'||n,$3,0,
			CASE WHEN n=50 THEN 1 ELSE 0 END,'{}'::jsonb FROM generate_series(1,50) n`, provider, credential, start)
		exec(`INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,
			channel,model,occurred_at,actual_cost,billing_type,cost_details) VALUES
			(42,1001,83,$1,$2,'G3','same-number',$3,0.000001,0,'{}'),
			(42,1002,83,$1,$2,'G3','previous-day',$3::timestamptz - interval '1 microsecond',0,0,'{}')`,
			provider, credential, start)
		first, err := s.ArchivedReceipts(ctx, provider, day, "")
		if err != nil || len(first.Items) != archivedReceiptPageSize || first.NextCursor == nil {
			t.Fatalf("expected a full first page with more rows, got %d, error %v", len(first.Items), err)
		}
		unpriced, zero := first.Items[0], first.Items[1]
		if unpriced.Model != "model-50" || unpriced.ActualCost != nil || unpriced.Matched || unpriced.StudentName != nil ||
			unpriced.ChargedCredits != nil || zero.ActualCost == nil || *zero.ActualCost != "0" || zero.DifferenceCredits != nil {
			t.Fatal("unmatched, unpriced and confirmed zero must remain distinguishable")
		}
		if first.Items[len(first.Items)-1].Model != "same-number" {
			t.Fatal("expected the page boundary inside a set with equal row IDs and times")
		}
		second, err := s.ArchivedReceipts(ctx, provider, day, *first.NextCursor)
		if err != nil || len(second.Items) != 1 || second.Items[0].Model != "model-1" || second.NextCursor != nil {
			t.Fatalf("missing or repeated row after the bookmark: %+v, error %v", second, err)
		}
		if _, err = s.ArchivedReceipts(ctx, provider, "2020-06-13", *first.NextCursor); err == nil {
			t.Fatal("cursor from a different Beijing day was accepted")
		}
	})
}
