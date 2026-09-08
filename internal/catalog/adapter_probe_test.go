package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
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
