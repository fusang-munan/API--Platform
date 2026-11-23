package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

type RouteProbe struct {
	BaseURL    string
	Credential string
	Client     *http.Client
}

const bindingProbeTimeout = 8 * time.Second

type BindingRouteProbeEndpoint string

const (
	BindingRouteProbeChatCompletions   BindingRouteProbeEndpoint = "chat_completions"
	BindingRouteProbeResponses         BindingRouteProbeEndpoint = "responses"
	BindingRouteProbeMessages          BindingRouteProbeEndpoint = "messages"
	BindingRouteProbeImagesGenerations BindingRouteProbeEndpoint = "images_generations"
	BindingRouteProbeGenerateContent   BindingRouteProbeEndpoint = "generate_content"
)

var bindingRouteProbeOrder = []BindingRouteProbeEndpoint{
	BindingRouteProbeChatCompletions,
	BindingRouteProbeResponses,
	BindingRouteProbeMessages,
	BindingRouteProbeImagesGenerations,
}

// ProbeUsage contains native protocol counts. Missing counters remain nil.
type ProbeUsage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens *int64
}

type RouteProbeStep struct {
	Usage       ProbeUsage                `json:"-"`
	Endpoint    BindingRouteProbeEndpoint `json:"endpoint"`
	Path        string                    `json:"path"`
	Adapter     modelbinding.Adapter      `json:"adapter"`
	HTTPStatus  int                       `json:"http_status"`
	OK          bool                      `json:"ok"`
	DurationMS  int64                     `json:"duration_ms"`
	Error       string                    `json:"error,omitempty"`
	FailureCode string                    `json:"-"`
}

func TokenProbeEndpoint(protocol string) (BindingRouteProbeEndpoint, bool) {
	switch modelbinding.Adapter(protocol) {
	case modelbinding.AdapterOpenaiCompatible:
		return BindingRouteProbeChatCompletions, true
	case modelbinding.AdapterOpenaiResponses:
		return BindingRouteProbeResponses, true
	case modelbinding.AdapterAnthropic:
		return BindingRouteProbeMessages, true
	case modelbinding.AdapterGoogleGeminiV1beta:
		return BindingRouteProbeGenerateContent, true
	default:
		return "", false
	}
}

func BindingRouteProbeOrder() []BindingRouteProbeEndpoint {
	out := make([]BindingRouteProbeEndpoint, len(bindingRouteProbeOrder))
	copy(out, bindingRouteProbeOrder)
	return out
}

// BindingRouteProbeEndpointsForModel returns the endpoints worth probing for a model.
// Chat models skip /v1/images/generations to avoid slow upstream failures blocking saves.
func BindingRouteProbeEndpointsForModel(modelID string) []BindingRouteProbeEndpoint {
	if IsImageGenerationModel(modelID) {
		return []BindingRouteProbeEndpoint{BindingRouteProbeImagesGenerations}
	}
	return []BindingRouteProbeEndpoint{
		BindingRouteProbeChatCompletions,
		BindingRouteProbeResponses,
		BindingRouteProbeMessages,
	}
}

func probeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func NewRouteProbe(baseURL, credential string) *RouteProbe {
	return &RouteProbe{
		BaseURL:    strings.TrimSpace(baseURL),
		Credential: strings.TrimSpace(credential),
		Client:     probeHTTPClient(bindingProbeTimeout),
	}
}

type bindingProbeRequest struct {
	path      string
	body      []byte
	adapter   modelbinding.Adapter
	anthropic bool
}

func bindingProbeRequests(model string) ([]bindingProbeRequest, error) {
	chatBody, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with OK."}},
		"max_tokens": 1,
	})
	if err != nil {
		return nil, err
	}
	responsesBody, err := json.Marshal(map[string]any{
		"model":             model,
		"input":             "Reply with OK.",
		"max_output_tokens": 1,
	})
	if err != nil {
		return nil, err
	}
	messagesBody, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with OK."}},
	})
	if err != nil {
		return nil, err
	}
	imagesBody, err := json.Marshal(map[string]any{
		"model":  model,
		"prompt": "probe",
		"size":   "256x256",
		"n":      1,
	})
	if err != nil {
		return nil, err
	}
	return []bindingProbeRequest{
		{path: "/v1/chat/completions", body: chatBody, adapter: modelbinding.AdapterOpenaiCompatible},
		{path: "/v1/responses", body: responsesBody, adapter: modelbinding.AdapterOpenaiResponses},
		{path: "/v1/messages", body: messagesBody, adapter: modelbinding.AdapterAnthropic, anthropic: true},
		{path: "/v1/images/generations", body: imagesBody, adapter: modelbinding.AdapterOpenaiImages},
	}, nil
}

func bindingProbeRequestForEndpoint(model string, endpoint BindingRouteProbeEndpoint) (bindingProbeRequest, error) {
	if endpoint == BindingRouteProbeGenerateContent {
		body, err := json.Marshal(map[string]any{
			"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "Reply with OK."}}}},
			"generationConfig": map[string]int{"maxOutputTokens": 1},
		})
		path := "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent"
		return bindingProbeRequest{path: path, body: body, adapter: modelbinding.AdapterGoogleGeminiV1beta}, err
	}
	requests, err := bindingProbeRequests(model)
	if err != nil {
		return bindingProbeRequest{}, err
	}
	switch endpoint {
	case BindingRouteProbeChatCompletions:
		return requests[0], nil
	case BindingRouteProbeResponses:
		return requests[1], nil
	case BindingRouteProbeMessages:
		return requests[2], nil
	case BindingRouteProbeImagesGenerations:
		return requests[3], nil
	default:
		return bindingProbeRequest{}, fmt.Errorf("unknown binding route probe endpoint %q", endpoint)
	}
}

func (p *RouteProbe) ProbeStep(ctx context.Context, upstreamModelID string, endpoint BindingRouteProbeEndpoint) (RouteProbeStep, error) {
	return p.probeStep(ctx, upstreamModelID, endpoint, false)
}

// ProbeTokenStep makes exactly one capped request and verifies that a 2xx
// response is a generation response for the protocol. Upstream error bodies
// and transport descriptions are never returned from this path.
func (p *RouteProbe) ProbeTokenStep(ctx context.Context, upstreamModelID, protocol string) (RouteProbeStep, error) {
	endpoint, ok := TokenProbeEndpoint(protocol)
	if !ok {
		return RouteProbeStep{}, errors.New("protocol does not support a one-token probe")
	}
	return p.probeStep(ctx, upstreamModelID, endpoint, true)
}

func (p *RouteProbe) probeStep(ctx context.Context, upstreamModelID string, endpoint BindingRouteProbeEndpoint, validateResponse bool) (RouteProbeStep, error) {
	upstreamModelID = strings.TrimSpace(upstreamModelID)
	if upstreamModelID == "" {
		return RouteProbeStep{}, errors.New("upstream model id is required")
	}
	if p == nil || p.BaseURL == "" || p.Credential == "" {
		return RouteProbeStep{}, errors.New("route probe is not configured")
	}
	item, err := bindingProbeRequestForEndpoint(upstreamModelID, endpoint)
	if err != nil {
		return RouteProbeStep{}, err
	}
	step := RouteProbeStep{Endpoint: endpoint, Path: item.path, Adapter: item.adapter}
	client := p.Client
	if client == nil {
		client = NewRouteProbe(p.BaseURL, p.Credential).Client
	}
	target, err := probeTargetURL(probeBaseForRequest(p.BaseURL, item.anthropic), item.path)
	if err != nil {
		step.Error = err.Error()
		step.FailureCode = "network_error"
		return step, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(item.body))
	if err != nil {
		return RouteProbeStep{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if validateResponse {
		req.Header.Set("X-Nebula-Probe", "1")
		req.Header.Set("User-Agent", "Nebula-Probe/1.0")
	}
	if item.anthropic {
		req.Header.Set("x-api-key", p.Credential)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if item.adapter == modelbinding.AdapterGoogleGeminiV1beta {
		req.Header.Set("x-goog-api-key", p.Credential)
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Credential)
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		step.DurationMS = time.Since(started).Milliseconds()
		step.FailureCode = probeTransportError(err)
		if validateResponse {
			step.Error = step.FailureCode
		} else {
			step.Error = err.Error()
		}
		return step, nil
	}
	defer resp.Body.Close()
	step.HTTPStatus = resp.StatusCode
	step.OK = probeEndpointSuccess(resp.StatusCode)
	if !step.OK {
		step.FailureCode = "http_error"
		step.Error = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	}
	if validateResponse {
		const maxBody = 64 << 10
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		var fields map[string]json.RawMessage
		if err == nil && len(body) <= maxBody && json.Unmarshal(body, &fields) == nil {
			raw := fields["usage"]
			if endpoint == BindingRouteProbeGenerateContent {
				raw = fields["usageMetadata"]
			}
			var counters usage.CallMetrics
			counters.MergeProtocolUsage(item.adapter, raw)
			step.Usage = ProbeUsage{InputTokens: counters.PromptTokens, OutputTokens: counters.CompletionTokens,
				CacheReadTokens: counters.CacheHitTokens, CacheWriteTokens: counters.CacheWriteTokens}
		}
		if step.OK {
			switch {
			case err != nil:
				step.FailureCode = probeTransportError(err)
			case !validTokenProbeResponse(fields, endpoint):
				step.FailureCode = "invalid_response"
			}
			step.OK = step.FailureCode == ""
			step.Error = step.FailureCode
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	}
	step.DurationMS = time.Since(started).Milliseconds()
	return step, nil
}

func probeTransportError(err error) string {
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "network_error"
}

func validTokenProbeResponse(fields map[string]json.RawMessage, endpoint BindingRouteProbeEndpoint) bool {
	if fields == nil ||
		len(fields["error"]) > 0 && string(fields["error"]) != "null" {
		return false
	}
	var field string
	switch endpoint {
	case BindingRouteProbeChatCompletions:
		field = "choices"
	case BindingRouteProbeResponses:
		status := string(fields["status"])
		if status == `"failed"` || status == `"cancelled"` {
			return false
		}
		field = "output"
	case BindingRouteProbeMessages:
		field = "content"
	case BindingRouteProbeGenerateContent:
		field = "candidates"
	}
	value := fields[field]
	return len(value) > 0 && value[0] == '['
}

func (p *RouteProbe) Discover(ctx context.Context, upstreamModelID string) ([]modelbinding.Adapter, error) {
	upstreamModelID = strings.TrimSpace(upstreamModelID)
	if upstreamModelID == "" {
		return nil, errors.New("upstream model id is required")
	}
	if p == nil || p.BaseURL == "" || p.Credential == "" {
		return nil, errors.New("route probe is not configured")
	}
	client := p.Client
	if client == nil {
		client = NewRouteProbe(p.BaseURL, p.Credential).Client
	}
	endpoints := BindingRouteProbeEndpointsForModel(upstreamModelID)
	seen := map[modelbinding.Adapter]struct{}{}
	result := make([]modelbinding.Adapter, 0, len(endpoints))
	for _, endpoint := range endpoints {
		item, err := bindingProbeRequestForEndpoint(upstreamModelID, endpoint)
		if err != nil {
			return nil, err
		}
		target, err := probeTargetURL(probeBaseForRequest(p.BaseURL, item.anthropic), item.path)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(item.body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if item.anthropic {
			req.Header.Set("x-api-key", p.Credential)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+p.Credential)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if !probeEndpointSuccess(resp.StatusCode) {
			continue
		}
		if _, ok := seen[item.adapter]; ok {
			continue
		}
		seen[item.adapter] = struct{}{}
		result = append(result, item.adapter)
	}
	return result, nil
}

func probeEndpointSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

func probeBaseForRequest(baseURL string, anthropic bool) string {
	if anthropic {
		return AnthropicUpstreamBase(baseURL)
	}
	return baseURL
}

func probeTargetURL(baseURL, requestPath string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid probe base URL")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(requestPath, "/v1/") {
		requestPath = requestPath[3:]
	}
	rawPath := strings.TrimRight(parsed.EscapedPath(), "/") + "/" + strings.TrimLeft(requestPath, "/")
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", errors.New("invalid probe path")
	}
	parsed.Path, parsed.RawPath = decoded, rawPath
	return parsed.String(), nil
}

func AdaptersFromCatalogSignals(entry ProviderEntry) []modelbinding.Adapter {
	suggestion := SuggestConfiguration(ProviderEntryItem(entry))
	switch suggestion.Category {
	case "image":
		return []modelbinding.Adapter{modelbinding.AdapterOpenaiImages}
	case "embedding":
		return []modelbinding.Adapter{modelbinding.AdapterOpenaiEmbeddings}
	case "rerank":
		return []modelbinding.Adapter{modelbinding.AdapterCohereRerankV2}
	case "audio":
		return []modelbinding.Adapter{modelbinding.AdapterOpenaiAudio}
	case "video":
		return []modelbinding.Adapter{modelbinding.AdapterOpenaiVideo}
	}
	signals := EndpointSignals(entry)
	if len(signals) == 0 {
		return nil
	}
	adapters := AdaptersFromSignals(signals...)
	if len(adapters) == 0 {
		return nil
	}
	for _, adapter := range adapters {
		if adapter == modelbinding.AdapterOpenaiImages ||
			adapter == modelbinding.AdapterOpenaiEmbeddings ||
			adapter == modelbinding.AdapterCohereRerankV2 ||
			adapter == modelbinding.AdapterOpenaiAudio ||
			adapter == modelbinding.AdapterOpenaiVideo {
			return []modelbinding.Adapter{adapter}
		}
	}
	return expandChatGatewayAdapters(entry.ID, adapters)
}

func ResolveBindingAdapters(ctx context.Context, probe *RouteProbe, entry ProviderEntry) ([]modelbinding.Adapter, error) {
	if catalogAdapters := AdaptersFromCatalogSignals(entry); len(catalogAdapters) > 0 {
		return catalogAdapters, nil
	}
	if probe != nil {
		return probe.Discover(ctx, entry.ID)
	}
	return DefaultAdaptersForModel(entry.ID), nil
}

func expandChatGatewayAdapters(modelID string, adapters []modelbinding.Adapter) []modelbinding.Adapter {
	return ExpandChatGatewayAdapters(modelID, adapters)
}
