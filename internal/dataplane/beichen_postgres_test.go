package dataplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

const (
	studentKey    = "fixture-student-api-key"
	upstreamKey   = "fixture-beichen-api-key"
	publicModel   = "fixture-public"
	upstreamModel = "fixture-g3"
)

type beichenGatewayFixture struct {
	db                                     *sql.DB
	gateway                                *Gateway
	provider, credential, binding, teacher uuid.UUID
}

func newBeichenGatewayFixture(t *testing.T, ctx context.Context) beichenGatewayFixture {
	t.Helper()
	db := openBeichenGatewayTestSchema(t, ctx)
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
		{"model_bindings", "(id,model_id,provider_id,credential_id,upstream_model_name,adapter,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'openai_responses',now(),now())", []any{binding, model, provider, credential, upstreamModel}},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+row.table+" "+row.sql, row.args...); err != nil {
			t.Fatalf("seed %s: %v", row.table, err)
		}
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES ($1,$2,'upstream','base','billing',2000000,10000000,100000,2500000)`, credential, upstreamModel); err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	store := usage.NewStore(db)
	gateway := NewGateway(client, store, nil, manager, Config{MaxRequestBytes: 4096})
	return beichenGatewayFixture{db, gateway, provider, credential, binding, teacher}
}

func TestBeichenPostgresGatewayReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newBeichenGatewayFixture(t, ctx)
	db, gateway, store := f.db, f.gateway, f.gateway.usage
	provider, credential, binding, teacher := f.provider, f.credential, f.binding, f.teacher
	const gmModel = "fixture-gm1"
	const trustedReceipt = "7482ba6e-0219-4a6a-b935-fde623e9baa5"
	const forgedReceipt = "706d4cbe-fb65-4839-8f33-7ae5d878b8f6"
	const outputChunk = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
	for _, tc := range []struct {
		id, contentType, body, state, exact, monthly string
		chargedCount                                 int64
		hit                                          sql.NullInt64
		send                                         bool
	}{
		{"g3-original-sse", "text/event-stream", outputChunk + "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_g3\",\"model\":\"" + upstreamModel + "\",\"status\":\"completed\",\"usage\":{\"input_tokens\":4225,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":4096}}}}\n\n", "pending_review", "", "0", 0, sql.NullInt64{Int64: 4096, Valid: true}, true},
		{"gm1-original-json", "application/json", `{"id":"` + forgedReceipt + `","model":"` + gmModel + `","status":"completed","usage":{"input_tokens":59,"output_tokens":3}}`, "pending_review", "", "0.014352", 1, sql.NullInt64{}, true},
		{"stream-interrupted", "text/event-stream", outputChunk, "pending_review", "", "0.015488", 2, sql.NullInt64{}, true},
		{"not-sent", "", "", "released", "", "0.015488", 2, sql.NullInt64{}, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			servedModel := upstreamModel
			requestLimit, wantBudget := `,"max_output_tokens":5`, int64(5)
			if tc.id == "g3-original-sse" || tc.id == "gm1-original-json" {
				multiplier := "0.2"
				// 10 credits balance and 2 yuan per million output tokens.
				requestLimit, wantBudget = "", 50_000
				if tc.id == "gm1-original-json" {
					requestLimit, wantBudget = `,"max_output_tokens":3`, 3
					servedModel, multiplier = gmModel, "0.08"
					if _, err := db.ExecContext(ctx, `
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES ($1,$2,'upstream','base','billing',2000000,8000000,40000,0)`, credential, servedModel); err != nil {
						t.Fatal(err)
					}
					if _, err := db.ExecContext(ctx, `
INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, credential, servedModel); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES ($1,$2::numeric)", credential, multiplier); err != nil {
					t.Fatal(err)
				}
			} else {
				servedModel = gmModel
			}
			if _, err := db.ExecContext(ctx, "UPDATE model_bindings SET upstream_model_name=$2 WHERE id=$1", binding, servedModel); err != nil {
				t.Fatal(err)
			}
			var observedSend time.Time
			gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				defer r.Body.Close()
				if r.Method != http.MethodPost || r.URL.String() != "https://api.beichenapi.com/v1/responses" ||
					r.Header.Get("Authorization") != "Bearer "+upstreamKey {
					t.Fatal("gateway did not use the fixture Beichen route and credential")
				}
				if r.Header.Get(beichenReceiptHeader) != "" {
					t.Fatal("student-supplied receipt reached the upstream")
				}
				var payload struct {
					Model           string `json:"model"`
					MaxOutputTokens int64  `json:"max_output_tokens"`
				}
				stream := tc.contentType == "text/event-stream"
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != servedModel {
					t.Fatal("gateway did not rewrite the model")
				}
				if payload.MaxOutputTokens != wantBudget {
					t.Fatalf("upstream max_output_tokens=%d, want %d", payload.MaxOutputTokens, wantBudget)
				}
				if !tc.send {
					return nil, io.ErrClosedPipe
				}
				observedSend = time.Now().Truncate(time.Microsecond)
				httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				body := io.Reader(strings.NewReader(tc.body))
				if stream {
					body = io.MultiReader(strings.NewReader(outputChunk), beichenReadOnce(func() io.Reader {
						var state string
						var open bool
						err := db.QueryRowContext(ctx, `SELECT beichen_state, beichen_credit_exact IS NULL FROM gateway_calls WHERE request_id=$1`, tc.id).Scan(&state, &open)
						if err != nil || state != "reserved" || !open {
							t.Fatalf("before terminal event: state=%q uncharged=%t error=%v", state, open, err)
						}
						return strings.NewReader(strings.TrimPrefix(tc.body, outputChunk))
					}))
				}
				headers := http.Header{"Content-Type": {tc.contentType}}
				switch tc.id {
				case "g3-original-sse":
					headers.Set(beichenReceiptHeader, trustedReceipt)
				case "stream-interrupted":
					headers.Add(beichenReceiptHeader, trustedReceipt)
					headers.Add(beichenReceiptHeader, forgedReceipt)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(body)}, nil
			})
			r := httptest.NewRequest(http.MethodPost, "/v1/responses",
				strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":%t,"input":"Fixture prompt"%s}`, publicModel, tc.contentType == "text/event-stream", requestLimit))).
				WithContext(usage.WithRequestID(ctx, tc.id))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+studentKey)
			r.Header.Set(beichenReceiptHeader, forgedReceipt)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, r)
			if tc.send && (w.Code != http.StatusOK || w.Body.String() != tc.body || (tc.contentType == "text/event-stream" && !w.Flushed)) {
				t.Fatalf("fixture response: status=%d body=%q", w.Code, w.Body.String())
			}
			if !tc.send && w.Code != http.StatusBadGateway {
				t.Fatalf("not sent: status=%d", w.Code)
			}
			var budget int64
			var truncated bool
			if err := db.QueryRowContext(ctx, "SELECT quota_budget_tokens,truncated_quota FROM gateway_calls WHERE request_id=$1", tc.id).
				Scan(&budget, &truncated); err != nil || budget != wantBudget || truncated != (tc.id == "gm1-original-json") {
				t.Fatalf("quota budget=%d truncated=%t: %v", budget, truncated, err)
			}
			var state, protocol, gotModel string
			var exact, review sql.NullString
			var sent sql.NullTime
			var hit, cacheWrite, input, output sql.NullInt64
			var receipt uuid.NullUUID
			var callID, gotCredential, gotBinding uuid.UUID
			err := db.QueryRowContext(ctx, `SELECT id,beichen_state,protocol,beichen_credit_exact::text,
				beichen_review_reason,sent_at,cache_hit_tokens,cache_write_tokens,provider_credential_id,binding_id,upstream_model_name,
				prompt_tokens,completion_tokens,beichen_receipt_response_id
				FROM gateway_calls WHERE request_id=$1`, tc.id).
				Scan(&callID, &state, &protocol, &exact, &review, &sent, &hit, &cacheWrite, &gotCredential, &gotBinding, &gotModel, &input, &output, &receipt)
			if err != nil {
				t.Fatal(err)
			}
			wantReceipt := uuid.NullUUID{}
			if tc.id == "g3-original-sse" {
				wantReceipt = uuid.NullUUID{UUID: uuid.MustParse(trustedReceipt), Valid: true}
			}
			if receipt != wantReceipt {
				t.Fatalf("receipt = %v, want %v", receipt, wantReceipt)
			}
			if state != tc.state || protocol != "openai_responses" || strings.TrimRight(exact.String, "0") != tc.exact ||
				exact.Valid != (tc.exact != "") || review.Valid != (tc.state == "pending_review") ||
				sent.Valid != tc.send || (sent.Valid && sent.Time.Before(observedSend)) || hit != tc.hit ||
				cacheWrite.Valid || gotCredential != credential || gotBinding != binding || gotModel != servedModel {
				t.Fatalf("gateway call: state=%q protocol=%q charge=%v review=%v sent=%v hit=%v credential=%s binding=%s",
					state, protocol, exact, review, sent, hit, gotCredential, gotBinding)
			}
			assertBeichenGatewayMonth(t, ctx, db, tc.id, tc.monthly, tc.chargedCount)
			if tc.id != "g3-original-sse" && tc.id != "gm1-original-json" {
				return
			}
			wantInput, wantOutput := int64(4225), int64(5)
			if tc.id == "gm1-original-json" {
				wantInput, wantOutput = 59, 3
			}
			if !input.Valid || !output.Valid || input.Int64 != wantInput || output.Int64 != wantOutput {
				t.Fatalf("original usage: input=%v output=%v", input, output)
			}
			t.Run("confirmed-bill-replay", func(t *testing.T) {
				// Keep confirmed billing data separate from the original protocol fixture.
				input, output, hit, write := int64(4225), int64(5), int64(4096), int64(0)
				want, total, count := "0.014352", "0.014352", int64(1)
				if tc.id == "gm1-original-json" {
					input, output, hit = 59, 3, 0
					want, total, count = "0.001136", "0.015488", 2
				}
				report := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &input, CompletionTokens: &output, CacheHitTokens: &hit, CacheWriteTokens: &write}
				for range 2 {
					if err := store.CompleteCallMetrics(ctx, callID, report); err != nil {
						t.Fatal(err)
					}
					var charged bool
					if err := db.QueryRowContext(ctx, `SELECT COALESCE(beichen_state='charged' AND beichen_credit_exact=$2::numeric AND beichen_review_reason IS NULL,false)
FROM gateway_calls WHERE id=$1`, callID, want).Scan(&charged); err != nil || !charged {
						t.Fatalf("confirmed bill should charge exactly %s: %v", want, err)
					}
					assertBeichenGatewayMonth(t, ctx, db, tc.id, total, count)
				}
			})

		})
	}
	testBeichenResponsesSocket(t, ctx, db, gateway, publicModel, gmModel, studentKey, upstreamKey, credential)
	if _, err := db.ExecContext(ctx, "UPDATE model_bindings SET adapter='openai_realtime' WHERE id=$1", binding); err != nil {
		t.Fatal(err)
	}
	testBeichenRealtimeConnection(t, ctx, db, gateway, publicModel, gmModel, studentKey, upstreamKey, credential)
	testBeichenHTTPConditions(t, ctx, db, gateway, binding, credential, publicModel, gmModel, studentKey)
	t.Run("41-call-token-audit", func(t *testing.T) {
		testBeichen41CallTokenAudit(t, ctx, db, gateway, provider, credential, binding, teacher, publicModel, gmModel, studentKey)
	})
}

func testBeichenHTTPConditions(t *testing.T, ctx context.Context, db *sql.DB, gateway *Gateway, binding, credential uuid.UUID, publicModel, upstreamModel, studentKey string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `WITH multiplier AS (
		INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,1)
		RETURNING credential_id,id,effective_at
	)
	INSERT INTO beichen_panel_snapshots
		(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
	SELECT c.id,m.effective_at,c.updated_at,1,now()+interval '1 hour',m.id,
		jsonb_build_object($2::text,'{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,
		  "cache_read_price":0.0000005,"cache_write_price":0.000001,"cache_write_1h_price":0.000003,
		  "reasoning_effort_multipliers":{"medium":1,"high":2}}'::jsonb), sha256(c.credential_ciphertext)
	FROM multiplier m JOIN provider_credentials c ON c.id=m.credential_id`, credential, upstreamModel); err != nil {
		t.Fatal(err)
	}
	const chatUsage = `"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":2,"prompt_cache_write_tokens":3}`
	const responsesUsage = `"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}`
	const messagesUsage = `"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}`
	const messagesNoWrite = `"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`
	const messagesChildConflict = `"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":3}}`
	for _, tc := range []struct {
		name, adapter, path, input, report string
		effort, ttl, credits, review       string
	}{
		{"chat-high", "openai_compatible", "/v1/chat/completions", `"messages":[],"reasoning_effort":"high"`, chatUsage, "high", "", "0.0026", ""},
		{"responses-default", "openai_responses", "/v1/responses", `"input":"fixture"`, responsesUsage, "medium", "", "0.0013", ""},
		{"responses-large-output-limit", "openai_responses", "/v1/responses", `"input":"fixture","max_output_tokens":8`,
			`"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":33267,"output_tokens":8,"prompt_cache_miss_tokens":33267}`,
			"medium", "", "3.3283", ""},
		{"responses-large-cache-unconfirmed", "openai_responses", "/v1/responses", `"input":"fixture"`,
			`"status":"completed","usage":{"input_tokens":33267,"output_tokens":8,"input_tokens_details":{"cached_tokens":32768}}`,
			"medium", "", "1.6899", ""},
		{"responses-content-filter", "openai_responses", "/v1/responses", `"input":"fixture"`,
			`"status":"incomplete","incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}`,
			"medium", "", "", "missing_terminal_usage"},
		{"responses-absent-tier", "openai_responses", "/v1/responses", `"input":"fixture","reasoning":{"effort":"low"}`, responsesUsage, "low", "", "", "missing_reasoning_effort_multiplier"},
		{"nov23-chat-1", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":4389,"completion_tokens":5,"total_tokens":4394,"prompt_tokens_details":{"cached_tokens":4224}}`, "medium", "", "0.2287", ""},
		{"nov23-chat-2", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":6587,"completion_tokens":5,"total_tokens":6592,"prompt_tokens_details":{"cached_tokens":4352}}`, "medium", "", "0.4421", ""},
		{"nov23-chat-3", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":7571,"completion_tokens":5,"total_tokens":7576}`, "medium", "", "", "incomplete_token_breakdown"},
		{"nov23-chat-4", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":4161,"completion_tokens":5,"total_tokens":4166,"prompt_tokens_details":{"cached_tokens":3968}}`, "medium", "", "0.2187", ""},
		{"nov23-chat-5", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":58,"completion_tokens":2,"total_tokens":60}`, "medium", "", "", "incomplete_token_breakdown"},
		{"nov23-chat-6", "openai_compatible", "/v1/chat/completions", `"messages":[]`,
			`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":30140,"completion_tokens":6,"total_tokens":30146}`, "medium", "", "", "incomplete_token_breakdown"},
		{"messages-1h", "anthropic", "/v1/messages", `"max_tokens":10,"thinking":{"effort":"high"},"messages":[{"role":"user","content":[{"type":"text","text":"fixture","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`, messagesUsage, "high", "1h", "0.0038", ""},
		{"messages-5m", "anthropic", "/v1/messages", `"max_tokens":10,"messages":[],"cache_control":{"type":"ephemeral"}`, messagesUsage, "medium", "5m", "0.0013", ""},
		{"messages-unknown-ttl", "anthropic", "/v1/messages", `"max_tokens":10,"messages":[]`, messagesUsage, "medium", "", "", "unknown_cache_ttl"},
		{"messages-no-writes", "anthropic", "/v1/messages", `"max_tokens":10,"messages":[]`, messagesNoWrite, "medium", "", "0.001", ""},
		{"messages-cache-child-conflict", "anthropic", "/v1/messages", `"max_tokens":10,"messages":[],"cache_control":{"type":"ephemeral"}`, `"stop_reason":"end_turn",` + messagesChildConflict, "medium", "5m", "", "incomplete_token_breakdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, "UPDATE model_bindings SET adapter=$2 WHERE id=$1", binding, tc.adapter); err != nil {
				t.Fatal(err)
			}
			gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				defer r.Body.Close()
				if r.URL.Path != tc.path {
					t.Fatalf("unexpected fixture path %q", r.URL.Path)
				}
				httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				body := fmt.Sprintf(`{"model":%q,%s}`, upstreamModel, tc.report)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(`{"model":%q,%s}`, publicModel, tc.input))).
				WithContext(usage.WithRequestID(ctx, tc.name))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+studentKey)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("fixture gateway status=%d", w.Code)
			}
			var effort, ttl, credits, review, state string
			err := db.QueryRowContext(ctx, `SELECT reasoning_effort,coalesce(cache_ttl,''),
				coalesce(trim_scale(beichen_credit_exact)::text,''),coalesce(beichen_review_reason,''),beichen_state
				FROM gateway_calls WHERE request_id=$1`, tc.name).Scan(&effort, &ttl, &credits, &review, &state)
			wantState := "charged"
			if tc.review != "" {
				wantState = "pending_review"
			}
			if err != nil || effort != tc.effort || ttl != tc.ttl || credits != tc.credits || review != tc.review || state != wantState {
				t.Fatalf("ledger effort=%q TTL=%q credits=%q review=%q state=%q: %v", effort, ttl, credits, review, state, err)
			}
		})
	}
	var conflictReason string
	var conflictWrite sql.NullInt64
	var conflictCredits sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT coalesce(usage_reason,''),cache_write_tokens,beichen_credit_exact::text
		FROM gateway_calls WHERE request_id='messages-cache-child-conflict'`).
		Scan(&conflictReason, &conflictWrite, &conflictCredits); err != nil ||
		conflictReason != "conflicting_cache_creation_counts" || !conflictWrite.Valid || conflictWrite.Int64 != 3 || conflictCredits.Valid {
		t.Fatalf("conflicting Anthropic cache child: reason=%q write=%v credits=%v err=%v", conflictReason, conflictWrite, conflictCredits, err)
	}
	var charged, pending, partial, missing int
	err := db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE beichen_state='charged' AND pricing_detail->>'amount_source'='estimated_unique_amount'
		  AND pricing_detail->'input_tokens'='null'::jsonb AND pricing_detail->'cache_write_tokens'='null'::jsonb
		  AND (pricing_detail->>'unsplit_input_tokens')::bigint=prompt_tokens-cache_hit_tokens),
		count(*) FILTER (WHERE beichen_state='pending_review' AND cache_hit_tokens IS NULL AND actual_credits IS NULL),
		count(*) FILTER (WHERE usage_state='partial' AND cache_miss_tokens IS NULL AND cache_write_tokens IS NULL),
		sum(prompt_tokens-coalesce(cache_hit_tokens,0))
		FROM gateway_calls WHERE request_id LIKE 'nov23-chat-%'`).Scan(&charged, &pending, &partial, &missing)
	if err != nil || charged != 3 || pending != 3 || partial != 6 || missing != 40362 {
		t.Fatalf("Nov 23 Chat replay: charged=%d pending=%d partial=%d unsplit=%d: %v", charged, pending, partial, missing, err)
	}

}

func testBeichenRealtimeConnection(t *testing.T, ctx context.Context, db *sql.DB, gateway *Gateway, publicModel, upstreamModel, studentKey, upstreamKey string, credential uuid.UUID) {
	t.Helper()
	t.Run("realtime-multiple-server-responses", func(t *testing.T) {
		const requestID = "realtime-connection"
		const appendAudio = `{"type":"input_audio_buffer.append","audio":"AA=="}`
		events := []string{
			`{"type":"response.done","response":{"id":"resp_realtime_a","usage":{"input_tokens":10,"output_tokens":2}}}`,
			`{"type":"response.done","response":{"id":"resp_realtime_b","usage":{"input_tokens":8,"output_tokens":3}}}`,
		}
		upstreamErr := make(chan error, 1)
		upgrader := websocket.Upgrader{}
		upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/realtime" || r.URL.Query().Get("model") != upstreamModel ||
				r.Header.Get("Authorization") != "Bearer "+upstreamKey {
				upstreamErr <- fmt.Errorf("unexpected Realtime upstream route")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				upstreamErr <- err
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil || string(data) != appendAudio {
				upstreamErr <- fmt.Errorf("audio frame was not proxied: %v", err)
				return
			}
			for _, event := range events {
				if err = conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
					break
				}
			}
			upstreamErr <- err
		}))
		defer upstream.Close()
		useBeichenWebSocketFixture(gateway, upstream)
		finished := make(chan struct{})
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(finished)
			gateway.ServeHTTP(w, r.WithContext(usage.WithRequestID(r.Context(), requestID)))
		}))
		defer front.Close()
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+
			"/v1/realtime?model="+url.QueryEscape(publicModel), http.Header{"Authorization": {"Bearer " + studentKey}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(appendAudio)); err != nil {
			t.Fatal(err)
		}
		for _, want := range events {
			_, data, err := conn.ReadMessage()
			if err != nil || string(data) != want {
				t.Fatalf("Realtime terminal event was not proxied: %v", err)
			}
		}
		if err := <-upstreamErr; err != nil {
			t.Fatal(err)
		}
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var count int
		var ok bool
		err = db.QueryRowContext(ctx, `SELECT count(*), COALESCE(bool_and(request_id=$1 AND beichen_state='pending_review'
			AND sent_at IS NOT NULL AND provider_credential_id=$2 AND upstream_model_name=$3
			AND beichen_price_at IS NULL AND beichen_credit_exact IS NULL AND beichen_receipt_response_id IS NULL
			AND prompt_tokens=18 AND completion_tokens=5),false)
			FROM gateway_calls WHERE protocol='openai_realtime'`, requestID, credential, upstreamModel).Scan(&count, &ok)
		if err != nil || count != 1 || !ok {
			t.Fatalf("expected one attributed, uncharged Realtime connection: count=%d valid=%t error=%v", count, ok, err)
		}
		assertBeichenGatewayMonth(t, ctx, db, requestID, "0.016624", 3)
	})
}

func useBeichenWebSocketFixture(gateway *Gateway, upstream *httptest.Server) {
	gateway.dialer.Proxy = nil
	gateway.dialer.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	gateway.dialer.TLSClientConfig.ServerName = "127.0.0.1"
	gateway.dialer.NetDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
}

func testBeichenResponsesSocket(t *testing.T, ctx context.Context, db *sql.DB, gateway *Gateway, publicModel, upstreamModel, studentKey, upstreamKey string, credential uuid.UUID) {
	t.Helper()
	for _, tc := range []struct {
		id, terminal, usage, state, monthly string
		chargedCount                        int64
	}{
		{"ws-terminal", `{"type":"response.completed","response":{"id":"resp_ws","status":"completed","usage":{"input_tokens":59,"output_tokens":3}}}`,
			"partial", "pending_review", "0.015488", 2},
		{"ws-interrupted", "", "interrupted", "pending_review", "0.015488", 2},
		{"ws-output-limit", fmt.Sprintf(`{"type":"response.incomplete","response":{"id":"resp_ws","model":%q,"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":59,"output_tokens":3,"prompt_cache_miss_tokens":59}}}`, upstreamModel),
			"reported", "charged", "0.016624", 3},
	} {
		id := tc.id
		terminal := tc.terminal != ""
		t.Run(id, func(t *testing.T) {
			upstreamErr := make(chan error, 1)
			upgrader := websocket.Upgrader{}
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+upstreamKey {
					upstreamErr <- fmt.Errorf("unexpected upstream route")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				conn, err := upgrader.Upgrade(w, r, http.Header{beichenReceiptHeader: {uuid.NewString()}})
				if err != nil {
					upstreamErr <- err
					return
				}
				defer conn.Close()
				var payload struct{ Model string }
				if err = conn.ReadJSON(&payload); err != nil || payload.Model != upstreamModel {
					upstreamErr <- fmt.Errorf("unexpected WS model %q: %v", payload.Model, err)
					return
				}
				if terminal {
					for _, event := range []string{
						`{"type":"response.created","response":{"id":"resp_ws"}}`,
						`{"type":"response.completed","response":{"id":"resp_other","status":"completed","usage":{"input_tokens":100,"output_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}}`,
						tc.terminal,
					} {
						if err = conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
							break
						}
					}
				}
				upstreamErr <- err
			}))
			defer upstream.Close()
			useBeichenWebSocketFixture(gateway, upstream)
			finished := make(chan struct{})
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				gateway.ServeHTTP(w, r.WithContext(usage.WithRequestID(r.Context(), id)))
			}))
			defer front.Close()
			conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + studentKey}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err = conn.WriteJSON(map[string]string{"type": "response.create", "model": publicModel, "input": "fixture"}); err != nil {
				t.Fatal(err)
			}
			if terminal {
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				for range 3 {
					if _, _, err = conn.ReadMessage(); err != nil {
						t.Fatal(err)
					}
				}
			}
			select {
			case err = <-upstreamErr:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var ok bool
			err = db.QueryRowContext(ctx, `SELECT beichen_state=$4 AND protocol='openai_responses'
				AND usage_state=$2 AND sent_at IS NOT NULL AND provider_credential_id=$3
				AND beichen_receipt_response_id IS NULL
				AND reasoning_effort='medium'
				AND ($2 <> 'reported' OR (prompt_tokens=59 AND completion_tokens=3 AND beichen_credit_exact=0.001136))
				FROM gateway_calls WHERE request_id=$1`, id, tc.usage, credential, tc.state).Scan(&ok)
			if err != nil || !ok {
				t.Fatalf("unexpected WS ledger for %s/%s: %v", tc.usage, tc.state, err)
			}
			assertBeichenGatewayMonth(t, ctx, db, id, tc.monthly, tc.chargedCount)
		})
	}
}

func assertBeichenGatewayMonth(t *testing.T, ctx context.Context, db *sql.DB, requestID, monthly string, chargedCount int64) {
	t.Helper()
	var monthlyOK bool
	err := db.QueryRowContext(ctx, `
SELECT (COALESCE(m.charged_milli+m.charged_milli_remainder,0))=($2::numeric*1000)
   AND COALESCE(m.charged_count,0)=$3
   AND (p.charged_milli+p.charged_milli_remainder)=($2::numeric*1000) AND p.pending_milli=0
   AND (u.charged_milli+u.charged_milli_remainder)=($2::numeric*1000) AND u.pending_milli=0
   AND (k.charged_milli+k.charged_milli_remainder)=($2::numeric*1000) AND k.pending_milli=0
FROM gateway_calls c
LEFT JOIN monthly_usage_cube m ON (m.api_key_id,m.model_id,m.month)=(c.api_key_id,c.model_id,c.month)
JOIN project_month_credit_buckets p ON (p.project_id,p.month)=(c.project_id,c.month)
JOIN project_member_month_credit_buckets u ON (u.project_id,u.user_id,u.month)=(c.project_id,c.user_id,c.month)
JOIN api_key_month_credit_buckets k ON (k.api_key_id,k.month)=(c.api_key_id,c.month)
WHERE c.request_id=$1`, requestID, monthly, chargedCount).Scan(&monthlyOK)
	if err != nil || !monthlyOK {
		t.Fatalf("monthly balance does not equal %s for %d charged calls: %v", monthly, chargedCount, err)
	}
}

type beichenDeferredReader struct {
	open   func() io.Reader
	reader io.Reader
}

func beichenReadOnce(open func() io.Reader) io.Reader {
	return &beichenDeferredReader{open: open}
}

func (r *beichenDeferredReader) Read(p []byte) (int, error) {
	if r.reader == nil {
		r.reader = r.open()
	}
	return r.reader.Read(p)
}

type beichenGatewayTestTransport func(*http.Request) (*http.Response, error)

func (f beichenGatewayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// NewBeichenActivationFixture lets the external HTTP integration test exercise
// the real gateway with an in-memory upstream and an isolated database.
func NewBeichenActivationFixture(t *testing.T, ctx context.Context, manager *security.Manager, transport func(*http.Request) (*http.Response, error)) (*sql.DB, *ent.Client, *Gateway) {
	t.Helper()
	db := openBeichenGatewayTestSchema(t, ctx)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	gateway := NewGateway(client, usage.NewStore(db), nil, manager, Config{MaxRequestBytes: 4096})
	gateway.client.Transport = beichenGatewayTestTransport(transport)
	return db, client, gateway
}

func openBeichenGatewayTestSchema(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES opts in to PostgreSQL gateway reconciliation")
	}
	const required = "test requires the disposable loopback gateway or rollout test database"
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/nebula_test" ||
		u.User == nil || (u.User.Username() != "nebula_qa" && u.User.Username() != "nebula_test") ||
		u.RawQuery != "sslmode=disable" || u.Fragment != "" {
		t.Fatal(required)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || config.Database != "nebula_test" ||
		config.User != u.User.Username() || fmt.Sprint(config.Port) != u.Port() || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
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
