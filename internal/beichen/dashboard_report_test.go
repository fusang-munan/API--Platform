package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testDashboardBearer = "fixture-bearer-sensitive"

type dashboardTestFixture struct {
	Account   json.RawMessage            `json:"account"`
	Keys      []json.RawMessage          `json:"keys"`
	Dashboard json.RawMessage            `json:"dashboard"`
	Record    map[string]json.RawMessage `json:"record"`
}

func dashboardFixture(t *testing.T) dashboardTestFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/dashboard-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture dashboardTestFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func dashboardTestResponse(t *testing.T, status int, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}
}

func dashboardTestService(t *testing.T, reply func(*http.Request) *http.Response) (*Service, *LocalLoginStore) {
	t.Helper()
	store := &LocalLoginStore{dir: t.TempDir()}
	now := time.Date(2026, 11, 21, 12, 30, 0, 0, beijing)
	expires := now.Add(time.Hour)
	if err := store.save(localPanelLogin{Access: testDashboardBearer, Refresh: "fixture-refresh", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	service := New(nil, nil)
	service.UseLocalLogin(store)
	service.now = func() time.Time { return now }
	service.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "beichenapi.com" {
			t.Fatalf("unexpected request method or origin: %s %s", req.Method, req.URL.Host)
		}
		if req.Header.Get("Authorization") != "Bearer "+testDashboardBearer ||
			req.Header.Get("X-User-UI-Request") != "1" || req.URL.User != nil {
			t.Fatal("bad web request headers")
		}
		return reply(req), nil
	})
	return service, store
}

func fixtureDashboardStats(n int, prefix string) map[string]any {
	return map[string]any{
		prefix + "requests": n, "total_tokens": n * 5,
		prefix + "input_tokens": n * 2, prefix + "output_tokens": n,
		prefix + "cache_read_tokens": n, prefix + "cache_creation_tokens": n,
		prefix + "cost":        json.Number(fmt.Sprintf("%d.%02d", 2*n/100, 2*n%100)),
		prefix + "actual_cost": json.Number(fmt.Sprintf("%d.%02d", n/100, n%100)),
	}
}

func TestDashboardReportOptionsAndPagination(t *testing.T) {
	for _, tt := range []struct {
		name, period, start string
		requests, pages     int
		empty, keyGap       bool
	}{
		{"today", "today", "2026-11-21", 1, 0, false, false},
		{"seven_days_two_pages", "7d", "2026-11-15", 101, 2, false, false},
		{"all_since_signup", "all", "2026-11-10", 101, 0, false, false},
		{"show_key_difference", "all", "2026-11-10", 101, 0, false, true},
		{"empty", "7d", "2026-11-15", 0, 1, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := dashboardFixture(t)
			ledgerPages, statsFilter, modelsFilter := []string{}, []string{}, []string{}
			s, _ := dashboardTestService(t, func(r *http.Request) *http.Response {
				path := strings.TrimPrefix(r.URL.Path, "/api/v1")
				q := r.URL.Query()
				if path != "/keys" && q.Get("timezone") != "Asia/Shanghai" {
					t.Fatalf("wrong timezone for %s", path)
				}
				switch path {
				case "/usage/stats", "/usage/dashboard/models", "/usage/dashboard/trend", "/usage":
					if q.Get("start_date") != tt.start || q.Get("end_date") != "2026-11-21" || q.Has("range") {
						t.Fatalf("wrong date query for %s: %v", path, q)
					}
				}
				key := q.Get("api_key_id")
				n := tt.requests
				switch key {
				case "11":
					n = max(0, n-1)
				case "12":
					n = min(1, n)
				case "":
				default:
					t.Fatal("wrong Key filter")
				}

				var payload any
				switch path {
				case "/auth/me":
					payload = f.Account
				case dashboardStats:
					payload = f.Dashboard
					if tt.empty {
						payload = fixtureDashboardStats(0, "total_")
						p := payload.(map[string]any)
						p["total_api_keys"], p["active_api_keys"], p["by_platform"] = 0, 0, nil
						for k, v := range fixtureDashboardStats(0, "today_") {
							p[k] = v
						}
						p["today_tokens"] = 0
					}
					if q.Has("start_date") || q.Has("granularity") {
						t.Fatal("dashboard cards should remain fixed")
					}
				case "/keys":
					if q.Get("page") != "1" || q.Get("page_size") != "100" {
						t.Fatal("unexpected Key page")
					}
					keys := f.Keys
					if tt.empty {
						keys = keys[:0]
					}
					payload = map[string]any{"items": keys, "total": len(keys), "page": 1, "page_size": 100, "pages": 1}
				case "/usage/stats":
					statsFilter = append(statsFilter, key)
					if tt.keyGap && key == "11" {
						n--
					}
					payload = fixtureDashboardStats(n, "total_")
				case "/usage/dashboard/models":
					modelsFilter = append(modelsFilter, key)
					rows := []any{}
					if n > 0 {
						row := fixtureDashboardStats(n, "")
						row["model"] = "fixture-model"
						rows = append(rows, row)
					}
					payload = map[string]any{"models": rows}
				case "/usage/dashboard/trend":
					if q.Get("granularity") != "day" {
						t.Fatal("wrong granularity")
					}
					rows := []any{}
					if n > 0 {
						row := fixtureDashboardStats(1, "")
						row["date"] = "2026-11-21"
						rows = append(rows, row)
					}
					payload = map[string]any{"trend": rows}
				case "/usage":
					records := []any{}
					page, _ := strconv.Atoi(q.Get("page"))
					size, _ := strconv.Atoi(q.Get("page_size"))
					switch {
					case size == 5 && page == 1 && q.Get("sort_by") == "created_at" && q.Get("sort_order") == "desc":
						for i := 0; i < min(size, n); i++ {
							var record any = f.Record
							if i != 0 {
								record = fixtureEarlierReceipt(101 - i)
							}
							records = append(records, record)
						}
					case size == 100 && q.Get("sort_by") == "id" && q.Get("sort_order") == "asc":
						ledgerPages = append(ledgerPages, strconv.Itoa(page))
						for i := (page-1)*size + 1; i <= min(page*size, n); i++ {
							if i == 101 {
								records = append(records, f.Record)
							} else {
								records = append(records, fixtureEarlierReceipt(i))
							}
						}
					default:
						t.Fatalf("unexpected paging query: %v", q)
					}
					payload = map[string]any{"items": records, "total": n, "page": page, "page_size": size, "pages": max(1, (n+size-1)/size)}
				default:
					t.Fatalf("unexpected web path %s", path)
				}
				return dashboardTestResponse(t, http.StatusOK, map[string]any{"code": 0, "data": payload})
			})
			got, err := s.ReadDashboard(context.Background(), DashboardOptions{Range: tt.period, Granularity: "day"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Totals.Requests != int64(tt.requests) || len(ledgerPages) != tt.pages ||
				got.Currency != "CNY" || got.StartDate != tt.start || got.Totals.Tokens == nil ||
				*got.Totals.Tokens != int64(tt.requests*5) || got.KeyCheck.Matches == tt.keyGap {
				t.Fatalf("wrong usage summary or paging: %+v pages=%v", got.KeyCheck, ledgerPages)
			}
			if tt.empty {
				if len(got.Recent)+len(got.Trend)+len(got.Models)+len(got.Keys)+len(got.Platforms.Items) != 0 {
					t.Fatal("empty data should stay empty")
				}
			} else if strings.Join(statsFilter, ",") != ",11,12" || strings.Join(modelsFilter, ",") != ",11,12" ||
				got.Recent[0].DisplayTokens == nil || *got.Recent[0].DisplayTokens != 3 {
				t.Fatal("wrong Key filters or recent dashboard Token convention")
			}
			if tt.keyGap && (got.KeyCheck.Delta.Requests != 1 || *got.KeyCheck.Delta.Actual != "0.01" ||
				got.Keys[0].ModelCheck.Matches) {
				t.Fatal("expected a Key and model reconciliation gap")
			}
			if tt.period != "7d" && (got.CardCheck == nil || !got.CardCheck.Matches) {
				t.Fatal("dashboard totals should agree with the range after decimal rounding")
			}
			if tt.pages == 2 && (strings.Join(ledgerPages, ",") != "1,2" ||
				got.Platforms.Source != "usage.group.platform" || got.Platforms.Delta.Requests != 0 ||
				*got.Platforms.Delta.Actual != "0") {
				t.Fatal("wrong platform rollup across pages")
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{testDashboardBearer, "fixture-refresh", "sk-fixtureSecretNeverOutput",
				"fixture@example.invalid", "fixture-private-request-id"} {
				if bytes.Contains(data, []byte(private)) {
					t.Fatal("report retained private fixture information")
				}
			}
		})
	}
}

func fixtureEarlierReceipt(id int) map[string]any {
	return map[string]any{"id": id, "api_key_id": 11, "user_id": 42, "model": "fixture-model",
		"created_at": "2026-11-20T13:00:00+08:00", "input_tokens": 2, "output_tokens": 1,
		"cache_read_tokens": 1, "cache_creation_tokens": 1, "total_cost": 0.02, "actual_cost": 0.01,
		"group": map[string]any{"platform": "openai"}}
}

func TestDashboardReportExpiredIsReadOnly(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			count := 0
			s, store := dashboardTestService(t, func(r *http.Request) *http.Response {
				count++
				if r.URL.Path != "/api/v1/auth/me" {
					t.Fatalf("unexpected request after expiration: %s", r.URL.Path)
				}
				return dashboardTestResponse(t, status, map[string]any{"code": 401, "message": testDashboardBearer})
			})
			path := filepath.Join(store.dir, localSessionFile)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ReadDashboard(context.Background(), DashboardOptions{Range: "7d", Granularity: "day"})
			if !errors.Is(err, errLocalReauthenticate) || strings.Contains(err.Error(), testDashboardBearer) || count != 1 {
				t.Fatal("expected sanitized expiry with exactly one GET")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only report modified encrypted session")
			}
			_, err = s.ReadDashboard(context.Background(), DashboardOptions{Range: "7d", Granularity: "hour"})
			if err == nil || count != 1 {
				t.Fatal("unexpected request for unsupported granularity")
			}
		})
	}
}
