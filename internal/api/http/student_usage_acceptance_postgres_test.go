package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/testutil"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type usageHTTPQueryTrace struct {
	mu         sync.Mutex
	statements []string
}

func (t *usageHTTPQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	t.statements = append(t.statements, data.SQL)
	t.mu.Unlock()
	return ctx
}

func (*usageHTTPQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *usageHTTPQueryTrace) take() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	statements := t.statements
	t.statements = nil
	return statements
}

type usageHTTPFixture struct {
	ctx     context.Context
	db      *sql.DB
	trace   *usageHTTPQueryTrace
	manager *security.Manager
	handler http.Handler
}

func newUsageHTTPFixture(t *testing.T) *usageHTTPFixture {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || !testutil.IsIsolatedPostgresDatabase(config.Database) ||
		config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("requires a loopback-only nebula_test database with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	schema := pgx.Identifier{"usage_http_" + uuid.NewString()}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	trace := &usageHTTPQueryTrace{}
	config.RuntimeParams["search_path"] = schema + ",public"
	config.Tracer = trace
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	key := make([]byte, 32)
	manager, err := security.NewManager(key, key, key, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	server := NewServer(controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{}),
		manager, nil, nil, HealthDependencies{}, Config{})
	fixture := &usageHTTPFixture{ctx: ctx, db: db, trace: trace, manager: manager, handler: server.Handler()}
	paths, err := filepath.Glob("../../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("locate PostgreSQL migrations: %v", err)
	}
	for _, path := range paths {
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migrate %s: %v", filepath.Base(path), err)
		}
	}
	return fixture
}

func (f *usageHTTPFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *usageHTTPFixture) account(t *testing.T, role string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.New()
	f.exec(t, `INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Fixture',$2,'fixture',$3,now(),now())`, id, id.String()+"@example.test", role)
	if role == "student" {
		f.exec(t, `INSERT INTO student_profiles(id,user_id,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,now(),now())`, id)
	}
	token, _, err := f.manager.IssueAccessToken(id, role)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func (f *usageHTTPFixture) get(path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(f.ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

func readUsageHTTPData[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if w.Code != http.StatusOK {
		t.Fatalf("want HTTP 200, got %d: %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestStudentUsageEffectiveBalanceHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	student, token := f.account(t, "student")
	org, model := uuid.New(), uuid.New()
	projects := []uuid.UUID{uuid.New(), uuid.New()}
	keys := []uuid.UUID{uuid.New(), uuid.New()}
	month := usage.Month(time.Now())
	f.exec(t, `INSERT INTO organizations(id,name,created_at,updated_at) VALUES ($1,'Fixture',now(),now())`, org)
	f.exec(t, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES ($1,'billed','Billed','active',1,now(),now())`, model)
	store := usage.NewStore(f.db)
	for i, key := range keys {
		project := projects[i]
		name := fmt.Sprintf("Fixture %d", i)
		f.exec(t, `INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
			VALUES ($1,$2,$3,200000,now(),now())`, project, org, name)
		f.exec(t, `INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
			VALUES (gen_random_uuid(),$1,$2,150000,now())`, project, student)
		f.exec(t, `INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
			VALUES ($1,$2,$3,$4,'active',digest($1::uuid::text,'sha256'),'fixture',now(),now(),now())`,
			key, project, student, name)
		f.exec(t, `INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES (gen_random_uuid(),$1,$2,now())`, key, model)
		if _, err := store.Reserve(f.ctx, key, model, key.String(), "openai_responses",
			"/v1/responses", "", "", usage.CallCorrelation{}); err != nil {
			t.Fatalf("gateway rejected fixture Key %d: %v", i, err)
		}
	}
	f.exec(t, `UPDATE project_member_month_credit_buckets SET
		charged_milli=CASE WHEN project_id=$1 THEN 50379 ELSE 74999 END,quota_milli=0
		WHERE user_id=$2 AND month=$3`, projects[0], student, month)

	check := func(firstEffective string) {
		t.Helper()
		view := readUsageHTTPData[usage.StudentUsage](t, f.get("/api/v1/student/usage", token))
		if len(view.Projects) != len(projects) {
			t.Fatalf("expected both projects: %+v", view.Projects)
		}
		memberBalances := []string{"99.620", "75.000"}
		effectiveBalances := []string{firstEffective, "75.000"}
		for i, project := range view.Projects {
			if project.ProjectID != projects[i].String() || project.Available != memberBalances[i] || len(project.Keys) != 1 {
				t.Fatalf("unexpected project balance: %+v", project)
			}
			key := project.Keys[0]
			if key.ID != keys[i].String() || key.Available != "0.000" || key.AvailableEffective != effectiveBalances[i] ||
				key.ProjectMemberAvailable != memberBalances[i] {
				t.Fatalf("Key balance: %+v, want effective=%s, member=%s", key, effectiveBalances[i], memberBalances[i])
			}
		}
	}
	check("99.620")
	f.exec(t, `UPDATE project_month_credit_buckets SET charged_milli=199998,charged_milli_remainder=0.5
		WHERE project_id=$1 AND month=$2`, projects[0], month)
	check("0.0005")
	f.exec(t, `UPDATE project_month_credit_buckets SET charged_milli=199999 WHERE project_id=$1 AND month=$2`, projects[0], month)
	check("0.000")
	_, err := store.Reserve(f.ctx, keys[0], model, "quota-exhausted", "openai_responses", "/v1/responses", "", "", usage.CallCorrelation{})
	var quota *usage.QuotaError
	if !errors.As(err, &quota) || quota.Code != "project_quota_exceeded" {
		t.Fatalf("expected exhausted project to reject the call: %v", err)
	}
}

func TestTeacherStudentUsageUnattributedReceiptHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "teacher")
	student, _ := f.account(t, "student")
	credential, provider := uuid.New(), uuid.New()
	month := time.Date(2024, 12, 31, 16, 0, 0, 0, time.UTC)
	next := month.AddDate(0, 0, 31)
	observed := next.Add(-time.Second)
	org, project, key, model := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	addClaim := func(occurred time.Time) (uuid.UUID, uuid.UUID) {
		t.Helper()
		call, response := uuid.New(), uuid.New()
		f.exec(t, `INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,multiplier_milli,credit_milli,
			created_at,sent_at,finalized_at,beichen_dispatch_kind,beichen_dispatch_credential_id,beichen_dispatch_model,
			beichen_receipt_response_id,beichen_receipt_api_key_id)
			VALUES ($1,$1::uuid::text,$2,$3,$4,$5,$6,$7,'School','Project','Student','Key','Model',
				'openai_responses','/fixture',0,0,$8,$8,$8,'beichen',$9,'upstream',$10,42)`,
			call, usage.Month(occurred), org, project, student, key, model, occurred, credential, response)
		f.exec(t, `INSERT INTO beichen_receipt_response_claims(response_id,first_call_id) VALUES ($1,$2)`, response, call)
		return call, response
	}
	verifiedCall, verified := addClaim(observed)
	_, unique := addClaim(observed)
	march := next.AddDate(0, 0, 28)
	_, ambiguous := addClaim(march.Add(-time.Second))
	byRowTime := next.AddDate(0, 0, 7)
	byRowCall, byRowResponse := addClaim(byRowTime)
	f.exec(t, `INSERT INTO beichen_call_receipts(row_id,call_id,credential_id,response_id,api_key_id,model,occurred_at,
		actual_cost,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,credits_booked,applied)
		VALUES (1,$1,$2,$3,42,'upstream',$4,1,1,1,0,0,0,false),
		       (9,$5,$2,$6,42,'upstream',$7,1,1,1,0,0,0,false)`,
		verifiedCall, credential, verified, observed, byRowCall, byRowResponse, byRowTime)
	archive := func(id int, requestID string, when time.Time, debit any) {
		t.Helper()
		f.exec(t, `INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,
			channel,request_id,model,occurred_at,billing_type,actual_cost,cost_details)
			VALUES (1,$1,42,$2,$3,'Gemini',$4,'upstream',$5,0,$6::numeric,'{}'::jsonb)`,
			id, provider, credential, requestID, when, debit)
	}
	archive(1, "client:"+verified.String(), observed, "1")
	archive(2, "client:"+verified.String(), observed, "0.01999")
	archive(3, "client:"+unique.String(), observed, "1")
	archive(4, "unrelated", observed, "0.25")
	archive(5, "client:"+ambiguous.String(), march.Add(-time.Second), "0.55")
	archive(6, "unknown-debit", observed, nil)
	archive(7, "observed-zero", observed, "0")
	archive(8, "client:"+ambiguous.String(), march, "0.75")
	archive(9, "archived-without-response", byRowTime, "1")

	const route = "/api/v1/teacher/usage/students?month="
	january := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route+"2025-01", token)).UnattributedUpstream
	if january.Rows != 4 || january.DebitRows != 3 || january.BalanceDebit == nil || *january.BalanceDebit != "0.26999" {
		t.Fatalf("January unattributed rows: %+v", january)
	}
	february := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route+"2025-02", token)).UnattributedUpstream
	if february.Rows != 1 || february.DebitRows != 1 || february.BalanceDebit == nil || *february.BalanceDebit != "0.55" {
		t.Fatalf("February candidate must stay unattributed: %+v", february)
	}
	marchResult := readUsageHTTPData[usage.TeacherStudentUsagePage](t, f.get(route+"2025-03", token)).UnattributedUpstream
	if marchResult.Rows != 1 || marchResult.DebitRows != 1 || marchResult.BalanceDebit == nil || *marchResult.BalanceDebit != "0.75" {
		t.Fatalf("the other candidate crosses the month boundary: %+v", marchResult)
	}
}

func TestStudentCatalogBatchHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "student")
	first, second, firstCredential, secondCredential := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ciphertext, err := f.manager.EncryptCredential("synthetic-credential")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES
		($1,'First route','https://api.beichenapi.com',now(),now()),
		($2,'Second route','https://fixture.example.test',now(),now())`, first, second)
	f.exec(t, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES
		($1,$2,'fixture',$5,now(),now()),($3,$4,'fixture',$5,now(),now())`, firstCredential, first, secondCredential, second, ciphertext)
	f.exec(t, `INSERT INTO models(id,model_id,display_name,description,status,credit_multiplier_milli,created_at,updated_at)
		SELECT gen_random_uuid(),'batch-'||lpad(n::text,2,'0'),'Visible '||n,
			CASE WHEN n=23 THEN 'Special description' END,'active',1000,now(),now()
		FROM generate_series(1,24) n`)
	f.exec(t, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at) VALUES
		(gen_random_uuid(),'batch-inactive','Inactive','inactive',1000,now(),now()),
		(gen_random_uuid(),'batch-zero','Free','active',0,now(),now())`)
	retiredProvider, retiredCredential := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO providers(id,name,base_url,created_at,updated_at)
		VALUES($1,'Retired route','https://fixture.example.test',now(),now())`, retiredProvider)
	f.exec(t, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,'retired',$3,now(),now())`, retiredCredential, retiredProvider, ciphertext)
	f.exec(t, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
		SELECT gen_random_uuid(),m.id,b.provider,b.credential,m.model_id,b.adapter,b.priority,now(),now()
		FROM models m CROSS JOIN (VALUES ($1::uuid,$2::uuid,'openai_responses',10),($3::uuid,$4::uuid,'anthropic',20)) b(provider,credential,adapter,priority)
		WHERE m.model_id <> 'batch-24'`, first, firstCredential, second, secondCredential)
	// batch-24 is only reachable through a credential that is retired below.
	f.exec(t, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
		SELECT gen_random_uuid(),m.id,$1,$2,m.model_id,'openai_responses',10,now(),now()
		FROM models m WHERE m.model_id='batch-24'`, retiredProvider, retiredCredential)
	f.exec(t, `INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
		SELECT gen_random_uuid(),b.provider_id,b.credential_id,m.model_id,now(),now(),now(),now()
		FROM models m JOIN model_bindings b ON m.id=b.model_id`)
	f.exec(t, `UPDATE provider_credentials SET status='inactive' WHERE id=$1`, retiredCredential)
	f.exec(t, `INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at)
		VALUES ($1,$2,'batch-23','openai_responses',200,1,now())`, first, firstCredential)

	const route = "/api/v1/student/model-catalog"
	f.trace.take()
	single := readUsageHTTPData[controlplane.ModelCatalogPage](t, f.get(route+"?q=Special+description", token))
	singleSQL := len(f.trace.take())
	page := readUsageHTTPData[controlplane.ModelCatalogPage](t, f.get(route+"?q=%20BATCH-%20&page=2", token))
	pageSQL := len(f.trace.take())
	t.Logf("SQL statements for 1 match / page 2 of 23 matches: %d / %d", singleSQL, pageSQL)
	if singleSQL != pageSQL || pageSQL > 10 {
		t.Fatalf("selects scale with the matching model count: %d / %d", singleSQL, pageSQL)
	}
	if single.Total != 1 || len(single.Items) != 1 || single.Items[0].ModelID != "batch-23" ||
		page.Total != 23 || page.Page != 2 || page.PageSize != 20 || len(page.Items) != 3 {
		t.Fatalf("filtering or pagination changed: single=%+v page=%+v", single, page)
	}
	for i, item := range page.Items {
		if item.ModelID != fmt.Sprintf("batch-%02d", i+21) || item.ProviderName != "First route" ||
			!reflect.DeepEqual(item.RawEntry["protocols"], []any{"anthropic", "openai_responses"}) {
			t.Fatalf("primary binding, order or protocols changed: %+v", item)
		}
	}
	raw := page.Items[2].RawEntry
	if raw["availability_state"] != "available" || !reflect.DeepEqual(raw["available_protocols"], []any{"openai_responses"}) ||
		raw["last_probe_at"] == nil {
		t.Fatalf("page missing stored availability evidence: %+v", raw)
	}
	directory := readUsageHTTPData[controlplane.StudentCatalogPage](t, f.get(route+"?page_size=1", token))
	if len(directory.Items) != 1 || directory.Total != 23 || directory.Directory == nil || len(directory.Directory.Models) != 23 {
		t.Fatalf("directory coverage changed with page size: %+v", directory)
	}
	for _, model := range directory.Directory.Models {
		if model.ModelID == "batch-24" {
			t.Fatalf("retired route leaked into the shared directory: %+v", model)
		}
	}
	addable := directory.Directory.Models[22]
	if addable.ModelID != "batch-23" || addable.WithGrant != "callable" || addable.WithoutGrant != "addable" || !addable.Addable {
		t.Fatalf("eligible model beyond the first page is missing its grant outcomes: %+v", addable)
	}
	unverified := directory.Directory.Models[0]
	if unverified.WithGrant != "unverified" || unverified.WithoutGrant != "addable" || !unverified.Addable ||
		unverified.RawEntry["availability_reason"] == "" || unverified.RawEntry["addable"] != true {
		t.Fatalf("unverified model is missing its explanation: %+v", unverified)
	}
}

func TestStudentCatalogBoundsHTTPPostgres(t *testing.T) {
	f := newUsageHTTPFixture(t)
	_, token := f.account(t, "student")
	const route = "/api/v1/student/model-catalog"
	withinLimit := url.QueryEscape(strings.Repeat("模", 128))
	tooLong := url.QueryEscape(strings.Repeat("模", 129))
	for _, raw := range []string{
		"q=" + tooLong, "q=" + strings.Repeat("+", 129), "q=%00", "q=%FF", "q=%xx",
		"page=0", "page=-1", "page=10001", "page=9223372036854775807", "page=18446744073709551616",
		"page=1%20garbage", "page=", "page_size=0", "page_size=51", "page_size=1.5",
	} {
		w := f.get(route+"?"+raw, token)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"VALIDATION_ERROR"`) {
			t.Errorf("%s: got HTTP %d: %s", raw, w.Code, w.Body)
		}
	}
	view := readUsageHTTPData[controlplane.ModelCatalogPage](t, f.get(route+"?page=10000&page_size=50&q="+withinLimit, token))
	if view.Total != 0 || view.Page != 10000 || view.PageSize != 50 || view.Items == nil || len(view.Items) != 0 {
		t.Fatalf("items or pagination metadata incorrect at valid limits: %+v", view)
	}
}
