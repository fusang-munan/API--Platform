package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
)

func TestProbeModelRejectsInactiveCredential(t *testing.T) {
	service, mock, closeService := newIdentityTestService(t)
	defer closeService()
	id, providerID := uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectQuery(`FROM "provider_credentials"`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows(providercredential.Columns).AddRow(
			id, now, now, providerID, "哈吉米", "5", []byte{1}, "inactive",
		))
	mock.ExpectQuery(`FROM "providers"`).WithArgs(providerID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "base_url"}).AddRow(providerID, "https://api.beichenapi.com"))

	_, err := service.ProbeModel(context.Background(), ProbeInput{CredentialID: &id, UpstreamModelName: "gemini-fixture"})
	if err == nil || domain.ErrorCode(err) != domain.CodeValidation || err.Error() != "provider credential is inactive" {
		t.Fatalf("expected inactive credential rejection before decryption, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRequestDefinitions(t *testing.T) {
	for _, endpoint := range []string{"chat_completions", "responses", "messages", "embeddings", "rerank"} {
		definition, err := buildProbeRequest(endpoint, "upstream-model")
		if err != nil || definition.Path == "" || len(definition.Body) == 0 {
			t.Fatalf("invalid %s definition: %#v, err=%v", endpoint, definition, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(definition.Body, &payload); err != nil || payload["model"] != "upstream-model" {
			t.Fatalf("invalid %s body: %s", endpoint, definition.Body)
		}
	}
}

func TestModelCatalogProbeUsesModelsEndpoint(t *testing.T) {
	definition := modelCatalogProbeRequest()
	if definition.Method != "GET" || definition.Path != "/v1/models" || len(definition.Body) != 0 {
		t.Fatalf("unexpected model catalog probe: %#v", definition)
	}
}

func TestExtractProbeConfigurationMatchesModelAndUsesExplicitFields(t *testing.T) {
	configuration := extractProbeConfiguration([]byte(`{"data":[{"id":"other"},{"id":"GLM-5.1","description":"<p>GLM reasoning model</p>","model_type":"文本","tags":"思考,工具","supported_endpoint_types":["openai-response"],"context_window":128000,"max_output_tokens":8192}]}`), "glm-5.1")
	if configuration.Description != "GLM reasoning model" || configuration.Category != "" || configuration.ContextWindow == nil || *configuration.ContextWindow != 128000 || configuration.MaxOutputTokens == nil || *configuration.MaxOutputTokens != 8192 {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}
	if len(configuration.Capabilities) != 0 || len(configuration.InputModalities) != 0 || len(configuration.OutputModalities) != 0 {
		t.Fatalf("expected no guessed modalities/capabilities: %#v", configuration)
	}
}

func TestProbeResponseDoesNotExposeUpstreamBody(t *testing.T) {
	raw, err := json.Marshal(ProbeResponse{Results: []ProbeResult{{Endpoint: "models", Path: "/v1/models", HTTPStatus: 200}}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("unexpected results: %s", raw)
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected result: %s", raw)
	}
	if _, exposed := result["response"]; exposed {
		t.Fatalf("upstream response body was exposed: %s", raw)
	}
}

func TestExtractProbeLimitsIgnoresUsageAndByteFields(t *testing.T) {
	limits := extractProbeLimits([]byte(`{"context_window":128000,"max_input_tokens":120000,"max_output_tokens":8000,"usage":{"input_tokens":10},"max_bytes":999999}`))
	if limits.ContextWindow == nil || *limits.ContextWindow != 128000 || limits.MaxInputTokens == nil || *limits.MaxInputTokens != 120000 || limits.MaxOutputTokens == nil || *limits.MaxOutputTokens != 8000 {
		t.Fatalf("unexpected limits: %#v", limits)
	}
}

func TestPublicAddressRejectsPrivateRanges(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1"} {
		if isPublicIP(value) {
			t.Fatalf("private address %s was accepted", value)
		}
	}
}

func TestProbeTargetDoesNotDuplicateVersionPrefix(t *testing.T) {
	target, err := probeTarget("https://example.com/v1", "/v1/responses")
	if err != nil || target != "https://example.com/v1/responses" {
		t.Fatalf("unexpected target %q, err=%v", target, err)
	}
	target, err = probeTarget("https://api.beichenapi.com/v1", "/v1beta/models/gemini-fixture:embedContent")
	if err != nil || target != "https://api.beichenapi.com/v1beta/models/gemini-fixture:embedContent" {
		t.Fatalf("unexpected Beichen native probe target %q, err=%v", target, err)
	}
}

func TestNativeProbeEndpointsUseCatalogCredential(t *testing.T) {
	service, mock, closeService := newIdentityTestService(t)
	defer closeService()
	var err error
	service.security, err = security.NewManager(nil, nil, []byte("fixture"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const key = "fixture-gemini-catalog-owner"
	ciphertext, err := service.security.EncryptCredential(key)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("x-goog-api-key") != key || r.Header.Get("Authorization") != "" {
			t.Error("native probe used the wrong authentication")
		}
		switch r.URL.Path {
		case "/v1beta/models/gemini-fixture:generateContent":
			_, _ = io.WriteString(w, `{"candidates":[]}`)
		case "/v1beta/models/gemini-fixture:streamGenerateContent":
			_, _ = io.WriteString(w, `[{"candidates":[]}]`)
		case "/v1beta/models/gemini-fixture:embedContent":
			_, _ = io.WriteString(w, `{"embedding":{"values":[0.5]}}`)
		default:
			t.Error("probe used the wrong native endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	providerID, owner := uuid.New(), uuid.New()
	expectProvider := func(base string) {
		mock.ExpectQuery(`FROM "providers"`).WithArgs(providerID).
			WillReturnRows(sqlmock.NewRows([]string{"id", "base_url"}).AddRow(providerID, base))
	}
	const catalogQuery = `FROM "provider_credentials".*"provider_model_catalog"`
	expectOwner := func() {
		expectProvider(upstream.URL)
		mock.ExpectQuery(catalogQuery).
			WillReturnRows(sqlmock.NewRows([]string{"id", "credential_ciphertext"}).AddRow(owner, ciphertext))
	}
	endpoints := []string{"generate_content", "stream_generate_content", "embed_content"}
	expectOwner()
	got, err := service.ProbeModel(context.Background(), ProbeInput{
		ProviderID: &providerID, UpstreamModelName: "models/gemini-fixture", Endpoints: endpoints,
	})
	if err != nil || len(got.Results) != len(endpoints)+1 {
		t.Fatalf("generic probe did not report the requested endpoints: %+v %v", got, err)
	}
	for i, endpoint := range endpoints {
		item := got.Results[i+1]
		if item.Endpoint != endpoint || item.HTTPStatus != 200 || item.Error != "" {
			t.Fatalf("generic probe failed for %s: %+v", endpoint, item)
		}
	}
	expectOwner()
	step, err := service.ProbeModelBindingRoute(context.Background(), BindingRouteProbeInput{
		ProviderID: providerID, UpstreamModelName: "gemini-fixture", Endpoint: string(catalog.BindingRouteProbeStreamGenerateContent),
	})
	if err != nil || !step.OK || step.Adapter != modelbinding.AdapterGoogleGeminiV1beta {
		t.Fatalf("binding probe did not return native success: %+v %v", step, err)
	}

	for _, preflight := range []bool{false, true} {
		expectProvider("https://api.beichenapi.com/v1")
		mock.ExpectQuery(catalogQuery + `.*"catalog_source"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		if preflight {
			_, err = service.ProbeModelBindingRoute(context.Background(), BindingRouteProbeInput{
				ProviderID: providerID, UpstreamModelName: "gemini-unlisted", Endpoint: "embed_content",
			})
		} else {
			_, err = service.ProbeModel(context.Background(), ProbeInput{
				ProviderID: &providerID, UpstreamModelName: "gemini-unlisted", Endpoints: []string{"embed_content"},
			})
		}
		if domain.ErrorCode(err) != domain.CodeValidation || !strings.Contains(err.Error(), "待核") {
			t.Fatalf("probe did not stop at missing ownership: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
