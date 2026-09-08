package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) studentOrganizations(c *gin.Context) {
	rows, err := s.service.StudentOrganizations(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) studentProjects(c *gin.Context) {
	organizationID, ok := pathID(c, "organization_id")
	if !ok {
		return
	}
	rows, err := s.service.StudentProjects(c.Request.Context(), organizationID)
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) studentModels(c *gin.Context) {
	view, err := s.service.StudentModels(c.Request.Context(), identity(c).UserID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) studentCommonModels(c *gin.Context) {
	rows, err := s.service.StudentCommonModels(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) studentModelCatalog(c *gin.Context) {
	page, pageSize := 1, 20
	if raw := c.Query("page"); raw != "" {
		if _, err := fmt.Sscan(raw, &page); err != nil {
			writeError(c, domain.NewError(domain.CodeValidation, "invalid page"))
			return
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		if _, err := fmt.Sscan(raw, &pageSize); err != nil {
			writeError(c, domain.NewError(domain.CodeValidation, "invalid page_size"))
			return
		}
	}
	result, err := s.service.StudentCatalogSearch(c.Request.Context(), c.Query("q"), page, pageSize)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (s *Server) addStudentModel(c *gin.Context) {
	var input struct {
		ModelID string `json:"model_id" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.AddStudentModel(c.Request.Context(), identity(c).UserID, input.ModelID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) removeStudentModel(c *gin.Context) {
	var input struct {
		ModelID string `json:"model_id" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	if err := s.service.RemoveStudentModel(c.Request.Context(), identity(c).UserID, input.ModelID); err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"removed": true})
}

func (s *Server) submitAPIKey(c *gin.Context) {
	var input struct {
		Name           string `json:"name" binding:"required"`
		OrganizationID string `json:"organization_id" binding:"required"`
		ProjectID      string `json:"project_id" binding:"required"`
		Models         []struct {
			ModelID     string `json:"model_id" binding:"required"`
			DisplayName string `json:"display_name"`
		} `json:"models" binding:"required"`
		RequestedMonthlyCredits string `json:"requested_monthly_credits" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	organizationID, err := controlplane.UUID(input.OrganizationID)
	if err != nil {
		writeError(c, err)
		return
	}
	projectID, err := controlplane.UUID(input.ProjectID)
	if err != nil {
		writeError(c, err)
		return
	}
	requestedCredits, err := domain.ParseCredits(input.RequestedMonthlyCredits)
	if err != nil {
		writeError(c, err)
		return
	}
	requested := make([]controlplane.RequestedModelInput, 0, len(input.Models))
	for _, item := range input.Models {
		requested = append(requested, controlplane.RequestedModelInput{ModelID: item.ModelID, DisplayName: item.DisplayName})
	}
	view, err := s.service.SubmitAPIKey(c.Request.Context(), identity(c).UserID, controlplane.SubmitAPIKeyInput{
		Name: input.Name, OrganizationID: organizationID, ProjectID: projectID,
		Models:                  requested,
		RequestedMonthlyCredits: requestedCredits,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) studentAPIKeys(c *gin.Context) {
	rows, err := s.service.StudentAPIKeys(c.Request.Context(), identity(c).UserID)
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) studentAPIKey(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	view, err := s.service.StudentAPIKey(c.Request.Context(), identity(c).UserID, keyID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) claimAPIKey(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	view, err := s.service.ClaimAPIKey(c.Request.Context(), identity(c).UserID, keyID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) withdrawAPIKey(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	err := s.service.WithdrawAPIKey(c.Request.Context(), identity(c).UserID, keyID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"withdrawn": true})
}

func (s *Server) regenerateAPIKey(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	view, err := s.service.RegenerateAPIKey(c.Request.Context(), identity(c).UserID, keyID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}
