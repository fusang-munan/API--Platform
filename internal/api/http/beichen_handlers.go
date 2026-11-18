package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) beichenOverview(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	view, err := s.service.Beichen().Overview(c.Request.Context(), providerID, c.Query("day"))
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
		CredentialID string `json:"credential_id"`
		Kind         string `json:"kind"`
		CSV          string `json:"csv"`
	}
	if !bindBeichenJSON(c, &input, false) {
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
