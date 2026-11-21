package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	detailClaude = "claude-sonnet-4-6"
	detailGPT    = "gpt-6.2-astra"
)

func readWebsitePriceDetailsFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/website-price-details.json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBeichenWebsitePriceDetails(t *testing.T) {
	body := readWebsitePriceDetailsFixture(t)
	report, err := New(nil, nil).CrawlDirectory(context.Background(), CrawlOptions{Fixture: body})
	if err != nil || !report.Complete || report.ModelCount != 2 || report.GroupCount != 4 {
		t.Fatalf("报价抓取报告不完整：%+v (%v)", report.CrawlRun, err)
	}
	for _, want := range []struct {
		name   string
		groups []int64
		prices map[string]string
	}{
		{detailClaude, []int64{2}, map[string]string{
			"input_price": "3", "output_price": "15", "cache_write_price": "3.75", "cache_read_price": "0.3",
		}},
		{detailGPT, []int64{11, 8, 10}, map[string]string{
			"input_price": "8", "output_price": "40", "cache_write_price": "10", "cache_write_1h_price": "10", "cache_read_price": "0.4",
		}},
	} {
		row := crawlTestModel(t, report, want.name)
		if len(row.Sources) != 1 {
			t.Fatalf("%s 官网来源数 = %d", want.name, len(row.Sources))
		}
		source := row.Sources[0]
		if !source.PriceComplete || source.PriceBlocker != "" || !maps.Equal(source.BaseTokenPricesPerMillion, want.prices) {
			t.Fatalf("%s 每百万 Token 官网价：%+v", want.name, source)
		}
		var groups []int64
		for _, group := range source.Groups {
			groups = append(groups, group.ID)
		}
		if !slices.Equal(groups, want.groups) {
			t.Fatalf("%s 官网分组 = %v", want.name, groups)
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(source.Pricing, &raw) != nil || string(raw["billing_mode"]) != `"token"` || string(raw["intervals"]) != "[]" {
			t.Fatalf("%s 原始 Token 报价结构丢失：%s", want.name, source.Pricing)
		}
		if want.name == detailClaude && string(raw["cache_write_1h_price"]) != "null" {
			t.Fatal("Claude 的官网报价没有列出 1 小时单价")
		}
	}
}

func TestBeichenWebsitePriceDetailsGroups(t *testing.T) {
	ctx := context.Background()
	complete := readWebsitePriceDetailsFixture(t)
	// Leave group membership intact while making the other channel's quote invalid.
	partial := bytes.Replace(complete, []byte(`"input_price": 0.000003`), []byte(`"input_price": false`), 1)
	service := New(nil, nil)
	body := complete
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != panelURL {
			return nil, errors.New("仅模拟官网渠道页 GET")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	check := func(groupID int64, name string) {
		t.Helper()
		directory, err := service.fetchWebsiteKeyCatalog(ctx, "fixture-web-session", listedKey{GroupID: groupID})
		if err != nil || len(directory.entries) != 1 || directory.entries[0].ID != name {
			t.Fatalf("分组 %d 应只得到 %s：%+v (%v)", groupID, name, directory.entries, err)
		}
	}
	check(2, detailClaude)
	for _, id := range []int64{8, 10, 11} {
		check(id, detailGPT)
	}
	if _, err := service.fetchWebsiteKeyCatalog(ctx, "fixture-web-session", listedKey{GroupID: 16}); !errors.Is(err, errPanelGroupNotFound) {
		t.Fatalf("分组 16 应保留独立状态：%v", err)
	}
	body = partial
	report, err := service.CrawlDirectory(ctx, CrawlOptions{Fixture: partial})
	if !errors.Is(err, errWebsiteDirectoryPartial) || report.Complete {
		t.Fatalf("预期报告仍会记录损坏的报价：%v", err)
	}
	check(10, detailGPT)
}

func TestBeichenWebsitePriceDetailsPostgres(t *testing.T) {
	ctx, db, provider, _, credentials := newActionsPostgres(t)
	body := readWebsitePriceDetailsFixture(t)
	const claudeKey, gptKey = "sk-fixture-price-details-claude", "sk-fixture-price-details-gpt"
	manager := switchTestManager(t)
	service := New(db, manager)
	service.ConfigurePanelFallback("fixture-web-session")
	priceCredentials := []uuid.UUID{credentials[3], credentials[2]} // CC1, G3
	for i, key := range []string{claudeKey, gptKey} {
		sealed, err := manager.EncryptCredential(key)
		if err != nil {
			t.Fatal(err)
		}
		execCatalogTestSQL(t, ctx, db, `UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1`, priceCredentials[i], sealed)
	}
	keysFixture := fmt.Sprintf(`{"code":0,"data":{"items":[
{"id":41,"user_id":1,"group_id":16,"key":%q,"name":"fixture Claude","status":"active","group":{"id":16,"name":"分组 16","rate_multiplier":0.6}},
{"id":42,"user_id":1,"group_id":10,"key":%q,"name":"fixture GPT","status":"active","group":{"id":10,"rate_multiplier":0.18}}
],"total":2,"page":1,"pages":1,"page_size":100}}`, claudeKey, gptKey)
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("仅模拟元数据 GET")
		}
		var reply string
		switch {
		case req.URL.String() == panelURL:
			reply = string(body)
		case req.URL.Host == publicKeyHost && req.URL.Path == "/api/v1/keys":
			reply = keysFixture
		case req.URL.Host == "api.beichenapi.com" && req.Header.Get("Authorization") == "Bearer "+claudeKey && req.URL.Path == billingPath:
			reply = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
"effective_rate_multiplier":0.6,"group_rate_multiplier":0.6,"peak_rate_enabled":false,"group_id":16,"api_key_id":41,"observed_at":%q}`,
				time.Now().UTC().Format(time.RFC3339Nano))
		case req.URL.Host == "api.beichenapi.com" && req.Header.Get("Authorization") == "Bearer "+claudeKey && req.URL.Path == modelsPath:
			reply = `{"data":[{"id":"claude-sonnet-4-6","supported_protocols":["anthropic"]}]}`
		default:
			return nil, errors.New("未模拟的接口")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})
	if _, err := service.syncWebsiteDirectory(ctx, provider, true); err != nil {
		t.Fatal(err)
	}
	for _, credential := range priceCredentials {
		job := syncJob{kind: "billing", provider: provider, credential: credential}
		if _, err := service.runJob(ctx, job, true); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Overview(ctx, provider, "")
	if err != nil {
		t.Fatal(err)
	}
	readModel := func(name string) CatalogModel {
		t.Helper()
		index := slices.IndexFunc(view.ModelCatalog, func(model CatalogModel) bool { return model.Model == name })
		if index < 0 {
			t.Fatalf("教师模型目录缺少 %s", name)
		}
		return view.ModelCatalog[index]
	}
	gpt := readModel(detailGPT)
	if len(gpt.WebsiteLocations) != 1 || !gpt.WebsiteLocations[0].PriceComplete || !gpt.PriceCovered || gpt.PriceState != "conditional" ||
		gpt.WebsiteLocations[0].PriceConditionsExact["cache_write_1h_price"] != "0.00001" {
		t.Fatalf("教师概览缺少 GPT 的 1 小时报价：%+v", gpt)
	}
	claude := readModel(detailClaude)
	if len(claude.WebsiteLocations) != 1 || !claude.WebsiteLocations[0].PriceComplete || claude.PriceCovered || claude.Activation != "blocked" ||
		claude.WebsiteLocations[0].PriceConditionsExact["cache_write_price"] != "0.00000375" {
		t.Fatalf("教师概览应提供 Claude 官网价，并等待当前 Key 的报价：%+v", claude)
	}
	if slices.ContainsFunc(claude.Blockers, func(blocker CatalogBlocker) bool { return blocker.Code == "sync_failed" }) {
		t.Fatal("当前 Key 分组未列官网报价被误报为同步失败")
	}
	assertGroupMismatch := func(matches []CatalogKey, credential uuid.UUID) {
		t.Helper()
		for _, match := range matches {
			if match.CredentialID == credential && match.GroupID != nil && *match.GroupID == 16 &&
				match.GroupDiagnosis == "price_outside_key_group" {
				return
			}
		}
		t.Fatalf("未识别官网分组 2 与 Claude Key 分组 16 的差异：%+v", matches)
	}
	assertGroupMismatch(claude.CatalogKeys, priceCredentials[0])
	report, err := service.CrawlDirectory(ctx, CrawlOptions{Fixture: body, MatchDB: true, ProviderID: provider})
	if err != nil || report.KeyMatching != "checked" {
		t.Fatalf("抓取报告应核对 Key 分组：%+v (%v)", report.CrawlRun, err)
	}
	assertGroupMismatch(crawlTestModel(t, report, detailClaude).KeyMatches, priceCredentials[0])
}
