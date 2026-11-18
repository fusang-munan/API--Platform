package dataplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenPostgresGatewayReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := openBeichenGatewayTestSchema(t, ctx)

	const studentKey = "fixture-student-api-key"
	const upstreamKey = "fixture-beichen-api-key"
	const publicModel = "fixture-public"
	const upstreamModel = "fixture-upstream"
	const requestID = "fixture-beichen-gateway"
	const upstreamResponseID = "chatcmpl-fixture-beichen"
	const sse = "data: {\"id\":\"" + upstreamResponseID + "\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"id\":\"" + upstreamResponseID + "\",\"choices\":[],\"usage\":{\"prompt_tokens\":12000,\"completion_tokens\":1000,\"total_tokens\":13000,\"prompt_tokens_details\":{\"cached_tokens\":2000}}}\n\n" +
		"data: [DONE]\n\n"
	manager, err := security.NewManager(nil, nil, []byte("fixture-key-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := manager.EncryptCredential(upstreamKey)
	if err != nil {
		t.Fatal(err)
	}
	student, teacher, org, project, key, model := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	provider, credential, binding := uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		table string
		sql   string
		args  []any
	}{
		{"users", `(id,name,email,password_hash,role,created_at,updated_at) VALUES
			($1,'Student','student@example.test','fixture','student',now(),now()),
			($2,'Teacher','teacher@example.test','fixture','teacher',now(),now())`, []any{student, teacher}},
		{"organizations", "(id,name,created_at,updated_at) VALUES($1,'Fixture org',now(),now())", []any{org}},
		{"projects", "(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at) VALUES($1,$2,'Fixture project',10000,now(),now())", []any{project, org}},
		{"project_members", "(id,project_id,user_id,monthly_credit_quota_milli,created_at) VALUES(gen_random_uuid(),$1,$2,10000,now())", []any{project, student}},
		{"api_keys", "(id,owner_user_id,project_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at) VALUES($1,$2,$3,'Fixture key','active',$4,'fixture',now(),now(),now())", []any{key, student, project, manager.HashAPIKey(studentKey)}},
		{"models", "(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at) VALUES($1,$2,'Fixture model','active',1250,now(),now())", []any{model, publicModel}},
		{"api_key_models", "(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())", []any{key, model}},
		{"providers", "(id,name,base_url,created_at,updated_at) VALUES($1,'Fixture Beichen','https://api.beichenapi.com',now(),now())", []any{provider}},
		{"provider_credentials", "(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'fixture',$3,now(),now())", []any{credential, provider, ciphertext}},
		{"provider_model_catalog", "(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())", []any{provider, credential, upstreamModel}},
		{"model_bindings", "(id,model_id,provider_id,credential_id,upstream_model_name,adapter,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'openai_compatible',now(),now())", []any{binding, model, provider, credential, upstreamModel}},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+row.table+" "+row.sql, row.args...); err != nil {
			t.Fatalf("seed %s: %v", row.table, err)
		}
	}

	north := beichen.New(db, manager)
	err = north.Import(ctx, provider, credential, teacher, "prices",
		"model,input,output,cache_read,cache_write\n"+upstreamModel+",2000000,3000000,500000,0\n")
	if err != nil {
		t.Fatalf("import upstream price before gateway request: %v", err)
	}

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	gateway := NewGateway(client, usage.NewStore(db), nil, manager, Config{MaxRequestBytes: 4096})
	var sentAfter, sentBefore time.Time
	gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		if r.Method != http.MethodPost || r.URL.String() != "https://api.beichenapi.com/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer "+upstreamKey {
			t.Fatal("gateway did not use the fixture Beichen route and credential")
		}
		var payload struct {
			Model         string `json:"model"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != upstreamModel || !payload.StreamOptions.IncludeUsage {
			t.Fatal("gateway did not rewrite the model and request stream usage")
		}
		trace := httptrace.ContextClientTrace(r.Context())
		if trace == nil || trace.WroteRequest == nil {
			t.Fatal("gateway did not track the upstream send time")
		}
		sentAfter = time.Now().Truncate(time.Microsecond)
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		sentBefore = time.Now()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(sse))}, nil
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+publicModel+`","stream":true,"messages":[{"role":"user","content":"Fixture prompt"}]}`)).
		WithContext(usage.WithRequestID(ctx, requestID))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+studentKey)
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != sse || !w.Flushed {
		t.Fatalf("gateway did not flush the entire fixture stream, status %d, body %q", w.Code, w.Body.String())
	}

	var gotCredential, gotBinding uuid.UUID
	var state, outcome, billing, modelName, responseID string
	var sentAt time.Time
	var charged, calls int64
	var rawCacheWrites sql.NullInt64
	err = db.QueryRowContext(ctx, `
SELECT c.provider_credential_id,c.binding_id,c.upstream_model_name,c.upstream_response_id,c.usage_state,c.outcome,
       c.billing_state,c.sent_at,m.charged_milli,m.charged_count,c.cache_write_tokens
FROM gateway_calls c JOIN monthly_usage_cube m ON m.api_key_id=c.api_key_id AND m.model_id=c.model_id AND m.month=c.month
WHERE c.request_id=$1 AND c.provider_id=$2`, requestID, provider).Scan(
		&gotCredential, &gotBinding, &modelName, &responseID, &state, &outcome, &billing, &sentAt, &charged, &calls, &rawCacheWrites)
	if err != nil {
		t.Fatalf("read settled gateway call: %v", err)
	}
	if gotCredential != credential || gotBinding != binding || modelName != upstreamModel || responseID != upstreamResponseID ||
		state != "reported" || outcome != "succeeded" || billing != "charged" ||
		sentAt.Before(sentAfter) || sentAt.After(sentBefore) || charged != 1250 || calls != 1 {
		t.Fatalf("unexpected gateway ledger: model=%s response=%s state=%s outcome=%s billing=%s sent_at=%s charged=%d calls=%d",
			modelName, responseID, state, outcome, billing, sentAt, charged, calls)
	}
	if rawCacheWrites.Valid {
		t.Fatalf("omitted cache writes must stay NULL in the gateway ledger, got %d", rawCacheWrites.Int64)
	}

	day := sentAt.In(time.FixedZone("Beijing", 8*60*60)).Format("2006-01-02")
	err = north.Import(ctx, provider, credential, teacher, "usage",
		"date,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros\n"+
			fmt.Sprintf("%s,%s,1,10000,1000,2000,,24001\n", day, upstreamModel))
	if err != nil {
		t.Fatalf("import external usage snapshot: %v", err)
	}
	view, err := north.Overview(ctx, provider, day)
	if err != nil {
		t.Fatalf("read Beichen overview: %v", err)
	}
	if len(view.Comparison) != 1 || view.Comparison[0].CredentialID != credential || view.Comparison[0].Model != upstreamModel {
		t.Fatalf("overview did not join the single gateway call and external snapshot: %+v", view.Comparison)
	}
	row := view.Comparison[0]
	if row.UpstreamCacheWriteTokens != nil {
		t.Errorf("omitted cache writes must stay null in the external snapshot, got %d", *row.UpstreamCacheWriteTokens)
	}
	for _, want := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"local requests", row.LocalRequests, 1}, {"upstream requests", row.UpstreamRequests, 1},
		{"local uncached input", row.LocalInputTokens, 10000}, {"upstream uncached input", row.UpstreamInputTokens, 10000},
		{"local output", row.LocalOutputTokens, 1000}, {"upstream output", row.UpstreamOutputTokens, 1000},
		{"local cache hits", row.LocalCacheReadTokens, 2000}, {"upstream cache hits", row.UpstreamCacheReadTokens, 2000},
		{"OpenAI-compatible comparison cache writes", row.LocalCacheWriteTokens, 0},
		{"estimated upstream micros", row.EstimatedUpstreamMicros, 24000}, {"billed upstream micros", row.BilledUpstreamMicros, 24001},
		{"trial credits milli", row.TrialCreditMilli, 24}, {"unpriced calls", row.TrialUnpricedCalls, 0},
	} {
		if want.got == nil {
			t.Errorf("%s is null, want %d", want.name, want.want)
		} else if *want.got != want.want {
			t.Errorf("%s = %d, want %d", want.name, *want.got, want.want)
		}
	}
}

type beichenGatewayTestTransport func(*http.Request) (*http.Response, error)

func (f beichenGatewayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func openBeichenGatewayTestSchema(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES opts in to PostgreSQL gateway reconciliation")
	}
	const required = "test requires a PostgreSQL URL for nebula_qa@127.0.0.1:55439/nebula_test?sslmode=disable"
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Host != "127.0.0.1:55439" || u.Path != "/nebula_test" || u.User.Username() != "nebula_qa" ||
		u.RawQuery != "sslmode=disable" || u.Fragment != "" {
		t.Fatal(required)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || config.Port != 55439 || config.Database != "nebula_test" ||
		config.User != "nebula_qa" || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal(required)
	}
	schema := pgx.Identifier{"beichen_gateway_" + uuid.NewString()}.Sanitize()
	config.RuntimeParams = map[string]string{"search_path": schema}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("could not connect to the disposable loopback PostgreSQL database")
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop isolated test schema: %v", err)
		}
	})

	// Use the migration DDL without registering database-wide extensions.
	// The fixture uses exact lowercase lookups and does not search prompt text.
	localDDL := strings.NewReplacer(
		"CREATE EXTENSION IF NOT EXISTS citext;", "CREATE DOMAIN citext AS text;",
		"CREATE EXTENSION IF NOT EXISTS pgcrypto;", "", // gen_random_uuid is built into supported PostgreSQL.
		"CREATE EXTENSION IF NOT EXISTS pg_trgm;", "",
		"CREATE INDEX monitored_inputs_content_trgm ON monitored_inputs USING gin(content gin_trgm_ops) WHERE visible;", "",
	)
	files, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("could not find PostgreSQL migrations for the isolated schema")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		script := localDDL.Replace(string(raw))
		if strings.Contains(strings.ToUpper(script), "CREATE EXTENSION") {
			t.Fatalf("unexpected database-wide extension in %s", filepath.Base(file))
		}
		if _, err := db.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply %s in isolated schema: %v", filepath.Base(file), err)
		}
	}
	return db
}
