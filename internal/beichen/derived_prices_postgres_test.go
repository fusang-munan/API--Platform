package beichen

import (
	"database/sql"
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
	const noOfficial = `{"code":0,"data":[{"platforms":[{"platform":"anthropic","groups":[{"id":2}],"supported_models":[
	{"name":"claude-opus-5-5","pricing":{"billing_mode":"token","input_price":0.9,"output_price":0.9}}]}]}]}`
	const official = `{"code":0,"data":[{"platforms":[{"platform":"anthropic","groups":[{"id":16,
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
	website, keyGroup := noOfficial, 16
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			return nil, errors.New("only metadata GET is accepted")
		}
		var body string
		switch {
		case r.URL.String() == panelURL:
			body = website
		case r.URL.Host == publicKeyHost && r.URL.Path == "/api/v1/keys":
			body = websiteKeyListFixture(secret, "CC1", 41, keyGroup)
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
	observed := time.Date(2026, 11, 21, 12, 14, 0, 0, time.UTC)
	view, err := service.Overview(ctx, provider, "")
	if err != nil || len(view.Models) != 2 {
		t.Fatalf("overview should have both Key models: %v", err)
	}
	for _, row := range view.Models {
		if row.PriceStatus != "derived" || row.PriceSource != derivedPriceSource || row.PriceObservedAt == nil ||
			!row.PriceObservedAt.Equal(observed) || !row.PriceCovered || row.UpstreamRates == nil ||
			row.UpstreamRates.CacheRead != nil || row.UpstreamRates.CacheWrite != nil {
			t.Fatalf("unlabelled or incomplete derived quote: %+v", row)
		}
	}
	capabilities, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
	if err != nil || len(capabilities.Capabilities) != 2 || capabilities.Capabilities[0].PriceState != "derived" ||
		capabilities.Capabilities[1].PriceState != "derived" {
		t.Fatalf("capability report lost derived provenance: %+v, %v", capabilities.Capabilities, err)
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
	settle := func(call usage.Reservation, route usage.CallRoute, cacheRead int64, wantSource, wantVersion, wantBase, wantDebit string) {
		t.Helper()
		input, output, zero := int64(1000), int64(100), int64(0)
		metrics := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &input, CompletionTokens: &output,
			CacheHitTokens: &cacheRead, CacheWriteTokens: &zero}
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
		if source != wantSource || version != wantVersion || base != wantBase || debit != wantDebit ||
			(cacheRead > 0 && why != "missing_cache_read_price") {
			t.Fatalf("%s: source %q, version %q, base %q, debit %q, review %q", route.UpstreamModelName, source, version, base, debit, why)
		}
	}
	call, route := dispatch(opus)
	settle(call, route, 0, derivedPriceSource, derivedPriceSource, "0.36", "0.396")
	call, route = dispatch(sonnet)
	settle(call, route, 0, derivedPriceSource, derivedPriceSource, "0.18", "0.198")
	call, route = dispatch(opus)
	settle(call, route, 10, "", derivedPriceSource, "", "")
	old, oldRoute := dispatch(opus)

	website = official
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	settle(old, oldRoute, 0, derivedPriceSource, derivedPriceSource, "0.36", "0.396")
	call, route = dispatch(opus)
	settle(call, route, 0, "gateway_usage_price", "panel", "0.6", "0.66")
	call, route = dispatch(sonnet)
	settle(call, route, 0, "gateway_usage_price", "", "0.252", "0.2772")

	input, output, zero := int64(500_000_000), int64(2_000_000_000), int64(0)
	if err := service.SetPrice(ctx, provider, credential, teacher, opus, PriceInput{Mode: "manual", Rates: &Rates{
		Input: &input, Output: &output, CacheRead: &zero, CacheWrite: &zero,
	}}); err != nil {
		t.Fatal(err)
	}
	call, route = dispatch(opus)
	settle(call, route, 0, "teacher_manual_price", "panel", "0.7", "0.77")

	today := time.Now().In(beijing).Format(dayLayout)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	report, err := readDashboardLocalRows(ctx, tx, provider, dashboardWindow{StartDate: today, EndDate: today})
	if err != nil {
		t.Fatal(err)
	}
	for key, row := range report {
		if key.Model == opus && (row.StandardSource != "mixed" || row.StandardPriceLabel != "含反推价，待核对") {
			t.Fatalf("report lost mixed source label: %+v", row)
		}
		if key.Model == sonnet && (row.StandardSource != derivedPriceSource || row.StandardPriceLabel != derivedPriceLabel) {
			t.Fatalf("report lost derived source label: %+v", row)
		}
	}
	if len(report) != 2 {
		t.Fatalf("expected both models in report, got %d rows", len(report))
	}
}
