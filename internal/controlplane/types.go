package controlplane

import (
	"slices"
	"strings"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

type UserView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type OrganizationView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
}

type StudentProjectView struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organization_id"`
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	Status         string  `json:"status"`
	HasMentor      bool    `json:"has_mentor"`
}

type ProjectView struct {
	ID                 string              `json:"id"`
	OrganizationID     string              `json:"organization_id"`
	Name               string              `json:"name"`
	Description        *string             `json:"description"`
	Status             string              `json:"status"`
	HasMentor          bool                `json:"has_mentor"`
	IsResponsible      bool                `json:"is_responsible,omitempty"`
	ApplicationStatus  *string             `json:"application_status,omitempty"`
	MonthlyCreditQuota string              `json:"monthly_credit_quota"`
	Mentors            []MentorSummaryView `json:"mentors,omitempty"`
}

type StudentModelPlazaView struct {
	CommonModels  []ModelView `json:"common_models"`
	LibraryModels []ModelView `json:"library_models"`
}

type ModelView struct {
	*catalog.ModelAvailability
	ModelID            string         `json:"model_id"`
	DisplayName        string         `json:"display_name"`
	Description        *string        `json:"description"`
	Category           string         `json:"category"`
	Capabilities       []string       `json:"capabilities"`
	InputModalities    []string       `json:"input_modalities"`
	OutputModalities   []string       `json:"output_modalities"`
	ContextWindow      *int           `json:"context_window"`
	MaxInputTokens     *int           `json:"max_input_tokens"`
	MaxOutputTokens    *int           `json:"max_output_tokens"`
	IsCommon           bool           `json:"is_common"`
	Status             string         `json:"status"`
	RouteReady         bool           `json:"route_ready"`
	CreditMultiplier   *string        `json:"credit_multiplier"`
	UpstreamMetadata   map[string]any `json:"upstream_metadata"`
	ProviderID         string         `json:"provider_id,omitempty"`
	ProviderName       string         `json:"provider_name,omitempty"`
	PrimaryAdapter     string         `json:"primary_adapter,omitempty"`
	SupportedProtocols []string       `json:"supported_protocols,omitempty"`
	AnthropicReady     bool           `json:"anthropic_ready"`
	ResponsesReady     bool           `json:"responses_ready"`
	CredentialID       string         `json:"credential_id,omitempty"`
	CredentialLabel    string         `json:"credential_label,omitempty"`
}

func (view *ModelView) setAvailability(availability catalog.ModelAvailability) {
	view.ModelAvailability = &availability
	view.SupportedProtocols = availability.Protocols
	view.RouteReady = len(availability.Protocols) > 0
	view.AnthropicReady = slices.Contains(availability.Protocols, string(modelbinding.AdapterAnthropic))
	view.ResponsesReady = slices.Contains(availability.Protocols, string(modelbinding.AdapterOpenaiResponses))
}

type MentorCandidateView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsMember bool   `json:"is_member"`
}

type AuditView struct {
	Action    string    `json:"action"`
	ActorRole string    `json:"actor_role"`
	Comment   *string   `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
}

type ProgressView struct {
	Current        string   `json:"current"`
	CompletedSteps []string `json:"completed_steps"`
}

type APIKeyView struct {
	ID                      string           `json:"id"`
	Name                    string           `json:"name"`
	Status                  string           `json:"status"`
	Progress                ProgressView     `json:"progress"`
	Applicant               UserView         `json:"applicant,omitempty"`
	Organization            OrganizationView `json:"organization"`
	Project                 ProjectView      `json:"project"`
	Models                  []ModelView      `json:"models"`
	RouteReady              bool             `json:"route_ready"`
	Audits                  []AuditView      `json:"audits,omitempty"`
	KeyPrefix               *string          `json:"key_prefix"`
	APIKey                  *string          `json:"api_key,omitempty"`
	ClaimedAt               *time.Time       `json:"claimed_at"`
	CreatedAt               time.Time        `json:"created_at"`
	UpdatedAt               time.Time        `json:"updated_at"`
	RequestedMonthlyCredits string           `json:"requested_monthly_credits"`
	MentorMonthlyCredits    *string          `json:"mentor_monthly_credits"`
	MonthlyCredits          *string          `json:"monthly_credits"`
	MemberMonthlyCredits    *string          `json:"member_monthly_credits,omitempty"`
	MemberUsedCredits       *string          `json:"member_used_credits,omitempty"`
	MemberPendingCredits    *string          `json:"member_pending_credits,omitempty"`
}

type SessionView struct {
	AccessToken  string   `json:"access_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    int64    `json:"expires_in"`
	RefreshToken string   `json:"-"`
	Identity     string   `json:"identity"`
	User         UserView `json:"user"`
}

type LoginChallengeView struct {
	User       UserView `json:"user"`
	Identities []string `json:"identities"`
}

type MeView struct {
	User       UserView `json:"user"`
	Identity   string   `json:"identity"`
	Identities []string `json:"identities"`
}

type MentorProjectApplicationView struct {
	ID            string      `json:"id"`
	Status        string      `json:"status"`
	Mentor        UserView    `json:"mentor"`
	Project       ProjectView `json:"project"`
	Reviewer      *UserView   `json:"reviewer,omitempty"`
	ReviewComment *string     `json:"review_comment"`
	ReviewedAt    *time.Time  `json:"reviewed_at"`
	CreatedAt     time.Time   `json:"created_at"`
}

type ProviderView struct {
	ID                   string                   `json:"id"`
	Name                 string                   `json:"name"`
	BaseURL              string                   `json:"base_url"`
	CredentialConfigured bool                     `json:"credential_configured"`
	Status               string                   `json:"status"`
	Credentials          []ProviderCredentialView `json:"credentials,omitempty"`
	CreatedAt            time.Time                `json:"created_at"`
	UpdatedAt            time.Time                `json:"updated_at"`
	LastCatalogSync      *ProviderCatalogSyncView `json:"last_catalog_sync,omitempty"`
}

type BindingView struct {
	ID                string    `json:"id"`
	ModelID           string    `json:"model_id"`
	ProviderID        string    `json:"provider_id"`
	CredentialID      *string   `json:"credential_id,omitempty"`
	CredentialLabel   string    `json:"credential_label,omitempty"`
	UpstreamModelName string    `json:"upstream_model_name"`
	Adapter           string    `json:"adapter"`
	Priority          int       `json:"priority"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func userView(user *ent.User) UserView {
	return UserView{
		ID: user.ID.String(), Name: user.Name, Email: user.Email,
		Role: string(user.Role), Status: string(user.Status), CreatedAt: user.CreatedAt,
	}
}

func organizationView(organization *ent.Organization) OrganizationView {
	return OrganizationView{
		ID: organization.ID.String(), Name: organization.Name,
		Description: organization.Description, Status: string(organization.Status),
	}
}

func studentProjectView(project *ent.Project, hasMentor bool) StudentProjectView {
	return StudentProjectView{
		ID: project.ID.String(), OrganizationID: project.OrganizationID.String(),
		Name: project.Name, Description: project.Description,
		Status: string(project.Status), HasMentor: hasMentor,
	}
}

func projectView(project *ent.Project, hasMentor bool) ProjectView {
	return ProjectView{
		ID: project.ID.String(), OrganizationID: project.OrganizationID.String(),
		Name: project.Name, Description: project.Description,
		Status: string(project.Status), HasMentor: hasMentor,
		MonthlyCreditQuota: domain.FormatCredits(project.MonthlyCreditQuotaMilli),
	}
}

func normalizeModelDescription(modelID, displayName string, description *string) *string {
	if description == nil {
		return nil
	}
	text := strings.TrimSpace(*description)
	if text == "" {
		return nil
	}
	modelID = strings.TrimSpace(modelID)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = modelID
	}
	redundant := displayName + "（" + modelID + "）"
	if strings.EqualFold(text, redundant) || strings.EqualFold(text, modelID) || strings.EqualFold(text, displayName) {
		return nil
	}
	return &text
}

func modelView(model *ent.Model) ModelView {
	view := ModelView{
		ModelID: model.ModelID, DisplayName: model.DisplayName,
		Description: normalizeModelDescription(model.ModelID, model.DisplayName, model.Description), Category: string(model.Category),
		Capabilities: model.Capabilities, InputModalities: model.InputModalities,
		OutputModalities: model.OutputModalities, ContextWindow: model.ContextWindow, MaxInputTokens: model.MaxInputTokens,
		MaxOutputTokens: model.MaxOutputTokens, IsCommon: model.IsCommon,
		Status: string(model.Status), UpstreamMetadata: model.UpstreamMetadata,
	}
	if model.CreditMultiplierMilli != nil {
		value := domain.FormatMultiplier(*model.CreditMultiplierMilli)
		view.CreditMultiplier = &value
	}
	return view
}
