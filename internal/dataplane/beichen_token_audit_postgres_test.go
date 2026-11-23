package dataplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func testBeichen41CallTokenAudit(t *testing.T, ctx context.Context, db *sql.DB, gateway *Gateway, provider, credential, binding, teacher uuid.UUID, publicModel, upstreamModel, studentKey string) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
	     VALUES($1,'GM1',decode(repeat('12',32),'hex'),false)`, credential)
	exec(`INSERT INTO beichen_panel_access(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
	     SELECT id,1,51,decode('ab','hex'),$2,updated_at FROM provider_credentials WHERE id=$1`, credential, teacher)
	exec(`WITH multiplier AS (
		INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,1.5)
		RETURNING credential_id,id,effective_at
	)
	INSERT INTO beichen_panel_snapshots
		(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
	SELECT c.id,m.effective_at,c.updated_at,1,now()+interval '1 hour',m.id,
		jsonb_build_object($2::text,'{"billing_mode":"token","intervals":[
		  {"min_tokens":0,"max_tokens":100,"input_price":0.000001,"output_price":0.000002},
		  {"min_tokens":100,"max_tokens":null,"input_price":0.000002,"output_price":0.000004,
		   "cache_read_price":0.0000005,"cache_write_price":0.000003,"cache_write_1h_price":0.000005}],
		  "reasoning_effort_multipliers":{"medium":1,"high":2}}'::jsonb),sha256(c.credential_ciphertext)
	FROM multiplier m JOIN provider_credentials c ON c.id=m.credential_id`, credential, upstreamModel)

	service, ledger := beichen.New(db, nil), usage.NewStore(db)
	started := time.Now()
	before, err := service.Reconciliation(ctx, provider, "all", started)
	if err != nil {
		t.Fatal(err)
	}
	object := func(fields string) string { return fmt.Sprintf(`{"model":%q,%s}`, upstreamModel, fields) }
	event := func(payload string) string { return "data: " + strings.ReplaceAll(payload, "\n", "\ndata: ") + "\n\n" }
	fullResponses := object(`"id":"resp_audit","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,
		"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":10}}`)
	gemini := `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":15,
		"cachedContentTokenCount":30,"thoughtsTokenCount":5,"toolUsePromptTokenCount":2,"totalTokenCount":122}}`
	geminiPath := "/v1beta/models/" + publicModel
	cases := []struct {
		name, adapter, path, request, response string
		count                                  int
		stream, pricesLocally                  bool
		usageState, actualCost, base           string
		input, write, total                    int64
	}{
		{"chat-json", "openai_compatible", "/v1/chat/completions", `"messages":[],"reasoning_effort":"high"`,
			object(`"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,
				"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":10}}`),
			5, false, true, "reported", "0.000735", "0.0735", 60, 10, 120},
		{"chat-sse", "openai_compatible", "/v1/chat/completions", `"messages":[],"stream":true`,
			event(object(`"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":30,
				"prompt_cache_miss_tokens":70,"total_tokens":120}`)) + event("[DONE]"),
			5, true, true, "reported", "0.0003525", "0.03525", 70, 0, 120},
		{"responses-json", "openai_responses", "/v1/responses", `"input":"fixture"`,
			object(`"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,
				"input_tokens_details":{"cached_tokens":30}}`),
			5, false, false, "partial", "0.000421", "0.0421", 60, 10, 120},
		{"responses-sse", "openai_responses", "/v1/responses", `"input":"fixture","stream":true`,
			event(`{"type":"response.completed","response":` + fullResponses + `}`),
			5, true, true, "reported", "0.0003675", "0.03675", 60, 10, 120},
		{"anthropic-json", "anthropic", "/v1/messages", `"messages":[],"max_tokens":50,"cache_control":{"type":"ephemeral","ttl":"1h"}`,
			object(`"stop_reason":"end_turn","usage":{"input_tokens":60,"output_tokens":20,"cache_read_input_tokens":30,
				"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10}}`),
			5, false, true, "reported", "0.0003975", "0.03975", 60, 10, 120},
		{"anthropic-sse", "anthropic", "/v1/messages", `"messages":[],"max_tokens":50,"stream":true,"cache_control":{"type":"ephemeral"}`,
			event(`{"type":"message_start","message":`+object(`"usage":{"input_tokens":60,"output_tokens":1,
				"cache_read_input_tokens":30,"cache_creation_input_tokens":10}`)+`}`) +
				event(`{"type":"message_delta","usage":{"output_tokens":20}}`) + event(`{"type":"message_stop"}`),
			5, true, true, "reported", "0.0003675", "0.03675", 60, 10, 120},
		{"gemini-json", "google_gemini_v1beta", geminiPath + ":generateContent", `"contents":[]`, gemini,
			5, false, false, "partial", "0.000362", "0.0362", 72, 0, 122},
		{"gemini-sse", "google_gemini_v1beta", geminiPath + ":streamGenerateContent?alt=sse", `"contents":[]`, event(gemini),
			5, true, false, "partial", "0.000362", "0.0362", 72, 0, 122},
		{"missing", "openai_responses", "/v1/responses", `"input":"fixture"`, object(`"status":"completed","usage":null`),
			1, false, false, "missing", "", "", 0, 0, 0},
	}
	type wantAudit struct {
		state, base, cost, delta string
		total                    int64
		pricesLocally            bool
	}
	wants := make(map[string]wantAudit, 41)
	var rowID int64 = 1_000
	for _, tc := range cases {
		exec("UPDATE model_bindings SET adapter=$2 WHERE id=$1", binding, tc.adapter)
		for i := range tc.count {
			id, receiptID := fmt.Sprintf("audit41-%s-%d", tc.name, i), uuid.New()
			verifyCost, delta := tc.actualCost, "0"
			if tc.name == "chat-json" && i == 0 {
				verifyCost, delta = "0.0008", "-0.0065"
			}
			wants[id] = wantAudit{tc.usageState, tc.base, verifyCost, delta, tc.total, tc.pricesLocally}
			gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
				defer r.Body.Close()
				if tc.name == "chat-sse" {
					var body struct {
						StreamOptions struct {
							IncludeUsage bool `json:"include_usage"`
						} `json:"stream_options"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.StreamOptions.IncludeUsage {
						t.Fatalf("Chat SSE usage was not requested: %v", err)
					}
				}
				httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				header := http.Header{"Content-Type": {"application/json"}}
				if tc.stream {
					header.Set("Content-Type", "text/event-stream")
				}
				if verifyCost != "" {
					header.Set(beichenReceiptHeader, receiptID.String())
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(`{"model":%q,%s}`, publicModel, tc.request))).
				WithContext(usage.WithRequestID(ctx, id))
			req.Header.Set("Authorization", "Bearer "+studentKey)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != tc.response {
				t.Fatalf("%s: gateway status=%d or changed response", id, w.Code)
			}
			var sent time.Time
			var charged bool
			var state string
			err := db.QueryRowContext(ctx, `SELECT sent_at,actual_credits IS NOT NULL,usage_state FROM gateway_calls WHERE request_id=$1`, id).
				Scan(&sent, &charged, &state)
			if err != nil || charged != tc.pricesLocally || state != tc.usageState {
				t.Fatalf("%s: pre-receipt state=%s charged=%t err=%v", id, state, charged, err)
			}
			if verifyCost != "" {
				rowID++
				receipt := usage.BeichenReceipt{ResponseID: receiptID, RowID: rowID, APIKeyID: 51, Model: upstreamModel,
					OccurredAt: sent, ActualCost: verifyCost, InputTokens: tc.input, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: tc.write}
				applied, err := ledger.ApplyBeichenReceipt(ctx, credential, receipt)
				if err != nil || applied != !tc.pricesLocally {
					t.Fatalf("%s: receipt applied=%t err=%v", id, applied, err)
				}
				if applied, err := ledger.ApplyBeichenReceipt(ctx, credential, receipt); err != nil || applied {
					t.Fatalf("%s: receipt replay applied=%t err=%v", id, applied, err)
				}
			}
		}
	}

	after, err := service.Reconciliation(ctx, provider, "all", time.Now())
	if err != nil || after.Totals.Local.Requests-before.Totals.Local.Requests != 41 ||
		after.Totals.Local.Tokens-before.Totals.Local.Tokens != 4_820 ||
		after.Coverage.VerifiedMatchedCalls-before.Coverage.VerifiedMatchedCalls != 40 ||
		after.Coverage.VerifiedMatchedCompleteTokenCalls-before.Coverage.VerifiedMatchedCompleteTokenCalls != 25 ||
		after.Totals.Local.ChargedTokensMissingCalls-before.Totals.Local.ChargedTokensMissingCalls != 15 ||
		after.Totals.Local.PendingCalls-before.Totals.Local.PendingCalls != 1 || after.Totals.Money.MarginAvailable {
		t.Fatalf("unexpected reconciliation totals: before=%+v after=%+v err=%v", before.Totals, after.Totals, err)
	}

	studentTotal := new(big.Rat)
	day := started.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	for cursor := ""; ; {
		page, err := service.ReconciliationCalls(ctx, provider, day, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Items {
			want, ok := wants[row.RequestID]
			if !ok {
				continue
			}
			delete(wants, row.RequestID)
			if row.UsageState != want.state {
				t.Fatalf("%s: usage state=%s", row.RequestID, row.UsageState)
			}
			if want.cost == "" {
				if row.KnownTotalTokens != nil || row.BaseCredits != nil || row.ActualCredits != nil ||
					len(row.MissingTokenReasons) != 4 || row.BillingState == nil || *row.BillingState != "pending_review" {
					t.Fatalf("%s: missing usage produced a charge or known count: %+v", row.RequestID, row)
				}
				t.Logf("synthetic audit %s state=%s receipt=%s missing=%v", row.RequestID, row.UsageState, row.ReceiptStatus, row.MissingTokenReasons)
				continue
			}
			if !tokenIs(row.KnownTotalTokens, want.total) || !tokenIs(row.LocalTokens.Output, 20) || row.ReceiptTokens == nil ||
				row.ReceiptStatus != "matched" || row.BaseCredits == nil || *row.BaseCredits != want.base ||
				row.BaseDifferenceCredits == nil || *row.BaseDifferenceCredits != want.delta {
				t.Fatalf("%s: unexpected audit row %+v", row.RequestID, row)
			}
			if want.pricesLocally {
				if row.ExpectedCredits == nil || *row.ExpectedCredits != want.base || row.DifferenceCredits == nil ||
					*row.DifferenceCredits != want.delta || row.PriceTier == nil || *row.PriceTier != "[100,)" {
					t.Fatalf("%s: local tier, quote or receipt difference lost: %+v", row.RequestID, row)
				}
			} else if row.ExpectedCredits != nil || row.DifferenceCredits != nil || row.LocalTokens.CacheWrite != nil ||
				row.MissingTokenReasons["cache_write_tokens"] == "" {
				t.Fatalf("%s: receipt altered local observation or was presented as an estimate: %+v", row.RequestID, row)
			}
			if row.StudentCredits == nil {
				t.Fatalf("%s: student charge is missing", row.RequestID)
			}
			credits, ok := new(big.Rat).SetString(*row.StudentCredits)
			if !ok {
				t.Fatalf("%s: invalid student credits", row.RequestID)
			}
			studentTotal.Add(studentTotal, credits)
			t.Logf("synthetic audit %s state=%s tokens=%d base=%s receipt=%s difference=%s student=%s missing=%v",
				row.RequestID, row.UsageState, *row.KnownTotalTokens, *row.BaseCredits, *row.ActualCredits, *row.BaseDifferenceCredits,
				*row.StudentCredits, row.MissingTokenReasons)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(wants) != 0 || studentTotal.FloatString(5) != "1.85075" {
		t.Fatalf("missing %d audit rows or student credits %s", len(wants), studentTotal.FloatString(5))
	}
	t.Log("41 calls, 4,820 known tokens, 25 locally priced / 15 receipt-priced / 1 pending, 1.85075 student credits")
}
