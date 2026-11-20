package beichen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

func TestPanelRates(t *testing.T) {
	for _, tc := range []struct {
		name, pricing string
		want          bool
	}{
		{"flat", `"input_price":1.234567e-6,"output_price":0,"cache_read_price":0.000000000001,"intervals":[]`, true},
		{"overflow", `"input_price":1.000000000001,"output_price":1,"intervals":[]`, false},
		{"fractional micro rate", `"input_price":1e-13,"output_price":1,"intervals":[]`, false},
		{"tiered", `"input_price":0,"output_price":1,"intervals":[{"min_tokens":1000}]`, false},
		{"image", `"input_price":0,"output_price":1,"image_output_price":0,"intervals":[]`, false},
		{"per request", `"input_price":0,"output_price":1,"per_request_price":1,"intervals":[]`, false},
		{"cache tiers", `"input_price":0,"output_price":1,"cache_write_1h_price":0,"intervals":[]`, false},
		{"reasoning tiers", `"input_price":0,"output_price":1,"reasoning_effort_multipliers":{"low":0},"intervals":[]`, false},
		{"missing input", `"output_price":0,"intervals":[]`, false},
		{"missing intervals", `"input_price":1.234567e-6,"output_price":0,"cache_read_price":1e-12`, true},
		{"null intervals", `"input_price":1.234567e-6,"output_price":0,"cache_read_price":1e-12,"intervals":null,"reasoning_effort_multipliers":{}`, true},
		{"neutral reasoning", `"input_price":1.234567e-6,"output_price":0,"cache_read_price":1e-12,"reasoning_effort_multipliers":{"low":1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rates, ok := flatPanelRates(json.RawMessage(`{"billing_mode":"token",` + tc.pricing + `}`))
			if ok != tc.want {
				t.Fatalf("flatPanelRates = %v, want %v", ok, tc.want)
			}
			if ok && (*rates.Input != 1234567 || *rates.Output != 0 || *rates.CacheRead != 1 || rates.CacheWrite != nil) {
				t.Fatalf("unexpected converted rates: %+v", rates)
			}
		})
	}
}

const panelFirst = `{"platforms":[{"platform":"openai","groups":[{"id":1,"platform":"openai","subscription_type":"standard","rate_multiplier":1,"peak_rate_enabled":false}],"supported_models":[{"name":"shared","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0,"intervals":[]}},{"name":"unlisted","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0,"intervals":[]}}]}]}`
const panelSecond = `{"name":"example","description":"","platforms":[{"platform":"openai","groups":[{"id":2,"name":"example group","platform":"openai","subscription_type":"standard","rate_multiplier":1,"peak_rate_enabled":false,"peak_start":"","peak_end":"","peak_rate_multiplier":0,"is_exclusive":false}],"supported_models":[{"name":"shared","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0,"cache_read_price":null,"cache_write_price":null,"cache_write_1h_price":null,"cache_write_12h_price":null,"image_input_price":null,"image_output_price":null,"per_request_price":null,"intervals":[]}}]}]}`

func panelPayload(channels ...string) string {
	return `{"code":0,"data":[` + strings.Join(channels, ",") + `]}`
}

func TestPanelGroupIsolation(t *testing.T) {
	peak := false
	billing := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	entries := []catalog.ProviderEntry{{ID: "shared"}}
	for group := int64(1); group <= 2; group++ {
		rows, _, err := parsePanelPrices([]byte(panelPayload(panelFirst, panelSecond)), group, billing, entries)
		if err != nil || len(rows) != 1 || rows[0].Model != "shared" || *rows[0].Rates.Input != group*1_000_000 {
			t.Fatalf("group %d: rows %+v, err %v", group, rows, err)
		}
	}
	for _, body := range []string{panelPayload(panelSecond), panelPayload(panelFirst, panelFirst), panelPayload()} {
		if _, _, err := parsePanelPrices([]byte(body), 1, billing, entries); err == nil {
			t.Fatal("accepted missing or ambiguous group")
		}
	}
}

func TestPanelConfirmsModelsIndependently(t *testing.T) {
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	models := []catalog.ProviderEntry{{ID: "shared"}, {ID: "unlisted"}, {ID: "no-price"}}
	const flat = `"input_price":0.000001,"output_price":0,"intervals":[]`
	for _, row := range []string{
		`{"name":"shared","platform":"other","pricing":{` + flat + `}}`,
		`{"name":"shared","platform":"openai","pricing":{"billing_mode":"per_request","per_request_price":1}}`,
		`{"name":"shared","platform":"openai","pricing":null}`,
	} {
		start := strings.Index(panelFirst, `{"name":"shared"`)
		end := strings.Index(panelFirst, `},{"name":"unlisted"`) + 1
		body := panelPayload(panelFirst[:start]+row+panelFirst[end:], panelSecond)
		prices, _, err := parsePanelPrices([]byte(body), 1, info, models)
		if err != nil || len(prices) != 1 || prices[0].Model != "unlisted" {
			t.Fatalf("expected just the verified price from group 1, got %+v, %v", prices, err)
		}
	}
	rates, err := panelBillingRates(json.RawMessage(`{"basis":"base","unit":"beichen_micros_per_million_tokens","models":[
		{"model":"verified","input":0,"output":1},{"model":"tiered","input":1,"output":1,"intervals":[{}]},
		{"model":"duplicate","input":1,"output":1},{"model":"duplicate","input":1,"output":1,"intervals":[{}]}]}`))
	if err != nil || len(rates) != 1 || rates["verified"].Input == nil || *rates["verified"].Input != 0 {
		t.Fatalf("billing extension must corroborate only the unambiguous model: %+v, %v", rates, err)
	}
}

func TestPanelRetainsVisibleConditionalPricing(t *testing.T) {
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	for _, tc := range []struct {
		pricing     string
		conditional bool
	}{
		{`{"billing_mode":"token","input_price":0.000001,"cache_read_price":0}`, false},
		{`{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,"cache_write_1h_price":0,"cache_write_12h_price":0.000004}`, true},
		{`{"billing_mode":"token","intervals":[{"min_tokens":200000,"max_tokens":null,"input_price":0.000008}]}`, true},
		{`{"billing_mode":"per_request","per_request_price":0.3}`, true},
		{`{"billing_mode":"future","unknown_fee":0.4}`, false},
	} {
		const original = `{"billing_mode":"token","input_price":0.000001,"output_price":0,"intervals":[]}`
		body := panelPayload(strings.Replace(panelFirst, original, tc.pricing, 1), panelSecond)
		rows, terms, err := parsePanelPrices([]byte(body), 1, info, []catalog.ProviderEntry{{ID: "shared"}, {ID: "missing"}})
		if err != nil || len(rows) != 0 || len(terms) != 2 || string(terms["missing"]) != "null" || string(terms["shared"]) != tc.pricing ||
			conditionalPanelPricing(terms["shared"]) != tc.conditional {
			t.Fatalf("expected original per-key terms and no flat ledger price: %s %v", terms["shared"], err)
		}
	}
}

func TestPanelRechecksMissingModelInEachCatalog(t *testing.T) {
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	entries := []catalog.ProviderEntry{{ID: "new-model"}, {ID: "shared"}}
	first := panelPayload(panelFirst)
	_, terms, err := parsePanelPrices([]byte(first), 1, info, entries)
	if err != nil || string(terms["new-model"]) != "null" || !panelQuoteUsable(panelQuote{terms: terms}) {
		t.Fatalf("expected an explicitly unlisted model alongside a real price: %v", err)
	}
	second := strings.Replace(first, `"name":"unlisted"`, `"name":"new-model"`, 1)
	rows, terms, err := parsePanelPrices([]byte(second), 1, info, entries)
	if err != nil || len(rows) != 2 || string(terms["new-model"]) == "null" || len(terms["new-model"]) == 0 {
		t.Fatalf("newly listed model did not acquire a price on the next pass: %v", err)
	}
	_, terms, err = parsePanelPrices([]byte(first), 1, info, entries[:1])
	if err != nil || string(terms["new-model"]) != "null" || panelQuoteUsable(panelQuote{terms: terms}) {
		t.Fatal("an absent name must never count as a usable price")
	}
	end := strings.Index(panelFirst, `"supported_models":[`)
	empty := panelPayload(panelFirst[:end] + `"supported_models":[]}]}`)
	if _, _, err := parsePanelPrices([]byte(empty), 1, info, entries); err == nil {
		t.Fatal("an empty upstream table must not mark catalog names as missing")
	}
	incomplete := strings.Replace(first, `"name":"unlisted"`, `"name":""`, 1)
	_, terms, err = parsePanelPrices([]byte(incomplete), 1, info, entries)
	if _, marked := terms["new-model"]; err != nil || marked || len(terms["shared"]) == 0 {
		t.Fatal("an unnamed row must not hide confirmed prices or prove another name is absent")
	}
	wrongPlatform := strings.Replace(first, `"name":"unlisted","platform":"openai"`, `"name":"unlisted","platform":"other"`, 1)
	_, terms, err = parsePanelPrices([]byte(wrongPlatform), 1, info, entries)
	if _, marked := terms["new-model"]; err != nil || marked || len(terms["shared"]) == 0 {
		t.Fatal("a misplaced row must not prove absence or hide a correctly attributed price")
	}
	onlyWrong := strings.Replace(wrongPlatform, `"name":"shared","platform":"openai"`, `"name":"shared","platform":""`, 1)
	_, terms, err = parsePanelPrices([]byte(onlyWrong), 1, info, entries)
	if err != nil || len(terms) != 0 {
		t.Fatal("a list entirely outside the expected platform does not establish any match")
	}
}

func TestPanelPricesRequireStandardBalanceGroup(t *testing.T) {
	peak := false
	info := billingInfo{PeakEnabled: &peak, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	models := []catalog.ProviderEntry{{ID: "shared"}}
	const standard = `"subscription_type":"standard",`
	for _, tc := range []struct {
		name, field string
	}{
		{"subscription", `"subscription_type":"subscription",`},
		{"omitted", ""},
		{"null", `"subscription_type":null,`},
		{"empty", `"subscription_type":"",`},
		{"unknown", `"subscription_type":"future",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped := strings.Replace(panelSecond, standard, tc.field, 1)
			rows, _, err := parsePanelPrices([]byte(panelPayload(panelFirst, mapped)), 2, info, models)
			if err == nil || !strings.Contains(err.Error(), "标准余额组") || len(rows) != 0 {
				t.Fatalf("mapped nonstandard group must fail without using a standard group with the same model: %v", err)
			}
		})
	}
	rows, _, err := parsePanelPrices([]byte(panelPayload(panelFirst, panelSecond)), 2, info, models)
	if err != nil || len(rows) != 1 || *rows[0].Rates.Input != 2_000_000 {
		t.Fatalf("standard v0.2.15 group: rows %v, error %v", rows, err)
	}
}

type panelTransport func(*http.Request) (*http.Response, error)

func (f panelTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestChannelQuoteUsesPublicAccessBeforeLogin(t *testing.T) {
	now := time.Now().UTC()
	peak := false
	info := billingInfo{PeakEnabled: &peak, ObservedAt: now, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
	job := syncJob{provider: uuid.New(), credential: uuid.New()}
	for _, tc := range []struct {
		name                          string
		panelStatus, keyStatus, calls int
		needsLogin, priced            bool
	}{
		{"public", 200, 200, 2, false, true},
		{"login required", 401, 200, 4, true, true},
		{"forbidden for anonymous", 403, 200, 3, true, true},
		{"site unavailable", 503, 200, 1, false, false},
		{"bad inference key", 200, 401, 2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := New(db, nil)
			s.now = func() time.Time { return now }
			// The resolver also supports explicitly configured existing authorizations.
			s.panelGroups = map[uuid.UUID]int64{job.credential: 2}
			if tc.panelStatus == http.StatusForbidden {
				s.panelToken = "synthetic-web-session"
			} else if err := s.ConfigureWebSession("school@example.test", "synthetic-password"); err != nil {
				t.Fatal(err)
			}
			if tc.needsLogin {
				mock.ExpectQuery("SELECT group_id,token_ciphertext").WithArgs(job.credential).
					WillReturnRows(sqlmock.NewRows([]string{"group_id", "token_ciphertext"}))
			}
			calls := 0
			s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status, body := http.StatusOK, ""
				auth := r.Header.Get("Authorization")
				switch r.URL.String() {
				case webAuthURL + "login":
					if calls != 2 || tc.panelStatus != http.StatusUnauthorized || r.Method != http.MethodPost || auth != "" {
						t.Fatal("unexpected website sign-in")
					}
					body = `{"code":0,"data":{"token_type":"Bearer","access_token":"synthetic-web-session","expires_in":900}}`
				case panelURL:
					if calls == 1 {
						if auth != "" {
							t.Fatal("public price check sent an authorization")
						}
						status = tc.panelStatus
					} else if !tc.needsLogin || auth != "Bearer synthetic-web-session" {
						t.Fatal("unexpected authenticated panel access")
					}
					body = panelPayload(panelSecond)
				case origin + modelsPath:
					if auth != "Bearer synthetic-inference" {
						t.Fatal("catalog used the wrong credential")
					}
					status, body = tc.keyStatus, `{"data":[{"id":"shared"}]}`
				default:
					t.Fatal("unexpected URL")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			quote := s.fetchChannelQuote(context.Background(), job, "synthetic-inference", 2, info)
			if (quote.err == nil) != tc.priced || (len(quote.rows) == 1) != tc.priced || calls != tc.calls {
				t.Fatalf("price %+v, error %v, requests %d", quote.rows, quote.err, calls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPanelFetchSeparatesCredentials(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	group := int64(2)
	info, err := parseBilling([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":1,"resolved_rate_multiplier":0,"user_rate_multiplier":0,"effective_rate_multiplier":0,"peak_rate_enabled":false,"observed_at":"2026-11-18T10:00:00Z"}`))
	if err != nil || info.GroupID != nil {
		t.Fatal("expected a valid standard billing response without group_id")
	}
	s := &Service{now: func() time.Time { return now }, client: newClient()}
	calls := 0
	s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var expected, body string
		switch req.URL.String() {
		case panelURL:
			expected, body = "Bearer panel-test", panelPayload(panelFirst, panelSecond)
		case origin + modelsPath:
			expected, body = "Bearer inference-test", `{"object":"list","data":[{"id":"shared"}]}`
		default:
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		if req.Header.Get("Authorization") != expected {
			t.Fatal("credential was sent to the wrong endpoint")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	quote := s.fetchPanelQuote(context.Background(), "panel-test", "inference-test", 2, info)
	if quote.err != nil || len(quote.rows) != 1 || calls != 2 {
		t.Fatalf("quote %+v, calls %d", quote, calls)
	}
	if *quote.rows[0].Rates.Input != 2_000_000 || *quote.rows[0].Rates.Output != 0 || info.Multiplier != "0" {
		t.Fatal("standard response lost the mapped quote or explicit zero")
	}
	otherGroup := int64(1)
	info.GroupID = &otherGroup
	if quote := s.fetchPanelQuote(context.Background(), "panel-test", "inference-test", 2, info); quote.err == nil || calls != 2 {
		t.Fatal("group mismatch should fail before fetching panel")
	}
	info.GroupID = &group
	if quote := s.fetchPanelQuote(context.Background(), "inference-test", "inference-test", 2, info); quote.err == nil || calls != 2 {
		t.Fatal("inference key must not be sent to the panel")
	}
	s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != panelURL {
			t.Fatal("unexpected fallback request")
		}
		return &http.Response{StatusCode: 401, Body: http.NoBody}, nil
	})
	if quote := s.fetchPanelQuote(context.Background(), "panel-test", "inference-test", 2, info); quote.err == nil || calls != 3 {
		t.Fatal("401 should fail without fallback")
	}
}

func TestPanelRejectsDisplayFallback(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	peak := false
	group := int64(2)
	for _, tc := range []struct {
		name, model, input, extra string
		age                       time.Duration
	}{
		{name: "other model", model: "other", input: "2000000"},
		{name: "different price", model: "shared", input: "1999999"},
		{name: "tiered", model: "shared", input: "2000000", extra: `,"intervals":[{}]`},
		{name: "stale", model: "shared", input: "2000000", age: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := billingInfo{ObservedAt: now.Add(-tc.age), PeakEnabled: &peak, GroupID: &group, GroupRate: "1", ResolvedRate: "1", Multiplier: "1"}
			if tc.model != "" {
				info.Pricing = json.RawMessage(`{"basis":"base","unit":"beichen_micros_per_million_tokens","models":[{"model":"` + tc.model + `","input":` + tc.input + `,"output":0` + tc.extra + `}]}`)
			}
			s := &Service{now: func() time.Time { return now }, client: newClient()}
			s.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
				body := `{"object":"list","data":[{"id":"shared"}]}`
				if req.URL.String() == panelURL {
					body = panelPayload(panelSecond)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if quote := s.fetchPanelQuote(context.Background(), "panel-test", "inference-test", group, info); (quote.err == nil && quote.verificationError == nil) || len(quote.rows) != 0 {
				t.Fatal("accepted unverified panel price")
			}
		})
	}
}

func TestPanelExpiry(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 59, 59, 0, beijing)
	peak := true
	info := billingInfo{ObservedAt: now, PeakEnabled: &peak, PeakStart: "10:00", PeakEnd: "11:00", Timezone: "Asia/Shanghai"}
	expires, err := panelExpiry(info, now)
	if err != nil || !expires.Equal(now.Add(time.Second)) {
		t.Fatalf("got %s, %v, want peak boundary", expires, err)
	}
	if _, err := panelExpiry(info, now.Add(time.Minute)); err == nil {
		t.Fatal("accepted stale observation")
	}
	peak = false
	expires, err = panelExpiry(info, now)
	if err != nil || !expires.Equal(now.Add(panelQuoteTTL)) {
		t.Fatalf("got %s, %v, want TTL expiry", expires, err)
	}
}

func TestPanelMatchesOfficialPeakBilling(t *testing.T) {
	info, err := parseBilling([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":1,"user_rate_multiplier":0.4,"resolved_rate_multiplier":0.4,"peak_rate_enabled":true,"peak_start":"10:00","peak_end":"11:00","peak_rate_multiplier":0.5,"applied_peak_multiplier":0.5,"effective_rate_multiplier":0.2,"timezone":"Asia/Shanghai","observed_at":"2026-11-18T02:30:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	var group panelGroup
	if err := json.Unmarshal([]byte(`{"id":2,"platform":"openai","rate_multiplier":1,"peak_rate_enabled":true,"peak_start":"10:00","peak_end":"11:00","peak_rate_multiplier":0.5}`), &group); err != nil {
		t.Fatal(err)
	}
	if !matchesPanelMultiplier(group, info) {
		t.Fatal("rejected official peak response with a user-specific rate")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*panelGroup, *billingInfo)
	}{
		{"wrong group rate", func(group *panelGroup, _ *billingInfo) { group.Rate = "2" }},
		{"wrong peak rule", func(group *panelGroup, _ *billingInfo) { group.PeakRate = "1" }},
		{"wrong applied multiplier", func(_ *panelGroup, info *billingInfo) { info.AppliedPeak = "1" }},
		{"wrong effective multiplier", func(_ *panelGroup, info *billingInfo) { info.Multiplier = "1" }},
		{"outside peak", func(_ *panelGroup, info *billingInfo) { info.ObservedAt = info.ObservedAt.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped, billing := group, info
			tc.mutate(&mapped, &billing)
			if matchesPanelMultiplier(mapped, billing) {
				t.Fatal("accepted mismatched multiplier")
			}
		})
	}
}

func TestPanelPreservesZeroAndUpperBoundRates(t *testing.T) {
	rates, ok := flatPanelRates(json.RawMessage(`{"billing_mode":"token","input_price":0,"output_price":1,"cache_read_price":0,"reasoning_effort_multipliers":null}`))
	if !ok || *rates.Input != 0 || *rates.Output != maxRate || *rates.CacheRead != 0 {
		t.Fatal("explicit zero or maximum supported rate was lost")
	}
}

func TestConfigurePanelMapping(t *testing.T) {
	id := uuid.New()
	s := &Service{}
	if err := s.ConfigurePanel("panel-test", `{"`+id.String()+`":2}`); err != nil || s.panelGroups[id] != 2 {
		t.Fatalf("valid mapping rejected: %v", err)
	}
	if err := s.ConfigurePanel("panel-test", `{"`+id.String()+`":2,"`+strings.ToUpper(id.String())+`":3}`); err == nil {
		t.Fatal("accepted duplicate credential binding")
	}
}
