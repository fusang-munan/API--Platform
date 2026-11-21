package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/cache"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymodel"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

const replayMemoryThreshold = 1 << 20

const defaultAnthropicVersion = "2023-06-01"

type Config struct {
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	MaxRequestBytes       int64
	ResourceRouteTTL      time.Duration
	AllowedOrigin         string
}

type Gateway struct {
	db       *ent.Client
	cache    *cache.Store
	security *security.Manager
	usage    *usage.Store
	client   *http.Client
	dialer   *websocket.Dialer
	config   Config
}

type route struct {
	Binding      *ent.ModelBinding
	Provider     *ent.Provider
	Credential   string
	CredentialID uuid.UUID
}

func NewGateway(db *ent.Client, usageStore *usage.Store, cacheStore *cache.Store, securityManager *security.Manager, cfg Config) *Gateway {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   cfg.ConnectTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &Gateway{
		db: db, usage: usageStore, cache: cacheStore, security: securityManager,
		client: &http.Client{Transport: transport},
		dialer: &websocket.Dialer{
			Proxy:             http.ProxyFromEnvironment,
			HandshakeTimeout:  cfg.ResponseHeaderTimeout,
			EnableCompression: true,
		},
		config: cfg,
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isWebSocketUpgrade(r) && (r.URL.Path == "/v1/realtime" || r.URL.Path == "/v1/responses") {
		g.serveWebSocket(w, r)
		return
	}
	if !supportedRoute(r.Method, r.URL.Path) {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "route_not_found", gatewayErrorMessage(http.StatusNotFound, "route_not_found", "Route not found."), "")
		return
	}
	if isGeminiPath(r.URL.Path) && r.URL.Query().Has("key") {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "API keys are not accepted in query parameters.")
		return
	}
	key, err := g.authenticate(r)
	if err != nil {
		writeProtocolError(w, r.URL.Path, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Invalid API key.", "")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		g.serveModels(w, r, key)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/codex/model_catalog" {
		g.serveCodexModelCatalog(w, r, key)
		return
	}
	var replay *replayBody
	if r.Body != nil {
		replay, err = captureBody(r.Body, g.config.MaxRequestBytes)
		if err != nil {
			writeProtocolError(w, r.URL.Path, http.StatusBadRequest, "invalid_request_error", "invalid_request_body", err.Error(), "")
			return
		}
		defer replay.Close()
	}

	adapter := adapterForPath(r.URL.Path)
	if adapter == modelbinding.AdapterAnthropic {
		ensureAnthropicVersion(r.Header)
	}
	if resourceType, resourceID, ok := routedResource(r.Method, r.URL.Path); ok {
		routes, routeErr := g.resourceRoutes(r.Context(), key.ID, resourceType, resourceID, adapter)
		if routeErr != nil || len(routes) == 0 {
			writeProtocolError(w, r.URL.Path, http.StatusNotFound, "invalid_request_error", "resource_not_found", "The requested upstream resource is unavailable.", "")
			return
		}
		g.proxyHTTP(w, r, replay, routes, nil)
		return
	}

	modelID, extractErr := extractRequestedModel(r.URL.Path, r.Header.Get("Content-Type"), replay)
	if extractErr != nil {
		writeProtocolError(w, r.URL.Path, http.StatusBadRequest, "invalid_request_error", "invalid_model", extractErr.Error(), "model")
		return
	}
	var routes []route
	if r.URL.Path == "/v1/messages" {
		routes, err = g.routesForMessages(r.Context(), key.ID, modelID)
	} else {
		routes, err = g.routesForModel(r.Context(), key.ID, modelID, adapter)
	}
	if err != nil || len(routes) == 0 {
		g.writeModelProtocolRejection(w, r, key.ID, modelID, adapter)
		return
	}
	prompt, promptSource := extractInputSnapshot(r.URL.Path, r.Header.Get("Content-Type"), replay)
	prompt, usageCorrelation := turnCorrelation(prompt, extractCallCorrelation(r.URL.Path, r.Header.Get("Content-Type"), replay, r.Header), uuid.Nil)
	reservation, reserveErr := g.usage.Reserve(r.Context(), key.ID, routes[0].Binding.ModelID, requestIDFromContext(r), string(adapter), r.URL.Path, prompt, promptSource, usageCorrelation, beichenReservationRoutes(routes)...)
	if reserveErr != nil {
		writeModelCallRejection(w, r.URL.Path, reserveErr)
		return
	}
	g.proxyHTTP(w, r, replay, routes, &reservation)
}

func (g *Gateway) routesForMessages(ctx context.Context, keyID uuid.UUID, modelID string) ([]route, error) {
	return g.routesForModel(ctx, keyID, modelID, modelbinding.AdapterAnthropic)
}

func requestIDFromContext(r *http.Request) string {
	if value := usage.RequestID(r.Context()); value != "" {
		return value
	}
	return uuid.Must(uuid.NewV7()).String()
}

func (g *Gateway) authenticate(r *http.Request) (*ent.APIKey, error) {
	return authenticateAPIKey(r, func(raw string) (*ent.APIKey, error) {
		hash := g.security.HashAPIKey(raw)
		key, err := g.db.APIKey.Query().Where(
			apikey.KeyHashEQ(hash),
			apikey.StatusEQ(apikey.StatusActive),
		).Only(r.Context())
		if err != nil {
			return nil, errors.New("invalid API key")
		}
		return key, nil
	})
}

// authenticateAPIKey accepts the protocol-native header first and then the
// Bearer header. When a client sends both, a stale or unrelated value in one
// header must not prevent the other valid credential from being used.
func authenticateAPIKey(r *http.Request, verify func(string) (*ent.APIKey, error)) (*ent.APIKey, error) {
	candidates := apiKeyCandidates(r)
	if len(candidates) == 0 {
		return nil, errors.New("missing API key")
	}
	var lastErr error
	for _, raw := range candidates {
		key, err := verify(raw)
		if err == nil && key != nil {
			return key, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("invalid API key")
	}
	return nil, lastErr
}

func apiKeyCandidates(r *http.Request) []string {
	values := make([]string, 0, 3)
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		for _, existing := range values {
			if existing == raw {
				return
			}
		}
		values = append(values, raw)
	}
	if r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/models" {
		add(r.Header.Get("x-api-key"))
	}
	if isGeminiPath(r.URL.Path) {
		add(r.Header.Get("x-goog-api-key"))
	}
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		add(authorization[7:])
	}
	return values
}

func ensureAnthropicVersion(header http.Header) {
	if strings.TrimSpace(header.Get("anthropic-version")) == "" {
		header.Set("anthropic-version", defaultAnthropicVersion)
	}
}

type listedKeyModel struct {
	row          *ent.Model
	availability catalog.ModelAvailability
}

func (g *Gateway) listKeyModels(r *http.Request, keyID uuid.UUID) ([]listedKeyModel, error) {
	ctx := r.Context()
	rows, err := g.db.Model.Query().Where(
		entmodel.HasAPIKeyModelsWith(apikeymodel.APIKeyIDEQ(keyID)),
	).Order(ent.Asc(entmodel.FieldModelID)).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	protocols, err := catalog.LoadPublishedProtocols(ctx, g.db, ids, g.security)
	if err != nil {
		return nil, err
	}
	includeUnavailable := r.URL.Query().Get("include_unavailable") == "1"
	models := make([]listedKeyModel, 0, len(rows))
	for _, row := range rows {
		availability := catalog.SummarizeModelAvailability(row.ModelID, row.Status, protocols[row.ID])
		if includeUnavailable || availability.Availability != catalog.AvailabilityMaintenance {
			models = append(models, listedKeyModel{row: row, availability: availability})
		}
	}
	return models, nil
}

func (g *Gateway) serveModels(w http.ResponseWriter, r *http.Request, key *ent.APIKey) {
	w.Header().Set("Cache-Control", "no-store")
	models, err := g.listKeyModels(r, key.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "server_error", "dependency_unavailable", "Model catalog is unavailable.", "")
		return
	}
	type item struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
		catalog.ModelAvailability
	}
	data := make([]item, 0, len(models))
	for _, model := range models {
		data = append(data, item{
			ID: model.row.ModelID, Object: "model", Created: model.row.CreatedAt.Unix(), OwnedBy: "nebula",
			ModelAvailability: model.availability,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) modelProtocolRejection(ctx context.Context, keyID uuid.UUID, modelID string, requested modelbinding.Adapter) (int, map[string]any) {
	fields := map[string]any{"type": "invalid_request_error", "code": "model_not_allowed", "param": "model", "message": gatewayMessageModelNotAllowed}
	model, err := g.db.Model.Query().Where(
		entmodel.ModelIDEQ(modelID),
		entmodel.HasAPIKeyModelsWith(apikeymodel.APIKeyIDEQ(keyID)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return http.StatusBadRequest, fields
	}
	var options map[uuid.UUID][]catalog.ProtocolCapability
	if err == nil {
		options, err = catalog.LoadPublishedProtocols(ctx, g.db, []uuid.UUID{model.ID}, g.security)
	}
	if err != nil {
		fields["type"], fields["code"], fields["message"] = "server_error", "dependency_unavailable", "暂时无法读取模型线路，请稍后重试。"
		return http.StatusServiceUnavailable, fields
	}

	summary := catalog.SummarizeModelAvailability(model.ModelID, model.Status, options[model.ID])
	paths := make(map[string]string, len(summary.Protocols))
	names := make([]string, 0, len(summary.Protocols))
	for _, protocol := range summary.Protocols {
		endpoint := catalog.EndpointForProtocol(protocol)
		paths[protocol] = endpoint.Path
		names = append(names, endpoint.Label)
	}
	fields["supported_protocols"] = summary.Protocols
	fields["available_protocols"] = summary.AvailableProtocols
	fields["recommended_protocol"] = summary.RecommendedProtocol
	fields["requested_protocol"], fields["protocol_paths"] = requested, paths

	if summary.Availability == catalog.AvailabilityMaintenance || summary.RecommendedProtocol == nil || *summary.RecommendedProtocol == string(requested) {
		reason := summary.AvailabilityReason
		if reason == "" {
			reason = "该协议的线路暂不可用，请稍后重试或联系老师检查绑定。"
		}
		fields["type"], fields["code"], fields["message"] = "server_error", "model_unavailable", reason
		return http.StatusServiceUnavailable, fields
	}
	endpoint := catalog.EndpointForProtocol(*summary.RecommendedProtocol)
	instruction := "如果客户端自动加 /v1，Base URL 填学校网关根地址。如果客户端只追加接口名，Base URL 以 /v1 结尾。"
	switch modelbinding.Adapter(*summary.RecommendedProtocol) {
	case modelbinding.AdapterGoogleGeminiV1beta, modelbinding.AdapterCohereRerankV2:
		instruction = "Base URL 填学校网关根地址，按完整路径使用相应版本。"
	}
	fields["code"] = "protocol_mismatch"
	fields["message"] = fmt.Sprintf("该模型支持 %s。请改用 %s，完整请求路径为 %s。%s",
		strings.Join(names, "、"), endpoint.Label, endpoint.Path, instruction)
	return http.StatusBadRequest, fields
}

func (g *Gateway) writeModelProtocolRejection(w http.ResponseWriter, r *http.Request, keyID uuid.UUID, modelID string, requested modelbinding.Adapter) {
	status, fields := g.modelProtocolRejection(r.Context(), keyID, modelID, requested)
	path := r.URL.Path
	code := fields["code"].(string)
	if fields["supported_protocols"] == nil {
		writeProtocolError(w, path, status, fields["type"].(string), code, fields["message"].(string), "model")
		return
	}
	switch {
	case path == "/v1/messages":
		delete(fields, "param")
		if status == http.StatusServiceUnavailable {
			fields["type"] = "api_error"
		}
		writeJSON(w, status, map[string]any{"type": "error", "error": fields})
	case isGeminiPath(path):
		delete(fields, "type")
		delete(fields, "param")
		fields["code"], fields["status"], fields["reason"] = status, geminiStatus(status), code
		writeJSON(w, status, map[string]any{"error": fields})
	case path == "/v2/rerank":
		delete(fields, "type")
		delete(fields, "param")
		writeJSON(w, status, fields)
	default:
		writeJSON(w, status, map[string]any{"error": fields})
	}
}

func (g *Gateway) serveCodexModelCatalog(w http.ResponseWriter, r *http.Request, key *ent.APIKey) {
	w.Header().Set("Cache-Control", "no-store")
	models, err := g.listKeyModels(r, key.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "server_error", "dependency_unavailable", "Model catalog is unavailable.", "")
		return
	}
	modelIDs := make([]string, 0, len(models))
	displayNames := make(map[string]string, len(models))
	for _, model := range models {
		row := model.row
		modelIDs = append(modelIDs, row.ModelID)
		if name := strings.TrimSpace(row.DisplayName); name != "" {
			displayNames[row.ModelID] = name
		}
	}
	payload, err := catalog.MarshalCodexModelCatalog(modelIDs, displayNames)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "server_error", "dependency_unavailable", "Model catalog is unavailable.", "")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (g *Gateway) routesForModel(
	ctx context.Context,
	keyID uuid.UUID,
	modelID string,
	adapter modelbinding.Adapter,
) ([]route, error) {
	allowed, err := g.db.APIKeyModel.Query().Where(
		apikeymodel.APIKeyIDEQ(keyID),
		apikeymodel.HasModelWith(
			entmodel.ModelIDEQ(modelID),
			entmodel.StatusEQ(entmodel.StatusActive),
		),
	).WithModel().Only(ctx)
	if err != nil || allowed.Edges.Model == nil {
		return nil, errors.New("model is not allowed")
	}
	bindings, err := g.db.ModelBinding.Query().Where(
		modelbinding.ModelIDEQ(allowed.ModelID),
		modelbinding.AdapterEQ(adapter),
		modelbinding.StatusEQ(modelbinding.StatusActive),
		modelbinding.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
	).WithProvider().Order(
		ent.Asc(modelbinding.FieldPriority),
		ent.Asc(modelbinding.FieldID),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	return g.decryptRoutes(ctx, bindings), nil
}

func (g *Gateway) resourceRoutes(ctx context.Context, keyID uuid.UUID, resourceType, resourceID string, adapter modelbinding.Adapter) ([]route, error) {
	bindingIDRaw, err := g.cache.GetGatewayResourceRoute(ctx, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	bindingID, err := uuid.Parse(bindingIDRaw)
	if err != nil {
		return nil, err
	}
	binding, err := g.db.ModelBinding.Query().Where(
		modelbinding.IDEQ(bindingID),
		modelbinding.AdapterEQ(adapter),
		modelbinding.StatusEQ(modelbinding.StatusActive),
		modelbinding.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
		modelbinding.HasModelWith(
			entmodel.StatusEQ(entmodel.StatusActive),
			entmodel.HasAPIKeyModelsWith(apikeymodel.APIKeyIDEQ(keyID)),
		),
	).WithProvider().Only(ctx)
	if err != nil {
		return nil, err
	}
	return g.decryptRoutes(ctx, []*ent.ModelBinding{binding}), nil
}

func (g *Gateway) decryptRoutes(ctx context.Context, bindings []*ent.ModelBinding) []route {
	routes := make([]route, 0, len(bindings))
	for _, binding := range bindings {
		upstream := binding.Edges.Provider
		if upstream == nil || upstream.Status != provider.StatusActive {
			continue
		}
		credential, credentialID, err := catalog.ResolveBindingCredential(ctx, g.db, g.security, binding)
		if err != nil || credential == "" {
			continue
		}
		routes = append(routes, route{Binding: binding, Provider: upstream, Credential: credential, CredentialID: credentialID})
	}
	return routes
}

func (g *Gateway) upstreamRequest(original *http.Request, replay *replayBody, candidate route, adjustments ...func(map[string]json.RawMessage) error) (*http.Request, func(), error) {
	requestPath, err := upstreamPath(original.URL.Path, candidate.Binding.UpstreamModelName, candidate.Binding.Adapter)
	if err != nil {
		return nil, func() {}, err
	}
	target, err := joinUpstreamURL(catalog.UpstreamBaseForAdapter(candidate.Provider.BaseURL, candidate.Binding.Adapter), requestPath, original.URL.RawQuery)
	if err != nil {
		return nil, func() {}, err
	}
	body, contentType, contentLength, cleanup, err := rewriteUpstreamBody(
		original.Header.Get("Content-Type"), replay, candidate.Binding.UpstreamModelName, candidate.Binding.Adapter, candidate.Provider.BaseURL, original.URL.Path, adjustments...,
	)
	if err != nil {
		return nil, func() {}, err
	}
	request, err := http.NewRequestWithContext(original.Context(), original.Method, target, body)
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		cleanup()
		return nil, func() {}, err
	}
	request.Header = original.Header.Clone()
	removeHopByHop(request.Header)
	if isBeichenAPI(candidate.Provider.BaseURL) {
		for header := range request.Header {
			if strings.EqualFold(header, beichenReceiptHeader) {
				delete(request.Header, header)
			}
		}
	}
	request.Header.Del("Content-Length")
	request.Header.Del("Authorization")
	request.Header.Del("x-api-key")
	request.Header.Del("x-goog-api-key")
	if candidate.Binding.Adapter == modelbinding.AdapterAnthropic {
		ensureAnthropicVersion(request.Header)
		request.Header.Set("x-api-key", candidate.Credential)
	} else if candidate.Binding.Adapter == modelbinding.AdapterGoogleGeminiV1beta {
		request.Header.Set("x-goog-api-key", candidate.Credential)
	} else {
		request.Header.Set("Authorization", "Bearer "+candidate.Credential)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	// DeepSeek mixes compressed bytes into deepseek-flash SSE when the client
	// offers gzip or brotli. An uncompressed stream keeps data: lines intact for
	// the harness and for usage parsing. Leaving this header unset would let
	// Go's transport add gzip on its own.
	request.Header.Set("Accept-Encoding", "identity")
	request.Host = request.URL.Host
	request.ContentLength = contentLength
	return request, cleanup, nil
}

func (g *Gateway) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/responses" {
		g.serveResponsesWebSocket(w, r)
		return
	}
	g.serveRealtimeWebSocket(w, r)
}

func (g *Gateway) serveRealtimeWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("api_key") != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "api_key_query_forbidden", "API keys are not accepted in query parameters.", "")
		return
	}
	key, err := g.authenticateRealtime(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", gatewayErrorMessage(http.StatusUnauthorized, "invalid_api_key", "Invalid API key."), "")
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model"))
	if modelID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model", "model query parameter is required.", "model")
		return
	}
	routes, err := g.routesForModel(r.Context(), key.ID, modelID, modelbinding.AdapterOpenaiRealtime)
	if err != nil || len(routes) == 0 {
		g.writeModelProtocolRejection(w, r, key.ID, modelID, modelbinding.AdapterOpenaiRealtime)
		return
	}
	var upstream *websocket.Conn
	var selected route
	for _, candidate := range routes {
		query := r.URL.Query()
		query.Set("model", candidate.Binding.UpstreamModelName)
		target, err := joinUpstreamURL(candidate.Provider.BaseURL, r.URL.Path, query.Encode())
		if err != nil {
			if isBeichenAPI(candidate.Provider.BaseURL) {
				writeOpenAIError(w, http.StatusBadGateway, "server_error", "upstream_unavailable", "Unable to prepare the Realtime upstream.", "")
				return
			}
			continue
		}
		target = strings.Replace(target, "https://", "wss://", 1)
		target = strings.Replace(target, "http://", "ws://", 1)
		headers := upstreamWebSocketHeaders(r.Header, candidate.Credential)
		if isBeichenAPI(candidate.Provider.BaseURL) {
			g.serveBeichenRealtime(w, r, candidate, key.ID, target, headers)
			return
		}
		upstream, _, err = g.dialer.DialContext(r.Context(), target, headers)
		if err == nil {
			selected = candidate
			break
		}
	}
	if upstream == nil {
		writeOpenAIError(w, http.StatusBadGateway, "server_error", "upstream_unavailable", "No WebSocket provider is available.", "")
		return
	}
	defer upstream.Close()
	upgrader := websocket.Upgrader{CheckOrigin: g.validWebSocketOrigin}
	client, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	client.SetReadLimit(g.config.MaxRequestBytes)
	upstream.SetReadLimit(g.config.MaxRequestBytes)
	proxyRealtimeWebSocket(client, upstream, selected.callRoute(), g, key.ID, selected.Binding.ModelID, uuid.Nil)
}

func (g *Gateway) serveResponsesWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("api_key") != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "api_key_query_forbidden", "API keys are not accepted in query parameters.", "")
		return
	}
	key, err := g.authenticateRealtime(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", gatewayErrorMessage(http.StatusUnauthorized, "invalid_api_key", "Invalid API key."), "")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: g.validWebSocketOrigin}
	client, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	client.SetReadLimit(g.config.MaxRequestBytes)
	messageType, firstMessage, err := client.ReadMessage()
	if err != nil {
		return
	}
	modelID, err := responsesWebSocketModel(firstMessage)
	if err != nil {
		_ = client.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "invalid_model", "message": err.Error()}})
		return
	}
	routes, err := g.routesForModel(r.Context(), key.ID, modelID, modelbinding.AdapterOpenaiResponses)
	if err != nil || len(routes) == 0 {
		_, fields := g.modelProtocolRejection(r.Context(), key.ID, modelID, modelbinding.AdapterOpenaiResponses)
		_ = client.WriteJSON(map[string]any{"type": "error", "error": fields})
		return
	}
	var upstream *websocket.Conn
	var rewritten []byte
	var selected route
	for _, candidate := range routes {
		rewritten, err = rewriteResponsesWebSocketModel(firstMessage, candidate.Binding.UpstreamModelName)
		if err != nil {
			return
		}
		target, joinErr := joinUpstreamURL(candidate.Provider.BaseURL, "/v1/responses", r.URL.RawQuery)
		if joinErr != nil {
			continue
		}
		target = strings.Replace(target, "https://", "wss://", 1)
		target = strings.Replace(target, "http://", "ws://", 1)
		headers := upstreamWebSocketHeaders(r.Header, candidate.Credential)
		upstream, _, err = g.dialer.DialContext(r.Context(), target, headers)
		if err == nil {
			selected = candidate
			break
		}
	}
	if upstream == nil {
		_ = client.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"type": "server_error", "code": "upstream_unavailable", "message": "No Responses WebSocket provider is available."}})
		return
	}
	defer upstream.Close()
	upstream.SetReadLimit(g.config.MaxRequestBytes)
	if isBeichenAPI(selected.Provider.BaseURL) && messageType != websocket.TextMessage {
		return
	}
	wsPrompt, wsPromptSource := extractPromptFromJSON(firstMessage)
	wsPrompt, usageCorrelation := turnCorrelation(wsPrompt, extractCallCorrelationFromJSON(firstMessage), uuid.Nil)
	reservation, reserveErr := g.usage.Reserve(r.Context(), key.ID, selected.Binding.ModelID, requestIDFromContext(r), string(modelbinding.AdapterOpenaiResponses), "/v1/responses", wsPrompt, wsPromptSource, usageCorrelation, beichenReservationRoutes([]route{selected})...)
	if reserveErr != nil {
		_ = client.WriteJSON(modelCallErrorEvent(reserveErr))
		return
	}
	if isBeichenAPI(selected.Provider.BaseURL) {
		g.proxyBeichenWebSocket(r.Context(), client, upstream, selected, key.ID, reservation, rewritten)
		return
	}
	rejection, err := g.sendLegacyWebSocketCall(r.Context(), upstream, reservation, selected.callRoute(), messageType, rewritten)
	if rejection != nil {
		_ = client.WriteJSON(modelCallErrorEvent(rejection))
		return
	}
	if err != nil {
		return
	}
	proxyResponsesWebSockets(r.Context(), client, upstream, selected.callRoute(), g, key.ID, selected.Binding.ModelID, reservation)
}

func (g *Gateway) validWebSocketOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" || origin == g.config.AllowedOrigin {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}

func upstreamWebSocketHeaders(source http.Header, credential string) http.Header {
	headers := source.Clone()
	removeHopByHop(headers)
	for _, name := range []string{
		"Authorization", "Sec-WebSocket-Key", "Sec-WebSocket-Version",
		"Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol",
	} {
		headers.Del(name)
	}
	headers.Set("Authorization", "Bearer "+credential)
	return headers
}

func responsesWebSocketModel(message []byte) (string, error) {
	var payload struct {
		Type  string `json:"type"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(message, &payload); err != nil {
		return "", errors.New("invalid response.create message")
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Type != "response.create" || payload.Model == "" || len(payload.Model) > 256 {
		return "", errors.New("response.create with model is required")
	}
	return payload.Model, nil
}

func rewriteResponsesWebSocketModel(message []byte, upstreamModel string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(message, &payload); err != nil {
		return nil, errors.New("invalid response.create message")
	}
	var messageType string
	if err := json.Unmarshal(payload["type"], &messageType); err != nil || messageType != "response.create" {
		return nil, errors.New("response.create message is required")
	}
	model, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = model
	return json.Marshal(payload)
}

func (g *Gateway) sendLegacyWebSocketCall(ctx context.Context, upstream *websocket.Conn, r usage.Reservation, selected usage.CallRoute, messageType int, data []byte) (rejection, writeErr error) {
	started := time.Now()
	attempt, err := g.usage.StartAttempt(ctx, r, selected, 0)
	if err != nil {
		_ = g.finalizeCall(ctx, r, false, "dispatch_rejected", "Unable to authorize upstream dispatch.", selected, usage.CallMetrics{})
		return err, nil
	}
	if err = upstream.WriteMessage(messageType, data); err != nil {
		const message = "Unable to send upstream WebSocket message."
		g.finishAttempt(ctx, attempt, started, false, nil, "connection", message)
		_ = g.usage.Finalize(ctx, r, false, "outcome_unknown", "connection", message, selected, usage.CallMetrics{})
		return nil, err
	}
	g.markCallSent(ctx, r.CallID)
	g.finishAttempt(ctx, attempt, started, true, nil, "", "")
	_ = g.finalizeCall(ctx, r, true, "", "", selected, usage.CallMetrics{})
	return nil, nil
}

func proxyResponsesWebSockets(ctx context.Context, client, upstream *websocket.Conn, selected usage.CallRoute, gateway *Gateway, keyID, modelID uuid.UUID, first usage.Reservation) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	activeTurnID := first.TurnID
	var clientWriteMu sync.Mutex
	var observationMu sync.Mutex
	observation := legacyResponsesObservation{callID: first.CallID, seen: make(map[string]struct{})}
	record := func(callID uuid.UUID, metrics usage.CallMetrics) {
		if callID != uuid.Nil {
			gateway.completeCallMetrics(ctx, callID, metrics)
		}
	}
	go func() {
		for {
			messageType, data, err := client.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			if messageType == websocket.TextMessage {
				var envelope struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(data, &envelope) == nil && envelope.Type == "response.create" {
					wsPrompt, wsPromptSource := extractPromptFromJSON(data)
					wsPrompt, usageCorrelation := turnCorrelation(wsPrompt, extractCallCorrelationFromJSON(data), activeTurnID)
					reservation, reserveErr := gateway.usage.Reserve(ctx, keyID, modelID, uuid.Must(uuid.NewV7()).String(), string(modelbinding.AdapterOpenaiResponses), "/v1/responses", wsPrompt, wsPromptSource, usageCorrelation)
					if reserveErr != nil {
						clientWriteMu.Lock()
						_ = client.WriteJSON(modelCallErrorEvent(reserveErr))
						clientWriteMu.Unlock()
						continue
					}
					activeTurnID = reservation.TurnID
					data, err = rewriteResponsesWebSocketModel(data, selected.UpstreamModelName)
					if err != nil {
						errs <- err
						return
					}
					observationMu.Lock()
					rejection, err := gateway.sendLegacyWebSocketCall(ctx, upstream, reservation, selected, messageType, data)
					if rejection == nil && err == nil {
						if observation.ambiguous || observation.callID != uuid.Nil {
							// Several possible calls for response.created. Keep
							// legacy charging and preserve only tied counters.
							observation.ambiguous = true
							record(observation.callID, observation.metrics.finished(true))
							observation.callID = uuid.Nil
							record(reservation.CallID, usage.CallMetrics{State: usage.UsageMissing, UsageReason: "response_id_unconfirmed"})
						} else {
							observation.callID, observation.responseID, observation.metrics = reservation.CallID, "", ResponseMetrics{}
						}
					}
					observationMu.Unlock()
					if rejection != nil {
						clientWriteMu.Lock()
						_ = client.WriteJSON(modelCallErrorEvent(rejection))
						clientWriteMu.Unlock()
						continue
					}
					if err != nil {
						errs <- err
						return
					}
					continue
				}
			}
			if err := upstream.WriteMessage(messageType, data); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, data, err := upstream.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			var callID uuid.UUID
			var metrics usage.CallMetrics
			if messageType == websocket.TextMessage {
				observationMu.Lock()
				callID, metrics = observation.observe(data)
				observationMu.Unlock()
			}
			clientWriteMu.Lock()
			err = client.WriteMessage(messageType, data)
			clientWriteMu.Unlock()
			record(callID, metrics)
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	<-errs
	cancel()
	_ = client.Close()
	_ = upstream.Close()
	<-errs
	record(observation.callID, observation.metrics.finished(true))
}

type legacyResponsesObservation struct {
	callID     uuid.UUID
	responseID string
	metrics    ResponseMetrics
	ambiguous  bool
	seen       map[string]struct{}
}

func (o *legacyResponsesObservation) observe(data []byte) (uuid.UUID, usage.CallMetrics) {
	if o.callID == uuid.Nil {
		return uuid.Nil, usage.CallMetrics{}
	}
	adapter := modelbinding.AdapterOpenaiResponses
	payload := jsonObject(data)
	event, id := rawString(payload["type"]), responsesEventID(payload)
	if event == "response.created" && o.responseID == "" && id != "" && len(o.seen) < 4096 {
		if _, duplicate := o.seen[id]; duplicate {
			return uuid.Nil, usage.CallMetrics{}
		}
		o.responseID = id
		o.seen[id] = struct{}{}
	}
	if id == "" || id != o.responseID {
		return uuid.Nil, usage.CallMetrics{}
	}
	if !responsesTerminalEvent(event) {
		o.metrics.applyPayload(adapter, payload)
		return uuid.Nil, usage.CallMetrics{}
	}
	o.metrics.applyUsageSnapshot(adapter, payload)
	status := rawString(jsonObject(payload["response"])["status"])
	interrupted := (event != "response.completed" && event != "response.done") || (status != "" && status != "completed")
	callID := o.callID
	o.callID = uuid.Nil
	return callID, o.metrics.finished(interrupted).ClassifyBeichenUsage(string(adapter))
}

func proxyRealtimeWebSocket(client, upstream *websocket.Conn, selected usage.CallRoute, gateway *Gateway, keyID, modelID, sessionTurnID uuid.UUID) {
	errs := make(chan error, 2)
	activeTurnID := sessionTurnID
	var clientWriteMu sync.Mutex
	go func() {
		for {
			messageType, data, err := client.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			if messageType == websocket.TextMessage {
				var event struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(data, &event) == nil && event.Type == "response.create" {
					wsPrompt, wsPromptSource := extractPromptFromJSON(data)
					wsPrompt, usageCorrelation := turnCorrelation(wsPrompt, extractCallCorrelationFromJSON(data), activeTurnID)
					reservation, reserveErr := gateway.usage.Reserve(context.Background(), keyID, modelID, uuid.Must(uuid.NewV7()).String(), string(modelbinding.AdapterOpenaiRealtime), "/v1/realtime", wsPrompt, wsPromptSource, usageCorrelation)
					if reserveErr != nil {
						clientWriteMu.Lock()
						_ = client.WriteJSON(modelCallErrorEvent(reserveErr))
						clientWriteMu.Unlock()
						continue
					}
					activeTurnID = reservation.TurnID
					rejection, err := gateway.sendLegacyWebSocketCall(context.Background(), upstream, reservation, selected, messageType, data)
					if rejection != nil {
						clientWriteMu.Lock()
						_ = client.WriteJSON(modelCallErrorEvent(rejection))
						clientWriteMu.Unlock()
						continue
					}
					if err != nil {
						errs <- err
						return
					}
					continue
				}
			}
			if err := upstream.WriteMessage(messageType, data); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, data, err := upstream.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			clientWriteMu.Lock()
			err = client.WriteMessage(messageType, data)
			clientWriteMu.Unlock()
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	<-errs
}

func (g *Gateway) authenticateRealtime(r *http.Request) (*ent.APIKey, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	raw := ""
	if len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		raw = strings.TrimSpace(authorization[7:])
	}
	if raw == "" {
		for _, protocol := range websocket.Subprotocols(r) {
			if strings.HasPrefix(protocol, "nebula-api-key.") {
				raw = strings.TrimPrefix(protocol, "nebula-api-key.")
				break
			}
		}
	}
	if raw == "" {
		return nil, errors.New("missing API key")
	}
	hash := g.security.HashAPIKey(raw)
	return g.db.APIKey.Query().Where(
		apikey.KeyHashEQ(hash),
		apikey.StatusEQ(apikey.StatusActive),
	).Only(r.Context())
}

func proxyWebSockets(left, right *websocket.Conn, observeUpstream func(int, []byte), renewLease func()) {
	done := make(chan struct{}, 2)
	copyFrames := func(dst, src *websocket.Conn, observe func(int, []byte)) {
		defer func() { done <- struct{}{} }()
		for {
			messageType, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if observe != nil {
				observe(messageType, data)
			}
			if err := dst.WriteMessage(messageType, data); err != nil {
				return
			}
		}
	}
	go copyFrames(right, left, nil)
	go copyFrames(left, right, observeUpstream)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			renewLease()
		case <-done:
			deadline := time.Now().Add(time.Second)
			message := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = left.WriteControl(websocket.CloseMessage, message, deadline)
			_ = right.WriteControl(websocket.CloseMessage, message, deadline)
			_ = left.Close()
			_ = right.Close()
			<-done
			return
		}
	}
}

func supportedRoute(method, requestPath string) bool {
	switch {
	case method == http.MethodGet && requestPath == "/v1/realtime":
		return true
	case method == http.MethodGet && requestPath == "/v1/models":
		return true
	case method == http.MethodGet && requestPath == "/v1/codex/model_catalog":
		return true
	case method == http.MethodPost && requestPath == "/v1/chat/completions":
		return true
	case method == http.MethodPost && requestPath == "/v1/completions":
		return true
	case method == http.MethodPost && requestPath == "/v1/responses":
		return true
	case method == http.MethodPost && requestPath == "/v1/responses/compact":
		return true
	case method == http.MethodPost && requestPath == "/v1/embeddings":
		return true
	case method == http.MethodPost && requestPath == "/v2/rerank":
		return true
	case method == http.MethodPost && requestPath == "/v1/images/generations":
		return true
	case method == http.MethodPost && requestPath == "/v1/images/edits":
		return true
	case method == http.MethodPost && requestPath == "/v1/images/variations":
		return true
	case method == http.MethodPost && requestPath == "/v1/audio/transcriptions":
		return true
	case method == http.MethodPost && requestPath == "/v1/audio/translations":
		return true
	case method == http.MethodPost && requestPath == "/v1/audio/speech":
		return true
	case method == http.MethodPost && requestPath == "/v1/videos":
		return true
	case method == http.MethodGet && isVideoResourcePath(requestPath):
		return true
	case method == http.MethodGet && isVideoContentPath(requestPath):
		return true
	case method == http.MethodPost && isVideoRemixPath(requestPath):
		return true
	case method == http.MethodPost && requestPath == "/v1/moderations":
		return true
	case method == http.MethodPost && requestPath == "/v1/messages":
		return true
	case method == http.MethodPost && isGeminiPath(requestPath):
		return true
	default:
		return false
	}
}

func adapterForPath(requestPath string) modelbinding.Adapter {
	return catalog.ResolveAdapterFromRequestPath(requestPath)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func isVideoResourcePath(requestPath string) bool {
	if !strings.HasPrefix(requestPath, "/v1/videos/") || isVideoContentPath(requestPath) || isVideoRemixPath(requestPath) {
		return false
	}
	return validResourceID(strings.TrimPrefix(requestPath, "/v1/videos/"))
}

func isVideoContentPath(requestPath string) bool {
	if !strings.HasSuffix(requestPath, "/content") {
		return false
	}
	return validResourceID(strings.TrimSuffix(strings.TrimPrefix(requestPath, "/v1/videos/"), "/content"))
}

func isVideoRemixPath(requestPath string) bool {
	if !strings.HasSuffix(requestPath, "/remix") {
		return false
	}
	return validResourceID(strings.TrimSuffix(strings.TrimPrefix(requestPath, "/v1/videos/"), "/remix"))
}

func validResourceID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "/")
}

func createdResourceType(method, requestPath string) (string, bool) {
	if method == http.MethodPost && requestPath == "/v1/videos" {
		return "video", true
	}
	return "", false
}

func routedResource(method, requestPath string) (string, string, bool) {
	if adapterForPath(requestPath) != modelbinding.AdapterOpenaiVideo {
		return "", "", false
	}
	if method == http.MethodGet && isVideoResourcePath(requestPath) {
		return "video", strings.TrimPrefix(requestPath, "/v1/videos/"), true
	}
	if method == http.MethodGet && isVideoContentPath(requestPath) {
		return "video", strings.TrimSuffix(strings.TrimPrefix(requestPath, "/v1/videos/"), "/content"), true
	}
	if method == http.MethodPost && isVideoRemixPath(requestPath) {
		return "video", strings.TrimSuffix(strings.TrimPrefix(requestPath, "/v1/videos/"), "/remix"), true
	}
	return "", "", false
}

func isGeminiPath(requestPath string) bool {
	if !strings.HasPrefix(requestPath, "/v1beta/models/") {
		return false
	}
	for _, operation := range []string{":generateContent", ":streamGenerateContent", ":embedContent", ":batchEmbedContents"} {
		if strings.HasSuffix(requestPath, operation) && len(strings.TrimSuffix(strings.TrimPrefix(requestPath, "/v1beta/models/"), operation)) > 0 {
			return true
		}
	}
	return false
}

func extractRequestedModel(requestPath, contentType string, replay *replayBody) (string, error) {
	if isGeminiPath(requestPath) {
		value := strings.TrimPrefix(requestPath, "/v1beta/models/")
		if index := strings.LastIndex(value, ":"); index >= 0 {
			value = value[:index]
		}
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || strings.Contains(value, "/") {
			return "", errors.New("model is required")
		}
		return value, nil
	}
	return extractModel(contentType, replay)
}

func extractNestedJSONModel(contentType string, replay *replayBody, objectField string) (string, error) {
	if replay == nil {
		return "", errors.New("request body is required")
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return "", errors.New("Content-Type must be application/json")
	}
	reader, err := replay.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return "", errors.New("invalid JSON body")
	}
	var nested struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(payload[objectField], &nested); err != nil {
		return "", fmt.Errorf("%s.model is required", objectField)
	}
	nested.Model = strings.TrimSpace(nested.Model)
	if nested.Model == "" || len(nested.Model) > 256 {
		return "", fmt.Errorf("%s.model is required", objectField)
	}
	return nested.Model, nil
}

func extractRealtimeCallModel(contentType string, replay *replayBody) (string, error) {
	if replay == nil {
		return "", errors.New("request body is required")
	}
	reader, err := replay.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return "", errors.New("Content-Type must be multipart/form-data")
	}
	form := multipart.NewReader(reader, params["boundary"])
	for {
		part, err := form.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("invalid multipart body")
		}
		if part.FormName() != "session" {
			_ = part.Close()
			continue
		}
		var session struct {
			Model string `json:"model"`
		}
		err = json.NewDecoder(io.LimitReader(part, 1<<20)).Decode(&session)
		_ = part.Close()
		session.Model = strings.TrimSpace(session.Model)
		if err != nil || session.Model == "" || len(session.Model) > 256 {
			return "", errors.New("model is required in session")
		}
		return session.Model, nil
	}
	return "", errors.New("session is required")
}

func upstreamPath(requestPath, upstreamModel string, adapter modelbinding.Adapter) (string, error) {
	switch adapter {
	case modelbinding.AdapterCohereRerankV2:
		return "/v2/rerank", nil
	case modelbinding.AdapterGoogleGeminiV1beta:
		if !isGeminiPath(requestPath) {
			return "", errors.New("invalid Gemini route")
		}
		operation := requestPath[strings.LastIndex(requestPath, ":"):]
		return "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(upstreamModel, "models/")) + operation, nil
	default:
		return requestPath, nil
	}
}

func rewriteUpstreamBody(contentType string, replay *replayBody, upstreamModel string, adapter modelbinding.Adapter, baseURL, requestPath string, adjustments ...func(map[string]json.RawMessage) error) (
	io.ReadCloser,
	string,
	int64,
	func(),
	error,
) {
	if adapter != modelbinding.AdapterGoogleGeminiV1beta {
		if adapter == modelbinding.AdapterOpenaiRealtime && replay != nil {
			mediaType, _, _ := mime.ParseMediaType(contentType)
			if mediaType == "application/json" || mediaType == "multipart/form-data" {
				return rewriteRealtimeBody(contentType, replay, upstreamModel)
			}
		}
		if adapter == modelbinding.AdapterAnthropic && deepseekHost(baseURL) {
			adjustments = append(adjustments, repairDeepSeekAnthropicThinking)
		} else if adapter == modelbinding.AdapterOpenaiCompatible && requestPath == "/v1/chat/completions" && isBeichenAPI(baseURL) {
			adjustments = append(adjustments, requestBeichenStreamUsage)
		}
		return rewriteRequestBodyAdjusted(contentType, replay, upstreamModel, adjustments...)
	}
	if replay == nil {
		return nil, contentType, 0, func() {}, nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return nil, "", 0, func() {}, errors.New("Gemini requests require application/json")
	}
	source, err := replay.Open()
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	defer source.Close()
	var payload map[string]any
	if err := json.NewDecoder(source).Decode(&payload); err != nil {
		return nil, "", 0, func() {}, errors.New("invalid JSON body")
	}
	qualifiedModel := "models/" + strings.TrimPrefix(upstreamModel, "models/")
	if _, exists := payload["model"]; exists {
		payload["model"] = qualifiedModel
	}
	if requests, ok := payload["requests"].([]any); ok {
		for _, raw := range requests {
			if request, ok := raw.(map[string]any); ok {
				request["model"] = qualifiedModel
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	return io.NopCloser(bytes.NewReader(body)), contentType, int64(len(body)), func() {}, nil
}

func rewriteRealtimeBody(contentType string, replay *replayBody, upstreamModel string) (io.ReadCloser, string, int64, func(), error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	if mediaType == "application/json" {
		return rewriteNestedJSONModel(contentType, replay, "session", upstreamModel)
	}
	if mediaType != "multipart/form-data" {
		return nil, "", 0, func() {}, errors.New("Realtime requests require application/json or multipart/form-data")
	}
	return rewriteMultipartJSONField(contentType, replay, "session", "model", upstreamModel)
}

func rewriteNestedJSONModel(contentType string, replay *replayBody, objectField, upstreamModel string) (io.ReadCloser, string, int64, func(), error) {
	source, err := replay.Open()
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	defer source.Close()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(source).Decode(&payload); err != nil {
		return nil, "", 0, func() {}, errors.New("invalid JSON body")
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(payload[objectField], &nested); err != nil {
		return nil, "", 0, func() {}, fmt.Errorf("%s is required", objectField)
	}
	nested["model"], err = json.Marshal(upstreamModel)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	payload[objectField], err = json.Marshal(nested)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	return io.NopCloser(bytes.NewReader(body)), contentType, int64(len(body)), func() {}, nil
}

func rewriteMultipartJSONField(contentType string, replay *replayBody, fieldName, jsonField, replacement string) (io.ReadCloser, string, int64, func(), error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", 0, func() {}, errors.New("invalid multipart Content-Type")
	}
	source, err := replay.Open()
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	file, err := os.CreateTemp("", "nebula-upstream-*")
	if err != nil {
		_ = source.Close()
		return nil, "", 0, func() {}, err
	}
	filePath := file.Name()
	cleanupOnError := func() {
		_ = source.Close()
		_ = file.Close()
		_ = os.Remove(filePath)
	}
	writer := multipart.NewWriter(file)
	reader := multipart.NewReader(source, params["boundary"])
	found := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
		target, err := writer.CreatePart(cloneMIMEHeader(part.Header))
		if err != nil {
			_ = part.Close()
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
		if part.FormName() == fieldName {
			var payload map[string]json.RawMessage
			err = json.NewDecoder(io.LimitReader(part, 1<<20)).Decode(&payload)
			if err == nil {
				payload[jsonField], err = json.Marshal(replacement)
			}
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(payload)
				if err == nil {
					_, err = target.Write(encoded)
				}
			}
			found = true
		} else {
			_, err = io.Copy(target, part)
		}
		_ = part.Close()
		if err != nil {
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
	}
	_ = source.Close()
	if !found {
		cleanupOnError()
		return nil, "", 0, func() {}, fmt.Errorf("%s is required", fieldName)
	}
	if err := writer.Close(); err != nil {
		cleanupOnError()
		return nil, "", 0, func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanupOnError()
		return nil, "", 0, func() {}, err
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		_ = os.Remove(filePath)
		return nil, "", 0, func() {}, err
	}
	body, err := os.Open(filePath)
	if err != nil {
		_ = os.Remove(filePath)
		return nil, "", 0, func() {}, err
	}
	cleanup := func() {
		_ = body.Close()
		_ = os.Remove(filePath)
	}
	return body, writer.FormDataContentType(), stat.Size(), cleanup, nil
}

func extractModel(contentType string, replay *replayBody) (string, error) {
	if replay == nil {
		return "", errors.New("request body is required")
	}
	reader, err := replay.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", errors.New("invalid Content-Type")
	}
	if mediaType == "multipart/form-data" {
		boundary := params["boundary"]
		if boundary == "" {
			return "", errors.New("multipart boundary is required")
		}
		form := multipart.NewReader(reader, boundary)
		for {
			part, err := form.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", errors.New("invalid multipart body")
			}
			if part.FormName() == "model" {
				value, err := io.ReadAll(io.LimitReader(part, 257))
				_ = part.Close()
				if err != nil || len(value) > 256 {
					return "", errors.New("invalid model")
				}
				modelID := strings.TrimSpace(string(value))
				if modelID == "" {
					return "", errors.New("model is required")
				}
				return modelID, nil
			}
			_ = part.Close()
		}
		return "", errors.New("model is required")
	}
	if mediaType != "application/json" {
		return "", errors.New("Content-Type must be application/json or multipart/form-data")
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return "", errors.New("invalid JSON body")
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Model == "" || len(payload.Model) > 256 {
		return "", errors.New("model is required")
	}
	return payload.Model, nil
}

func extractInputSnapshot(requestPath, contentType string, replay *replayBody) (string, string) {
	if requestPath == "/v1/embeddings" || requestPath == "/v1/moderations" || requestPath == "/v2/rerank" || requestPath == "/v1/responses/compact" || requestPath == "/v1/images/variations" || requestPath == "/v1/audio/transcriptions" || requestPath == "/v1/audio/translations" {
		return "", ""
	}
	if replay == nil {
		return "", ""
	}
	reader, err := replay.Open()
	if err != nil {
		return "", ""
	}
	defer reader.Close()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", ""
	}
	if mediaType == "multipart/form-data" {
		form := multipart.NewReader(reader, params["boundary"])
		for {
			part, e := form.NextPart()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				return "", ""
			}
			name := part.FormName()
			if name == "prompt" || name == "input" {
				value, _ := io.ReadAll(io.LimitReader(part, 1<<20))
				_ = part.Close()
				return strings.TrimSpace(string(value)), name
			}
			_ = part.Close()
		}
		return "", ""
	}
	if mediaType != "application/json" {
		return "", ""
	}
	var payload map[string]any
	if json.NewDecoder(reader).Decode(&payload) != nil {
		return "", ""
	}
	return extractUserPromptFromPayload(payload)
}

func allTextFromJSONValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := allTextFromJSONValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		typeName, _ := v["type"].(string)
		if typeName == "message" {
			for _, key := range []string{"content", "text", "input_text"} {
				if text := allTextFromJSONValue(v[key]); text != "" {
					return text
				}
			}
			return ""
		}
		if typeName == "tool_result" || typeName == "tool_use" || typeName == "thinking" {
			return ""
		}
		if typeName != "" && typeName != "text" && typeName != "input_text" {
			return ""
		}
		for _, key := range []string{"text", "input_text", "content"} {
			if text := allTextFromJSONValue(v[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func textFromJSONValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for i := len(v) - 1; i >= 0; i-- {
			if text := textFromJSONValue(v[i]); text != "" {
				return text
			}
		}
	case map[string]any:
		for _, key := range []string{"text", "input_text", "content"} {
			if text := textFromJSONValue(v[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func rewriteRequestBody(contentType string, replay *replayBody, upstreamModel string) (
	io.ReadCloser,
	string,
	int64,
	func(),
	error,
) {
	return rewriteRequestBodyAdjusted(contentType, replay, upstreamModel)
}

func rewriteRequestBodyAdjusted(contentType string, replay *replayBody, upstreamModel string, adjustments ...func(map[string]json.RawMessage) error) (
	io.ReadCloser,
	string,
	int64,
	func(),
	error,
) {
	if replay == nil {
		return nil, contentType, 0, func() {}, nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	source, err := replay.Open()
	if err != nil {
		return nil, "", 0, func() {}, err
	}
	if mediaType == "application/json" {
		defer source.Close()
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(source).Decode(&payload); err != nil {
			return nil, "", 0, func() {}, err
		}
		modelValue, err := json.Marshal(upstreamModel)
		if err != nil {
			return nil, "", 0, func() {}, err
		}
		payload["model"] = modelValue
		for _, adjust := range adjustments {
			if err := adjust(payload); err != nil {
				return nil, "", 0, func() {}, err
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, "", 0, func() {}, err
		}
		return io.NopCloser(bytes.NewReader(body)), contentType, int64(len(body)), func() {}, nil
	}
	if mediaType != "multipart/form-data" {
		_ = source.Close()
		return nil, "", 0, func() {}, errors.New("unsupported request body type")
	}
	boundary := params["boundary"]
	if boundary == "" {
		_ = source.Close()
		return nil, "", 0, func() {}, errors.New("multipart boundary is required")
	}
	file, err := os.CreateTemp("", "nebula-upstream-*")
	if err != nil {
		_ = source.Close()
		return nil, "", 0, func() {}, err
	}
	filePath := file.Name()
	cleanupOnError := func() {
		_ = source.Close()
		_ = file.Close()
		_ = os.Remove(filePath)
	}
	writer := multipart.NewWriter(file)
	reader := multipart.NewReader(source, boundary)
	modelWritten := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
		header := cloneMIMEHeader(part.Header)
		target, err := writer.CreatePart(header)
		if err != nil {
			_ = part.Close()
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
		if part.FormName() == "model" {
			_, err = io.WriteString(target, upstreamModel)
			modelWritten = true
		} else {
			_, err = io.Copy(target, part)
		}
		_ = part.Close()
		if err != nil {
			cleanupOnError()
			return nil, "", 0, func() {}, err
		}
	}
	_ = source.Close()
	if !modelWritten {
		cleanupOnError()
		return nil, "", 0, func() {}, errors.New("model is required")
	}
	if err := writer.Close(); err != nil {
		cleanupOnError()
		return nil, "", 0, func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanupOnError()
		return nil, "", 0, func() {}, err
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		_ = os.Remove(filePath)
		return nil, "", 0, func() {}, err
	}
	body, err := os.Open(filePath)
	if err != nil {
		_ = os.Remove(filePath)
		return nil, "", 0, func() {}, err
	}
	cleanup := func() {
		_ = body.Close()
		_ = os.Remove(filePath)
	}
	return body, writer.FormDataContentType(), stat.Size(), cleanup, nil
}

func cloneMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	result := make(textproto.MIMEHeader, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func joinUpstreamURL(baseURL, requestPath, rawQuery string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Host == "" {
		return "", errors.New("invalid provider URL")
	}
	suffix := requestPath
	for _, version := range []string{"/v1beta", "/v2", "/v1"} {
		if strings.HasSuffix(base.Path, version) && strings.HasPrefix(suffix, version+"/") {
			suffix = strings.TrimPrefix(suffix, version)
			break
		}
	}
	base.Path = strings.TrimRight(base.Path, "/") + suffix
	base.RawQuery = rawQuery
	return base.String(), nil
}

func copyUpstreamResponse(w http.ResponseWriter, response *http.Response) error {
	defer response.Body.Close()
	copyHeaders(w.Header(), response.Header)
	removeHopByHop(w.Header())
	w.WriteHeader(response.StatusCode)
	buffer := make([]byte, 32<<10)
	controller := http.NewResponseController(w)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if writeErr := forwardResponseChunk(w, controller, buffer[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func removeHopByHop(headers http.Header) {
	for _, header := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		headers.Del(header)
	}
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

const (
	gatewayMessageInvalidAPIKey   = "API Key 无效或已停用。请打开 Nebula 控制台「申请密钥」页确认 Key 状态，重新领取或重新生成 Key 后，再更新客户端配置。"
	gatewayMessageForbidden       = "当前 Key 无权执行此操作。请确认使用的是本人已生效的 Key，并在 Nebula 控制台「模型广场」核对该模型的授权。"
	gatewayMessageNotFound        = "请求的接口或资源不存在。请检查客户端 Base URL 不要重复带 /v1，并确认该模型仍在模型广场中。"
	gatewayMessageModelNotAllowed = "当前 Key 未包含该模型。请打开 Nebula 控制台「模型广场」，把该模型添加到当前 Key，然后用同一把 Key 重试。"
)

func gatewayErrorMessage(status int, code, fallback string) string {
	if code == "model_not_allowed" {
		return gatewayMessageModelNotAllowed
	}
	switch status {
	case http.StatusUnauthorized:
		return gatewayMessageInvalidAPIKey
	case http.StatusForbidden:
		return gatewayMessageForbidden
	case http.StatusNotFound:
		return gatewayMessageNotFound
	default:
		return fallback
	}
}

func writeProtocolError(w http.ResponseWriter, requestPath string, status int, errorType, code, message, parameter string) {
	message = gatewayErrorMessage(status, code, message)
	if requestPath == "/v1/messages" {
		writeAnthropicError(w, status, errorType, message)
		return
	}
	if isGeminiPath(requestPath) {
		writeGeminiError(w, status, geminiStatus(status), message)
		return
	}
	if requestPath == "/v2/rerank" {
		writeJSON(w, status, map[string]any{"message": message})
		return
	}
	writeOpenAIError(w, status, errorType, code, message, parameter)
}

func writeGeminiError(w http.ResponseWriter, status int, errorStatus, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": status, "message": message, "status": errorStatus,
	}})
}

func geminiStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, errorType, code, message, parameter string) {
	var parameterValue any
	if parameter != "" {
		parameterValue = parameter
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"message": message, "type": errorType, "param": parameterValue, "code": code,
	}})
}

func writeAnthropicError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error", "error": map[string]any{"type": errorType, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

type replayBody struct {
	memory []byte
	path   string
}

func captureBody(source io.ReadCloser, maxBytes int64) (*replayBody, error) {
	defer source.Close()
	var memory bytes.Buffer
	limited := io.LimitReader(source, maxBytes+1)
	if _, err := io.CopyN(&memory, limited, replayMemoryThreshold+1); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(memory.Len()) > maxBytes {
		return nil, errors.New("request body is too large")
	}
	if memory.Len() <= replayMemoryThreshold {
		remaining, err := io.ReadAll(limited)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		if int64(memory.Len()+len(remaining)) > maxBytes {
			return nil, errors.New("request body is too large")
		}
		memory.Write(remaining)
		return &replayBody{memory: memory.Bytes()}, nil
	}
	file, err := os.CreateTemp("", "nebula-gateway-*")
	if err != nil {
		return nil, fmt.Errorf("create request spool: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if _, err := file.Write(memory.Bytes()); err != nil {
		cleanup()
		return nil, fmt.Errorf("write request spool: %w", err)
	}
	written, err := io.Copy(file, limited)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("write request spool: %w", err)
	}
	if int64(memory.Len())+written > maxBytes {
		cleanup()
		return nil, errors.New("request body is too large")
	}
	if err := file.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close request spool: %w", err)
	}
	return &replayBody{path: file.Name()}, nil
}

func (r *replayBody) Open() (io.ReadCloser, error) {
	if r.path == "" {
		return io.NopCloser(bytes.NewReader(r.memory)), nil
	}
	return os.Open(r.path)
}

func (r *replayBody) Close() error {
	if r.path == "" {
		return nil
	}
	return os.Remove(r.path)
}
