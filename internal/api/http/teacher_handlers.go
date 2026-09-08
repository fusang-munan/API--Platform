package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) inviteTeacher(c *gin.Context) {
	var input struct {
		Email string `json:"email" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	if err := s.service.InviteTeacher(c.Request.Context(), identity(c), input.Email); err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusAccepted, gin.H{"invited": true})
}

func (s *Server) teacherOrganizations(c *gin.Context) {
	rows, err := s.service.TeacherOrganizations(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) createOrganization(c *gin.Context) {
	var input struct {
		Name        string  `json:"name" binding:"required"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.CreateOrganization(c.Request.Context(), controlplane.OrganizationInput{
		Name: input.Name, Description: input.Description, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) updateOrganization(c *gin.Context) {
	organizationID, ok := pathID(c, "organization_id")
	if !ok {
		return
	}
	var input struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.UpdateOrganization(c.Request.Context(), organizationID, controlplane.OrganizationInput{
		Name: input.Name, Description: input.Description, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) projectMentorCandidates(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(c, domain.NewError(domain.CodeValidation, "invalid limit"))
			return
		}
		limit = parsed
	}
	cursor, err := controlplane.DecodeMentorCandidateCursor(c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	rows, nextCursor, err := s.service.ProjectMentorCandidates(
		c.Request.Context(), projectID, c.Query("q"), cursor, limit,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	respondPage(c, rows, nextCursor)
}

func (s *Server) teacherProjects(c *gin.Context) {
	var organizationID *string
	if raw := strings.TrimSpace(c.Query("organization_id")); raw != "" {
		organizationID = &raw
	}
	var parsedOrganizationID *uuid.UUID
	if organizationID != nil {
		id, err := controlplane.UUID(*organizationID)
		if err != nil {
			writeError(c, err)
			return
		}
		parsedOrganizationID = &id
	}
	var status *string
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status = &raw
	}
	rows, err := s.service.TeacherProjects(c.Request.Context(), parsedOrganizationID, status)
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) createProject(c *gin.Context) {
	var input struct {
		OrganizationID     string  `json:"organization_id" binding:"required"`
		Name               string  `json:"name" binding:"required"`
		Description        *string `json:"description"`
		Status             *string `json:"status"`
		MonthlyCreditQuota string `json:"monthly_credit_quota" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	organizationID, err := controlplane.UUID(input.OrganizationID)
	if err != nil {
		writeError(c, err)
		return
	}
	value, parseErr := domain.ParseCredits(input.MonthlyCreditQuota)
	if parseErr != nil {
		writeError(c, parseErr)
		return
	}
	quota := &value
	view, err := s.service.CreateProject(c.Request.Context(), controlplane.ProjectInput{
		OrganizationID: organizationID, Name: input.Name,
		Description: input.Description, Status: input.Status,
		MonthlyCreditQuotaMilli: quota,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) updateProject(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	var input struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.UpdateProject(c.Request.Context(), identity(c).UserID, projectID, controlplane.ProjectInput{
		Name: input.Name, Description: input.Description, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) teacherMentorApplications(c *gin.Context) {
	var status *string
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status = &raw
	}
	rows, err := s.service.TeacherMentorApplications(c.Request.Context(), status)
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) reviewMentorApplication(approve bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		applicationID, ok := pathID(c, "application_id")
		if !ok {
			return
		}
		comment, ok := bindOptionalComment(c)
		if !ok {
			return
		}
		if err := s.service.ReviewMentorApplication(
			c.Request.Context(), identity(c).UserID, applicationID, approve, comment,
		); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func (s *Server) teacherProviders(c *gin.Context) {
	rows, err := s.service.TeacherProviders(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) createProvider(c *gin.Context) {
	var input struct {
		Name       string  `json:"name" binding:"required"`
		BaseURL    string  `json:"base_url" binding:"required"`
		Credential string  `json:"credential" binding:"required"`
		Status     *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.CreateProvider(c.Request.Context(), controlplane.ProviderInput{
		Name: input.Name, BaseURL: input.BaseURL, Credential: &input.Credential, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) teacherProvider(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	view, err := s.service.TeacherProvider(c.Request.Context(), providerID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) updateProvider(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		Name       string  `json:"name"`
		BaseURL    string  `json:"base_url"`
		Credential *string `json:"credential"`
		Status     *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.UpdateProvider(c.Request.Context(), providerID, controlplane.ProviderInput{
		Name: input.Name, BaseURL: input.BaseURL, Credential: input.Credential, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) deleteProvider(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	if err := s.service.DeleteProvider(c.Request.Context(), providerID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) providerCredentials(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	rows, err := s.service.ProviderCredentials(c.Request.Context(), providerID)
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) createProviderCredential(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		Label      string  `json:"label" binding:"required"`
		GroupHint  *string `json:"group_hint"`
		Credential string  `json:"credential" binding:"required"`
		Status     *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.CreateProviderCredential(c.Request.Context(), providerID, controlplane.ProviderCredentialInput{
		Label: input.Label, GroupHint: input.GroupHint, Credential: &input.Credential, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) updateProviderCredential(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	credentialID, ok := pathID(c, "credential_id")
	if !ok {
		return
	}
	var input struct {
		Label      string  `json:"label"`
		GroupHint  *string `json:"group_hint"`
		Credential *string `json:"credential"`
		Status     *string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	view, err := s.service.UpdateProviderCredential(c.Request.Context(), providerID, credentialID, controlplane.ProviderCredentialInput{
		Label: input.Label, GroupHint: input.GroupHint, Credential: input.Credential, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) deleteProviderCredential(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	credentialID, ok := pathID(c, "credential_id")
	if !ok {
		return
	}
	if err := s.service.DeleteProviderCredential(c.Request.Context(), providerID, credentialID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) resetProviderCatalog(c *gin.Context) {
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	result, err := s.service.ResetProviderCatalog(c.Request.Context(), providerID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (s *Server) teacherProviderCatalogCandidates(c *gin.Context) {
	providerID, err := controlplane.UUID(c.Query("provider_id"))
	if err != nil {
		writeError(c, err)
		return
	}
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
	result, err := s.service.ProviderCatalogCandidates(c.Request.Context(), providerID, c.Query("q"), page, pageSize)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

type modelPayload struct {
	ModelID                string   `json:"model_id"`
	DisplayName            string   `json:"display_name"`
	Description            *string  `json:"description"`
	Category               string   `json:"category"`
	Capabilities           []string `json:"capabilities"`
	InputModalities        []string `json:"input_modalities"`
	OutputModalities       []string `json:"output_modalities"`
	ContextWindow          *int     `json:"context_window"`
	MaxInputTokens         *int     `json:"max_input_tokens"`
	MaxOutputTokens        *int     `json:"max_output_tokens"`
	IsCommon               *bool    `json:"is_common"`
	Status                 *string  `json:"status"`
	CreditMultiplier       string   `json:"credit_multiplier"`
	MultiplierChangeReason string   `json:"multiplier_change_reason"`
}

type nullableIntPayload struct {
	Set   bool
	Value *int
}

func (value *nullableIntPayload) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		value.Value = nil
		return nil
	}
	var parsed int
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	value.Value = &parsed
	return nil
}

type modelPatchPayload struct {
	DisplayName            string             `json:"display_name"`
	Description            *string            `json:"description"`
	Category               string             `json:"category"`
	Capabilities           []string           `json:"capabilities"`
	InputModalities        []string           `json:"input_modalities"`
	OutputModalities       []string           `json:"output_modalities"`
	ContextWindow          nullableIntPayload `json:"context_window"`
	MaxOutputTokens        nullableIntPayload `json:"max_output_tokens"`
	MaxInputTokens         nullableIntPayload `json:"max_input_tokens"`
	IsCommon               *bool              `json:"is_common"`
	Status                 *string            `json:"status"`
	CreditMultiplier       string             `json:"credit_multiplier"`
	MultiplierChangeReason string             `json:"multiplier_change_reason"`
}

func (payload modelPayload) input() controlplane.ModelInput {
	var multiplier *int64
	if payload.CreditMultiplier != "" {
		value, err := domain.ParseMultiplier(payload.CreditMultiplier)
		if err == nil {
			multiplier = &value
		}
	}
	return controlplane.ModelInput{
		ModelID: payload.ModelID, DisplayName: payload.DisplayName,
		Description: payload.Description, Category: payload.Category,
		Capabilities: payload.Capabilities, InputModalities: payload.InputModalities,
		OutputModalities: payload.OutputModalities, ContextWindow: payload.ContextWindow,
		MaxInputTokens:  payload.MaxInputTokens,
		MaxOutputTokens: payload.MaxOutputTokens, IsCommon: payload.IsCommon, Status: payload.Status,
		CreditMultiplierMilli: multiplier, MultiplierChangeReason: payload.MultiplierChangeReason,
	}
}

func (s *Server) teacherModels(c *gin.Context) {
	rows, err := s.service.TeacherModels(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) probeModel(c *gin.Context) {
	var input struct {
		ProviderID        string   `json:"provider_id"`
		BaseURL           string   `json:"base_url"`
		Credential        string   `json:"credential"`
		UpstreamModelName string   `json:"upstream_model_name" binding:"required"`
		Endpoints         []string `json:"endpoints" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	var providerID *uuid.UUID
	if strings.TrimSpace(input.ProviderID) != "" {
		id, err := controlplane.UUID(input.ProviderID)
		if err != nil {
			writeError(c, err)
			return
		}
		providerID = &id
	}
	result, err := s.service.ProbeModel(c.Request.Context(), controlplane.ProbeInput{ProviderID: providerID, BaseURL: input.BaseURL, Credential: input.Credential, UpstreamModelName: input.UpstreamModelName, Endpoints: input.Endpoints})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (s *Server) createModel(c *gin.Context) {
	var input modelPayload
	if !bindJSON(c, &input) {
		return
	}
	if input.ModelID == "" || input.DisplayName == "" {
		writeError(c, domain.NewError(domain.CodeValidation, "model_id and display_name are required"))
		return
	}
	if input.CreditMultiplier != "" {
		if _, err := domain.ParseMultiplier(input.CreditMultiplier); err != nil {
			writeError(c, err)
			return
		}
	}
	view, err := s.service.CreateModel(c.Request.Context(), input.input())
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) teacherModel(c *gin.Context) {
	modelID := strings.TrimSpace(c.Query("model_id"))
	if modelID == "" {
		writeError(c, domain.NewError(domain.CodeValidation, "model_id is required"))
		return
	}
	var credentialID *uuid.UUID
	if rawCredentialID := strings.TrimSpace(c.Query("credential_id")); rawCredentialID != "" {
		id, err := controlplane.UUID(rawCredentialID)
		if err != nil {
			writeError(c, err)
			return
		}
		credentialID = &id
	}
	model, bindings, err := s.service.TeacherModel(c.Request.Context(), modelID, credentialID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"model": model, "bindings": bindings})
}

func (s *Server) updateModel(c *gin.Context) {
	var input struct {
		ModelID string `json:"model_id" binding:"required"`
		modelPatchPayload
	}
	if !bindJSON(c, &input) {
		return
	}
	var multiplier *int64
	if input.CreditMultiplier != "" {
		value, err := domain.ParseMultiplier(input.CreditMultiplier)
		if err != nil {
			writeError(c, err)
			return
		}
		multiplier = &value
	}
	view, err := s.service.UpdateModel(c.Request.Context(), identity(c).UserID, input.ModelID, controlplane.ModelInput{
		DisplayName: input.DisplayName, Description: input.Description, Category: input.Category,
		Capabilities: input.Capabilities, InputModalities: input.InputModalities,
		OutputModalities: input.OutputModalities, ContextWindow: input.ContextWindow.Value,
		ContextWindowSet: input.ContextWindow.Set, MaxOutputTokens: input.MaxOutputTokens.Value,
		MaxInputTokens: input.MaxInputTokens.Value, MaxInputTokensSet: input.MaxInputTokens.Set,
		MaxOutputTokensSet: input.MaxOutputTokens.Set, IsCommon: input.IsCommon, Status: input.Status,
		CreditMultiplierMilli: multiplier, MultiplierChangeReason: input.MultiplierChangeReason,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) probeModelBindingRoute(c *gin.Context) {
	var input struct {
		ProviderID        string `json:"provider_id" binding:"required"`
		UpstreamModelName string `json:"upstream_model_name" binding:"required"`
		Endpoint          string `json:"endpoint" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	providerID, err := controlplane.UUID(input.ProviderID)
	if err != nil {
		writeError(c, err)
		return
	}
	step, err := s.service.ProbeModelBindingRoute(c.Request.Context(), controlplane.BindingRouteProbeInput{
		ProviderID: providerID, UpstreamModelName: input.UpstreamModelName, Endpoint: input.Endpoint,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, step)
}

func (s *Server) createBinding(c *gin.Context) {
	var input struct {
		ModelID           string   `json:"model_id" binding:"required"`
		ProviderID        string   `json:"provider_id" binding:"required"`
		UpstreamModelName string   `json:"upstream_model_name" binding:"required"`
		Adapter           string   `json:"adapter"`
		Priority          *int     `json:"priority"`
		Status            string   `json:"status"`
		ProbedAdapters    []string `json:"probed_adapters"`
	}
	if !bindJSON(c, &input) {
		return
	}
	providerID, err := controlplane.UUID(input.ProviderID)
	if err != nil {
		writeError(c, err)
		return
	}
	priority := 100
	if input.Priority != nil {
		priority = *input.Priority
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if input.Adapter == "" {
		input.Adapter = "openai_compatible"
	}
	view, err := s.service.CreateBinding(c.Request.Context(), input.ModelID, controlplane.BindingInput{
		ProviderID: providerID, UpstreamModelName: input.UpstreamModelName,
		Adapter: input.Adapter, Priority: priority, Status: input.Status, ProbedAdapters: input.ProbedAdapters,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusCreated, view)
}

func (s *Server) updateBinding(c *gin.Context) {
	bindingID, ok := pathID(c, "binding_id")
	if !ok {
		return
	}
	var input struct {
		UpstreamModelName string `json:"upstream_model_name"`
		Adapter           string `json:"adapter"`
		Priority          *int   `json:"priority"`
		Status            string `json:"status"`
	}
	if !bindJSON(c, &input) {
		return
	}
	priority := -1
	if input.Priority != nil {
		priority = *input.Priority
	}
	view, err := s.service.UpdateBinding(c.Request.Context(), bindingID, controlplane.BindingInput{
		UpstreamModelName: input.UpstreamModelName,
		Adapter:           input.Adapter, Priority: priority, Status: input.Status,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) teacherProjectBudgets(c *gin.Context) {
	month, err := monthQuery(c)
	if err != nil {
		writeError(c, err)
		return
	}
	rows, err := s.service.ListProjectBudgets(c.Request.Context(), month, c.Query("organization_id"), c.Query("q"))
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) updateProjectBudget(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	var input struct {
		MonthlyCreditQuota string `json:"monthly_credit_quota" binding:"required"`
	}
	if !bindJSON(c, &input) {
		return
	}
	quota, err := domain.ParseCredits(input.MonthlyCreditQuota)
	if err != nil {
		writeError(c, err)
		return
	}
	view, err := s.service.UpdateProjectBudget(c.Request.Context(), identity(c).UserID, projectID, quota, "update")
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) assignProjectMentor(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	mentorID, ok := pathID(c, "mentor_id")
	if !ok {
		return
	}
	if err := s.service.AssignProjectMentor(c.Request.Context(), projectID, mentorID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) removeProjectMentor(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	mentorID, ok := pathID(c, "mentor_id")
	if !ok {
		return
	}
	if err := s.service.RemoveProjectMentor(c.Request.Context(), projectID, mentorID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) teacherStudents(c *gin.Context) {
	rows, err := s.service.TeacherStudents(c.Request.Context(), c.Query("q"))
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}

func (s *Server) grantMentorProfile(c *gin.Context) {
	studentID, ok := pathID(c, "student_id")
	if !ok {
		return
	}
	view, err := s.service.GrantMentorProfile(c.Request.Context(), identity(c).UserID, studentID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) revokeMentorProfile(c *gin.Context) {
	studentID, ok := pathID(c, "student_id")
	if !ok {
		return
	}
	view, err := s.service.RevokeMentorProfile(c.Request.Context(), studentID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}
