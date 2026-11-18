package beichen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFixedOriginRejectsRedirectsAndBoundsResponses(t *testing.T) {
	service := New(nil, nil)
	calls := 0
	service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != origin+billingPath || r.Method != http.MethodGet ||
			r.Header.Get("Authorization") != "Bearer fake-test-key" {
			t.Fatal("unexpected authenticated destination")
		}
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"https://elsewhere.invalid/"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: -1,
			Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
		}, nil
	})
	for _, expected := range []string{"HTTP 302", "2 MiB"} {
		_, err := service.fetch(context.Background(), "fake-test-key", billingPath, nil)
		if err == nil || !strings.Contains(err.Error(), expected) || strings.Contains(err.Error(), "fake-test-key") {
			t.Fatalf("unexpected fetch error: %v", err)
		}
	}
	if _, err := service.fetch(context.Background(), "fake-test-key", "//elsewhere.invalid", nil); err == nil || calls != 2 {
		t.Fatalf("invalid path was used, requests=%d", calls)
	}
}

func TestBillingDoesNotInferRatesFromMultiplier(t *testing.T) {
	info, err := parseBilling([]byte(`{
		"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
		"effective_rate_multiplier":1.25,"timezone":"Asia/Shanghai"
	}`))
	if err != nil || info.Multiplier.String() != "1.25" {
		t.Fatalf("parse billing: %+v, %v", info, err)
	}
	if _, _, err = parsePricing(info.Pricing); err == nil {
		t.Fatal("multiplier-only response was treated as a price list")
	}
	basis, rows, err := parsePricing(json.RawMessage(`{
		"basis":"base","unit":"usd_micros_per_million_tokens",
		"models":[{"model":"example","input":150,"output":300,"cache_read":0}]
	}`))
	if err != nil || basis != "base" || len(rows) != 1 || rows[0].CacheRead == nil ||
		*rows[0].CacheRead != 0 || rows[0].CacheWrite != nil {
		t.Fatalf("parse confirmed base prices: %s, %+v, %v", basis, rows, err)
	}
}

func TestUsageNeedsVerifiedDayAndActualCurrency(t *testing.T) {
	payload := `{"mode":"unrestricted","isValid":true,"unit":"USD","date":"2026-11-18","timezone":"Asia/Shanghai","model_stats":[
		{"model":"example","requests":0,"input_tokens":20,"cache_tokens":7,"actual_cost":0.0000015,"cost":1000},
		{"model":"other","requests":1,"input_tokens":18,"output_tokens":3,"cache_read_tokens":0,"cache_creation_tokens":4,"cost":99}
	]}`
	for _, candidate := range []string{
		strings.Replace(payload, `,"timezone":"Asia/Shanghai"`, "", 1),
		strings.Replace(payload, `,"date":"2026-11-18"`, "", 1),
		strings.Replace(payload, `"Asia/Shanghai"`, `"UTC"`, 1),
		strings.Replace(payload, `"2026-11-18"`, `"2026-11-17"`, 1),
	} {
		if _, err := parseUsage([]byte(candidate), "2026-11-18"); err == nil {
			t.Fatal("unverified or mismatched window was accepted")
		}
	}
	rows, err := parseUsage([]byte(payload), "2026-11-18")
	if err != nil || len(rows) != 2 {
		t.Fatalf("parse usage: %v, %v", rows, err)
	}
	if rows[0].requests == nil || *rows[0].requests != 0 || rows[0].billed == nil || *rows[0].billed != 2 ||
		rows[0].cacheRead != nil || rows[0].cacheWrite != nil || rows[0].output != nil ||
		rows[1].billed != nil || rows[1].cacheRead == nil || *rows[1].cacheRead != 0 ||
		rows[1].cacheWrite == nil || *rows[1].cacheWrite != 4 {
		t.Fatalf("bad explicit/unknown values: %+v", rows)
	}
	unknownUnit := strings.Replace(payload, `"USD"`, `"credits"`, 1)
	rows, err = parseUsage([]byte(unknownUnit), "2026-11-18")
	if err != nil || rows[0].billed != nil {
		t.Fatalf("unconfirmed currency mapped to dollars: %+v, %v", rows, err)
	}
	aggregate := `{"mode":"unrestricted","isValid":true,"unit":"USD","usage":{"today":{"requests":3,"actual_cost":2}}}`
	if _, err := parseUsage([]byte(aggregate), "2026-11-18"); err == nil {
		t.Fatal("key-wide statistics were treated as model statistics")
	}
}

func TestUsageDistinguishesConfirmedEmptyFromMissingModels(t *testing.T) {
	payload := `{"mode":"unrestricted","isValid":true,"date":"2026-11-18","timezone":"Asia/Shanghai","model_stats":[]}`
	rows, err := parseUsage([]byte(payload), "2026-11-18")
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("explicit empty day rejected: %+v, %v", rows, err)
	}
	for _, candidate := range []string{
		strings.Replace(payload, `,"model_stats":[]`, "", 1),
		strings.Replace(payload, `"model_stats":[]`, `"model_stats":null`, 1),
		strings.Replace(payload, `"model_stats":[]`, `"model_stats":[{"model":"example","total_tokens":20}]`, 1),
	} {
		if _, err = parseUsage([]byte(candidate), "2026-11-18"); err == nil {
			t.Fatal("unreported or unusable models were treated as an empty day")
		}
	}
}

func TestUsageVerifiedEmptyDayAllowsOtherZeroDays(t *testing.T) {
	const payload = `{"mode":"unrestricted","isValid":true,"unit":"USD","model_stats":[],
	  "daily_usage":[
	    {"date":"2026-11-18","requests":0,"input_tokens":0,"output_tokens":0,"actual_cost":0},
	    {"date":"2026-11-17","requests":0,"input_tokens":0,"output_tokens":0,"actual_cost":0}
	  ]}`
	for _, day := range []string{"2026-11-18", "2026-11-17"} {
		rows, err := parseUsage([]byte(payload), day)
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("verified zero day was rejected for %s: %+v, %v", day, rows, err)
		}
	}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"nonzero requests", strings.Replace(payload, `"requests":0`, `"requests":1`, 1), "不一致"},
		{"missing requests", strings.Replace(payload, `"requests":0,`, "", 1), "缺少可核对"},
		{"missing cost", strings.Replace(payload, `,"actual_cost":0`, "", 1), "缺少实付金额"},
		{"sub-micro positive cost", strings.Replace(payload, `"actual_cost":0`, `"actual_cost":0.0000001`, 1), "不一致"},
		{"invalid other date", strings.Replace(payload, "2026-11-17", "2026-11-31", 1), "日期无效"},
		{"duplicate date", strings.Replace(payload, "2026-11-17", "2026-11-18", 1), "日期无效或重复"},
		{"explicit date contradicts daily cost", strings.Replace(
			strings.Replace(payload, `"model_stats":`, `"date":"2026-11-18","timezone":"Asia/Shanghai","model_stats":`, 1),
			`"actual_cost":0`, `"actual_cost":1`, 1), "不一致"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := parseUsage([]byte(tc.body), "2026-11-18")
			if rows != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ambiguous empty snapshot was accepted: %+v, %v", rows, err)
			}
		})
	}
}

const geminiUsageSample = `{
  "mode":"unrestricted","unit":"USD",
  "daily_usage":[{"date":"2026-11-18","requests":1,"input_tokens":4,"output_tokens":2,
    "cache_read_tokens":0,"cache_write_tokens":0,"actual_cost":0.0000012}],
  "model_stats":[{"model":"gemini-3.1-flash-lite","requests":1,"input_tokens":4,"output_tokens":2,
    "cache_read_tokens":0,"cache_creation_tokens":0,"actual_cost":0.0000012}]
}`

func TestUsageVerifiesGeminiDailySample(t *testing.T) {
	for _, tc := range []struct {
		body   string
		micros int64
	}{
		{geminiUsageSample, 1},
		{strings.ReplaceAll(geminiUsageSample, "0.0000012", "0.00000049"), 0},
	} {
		rows, err := parseUsage([]byte(tc.body), "2026-11-18")
		if err != nil || len(rows) != 1 {
			t.Fatalf("Gemini sample: rows=%+v, error=%v", rows, err)
		}
		if rows[0].model != "gemini-3.1-flash-lite" || rows[0].billed == nil || *rows[0].billed != tc.micros {
			t.Fatalf("Gemini sample was mapped incorrectly: %+v", rows[0])
		}
	}
}

func TestUsageRequiresUniqueDailyAndModelTotals(t *testing.T) {
	const anotherDay = `{"date":"2026-11-17","requests":1,"input_tokens":4,"output_tokens":2,
       "cache_read_tokens":0,"cache_write_tokens":0,"actual_cost":0.0000012}`
	withAnotherDay := func(point string) string {
		return strings.Replace(geminiUsageSample, `0.0000012}],`, `0.0000012},`+point+`],`, 1)
	}
	for _, tc := range []struct {
		body string
		day  string
		want string
	}{
		{geminiUsageSample, "2026-11-17", "缺少目标日期"},
		{strings.Replace(geminiUsageSample, `,"output_tokens":2`, "", 1), "2026-11-18", "缺少可核对"},
		{strings.Replace(geminiUsageSample, `"input_tokens":4`, `"input_tokens":9`, 1), "2026-11-18", "不一致"},
		{strings.Replace(geminiUsageSample, `"cache_write_tokens":0`, `"cache_write_tokens":1`, 1), "2026-11-18", "不一致"},
		{strings.Replace(geminiUsageSample, "0.0000012", "0.0000013", 1), "2026-11-18", "不一致"},
		{withAnotherDay(anotherDay), "2026-11-18", "匹配多个日期"},
		{withAnotherDay(strings.Replace(anotherDay, `,"actual_cost":0.0000012`, "", 1)), "2026-11-18", "匹配多个日期"},
		{withAnotherDay(strings.Replace(anotherDay, "0.0000012", "0.0000013", 1)), "2026-11-18", ""},
		{strings.Replace(geminiUsageSample, `"mode":`, `"isValid":false,"mode":`, 1), "2026-11-18", "未返回"},
	} {
		_, err := parseUsage([]byte(tc.body), tc.day)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("day %s: want %q, got %v", tc.day, tc.want, err)
		}
	}
}

func TestUsageMatchesDailyTotalAcrossAllModels(t *testing.T) {
	payload := `{"mode":"unrestricted","unit":"USD",
	  "daily_usage":[{"date":"2026-11-18","requests":3,"input_tokens":9,"output_tokens":3,"actual_cost":0.0000012}],
	  "model_stats":[
	    {"model":"gemini-one","requests":1,"input_tokens":4,"output_tokens":2,"actual_cost":0.0000007},
	    {"model":"gemini-two","requests":2,"input_tokens":5,"output_tokens":1,"actual_cost":0.0000005}
	  ]}`
	rows, err := parseUsage([]byte(payload), "2026-11-18")
	if err != nil || len(rows) != 2 {
		t.Fatalf("valid combined model totals rejected: %+v, %v", rows, err)
	}
}

func TestUsageRescanDaysAtBeijingMidnight(t *testing.T) {
	service := New(nil, nil)
	service.now = func() time.Time { return time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		manual bool
		days   string
	}{
		{true, "2026-11-18,2026-11-17"},
		{false, "2026-11-17,2026-11-16,2026-11-15"},
	} {
		var days []string
		service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			query := r.URL.Query()
			day := query.Get("start_date")
			if r.URL.Path != usagePath || query.Get("end_date") != day || query.Get("days") != "4" || query.Get("timezone") != "Asia/Shanghai" {
				t.Fatalf("unexpected query %s", r.URL)
			}
			days = append(days, day)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
				`{"mode":"unrestricted","isValid":true,"date":"` + day + `","timezone":"Asia/Shanghai","model_stats":[]}`))}, nil
		})
		result := service.fetchUsage(context.Background(), syncJob{}, "fake-key", tc.manual)
		if result.err != nil || result.save == nil || strings.Join(days, ",") != tc.days {
			t.Fatalf("wrong rescan dates for manual=%v: %v (%v)", tc.manual, days, result.err)
		}
	}
}

func TestUsageDoesNotStageRepeatedDayForAnotherWindow(t *testing.T) {
	service := New(nil, nil)
	service.now = func() time.Time { return time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC) }
	service.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			geminiUsageSample))}, nil
	})
	result := service.fetchUsage(context.Background(), syncJob{}, "fake-key", false)
	if result.err == nil || result.save != nil {
		t.Fatalf("repeated today response staged for past days: %v", result.err)
	}
}

func TestCSVValidationPreservesZeroAndRejectsDuplicateRows(t *testing.T) {
	priceCSV := "\ufeffmodel,input,output,cache_read,cache_write\r\nexample,0,300,0,\r\n"
	csvRows, err := readCSV(priceCSV, priceHeader)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := parseCSVPrices(csvRows)
	if err != nil || len(prices) != 1 || prices[0].Input == nil || *prices[0].Input != 0 ||
		prices[0].CacheRead == nil || *prices[0].CacheRead != 0 || prices[0].CacheWrite != nil {
		t.Fatalf("price import values changed: %+v, %v", prices, err)
	}
	usageCSV := strings.Join(usageHeader, ",") + "\n2026-11-18,example,2,3,4,,0,\n"
	data, err := readCSV(usageCSV, usageHeader)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := parseCSVUsage(data)
	if err != nil || usage[0].cacheWrite == nil || *usage[0].cacheWrite != 0 || usage[0].billed != nil {
		t.Fatalf("usage import values changed: %+v, %v", usage, err)
	}
	if _, err = parseCSVUsage(append(data, data[0])); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatalf("duplicate CSV row is not a validation error: %v", err)
	}
	service := New(nil, nil)
	invalidPrice := PriceInput{Mode: "manual", Rates: &Rates{}}
	if err = service.SetPrice(context.Background(), uuid.New(), uuid.New(), uuid.New(), "example", invalidPrice); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatalf("incomplete manual price is not a validation error: %v", err)
	}
}
