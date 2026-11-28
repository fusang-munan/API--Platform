package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func TestBindingRouteProbeEndpointsForModel(t *testing.T) {
	t.Parallel()
	chat := BindingRouteProbeEndpointsForModel("gpt-5.6-luna")
	if len(chat) != 3 || chat[0] != BindingRouteProbeChatCompletions || chat[1] != BindingRouteProbeResponses || chat[2] != BindingRouteProbeMessages {
		t.Fatalf("unexpected chat probe endpoints: %#v", chat)
	}
	image := BindingRouteProbeEndpointsForModel("gpt-image-2")
	if len(image) != 1 || image[0] != BindingRouteProbeImagesGenerations {
		t.Fatalf("unexpected image probe endpoints: %#v", image)
	}
}

func TestRouteProbeDiscoverKeepsSuccessfulRoutes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions", "/v1/responses":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		case "/v1/images/generations":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	probe := NewRouteProbe(server.URL+"/v1", "test-key")
	adapters, err := probe.Discover(context.Background(), "gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}
	found := map[modelbinding.Adapter]bool{}
	for _, adapter := range adapters {
		found[adapter] = true
	}
	if !found[modelbinding.AdapterOpenaiCompatible] || !found[modelbinding.AdapterOpenaiResponses] || found[modelbinding.AdapterOpenaiImages] {
		t.Fatalf("unexpected adapters: %#v", adapters)
	}
}

func TestRouteProbeDiscoverKeepsImagesOnly(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/images/generations" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"url":"http://example/image.png"}]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	probe := NewRouteProbe(server.URL+"/v1", "test-key")
	adapters, err := probe.Discover(context.Background(), "gpt-image-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0] != modelbinding.AdapterOpenaiImages {
		t.Fatalf("expected images only, got %#v", adapters)
	}
}

func TestResolveBindingAdaptersPrefersCatalogSignals(t *testing.T) {
	t.Parallel()
	entry := ProviderEntry{
		ID: "flux-1",
		Raw: map[string]any{
			"model_type":               "图像",
			"supported_endpoint_types": []any{"image-generation"},
		},
	}
	adapters, err := ResolveBindingAdapters(context.Background(), nil, entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0] != modelbinding.AdapterOpenaiImages {
		t.Fatalf("expected image adapter from catalog signals, got %#v", adapters)
	}
}

func TestRouteProbeStepReportsFailureWithoutAbortingDiscover(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	probe := NewRouteProbe(server.URL+"/v1", "test-key")
	adapters, err := probe.Discover(context.Background(), "gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0] != modelbinding.AdapterOpenaiResponses {
		t.Fatalf("expected responses only, got %#v", adapters)
	}
}

func TestRouteProbeStepReturnsStructuredResult(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	probe := NewRouteProbe(server.URL+"/v1", "test-key")
	step, err := probe.ProbeStep(context.Background(), "gpt-5.6-luna", BindingRouteProbeChatCompletions)
	if err != nil || !step.OK || step.Adapter != modelbinding.AdapterOpenaiCompatible {
		t.Fatalf("unexpected step: %#v err=%v", step, err)
	}
}

func TestGeminiRouteProbeRecordsOnlyRequestedOperation(t *testing.T) {
	for _, base := range []string{"https://api.beichenapi.com", "https://api.beichenapi.com/v1"} {
		for _, tc := range []struct {
			endpoint BindingRouteProbeEndpoint
			op, body string
		}{
			{BindingRouteProbeGenerateContent, "generateContent", `{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"MAX_TOKENS"}]}`},
			{BindingRouteProbeStreamGenerateContent, "streamGenerateContent", `[{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]},{"candidates":[{"finishReason":"MAX_TOKENS"}]}]`},
			{BindingRouteProbeEmbedContent, "embedContent", `{"embedding":{"values":[0.5]}}`},
		} {
			calls := 0
			probe := &RouteProbe{BaseURL: base, Credential: "fixture", Client: &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				want := "/v1beta/models/upstream:" + tc.op
				if r.Method != http.MethodPost || r.URL.Path != want || r.Header.Get("x-goog-api-key") != "fixture" || r.Header.Get("Authorization") != "" {
					t.Errorf("%s: native request used wrong method, path or authentication", tc.op)
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("%s: probe payload invalid: %v", tc.op, err)
				}
				if tc.endpoint == BindingRouteProbeEmbedContent {
					if string(payload["model"]) != `"models/upstream"` || len(payload["content"]) == 0 {
						t.Error("embedding probe is missing the native model or text")
					}
				} else if len(payload["contents"]) == 0 || string(payload["generationConfig"]) != `{"maxOutputTokens":1}` {
					t.Error("generation probe is missing the native prompt or output limit")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			step, err := probe.ProbeStep(context.Background(), "models/upstream", tc.endpoint)
			if err != nil || calls != 1 || !step.OK || step.Endpoint != tc.endpoint || step.Adapter != modelbinding.AdapterGoogleGeminiV1beta {
				t.Fatalf("%s %s: calls=%d step=%+v err=%v", base, tc.op, calls, step, err)
			}
		}
	}
}

func TestGeminiDiscoveryAcceptsOnePassingOperation(t *testing.T) {
	probe := &RouteProbe{BaseURL: "https://api.beichenapi.com/v1", Credential: "fixture", Client: &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusNotFound, `{}`
		switch r.URL.Path {
		case "/v1beta/models/gemini-pro:generateContent":
			status, body = http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"MAX_TOKENS"}]}`
		case "/v1beta/models/gemini-pro:streamGenerateContent":
			status, body = http.StatusOK, `{"wrong":"response shape"}`
		case "/v1beta/models/gemini-embedding-001:embedContent":
			status, body = http.StatusOK, `{"embedding":{"values":[0.5]}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	adapters, err := probe.Discover(context.Background(), "gemini-pro")
	if err != nil || !slices.Equal(adapters, []modelbinding.Adapter{modelbinding.AdapterGoogleGeminiV1beta}) {
		t.Fatalf("native protocol lost despite one operation passing: %v %v", adapters, err)
	}
	step, err := probe.ProbeStep(context.Background(), "gemini-pro", BindingRouteProbeStreamGenerateContent)
	if err != nil || step.OK || step.FailureCode != "invalid_response" {
		t.Fatalf("non-native response marked stream probe passed: %+v %v", step, err)
	}
	step, err = probe.ProbeTokenStep(context.Background(), "gemini-embedding-001", string(modelbinding.AdapterGoogleGeminiV1beta))
	if err != nil || step.Endpoint != BindingRouteProbeEmbedContent || !step.OK {
		t.Fatalf("embedding model probed the wrong operation: %+v %v", step, err)
	}
}

func TestGeminiProbeRejectsEmptyOrIncompleteGeneration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint BindingRouteProbeEndpoint
		body     string
	}{
		{"empty candidates", BindingRouteProbeGenerateContent, `{"candidates":[]}`},
		{"text without finish", BindingRouteProbeGenerateContent, `{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]}`},
		{"stream text without finish", BindingRouteProbeStreamGenerateContent, `[{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]}]`},
		{"stream finish without text", BindingRouteProbeStreamGenerateContent, `[{"candidates":[{"finishReason":"STOP"}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &RouteProbe{BaseURL: "https://api.beichenapi.com/v1", Credential: "fixture",
				Client: &http.Client{Transport: modelScanTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}}
			step, err := probe.ProbeStep(context.Background(), "gemini-pro", tc.endpoint)
			if err != nil || step.OK || step.FailureCode != "invalid_response" {
				t.Fatalf("incomplete response passed the probe: %+v %v", step, err)
			}
		})
	}
}
