package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/testutil"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestOverviewStudentPriceDiagnosticsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	primary, backup, pending := keys[1], keys[2], keys[3]
	const fixed, higher, manual, unverified, noRoute = "fixture-fixed", "fixture-higher", "fixture-manual", "fixture-unverified", "fixture-no-route"
	const missing, candidateMissing = "gemini-3.5-flash-lite", "fixture-candidate-missing"
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	for _, item := range []struct {
		key   uuid.UUID
		names []string
	}{
		{primary, []string{fixed, manual, noRoute, missing, candidateMissing}}, {backup, []string{fixed, higher, missing}}, {pending, []string{unverified}},
	} {
		entries := make([]catalog.ProviderEntry, 0, len(item.names))
		for _, name := range item.names {
			entries = append(entries, catalog.ProviderEntry{ID: name, SupportedProtocols: []string{"openai_compatible"}})
		}
		saveCatalogFixture(t, ctx, db, provider, item.key, entries, nil)
	}
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
SELECT gen_random_uuid(),n,n,'active',1000,now(),now() FROM unnest($1::text[]) n`, []string{fixed, higher, manual, unverified, missing})
	for _, route := range []struct {
		key      uuid.UUID
		name     string
		priority int
		adapter  string
	}{{primary, fixed, 1, "openai_compatible"}, {backup, fixed, 2, "openai_responses"}, {backup, higher, 1, "openai_compatible"},
		{primary, manual, 1, "openai_compatible"}, {pending, unverified, 1, "openai_compatible"},
		{primary, missing, 1, "openai_compatible"}, {backup, missing, 2, "openai_responses"},
		{primary, candidateMissing, 1, "openai_compatible"}} {
		exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,model_id,$5,'active',$4,now(),now() FROM models WHERE model_id=$3`,
			provider, route.key, route.name, route.priority, route.adapter)
	}
	exec(`UPDATE models SET status='pending_configuration' WHERE model_id=$1`, candidateMissing)
	exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at)
SELECT $1,k,n,p,200,1,clock_timestamp()
FROM (VALUES ($2::uuid,$4::text,'openai_compatible'),($3::uuid,$4::text,'openai_responses'),
 ($2::uuid,$5::text,'openai_compatible')) fixture(k,n,p)`, provider, primary, backup, missing, manual)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
SELECT id,n,sha256(credential_ciphertext || convert_to(n,'UTF8')),false
FROM (VALUES ($1::uuid,'GM1'),($2::uuid,'G3')) k(id,n)
JOIN provider_credentials c USING (id)`, primary, backup)
	exec(`INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at)
VALUES($1,0.5,10,clock_timestamp()),($2,0.5,10,clock_timestamp())`, primary, backup)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success)
VALUES($1,'billing',now()),($2,'billing',now())
ON CONFLICT(credential_id,kind) DO UPDATE SET last_success=EXCLUDED.last_success`, primary, backup)
	exec(`INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing)
SELECT c.id,clock_timestamp(),c.updated_at,10,now()+interval '1 hour',mv.id,$2::jsonb
FROM provider_credentials c JOIN beichen_multiplier_versions mv ON mv.credential_id=c.id WHERE c.id=$1`, backup,
		`{"fixture-fixed":{"billing_mode":"token","input_price":0.000001,"output_price":0.000001,"cache_read_price":0,"cache_write_price":0},
 "fixture-higher":{"billing_mode":"token","intervals":[{"min_tokens":500000,"input_price":0.000001,"output_price":0.000001}]}}`)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,effective_at)
SELECT credential_id,$2,'upstream','base','panel',1000000,1000000,0,0,effective_at FROM beichen_panel_snapshots WHERE credential_id=$1`,
		backup, fixed)
	service := New(db, nil)
	base := int64(50_000_000)
	for _, name := range []string{manual, noRoute} {
		if err := service.SetPrice(ctx, provider, primary, actor, name, PriceInput{Mode: "manual", Rates: &Rates{Input: &base, Output: &base}}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{fixed: studentPriceNormal, higher: studentPriceHigher, manual: studentPriceManual,
		unverified: studentPriceRatePending, noRoute: studentPriceNoRoute, missing: studentPriceNoBase,
		candidateMissing: studentPriceNoRoute}
	for _, row := range view.ModelCatalog {
		if want, ok := expected[row.Model]; ok {
			if row.StudentPrice.Status != want || row.StudentPrice.Reason == "" {
				t.Errorf("%s student price = %+v, want %s", row.Model, row.StudentPrice, want)
			}
			delete(expected, row.Model)
		}
		if row.Model == fixed {
			statuses := make(map[uuid.UUID]string)
			for _, route := range row.BoundRoutes {
				statuses[*route.CredentialID] = route.StudentPrice.Status
			}
			if statuses[primary] != studentPriceNoBase || statuses[backup] != studentPriceNormal {
				t.Errorf("expected primary missing price and priced backup, got %+v", row.BoundRoutes)
			}
			if !row.PriceCovered || row.ModelAvailability.Availability == catalog.AvailabilityUnavailable {
				t.Errorf("the priced backup must cover the model: %+v", row)
			}
		}
		switch row.Model {
		case missing:
			if !row.Published || !row.Active || row.ReadyCount != 2 || row.PriceCovered || row.PriceState != "missing" ||
				row.ModelAvailability.Availability != catalog.AvailabilityUnavailable || row.ModelAvailability.ProbeState != "passed" ||
				row.PriceBlocker == nil || row.PriceBlocker.Code != "missing_price" {
				t.Errorf("unpriced model must stay published and show unavailable: %+v", row)
			}
		case manual:
			if !row.Published || !row.Active || !row.PriceCovered || row.PriceState != "manual" ||
				row.ModelAvailability.Availability != catalog.AvailabilityAvailable {
				t.Errorf("manual price must cover the ready route: %+v", row)
			}
		case noRoute:
			if row.Published || row.Active || !row.PriceCovered || !strings.Contains(row.StudentPrice.Reason, "尚未发布") {
				t.Errorf("priced candidate must explain why it has no student route: %+v", row)
			}
		case candidateMissing:
			if row.Published || row.Active || row.PriceCovered || row.PriceState != "missing" ||
				row.PriceBlocker == nil || row.ModelAvailability.Availability != "not_published" ||
				row.StudentPrice.Status != studentPriceNoRoute {
				t.Errorf("an unpublished model without a price must stay not_published: %+v", row)
			}
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing overview rows: %+v", expected)
	}
	for _, row := range view.Models {
		if row.CredentialID == backup && row.Model == fixed &&
			(!row.SchoolRatesIncludeEffectiveMultiplier || row.SchoolRates == nil || row.SchoolRates.Input == nil ||
				*row.SchoolRates.Input != "50000000" || row.SchoolRates.CacheRead == nil || *row.SchoolRates.CacheRead != "0") {
			t.Errorf("expected 50 base credits and exact zero cache price: %+v", row)
		}
		if row.Model == higher && (!row.QuoteUsable || row.QuoteEffectiveMultiplier == nil || *row.QuoteEffectiveMultiplier != "0.5") {
			t.Errorf("conditional quote has no effective rate: %+v", row)
		}
	}
	if view.SchoolMultipliers[primary] != "1.10" || view.SchoolMultipliers[backup] != "1.10" || view.SchoolMultipliers[pending] != "1" {
		t.Fatalf("unexpected school coefficients: %+v", view.SchoolMultipliers)
	}
	for name, key := range map[string]uuid.UUID{fixed: backup, manual: primary} {
		got, err := usage.NewStore(db).BeichenOutputPrice(ctx, usage.CallRoute{CredentialID: key, ProviderID: provider, UpstreamModelName: name})
		if err != nil || got != "0.55" {
			t.Errorf("%s should cost 0.55 yuan (55 credits) per 1M output, got %q: %v", name, got, err)
		}
	}
	capabilities, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range capabilities.Capabilities {
		if route.Model == missing && (!route.Published || route.Availability != catalog.AvailabilityUnavailable ||
			route.PriceCovered || route.BindingState != "ready" || route.RecommendedProtocol != nil) {
			t.Errorf("capability must preserve the unpriced publication and ready binding: %+v", route)
		}
		if route.Model == candidateMissing && (route.Published || route.PriceCovered ||
			route.Availability != "not_published") {
			t.Errorf("unpublished missing-price capability must stay not_published: %+v", route)
		}
	}
}

func TestOverviewClaudeQuoteRenewalPostgres(t *testing.T) {
	f := newAccountRateJobFixture(t)
	key, provider, model := f.job.credential, f.job.provider, accountRateFixtureModel
	f.rates = `{"16":0.5}`
	f.quote = websiteGroupFixture(16, "0.6", `{"name":"claude-sonnet-5-5","pricing":{
 "billing_mode":"token","input_price":0.000001,"output_price":0.000002,"cache_read_price":0,
 "cache_write_price":0.000003,"cache_write_1h_price":0.000004}}`)
	exec := func(query string, args ...any) {
		t.Helper()
		execCatalogTestSQL(t, f.ctx, f.db, query, args...)
	}
	refresh := func() {
		t.Helper()
		f.at = time.Now().UTC().Truncate(time.Microsecond)
		f.syncBilling()
	}
	refresh()
	exec(`WITH school AS (
 INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
 VALUES(gen_random_uuid(),$3::text,$3::text,'active',1000,now(),now()) RETURNING id
)
INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,$3,'anthropic','active',1,now(),now() FROM school`, provider, key, model)
	exec(`UPDATE beichen_multiplier_versions SET effective_at=effective_at-interval '22 minutes' WHERE credential_id=$1`, key)
	ageQuote := func(minutes int) {
		t.Helper()
		exec(`UPDATE beichen_panel_snapshots s
SET effective_at=effective_at-$2::int*interval '1 minute', expires_at=expires_at-$2::int*interval '1 minute'
WHERE credential_id=$1 AND effective_at=(SELECT max(effective_at) FROM beichen_panel_snapshots WHERE credential_id=s.credential_id)`, key, minutes)
		exec(`UPDATE beichen_datasets SET last_success=last_success-$2::int*interval '1 minute'
WHERE credential_id=$1 AND kind IN ('prices','billing')`, key, minutes)
	}
	read := func(wantBlocker string) Dataset {
		t.Helper()
		view, err := f.service.Overview(f.ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(view.ModelCatalog, func(row CatalogModel) bool { return row.Model == model })
		j := slices.IndexFunc(view.Models, func(row Model) bool { return row.Model == model && row.CredentialID == key })
		if i < 0 || j < 0 {
			t.Fatal("missing Claude catalog or quote for the bound Key")
		}
		row, quote := view.ModelCatalog[i], view.Models[j]
		if !row.Published || !row.Active || len(row.BoundRoutes) != 1 || !row.BoundRoutes[0].BindingReady || !quote.RouteReady {
			t.Fatalf("quote expiry changed the published binding: %+v", row)
		}
		if wantBlocker == "" {
			if row.PriceState != "conditional" || !row.PriceCovered || row.StudentPrice.Status != studentPriceNormal ||
				!quote.QuoteUsable || quote.StudentPrice.Status != studentPriceNormal || quote.SchoolRates != nil ||
				quote.QuoteEffectiveMultiplier == nil || *quote.QuoteEffectiveMultiplier != "0.5" ||
				quote.PriceConditionsExact["input_price"] != "0.000001" || quote.PriceConditionsExact["cache_write_1h_price"] != "0.000004" {
				t.Fatalf("Claude must have the same Key's calculable conditional quote: %+v, %+v", row, quote)
			}
		} else if row.PriceBlocker == nil || row.PriceBlocker.Code != wantBlocker ||
			row.ModelAvailability.Availability != catalog.AvailabilityUnavailable || row.PriceCovered || quote.QuoteUsable ||
			row.StudentPrice.Status != studentPriceNoBase || quote.QuoteEffectiveMultiplier != nil || quote.SchoolRates != nil {
			t.Fatalf("Claude should be unavailable with %s: %+v, %+v", wantBlocker, row, quote)
		}
		return datasetForCredential(t, view, key, "prices")
	}
	ageQuote(22)
	read("price_snapshot_expired")

	refresh()
	var scheduledBeforeExpiry, bounded bool
	err := f.db.QueryRowContext(f.ctx, `
SELECT d.next_attempt < s.expires_at, s.expires_at=m.observed_at+interval '20 minutes'
FROM beichen_panel_snapshots s JOIN beichen_multiplier_versions m ON m.id=s.multiplier_version
JOIN beichen_datasets d ON d.credential_id=s.credential_id AND d.kind='billing'
WHERE s.credential_id=$1 ORDER BY s.effective_at DESC LIMIT 1`, key).Scan(&scheduledBeforeExpiry, &bounded)
	if err != nil || !scheduledBeforeExpiry || !bounded {
		t.Fatalf("successful renewal must be bounded and schedule a refresh before expiry: %v", err)
	}
	prices := read("")
	if prices.LastSuccess == nil || prices.LastError != nil || prices.RefreshError != nil {
		t.Fatalf("the successful renewal must clear errors: %+v", prices)
	}
	ageQuote(17)
	const refreshError = "fixture price refresh failed"
	exec(`UPDATE beichen_datasets SET last_error=$2 WHERE credential_id=$1 AND kind='prices'`, key, refreshError)
	prices = read("")
	if prices.LastSuccess == nil || prices.LastError != nil || prices.RefreshError == nil || *prices.RefreshError != refreshError {
		t.Fatalf("a failed attempt must remain visible without blocking the usable quote: %+v", prices)
	}
	ageQuote(4)
	prices = read("price_snapshot_expired")
	if prices.LastError == nil || *prices.LastError != refreshError || prices.RefreshError != nil {
		t.Fatalf("an expired quote must no longer suppress its sync error: %+v", prices)
	}

	refresh()
	f.quote = websiteGroupFixture(16, "0.6", `{"name":"claude-sonnet-5-5","pricing":null}`)
	if _, err := f.service.runJob(f.ctx, f.job, true); !errors.Is(err, errPanelEmpty) {
		t.Fatalf("a verified missing price must stop the prior quote: %v", err)
	}
	if prices = read("missing_price"); !prices.OfficialPriceMissing || prices.LastError == nil {
		t.Fatalf("missing official quote must keep its diagnostic: %+v", prices)
	}
}

func TestBeichenOverviewRetiredCatalogPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	retired, current := keys[0], keys[1]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", retired)
	exec(`
INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
SELECT gen_random_uuid(), name, name, status, 1000, now(), now()
FROM (VALUES ('retired-binding','active'), ('retired-website-record','pending_configuration'),
  ('stale-active-key','active'), ('active-binding','active'), ('inactive-binding-active-key','active'),
  ('active-binding-retired-key','active')) m(name,status)`)
	exec(`
INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,catalog_source,status,last_seen_at,synced_at,created_at,updated_at)
VALUES (gen_random_uuid(),$1,$2,'retired-website-record','website','inactive',now(),now(),now(),now()),
       (gen_random_uuid(),$1,$2,'stale-active-key','key','inactive',now(),now(),now(),now()),
       (gen_random_uuid(),$1,$3,'stale-active-key','key','active',now()-interval '1 hour',now()-interval '1 hour',now(),now()),
       (gen_random_uuid(),$1,$2,'active-binding','key','inactive',now(),now(),now(),now())`, provider, retired, current)
	exec(`
INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(), m.id, $1, b.credential_id, b.upstream, b.adapter, b.status, 10, now(), now()
FROM (VALUES ('retired-binding',$2::uuid,' retired-binding ','openai_compatible','inactive'),
             ('active-binding',$3::uuid,'alias-with-missing-directory','openai_compatible','active'),
             ('inactive-binding-active-key',$3::uuid,'inactive-binding-active-key','openai_compatible','inactive'),
             ('active-binding-retired-key',$2::uuid,'active-binding-retired-key','openai_compatible','active'),
             ('active-binding-retired-key',$2::uuid,'active-binding-retired-key','openai_responses','inactive'))
  b(school,credential_id,upstream,adapter,status)
JOIN models m ON m.model_id=b.school::citext`, provider, retired, current)

	service := New(db, nil)
	read := func() map[string]CatalogModel {
		t.Helper()
		view, err := service.Overview(ctx, provider, "2026-01-01")
		if err != nil {
			t.Fatal(err)
		}
		rows := make(map[string]CatalogModel)
		for _, row := range view.ModelCatalog {
			rows[row.Model] = row
		}
		return rows
	}
	rows := read()
	for name, status := range map[string]string{"retired-binding": "active", "retired-website-record": "pending_configuration"} {
		row := rows[name]
		if !row.Retired || row.BindingIssue != nil || row.SchoolStatus != status || row.CanEnable ||
			!slices.Equal(row.HistoricalChannels, []string{"Gemini"}) {
			t.Errorf("%s 应保留校内状态并退出当前异常: %+v", name, row)
		}
	}
	checkRetired := func(names ...string) {
		t.Helper()
		view, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
		if err != nil {
			t.Fatal(err)
		}
		if len(view.RetiredModels) != len(names) {
			t.Fatalf("能力目录的作废集合有误: %+v", view.RetiredModels)
		}
		for _, name := range names {
			if _, retired := view.RetiredModels[name]; !retired ||
				slices.ContainsFunc(view.Capabilities, func(row catalog.ModelCapability) bool { return row.Model == name }) ||
				slices.ContainsFunc(view.Gaps, func(gap catalog.CapabilityGap) bool { return gap.Model == name }) {
				t.Fatalf("历史型号仍进入当前能力或缺口: %s", name)
			}
		}
	}
	checkRetired("retired-binding", "retired-website-record")
	result, err := service.EnableModelsBatch(ctx, provider, actor, []string{"retired-binding", "retired-website-record"})
	if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 2 {
		t.Fatalf("批量接口应拒绝历史型号: %+v %v", result, err)
	}
	for _, name := range []string{"stale-active-key", "active-binding", "inactive-binding-active-key", "active-binding-retired-key"} {
		row := rows[name]
		if row.Retired || row.BindingIssue == nil {
			t.Errorf("%s 仍需显示线路异常: %+v", name, row)
		}
	}

	exec(`
INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,supported_protocols,last_seen_at,synced_at,created_at,updated_at)
VALUES (gen_random_uuid(),$1,$2,'retired-binding',ARRAY['openai_compatible'],now(),now(),now(),now())`, provider, current)
	exec("INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES ($1,'catalog',now())", current)
	row := read()["retired-binding"]
	if row.Retired || row.CatalogState != "key_listed" || row.CurrentKeyCount != 1 {
		t.Fatalf("同型号被启用 Key 重新列出后应按当前目录展示: %+v", row)
	}
	checkRetired("retired-website-record")

	exec(`INSERT INTO upstream_model_aliases(provider_id,credential_id,upstream_model,school_model,created_by)
VALUES($1,$2,'retired-binding','retired-website-record',$3)`, provider, current, actor)
	checkRetired()
	if read()["retired-website-record"].Retired {
		t.Fatal("已启用 Key 的接替别名保护了原型号")
	}
	exec("DELETE FROM upstream_model_aliases WHERE provider_id=$1", provider)
	other := uuid.New()
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'Other','https://example.test',now(),now())`, other)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,'retired-website-record','openai_responses','active',10,now(),now()
FROM models WHERE model_id='active-binding'`, other)
	checkRetired()
}

func TestBeichenTeacherCoveragePostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	gm1, g3, cc1 := keys[1], keys[2], keys[3]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	chat, responses := "openai_compatible", "openai_responses"
	gpt := []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6.2-astra", "gpt-6.2-sol"}
	deepseek := []string{"deepseek-v4-pro", "deepseek-v4-flash"}
	bind := func(school, upstream string, key uuid.UUID, protocol string) {
		t.Helper()
		exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES(gen_random_uuid(),$1::text,$1::text,'active',1000,now(),now()) ON CONFLICT(model_id) DO NOTHING`, school)
		exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,$4,$5,'active',10,now(),now() FROM models WHERE model_id=$3`, provider, key, school, upstream, protocol)
	}
	group := func(key uuid.UUID, id int) {
		t.Helper()
		exec(`INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at) VALUES($1,1,$2,clock_timestamp())`, key, id)
	}
	probe := func(key uuid.UUID, name, protocol string) {
		t.Helper()
		exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at,last_passed_at)
VALUES($1,$2,$3,$4,200,1,clock_timestamp(),clock_timestamp())
ON CONFLICT(provider_id,credential_id,model,protocol) DO UPDATE SET checked_at=excluded.checked_at,last_passed_at=excluded.last_passed_at`,
			provider, key, name, protocol)
	}
	scan := func(key uuid.UUID, names []string, protocols ...string) {
		t.Helper()
		entries := make([]catalog.ProviderEntry, 0, len(names))
		for _, name := range names {
			entries = append(entries, catalog.ProviderEntry{ID: name, SupportedProtocols: protocols})
		}
		saveCatalogFixture(t, ctx, db, provider, key, entries, nil)
	}
	group(g3, 10)
	group(gm1, 6)
	group(cc1, 24)
	scan(g3, gpt, chat, responses)
	scan(gm1, deepseek, chat)
	for _, name := range gpt {
		bind(name, name, g3, chat)
		bind(name, name, g3, responses)
		probe(g3, name, responses)
	}
	for _, name := range deepseek {
		bind(name, name, gm1, chat)
		probe(gm1, name, chat)
	}
	exec(`INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing)
SELECT c.id,clock_timestamp(),c.updated_at,6,now()+interval '1 hour',mv.id,
 '{"deepseek-v4-pro":{"billing_mode":"token","intervals":[{"min_tokens":0,"input_price":0,"output_price":0.000001}]}}'
FROM provider_credentials c JOIN beichen_multiplier_versions mv ON mv.credential_id=c.id WHERE c.id=$1`, gm1)

	read := func(name string) CatalogModel {
		t.Helper()
		view, err := New(db, nil).Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range view.ModelCatalog {
			if row.Model == name {
				return row
			}
		}
		t.Fatalf("未找到概览型号 %s", name)
		return CatalogModel{}
	}
	capability := func(name string, key uuid.UUID) catalog.ModelCapability {
		t.Helper()
		view, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range view.Capabilities {
			if row.Model == name && row.CredentialID != nil && *row.CredentialID == key {
				return row
			}
		}
		t.Fatalf("未找到渠道能力 %s", name)
		return catalog.ModelCapability{}
	}
	for _, name := range append(slices.Clone(gpt), deepseek...) {
		row := read(name)
		owner, groupID, routes := g3, int64(10), 2
		wantAvailability, wantPriceCovered := catalog.AvailabilityUnavailable, false
		if slices.Contains(deepseek, name) {
			owner, groupID, routes = gm1, 6, 1
			if name == deepseek[0] {
				wantAvailability, wantPriceCovered = catalog.AvailabilityAvailable, true
			}
		}
		if row.BoundKeyCount != 1 || row.CoveredBoundKeyCount != 1 || row.ReadyCount != routes || len(row.BoundRoutes) != routes ||
			row.ModelAvailability.ProbeState != "passed" || row.ModelAvailability.Availability != wantAvailability ||
			row.PriceCovered != wantPriceCovered || row.BindingIssue != nil ||
			*row.BoundRoutes[0].CredentialID != owner || *row.BoundRoutes[0].GroupID != groupID {
			t.Fatalf("绑定证据不应混入其他 Key: %+v", row)
		}
	}
	if row := read(deepseek[0]); row.ModelAvailability.Availability != catalog.AvailabilityAvailable ||
		row.ModelAvailability.ProbeState != "passed" || !row.PriceCovered {
		t.Fatalf("deepseek-v4-pro must stay available with its conditional price: %+v", row)
	}
	if row, cap := read(deepseek[0]), capability(deepseek[0], gm1); !row.PriceCovered || !cap.PriceCovered ||
		row.PriceState != "conditional" || cap.PriceState != row.PriceState {
		t.Fatalf("可计算的条件价应同时计入两个入口: %+v %+v", row, cap)
	}
	if row := read(deepseek[1]); row.PriceCovered || row.PriceState != "missing" ||
		!row.Published || row.ModelAvailability.Availability != catalog.AvailabilityUnavailable {
		t.Fatalf("缺价应保留: %+v", row)
	}

	const raw, school = "claude-sonnet-5-5", "claude-sonnet-5.5"
	saveCatalogFixture(t, ctx, db, provider, cc1, []catalog.ProviderEntry{{
		ID: raw, SupportedProtocols: []string{"anthropic", responses},
	}}, []string{school})
	exec("UPDATE beichen_website_catalogs SET group_id=24 WHERE credential_id=$1", cc1)
	scan(g3, append(gpt, raw), responses)
	bind(school, raw, cc1, "anthropic")
	bind(school, raw, cc1, responses)
	bind(school+"-fallback", raw, g3, responses)
	probe(cc1, raw, responses)
	var schoolID, fallbackID uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM models WHERE model_id=$1", school).Scan(&schoolID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT id FROM models WHERE model_id=$1", school+"-fallback").Scan(&fallbackID); err != nil {
		t.Fatal(err)
	}
	view, err := New(db, nil).Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ModelCatalog) != 8 {
		t.Fatalf("教师概览应以校内模型为身份返回 8 行: %+v", view.ModelCatalog)
	}
	ids := make(map[uuid.UUID]string, len(view.ModelCatalog))
	for _, row := range view.ModelCatalog {
		if row.SchoolModelID == nil || *row.SchoolModelID == uuid.Nil || row.SchoolModel == "" {
			t.Fatalf("目录行缺少校内模型 UUID: %+v", row)
		}
		if previous, ok := ids[*row.SchoolModelID]; ok {
			t.Fatalf("不同目录行复用了校内 UUID %s: %s / %s", *row.SchoolModelID, previous, row.SchoolModel)
		}
		ids[*row.SchoolModelID] = row.SchoolModel
	}
	var rawRows []CatalogModel
	for _, row := range view.ModelCatalog {
		if row.Model == raw {
			rawRows = append(rawRows, row)
		}
	}
	if len(rawRows) != 2 || *rawRows[0].SchoolModelID == *rawRows[1].SchoolModelID {
		t.Fatalf("同一上游的两个校内模型必须分别成行: %+v", rawRows)
	}
	for _, row := range rawRows {
		wantID := fallbackID
		wantKey := g3
		wantRoutes := 1
		if row.SchoolModel == school {
			wantID, wantKey, wantRoutes = schoolID, cc1, 2
		} else if row.SchoolModel != school+"-fallback" {
			t.Fatalf("意外的校内模型身份: %+v", row)
		}
		if *row.SchoolModelID != wantID || len(row.PreferredRoutes) != wantRoutes {
			t.Fatalf("校内模型 UUID 或专属线路错误: %+v", row)
		}
		if row.CatalogState != "key_listed" || row.CurrentKeyCount != 1 ||
			len(row.CatalogKeys) != 1 || row.CatalogKeys[0].CredentialID != wantKey ||
			len(row.CredentialIDs) != 1 || row.CredentialIDs[0] != wantKey ||
			len(row.WebsiteLocations) == 0 {
			t.Fatalf("同上游目录行未保留自己的 Key 和官网证据: %+v", row)
		}
		for _, route := range row.PreferredRoutes {
			if !strings.EqualFold(route.SchoolModel, row.SchoolModel) {
				t.Fatalf("概览行混入了其他校内模型的线路: %+v", row)
			}
		}
	}
	if cap := capability(raw, cc1); !cap.ResponsesReady || cap.Bindings[0].SchoolModel != school {
		t.Fatalf("别名与同协议备用绑定应保留: %+v", cap)
	}
	caps, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(caps.Capabilities, func(row catalog.ModelCapability) bool { return row.Model == school }) {
		t.Fatal("官网拼写不应给同一 Key 留下重复能力记录")
	}

	probeAttempt := func() {
		exec(`INSERT INTO beichen_probe_usage(id,provider_id,credential_id,model,protocol,checked_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,clock_timestamp())`, provider, gm1, deepseek[0], chat)
	}
	probeAttempt()
	group(gm1, 12)
	if row, cap := read(deepseek[0]), capability(deepseek[0], gm1); row.ReadyCount != 0 || row.CoveredBoundKeyCount != 0 ||
		row.BoundKeyCount != 1 || !row.BoundCatalogPending || row.LastProbeAt != nil || row.PriceCovered || cap.PriceCovered ||
		row.ModelAvailability.Availability != "unverified" || cap.Availability != "unverified" || len(cap.AvailableProtocols) != 0 {
		t.Fatalf("换组应废止原组证据: %+v %+v", row, cap)
	}
	scan(gm1, deepseek, chat)
	if row := read(deepseek[0]); row.CoveredBoundKeyCount != 1 || row.ReadyCount != 1 || row.LastProbeAt != nil || row.PriceCovered ||
		row.ModelAvailability.Availability != catalog.AvailabilityUnavailable {
		t.Fatalf("新目录不应恢复旧探活或报价: %+v", row)
	}
	probe(gm1, deepseek[0], chat)
	if row := read(deepseek[0]); row.LastProbeAt != nil || row.ModelAvailability.ProbeState != "unprobed" ||
		row.ModelAvailability.Availability != catalog.AvailabilityUnavailable {
		t.Fatalf("换组前发起的探活即使随后保存通过，也应等待当前组复核: %+v", row)
	}
	probeAttempt()
	probe(gm1, deepseek[0], chat)
	if row := read(deepseek[0]); row.ModelAvailability.Availability != catalog.AvailabilityUnavailable ||
		row.ModelAvailability.ProbeState != "passed" || row.PriceCovered {
		t.Fatalf("同组新探活只恢复探活状态，仍需补价: %+v", row)
	}
	exec("UPDATE beichen_datasets SET last_success=now()-interval '31 minutes' WHERE credential_id=$1 AND kind='catalog'", gm1)
	if row, cap := read(deepseek[0]), capability(deepseek[0], gm1); !row.BoundCatalogPending || row.CoveredBoundKeyCount != 0 ||
		row.ModelAvailability.Availability != "unverified" || row.LastProbeAt != nil || cap.CatalogState != "catalog_stale" || cap.PriceCovered {
		t.Fatalf("过期目录应同时撤销两端的证据: %+v %+v", row, cap)
	}

	const withBackup = "gpt-fixture-with-backup"
	scan(g3, []string{withBackup}, responses)
	scan(gm1, []string{withBackup}, chat)
	bind(withBackup, withBackup, g3, responses)
	bind(withBackup, withBackup, gm1, chat)
	probe(g3, withBackup, responses)
	probe(gm1, withBackup, chat)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate)
VALUES($1,$3,'school','manual','teacher',$4,1000000,2000000),
      ($2,$3,'school','manual','teacher',$4,9000000,10000000)`, g3, gm1, withBackup, actor)
	if row := read(withBackup); row.BoundKeyCount != 2 || row.CoveredBoundKeyCount != 2 ||
		row.ReadyCount != 2 || !capability(withBackup, g3).PriceCovered || !capability(withBackup, gm1).PriceCovered {
		t.Fatalf("both Keys should start priced and ready: %+v", row)
	}
	group(gm1, 13)
	if row := read(withBackup); row.BoundKeyCount != 2 || row.CoveredBoundKeyCount != 1 || row.ReadyCount != 1 ||
		!row.BoundCatalogPending || !row.PriceCovered || row.PriceState != "manual" || capability(withBackup, gm1).PriceCovered ||
		row.ModelAvailability.Availability != "available" || row.ModelAvailability.RecommendedProtocol == nil ||
		*row.ModelAvailability.RecommendedProtocol != responses {
		t.Fatalf("backup group change affected the primary route: %+v", row)
	}
}

func TestBeichenPerKeyCatalogOverviewPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable local nebula_test database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || (cfg.User != "nebula_qa" && cfg.User != "nebula_test") ||
		!testutil.IsIsolatedPostgresDatabase(cfg.Database) ||
		cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires a loopback-only nebula_test database and test role with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema := pgx.Identifier{"beichen_catalog_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+", public")
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatal("locate migrations")
	}
	const sourceMigration = "0061_provider_catalog_source.sql"
	for _, path := range paths {
		if filepath.Base(path) == sourceMigration {
			continue
		}
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(sql))
	}
	var provider uuid.UUID
	if err := tx.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	first, second, alias := uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES ($1,$3,'Gemini','fake',now(),now()),($2,$3,'Claude','fake',now(),now())`, first, second, provider)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_model_catalog
		(id,provider_id,credential_id,model_id,raw_entry,last_seen_at,synced_at,created_at,updated_at)
		VALUES(gen_random_uuid(),$3,$1,'shared','{"source":"beichen_website_group"}',now(),now(),now(),now()),
		(gen_random_uuid(),$3,$1,'website-obsolete','{"source":"beichen_website_group"}',now(),now(),now(),now()),
		(gen_random_uuid(),$3,$2,'shared','{"id":"shared"}',now(),now(),now(),now())`, first, second, provider)
	applyBeichenTestMigrations(t, ctx, tx, sourceMigration)
	checkSource := func(key uuid.UUID, model, wantSource, wantStatus string) {
		t.Helper()
		var source, status string
		err := tx.QueryRowContext(ctx, "SELECT catalog_source,status FROM provider_model_catalog WHERE credential_id=$1 AND model_id=$2", key, model).Scan(&source, &status)
		if err != nil || source != wantSource || status != wantStatus {
			t.Fatalf("%s: expected %s/%s, got %s/%s (%v)", model, wantSource, wantStatus, source, status, err)
		}
	}
	checkSource(first, "shared", "website", "active")
	checkSource(second, "shared", "key", "active")
	execTestSQL(t, ctx, tx, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'school-alias','Alias','active',1000,now(),now()),
		(gen_random_uuid(),'special','Special','pending_configuration',NULL,now(),now())`, alias)
	execTestSQL(t, ctx, tx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,status,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,$3,'shared','openai_responses',0,'active',now(),now())`, alias, provider, first)

	s := New(nil, nil)
	snapshot := func(id uuid.UUID, body string) {
		t.Helper()
		s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.String() != origin+modelsPath {
				t.Fatal("expected a read-only catalog request")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		entries, err := s.fetchKeyCatalog(ctx, "fake-key")
		if err != nil {
			t.Fatalf("fetch catalog: %v", err)
		}
		if err := saveKeyCatalogFixture(ctx, tx, syncJob{provider: provider, credential: id}, entries, s.now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(first, `{"data":[{"id":"shared"},{"id":"removed"}]}`)
	snapshot(first, `{"data":[{"id":"shared"},{"id":"special"},{"id":"no-quote"}]}`)
	snapshot(second, `{"data":[{"id":"shared"}]}`)
	checkSource(first, "shared", "key", "active")
	checkSource(first, "website-obsolete", "website", "inactive")
	// A previous price, another key's quote and even an explicit zero have distinct meanings.
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate)
		VALUES($1,'shared','upstream','effective','billing',0,4),($2,'shared','upstream','effective','billing',19,7),
		($1,'removed','upstream','effective','billing',5,4),($2,'no-quote','upstream','effective','billing',7,4)`, first, second)
	read := func() map[uuid.UUID]map[string]Model {
		t.Helper()
		var view Overview
		if err := readModels(ctx, tx, provider, &view); err != nil {
			t.Fatal(err)
		}
		rows := map[uuid.UUID]map[string]Model{first: {}, second: {}}
		for _, row := range view.Models {
			rows[row.CredentialID][row.Model] = row
		}
		return rows
	}
	rows := read()
	a, b := rows[first]["shared"], rows[second]["shared"]
	if len(rows[first]) != 3 || len(rows[second]) != 1 || a.CatalogSource != "key" || b.CatalogSource != "key" || !a.RouteReady || b.RouteReady ||
		a.SchoolStatus != "active" || b.SchoolStatus != "not_configured" || a.PriceStatus != "confirmed" ||
		a.UpstreamRates == nil || *a.UpstreamRates.Input != "0" || b.UpstreamRates == nil || *b.UpstreamRates.Input != "19" {
		t.Fatalf("incorrect per-key route or price: %+v", rows)
	}
	for name, status := range map[string]string{"special": "pending_configuration", "no-quote": "not_configured"} {
		row := rows[first][name]
		if row.PriceStatus != "pending_confirmation" || row.UpstreamRates != nil || row.SchoolRates != nil ||
			row.SchoolStatus != status || row.RouteReady {
			t.Fatalf("unexpected unpriced model: %+v", row)
		}
	}
	// A new group sample supersedes an older flat quote if the upstream
	// changes only that model to an unsupported pricing format.
	var revision time.Time
	if err := tx.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", first).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	rate := int64(12)
	quote := panelQuote{rows: []priceRow{{Model: "shared", Rates: Rates{Input: &rate, Output: &rate}},
		{Model: "special", Rates: Rates{Input: &rate, Output: &rate}}}, expiresAt: time.Now().Add(time.Minute)}
	saveQuote := func() {
		t.Helper()
		if err := saveBillingObservation(ctx, tx, first, revision, billingInfo{Multiplier: "0.03125"}, nil, 1, quote); err != nil {
			t.Fatal(err)
		}
	}
	saveQuote()
	if read()[first]["special"].PriceStatus != "confirmed" {
		t.Fatal("missing original flat quote")
	}
	quote.rows = quote.rows[:1]
	const actualTerms = `{"billing_mode":"token","input_price":0.000008,"output_price":0.000016,
		"cache_read_price":0,"cache_write_price":0.000001,"cache_write_1h_price":0.000002,
		"cache_write_12h_price":0.000004,"intervals":[{"min_tokens":200000,"input_price":0.000012000000000000000019}]}`
	quote.terms = map[string]json.RawMessage{"special": json.RawMessage(actualTerms)}
	saveQuote()
	rows = read()
	special := rows[first]["special"]
	if special.PriceStatus != "conditional" || special.UpstreamRates != nil || special.SchoolRates != nil ||
		rows[first]["shared"].PriceStatus != "confirmed" || *rows[first]["shared"].UpstreamRates.Input != "0.375" ||
		*rows[first]["shared"].SchoolRates.Input != "37.5" || *rows[second]["shared"].UpstreamRates.Input != "19" {
		t.Fatalf("obsolete or cross-key quote survived a new panel snapshot: %+v", rows)
	}
	var terms struct {
		CacheRead     *json.Number `json:"cache_read_price"`
		CacheWrite1h  json.Number  `json:"cache_write_1h_price"`
		CacheWrite12h json.Number  `json:"cache_write_12h_price"`
		Intervals     []struct {
			Min   int         `json:"min_tokens"`
			Input json.Number `json:"input_price"`
		} `json:"intervals"`
	}
	if json.Unmarshal(special.PriceConditions, &terms) != nil || terms.CacheRead == nil ||
		!samePanelRate(*terms.CacheRead, "0") || !samePanelRate(terms.CacheWrite1h, "0.000002") ||
		!samePanelRate(terms.CacheWrite12h, "0.000004") || len(terms.Intervals) != 1 ||
		terms.Intervals[0].Min != 200000 || !samePanelRate(terms.Intervals[0].Input, "0.000012000000000000000019") {
		t.Fatal("cache durations or interval conditions changed")
	}
	exact, err := json.Marshal(special.PriceConditionsExact)
	if err != nil || !strings.Contains(string(exact), `"input_price":"0.000012000000000000000019"`) ||
		!strings.Contains(string(exact), `"min_tokens":"200000"`) {
		t.Fatal("conditional quote lost precision")
	}
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_datasets(credential_id,kind,last_success)
		VALUES($1,'billing',clock_timestamp()),($2,'billing',clock_timestamp())`, first, second)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,0.01)`, second)
	readRoute := func(upstream, school, adapter string) PreferredRoute {
		t.Helper()
		var view Overview
		if err := readModels(ctx, tx, provider, &view); err != nil {
			t.Fatal(err)
		}
		if err := readPreferredRoutes(ctx, tx, provider, &view); err != nil {
			t.Fatal(err)
		}
		if len(view.ModelCatalog) != 3 {
			t.Fatalf("expected three distinct models: %+v", view.ModelCatalog)
		}
		for _, m := range view.ModelCatalog {
			if m.Model != upstream {
				continue
			}
			if upstream == "shared" && len(m.CredentialIDs) != 2 {
				t.Fatal("lost shared credential")
			}
			if upstream == "shared" && (m.PriceState != "missing" || m.PriceCovered || m.ReadyCount != 1 || m.Activation != "active") {
				t.Fatalf("共享模型的已发布北辰线路或报价状态不正确: %+v", m)
			}
			if upstream == "special" && (m.PriceState != "missing" || m.Activation != "blocked" ||
				len(m.Blockers) != 1 || m.Blockers[0].Code != "conditional_price" ||
				m.PriceBlocker == nil || m.PriceBlocker.Code != "tier_missing_output" ||
				m.PriceBlocker.Message != m.Blockers[0].Message) {
				t.Fatalf("条件计价模型的状态或阻塞原因不正确: %+v", m)
			}
			for _, r := range m.PreferredRoutes {
				if r.SchoolModel == school && r.Adapter == adapter {
					return r
				}
			}
		}
		t.Fatalf("missing route %s %s %s", upstream, school, adapter)
		return PreferredRoute{}
	}
	preferred := readRoute("shared", "school-alias", "openai_responses")
	if preferred.State != "ready" || preferred.CredentialID == nil || *preferred.CredentialID != first ||
		preferred.EffectiveMultiplier == nil || !samePanelRate(*preferred.EffectiveMultiplier, "0.03125") {
		t.Fatalf("configured key should win even when another key charges 0.01: %+v", preferred)
	}
	preview := readRoute("no-quote", "no-quote", "openai_responses")
	if preview.State != "candidate" || preview.CredentialID == nil || *preview.CredentialID != first || preview.Priority != 50 {
		t.Fatalf("unconfigured model must retain a prospective channel: %+v", preview)
	}
	conditional := readRoute("special", "special", "openai_responses")
	if conditional.State != "candidate" {
		t.Fatalf("待配置的条件计价模型不应显示为已发布: %+v", conditional)
	}
	// A different supplier can win the school's route for the same protocol.
	other, otherKey := uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, `INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'Other','https://other.invalid',now(),now())`, other)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'external','fake',now(),now())`, otherKey, other)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,'remote-alias',now(),now(),now(),now())`, other, otherKey)
	execTestSQL(t, ctx, tx, `UPDATE model_bindings SET priority=90 WHERE model_id=$1`, alias)
	execTestSQL(t, ctx, tx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,status,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,$3,'remote-alias','openai_responses',10,'active',now(),now())`, alias, other, otherKey)
	external := readRoute("shared", "school-alias", "openai_responses")
	if external.ProviderID != other || external.EffectiveMultiplier != nil || external.UpstreamModel != "remote-alias" {
		t.Fatalf("preferred external route borrowed Beichen's rate: %+v", external)
	}
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET status='inactive' WHERE id=$1", first)
	if read()[first]["shared"].RouteReady {
		t.Fatal("inactive key reported an active route")
	}
	snapshot(first, `{"data":[]}`)
	if rows := read(); len(rows[first]) != 0 || len(rows[second]) != 1 {
		t.Fatal("empty snapshot changed another key or retained historical models")
	}
}

func TestOverviewCatalogAndCredentialsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	first, second, disabled, empty := keys[3], keys[2], keys[1], keys[0]
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'Mixed',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$3,'MIXED',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$2,'manual',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$3,'unpriced',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$4,'offline',now(),now(),now(),now())`, provider, first, second, disabled)
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", disabled)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
VALUES($1,'Mixed','upstream','effective','billing',0,2000000,0,0,NULL),
 ($1,'manual','school','manual','teacher',1000000,1000000,0,0,$2)`, first, actor)
	base := time.Now().UTC().Add(-4 * time.Minute).Truncate(time.Microsecond)
	exec("UPDATE provider_credentials SET updated_at=$3 WHERE id IN ($1,$2)", first, second, base)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success,last_error,failure_count,next_attempt)
VALUES($1,'catalog',$3,NULL,0,'-infinity'),
 ($1,'billing',$3+interval '1 minute','账单同步失败',2,$3+interval '7 minutes'),
 ($1,'key_usage',$3+interval '2 minutes','较早的用量同步失败',1,$3+interval '5 minutes'),
 ($1,'usage',$3+interval '3 hours','旧公开账单读取失败',1,$3+interval '1 day'),
 ($1,'prices',$3+interval '3 minutes',NULL,0,'-infinity'),
 ($2,'catalog',$3,NULL,0,'-infinity'),
 ($2,'prices',NULL,'价格同步失败',0,'-infinity')`, first, second, base)

	service := New(db, nil)
	view, err := service.Overview(ctx, provider, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[uuid.UUID]CredentialSummary)
	for _, entry := range view.Credentials {
		byKey[entry.CredentialID] = entry
	}
	entry := byKey[first]
	if len(byKey) != 4 || entry.Label != "CC1" || entry.Status != "active" ||
		entry.ModelCount != 2 || entry.PricedModelCount != 2 || entry.LastError == nil || *entry.LastError != "账单同步失败" {
		t.Fatalf("渠道汇总有误: %+v", view.Credentials)
	}
	for i, timestamp := range []*time.Time{entry.CatalogLastSuccess, entry.BillingLastSuccess, entry.UsageLastSuccess, entry.PricesLastSuccess} {
		if timestamp == nil || timestamp.Location() != time.UTC || !timestamp.Equal(base.Add(time.Duration(i)*time.Minute)) {
			t.Errorf("渠道成功时间 %d 有误: %v", i, timestamp)
		}
	}
	entry = byKey[empty]
	if entry.Label != "Gemini" || entry.ModelCount != 0 || entry.PricedModelCount != 0 ||
		entry.LastError != nil || entry.CatalogLastSuccess != nil || entry.BillingLastSuccess != nil ||
		entry.PricesLastSuccess != nil || entry.UsageLastSuccess != nil || byKey[disabled].Status != "inactive" {
		t.Fatalf("空渠道或停用状态有误: %+v", view.Credentials)
	}

	models := make(map[string]CatalogModel)
	for _, model := range view.ModelCatalog {
		models[strings.ToLower(model.Model)] = model
	}
	mixed := models["mixed"]
	if mixed.Activation != "blocked" || mixed.CanEnable || mixed.PriceState != "mixed" || mixed.KeyCount != 2 ||
		mixed.RouteCount != 4 || mixed.ReadyCount != 0 || len(mixed.PreferredRoutes) != 0 {
		t.Errorf("同一协议的两把候选 Key 需要教师确定线路: %+v", mixed)
	}
	manual := models["manual"]
	if manual.PriceState != "manual" || manual.Activation != "ready" {
		t.Errorf("仅手动价的模型应可启用: %+v", manual)
	}
	for name, want := range map[string]string{"unpriced": "not_configured,price_missing,sync_failed"} {
		model := models[name]
		var codes []string
		for _, blocker := range model.Blockers {
			if blocker.Message == "" {
				t.Errorf("%s 缺少原因文案: %+v", name, blocker)
			}
			codes = append(codes, blocker.Code)
		}
		if model.Activation != "blocked" || model.PriceState != "missing" || strings.Join(codes, ",") != want {
			t.Errorf("%s 的阻塞原因有误: %+v", name, model)
		}
	}
	if offline := models["offline"]; !offline.Retired || offline.CanEnable || len(offline.Blockers) != 0 {
		t.Fatalf("仅关联停用渠道的历史型号应退出启用预览: %+v", offline)
	}
	result, err := service.EnableModelsBatch(ctx, provider, actor, []string{mixed.Model, manual.Model})
	if err != nil || len(result.Enabled) != 1 || result.Enabled[0].Model != manual.Model ||
		len(result.Skipped) != 1 || result.Skipped[0].Model != mixed.Model ||
		!strings.Contains(result.Skipped[0].Message, "同一协议有多个候选线路") {
		t.Fatalf("批量启用应保留歧义并发布已就绪模型: %+v, %v", result, err)
	}
	view, err = service.Overview(ctx, provider, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range view.ModelCatalog {
		if strings.EqualFold(model.Model, mixed.Model) && (model.Activation != "blocked" || model.RouteCount != 4 || model.ReadyCount != 0) {
			t.Errorf("跳过歧义时应保留所有候选线路: %+v", model)
		}
	}
}
