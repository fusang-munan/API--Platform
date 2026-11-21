package beichen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

const publicKeyHost = "beichenapi.com"

func websiteFixture(t *testing.T, file string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/" + file + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestWebsiteCC2GroupCatalog(t *testing.T) {
	raw := websiteFixture(t, "cc2-website-channels")
	crawl, err := decodeWebsiteDirectory([]byte(raw), time.Now())
	if err != nil || crawl.GroupCount != 3 || crawl.ModelCount != 5 || string(crawl.rawResponse) != raw {
		t.Fatalf("group crawl = %+v, %v", crawl.CrawlRun, err)
	}
	cases := []struct {
		id     int64
		models []string
		rate   string
	}{
		{301, []string{"claude-haiku-4-5", "claude-sonnet-4-6"}, "0.3"},
		{602, []string{"claude-opus-4-6", "claude-sonnet-4-6"}, "0.6"},
		{160, []string{"glm-5.3", "kimi-k3.1-pro"}, "0.16"},
	}
	for _, want := range cases {
		found, err := selectWebsiteKeyCatalog(crawl.platforms, want.id)
		if err != nil {
			t.Fatal(err)
		}
		if found.group.Rate == nil || found.group.Rate.String() != want.rate ||
			!slices.Equal(found.catalogQuote().websiteModels, want.models) {
			t.Fatalf("incorrect group %d: %+v", want.id, found.catalogQuote().websiteModels)
		}
	}
	cc2, _ := selectWebsiteKeyCatalog(crawl.platforms, 602)
	info, quote, err := cc2.billing()
	if err != nil || quote.err != nil || info.Multiplier != "0.6" || len(quote.rows) != 2 {
		t.Fatalf("incorrect CC2 price: %+v, %v", quote, err)
	}
	// Prices from the website are per token. Fixed price versions store
	// millionths of a balance unit per million tokens.
	rates := quote.rows[0].Rates
	if rates.Input == nil || *rates.Input != 10_000_000 || rates.Output == nil || *rates.Output != 45_000_000 ||
		rates.CacheRead == nil || *rates.CacheRead != 1_000_000 || rates.CacheWrite == nil || *rates.CacheWrite != 12_500_000 {
		t.Fatalf("CC2 Opus rate mismatch: %+v", rates)
	}
}

func TestBeichenWebsiteCC2KeyUsagePostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	cc2, cc03 := keys[3], keys[1]
	manager, err := security.NewManager([]byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)),
		[]byte(strings.Repeat("c", 32)), []byte(strings.Repeat("d", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, manager)
	const bearer = "cc2-panel-fixture"
	const day = "2026-11-21"
	for id, key := range map[uuid.UUID]string{cc2: "sk-fixture-cc2", cc03: "sk-fixture-cc03"} {
		ciphertext, err := manager.EncryptCredential(key)
		if err != nil {
			t.Fatal(err)
		}
		execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", id, ciphertext)
	}

	// Older versions attached a public account sample to this credential.
	// Preserve history while proving it can no longer supply Key evidence.
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_key_observations (credential_id,key_digest,models,models_fetched_at,balance,unit,usage_fetched_at)
SELECT id,sha256(credential_ciphertext),'["glm-5.3","kimi-k3.1-pro"]',now(),4.97,'USD',now()
FROM provider_credentials WHERE id=$1`, cc2)
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_key_usage_days(credential_id,key_digest,day,unit,daily_usage,fetched_at)
SELECT id,sha256(credential_ciphertext),$2::date,'USD','{"date":"2026-11-21","cost":1,"actual_cost":0.16}',now()
FROM provider_credentials WHERE id=$1`, cc2, day)
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,billed_upstream_micros,source)
VALUES($1,$2,$3::date,'glm-5.3',2,160000,'usage')`, provider, cc2, day)
	read := func() (Overview, KeyCapability) {
		t.Helper()
		view, err := service.Overview(ctx, provider, day)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range view.KeyCapabilities {
			if key.CredentialID == cc2 {
				return view, key
			}
		}
		t.Fatal("missing CC2 capability")
		return view, KeyCapability{}
	}
	observedAt := time.Date(2026, 11, 21, 14, 0, 0, 0, beijing)
	service.now = func() time.Time { return observedAt }
	_, prior := read()
	if prior.Balance != nil || len(prior.Models) != 0 || prior.DailyUsage != nil || prior.WebsiteGroup != nil {
		t.Fatalf("account data became single-Key evidence: %+v", prior)
	}
	reconcile := func() ReconciliationReport {
		t.Helper()
		report, err := service.Reconciliation(ctx, provider, "day", observedAt)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	if report := reconcile(); report.Totals.Upstream.ActualCost != nil || len(report.Models) != 0 {
		t.Fatalf("legacy public amount reached accounting: %+v", report.Totals.Upstream)
	}

	keysBody := websiteFixture(t, "cc2-website-keys")
	channelsBody := websiteFixture(t, "cc2-website-channels")
	usageBody := websiteFixture(t, "cc2-website-usage")
	badKey, hits := false, 0
	service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.URL.Scheme != "https" || r.URL.Host != "beichenapi.com" || r.Method != http.MethodGet ||
			r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body := ""
		switch r.URL.Path {
		case "/api/v1/keys":
			body = keysBody
		case "/api/v1/channels/available":
			body = channelsBody
		case "/api/v1/usage":
			q := r.URL.Query()
			if q.Get("api_key_id") != "42" || q.Get("start_date") != q.Get("end_date") ||
				q.Get("timezone") != "Asia/Shanghai" || q.Get("page") != "1" || q.Get("sort_order") != "asc" {
				t.Fatalf("usage query lacks Key or day scope: %v", q)
			}
			switch q.Get("start_date") {
			case day:
				body = usageBody
				if badKey {
					body = strings.Replace(body, `"api_key_id": 42`, `"api_key_id": 43`, 1)
				}
			case "2026-11-20":
				body = `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"pages":0}}`
			default:
				t.Fatal("unexpected usage window")
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	target, claude := "0.6", true
	input := KeyUsageTarget{Multiplier: &target, ExpectClaude: &claude}
	if err := service.RefreshKeyUsage(ctx, provider, cc2, input); err == nil || hits != 0 {
		t.Fatal("a missing web session must not fall back to the public API")
	}
	service.ConfigurePanelFallback(bearer)
	if err := service.RefreshKeyUsage(ctx, provider, uuid.New(), input); domain.ErrorCode(err) != domain.CodeNotFound || hits != 0 {
		t.Fatal("a different credential must not send a request")
	}
	if err := service.RefreshKeyUsage(ctx, provider, cc2, input); err != nil {
		t.Fatal(err)
	}
	view, result := read()
	if result.WebsiteKeyID == nil || *result.WebsiteKeyID != 42 || result.WebsiteGroup == nil ||
		result.WebsiteGroup.ID != 602 || *result.WebsiteGroup.Rate != "0.6" ||
		result.GroupStatus != "assigned" || result.Balance != nil || result.HasClaude == nil || !*result.HasClaude ||
		!slices.Equal(result.Models, []string{"claude-opus-4-6", "claude-sonnet-4-6"}) ||
		result.GroupCheck != "target_observed" || result.DailyUsage == nil ||
		*result.DailyUsage.Cost != "0.11" || *result.DailyUsage.ActualCost != "0.066" ||
		*result.DailyUsage.ObservedMultiplier != "0.6" || result.DailyUsage.CacheRead != nil ||
		*result.DailyUsage.Input != 1000 || *result.DailyUsage.Output != 62 || len(result.ModelStats) != 2 ||
		*result.ModelStats[0].CacheRead != 2000 || *result.ModelStats[0].CacheWrite != 120 ||
		*result.ModelStats[1].Input != 0 || result.ModelStats[1].CacheWrite != nil {
		t.Fatalf("invalid CC2 evidence: %+v", result)
	}
	billing := datasetForCredential(t, view, cc2, "billing")
	if billing.GroupStatus != "assigned" || billing.GroupID == nil || *billing.GroupID != 602 {
		t.Fatalf("a Key check did not initialize its shared group observation: %+v", billing)
	}
	if report := reconcile(); report.Totals.Upstream.ActualCost == nil || *report.Totals.Upstream.ActualCost != "0.066" ||
		report.Totals.Upstream.Unit == nil || *report.Totals.Upstream.Unit != "USD" || len(report.Models) != 2 {
		t.Fatalf("website amount did not reach comparison: %+v", report.Totals.Upstream)
	}
	checkRaw := func() {
		t.Helper()
		var sealed []byte
		err := db.QueryRowContext(ctx, `SELECT raw_website_pages FROM beichen_key_usage_days WHERE credential_id=$1 AND day=$2::date`, cc2, day).Scan(&sealed)
		if err != nil {
			t.Fatal(err)
		}
		unsealed, err := manager.DecryptCredential(sealed)
		var pages [][]byte
		if err != nil || json.Unmarshal([]byte(unsealed), &pages) != nil || len(pages) != 1 || string(pages[0]) != usageBody {
			t.Fatal("raw usage JSON was not preserved")
		}
	}
	checkRaw()

	badKey = true
	if err := service.RefreshKeyUsage(ctx, provider, cc2, KeyUsageTarget{}); err == nil {
		t.Fatal("endpoint returned a different Key's row and reported success")
	}
	_, stale := read()
	if stale.UsageState != "stale" || stale.ModelsState != "fresh" || *stale.DailyUsage.ActualCost != "0.066" {
		t.Fatalf("failed usage overwrote good data: %+v", stale)
	}
	checkRaw()

	service.now = time.Now
	if _, err := service.CrawlPanelDirectory(ctx, provider); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, "SELECT raw_website_response FROM beichen_crawl_runs WHERE provider_id=$1", provider).Scan(&raw); err != nil ||
		string(raw) != channelsBody {
		t.Fatal("raw channel JSON was not preserved")
	}
	for _, id := range []uuid.UUID{cc2, cc03} {
		if busy, err := service.runJob(ctx, syncJob{provider: provider, credential: id, kind: "billing"}, true); err != nil || busy {
			t.Fatalf("price sync %s: %v", id, err)
		}
	}
	for _, want := range []struct {
		id          uuid.UUID
		group       int64
		input, rate string
	}{{cc2, 602, "4000000", "0.6"}, {cc03, 301, "6000000", "0.3"}} {
		var group int64
		var price, rate string
		err := db.QueryRowContext(ctx, `
SELECT p.group_id,p.input_rate::text,trim_scale(m.multiplier)::text
FROM beichen_price_versions p JOIN beichen_panel_snapshots s USING (credential_id,effective_at,group_id)
JOIN beichen_multiplier_versions m ON m.id=s.multiplier_version
WHERE p.credential_id=$1 AND p.model='claude-sonnet-4-6'`, want.id).Scan(&group, &price, &rate)
		if err != nil || group != want.group || price != want.input || rate != want.rate {
			t.Fatal(fmt.Errorf("stored scoped price group=%d input=%s rate=%s: %v", group, price, rate, err))
		}
	}
}

func TestWebsiteUsageMoneyAndEmptyFields(t *testing.T) {
	const raw = `{"model":"fixture","cost":0,"actual_cost":"","input_tokens":0,"output_tokens":null,"cache_read_tokens":"","cache_write_tokens":0,"currency":"USD","image_count":0,"video_output_tokens":""}`
	var row receiptRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	day, err := summarizeWebsiteUsage([]receiptRow{row}, "2026-11-21")
	if err != nil || day.daily.Cost == nil || *day.daily.Cost != "0" || day.daily.ActualCost != nil ||
		day.daily.Input == nil || *day.daily.Input != 0 || day.daily.CacheWrite == nil || *day.daily.CacheWrite != 0 ||
		day.daily.CacheRead != nil || day.daily.Output != nil {
		t.Fatalf("empty fields and zero conflated: %+v, %v", day.daily, err)
	}
	service := New(nil, nil)
	service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			websiteFixture(t, "cc1-public-usage")))}, nil
	})
	if _, err := service.fetchWebsiteUsageDay(context.Background(), "fixture-session", listedKey{ID: 42, UserID: 7}, "2026-11-21"); err == nil {
		t.Fatal("an account usage aggregate cannot satisfy the filtered page contract")
	}
}
