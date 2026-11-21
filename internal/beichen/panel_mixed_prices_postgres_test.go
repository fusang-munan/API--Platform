package beichen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestPanelMixedPricesActivationAndSettlementPostgres(t *testing.T) {
	ctx, db, provider, teacher, keys := newActionsPostgres(t)
	const flat, tiered, incomplete = "gpt-6.1-sol", "gpt-6.2-sol", "incomplete-tier"
	quote, info := mixedPanelFixture(t, flat, tiered, incomplete)
	credential := keys[0]
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []string{flat, tiered, incomplete} {
		exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,supported_protocols,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,ARRAY['openai_responses'],now(),now(),now(),now())`, provider, credential, model)
		exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at)
VALUES($1,$2,$3,'openai_responses',200,1,now())`, provider, credential, model)
	}
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'G3',decode(repeat('ab',32),'hex'),false)`, credential)
	var revision time.Time
	if err := db.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", credential).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := saveBillingObservation(ctx, tx, credential, revision, info, nil, 1, quote); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	service := New(db, nil)
	view, err := service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range view.ModelCatalog {
		if model.Model != flat && model.Model != tiered && model.Model != incomplete {
			continue
		}
		want := model.Model != incomplete
		if model.ProbeState != "passed" || (model.Activation == "ready") != want || model.PriceCovered != want {
			t.Fatalf("price or probe preview is inconsistent: %+v", model)
		}
	}
	if len(view.ModelCatalog) != 3 {
		t.Fatalf("catalog must show all three fixture models: %+v", view.ModelCatalog)
	}
	for _, model := range view.Models {
		if model.Model == flat && (model.PriceConditionsExact["input_price"] != "0.000002" ||
			model.PriceConditionsExact["output_price"] != "0.000012" ||
			model.PriceConditionsExact["per_request_price"] != nil ||
			!strings.Contains(string(model.PriceConditions), "per_request_price")) {
			t.Fatalf("expected exact text prices and preserved raw quote: %+v", model)
		}
	}
	flatID, err := service.EnableModel(ctx, provider, teacher, flat, "")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.EnableModelsBatch(ctx, provider, teacher, []string{tiered, incomplete})
	if err != nil || len(batch.Enabled) != 1 || batch.Enabled[0].Model != tiered || len(batch.Skipped) != 1 ||
		batch.Skipped[0].Model != incomplete || !strings.Contains(batch.Skipped[0].Message, "第 2 档") ||
		!strings.Contains(batch.Skipped[0].Message, "output_price") {
		t.Fatalf("unexpected batch price decision: %+v, %v", batch, err)
	}

	org, student, project, key := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())", org)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'Fixture','mixed-prices@example.test','fixture','student',now(),now())`, student)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
VALUES($1,$2,'Fixture',1000000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
VALUES(gen_random_uuid(),$1,$2,1000000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
VALUES($1,$2,$3,'Fixture','active','mixed-prices-fixture','fixture',now(),now(),now())`, key, project, student)
	batchID := batch.Enabled[0].ModelID
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
VALUES(gen_random_uuid(),$1,$2,now()),(gen_random_uuid(),$1,$3,now())`, key, flatID, batchID)

	store := usage.NewStore(db)
	for _, tc := range []struct {
		model, subtest, credits, tier, ignored, reason string
		modelID                                        uuid.UUID
		input                                          int64
	}{
		{model: flat, subtest: "flat", modelID: flatID, input: 10, credits: "0.00352", ignored: `["per_request_price","image_output_price","video_output_price"]`},
		{model: tiered, subtest: "first-tier", modelID: batchID, input: 10, credits: "0.00704", tier: "[0,128000)",
			ignored: `["per_request_price","image_output_price","video_output_price","intervals[0].image_output_price"]`},
		{model: tiered, subtest: "second-tier", modelID: batchID, input: 128000, credits: "112.64528", tier: "[128000,)",
			ignored: `["intervals[1].video_output_price"]`},
		// An already dispatched call can still settle on the priced tier.
		{model: incomplete, subtest: "priced-tier-of-incomplete-quote", modelID: batchID, input: 10, credits: "0.00132", tier: "[0,100)", ignored: "[]"},
		{model: incomplete, subtest: "unpriced-tier", modelID: batchID, input: 100, ignored: "[]", reason: "missing_output_price"},
	} {
		t.Run(tc.subtest, func(t *testing.T) {
			const protocol = "openai_responses"
			route := usage.CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: tc.model}
			reservation, err := store.Reserve(ctx, key, tc.modelID, uuid.NewString(), protocol, "/v1/responses", "", "", usage.CallCorrelation{}, route)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PrepareDispatch(ctx, reservation.CallID, route); err != nil {
				t.Fatal(err)
			}
			route.SentAt = time.Now()
			if err := store.MarkSent(ctx, reservation.CallID); err != nil {
				t.Fatal(err)
			}
			output, zero := int64(1), int64(0)
			metrics := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &tc.input, CompletionTokens: &output,
				CacheHitTokens: &zero, CacheWriteTokens: &zero}
			if err := store.Finalize(ctx, reservation, true, "succeeded", "", "", route, metrics); err != nil {
				t.Fatal(err)
			}
			var state, reason, credits, tier string
			var detailMatches, snapshotMatches bool
			if err := db.QueryRowContext(ctx, `SELECT beichen_state,COALESCE(beichen_review_reason,''),
    COALESCE(trim_scale(beichen_credit_exact)::text,''),COALESCE(price_tier,''),
    COALESCE(pricing_detail->'ignored_non_token_fields','[]'::jsonb) @> $2::jsonb,
    COALESCE(price_snapshot=$3::jsonb,false)
FROM gateway_calls WHERE id=$1`, reservation.CallID, json.RawMessage(tc.ignored), quote.terms[tc.model]).
				Scan(&state, &reason, &credits, &tier, &detailMatches, &snapshotMatches); err != nil {
				t.Fatal(err)
			}
			if tc.reason != "" {
				if state != "pending_review" || reason != tc.reason || credits != "" {
					t.Fatalf("expected pending %s, got state=%s reason=%s credits=%s", tc.reason, state, reason, credits)
				}
				return
			}
			if state != "charged" || reason != "" || credits != tc.credits || tier != tc.tier || !detailMatches || !snapshotMatches {
				t.Fatalf("state=%s reason=%s credits=%s tier=%s detail=%v snapshot=%v", state, reason, credits, tier, detailMatches, snapshotMatches)
			}
		})
	}
}
