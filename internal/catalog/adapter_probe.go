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
)

var bindingRouteProbeOrder = []BindingRouteProbeEndpoint{
	BindingRouteProbeChatCompletions,
	BindingRouteProbeResponses,
	BindingRouteProbeMessages,
	BindingRouteProbeImagesGenerations,
}

type RouteProbeStep struct {
	Endpoint   BindingRouteProbeEndpoint `json:"endpoint"`
	Path       string                    `json:"path"`
	Adapter    modelbinding.Adapter      `json:"adapter"`
	HTTPStatus int                       `json:"http_status"`
	OK         bool                      `json:"ok"`
	DurationMS int64                     `json:"duration_ms"`
	Error      string                    `json:"error,omitempty"`
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
	target, err := probeTargetURL(p.BaseURL, item.path)
	if err != nil {
		step.Error = err.Error()
		return step, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(item.body))
	if err != nil {
		return RouteProbeStep{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if item.anthropic {
		req.Header.Set("x-api-key", p.Credential)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Credential)
	}
	started := time.Now()
	resp, err := client.Do(req)
	step.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		step.Error = err.Error()
		return step, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	step.HTTPStatus = resp.StatusCode
	step.OK = probeEndpointSuccess(resp.StatusCode)
	if !step.OK {
		step.Error = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	}
	return step, nil
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
		target, err := probeTargetURL(p.BaseURL, item.path)
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
	parsed.Path = strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
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
