package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestWebsiteKeyBillingAccountRates(t *testing.T) {
	const bearer, key = "fixture-web-session", "sk-fixture"
	const model = `{"name":"fixture","pricing":{"input_price":0.000002,"output_price":0.00001}}`
	const keyFields = `"group_id":16,"api_key_id":12094,"group_rate_multiplier":0.60,"effective_rate_multiplier":0.6,"peak_rate_enabled":false`
	const usage = `{"date":"2026-11-22","group_id":16,"requests":2,"cost":0.01,"actual_cost":0.006}`
	for _, test := range []struct {
		name              string
		rates             string
		ratesStatus       int
		keyFields         string
		keyStatus         int
		sampleAge         time.Duration
		usage             string
		peakGroup         bool
		unknownPeak       bool
		nonStandard       bool
		wantRate          json.Number
		wantReason        string
		rejectKey         string
		wantKeyRead       bool
		wantConflict      bool
		wantGroupFallback bool
	}{
		{name: "website account rate wins", rates: `{"16":0.50}`, wantRate: "0.50"},
		{name: "billing 0.8 conflicts with an outage", ratesStatus: http.StatusServiceUnavailable,
			keyFields:    strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":0.8`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "billing 0.8 conflicts with an invalid table", rates: "null",
			keyFields:    strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":0.8`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "billing 0.8 conflicts with another malformed group",
			rates:        `{"6":"invalid"}`,
			keyFields:    strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":0.8`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "billing 0.2 conflicts with a reverse rate", ratesStatus: http.StatusServiceUnavailable,
			keyFields:    strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":0.2`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "billing group 17 conflicts with Key group 16", ratesStatus: http.StatusServiceUnavailable,
			keyFields:    strings.Replace(keyFields, `"group_id":16`, `"group_id":17`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "peak uses the website account rate", rates: `{"16":0.20}`, peakGroup: true, wantRate: "0.4"},
		{name: "CC1 explicit fields and exact evidence", usage: usage, wantRate: "0.6", wantKeyRead: true},
		{name: "effective 0.5 conflicts with the default",
			keyFields:    strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":0.5`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "account rates outage allows the verified group default", ratesStatus: http.StatusServiceUnavailable,
			keyStatus: http.StatusNotFound, wantKeyRead: true, rejectKey: "Key 账单读取失败（HTTP 404）"},
		{name: "invalid account rates response still prefers verified Key billing", rates: "null",
			usage: usage, wantRate: "0.6", wantKeyRead: true},
		{name: "subscription group cannot sign the default rate", nonStandard: true,
			wantReason: "官网分组不是标准余额计费"},
		{name: "unknown peak state does not allow the default rate", unknownPeak: true,
			wantReason: "官网分组未确认关闭高峰计费"},
		{name: "peak group cannot use the default rate", peakGroup: true,
			wantReason: "官网分组未确认关闭高峰计费"},
		{name: "Key billing HTTP 404 falls back to the group default", keyStatus: http.StatusNotFound, wantKeyRead: true,
			rejectKey: "Key 账单读取失败（HTTP 404）"},
		{name: "omitted group is distinct from a conflicting group", keyFields: strings.TrimPrefix(keyFields, `"group_id":16,`),
			rejectKey: "Key 账单缺少分组号（group_id 未提供或为 0）", wantKeyRead: true},
		{name: "zero group is missing", keyFields: strings.Replace(keyFields, `"group_id":16`, `"group_id":0`, 1),
			rejectKey: "Key 账单缺少分组号（group_id 未提供或为 0）", wantKeyRead: true},
		{name: "different group", keyFields: strings.Replace(keyFields, `"group_id":16`, `"group_id":17`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "different API Key", keyFields: strings.Replace(keyFields, `"api_key_id":12094`, `"api_key_id":12093`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "legacy key_id cannot supply api_key_id", keyFields: strings.Replace(keyFields, "api_key_id", "key_id", 1),
			rejectKey: "Key 账单缺少 Key 编号（api_key_id 未提供或为 0）", wantKeyRead: true},
		{name: "different group default", keyFields: strings.Replace(keyFields, `"group_rate_multiplier":0.60`, `"group_rate_multiplier":0.59`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "missing group default is not filled from the Key list", keyFields: strings.Replace(keyFields, `"group_rate_multiplier":0.60,`, "", 1),
			rejectKey: "近 30 分钟用量读取失败", wantKeyRead: true, wantGroupFallback: true},
		{name: "billing without effective rate uses group default",
			keyFields: strings.Replace(keyFields, `"effective_rate_multiplier":0.6,`, "", 1),
			rejectKey: "Key 账单格式或生效倍率无效", wantKeyRead: true, wantGroupFallback: true},
		{name: "different peak flag", keyFields: strings.Replace(keyFields, `"peak_rate_enabled":false`, `"peak_rate_enabled":true`, 1),
			wantConflict: true, wantKeyRead: true},
		{name: "missing peak flag", keyFields: strings.TrimSuffix(keyFields, `,"peak_rate_enabled":false`),
			rejectKey: "Key 账单缺少高峰状态", wantKeyRead: true},
		{name: "effective rate out of range", keyFields: strings.Replace(keyFields, `"effective_rate_multiplier":0.6`, `"effective_rate_multiplier":1001`, 1),
			rejectKey: "Key 账单格式或生效倍率无效", wantKeyRead: true},
		{name: "different resolved rate", keyFields: keyFields + `,"resolved_rate_multiplier":0.59`,
			wantConflict: true, wantKeyRead: true},
		{name: "expired sample", sampleAge: panelQuoteTTL + time.Second,
			rejectKey: "Key 账单采样时段已结束", wantKeyRead: true},
		{name: "future sample", sampleAge: -2 * time.Second,
			rejectKey: "Key 账单采样时间无法核对", wantKeyRead: true},
		{name: "missing usage", usage: "missing", rejectKey: "近 30 分钟用量不足以核对", wantKeyRead: true},
		{name: "usage with no explicit group", usage: strings.Replace(usage, `"group_id":16,`, "", 1),
			rejectKey: "近 30 分钟用量不足以核对", wantKeyRead: true},
		{name: "usage cost only matches after rounding", usage: strings.Replace(usage, "0.006", "0.005999999999", 1),
			rejectKey: "近 30 分钟用量的实付与原价比值和 Key 账单倍率不一致", wantKeyRead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const keyID, groupID = 12094, 16
			job := syncJob{provider: uuid.New(), credential: uuid.New(), kind: "billing"}
			service := New(db, nil)
			at := time.Date(2026, 11, 22, 12, 15, 0, 0, time.UTC)
			service.now = func() time.Time { return at }
			expectUsage := func() {
				if test.usage == "" {
					return
				}
				rows := sqlmock.NewRows([]string{"daily_usage"})
				if test.usage != "missing" {
					rows.AddRow(test.usage)
				}
				mock.ExpectQuery("FROM beichen_key_usage_days").WithArgs(job.credential, keyID, "2026-11-22",
					at.Add(-keyUsageFreshness), at).WillReturnRows(rows)
			}
			expectUsage()
			if test.wantReason == "" && !test.wantConflict {
				mock.ExpectQuery("SELECT EXISTS").WithArgs(job.provider, groupID).
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			}
			groupBody := websiteGroupFixture(groupID, "0.60", model)
			if test.nonStandard {
				groupBody = strings.Replace(groupBody, `"subscription_type":"standard"`, `"subscription_type":"subscription"`, 1)
			}
			if test.peakGroup {
				groupBody = strings.Replace(groupBody, `"peak_rate_enabled":false`, `"peak_rate_enabled":true,
					"peak_start":"12:00","peak_end":"12:20","peak_rate_multiplier":2,"timezone":"UTC"`, 1)
			}
			if test.unknownPeak {
				groupBody = strings.Replace(groupBody, `,"peak_rate_enabled":false`, "", 1)
			}
			rates := test.rates
			if rates == "" {
				rates = `{"6":0.08,"10":0.2}`
			}
			fields := test.keyFields
			if fields == "" {
				fields = keyFields
			}
			age := test.sampleAge
			if age == 0 {
				age = time.Second
			}
			keyAt := at.Add(-age)
			var rateReads, keyReads int
			service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
				reply := func(status int, body string) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				auth := req.Header.Get("Authorization")
				if req.Method == http.MethodGet && req.URL.Host == publicKeyHost && auth == "Bearer "+bearer {
					switch req.URL.Path {
					case "/api/v1/channels/available":
						return reply(http.StatusOK, groupBody)
					case "/api/v1/groups/rates":
						rateReads++
						if test.ratesStatus != 0 {
							return reply(test.ratesStatus, "fixture-response:"+bearer)
						}
						return reply(http.StatusOK, `{"code":0,"data":`+rates+`}`)
					}
				}
				if req.Method == http.MethodGet && req.URL.Host == "api.beichenapi.com" && req.URL.Path == billingPath && auth == "Bearer "+key {
					keyReads++
					if test.keyStatus != 0 {
						return reply(test.keyStatus, "fixture-response:"+key+":"+bearer)
					}
					return reply(http.StatusOK, fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
						"observed_at":%q,%s}`, keyAt.Format(time.RFC3339), fields))
				}
				t.Errorf("unexpected fixture endpoint or authorization for %s", req.URL.Path)
				return nil, errors.New("unexpected fixture request")
			})
			ctx := context.Background()
			identity := listedKey{ID: keyID, GroupID: groupID, Group: &panelGroup{Rate: "0.6"}}
			directory, err := service.fetchBillingCatalog(ctx, job, bearer, key, identity)
			if err != nil {
				t.Fatal(err)
			}
			info, quote, err := directory.billing()
			if test.wantConflict {
				if err == nil || !errors.Is(err, errAccountRateConflict) || !errors.Is(err, errWebsiteAccountRateUnverified) ||
					directory.group.AccountRate != nil || directory.group.AccountRateSource != "" ||
					info.accountRateSource != "" || len(quote.terms) != 0 {
					t.Fatalf("billing evidence conflict must block the group fallback: info=%+v quote=%v error=%v", info, quote.terms, err)
				}
			} else if test.wantReason != "" {
				want := "北辰官网账户倍率未核实，分组倍率仅供参考：" + test.wantReason
				if !errors.Is(err, errWebsiteAccountRateUnverified) || err.Error() != want ||
					directory.group.AccountRate != nil || info.accountRateSource != "" || len(quote.terms) != 0 {
					t.Fatalf("unexpected rejected account rate: info=%+v error=%v", info, err)
				}
			} else {
				wantRate, wantSource, wantID := test.wantRate, "", int64(0)
				wantObservation := at
				switch {
				case test.wantGroupFallback:
					wantSource, wantRate = groupRateSource, "0.60"
				case test.rejectKey != "":
					wantSource = groupRateSource
					if wantRate == "" {
						wantRate = "0.60"
					}
				case test.wantKeyRead:
					wantSource, wantID, wantObservation = keyBillingRateSource, keyID, keyAt
				}
				if err != nil || quote.err != nil || len(quote.terms) != 1 || info.GroupRate != "0.60" ||
					info.Multiplier != wantRate || info.UserRate == nil || info.accountRateSource != wantSource ||
					directory.group.AccountRateSource != wantSource || directory.group.AccountRateKeyID != wantID ||
					!info.ObservedAt.Equal(wantObservation) {
					t.Fatalf("unexpected billing info=%+v, quote error=%v, billing error=%v", info, quote.err, err)
				}
				if wantSource == groupRateSource && *info.UserRate != "0.60" {
					t.Fatalf("unexpected default rate before applying peak rules: %s", *info.UserRate)
				}
				wantExpiry := wantObservation.Add(panelQuoteTTL)
				if test.peakGroup {
					wantExpiry = at.Add(5 * time.Minute)
				}
				if !quote.expiresAt.Equal(wantExpiry) {
					t.Fatalf("unexpected quote expiry: got %v, want %v", quote.expiresAt, wantExpiry)
				}
			}
			wantKeyReads := 0
			if test.wantKeyRead {
				wantKeyReads = 1
			}
			if rateReads != 1 || keyReads != wantKeyReads || !slices.Equal(directory.catalogQuote().websiteModels, []string{"fixture"}) {
				t.Fatalf("unexpected reads or website models: rates=%d Key=%d models=%v", rateReads, keyReads, directory.catalogQuote().websiteModels)
			}
			if test.rejectKey != "" {
				// Keep the stricter evidence requirements covered independently of the group default.
				expectUsage()
				_, _, err := service.fetchVerifiedKeyAccountRate(ctx, job.credential, key, identity, directory.group)
				want := "北辰官网账户倍率未核实，分组倍率仅供参考：" + test.rejectKey
				if !errors.Is(err, errWebsiteAccountRateUnverified) || err.Error() != want {
					t.Fatalf("expected Key evidence rejection %q, got %v", want, err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectoryAccountRateCoverage(t *testing.T) {
	defaultRate := json.Number("0.6")
	peakOff, peakOn := false, true
	crawl := directoryCrawl{platforms: []directoryPlatform{{Groups: []directoryGroup{
		{WebsiteGroup: WebsiteGroup{ID: 1, Rate: &defaultRate, PeakEnabled: &peakOff}, Subscription: "standard"},
		{WebsiteGroup: WebsiteGroup{ID: 2, Rate: &defaultRate, PeakEnabled: &peakOff}, Subscription: "standard"},
		{WebsiteGroup: WebsiteGroup{ID: 3, Rate: &defaultRate, PeakEnabled: &peakOn}, Subscription: "standard"},
		{WebsiteGroup: WebsiteGroup{ID: 4, Rate: &defaultRate}, Subscription: "standard"},
	}}}}
	rates := map[int64]json.Number{1: "0.4"}
	fallback := map[int64]crawlKeyRate{1: {groupID: 1},
		2: {groupID: 2, info: &billingInfo{Multiplier: "0.5", APIKeyID: 42}},
		3: {groupID: 3, groupDefault: &defaultRate}}
	crawl.applyAccountRates(rates, fallback, nil)
	groups := crawl.platforms[0].Groups
	if crawl.AccountRateError != "" || !matchesAccountRate(&groups[0].WebsiteGroup, "0.4", "", 0) ||
		!matchesAccountRate(&groups[1].WebsiteGroup, "0.5", keyBillingRateSource, 42) ||
		!matchesAccountRate(&groups[2].WebsiteGroup, "0.6", groupRateSource, 0) ||
		!matchesAccountRate(&groups[3].WebsiteGroup, "", "", 0) {
		t.Fatalf("unexpected directory coverage: %s %+v", crawl.AccountRateError, groups)
	}
	*groups[2].AccountRate = "0.9"
	if *groups[2].Rate != "0.6" {
		t.Fatal("the fallback rate must be independent of the default rate")
	}

	fallback[4] = crawlKeyRate{groupID: 4}
	crawl.applyAccountRates(rates, fallback, nil)
	if crawl.AccountRateError != "部分北辰渠道的账户倍率未核实" ||
		!matchesAccountRate(&groups[3].WebsiteGroup, "", "", 0) {
		t.Fatalf("directory did not report a checked group without verified evidence: %s", crawl.AccountRateError)
	}
}

func TestKeyCapabilityAccountRateEvidence(t *testing.T) {
	rate, inherited := json.Number("0.6"), json.Number("0.5")
	const otherKey = 99
	keyID, groupID := int64(42), int64(16)
	for _, test := range []struct {
		name   string
		source string
		rate   *json.Number
		keyID  int64
	}{
		{name: "current group default clears the inherited personal Key ID", source: groupRateSource, rate: &rate},
		{name: "current personal rate uses its own Key ID", source: keyBillingRateSource, rate: &rate, keyID: keyID},
		{name: "pending billing clears the inherited fallback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := KeyCapability{WebsiteKeyID: &keyID, WebsiteGroup: &WebsiteGroup{
				ID: groupID, AccountRate: &inherited, AccountRateSource: keyBillingRateSource, AccountRateKeyID: otherKey,
			}}
			key.applyGroup(Dataset{
				GroupID: &groupID, GroupStatus: "assigned", AccountRate: test.rate, AccountRateSource: test.source, accountRateKeyID: test.keyID,
			})
			if key.WebsiteGroup.AccountRate != test.rate || key.WebsiteGroup.AccountRateSource != test.source ||
				key.WebsiteGroup.AccountRateKeyID != test.keyID {
				t.Fatalf("wrong account rate evidence: %+v", key.WebsiteGroup)
			}
		})
	}
}

func TestWebsiteKeyBillingSharedBasePrices(t *testing.T) {
	ctx := context.Background()
	service := New(nil, nil)
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return at }
	website := string(readWebsitePricingFixture(t))
	accountRates := `{"code":0,"data":{"2":0.15,"16":0.60}}`
	const bearer, key = "fixture-web-session", "sk-fixture-cc1"
	const opus, sonnet = "claude-opus-5-5", "claude-sonnet-5-5"
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("only fixture metadata GETs are accepted")
		}
		var body string
		switch auth := req.Header.Get("Authorization"); {
		case req.URL.String() == panelURL && auth == "Bearer "+bearer:
			body = website
		case req.URL.Host == publicKeyHost && req.URL.Path == "/api/v1/groups/rates" && auth == "Bearer "+bearer:
			body = accountRates
		case req.URL.Host == "api.beichenapi.com" && req.URL.Path == billingPath && auth == "Bearer "+key:
			body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
				"effective_rate_multiplier":0.60,"group_rate_multiplier":0.60,"peak_rate_enabled":false,
				"group_id":16,"api_key_id":41,"observed_at":%q}`, at.Format(time.RFC3339))
		case req.URL.Host == "api.beichenapi.com" && req.URL.Path == modelsPath && auth == "Bearer "+key:
			body = `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-sonnet-5-5"}]}`
		default:
			return nil, errors.New("unexpected fixture endpoint or credential")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	identity := listedKey{ID: 41, GroupID: 16, Group: &panelGroup{Platform: "anthropic"}}
	readQuote := func() panelQuote {
		t.Helper()
		directory, err := service.fetchBillingCatalog(ctx, syncJob{kind: "billing"}, bearer, key, identity)
		if err != nil {
			t.Fatal(err)
		}
		info, quote, err := directory.billing()
		if err != nil || directory.keyBilling == nil || quote.groupID != 16 ||
			info.ResolvedRate != "0.60" || info.Multiplier != "0.60" || !quote.verifiedKeyModels {
			t.Fatalf("expected verified Group 16 Key and 0.60 multiplier, got %+v (%v)", info, err)
		}
		return quote
	}
	quote := readQuote()
	if quote.err != nil || len(quote.terms) != 2 || len(quote.notListed) != 0 {
		t.Fatalf("shared website quote: %d terms, missing %v, error %v", len(quote.terms), quote.notListed, quote.err)
	}
	for _, want := range []struct {
		model, input, output string
	}{{opus, "0.000004", "0.00002"}, {sonnet, "0.000002", "0.00001"}} {
		var rates struct {
			Input       json.Number `json:"input_price"`
			Output      json.Number `json:"output_price"`
			CacheWrite1 json.Number `json:"cache_write_1h_price"`
		}
		if json.Unmarshal(quote.terms[want.model], &rates) != nil || rates.Input.String() != want.input ||
			rates.Output.String() != want.output || rates.CacheWrite1 == "" {
			t.Fatalf("incorrect website quote for %s: %s", want.model, quote.terms[want.model])
		}
	}
	website = strings.Replace(website, "claude-sonnet-5.5", "unrelated-sonnet", 1)
	quote = readQuote()
	if quote.err != nil || len(quote.terms) != 1 || !slices.Equal(quote.notListed, []string{sonnet}) {
		t.Fatalf("expected only Sonnet to lack a quote: %d terms, missing %v, error %v", len(quote.terms), quote.notListed, quote.err)
	}
	website = websiteGroupFixture(2, "0.15", `
		{"name":"claude-opus-5-5","pricing":{"input_price":0.000004,"output_price":0.00002}},
		{"name":"claude-sonnet-5.5","group_id":2,"pricing":{"input_price":0.000002,"output_price":0.00001}}`)
	// A still-current Key billing sample can confirm 0.60 when the account
	// endpoint omits this group.
	accountRates = `{"code":0,"data":{"2":0.15}}`
	quote = readQuote()
	if quote.err != nil || len(quote.rows) != 1 || quote.rows[0].Model != opus || *quote.rows[0].Input != 4_000_000 ||
		!slices.Equal(quote.notListed, []string{sonnet}) {
		t.Fatalf("only the shared flat price is eligible for a fixed row: %+v, missing %v", quote.rows, quote.notListed)
	}
	website = websiteGroupFixture(2, "0.15", `
		{"name":"claude-opus-5-5","pricing":{"input_price":0.000004,"output_price":0.00002}},
		{"name":"claude-opus-5-5","pricing":{"input_price":0.000005,"output_price":0.00002}}`)
	quote = readQuote()
	if !errors.Is(quote.err, errPanelEmpty) || len(quote.terms) != 0 || len(quote.rows) != 0 {
		t.Fatalf("conflicting shared quotes must leave the models unpriced: %d terms, missing %v, error %v",
			len(quote.terms), quote.notListed, quote.err)
	}
	website = string(readWebsitePricingFixture(t))
	identity.Group = nil
	quote = readQuote()
	if !errors.Is(quote.err, errPanelEmpty) || len(quote.terms) != 0 || !slices.Equal(quote.notListed, []string{opus, sonnet}) {
		t.Fatalf("an unknown Key platform must leave both models unpriced: terms=%d missing=%v error=%v",
			len(quote.terms), quote.notListed, quote.err)
	}
}
