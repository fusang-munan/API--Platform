package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func readWebsitePricingFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/website-pricing-2026-11-21.json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBeichenCrawlRealPriceFixture(t *testing.T) {
	report, err := New(nil, nil).CrawlDirectory(context.Background(), CrawlOptions{Fixture: readWebsitePricingFixture(t)})
	if err != nil || !report.Complete || report.ModelCount != 35 || report.ExpectedModelCount != 35 ||
		len(report.Models) != 35 || report.PlatformCount != 6 || report.GroupCount != 9 || len(report.Failures) != 0 ||
		report.PriceUnit != websitePriceUnit {
		t.Fatalf("expected complete directory with 35 models: %+v (%v)", report.CrawlRun, err)
	}
	count := 0
	for _, model := range report.Models {
		for _, source := range model.Sources {
			if !source.PriceComplete || source.PriceBlocker != "" {
				t.Errorf("incomplete quote for %s: %s", model.Name, source.PriceBlocker)
			}
			count++
		}
	}
	if count != 37 {
		t.Fatalf("want 37 sources for the 35 unique models, got %d", count)
	}
	for _, name := range []string{"gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"} {
		price := crawlTestModel(t, report, name).Sources[0].Pricing
		if !bytes.Contains(price, []byte(`"input_multiplier":2`)) || !usage.BeichenTokenQuoteUsable(price) {
			t.Errorf("lost source tier or usable Token price for %s", name)
		}
	}
	for _, name := range []string{"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		price := crawlTestModel(t, report, name).Sources[0].Pricing
		if !bytes.Contains(price, []byte(`"per_request_price":0.04`)) || usage.BeichenTokenQuoteUsable(price) {
			t.Errorf("incorrect image quote or Token eligibility for %s", name)
		}
	}
}

func TestBeichenCrawlPriceHistoryAndKeyModelsPostgres(t *testing.T) {
	ctx, db, provider, _, credentials := newActionsPostgres(t)
	manager := switchTestManager(t)
	service := New(db, manager)
	service.ConfigurePanelFallback(uuid.NewString())
	body := bytes.ReplaceAll(readWebsitePricingFixture(t), []byte("\r\n"), []byte("\n"))
	cc := []string{"claude-fable-5", "claude-fable-5-1", "claude-fable-5.1", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
		"claude-opus-5", "claude-opus-5-5", "claude-opus-5.5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-sonnet-5.5"}
	cc[0] = strings.ToUpper(cc[0])
	gm := []string{"deepseek-v4-pro", "deepseek-v4.1-flash", "minimax-m3.1-flash", "fixture-only-key"}
	var items []map[string]any
	models := map[string][]string{}
	denied := ""
	rates := `{"code":0,"data":{"6":0.08,"10":0.2}}`
	rateStatus := http.StatusOK
	exec := func(query string, args ...any) { t.Helper(); execCatalogTestSQL(t, ctx, db, query, args...) }
	exec(`INSERT INTO beichen_website_key_sync (provider_id,status,last_seen) VALUES($1,'current',now())`, provider)
	for i, key := range []struct {
		channel string
		group   int
		models  []string
	}{
		{"CC1", 2, cc}, {"GM1", 6, gm}, {"G3", 10, []string{"GTA6"}}, {"Gemini", 5, nil},
	} {
		credential := credentials[i]
		secret := "sk-" + uuid.NewString()
		if key.models == nil {
			denied = secret
		} else {
			models[secret] = key.models
		}
		ciphertext, err := manager.EncryptCredential(secret)
		if err != nil {
			t.Fatal(err)
		}
		id := 41 + i
		items = append(items, map[string]any{"id": id, "user_id": 1, "group_id": key.group,
			"key": secret, "name": key.channel, "status": "active"})
		exec(`UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1`, credential, ciphertext)
		exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,$2,sha256($3::bytea),false)`, credential, key.channel, ciphertext)
		exec(`INSERT INTO beichen_website_keys(provider_id,upstream_key_id,group_id,name,status,credential_id,ciphertext_digest,match_status)
VALUES($1,$2,$3,$4,'active',$5,sha256($6::bytea),'matched')`, provider, id, key.group, key.channel, credential, ciphertext)
	}
	keyList, err := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"items": items,
		"total": len(items), "page": 1, "pages": 1, "page_size": 100}})
	if err != nil {
		t.Fatal("cannot encode the fake Key list")
	}
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("only metadata GET is accepted")
		}
		reply := func(code int, data []byte) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(data))}, nil
		}
		switch {
		case req.URL.String() == panelURL:
			return reply(http.StatusOK, body)
		case req.URL.Host == publicKeyHost && req.URL.Path == "/api/v1/keys":
			return reply(http.StatusOK, keyList)
		case req.URL.Host == publicKeyHost && req.URL.Path == "/api/v1/groups/rates":
			return reply(rateStatus, []byte(rates))
		case req.URL.Host == "api.beichenapi.com" && req.URL.Path == modelsPath:
			secret := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if secret == denied {
				return reply(http.StatusForbidden, nil)
			}
			if ids, ok := models[secret]; ok {
				entries := make([]map[string]string, 0, len(ids))
				for _, id := range ids {
					entries = append(entries, map[string]string{"id": id})
				}
				data, _ := json.Marshal(map[string]any{"data": entries})
				return reply(http.StatusOK, data)
			}
		}
		return nil, errors.New("unexpected metadata endpoint")
	})

	refresh := func() {
		t.Helper()
		if summary, err := service.syncWebsiteDirectory(ctx, provider, true); err != nil || summary.Failures == nil || *summary.Failures != 0 {
			t.Fatalf("crawl failed: %v", err)
		}
	}
	rowCount := func(want int) {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM beichen_group_price_observations WHERE provider_id=$1 AND source='website'`, provider).Scan(&count); err != nil || count != want {
			t.Fatalf("history rows = %d, want %d: %v", count, want, err)
		}
	}
	refresh()
	rowCount(59)
	refresh()
	rowCount(59)
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT key_model_comparisons FROM beichen_crawl_runs WHERE provider_id=$1`, provider).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var comparisons []CrawlKeyModelComparison
	if err := json.Unmarshal(raw, &comparisons); err != nil || len(comparisons) != 4 {
		t.Fatal("four Key comparisons missing", err)
	}
	normalized := 0
	for _, comparison := range comparisons {
		switch comparison.Channel {
		case "CC1", "GM1":
			want := map[string]int{"CC1": 12, "GM1": 3}[comparison.Channel]
			if comparison.State != "checked" || len(comparison.Matched) != want || len(comparison.OnlyChannel) != 0 ||
				(comparison.Channel == "GM1" && !slices.Equal(comparison.OnlyKey, []string{"fixture-only-key"})) {
				t.Fatalf("incomplete comparison for %s", comparison.Channel)
			}
			for _, match := range comparison.Matched {
				if !match.PriceComplete {
					t.Errorf("missing price for %s", match.ChannelModel)
				}
				if match.Match == "normalized" {
					normalized++
				}
			}
		case "G3":
			if comparison.State != "checked" || len(comparison.Matched) != 1 || len(comparison.OnlyChannel) != 7 {
				t.Fatal("models missing from the Key were counted as available")
			}
		case "Gemini":
			if comparison.State != "unavailable" || !strings.Contains(comparison.Error, "403") || len(comparison.OnlyChannel) != 0 {
				t.Fatal("a rejected model list was treated as an empty list")
			}
		}
	}
	if normalized != 1 {
		t.Fatalf("wanted one unambiguous normalized match, got %d", normalized)
	}
	changed := strings.Replace(string(body), `"rate_multiplier": 0.1,`, `"rate_multiplier": 0.12,`, 1)
	if changed == string(body) {
		t.Fatal("could not change the GM1 rate in the test")
	}
	body = []byte(changed)
	refresh()
	rowCount(62)
	const firstModel = "{\n              \"name\": \"deepseek-v4-pro\","
	before, after, found := strings.Cut(changed, firstModel)
	repriced := strings.Replace(after, `"input_price": 0.000009,`, `"input_price": 0.000011,`, 1)
	if !found || repriced == after {
		t.Fatal("could not change the GM1 quote in the test")
	}
	body = []byte(before + `{"name":"fixture-new-model","pricing":{"input_price":0,"output_price":0}},` + firstModel + repriced)
	refresh()
	rowCount(64)
	refresh()
	rowCount(64)
	rates = `{"code":0,"data":{"6":0.07,"10":0.2}}`
	refresh()
	rowCount(68)
	// A damaged entry in group 6 must not suppress group 10's valid observation.
	rates = `{"code":0,"data":{"6":"invalid","10":0.22}}`
	refresh()
	rowCount(80)
	var validAccount string
	if err := db.QueryRowContext(ctx, `
SELECT trim_scale((group_rules->>'account_rate_multiplier')::numeric)
FROM beichen_group_price_observations
WHERE provider_id=$1 AND group_id=10 AND source='website'
ORDER BY observed_at DESC LIMIT 1`, provider).Scan(&validAccount); err != nil || validAccount != "0.22" {
		t.Fatalf("valid group 10 rate did not reach history while group 6 was malformed: %q (%v)", validAccount, err)
	}
	rateStatus = http.StatusBadGateway
	refresh()
	rowCount(88)
	var gmRate, otherRate, gmAccount, gmInput, otherInput, unit string
	if err := db.QueryRowContext(ctx, `
SELECT a.group_rules->>'rate_multiplier',b.group_rules->>'rate_multiplier',a.group_rules->>'account_rate_multiplier',
       a.pricing->>'input_price',b.pricing->>'input_price',a.unit
FROM beichen_group_price_observations a JOIN beichen_group_price_observations b USING(provider_id,model)
WHERE a.provider_id=$1 AND a.model='deepseek-v4-pro' AND a.group_id=6 AND b.group_id=15
ORDER BY a.observed_at DESC LIMIT 1`, provider).Scan(&gmRate, &otherRate, &gmAccount, &gmInput, &otherInput, &unit); err != nil ||
		gmRate != "0.12" || otherRate != "0.16" || gmAccount != "0.12" || gmInput != "0.000011" || otherInput != "0.000009" || unit != websitePriceUnit {
		t.Fatalf("group history or currency was mixed: %s/%s, %s/%s, %s (%v)", gmRate, otherRate, gmInput, otherInput, unit, err)
	}
}
