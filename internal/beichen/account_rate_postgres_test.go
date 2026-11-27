package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	ratesStatus := http.StatusOK
	accountRates := `{"code":0,"data":{"6":0.08,"10":0.2}}`
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
				if ratesStatus != http.StatusOK {
					return &http.Response{StatusCode: ratesStatus, Body: io.NopCloser(strings.NewReader("fixture-response"))}, nil
				}
				body = accountRates
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
	checkDirectory := func(wantRate, wantSource string, wantKey int64, wantError string) {
		t.Helper()
		// Each crawl has a distinct observation time, as its stored history requires.
		at = at.Add(time.Millisecond)
		if _, err := service.CrawlPanelDirectory(ctx, provider); err != nil {
			t.Fatal(err)
		}
		var platforms []byte
		var rateError string
		err := db.QueryRowContext(ctx, `
SELECT d.platforms, COALESCE(r.account_rate_error,'')
FROM beichen_website_directories d JOIN beichen_crawl_runs r USING (provider_id)
WHERE d.provider_id=$1`, provider).Scan(&platforms, &rateError)
		if err != nil || rateError != wantError {
			t.Fatalf("unexpected directory rate error: %q (%v)", rateError, err)
		}
		var sections []directoryPlatform
		if err := json.Unmarshal(platforms, &sections); err != nil {
			t.Fatal(err)
		}
		catalog, err := selectWebsiteKeyCatalog(sections, groupID)
		group := catalog.group
		if err != nil || !matchesAccountRate(&group.WebsiteGroup, wantRate, wantSource, wantKey) {
			t.Fatalf("unexpected directory rate or evidence: %+v (%v)", group.WebsiteGroup, err)
		}
	}
	history := func() (int, string, string) {
		t.Helper()
		var count int
		var rate, source string
		err := db.QueryRowContext(ctx, `
SELECT count(*) OVER (), COALESCE(group_rules->>'account_rate_multiplier',''), COALESCE(group_rules->>'account_rate_source','')
FROM beichen_group_price_observations
WHERE provider_id=$1 AND group_id=$2 AND model=$3 AND source='website'
ORDER BY observed_at DESC LIMIT 1`, provider, groupID, model).Scan(&count, &rate, &source)
		if err != nil {
			t.Fatal(err)
		}
		return count, rate, source
	}

	priceAt := func(when *time.Time, wantPriced bool) {
		t.Helper()
		var hasPrice, matchesAmount bool
		err := db.QueryRowContext(ctx, `
SELECT upstream_input IS NOT NULL, COALESCE(upstream_input * 0.000001::numeric = 1.2,false)
FROM beichen_price_at($1,$2,COALESCE($3::timestamptz,statement_timestamp()))`, credential, model, when).
			Scan(&hasPrice, &matchesAmount)
		if err != nil || hasPrice != wantPriced || matchesAmount != wantPriced {
			t.Fatalf("wrong price for one million input tokens: has=%t amount_is_1.2=%t (%v)", hasPrice, matchesAmount, err)
		}
	}

	job := syncJob{provider: provider, credential: credential, kind: "billing"}
	checkBilling := func(wantSource string) {
		t.Helper()
		wantPriced := wantSource != ""
		wantState, wantCount, wantRate, wantKey := "current", int64(1), "0.6", int64(0)
		if wantSource == keyBillingRateSource {
			wantKey = keyID
		}
		if !wantPriced {
			wantState, wantCount, wantRate = "stale", 0, ""
		}
		busy, err := service.runJob(ctx, job, true)
		if busy || (wantPriced && err != nil) || (!wantPriced && !errors.Is(err, errWebsiteAccountRateUnverified)) {
			t.Fatalf("unexpected billing job result: busy=%t err=%v", busy, err)
		}
		view, err := service.Overview(ctx, provider, today)
		if err != nil {
			t.Fatal(err)
		}
		item := datasetForCredential(t, view, credential, "billing")
		if item.BillingState != wantState || item.GroupStatus != "assigned" || item.GroupID == nil || *item.GroupID != groupID ||
			item.GroupRate == nil || *item.GroupRate != "0.6" || item.EffectiveMultiplierText != "0.6" ||
			!matchesAccountRate(&WebsiteGroup{AccountRate: item.AccountRate, AccountRateSource: item.AccountRateSource},
				wantRate, wantSource, 0) ||
			(item.LastError == nil) != wantPriced || view.WebsiteSync.PricedChannels != wantCount {
			t.Fatalf("unexpected billing or coverage after rate check: %+v", item)
		}
		if !wantPriced {
			const want = "北辰官网账户倍率未核实，分组倍率仅供参考：官网账户倍率表读取失败（HTTP 503）"
			if *item.LastError != want {
				t.Fatalf("expected stored billing error %q, got %+v", want, item)
			}
		}
		index := slices.IndexFunc(view.KeyCapabilities, func(capability KeyCapability) bool {
			return capability.CredentialID == credential && capability.Label == "CC1"
		})
		if index < 0 {
			t.Fatal("CC1 is missing from KeyCapabilities")
		}
		capability := view.KeyCapabilities[index]
		if capability.GroupStatus != "assigned" || !matchesAccountRate(capability.WebsiteGroup, wantRate, wantSource, wantKey) ||
			capability.WebsiteGroup.ID != groupID || capability.WebsiteGroup.Rate == nil || *capability.WebsiteGroup.Rate != "0.6" {
			t.Fatalf("CC1 capability disagrees with current billing evidence: %+v", capability.WebsiteGroup)
		}
		priceAt(nil, wantPriced)
	}

	checkBilling(groupRateSource)
	if inventory, err := service.RefreshWebsiteKeys(ctx, provider); err != nil || len(inventory.Keys) != 1 ||
		inventory.Keys[0].MatchStatus != "matched" {
		t.Fatalf("could not verify the fixture's CC1 inventory: %v", err)
	}
	checkDirectory("0.6", groupRateSource, 0, "")
	checkBilling(groupRateSource)
	if err := service.RefreshKeyUsage(ctx, provider, credential, KeyUsageTarget{}); err != nil {
		t.Fatal(err)
	}
	checkDirectory("0.6", keyBillingRateSource, keyID, "")
	checkBilling(keyBillingRateSource)

	// One Key's rate cannot be published as the shared rate of another Key.
	singleKeyList := keyList
	keyList = strings.Replace(keyList, `}],`, `},{"id":12095,"user_id":13,"group_id":16,"name":"other","status":"active"}],`, 1)
	keyList = strings.Replace(keyList, `"total":1`, `"total":2`, 1)
	checkDirectory("0.6", groupRateSource, 0, "")
	checkBilling(keyBillingRateSource)
	keyList = singleKeyList

	// Even fresh, matching usage cannot supply a missing group_id.
	billingGroup = 0
	checkBilling(groupRateSource)
	billingGroup = groupID
	checkBilling(keyBillingRateSource)

	billingStatus = http.StatusNotFound
	checkBilling(groupRateSource)
	billingStatus = http.StatusOK
	sampleAge = panelQuoteTTL + time.Second
	checkBilling(groupRateSource)
	sampleAge = time.Second

	exec("UPDATE beichen_key_usage_days SET fetched_at=$2 WHERE credential_id=$1", credential, at.Add(-31*time.Minute))
	checkDirectory("0.6", groupRateSource, 0, "")
	checkBilling(groupRateSource)
	actual = "0.005999999999"
	if err := service.RefreshKeyUsage(ctx, provider, credential, KeyUsageTarget{}); err != nil {
		t.Fatal(err)
	}
	checkDirectory("0.6", groupRateSource, 0, "")
	checkBilling(groupRateSource)

	count, rate, source := history()
	if count != 3 || rate != "0.6" || source != groupRateSource {
		t.Fatalf("unexpected history before outage: %d, %s, %s", count, rate, source)
	}
	var priorPriceAt time.Time
	if err := db.QueryRowContext(ctx, "SELECT statement_timestamp()").Scan(&priorPriceAt); err != nil {
		t.Fatal(err)
	}
	// The last Key usage check can retain a rate read from the website.
	// A subsequent billing outage must suppress that previously displayed rate.
	accountRates = `{"code":0,"data":{"16":0.5}}`
	if err := service.RefreshKeyUsage(ctx, provider, credential, KeyUsageTarget{}); err != nil {
		t.Fatal(err)
	}
	ratesStatus = http.StatusServiceUnavailable
	checkDirectory("", "", 0, websiteAccountRatesReadFailure)
	if afterCount, afterRate, afterSource := history(); afterCount != count || afterRate != rate || afterSource != source {
		t.Fatalf("outage overwrote verified directory history: %d, %s, %s", afterCount, afterRate, afterSource)
	}
	checkBilling("")
	priceAt(&priorPriceAt, true)
}

func matchesAccountRate(group *WebsiteGroup, rate, source string, keyID int64) bool {
	return group != nil && group.AccountRateSource == source && group.AccountRateKeyID == keyID &&
		((rate == "" && group.AccountRate == nil) || (group.AccountRate != nil && group.AccountRate.String() == rate))
}

func TestBeichenAccountRateKeyIdentityPostgres(t *testing.T) {
	f := newAccountRateJobFixture(t)
	f.refreshUsage()
	f.syncBilling()
	item, card := f.readBilling()
	if item.AccountRateSource != keyBillingRateSource || !matchesAccountRate(card.WebsiteGroup, "0.6", keyBillingRateSource, 12094) {
		t.Fatalf("first billing round did not bind the rate to Key 12094: %+v %+v", item, card.WebsiteGroup)
	}
	f.at = f.at.Add(time.Millisecond)
	f.refreshUsage()
	_, card = f.readBilling()
	if !matchesAccountRate(card.WebsiteGroup, "0.6", keyBillingRateSource, 12094) {
		t.Fatal("an independent usage refresh for the same Key erased the verified rate")
	}

	var beforeChange time.Time
	if err := f.db.QueryRowContext(f.ctx, "SELECT statement_timestamp()").Scan(&beforeChange); err != nil {
		t.Fatal(err)
	}
	f.keyID = 12095
	f.at = f.at.Add(time.Millisecond)
	f.refreshUsage()
	item, card = f.readBilling()
	if item.BillingState != "stale" || item.AccountRate != nil || item.AccountRateSource != "" ||
		card.WebsiteKeyID == nil || *card.WebsiteKeyID != 12095 ||
		!matchesAccountRate(card.WebsiteGroup, "", "", 0) || card.StoredMultiplier != nil {
		t.Fatalf("new Key was associated with prior personal billing: %+v %+v", item, card)
	}
	var currentQuote, historicalQuote bool
	err := f.db.QueryRowContext(f.ctx, `
SELECT (SELECT upstream_input IS NOT NULL FROM beichen_price_at($1,$2,statement_timestamp())),
       (SELECT upstream_input=1200000 FROM beichen_price_at($1,$2,$3))`,
		f.job.credential, accountRateFixtureModel, beforeChange).Scan(&currentQuote, &historicalQuote)
	if err != nil || currentQuote || !historicalQuote {
		t.Fatalf("Key change should end the current quote and preserve history: current=%t historical=%t (%v)", currentQuote, historicalQuote, err)
	}
	f.syncBilling()
	item, card = f.readBilling()
	if item.BillingState != "current" || !matchesAccountRate(card.WebsiteGroup, "0.6", keyBillingRateSource, 12095) {
		t.Fatalf("a separately verified round did not establish the new Key's rate: %+v %+v", item, card)
	}

	f.keyID = 12096
	f.at = f.at.Add(time.Millisecond)
	if _, err := f.service.RefreshWebsiteKeys(f.ctx, f.job.provider); err != nil {
		t.Fatal(err)
	}
	item, card = f.readBilling()
	if item.BillingState != "stale" || item.AccountRate != nil || !matchesAccountRate(card.WebsiteGroup, "", "", 0) {
		t.Fatalf("the key inventory change should also retire the personal rate: %+v %+v", item, card)
	}
}

func TestBeichenDirectAccountRateFreshnessPostgres(t *testing.T) {
	f := newAccountRateJobFixture(t)
	zone := "UTC"
	if f.at.Hour() >= 22 {
		zone = "Asia/Shanghai"
	}
	f.quote = strings.Replace(f.quote, `"peak_rate_enabled":false`,
		fmt.Sprintf(`"peak_rate_enabled":true,"peak_start":"00:00","peak_end":"23:59","peak_rate_multiplier":2,"timezone":%q`, zone), 1)
	f.rates = `{"16":0.5}`
	f.refreshUsage()
	_, card := f.readBilling()
	if !matchesAccountRate(card.WebsiteGroup, "0.5", "", 0) {
		t.Fatalf("fixture should start with a Key usage snapshot of 0.5: %+v", card.WebsiteGroup)
	}
	f.rates = `{"16":0.7}`
	f.syncBilling()
	item, card := f.readBilling()
	if item.BillingState != "current" || item.EffectiveMultiplierText != "1.4" ||
		!matchesAccountRate(card.WebsiteGroup, "0.7", "", 0) {
		t.Fatalf("expected the new pre-peak rate and separate effective rate: %+v %+v", item, card.WebsiteGroup)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"account_rate_`) {
		t.Fatalf("direct website rates should remain internal to the Dataset JSON: %s", raw)
	}

	// The Key-only billing route can succeed using the Key's own multiplier.
	// Its unlabeled multiplier must not be presented as an official table rate.
	f.rates = `{}`
	f.quote = websiteGroupFixture(17, "0.6", accountRateFixtureTerms)
	f.syncBilling()
	item, card = f.readBilling()
	if item.BillingState != "current" || item.EffectiveMultiplierText != "0.6" ||
		!matchesAccountRate(card.WebsiteGroup, "", "", 0) {
		t.Fatalf("the Key-only multiplier was confused with an account-table rate: %+v %+v", item, card.WebsiteGroup)
	}
}

func TestBeichenPersonalQuoteAfterGroupMovePostgres(t *testing.T) {
	checkPersonalQuoteAfterBindingChange(t, 17, "assigned")
}

func TestBeichenPersonalQuoteAfterGroupRemovedPostgres(t *testing.T) {
	checkPersonalQuoteAfterBindingChange(t, 0, "not_found")
}

func checkPersonalQuoteAfterBindingChange(t *testing.T, newGroup int, groupStatus string) {
	t.Helper()
	for _, source := range []string{"key_usage", "inventory"} {
		t.Run(source, func(t *testing.T) {
			f := newAccountRateJobFixture(t)
			f.refreshUsage()
			f.syncBilling()
			item, card := f.readBilling()
			if item.BillingState != "current" || !matchesAccountRate(card.WebsiteGroup, "0.6", keyBillingRateSource, 12094) {
				t.Fatal("fixture must begin with a payable personal quote for group 16")
			}
			var revision, priorPriceAt time.Time
			if err := f.db.QueryRowContext(f.ctx, "SELECT updated_at,statement_timestamp() FROM provider_credentials WHERE id=$1",
				f.job.credential).Scan(&revision, &priorPriceAt); err != nil {
				t.Fatal(err)
			}
			f.checkPersonalQuote(priorPriceAt, true)
			lease := uuid.New()
			setLease := func() {
				t.Helper()
				execCatalogTestSQL(t, f.ctx, f.db, `
UPDATE beichen_datasets SET lease_id=$2,lease_until=clock_timestamp()+interval '90 seconds'
WHERE credential_id=$1 AND kind='billing'`, f.job.credential, lease)
			}
			setLease()
			f.at = f.at.Add(time.Millisecond)
			oldFetch := f.service.fetchBillingJob(f.ctx, f.job, revision, accountRateFixtureSecret)
			if oldFetch.err != nil || oldFetch.accountRate == nil || oldFetch.accountRate.Source != keyBillingRateSource ||
				oldFetch.accountRate.KeyID != 12094 || oldFetch.accountRate.GroupID != 16 {
				t.Fatalf("expected the in-flight fetch to have verified the old binding: %+v (%v)", oldFetch.accountRate, oldFetch.err)
			}

			usageJob := syncJob{provider: f.job.provider, credential: f.job.credential, kind: "key_usage"}
			f.at = f.at.Add(time.Millisecond)
			f.keyListUnavailable = true
			if busy, err := f.service.runJob(f.ctx, usageJob, true); busy || err == nil {
				t.Fatalf("fixture should fail to read the Key list: busy=%t err=%v", busy, err)
			}
			if item, _ := f.readBilling(); item.BillingState != "current" || item.AccountRateSource != keyBillingRateSource {
				t.Fatal("an unconfirmed network error retired the personal quote")
			}
			f.keyListUnavailable = false
			f.at = f.at.Add(time.Millisecond)
			f.groupID = newGroup
			if newGroup > 0 {
				f.quote = websiteGroupFixture(newGroup, "0.6", accountRateFixtureTerms)
			}
			if source == "inventory" {
				if _, err := f.service.RefreshWebsiteKeys(f.ctx, f.job.provider); err != nil {
					t.Fatal(err)
				}
			} else if busy, err := f.service.runJob(f.ctx, usageJob, true); busy ||
				(newGroup == 0 && !errors.Is(err, errKeyGroupMissing)) || (newGroup != 0 && err != nil) {
				t.Fatalf("fixture failed to confirm the new binding: busy=%t err=%v", busy, err)
			}
			item, card = f.readBilling()
			if item.BillingState != "stale" || item.GroupStatus != groupStatus || item.AccountRate != nil ||
				(card.StoredMultiplier != nil && source == "key_usage") ||
				(newGroup == 0 && card.WebsiteGroup != nil) ||
				(newGroup != 0 && (!matchesAccountRate(card.WebsiteGroup, "", "", 0) || card.WebsiteGroup.ID != int64(newGroup))) {
				t.Fatalf("confirmed group change did not retire old account evidence: %+v %+v", item, card)
			}
			f.checkPersonalQuote(priorPriceAt, false)

			// An omitted group field is pending and cannot overwrite the
			// preceding confirmed binding used to reject an older fetch.
			f.at = f.at.Add(time.Millisecond)
			f.groupIDKnown = false
			if busy, err := f.service.runJob(f.ctx, usageJob, true); busy || !errors.Is(err, errPanelGroupUnverified) {
				t.Fatalf("fixture should leave the group pending: busy=%t err=%v", busy, err)
			}
			if err := f.service.finishJob(f.ctx, f.job, lease, credentialRecord{updatedAt: revision}, oldFetch, true); err == nil {
				t.Fatal("the superseded billing lease was still usable")
			}
			// Exercise the binding check independently of lease cancellation,
			// as in a first billing round without an existing quote to retire.
			setLease()
			err := f.service.finishJob(f.ctx, f.job, lease, credentialRecord{updatedAt: revision}, oldFetch, true)
			if !errors.Is(err, errBillingKeyBindingChanged) {
				t.Fatalf("the old-group personal round must fail before publishing: %v", err)
			}
			f.checkPersonalQuote(priorPriceAt, false)
		})
	}
}

func TestBeichenAgedDirectKeySnapshotPostgres(t *testing.T) {
	f := newAccountRateJobFixture(t)
	f.rates = `{"16":0.5}`
	f.refreshUsage()
	originalAt := f.at
	f.at = originalAt.Add(keyUsageFreshness)
	item, card := f.readBilling()
	if item.BillingState != "unverified" || !matchesAccountRate(card.WebsiteGroup, "0.5", "", 0) {
		t.Fatal("a verified Key snapshot should be usable up to the 30-minute boundary")
	}
	assertNoRate := func() {
		t.Helper()
		_, card := f.readBilling()
		if card.GroupStatus != "assigned" || !matchesAccountRate(card.WebsiteGroup, "", "", 0) ||
			card.WebsiteGroup.ID != 16 || card.WebsiteGroup.Rate == nil || *card.WebsiteGroup.Rate != "0.6" {
			t.Fatalf("only the unverified account rate should be hidden: %+v", card)
		}
	}
	f.at = originalAt.Add(31 * time.Minute)
	assertNoRate()
	f.at = originalAt.Add(-time.Second)
	assertNoRate()
	f.at = originalAt
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE beichen_key_observations SET models_fetched_at=NULL WHERE credential_id=$1`,
		f.job.credential)
	assertNoRate()

	f.rates = `{"16":0.7}`
	f.syncBilling()
	item, card = f.readBilling()
	if item.BillingState != "current" || !matchesAccountRate(card.WebsiteGroup, "0.7", "", 0) {
		t.Fatal("the authoritative billing rate must remain available without a trusted Key usage timestamp")
	}
}

const (
	accountRateFixtureModel  = "claude-sonnet-5-5"
	accountRateFixtureSecret = "sk-fixture-rate-identity"
	accountRateFixtureTerms  = `{"name":"claude-sonnet-5-5","pricing":{
		"billing_mode":"token","input_price":0.000002,"output_price":0.00001,"cache_read_price":0,"cache_write_price":0}}`
)

type accountRateJobFixture struct {
	t                  *testing.T
	ctx                context.Context
	db                 *sql.DB
	service            *Service
	job                syncJob
	at                 time.Time
	keyID              int
	groupID            int
	groupIDKnown       bool
	keyListUnavailable bool
	quote              string
	rates              string
}

func newAccountRateJobFixture(t *testing.T) *accountRateJobFixture {
	t.Helper()
	ctx, db, provider, _, credentials := newActionsPostgres(t)
	const bearer = "fixture-rate-account-session"
	credential := credentials[3]
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential(accountRateFixtureSecret)
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", credential, ciphertext)
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET status='inactive' WHERE id<>$1", credential)
	service := New(db, manager)
	service.ConfigurePanelFallback(bearer)
	f := &accountRateJobFixture{t: t, ctx: ctx, db: db, service: service,
		job: syncJob{provider: provider, credential: credential, kind: "billing"},
		at:  time.Now().UTC().Truncate(time.Microsecond), keyID: 12094, groupID: 16, groupIDKnown: true, rates: `{}`,
		quote: websiteGroupFixture(16, "0.6", accountRateFixtureTerms)}
	service.now = func() time.Time { return f.at }
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch auth := req.Header.Get("Authorization"); {
		case req.URL.String() == panelURL && auth == "Bearer "+bearer:
			body = f.quote
		case req.URL.Host == publicKeyHost && auth == "Bearer "+bearer:
			switch req.URL.Path {
			case "/api/v1/keys":
				if f.keyListUnavailable {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("fixture unavailable"))}, nil
				}
				body = websiteKeyListFixture(accountRateFixtureSecret, "CC1", f.keyID, f.groupID)
				if !f.groupIDKnown {
					body = strings.Replace(body, fmt.Sprintf(`"group_id":%d,`, f.groupID), "", 1)
				}
			case "/api/v1/groups/rates":
				body = `{"code":0,"data":` + f.rates + `}`
			case "/api/v1/usage":
				query := req.URL.Query()
				if query.Get("api_key_id") != fmt.Sprint(f.keyID) || query.Get("start_date") != query.Get("end_date") {
					return nil, errors.New("wrong fixture usage query")
				}
				body = emptyWebsiteUsageFixture
				if query.Get("start_date") == f.at.In(beijing).Format(dayLayout) {
					body = fmt.Sprintf(`{"code":0,"data":{"items":[{"id":1,"user_id":13,"api_key_id":%d,"group_id":%d,
						"model":%q,"created_at":%q,"total_cost":0.01,"actual_cost":0.006,"currency":"USD"}],
						"total":1,"page":1,"pages":1,"page_size":100}}`,
						f.keyID, f.groupID, accountRateFixtureModel, f.at.Format(time.RFC3339Nano))
				}
			}
		case req.URL.Host == "api.beichenapi.com" && auth == "Bearer "+accountRateFixtureSecret:
			switch req.URL.Path {
			case modelsPath:
				body = fmt.Sprintf(`{"data":[{"id":%q,"supported_protocols":["anthropic"]}]}`, accountRateFixtureModel)
			case billingPath:
				body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
					"api_key_id":%d,"group_id":%d,"group_rate_multiplier":0.6,"effective_rate_multiplier":0.6,
					"peak_rate_enabled":false,"observed_at":%q}`, f.keyID, f.groupID, f.at.Add(-time.Second).Format(time.RFC3339Nano))
			}
		}
		if body == "" || req.Method != http.MethodGet {
			return nil, errors.New("unexpected fixture endpoint")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return f
}

func (f *accountRateJobFixture) checkPersonalQuote(historicalAt time.Time, wantCurrent bool) {
	f.t.Helper()
	var current, historical bool
	err := f.db.QueryRowContext(f.ctx, `
SELECT (SELECT upstream_input IS NOT NULL FROM beichen_price_at($1,$2,statement_timestamp())) IS TRUE,
       (SELECT upstream_input=1200000 FROM beichen_price_at($1,$2,$3)) IS TRUE`,
		f.job.credential, accountRateFixtureModel, historicalAt).Scan(&current, &historical)
	if err != nil || current != wantCurrent || !historical {
		f.t.Fatalf("unexpected quote validity: current=%t historical=%t (%v)", current, historical, err)
	}
}

func (f *accountRateJobFixture) refreshUsage() {
	f.t.Helper()
	if err := f.service.RefreshKeyUsage(f.ctx, f.job.provider, f.job.credential, KeyUsageTarget{}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *accountRateJobFixture) syncBilling() {
	f.t.Helper()
	if busy, err := f.service.runJob(f.ctx, f.job, true); busy || err != nil {
		f.t.Fatalf("fixture billing sync: busy=%t err=%v", busy, err)
	}
}

func (f *accountRateJobFixture) readBilling() (Dataset, KeyCapability) {
	f.t.Helper()
	view, err := f.service.Overview(f.ctx, f.job.provider, f.at.In(beijing).Format(dayLayout))
	if err != nil {
		f.t.Fatal(err)
	}
	index := slices.IndexFunc(view.KeyCapabilities, func(key KeyCapability) bool { return key.CredentialID == f.job.credential })
	if index < 0 {
		f.t.Fatal("missing fixture Key from KeyCapabilities")
	}
	return datasetForCredential(f.t, view, f.job.credential, "billing"), view.KeyCapabilities[index]
}
