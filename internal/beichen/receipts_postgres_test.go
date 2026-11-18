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
	if err != nil || config.Host != "127.0.0.1" || config.Database != "nebula_test" ||
		config.User != "nebula_qa" || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("requires loopback PostgreSQL as nebula_qa/nebula_test with sslmode=disable")
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
	assertLedger("charged", "14.352", 1)
	sweep()
	assertLedger("charged", "14.352", 1)
	if requests != 6 {
		t.Fatalf("got %d HTTP requests, want four pages, boundary recheck and final page only", requests)
	}
}
