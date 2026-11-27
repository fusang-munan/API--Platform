package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

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
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeError(c, domain.NewError(domain.CodeValidation, "目录查询参数编码无效"))
		return
	}
	page, err := studentCatalogPageValue(params, "page", 1)
	if err != nil {
		writeError(c, err)
		return
	}
	pageSize, err := studentCatalogPageValue(params, "page_size", 20)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := s.service.StudentCatalogSearch(c.Request.Context(), params.Get("q"), page, pageSize)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func studentCatalogPageValue(params url.Values, field string, fallback int) (int, error) {
	if !params.Has(field) {
		return fallback, nil
	}
	value, err := strconv.Atoi(params.Get(field))
	if err != nil {
		return 0, domain.NewError(domain.CodeValidation, field+" 须为整数")
	}
	return value, nil
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
		Name                    string          `json:"name" binding:"required"`
		OrganizationID          string          `json:"organization_id" binding:"required"`
		ProjectID               string          `json:"project_id" binding:"required"`
		RequestedMonthlyCredits string          `json:"requested_monthly_credits" binding:"required"`
		Models                  json.RawMessage `json:"models"` // Accepted for older clients and ignored.
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
	view, err := s.service.SubmitAPIKey(c.Request.Context(), identity(c).UserID, controlplane.SubmitAPIKeyInput{
		Name: input.Name, OrganizationID: organizationID, ProjectID: projectID,
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

func (s *Server) addStudentAPIKeyModels(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	var input struct {
		ModelIDs []string `json:"model_ids"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.AddStudentAPIKeyModels(c.Request.Context(), identity(c).UserID, keyID, input.ModelIDs)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) removeStudentAPIKeyModels(c *gin.Context) {
	keyID, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	var input struct {
		ModelIDs []string `json:"model_ids"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.RemoveStudentAPIKeyModels(c.Request.Context(), identity(c).UserID, keyID, input.ModelIDs)
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
