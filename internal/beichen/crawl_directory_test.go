package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBeichenCrawlFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/website-directory.json")
	if err != nil {
		t.Fatal(err)
	}
	// Independently count the raw names before exercising the production parser.
	var raw struct {
		Data []struct {
			Platforms []map[string]json.RawMessage `json:"platforms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for _, channel := range raw.Data {
		for _, platform := range channel.Platforms {
			for _, field := range []string{"models", "supported_models"} {
				if platform[field] == nil {
					continue
				}
				var models []struct {
					Name string
					ID   json.RawMessage
				}
				if err := json.Unmarshal(platform[field], &models); err != nil {
					t.Fatal(err)
				}
				for _, model := range models {
					name := model.Name
					if name == "" {
						if err := json.Unmarshal(model.ID, &name); err != nil {
							t.Fatal(err)
						}
					}
					expected[name] = true
				}
			}
		}
	}
	report, err := New(nil, nil).CrawlDirectory(context.Background(), CrawlOptions{Fixture: fixture})
	if err != nil || !report.Complete || report.Source != "fixture" || report.KeyMatching != "not_checked" ||
		report.PlatformCount != 9 || report.GroupCount != 5 || report.ModelCount != 20 ||
		report.ExpectedModelCount != len(expected) || len(report.Models) != len(expected) || len(report.Failures) != 0 {
		t.Fatalf("invalid report counts or status: %+v, %v", report.CrawlRun, err)
	}
	for _, model := range report.Models {
		if !expected[model.Name] {
			t.Fatalf("unexpected or duplicate model %q", model.Name)
		}
		delete(expected, model.Name)
	}
	model := crawlTestModel(t, report, "grok-4.1")
	if len(model.Sources) != 2 || model.Sources[0].Groups[0].ID != 27 || model.Sources[1].Groups[0].ID != 28 {
		t.Fatalf("duplicate model lost group sources: %+v", model)
	}
	price := crawlTestModel(t, report, "grok-4.2").Sources[0]
	if !price.PriceComplete || !slices.Equal(price.IgnoredFields, []string{"per_request_price"}) {
		t.Fatalf("zero rates or ignored pricing field lost: %+v", price)
	}
	for _, name := range []string{"Claude Fable 5", "claude-opus-5", "glm-5-turbo", "kimi-k3-preview"} {
		if source := crawlTestModel(t, report, name).Sources[0]; source.PriceComplete || source.PriceBlocker != "missing_price" {
			t.Fatalf("missing price misclassified for %q: %+v", name, source)
		}
	}
}

func crawlTestModel(t *testing.T, report CrawlReport, name string) CrawlModel {
	t.Helper()
	for _, model := range report.Models {
		if model.Name == name {
			return model
		}
	}
	t.Fatalf("report is missing %q", name)
	return CrawlModel{}
}

func TestBeichenCrawlPartialFixture(t *testing.T) {
	body := []byte(`{"code":0,"data":[{"id":9,"name":"Fixture","platforms":[
		{"platform":"broken-list","groups":null,"models":{}},
		{"platform":"readable","groups":[{"id":3}],"models":[{"name":"no-price"},{"name":"bad-price","pricing":{"input_price":{}}}],
			"supported_models":[{"name":"empty-price","pricing":{"input_price":"","output_price":null}},null]},
		{"platform":[],"models":[{"name":"unlocated"}]},
		{"platform":"also-readable","models":[{"name":"works","pricing":{"input_price":0,"output_price":0}}]}
	]}]}`)
	report, err := New(nil, nil).CrawlDirectory(context.Background(), CrawlOptions{Fixture: body})
	if !errors.Is(err, errWebsiteDirectoryPartial) || report.Complete || report.ModelCount != 4 ||
		report.ExpectedModelCount != 5 || report.PlatformCount != 2 || report.GroupCount != 1 || len(report.Failures) != 3 {
		t.Fatalf("partial crawl lost readable results or failure evidence: %+v, %v", report.CrawlRun, err)
	}
	first := report.Failures[0]
	if first.Platform != "broken-list" || first.ChannelID != 9 || first.Path != "data[0].platforms[0]" ||
		!strings.Contains(first.Reason, "models 不是模型数组") {
		t.Fatalf("invalid platform failure detail: %+v", first)
	}
	if price := crawlTestModel(t, report, "bad-price").Sources[0]; price.PriceBlocker != crawlPriceInvalid {
		t.Fatalf("malformed price lost the model or diagnostic: %+v", price)
	}
	if price := crawlTestModel(t, report, "works").Sources[0]; !price.PriceComplete {
		t.Fatalf("failed sibling affected the healthy platform: %+v", price)
	}
}

func TestBeichenCrawlWebsiteUsesOnlyPanelEndpoints(t *testing.T) {
	const token = "fixture-access-token"
	for _, login := range []bool{true, false} {
		name := "token"
		if login {
			name = "login"
		}
		t.Run(name, func(t *testing.T) {
			service := New(nil, nil)
			options := CrawlOptions{PanelToken: token}
			if login {
				options.PanelToken = ""
				if err := service.ConfigureWebSession("fixture@example.test", "fixture-password"); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.Method+" "+r.URL.String())
				body := `{"code":0,"data":[{"platforms":[{"platform":"fixture","models":["fixture-model"]}]}]}`
				switch {
				case login && r.Method == http.MethodPost && r.URL.String() == webAuthURL+"login":
					body = `{"code":0,"data":{"token_type":"Bearer","access_token":"` + token + `"}}`
				case r.Method == http.MethodGet && r.URL.String() == panelURL && r.Header.Get("Authorization") == "Bearer "+token:
				default:
					t.Fatalf("unexpected website crawl request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			report, err := service.CrawlDirectory(context.Background(), options)
			want := []string{"GET " + panelURL}
			if login {
				want = append([]string{"POST " + webAuthURL + "login"}, want...)
			}
			if err != nil || report.Source != "website" || report.ModelCount != 1 || !slices.Equal(paths, want) {
				t.Fatalf("wrong website crawl: %+v, %v, %v", report.CrawlRun, paths, err)
			}
		})
	}
}
