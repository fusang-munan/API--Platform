package beichen

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBeichenCC1AccountRateFallbackPostgres(t *testing.T) {
	ctx, db, provider, actor, credentials := newActionsPostgres(t)
	const secret, bearer = "sk-fixture-cc1-account-rate", reviewCapture
	const model = "claude-sonnet-5-5"
	const keyID, groupID = 12094, 16
	credential := credentials[3]
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		execCatalogTestSQL(t, ctx, db, query, args...)
	}
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", credential, ciphertext)
	exec("UPDATE provider_credentials SET status='inactive' WHERE id<>$1", credential)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'CC1',sha256($2::bytea),false)`, credential, ciphertext)

	service := New(db, manager)
	configureReviewCapture(t, service)
	if err := service.ConfigurePanel(reviewOld, fmt.Sprintf(`{%q:%d}`, credential.String(), groupID)); err != nil {
		t.Fatal(err)
	}
	service.ConfigurePanelFallback(reviewOld)
	if _, err := service.ClearPanelAccount(ctx, provider, actor); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return at }
	today := at.In(beijing).Format(dayLayout)
	quote := websiteGroupFixture(groupID, "0.6", fmt.Sprintf(`{"name":%q,"pricing":{
		"billing_mode":"token","input_price":0.000002,"output_price":0.00001,"cache_read_price":0,"cache_write_price":0}}`, model))
	keyList := websiteKeyListFixture(secret, "CC1", keyID, groupID)
	actual := "0.006"
	billingStatus, billingGroup := http.StatusOK, groupID
	sampleAge := time.Second
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("only fixture metadata GETs are accepted")
		}
		var body string
		switch auth := req.Header.Get("Authorization"); {
		case req.URL.String() == panelURL && auth == "Bearer "+bearer:
			body = quote
		case req.URL.Host == publicKeyHost && auth == "Bearer "+bearer:
			switch req.URL.Path {
			case "/api/v1/keys":
				body = keyList
			case "/api/v1/groups/rates":
				body = `{"code":0,"data":{"6":0.08,"10":0.2}}`
			case "/api/v1/usage":
				query := req.URL.Query()
				if query.Get("api_key_id") != "12094" || query.Get("start_date") != query.Get("end_date") {
					return nil, errors.New("expected CC1-specific usage")
				}
				body = emptyWebsiteUsageFixture
				if query.Get("start_date") == today {
					body = fmt.Sprintf(`{"code":0,"data":{"items":[{
						"id":1,"user_id":13,"api_key_id":%d,"group_id":%d,"model":%q,"created_at":%q,
						"total_cost":0.01,"actual_cost":%s,"currency":"USD"}],
						"total":1,"page":1,"pages":1,"page_size":100}}`, keyID, groupID, model, at.Format(time.RFC3339Nano), actual)
				}
			}
		case req.URL.Host == "api.beichenapi.com" && auth == "Bearer "+secret:
			switch req.URL.Path {
			case modelsPath:
				body = fmt.Sprintf(`{"data":[{"id":%q,"supported_protocols":["anthropic"]}]}`, model)
			case billingPath:
				if billingStatus != http.StatusOK {
					body := "fixture-response:" + secret + ":" + bearer
					return &http.Response{StatusCode: billingStatus, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				groupField := ""
				if billingGroup != 0 {
					groupField = fmt.Sprintf(`"group_id":%d,`, billingGroup)
				}
				body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
					"api_key_id":%d,%s"group_rate_multiplier":0.60,"effective_rate_multiplier":0.6,
					"peak_rate_enabled":false,"observed_at":%q}`, keyID, groupField, at.Add(-sampleAge).Format(time.RFC3339Nano))
			}
		}
		if body == "" {
			return nil, errors.New("unexpected metadata request")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	checkDirectory := func(wantRate, wantSource, wantKey string) {
		t.Helper()
		// Each crawl has a distinct observation time, as its stored history requires.
		at = at.Add(time.Millisecond)
		if _, err := service.CrawlPanelDirectory(ctx, provider); err != nil {
			t.Fatal(err)
		}
		var rate, source, key, rateError string
		err := db.QueryRowContext(ctx, `
SELECT COALESCE(group_rules->>'account_rate_multiplier',''),
       COALESCE(group_rules->>'account_rate_source',''), COALESCE(group_rules->>'account_rate_key_id',''),
       COALESCE(r.account_rate_error,'')
FROM beichen_group_price_observations o JOIN beichen_crawl_runs r USING (provider_id)
WHERE o.provider_id=$1 AND o.group_id=$2 AND o.model=$3 AND o.source='website'
ORDER BY o.observed_at DESC LIMIT 1`, provider, groupID, model).Scan(&rate, &source, &key, &rateError)
		if err != nil || rate != wantRate || source != wantSource || key != wantKey || (rateError != "") != (wantRate == "") {
			t.Fatalf("unexpected directory price evidence: %s %s %s %q (%v)", rate, source, key, rateError, err)
		}
	}
	job := syncJob{provider: provider, credential: credential, kind: "billing"}
	checkBilling := func(wantState, wantReason string) {
		t.Helper()
		wantPriced := wantReason == ""
		busy, err := service.runJob(ctx, job, true)
		if busy || (wantPriced && err != nil) || (!wantPriced && !errors.Is(err, errWebsiteAccountRateUnverified)) {
			t.Fatalf("unexpected billing job result: busy=%t err=%v", busy, err)
		}
		view, err := service.Overview(ctx, provider, today)
		if err != nil {
			t.Fatal(err)
		}
		item := datasetForCredential(t, view, credential, "billing")
		if item.BillingState != wantState || (item.AccountRate != nil) != wantPriced ||
			(item.AccountRateSource == keyBillingRateSource) != wantPriced ||
			(wantPriced && (item.EffectiveMultiplierText != "0.6" || *item.AccountRate != "0.6" || item.LastError != nil)) ||
			(view.WebsiteSync.PricedChannels == 1) != wantPriced {
			t.Fatalf("unexpected billing or coverage after rate check: %+v", item)
		}
		if !wantPriced {
			want := "北辰官网账户倍率未核实，分组倍率仅供参考：" + wantReason
			if item.LastError == nil || *item.LastError != want {
				t.Fatalf("expected stored billing error %q, got %+v", want, item)
			}
		}
		for _, capability := range view.KeyCapabilities {
			if capability.CredentialID == credential && (capability.WebsiteGroup == nil ||
				(capability.WebsiteGroup.AccountRateSource == keyBillingRateSource) != wantPriced) {
				t.Fatal("CC1 capability disagrees with current billing evidence")
			}
		}
		var hasPrice, matchesAmount bool
		err = db.QueryRowContext(ctx, `
SELECT upstream_input IS NOT NULL, COALESCE(upstream_input=1200000,false)
FROM beichen_price_at($1,$2,statement_timestamp())`, credential, model).Scan(&hasPrice, &matchesAmount)
		if err != nil || hasPrice != wantPriced || matchesAmount != wantPriced {
			t.Fatalf("wrong payable snapshot: has=%t amount=%t (%v)", hasPrice, matchesAmount, err)
		}
	}

	const insufficientUsage = "近 30 分钟用量不足以核对"
	checkBilling("unverified", insufficientUsage)
	if inventory, err := service.RefreshWebsiteKeys(ctx, provider); err != nil || len(inventory.Keys) != 1 ||
		inventory.Keys[0].MatchStatus != "matched" {
		t.Fatalf("could not verify the fixture's CC1 inventory: %v", err)
	}
	checkDirectory("", "", "")
	// The inventory's default-rate observation is stale on arrival.
	checkBilling("stale", insufficientUsage)
	if err := service.RefreshKeyUsage(ctx, provider, credential, KeyUsageTarget{}); err != nil {
		t.Fatal(err)
	}
	checkDirectory("0.6", keyBillingRateSource, "12094")
	checkBilling("current", "")

	// One Key's rate cannot be published as the shared rate of another Key.
	singleKeyList := keyList
	keyList = strings.Replace(keyList, `}],`, `},{"id":12095,"user_id":13,"group_id":16,"name":"other","status":"active"}],`, 1)
	keyList = strings.Replace(keyList, `"total":1`, `"total":2`, 1)
	checkDirectory("", "", "")
	checkBilling("current", "")
	keyList = singleKeyList

	// Even fresh, matching usage cannot supply a missing group_id.
	billingGroup = 0
	checkBilling("stale", "Key 账单缺少分组号（group_id 未提供或为 0）")
	billingGroup = groupID
	checkBilling("current", "")

	billingStatus = http.StatusNotFound
	checkBilling("stale", "Key 账单读取失败（HTTP 404）")
	billingStatus = http.StatusOK
	sampleAge = panelQuoteTTL + time.Second
	checkBilling("stale", "Key 账单采样时段已结束")
	sampleAge = time.Second

	exec("UPDATE beichen_key_usage_days SET fetched_at=$2 WHERE credential_id=$1", credential, at.Add(-31*time.Minute))
	checkDirectory("", "", "")
	checkBilling("stale", insufficientUsage)
	actual = "0.005999999999"
	if err := service.RefreshKeyUsage(ctx, provider, credential, KeyUsageTarget{}); err != nil {
		t.Fatal(err)
	}
	checkDirectory("", "", "")
	checkBilling("stale", "近 30 分钟用量的实付与原价比值和 Key 账单倍率不一致")
}
