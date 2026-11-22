package beichen

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenDerivedPricesPostgres(t *testing.T) {
	ctx, db, provider, teacher, keys := newActionsPostgres(t)
	credential := keys[0]
	const opus, sonnet = "claude-opus-5-5", "claude-sonnet-5-5"
	const groupOfficial = `{"code":0,"data":[{"platforms":[{"platform":"anthropic","groups":[{"id":16,
	"platform":"anthropic","subscription_type":"standard","peak_rate_enabled":false,"rate_multiplier":0.6}],"supported_models":[
	{"name":"claude-opus-5.5","pricing":{"billing_mode":"token","input_price":0.000008,"output_price":0.000020,"cache_read_price":0,"cache_write_price":0}},
	{"name":"claude-sonnet-5-5","pricing":{"billing_mode":"token","intervals":[{"min_tokens":0,"input_price":0.000003,
	"output_price":0.000012,"cache_read_price":0,"cache_write_price":0}]}}]}]}]}`
	const catalogBody = `{"data":[{"id":"claude-opus-5-5","supported_protocols":["anthropic"]},
	{"id":"claude-sonnet-5-5","supported_protocols":["anthropic"]}]}`
	manager := switchTestManager(t)
	service := New(db, manager)
	service.ConfigurePanelFallback(uuid.NewString())
	secret := "sk-" + uuid.NewString()
	sealed, err := manager.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) { t.Helper(); execCatalogTestSQL(t, ctx, db, query, args...) }
	exec(`UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1`, credential, sealed)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'CC1',sha256('derived-price-fixture'::bytea),false)`, credential)
	sharedWebsite := string(readWebsitePricingFixture(t))
	website := sharedWebsite
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			return nil, errors.New("only metadata GET is accepted")
		}
		var body string
		switch {
		case r.URL.String() == panelURL:
			body = website
		case r.URL.Host == publicKeyHost && r.URL.Path == "/api/v1/keys":
			body = strings.Replace(websiteKeyListFixture(secret, "CC1", 41, 16), `"status":"active"`,
				`"status":"active","group":{"id":16,"name":"Claude 分组","platform":"anthropic","rate_multiplier":0.60}`, 1)
		case r.URL.Host == publicKeyHost && r.URL.Path == "/api/v1/groups/rates":
			body = `{"code":0,"data":{"2":0.15,"16":0.60}}`
		case r.URL.Host == "api.beichenapi.com" && r.URL.Path == billingPath:
			body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
"effective_rate_multiplier":0.6,"group_rate_multiplier":0.6,"peak_rate_enabled":false,"group_id":16,"api_key_id":41,"observed_at":%q}`,
				time.Now().UTC().Add(30*time.Millisecond).Format(time.RFC3339Nano))
		case r.URL.Host == "api.beichenapi.com" && r.URL.Path == modelsPath:
			body = catalogBody
		default:
			return nil, errors.New("unexpected metadata endpoint")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	refresh := func() error {
		t.Helper()
		_, err := service.runJob(ctx, syncJob{provider: provider, credential: credential, kind: "billing"}, true)
		return err
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	view, err := service.Overview(ctx, provider, "")
	if err != nil || len(view.Models) != 2 {
		t.Fatalf("overview should have both Key models: %v", err)
	}
	group := datasetForCredential(t, view, credential, "billing")
	if group.GroupID == nil || *group.GroupID != 16 || group.GroupName == nil || *group.GroupName != "Claude 分组" ||
		group.GroupRate == nil || *group.GroupRate != "0.60" || group.BillingState != "current" ||
		group.GroupStatus != "assigned" || group.LastError != nil {
		t.Fatalf("missing verified CC group and rate: %+v", group)
	}
	for _, row := range view.Models {
		if row.PriceStatus != "conditional" || !row.PriceCovered || row.PriceConditionsExact["cache_read_price"] == nil ||
			row.PriceConditionsExact["cache_write_1h_price"] == nil {
			t.Fatalf("expected the shared website quote with cache prices: %+v", row)
		}
	}
	capabilities, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
	if err != nil || len(capabilities.Capabilities) != 2 || capabilities.Capabilities[0].PriceState != "conditional" ||
		capabilities.Capabilities[1].PriceState != "conditional" {
		t.Fatalf("capability report did not select the website price: %+v, %v", capabilities.Capabilities, err)
	}
	org, student, project, studentKey := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'Fixture','derived-prices@example.test','fixture','student',now(),now())`, student)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
VALUES($1,$2,'Fixture',1000000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
VALUES(gen_random_uuid(),$1,$2,1000000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
VALUES($1,$2,$3,'Fixture','active','derived-prices-fixture','fixture',now(),now(),now())`, studentKey, project, student)
	modelIDs := map[string]uuid.UUID{}
	for _, name := range []string{opus, sonnet} {
		modelID, err := service.EnableModel(ctx, provider, teacher, name, "")
		if err != nil {
			t.Fatal(err)
		}
		modelIDs[name] = modelID
		exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, studentKey, modelID)
	}
	store := usage.NewStore(db)
	dispatch := func(model string) (usage.Reservation, usage.CallRoute) {
		t.Helper()
		route := usage.CallRoute{Beichen: true, ProviderID: provider, CredentialID: credential, UpstreamModelName: model}
		call, err := store.Reserve(ctx, studentKey, modelIDs[model], uuid.NewString(), "anthropic", "/v1/messages", "", "", usage.CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, call.CallID, route); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSent(ctx, call.CallID); err != nil {
			t.Fatal(err)
		}
		route.SentAt = time.Now().UTC()
		return call, route
	}
	settle := func(call usage.Reservation, route usage.CallRoute, wantSource, wantVersion, wantBase, wantDebit, wantReason string) {
		t.Helper()
		input, output, zero := int64(1000), int64(100), int64(0)
		metrics := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &input, CompletionTokens: &output,
			CacheHitTokens: &zero, CacheWriteTokens: &zero}
		if err := store.Finalize(ctx, call, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
		var base, debit, source, version, why string
		if err := db.QueryRowContext(ctx, `
SELECT COALESCE(c.beichen_base_source,''),COALESCE(p.source,''),COALESCE(trim_scale(c.beichen_base_credits)::text,''),
  COALESCE(trim_scale(c.student_credits)::text,''),COALESCE(c.beichen_review_reason,'')
FROM gateway_calls c LEFT JOIN beichen_price_versions p ON p.id=c.beichen_upstream_price_version
WHERE c.id=$1`, call.CallID).Scan(&source, &version, &base, &debit, &why); err != nil {
			t.Fatal(err)
		}
		if source != wantSource || version != wantVersion || base != wantBase || debit != wantDebit || why != wantReason {
			t.Fatalf("%s: source %q, version %q, base %q, debit %q, review %q", route.UpstreamModelName, source, version, base, debit, why)
		}
	}
	call, route := dispatch(opus)
	settle(call, route, "gateway_usage_price", "", "0.36", "0.396", "")
	call, route = dispatch(sonnet)
	settle(call, route, "gateway_usage_price", "", "0.18", "0.198", "")
	old, oldRoute := dispatch(sonnet)

	website = strings.Replace(sharedWebsite, "claude-sonnet-5.5", "unrelated-sonnet", 1)
	// Leave one usable, exact-name quote under OpenAI and none under Anthropic.
	website = strings.Replace(website, "deepseek-v4-pro", sonnet, 1)
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	call, route = dispatch(sonnet)
	settle(call, route, "", "", "", "", "missing_upstream_price")
	call, route = dispatch(opus)
	settle(call, route, "gateway_usage_price", "", "0.36", "0.396", "")

	website = `{"code":0,"data":[]}`
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	view, err = service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	if prices := datasetForCredential(t, view, credential, "prices"); !prices.OfficialPriceMissing {
		t.Fatalf("expected an explicit missing price state: %+v", prices)
	}
	call, route = dispatch(opus)
	settle(call, route, "", "", "", "", "missing_upstream_price")
	settle(old, oldRoute, "gateway_usage_price", "", "0.18", "0.198", "")

	website = groupOfficial
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	call, route = dispatch(opus)
	settle(call, route, "gateway_usage_price", "panel", "0.6", "0.66", "")
	call, route = dispatch(sonnet)
	settle(call, route, "gateway_usage_price", "", "0.252", "0.2772", "")

	input, output, zero := int64(500_000_000), int64(2_000_000_000), int64(0)
	if err := service.SetPrice(ctx, provider, credential, teacher, opus, PriceInput{Mode: "manual", Rates: &Rates{
		Input: &input, Output: &output, CacheRead: &zero, CacheWrite: &zero,
	}}); err != nil {
		t.Fatal(err)
	}
	call, route = dispatch(opus)
	settle(call, route, "teacher_manual_price", "panel", "0.7", "0.77", "")

	var observations, published int
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM beichen_group_price_observations WHERE provider_id=$1 AND group_id=16 AND source=$3),
  (SELECT count(*) FROM beichen_price_versions WHERE credential_id=$2 AND source=$3)`,
		provider, credential, derivedPriceSource).Scan(&observations, &published); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || published != 0 {
		t.Fatalf("derived prices must stay as audit evidence: observed=%d, published=%d", observations, published)
	}
}
