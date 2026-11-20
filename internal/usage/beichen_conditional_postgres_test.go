package usage

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBeichenConditionalLedgerPostgres(t *testing.T) {
	ctx, db := newBeichenLedgerTestDB(t)
	exec := func(q string, args ...any) { t.Helper(); ledgerTestExec(t, ctx, db, q, args...) }
	org, student, model, provider := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO organizations(id,name,created_at,updated_at)
		VALUES($1,'Conditional Org',now(),now())`, org)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
		VALUES($1,'Student','conditional@example.test','fixture','student',now(),now())`, student)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'conditional-test','Conditional test','active',1250,now(),now())`, model)
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
		VALUES($1,'Conditional Beichen','https://api.beichenapi.com',now(),now())`, provider)
	credential, project, key := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,'Conditional test',decode('08','hex'),now()-interval '2 hours',now()-interval '2 hours')`, credential, provider)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
		VALUES($1,'G3',decode(repeat('ab',32),'hex'),false)`, credential)
	exec(`INSERT INTO beichen_panel_access(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
		SELECT id,1,93,decode('09','hex'),$2,updated_at FROM provider_credentials WHERE id=$1`, credential, student)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
		VALUES($1,$2,'Conditional test',10000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
		VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
		VALUES($1,$2,$3,'Conditional Key','active','conditional-test-hash','fixture',now(),now(),now())`, key, project, student)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, key, model)

	const conditional = `{"billing_mode":"token","intervals":[
		{"min_tokens":0,"max_tokens":100,"input_price":0.01,"output_price":0.01},
		{"min_tokens":100,"max_tokens":null,"input_price":0.000001000000000000000001,"output_price":0.000002,
		"cache_read_price":0.0000005,"cache_write_price":0.00000025,"cache_write_1h_price":0.00000075}
		],"reasoning_effort_multipliers":{"high":1.25,"medium":2}}`
	const flat = `{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,
		"cache_read_price":0.0000005,"cache_write_price":0.00000025}`
	exec(`WITH first_rate AS (
		INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier)
		VALUES($1,now()-interval '1 hour',2.5) RETURNING id,credential_id,effective_at
	), quote AS (
		INSERT INTO beichen_panel_snapshots
		  (credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
		SELECT c.id, first_rate.effective_at,c.updated_at,1,now()+interval '1 hour',first_rate.id,
		  jsonb_build_object('conditional-model',$2::jsonb,'conditional-with-flat',$2::jsonb,'fixed-model',$3::jsonb),
		  sha256(c.credential_ciphertext)
		FROM first_rate JOIN provider_credentials c ON c.id=first_rate.credential_id
		RETURNING effective_at,credential_id
	)
	INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
	SELECT credential_id,names.model,'upstream','base','panel',effective_at,1000000,2000000,500000,250000
	FROM quote CROSS JOIN (VALUES ('fixed-model'),('conditional-with-flat')) names(model)`, credential, conditional, flat)
	exec(`INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier)
		VALUES($1,now()-interval '30 min',9)`, credential)
	exec(`INSERT INTO beichen_panel_snapshots
		  (credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing,credential_ciphertext_sha256)
		SELECT c.id,now()+interval '30 min',c.updated_at,1,now()+interval '2 hours',m.id,
		  '{"conditional-model":{"billing_mode":"per_request","per_request_price":9}}',sha256(c.credential_ciphertext)
		FROM provider_credentials c JOIN beichen_multiplier_versions m ON m.credential_id=c.id
		WHERE c.id=$1 ORDER BY m.effective_at DESC LIMIT 1`, credential)

	store := NewStore(db)
	dispatch := func(name, protocol string, effort *string, ttl string, cap any) (Reservation, CallRoute) {
		t.Helper()
		route := CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: name,
			ReasoningEffort: effort, CacheTTL: ttl}
		path := "/v1/responses"
		if protocol == "anthropic" {
			path = "/v1/messages"
		}
		r, err := store.Reserve(ctx, key, model, uuid.NewString(), protocol, path, "", "", CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, r.CallID, route); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE gateway_calls SET student_cap_milli=$2 WHERE id=$1`, r.CallID, cap)
		route.SentAt = time.Now()
		return r, route
	}
	finalize := func(r Reservation, route CallRoute, metrics CallMetrics) {
		t.Helper()
		if err := store.Finalize(ctx, r, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
	}
	input, output, read, write := int64(100), int64(3), int64(2), int64(4)
	report := CallMetrics{State: UsageReported, PromptTokens: &input, CompletionTokens: &output, CacheHitTokens: &read, CacheWriteTokens: &write}
	anthropicReport := report
	uncached := input - read - write
	anthropicReport.PromptTokens = &uncached
	checkCharge := func(r Reservation, actual, debit, school, base, source string) {
		t.Helper()
		var ok bool
		err := db.QueryRowContext(ctx, `SELECT beichen_state='charged' AND billing_state='charged'
			AND beichen_review_reason IS NULL AND actual_credits=$2::numeric AND student_credits=$3::numeric
			AND beichen_credit_exact=student_credits AND credit_milli=trunc(student_credits*1000)::bigint
			AND school_absorbed_credits=$4::numeric
			FROM gateway_calls WHERE id=$1`, r.CallID, actual, debit, school).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("want actual=%s debit=%s school=%s: %v", actual, debit, school, err)
		}
		got, err := store.StudentCallReceipt(ctx, student, r.RequestID)
		if err != nil || got.ChargedCredits == nil || *got.ChargedCredits != debit ||
			got.BaseCredits == nil || *got.BaseCredits != base || got.BaseSource == nil || *got.BaseSource != source {
			t.Fatalf("student receipt did not show debit=%s base=%s source=%s: %+v, %v", debit, base, source, got, err)
		}
	}

	const actual, base = "0.0357500000000000000323125", "0.032500000000000000029375"
	high := "high"
	for _, tc := range []struct {
		name   string
		cap    any
		debit  string
		school string
	}{
		{"uncapped", nil, actual, "0"},
		{"over cap", int64(29), "0.029", "0.0067500000000000000323125"},
		{"below cap", int64(36), actual, "0"},
		{"zero cap", int64(0), "0", actual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, route := dispatch("conditional-model", "anthropic", &high, "1h", tc.cap)
			var response uuid.UUID
			if tc.name == "over cap" {
				response = uuid.New()
				if err := store.RecordBeichenReceiptID(ctx, r.CallID, credential, response); err != nil {
					t.Fatal(err)
				}
				finalize(r, route, CallMetrics{})
				replays := make(chan error, 4)
				for range cap(replays) {
					go func() { replays <- store.CompleteCallMetrics(ctx, r.CallID, anthropicReport) }()
				}
				for range cap(replays) {
					if err := <-replays; err != nil {
						t.Fatal(err)
					}
				}
			}
			finalize(r, route, anthropicReport)
			finalize(r, route, anthropicReport)
			checkCharge(r, actual, tc.debit, tc.school, base, "gateway_usage_price")
			var ok bool
			err := db.QueryRowContext(ctx, `SELECT price_tier='[100,)' AND price_snapshot=$2::jsonb
				AND pricing_detail @> '{"input_tokens":94,"total_input_tokens":100,"reasoning_effort":"high",
				  "reasoning_multiplier":"1.25","cache_ttl":"1h","upstream_multiplier":"2.5","school_multiplier":"1.1"}'
				FROM gateway_calls WHERE id=$1`, r.CallID, conditional).Scan(&ok)
			if err != nil || !ok {
				t.Fatalf("tier, dispatch quote or price evidence missing: %v", err)
			}
			if response != uuid.Nil {
				receipt := BeichenReceipt{ResponseID: response, RowID: 941, APIKeyID: 93,
					Model: route.UpstreamModelName, OccurredAt: route.SentAt, ActualCost: "0.002",
					InputTokens: 94, OutputTokens: 3, CacheReadTokens: 2, CacheWriteTokens: 4}
				applied, err := store.ApplyBeichenReceipt(ctx, credential, receipt)
				if err != nil || applied {
					t.Fatalf("late receipt applied=%t: %v", applied, err)
				}
				err = db.QueryRowContext(ctx, `SELECT NOT applied AND credits_booked=$2::numeric
					FROM beichen_call_receipts WHERE call_id=$1`, r.CallID, actual).Scan(&ok)
				if err != nil || !ok {
					t.Fatalf("late receipt did not retain the full priced charge for audit: %v", err)
				}
			}
		})
	}
	t.Run("fixed price uses the same cap", func(t *testing.T) {
		r, route := dispatch("fixed-model", "openai_responses", nil, "", int64(20))
		finalize(r, route, report)
		checkCharge(r, "0.02805", "0.02", "0.00805", "0.0255", "gateway_usage_price")
	})
	t.Run("missing condition never falls back to flat, receipt uses the same cap", func(t *testing.T) {
		r, route := dispatch("conditional-with-flat", "anthropic", &high, "", int64(29))
		response := uuid.New()
		if err := store.RecordBeichenReceiptID(ctx, r.CallID, credential, response); err != nil {
			t.Fatal(err)
		}
		finalize(r, route, anthropicReport)
		var pending bool
		err := db.QueryRowContext(ctx, `SELECT beichen_state='pending_review' AND beichen_review_reason='unknown_cache_ttl'
			AND actual_credits IS NULL AND student_credits IS NULL AND school_absorbed_credits=0
			FROM gateway_calls WHERE id=$1`, r.CallID).Scan(&pending)
		if err != nil || !pending {
			t.Fatalf("expected explicit review with no charge: %v", err)
		}
		receipt := BeichenReceipt{ResponseID: response, RowID: 940, APIKeyID: 93,
			Model: route.UpstreamModelName, OccurredAt: route.SentAt, ActualCost: "0.002",
			InputTokens: 94, OutputTokens: 3, CacheReadTokens: 2, CacheWriteTokens: 4}
		for _, expected := range []bool{true, false} {
			applied, err := store.ApplyBeichenReceipt(ctx, credential, receipt)
			if err != nil || applied != expected {
				t.Fatalf("receipt applied=%t, want %t: %v", applied, expected, err)
			}
		}
		finalize(r, route, anthropicReport)
		checkCharge(r, "0.22", "0.029", "0.191", "0.2", "receipt_actual_cost")
	})

	for _, table := range []struct{ name, match, released string }{
		{"project_month_credit_buckets", "project_id", "pending_milli=0"},
		{"project_member_month_credit_buckets", "project_id", "pending_milli=0"},
		{"api_key_month_credit_buckets", "api_key_id", "pending_milli=0"},
		{"monthly_usage_cube", "project_id", "charged_count=5 AND zero_cost_count=1"},
	} {
		id := project
		if table.match == "api_key_id" {
			id = key
		}
		var total string
		var ok bool
		err := db.QueryRowContext(ctx, `SELECT trim_scale((charged_milli+charged_milli_remainder)*0.001)::text,
			`+table.released+` FROM `+table.name+` WHERE `+table.match+`=$1`, id).Scan(&total, &ok)
		if err != nil || !ok || total != "0.149500000000000000064625" {
			t.Errorf("%s charge=%s state ok=%t: %v", table.name, total, ok, err)
		}
	}
}
