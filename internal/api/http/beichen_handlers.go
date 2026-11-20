package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) beichenOverview(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	view, err := s.service.Beichen().Overview(c.Request.Context(), providerID, c.Query("day"))
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, s.config.SchoolWorkspace.overview(view, identity(c), c.Request))
}

func (s *Server) beichenArchivedReceipts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	view, err := s.service.Beichen().ArchivedReceipts(c.Request.Context(), providerID, c.Query("day"), c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) syncBeichen(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok || !bindBeichenJSON(c, &struct{}{}, true) {
		return
	}
	if err := s.service.Beichen().Sync(c.Request.Context(), providerID); err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"synced": true})
}

func (s *Server) enableBeichenModel(c *gin.Context) {
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		Model       string `json:"model"`
		SchoolModel string `json:"school_model"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	model, err := s.service.Beichen().EnableModel(c.Request.Context(), provider, identity(c).UserID, input.Model, input.SchoolModel)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"model_id": model, "status": "active"})
}

func (s *Server) beichenPanelAccess(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	credential, ok := pathID(c, "credential_id")
	if !ok {
		return
	}
	if c.Request.Method == http.MethodGet {
		status, err := s.service.Beichen().PanelStatus(c.Request.Context(), provider, credential)
		if err != nil {
			writeError(c, err)
			return
		}
		respond(c, http.StatusOK, status)
		return
	}
	if !panelManagementHTTPS(c.Request, s.config.AllowedOrigin) {
		writeError(c, domain.NewError(domain.CodeValidation, "请先配置可信 HTTPS 管理入口"))
		return
	}
	var input struct {
		Enabled       *bool  `json:"enabled"`
		GroupID       int64  `json:"group_id"`
		Token         string `json:"token"`
		UpstreamKeyID *int64 `json:"upstream_key_id"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	if input.Enabled == nil {
		writeError(c, domain.NewError(domain.CodeValidation, "请指定采价授权状态"))
		return
	}
	access := beichen.PanelAccessInput{Enabled: *input.Enabled, GroupID: input.GroupID, Token: input.Token, UpstreamKeyID: input.UpstreamKeyID}
	if err := s.service.Beichen().SetPanelAccess(c.Request.Context(), provider, credential, identity(c).UserID, access); err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, beichen.PanelAccessStatus{Configured: access.Enabled, GroupID: access.GroupID, UpstreamKeyID: access.UpstreamKeyID})
}

func panelManagementHTTPS(req *http.Request, allowedOrigin string) bool {
	if req.TLS != nil {
		return true
	}
	// The frontend proxy overwrites this header. The backend is only published
	// on loopback; only the private container network may forward HTTPS.
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	peer := net.ParseIP(host)
	proto := req.Header.Values("X-Forwarded-Proto")
	return err == nil && peer.IsPrivate() && strings.HasPrefix(allowedOrigin, "https://") &&
		len(proto) == 1 && proto[0] == "https" && req.Header.Get("Origin") == allowedOrigin
}

func (s *Server) setBeichenPrice(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		CredentialID string `json:"credential_id"`
		Model        string `json:"model"`
		beichen.PriceInput
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	credentialID, err := controlplane.UUID(input.CredentialID)
	if err == nil {
		err = s.service.Beichen().SetPrice(c.Request.Context(), providerID, credentialID, identity(c).UserID, input.Model, input.PriceInput)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"saved": true})
}

func (s *Server) importBeichen(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		CredentialID     string `json:"credential_id"`
		Kind             string `json:"kind"`
		CSV              string `json:"csv"`
		FullDayConfirmed bool   `json:"full_day_confirmed"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	if input.Kind == "usage_export" && !input.FullDayConfirmed {
		writeError(c, domain.NewError(domain.CodeValidation, "请确认北辰明细覆盖一个完整的北京时间账日"))
		return
	}
	credentialID, err := controlplane.UUID(input.CredentialID)
	if err == nil {
		err = s.service.Beichen().Import(c.Request.Context(), providerID, credentialID, identity(c).UserID, input.Kind, input.CSV)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"imported": true})
}

func bindBeichenJSON(c *gin.Context, destination any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil {
		writeError(c, domain.NewError(domain.CodeValidation, "invalid request body"))
		return false
	}
	body = bytes.TrimSpace(body)
	if allowEmpty && len(body) == 0 {
		return true
	}
	if len(body) == 0 || body[0] != '{' {
		writeError(c, domain.NewError(domain.CodeValidation, "invalid request body"))
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) == nil && decoder.Decode(new(any)) == io.EOF {
		return true
	}
	writeError(c, domain.NewError(domain.CodeValidation, "invalid request body"))
	return false
}
