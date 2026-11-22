package beichen

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBillingSyncReadsKeyCatalogPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	manager := switchTestManager(t)
	service := New(db, manager)
	const bearer, keyPrefix = "catalog-fixture-session", "sk-fixture-catalog-"
	service.ConfigurePanelFallback(bearer)
	tests := []struct {
		label  string
		actual []string
		extra  []string
	}{
		{label: "Gemini", extra: []string{"gemini-web-only"}},
		{label: "GM1", actual: []string{"deepseek-chat", "glm-5", "gpt-shared"}},
		{label: "G3", actual: []string{"gpt-shared", "gpt-a", "gpt-b", "gpt-c", "gpt-d"},
			extra: []string{"gpt-web-only", "gpt-web-published", "gpt-web-other"}},
		{label: "CC1", actual: []string{"claude-sonnet-5", "claude-opus-5"}},
	}
	const gpt = 2
	exec := func(query string, args ...any) { t.Helper(); execCatalogTestSQL(t, ctx, db, query, args...) }
	var index int
	failed := false
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("测试只允许读取元数据")
		}
		reply := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		tc := tests[index]
		group := 71 + index
		secret := keyPrefix + tc.label
		token := req.Header.Get("Authorization")
		if req.URL.String() == origin+modelsPath && token == "Bearer "+secret {
			if failed {
				return reply(http.StatusForbidden, "")
			}
			var entries []string
			for _, name := range tc.actual {
				entries = append(entries, fmt.Sprintf(`{"id":%q}`, name))
			}
			return reply(http.StatusOK, `{"data":[`+strings.Join(entries, ",")+`]}`)
		}
		if req.URL.Host == publicKeyHost && token == "Bearer "+bearer {
			switch req.URL.Path {
			case "/api/v1/keys":
				return reply(http.StatusOK, websiteKeyListFixture(secret, tc.label, 41+index, group))
			case "/api/v1/channels/available":
				var entries []string
				for _, name := range append(slices.Clone(tc.actual), tc.extra...) {
					entries = append(entries, fmt.Sprintf(`{"name":%q,"pricing":{"input_price":0.000001,"output_price":0.000002,"cache_read_price":0,"cache_write_price":0}}`, name))
				}
				return reply(http.StatusOK, websiteGroupFixture(group, "0.2", strings.Join(entries, ",")))
			case "/api/v1/groups/rates":
				return reply(http.StatusOK, fmt.Sprintf(`{"code":0,"data":{%q:0.2}}`, fmt.Sprint(group)))
			}
		}
		return nil, errors.New("请求超出假上游允许的元数据路径")
	})
	overview := func() Overview {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	model := func(view Overview, name string) CatalogModel {
		t.Helper()
		for _, row := range view.ModelCatalog {
			if row.Model == name {
				return row
			}
		}
		t.Fatalf("目录缺少 %s", name)
		return CatalogModel{}
	}
	for i, tc := range tests {
		ciphertext, err := manager.EncryptCredential(keyPrefix + tc.label)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE provider_credentials SET credential_ciphertext=$2, updated_at=clock_timestamp()-interval '2 hours' WHERE id=$1", keys[i], ciphertext)
	}
	exec(`INSERT INTO provider_model_catalog
(id,provider_id,credential_id,model_id,catalog_source,last_seen_at,synced_at,created_at,updated_at)
VALUES (gen_random_uuid(),$1,$2,'gpt-web-only','website',now(),now(),now(),now()),
       (gen_random_uuid(),$1,$2,'gpt-a','website',now(),now(),now(),now())`, provider, keys[gpt])
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success)
VALUES($1,'catalog',clock_timestamp())`, keys[gpt])
	exec(`INSERT INTO beichen_website_catalogs(credential_id,credential_revision,group_id,checked_at,models)
SELECT id,updated_at,73,clock_timestamp(),ARRAY['gpt-a','gpt-web-only']
FROM provider_credentials WHERE id=$1`, keys[gpt])
	legacy := overview()
	if row := model(legacy, "gpt-web-only"); row.CatalogState != "website_only" || row.KeyCount != 0 || row.CanEnable ||
		legacy.WebsiteOnlyStatus != "unverified" || len(row.CatalogKeys) != 1 || row.CatalogKeys[0].State != "pending" {
		t.Fatalf("旧官网目录行不能被当作 Key 证据: %+v", row)
	}
	school := uuid.New()
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,'gpt-web-published','Fixture school model','active',1000,now(),now())`, school)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,'gpt-web-published','openai_responses',10,now(),now())`, school, provider, keys[gpt])

	runBilling := func(i int) error {
		t.Helper()
		index = i
		busy, err := service.runJob(ctx, syncJob{provider: provider, credential: keys[i], kind: "billing"}, true)
		if busy {
			t.Fatal("没有并发任务时不应占用计费租约")
		}
		return err
	}
	for i := range tests {
		if err := runBilling(i); err != nil {
			t.Fatalf("%s 计费同步失败: %v", tests[i].label, err)
		}
	}
	fresh := overview()
	actual := make(map[uuid.UUID][]string)
	for _, row := range fresh.Models {
		if row.CatalogSource != "key" {
			t.Fatalf("同步后仍有启用的官网源行: %+v", row)
		}
		actual[row.CredentialID] = append(actual[row.CredentialID], row.Model)
	}
	for i, tc := range tests {
		if got := actual[keys[i]]; !slices.Equal(got, slices.Sorted(slices.Values(tc.actual))) {
			t.Fatalf("%s 应保存 %v，实际保存 %v", tc.label, tc.actual, got)
		}
	}
	if state := datasetForCredential(t, fresh, keys[0], "catalog"); state.LastSuccess == nil || state.LastError != nil {
		t.Fatalf("明确空目录应记录为成功: %+v", state)
	}
	if row := model(fresh, "gpt-shared"); row.KeyCount != 2 || row.CurrentKeyCount != 2 || row.StaleKeyCount != 0 {
		t.Fatalf("跨渠道型号应按当前 Key 去重: %+v", row)
	}
	if row := model(fresh, "gpt-web-only"); row.CatalogState != "website_only" || row.KeyCount != 0 || row.CanEnable {
		t.Fatalf("官网独有型号不能提供 Key 覆盖: %+v", row)
	}
	if _, err := service.EnableModel(ctx, provider, actor, "gpt-web-only", ""); err == nil {
		t.Fatal("官网独有型号不应能够启用")
	}
	if row := model(fresh, "gpt-web-published"); row.CatalogState != "published_unavailable" ||
		row.KeyCount != 0 || row.BindingIssue == nil || slices.ContainsFunc(fresh.WebsiteOnlyModels, func(item WebsiteOnlyModel) bool {
		return item.Model == row.Model
	}) {
		t.Fatalf("已发布且仅官网有记录的型号应保留在异常目录: %+v", row)
	}
	gptDiff := slices.IndexFunc(fresh.CatalogDifferences, func(diff CatalogDifference) bool {
		return diff.CredentialID == keys[gpt]
	})
	if gptDiff < 0 {
		t.Fatal("GPT Key 缺少目录对比")
	}
	if diff := fresh.CatalogDifferences[gptDiff]; diff.State != "compared" || len(diff.KeyOnly) != 0 ||
		!slices.Equal(diff.WebsiteOnly, slices.Sorted(slices.Values(tests[gpt].extra))) {
		t.Fatalf("GPT 分组的 8 个官网型号与 5 个 Key 型号差异有误: %+v", diff)
	}

	checkStale := func(view Overview) {
		t.Helper()
		row := model(view, "gpt-shared")
		if row.KeyCount != 1 || row.CurrentKeyCount != 1 || row.StaleKeyCount != 1 ||
			!slices.ContainsFunc(row.CatalogKeys, func(key CatalogKey) bool {
				return key.CredentialID == keys[gpt] && key.State == "listed" && key.Stale
			}) {
			t.Fatalf("过期 Key 应保留证据且当前覆盖只计 GM1: %+v", row)
		}
		if row := model(view, "gpt-a"); row.KeyCount != 0 || row.StaleKeyCount != 1 || row.CatalogState == "key_listed" {
			t.Fatalf("只有过期结果的型号不应计入 Key 覆盖: %+v", row)
		}
	}
	exec("UPDATE beichen_datasets SET last_success=clock_timestamp()-interval '31 minutes' WHERE credential_id=$1 AND kind='catalog'", keys[gpt])
	checkStale(overview())
	if err := runBilling(gpt); err != nil {
		t.Fatal(err)
	}
	beforeFailure := overview()
	failed = true
	if err := runBilling(gpt); err == nil {
		t.Fatal("目录 HTTP 403 应报告部分同步失败")
	}
	afterFailure := overview()
	checkStale(afterFailure)
	catalog := datasetForCredential(t, afterFailure, keys[gpt], "catalog")
	billing := datasetForCredential(t, afterFailure, keys[gpt], "billing")
	if catalog.LastError == nil || *catalog.LastError != "北辰接口返回 HTTP 403" || catalog.LastSuccess == nil ||
		!catalog.LastSuccess.Equal(*datasetForCredential(t, beforeFailure, keys[gpt], "catalog").LastSuccess) ||
		billing.BillingState != "current" || billing.LastError != nil || billing.LastSuccess == nil ||
		!billing.LastSuccess.After(*datasetForCredential(t, beforeFailure, keys[gpt], "billing").LastSuccess) {
		t.Fatalf("目录失败应保留上轮目录并提交计费: catalog=%+v billing=%+v", catalog, billing)
	}
}
