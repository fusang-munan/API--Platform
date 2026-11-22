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
	const rates = `{"code":0,"data":{"6":0.08,"10":0.20,"16":0.60}}`
	const missingGroupRate = `{"code":0,"data":{"6":0.08}}`
	const keyRate = `"group_id":10,"api_key_id":41,"group_rate_multiplier":0.24,"effective_rate_multiplier":0.20,"peak_rate_enabled":false`
	const keyPeakRate = `"group_id":10,"api_key_id":41,"group_rate_multiplier":0.24,"resolved_rate_multiplier":0.20,
		"effective_rate_multiplier":0.4,"peak_rate_enabled":true,"peak_start":"12:00","peak_end":"12:20",
		"peak_rate_multiplier":2,"applied_peak_multiplier":2,"timezone":"UTC"`
	for _, test := range []struct {
		name           string
		group          int
		groupRate      json.Number
		resolvedRate   json.Number
		ratesBody      string
		ratesStatus    int
		peak           bool
		keyBilling     string
		wantMultiplier json.Number
		wantErr        error
	}{
		{"GPT", 10, "0.24", "0.20", rates, http.StatusOK, false, "", "0.20", nil},
		{"domestic", 6, "0.10", "0.08", rates, http.StatusOK, false, "", "0.08", nil},
		{"CC", 16, "0.60", "0.60", rates, http.StatusOK, false, "", "0.60", nil},
		{"peak uses account rate", 10, "0.24", "0.20", rates, http.StatusOK, true, "", "0.4", nil},
		{"missing group uses same Key billing", 10, "0.24", "0.20", missingGroupRate, http.StatusOK, false, keyRate, "0.20", nil},
		{"account rates outage uses same Key billing", 10, "0.24", "0.20", "", http.StatusServiceUnavailable, false, keyRate, "0.20", nil},
		{"Key peak multiplier already applied", 10, "0.24", "0.20", missingGroupRate, http.StatusOK, true, keyPeakRate, "0.4", nil},
		{"Key billing group mismatch", 10, "0.24", "0.24", missingGroupRate, http.StatusOK, false,
			strings.Replace(keyRate, `"group_id":10`, `"group_id":11`, 1), "0.24", errWebsiteAccountRateUnverified},
		{"Key billing ID mismatch", 10, "0.24", "0.24", missingGroupRate, http.StatusOK, false,
			strings.Replace(keyRate, `"api_key_id":41`, `"api_key_id":42`, 1), "0.24", errWebsiteAccountRateUnverified},
		{"Key billing default rate mismatch", 10, "0.24", "0.24", missingGroupRate, http.StatusOK, false,
			strings.Replace(keyRate, `"group_rate_multiplier":0.24`, `"group_rate_multiplier":0.25`, 1),
			"0.24", errWebsiteAccountRateUnverified},
		{"Key billing effective rate mismatch", 10, "0.24", "0.24", missingGroupRate, http.StatusOK, false,
			keyRate + `,"resolved_rate_multiplier":0.21`, "0.24", errWebsiteAccountRateUnverified},
		{"neither endpoint available", 10, "0.24", "0.24", "", http.StatusServiceUnavailable, false, "",
			"0.24", errWebsiteAccountRateUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			provider := uuid.New()
			mock.ExpectQuery("SELECT EXISTS").WithArgs(provider, test.group).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			service := New(db, nil)
			at := time.Date(2026, 11, 22, 12, 15, 0, 0, time.UTC)
			service.now = func() time.Time { return at }
			groupBody := websiteGroupFixture(test.group, test.groupRate.String(), model)
			if test.peak {
				groupBody = strings.Replace(groupBody, `"peak_rate_enabled":false`, `"peak_rate_enabled":true,
					"peak_start":"12:00","peak_end":"12:20","peak_rate_multiplier":2,"timezone":"UTC"`, 1)
			}
			keyAt := at.Add(-time.Second)
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
						return reply(test.ratesStatus, test.ratesBody)
					}
				}
				if req.Method == http.MethodGet && req.URL.Host == "api.beichenapi.com" && req.URL.Path == billingPath && auth == "Bearer "+key {
					keyReads++
					if test.keyBilling == "" {
						return reply(http.StatusServiceUnavailable, "")
					}
					return reply(http.StatusOK, fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
						"observed_at":%q,%s}`, keyAt.Format(time.RFC3339), test.keyBilling))
				}
				t.Errorf("unexpected fixture endpoint or authorization for %s", req.URL.Path)
				return nil, errors.New("unexpected fixture request")
			})
			directory, err := service.fetchBillingCatalog(context.Background(), provider, bearer, key, listedKey{ID: 41, GroupID: int64(test.group)})
			if err != nil {
				t.Fatal(err)
			}
			info, quote, err := directory.billing()
			if !errors.Is(err, test.wantErr) || quote.err != nil || len(quote.terms) != 1 ||
				info.GroupRate != test.groupRate || info.ResolvedRate != test.resolvedRate || info.Multiplier != test.wantMultiplier {
				t.Fatalf("billing group=%s resolved=%s effective=%s, quote error=%v, billing error=%v",
					info.GroupRate, info.ResolvedRate, info.Multiplier, quote.err, err)
			}
			hasAccountRate := test.ratesBody == rates
			wantKeyReads := 0
			if !hasAccountRate {
				wantKeyReads = 1
			}
			if rateReads != 1 || keyReads != wantKeyReads || (directory.group.AccountRate != nil) != hasAccountRate ||
				(hasAccountRate && (info.UserRate == nil || *info.UserRate != test.resolvedRate)) {
				t.Fatalf("unexpected rate sources: website reads=%d Key reads=%d account rate=%v", rateReads, keyReads, info.UserRate)
			}
			if !hasAccountRate && test.wantErr == nil && (!info.ObservedAt.Equal(keyAt) ||
				!slices.Equal(directory.catalogQuote().websiteModels, []string{"fixture"})) {
				t.Fatalf("Key rate confirmation should keep website model evidence and use the Key sample time: %+v", info)
			}
			if test.peak && !quote.expiresAt.Equal(at.Add(5*time.Minute)) {
				t.Fatalf("quote should expire at the end of the peak window, got %v", quote.expiresAt)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
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
		directory, err := service.fetchBillingCatalog(ctx, uuid.Nil, bearer, key, identity)
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
