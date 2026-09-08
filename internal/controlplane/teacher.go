package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprojectapplication"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/organization"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/organizationmember"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

type OrganizationInput struct {
	Name        string
	Description *string
	Status      *string
}

type ProjectInput struct {
	OrganizationID          uuid.UUID
	Name                    string
	Description             *string
	Status                  *string
	MonthlyCreditQuotaMilli *int64
	QuotaChangeReason       string
}

type ProviderInput struct {
	Name       string
	BaseURL    string
	Credential *string
	Status     *string
}

type ModelInput struct {
	ModelID                string
	DisplayName            string
	Description            *string
	Category               string
	Capabilities           []string
	InputModalities        []string
	OutputModalities       []string
	ContextWindow          *int
	ContextWindowSet       bool
	MaxInputTokens         *int
	MaxInputTokensSet      bool
	MaxOutputTokens        *int
	MaxOutputTokensSet     bool
	IsCommon               *bool
	Status                 *string
	CreditMultiplierMilli  *int64
	MultiplierChangeReason string
}

type MentorCandidateCursor struct {
	Name string    `json:"name"`
	ID   uuid.UUID `json:"id"`
}

func DecodeMentorCandidateCursor(value string) (*MentorCandidateCursor, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, domain.NewError(domain.CodeValidation, "invalid cursor")
	}
	var cursor MentorCandidateCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Name == "" || cursor.ID == uuid.Nil {
		return nil, domain.NewError(domain.CodeValidation, "invalid cursor")
	}
	return &cursor, nil
}

func encodeMentorCandidateCursor(name string, id uuid.UUID) string {
	raw, _ := json.Marshal(MentorCandidateCursor{Name: name, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

type BindingInput struct {
	ProviderID        uuid.UUID
	UpstreamModelName string
	Adapter           string
	Priority          int
	Status            string
	ProbedAdapters    []string
}

func (s *Service) TeacherOrganizations(ctx context.Context) ([]OrganizationView, error) {
	rows, err := s.db.Organization.Query().Order(ent.Asc(organization.FieldName)).All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list organizations", err)
	}
	result := make([]OrganizationView, 0, len(rows))
	for _, row := range rows {
		result = append(result, organizationView(row))
	}
	return result, nil
}

func (s *Service) CreateOrganization(ctx context.Context, input OrganizationInput) (OrganizationView, error) {
	name, err := ValidateName(input.Name, 128)
	if err != nil {
		return OrganizationView{}, err
	}
	status, err := resourceStatus(input.Status, string(organization.StatusActive))
	if err != nil {
		return OrganizationView{}, err
	}
	builder := s.db.Organization.Create().SetName(name).SetStatus(organization.Status(status))
	if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
		builder.SetDescription(strings.TrimSpace(*input.Description))
	}
	row, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return OrganizationView{}, domain.NewError(domain.CodeNameConflict, "organization name already exists")
	}
	if err != nil {
		return OrganizationView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create organization", err)
	}
	return organizationView(row), nil
}

func (s *Service) UpdateOrganization(ctx context.Context, id uuid.UUID, input OrganizationInput) (OrganizationView, error) {
	builder := s.db.Organization.UpdateOneID(id)
	if input.Name != "" {
		name, err := ValidateName(input.Name, 128)
		if err != nil {
			return OrganizationView{}, err
		}
		builder.SetName(name)
	}
	if input.Description != nil {
		if strings.TrimSpace(*input.Description) == "" {
			builder.ClearDescription()
		} else if len([]rune(*input.Description)) > 1024 {
			return OrganizationView{}, domain.NewError(domain.CodeValidation, "description is too long")
		} else {
			builder.SetDescription(strings.TrimSpace(*input.Description))
		}
	}
	if input.Status != nil {
		status, err := resourceStatus(input.Status, "")
		if err != nil {
			return OrganizationView{}, err
		}
		builder.SetStatus(organization.Status(status))
	}
	row, err := builder.Save(ctx)
	if ent.IsNotFound(err) {
		return OrganizationView{}, domain.NewError(domain.CodeNotFound, "organization not found")
	}
	if ent.IsConstraintError(err) {
		return OrganizationView{}, domain.NewError(domain.CodeNameConflict, "organization name already exists")
	}
	if err != nil {
		return OrganizationView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update organization", err)
	}
	return organizationView(row), nil
}

func (s *Service) TeacherProjects(ctx context.Context, organizationID *uuid.UUID, status *string) ([]ProjectView, error) {
	query := s.db.Project.Query().Order(ent.Asc(project.FieldName))
	if organizationID != nil {
		query.Where(project.OrganizationIDEQ(*organizationID))
	}
	if status != nil {
		parsed, err := resourceStatus(status, "")
		if err != nil {
			return nil, err
		}
		query.Where(project.StatusEQ(project.Status(parsed)))
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list projects", err)
	}
	return s.enrichTeacherProjectViews(ctx, rows)
}

func (s *Service) CreateProject(ctx context.Context, input ProjectInput) (ProjectView, error) {
	name, err := ValidateName(input.Name, 128)
	if err != nil {
		return ProjectView{}, err
	}
	orgExists, err := s.db.Organization.Query().Where(organization.IDEQ(input.OrganizationID)).Exist(ctx)
	if err != nil {
		return ProjectView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query organization", err)
	}
	if !orgExists {
		return ProjectView{}, domain.NewError(domain.CodeNotFound, "organization not found")
	}
	status, err := resourceStatus(input.Status, string(project.StatusActive))
	if err != nil {
		return ProjectView{}, err
	}
	builder := s.db.Project.Create().
		SetOrganizationID(input.OrganizationID).
		SetName(name).
		SetStatus(project.Status(status))
	if input.MonthlyCreditQuotaMilli == nil {
		return ProjectView{}, domain.NewError(domain.CodeValidation, "monthly credit quota is required")
	}
	builder.SetMonthlyCreditQuotaMilli(*input.MonthlyCreditQuotaMilli)
	if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
		builder.SetDescription(strings.TrimSpace(*input.Description))
	}
	row, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return ProjectView{}, domain.NewError(domain.CodeNameConflict, "project name already exists in this organization")
	}
	if err != nil {
		return ProjectView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create project", err)
	}
	return projectView(row, false), nil
}

func (s *Service) UpdateProject(ctx context.Context, actorID, id uuid.UUID, input ProjectInput) (ProjectView, error) {
	builder := s.db.Project.UpdateOneID(id)
	if input.Name != "" {
		name, err := ValidateName(input.Name, 128)
		if err != nil {
			return ProjectView{}, err
		}
		builder.SetName(name)
	}
	if input.Description != nil {
		if strings.TrimSpace(*input.Description) == "" {
			builder.ClearDescription()
		} else if len([]rune(*input.Description)) > 1024 {
			return ProjectView{}, domain.NewError(domain.CodeValidation, "description is too long")
		} else {
			builder.SetDescription(strings.TrimSpace(*input.Description))
		}
	}
	if input.Status != nil {
		status, err := resourceStatus(input.Status, "")
		if err != nil {
			return ProjectView{}, err
		}
		builder.SetStatus(project.Status(status))
	}
	row, err := builder.Save(ctx)
	if ent.IsNotFound(err) {
		return ProjectView{}, domain.NewError(domain.CodeNotFound, "project not found")
	}
	if ent.IsConstraintError(err) {
		return ProjectView{}, domain.NewError(domain.CodeNameConflict, "project name already exists in this organization")
	}
	if err != nil {
		return ProjectView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update project", err)
	}
	return projectView(row, false), nil
}

func (s *Service) TeacherMentorApplications(ctx context.Context, status *string) ([]MentorProjectApplicationView, error) {
	query := s.db.MentorProjectApplication.Query().Order(ent.Desc(mentorprojectapplication.FieldCreatedAt))
	if status != nil {
		parsed, err := mentorApplicationStatus(*status)
		if err != nil {
			return nil, err
		}
		query.Where(mentorprojectapplication.StatusEQ(parsed))
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list mentor project applications", err)
	}
	result := make([]MentorProjectApplicationView, 0, len(rows))
	for _, row := range rows {
		item, err := s.mentorApplication(ctx, row.ID, uuid.Nil)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) ReviewMentorApplication(
	ctx context.Context,
	teacherID, applicationID uuid.UUID,
	approve bool,
	comment string,
) error {
	comment = strings.TrimSpace(comment)
	if !approve && comment == "" {
		return domain.NewError(domain.CodeRejectionReasonRequired, "rejection reason is required")
	}
	if len([]rune(comment)) > 1000 {
		return domain.NewError(domain.CodeValidation, "comment is too long")
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "start project application transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	application, err := tx.MentorProjectApplication.Query().Where(
		mentorprojectapplication.IDEQ(applicationID),
		mentorprojectapplication.StatusEQ(mentorprojectapplication.StatusPending),
	).WithProject().Only(ctx)
	if ent.IsNotFound(err) {
		return domain.NewError(domain.CodeInvalidTransition, "project application is no longer pending")
	}
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project application", err)
	}
	status := mentorprojectapplication.StatusRejected
	if approve {
		status = mentorprojectapplication.StatusApproved
		if application.Edges.Project == nil {
			return domain.NewError(domain.CodeNotFound, "project not found")
		}
		projectMember, err := tx.ProjectMember.Query().Where(
			projectmember.ProjectIDEQ(application.ProjectID),
			projectmember.UserIDEQ(application.MentorID),
		).Exist(ctx)
		if err != nil {
			return domain.WrapError(domain.CodeDependencyUnavailable, "query mentor project membership", err)
		}
		if projectMember {
			return domain.NewError(domain.CodeMentorAlreadyProjectMember, "mentor already belongs to the project")
		}
		if err := ensureOrganizationMember(ctx, tx, application.Edges.Project.OrganizationID, application.MentorID); err != nil {
			return err
		}
		if err := ensureProjectMember(ctx, tx, application.ProjectID, application.MentorID); err != nil {
			return err
		}
	}
	now := s.now().UTC()
	update := tx.MentorProjectApplication.Update().Where(
		mentorprojectapplication.IDEQ(applicationID),
		mentorprojectapplication.StatusEQ(mentorprojectapplication.StatusPending),
	).SetStatus(status).SetReviewedBy(teacherID).SetReviewedAt(now)
	if comment != "" {
		update.SetReviewComment(comment)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "review project application", err)
	}
	if updated != 1 {
		return domain.NewError(domain.CodeInvalidTransition, "project application was reviewed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "commit project application review", err)
	}
	return nil
}

func (s *Service) TeacherProviders(ctx context.Context) ([]ProviderView, error) {
	rows, err := s.db.Provider.Query().
		WithCredentials(func(query *ent.ProviderCredentialQuery) {
			query.Order(ent.Asc(providercredential.FieldLabel))
		}).
		Order(ent.Asc(provider.FieldName)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list providers", err)
	}
	result := make([]ProviderView, 0, len(rows))
	for _, row := range rows {
		result = append(result, providerViewWithCredentials(row, row.Edges.Credentials))
	}
	return result, nil
}

func (s *Service) CreateProvider(ctx context.Context, input ProviderInput) (ProviderView, error) {
	name, err := ValidateName(input.Name, 128)
	if err != nil {
		return ProviderView{}, err
	}
	baseURL, err := ValidateBaseURL(input.BaseURL)
	if err != nil {
		return ProviderView{}, err
	}
	if input.Credential == nil {
		return ProviderView{}, domain.NewError(domain.CodeValidation, "credential is required")
	}
	status, err := resourceStatus(input.Status, string(provider.StatusActive))
	if err != nil {
		return ProviderView{}, err
	}
	ciphertext, err := s.security.EncryptCredential(*input.Credential)
	if err != nil {
		return ProviderView{}, domain.NewError(domain.CodeValidation, err.Error())
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start provider create transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	row, err := tx.Provider.Create().
		SetName(name).
		SetBaseURL(baseURL).
		SetStatus(provider.Status(status)).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return ProviderView{}, domain.NewError(domain.CodeNameConflict, "provider name already exists")
	}
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create provider", err)
	}
	credentialRow, err := tx.ProviderCredential.Create().
		SetProviderID(row.ID).
		SetLabel("default").
		SetCredentialCiphertext(ciphertext).
		SetStatus(providercredential.StatusActive).
		Save(ctx)
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create provider credential", err)
	}
	if err := tx.Commit(); err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit provider create", err)
	}

	view := providerView(row)
	view.Credentials = []ProviderCredentialView{providerCredentialView(credentialRow)}
	if row.Status == provider.StatusActive {
		syncView, syncErr := s.SyncProviderCatalog(ctx, row.ID)
		if syncErr != nil {
			_ = s.DeleteProvider(ctx, row.ID)
			return ProviderView{}, syncErr
		}
		view.LastCatalogSync = &syncView
	}
	return view, nil
}

func (s *Service) TeacherProvider(ctx context.Context, id uuid.UUID) (ProviderView, error) {
	row, err := s.db.Provider.Query().
		Where(provider.IDEQ(id)).
		WithCredentials(func(query *ent.ProviderCredentialQuery) {
			query.Order(ent.Asc(providercredential.FieldLabel))
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ProviderView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	return providerViewWithCredentials(row, row.Edges.Credentials), nil
}

func (s *Service) UpdateProvider(ctx context.Context, id uuid.UUID, input ProviderInput) (ProviderView, error) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start provider update transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := tx.Provider.Query().Where(provider.IDEQ(id)).Only(ctx)
	if ent.IsNotFound(err) {
		return ProviderView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	builder := current.Update()
	if input.Name != "" {
		name, err := ValidateName(input.Name, 128)
		if err != nil {
			return ProviderView{}, err
		}
		builder.SetName(name)
	}
	if input.BaseURL != "" {
		baseURL, err := ValidateBaseURL(input.BaseURL)
		if err != nil {
			return ProviderView{}, err
		}
		builder.SetBaseURL(baseURL)
	}
	if input.Status != nil {
		status, err := resourceStatus(input.Status, "")
		if err != nil {
			return ProviderView{}, err
		}
		builder.SetStatus(provider.Status(status))
	}
	row, err := builder.Save(ctx)
	if ent.IsNotFound(err) {
		return ProviderView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if ent.IsConstraintError(err) {
		return ProviderView{}, domain.NewError(domain.CodeNameConflict, "provider name already exists")
	}
	if err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update provider", err)
	}
	if row.Status == provider.StatusInactive && current.Status == provider.StatusActive {
		if err := catalog.DeactivateProviderResources(ctx, tx.Client(), row.ID); err != nil {
			return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "deactivate provider routes", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ProviderView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit provider update", err)
	}
	view := providerView(row)
	if refreshed, refreshErr := s.TeacherProvider(ctx, row.ID); refreshErr == nil {
		view = refreshed
	}
	if shouldSyncProviderCatalog(current, row, input) {
		syncView, syncErr := s.SyncProviderCatalog(ctx, row.ID)
		if syncErr != nil {
			return ProviderView{}, syncErr
		}
		view.LastCatalogSync = &syncView
	}
	return view, nil
}

func (s *Service) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "start provider delete transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if exists, err := tx.Provider.Query().Where(provider.IDEQ(id)).Exist(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	} else if !exists {
		return domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if _, err := tx.ModelBinding.Delete().Where(modelbinding.ProviderIDEQ(id)).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete provider bindings", err)
	}
	if err := tx.Provider.DeleteOneID(id).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete provider", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "commit provider delete", err)
	}
	return nil
}

func shouldSyncProviderCatalog(before, after *ent.Provider, input ProviderInput) bool {
	if after.Status != provider.StatusActive {
		return false
	}
	if before == nil {
		return true
	}
	if before.Status != provider.StatusActive {
		return true
	}
	return input.BaseURL != "" && strings.TrimSpace(input.BaseURL) != before.BaseURL
}

func (s *Service) TeacherModels(ctx context.Context) ([]ModelView, error) {
	activeProviders, err := s.db.Provider.Query().
		Where(provider.StatusEQ(provider.StatusActive)).
		Order(ent.Asc(provider.FieldName)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list active providers", err)
	}
	if len(activeProviders) == 0 {
		return []ModelView{}, nil
	}
	result := make([]ModelView, 0)
	for _, providerRow := range activeProviders {
		lines, lineErr := s.teacherModelLinesForProvider(ctx, providerRow)
		if lineErr != nil {
			return nil, lineErr
		}
		result = append(result, lines...)
	}
	return result, nil
}

func (s *Service) teacherModelLinesForProvider(ctx context.Context, providerRow *ent.Provider) ([]ModelView, error) {
	if err := catalog.RestoreReadyGovernedModels(ctx, s.db, providerRow.ID); err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "restore governed models", err)
	}
	needsSync, err := s.activeProviderCatalogNeedsSync(ctx, providerRow.ID)
	if err != nil {
		return nil, err
	}
	if needsSync {
		if _, syncErr := s.SyncProviderCatalog(ctx, providerRow.ID); syncErr != nil {
			return nil, syncErr
		}
	}
	credentials, err := s.db.ProviderCredential.Query().
		Where(
			providercredential.ProviderIDEQ(providerRow.ID),
			providercredential.StatusEQ(providercredential.StatusActive),
		).
		Order(ent.Asc(providercredential.FieldLabel)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list provider credentials", err)
	}
	result := make([]ModelView, 0)
	for _, credential := range credentials {
		lines, lineErr := s.teacherModelLinesForCredential(ctx, credential, providerRow)
		if lineErr != nil {
			return nil, lineErr
		}
		result = append(result, lines...)
	}
	return result, nil
}

func (s *Service) activeProviderCatalogNeedsSync(ctx context.Context, providerID uuid.UUID) (bool, error) {
	count, err := s.db.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.ProviderIDEQ(providerID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
			providermodelcatalog.HasCredentialWith(
				providercredential.StatusEQ(providercredential.StatusActive),
			),
		).
		Count(ctx)
	if err != nil {
		return false, domain.WrapError(domain.CodeDependencyUnavailable, "count provider catalog", err)
	}
	return count == 0, nil
}

func (s *Service) teacherModelLinesForCredential(
	ctx context.Context,
	credential *ent.ProviderCredential,
	providerRow *ent.Provider,
) ([]ModelView, error) {
	catalogRows, err := s.db.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.CredentialIDEQ(credential.ID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
		).
		Order(ent.Asc(providermodelcatalog.FieldModelID)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list provider catalog", err)
	}
	if len(catalogRows) == 0 {
		return []ModelView{}, nil
	}
	uniqueCatalogRows := dedupeCatalogRows(catalogRows)
	publicModelIDs := make([]string, 0, len(uniqueCatalogRows))
	for _, row := range uniqueCatalogRows {
		publicModelIDs = append(publicModelIDs, row.ModelID)
	}
	modelByPublicID, err := s.modelsByPublicID(ctx, publicModelIDs)
	if err != nil {
		return nil, err
	}
	result := make([]ModelView, 0, len(uniqueCatalogRows))
	for _, catalogRow := range uniqueCatalogRows {
		modelID := strings.TrimSpace(catalogRow.ModelID)
		item := teacherModelLineView(catalogRow, credential, providerRow, modelByPublicID[modelID])
		if modelRow := modelByPublicID[modelID]; modelRow != nil {
			ready, readyErr := catalog.CredentialLineHasActiveRoute(ctx, s.db, modelRow.ID, credential.ID)
			if readyErr != nil {
				return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query model route", readyErr)
			}
			item.RouteReady = ready
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) modelsByPublicID(ctx context.Context, publicModelIDs []string) (map[string]*ent.Model, error) {
	if len(publicModelIDs) == 0 {
		return map[string]*ent.Model{}, nil
	}
	modelRows, err := s.db.Model.Query().Where(entmodel.ModelIDIn(publicModelIDs...)).All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list models", err)
	}
	modelByPublicID := make(map[string]*ent.Model, len(modelRows))
	for _, row := range modelRows {
		modelByPublicID[row.ModelID] = row
	}
	return modelByPublicID, nil
}

func teacherModelLineView(
	catalogRow *ent.ProviderModelCatalog,
	credential *ent.ProviderCredential,
	providerRow *ent.Provider,
	modelRow *ent.Model,
) ModelView {
	if modelRow != nil {
		view := modelView(modelRow)
		view.ProviderID = providerRow.ID.String()
		view.ProviderName = providerRow.Name
		view.CredentialID = credential.ID.String()
		view.CredentialLabel = credential.Label
		return view
	}
	displayName := catalogRow.ModelID
	if catalogRow.DisplayName != nil && strings.TrimSpace(*catalogRow.DisplayName) != "" {
		displayName = strings.TrimSpace(*catalogRow.DisplayName)
	}
	view := ModelView{
		ModelID:         catalogRow.ModelID,
		DisplayName:     displayName,
		Category:        string(entmodel.DefaultCategory),
		Status:          string(entmodel.StatusPendingConfiguration),
		ProviderID:      providerRow.ID.String(),
		ProviderName:    providerRow.Name,
		CredentialID:    credential.ID.String(),
		CredentialLabel: credential.Label,
	}
	if catalogRow.Description != nil && strings.TrimSpace(*catalogRow.Description) != "" {
		description := strings.TrimSpace(*catalogRow.Description)
		view.Description = &description
	}
	return view
}

func dedupeCatalogRows(rows []*ent.ProviderModelCatalog) []*ent.ProviderModelCatalog {
	seen := make(map[string]struct{}, len(rows))
	result := make([]*ent.ProviderModelCatalog, 0, len(rows))
	for _, row := range rows {
		modelID := strings.TrimSpace(row.ModelID)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		result = append(result, row)
	}
	return result
}

func (s *Service) CreateModel(ctx context.Context, input ModelInput) (ModelView, error) {
	modelID, err := normalizeSingleModelID(input.ModelID)
	if err != nil {
		return ModelView{}, err
	}
	displayName, err := ValidateName(input.DisplayName, 256)
	if err != nil {
		return ModelView{}, err
	}
	category, err := modelCategory(input.Category)
	if err != nil {
		return ModelView{}, err
	}
	status := entmodel.StatusPendingConfiguration
	if input.Status != nil {
		status, err = modelStatus(*input.Status)
		if err != nil {
			return ModelView{}, err
		}
		if status == entmodel.StatusActive {
			return ModelView{}, domain.NewError(domain.CodeModelNotReady, "create a binding before activating the model")
		}
	}
	capabilities, err := modelCapabilities(input.Capabilities)
	if err != nil {
		return ModelView{}, err
	}
	builder := s.db.Model.Create().
		SetModelID(modelID).
		SetDisplayName(displayName).
		SetCategory(category).
		SetCapabilities(capabilities).
		SetInputModalities(cleanStringList(input.InputModalities)).
		SetOutputModalities(cleanStringList(input.OutputModalities)).
		SetStatus(status)
	if input.CreditMultiplierMilli != nil {
		builder.SetCreditMultiplierMilli(*input.CreditMultiplierMilli)
	}
	if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
		builder.SetDescription(strings.TrimSpace(*input.Description))
	}
	if input.ContextWindow != nil {
		builder.SetContextWindow(*input.ContextWindow)
	}
	if input.MaxInputTokens != nil {
		builder.SetMaxInputTokens(*input.MaxInputTokens)
	}
	if input.MaxOutputTokens != nil {
		builder.SetMaxOutputTokens(*input.MaxOutputTokens)
	}
	if input.IsCommon != nil {
		builder.SetIsCommon(*input.IsCommon)
	}
	row, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return ModelView{}, domain.NewError(domain.CodeNameConflict, "model ID already exists")
	}
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create model", err)
	}
	if row.IsCommon && row.Status == entmodel.StatusActive {
		_ = s.cache.PublishGlobalEvent(ctx, "models.common_changed", map[string]any{"revision": uuid.Must(uuid.NewV7()).String()})
	}
	return modelView(row), nil
}

func (s *Service) TeacherModel(ctx context.Context, publicModelID string, credentialID *uuid.UUID) (ModelView, []BindingView, error) {
	modelID, err := normalizeSingleModelID(publicModelID)
	if err != nil {
		return ModelView{}, nil, err
	}
	row, err := s.db.Model.Query().Where(entmodel.ModelIDEQ(modelID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ModelView{}, nil, domain.NewError(domain.CodeNotFound, "model not found")
	}
	if err != nil {
		return ModelView{}, nil, domain.WrapError(domain.CodeDependencyUnavailable, "query model", err)
	}
	bindingQuery := s.db.ModelBinding.Query().Where(modelbinding.ModelIDEQ(row.ID))
	if credentialID != nil {
		bindingQuery = bindingQuery.Where(modelbinding.CredentialIDEQ(*credentialID))
	}
	bindings, err := bindingQuery.Order(ent.Asc(modelbinding.FieldPriority), ent.Asc(modelbinding.FieldID)).All(ctx)
	if err != nil {
		return ModelView{}, nil, domain.WrapError(domain.CodeDependencyUnavailable, "query model bindings", err)
	}
	result := make([]BindingView, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, s.bindingView(ctx, binding))
	}
	view := modelView(row)
	if credentialID != nil {
		view.CredentialID = credentialID.String()
		if credentialRow, credErr := s.db.ProviderCredential.Get(ctx, *credentialID); credErr == nil {
			view.CredentialLabel = credentialRow.Label
			if providerRow, provErr := s.db.Provider.Get(ctx, credentialRow.ProviderID); provErr == nil {
				view.ProviderID = providerRow.ID.String()
				view.ProviderName = providerRow.Name
			}
		}
		view.RouteReady, err = catalog.CredentialLineHasActiveRoute(ctx, s.db, row.ID, *credentialID)
	} else {
		view.RouteReady, err = s.modelHasActiveRoute(ctx, row.ID)
	}
	if err != nil {
		return ModelView{}, nil, err
	}
	return view, result, nil
}

func (s *Service) UpdateModel(ctx context.Context, actorID uuid.UUID, publicModelID string, input ModelInput) (ModelView, error) {
	modelID, normalizeErr := normalizeSingleModelID(publicModelID)
	if normalizeErr != nil {
		return ModelView{}, normalizeErr
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start model update transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := tx.Model.Query().Where(entmodel.ModelIDEQ(modelID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ModelView{}, domain.NewError(domain.CodeNotFound, "model not found")
	}
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model", err)
	}
	builder := current.Update()
	multiplierChanged := input.CreditMultiplierMilli != nil && (current.CreditMultiplierMilli == nil || *current.CreditMultiplierMilli != *input.CreditMultiplierMilli)
	if input.CreditMultiplierMilli != nil && multiplierChanged {
		builder.SetCreditMultiplierMilli(*input.CreditMultiplierMilli)
	}
	if input.DisplayName != "" {
		name, err := ValidateName(input.DisplayName, 256)
		if err != nil {
			return ModelView{}, err
		}
		builder.SetDisplayName(name)
	}
	if input.Description != nil {
		if strings.TrimSpace(*input.Description) == "" {
			builder.ClearDescription()
		} else {
			builder.SetDescription(strings.TrimSpace(*input.Description))
		}
	}
	if input.Category != "" {
		category, err := modelCategory(input.Category)
		if err != nil {
			return ModelView{}, err
		}
		builder.SetCategory(category)
	}
	if input.Capabilities != nil {
		capabilities, err := modelCapabilities(input.Capabilities)
		if err != nil {
			return ModelView{}, err
		}
		builder.SetCapabilities(capabilities)
	}
	if input.InputModalities != nil {
		builder.SetInputModalities(cleanStringList(input.InputModalities))
	}
	if input.OutputModalities != nil {
		builder.SetOutputModalities(cleanStringList(input.OutputModalities))
	}
	if input.ContextWindowSet {
		if input.ContextWindow == nil {
			builder.ClearContextWindow()
		} else {
			if *input.ContextWindow <= 0 {
				return ModelView{}, domain.NewError(domain.CodeValidation, "context_window must be positive")
			}
			builder.SetContextWindow(*input.ContextWindow)
		}
	}
	if input.MaxInputTokensSet {
		if input.MaxInputTokens == nil {
			builder.ClearMaxInputTokens()
		} else {
			if *input.MaxInputTokens <= 0 {
				return ModelView{}, domain.NewError(domain.CodeValidation, "max_input_tokens must be positive")
			}
			builder.SetMaxInputTokens(*input.MaxInputTokens)
		}
	}
	if input.MaxOutputTokensSet {
		if input.MaxOutputTokens == nil {
			builder.ClearMaxOutputTokens()
		} else {
			if *input.MaxOutputTokens <= 0 {
				return ModelView{}, domain.NewError(domain.CodeValidation, "max_output_tokens must be positive")
			}
			builder.SetMaxOutputTokens(*input.MaxOutputTokens)
		}
	}
	if input.IsCommon != nil {
		builder.SetIsCommon(*input.IsCommon)
	}
	if input.Status != nil {
		status, err := modelStatus(*input.Status)
		if err != nil {
			return ModelView{}, err
		}
		if status == entmodel.StatusActive {
			multiplier := current.CreditMultiplierMilli
			if input.CreditMultiplierMilli != nil {
				multiplier = input.CreditMultiplierMilli
			}
			if multiplier == nil || *multiplier <= 0 {
				return ModelView{}, domain.NewError(domain.CodeModelNotReady, "configure a positive credit multiplier before activating the model")
			}
			ready, err := modelHasActiveRouteTx(ctx, tx, current.ID)
			if err != nil {
				return ModelView{}, err
			}
			if !ready {
				appErr := domain.NewError(domain.CodeModelRoutingRequired, "model requires an active binding and provider")
				appErr.Details = map[string]any{"model_ids": []string{current.ModelID}}
				return ModelView{}, appErr
			}
		}
		builder.SetStatus(status)
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update model", err)
	}
	if multiplierChanged && current.CreditMultiplierMilli != nil {
		reason := strings.TrimSpace(input.MultiplierChangeReason)
		if reason == "" {
			reason = "update"
		}
		audit := tx.ModelMultiplierAudit.Create().SetModelID(current.ID).SetActorUserID(actorID).SetNewMultiplierMilli(*input.CreditMultiplierMilli).SetReason(reason)
		audit.SetOldMultiplierMilli(*current.CreditMultiplierMilli)
		if _, err := audit.Save(ctx); err != nil {
			return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create model multiplier audit", err)
		}
	}
	commonWasVisible := current.IsCommon && current.Status == entmodel.StatusActive
	commonIsVisible := row.IsCommon && row.Status == entmodel.StatusActive
	if err := tx.Commit(); err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit model update", err)
	}
	if commonWasVisible != commonIsVisible {
		_ = s.cache.PublishGlobalEvent(ctx, "models.common_changed", map[string]any{"revision": uuid.Must(uuid.NewV7()).String()})
	}
	return modelView(row), nil
}

func (s *Service) CreateBinding(ctx context.Context, publicModelID string, input BindingInput) (BindingView, error) {
	normalized, normalizeErr := normalizeSingleModelID(publicModelID)
	if normalizeErr != nil {
		return BindingView{}, normalizeErr
	}
	modelRow, err := s.db.Model.Query().Where(entmodel.ModelIDEQ(normalized)).Only(ctx)
	if ent.IsNotFound(err) {
		return BindingView{}, domain.NewError(domain.CodeNotFound, "model not found")
	} else if err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model", err)
	}
	if err := validateBindingInput(input); err != nil {
		return BindingView{}, err
	}
	providerExists, err := s.db.Provider.Query().Where(provider.IDEQ(input.ProviderID)).Exist(ctx)
	if err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if !providerExists {
		return BindingView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	credentialID, err := s.resolveProbeCredentialID(ctx, input.ProviderID, input.UpstreamModelName)
	if err != nil {
		return BindingView{}, err
	}
	adapters, err := bindingAdaptersFromInput(modelRow.ModelID, input)
	if err != nil {
		return BindingView{}, err
	}
	var primary *ent.ModelBinding
	for _, adapter := range adapters {
		exists, err := s.db.ModelBinding.Query().Where(
			modelbinding.ModelIDEQ(modelRow.ID),
			modelbinding.ProviderIDEQ(input.ProviderID),
			modelbinding.AdapterEQ(adapter),
		).Only(ctx)
		if err == nil {
			update := exists.Update().
				SetUpstreamModelName(strings.TrimSpace(input.UpstreamModelName)).
				SetPriority(catalog.DefaultBindingPriority(adapter)).
				SetStatus(modelbinding.Status(input.Status))
			if credentialID != nil {
				update.SetCredentialID(*credentialID)
			}
			row, saveErr := update.Save(ctx)
			if saveErr != nil {
				return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update model binding", saveErr)
			}
			if primary == nil {
				primary = row
			}
			continue
		}
		if !ent.IsNotFound(err) {
			return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model binding", err)
		}
		builder := s.db.ModelBinding.Create().
			SetModelID(modelRow.ID).
			SetProviderID(input.ProviderID).
			SetUpstreamModelName(strings.TrimSpace(input.UpstreamModelName)).
			SetAdapter(adapter).
			SetPriority(catalog.DefaultBindingPriority(adapter)).
			SetStatus(modelbinding.Status(input.Status))
		if credentialID != nil {
			builder.SetCredentialID(*credentialID)
		}
		row, saveErr := builder.Save(ctx)
		if ent.IsConstraintError(saveErr) {
			return BindingView{}, domain.NewError(domain.CodeNameConflict, "model binding already exists")
		}
		if saveErr != nil {
			return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create model binding", saveErr)
		}
		if primary == nil {
			primary = row
		}
	}
	if primary == nil {
		return BindingView{}, domain.NewError(domain.CodeDependencyUnavailable, "create model binding")
	}
	return s.bindingView(ctx, primary), nil
}

type BindingRouteProbeInput struct {
	ProviderID        uuid.UUID
	UpstreamModelName string
	Endpoint          string
}

func (s *Service) ProbeModelBindingRoute(ctx context.Context, input BindingRouteProbeInput) (catalog.RouteProbeStep, error) {
	endpoint := catalog.BindingRouteProbeEndpoint(strings.TrimSpace(input.Endpoint))
	switch endpoint {
	case catalog.BindingRouteProbeChatCompletions, catalog.BindingRouteProbeResponses, catalog.BindingRouteProbeMessages, catalog.BindingRouteProbeImagesGenerations:
	default:
		return catalog.RouteProbeStep{}, domain.NewError(domain.CodeValidation, "invalid binding route probe endpoint")
	}
	upstreamModelName := strings.TrimSpace(input.UpstreamModelName)
	if upstreamModelName == "" {
		return catalog.RouteProbeStep{}, domain.NewError(domain.CodeValidation, "upstream_model_name is required")
	}
	providerRow, err := s.db.Provider.Query().Where(provider.IDEQ(input.ProviderID)).Only(ctx)
	if ent.IsNotFound(err) {
		return catalog.RouteProbeStep{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return catalog.RouteProbeStep{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	credentialPlain, _, err := s.probeCredentialForProvider(ctx, input.ProviderID, upstreamModelName)
	if err != nil {
		return catalog.RouteProbeStep{}, err
	}
	probe := catalog.NewRouteProbe(providerRow.BaseURL, credentialPlain)
	return probe.ProbeStep(ctx, upstreamModelName, endpoint)
}

func bindingAdaptersFromInput(modelID string, input BindingInput) ([]modelbinding.Adapter, error) {
	if len(input.ProbedAdapters) == 0 {
		return nil, domain.NewError(domain.CodeValidation, "probed_adapters is required; run route probe before saving bindings")
	}
	seen := map[modelbinding.Adapter]struct{}{}
	result := make([]modelbinding.Adapter, 0, len(input.ProbedAdapters))
	for _, raw := range input.ProbedAdapters {
		adapter := modelbinding.Adapter(strings.TrimSpace(raw))
		if !validBindingAdapter(string(adapter)) {
			return nil, domain.NewError(domain.CodeValidation, "invalid probed adapter")
		}
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		result = append(result, adapter)
	}
	if len(result) == 0 {
		return nil, domain.NewError(domain.CodeValidation, "probed_adapters is required; run route probe before saving bindings")
	}
	return catalog.ExpandChatGatewayAdapters(modelID, result), nil
}

func (s *Service) resolveProbeCredentialID(ctx context.Context, providerID uuid.UUID, upstreamModelName string) (*uuid.UUID, error) {
	_, credentialID, err := s.probeCredentialForProvider(ctx, providerID, upstreamModelName)
	return credentialID, err
}

func (s *Service) probeCredentialForProvider(ctx context.Context, providerID uuid.UUID, upstreamModelName string) (string, *uuid.UUID, error) {
	if credentialID, resolveErr := s.ResolveCredentialForUpstream(ctx, providerID, upstreamModelName); resolveErr != nil {
		return "", nil, resolveErr
	} else if credentialID != nil {
		row, err := s.db.ProviderCredential.Get(ctx, *credentialID)
		if err != nil {
			return "", nil, domain.WrapError(domain.CodeDependencyUnavailable, "query provider credential", err)
		}
		plain, err := s.security.DecryptCredential(row.CredentialCiphertext)
		if err != nil {
			return "", nil, domain.WrapError(domain.CodeDependencyUnavailable, "decrypt provider credential", err)
		}
		return plain, credentialID, nil
	}
	credentials, err := catalog.ActiveCredentials(ctx, s.db, &providerID)
	if err != nil {
		return "", nil, domain.WrapError(domain.CodeDependencyUnavailable, "list active provider credentials", err)
	}
	if len(credentials) == 0 {
		return "", nil, domain.NewError(domain.CodeValidation, "provider has no active credentials")
	}
	plain, err := s.security.DecryptCredential(credentials[0].CredentialCiphertext)
	if err != nil {
		return "", nil, domain.WrapError(domain.CodeDependencyUnavailable, "decrypt provider credential", err)
	}
	id := credentials[0].ID
	return plain, &id, nil
}

func (s *Service) UpdateBinding(ctx context.Context, bindingID uuid.UUID, input BindingInput) (BindingView, error) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start binding update transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := tx.ModelBinding.Query().Where(modelbinding.IDEQ(bindingID)).Only(ctx)
	if ent.IsNotFound(err) {
		return BindingView{}, domain.NewError(domain.CodeNotFound, "model binding not found")
	}
	if err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model binding", err)
	}
	builder := current.Update()
	if input.UpstreamModelName != "" {
		builder.SetUpstreamModelName(strings.TrimSpace(input.UpstreamModelName))
	}
	if input.Adapter != "" {
		if !validBindingAdapter(input.Adapter) {
			return BindingView{}, domain.NewError(domain.CodeValidation, "invalid binding adapter")
		}
		builder.SetAdapter(modelbinding.Adapter(input.Adapter))
	}
	if input.Priority >= 0 {
		builder.SetPriority(input.Priority)
	}
	if input.Status != "" {
		if input.Status != string(modelbinding.StatusActive) && input.Status != string(modelbinding.StatusInactive) {
			return BindingView{}, domain.NewError(domain.CodeValidation, "invalid binding status")
		}
		builder.SetStatus(modelbinding.Status(input.Status))
	}
	row, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return BindingView{}, domain.NewError(domain.CodeNameConflict, "model binding already exists")
	}
	if err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update model binding", err)
	}
	if err := tx.Commit(); err != nil {
		return BindingView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit binding update", err)
	}
	return s.bindingView(ctx, row), nil
}

func (s *Service) populateRouteReadiness(ctx context.Context, view *APIKeyView) error {
	for i := range view.Models {
		modelRow, err := s.db.Model.Query().Where(entmodel.ModelIDEQ(view.Models[i].ModelID)).Only(ctx)
		if err != nil {
			return domain.WrapError(domain.CodeDependencyUnavailable, "query model route identity", err)
		}
		enriched, err := s.enrichStudentModelView(ctx, view.Models[i], modelRow.ID)
		if err != nil {
			return err
		}
		view.Models[i] = enriched
	}
	return nil
}

func (s *Service) populateKeyAvailability(ctx context.Context, view *APIKeyView) error {
	hasProvider, err := s.hasActiveProvider(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	if !hasProvider {
		view.RouteReady = false
		for i := range view.Models {
			view.Models[i].RouteReady = false
		}
		return nil
	}
	if err := s.populateRouteReadiness(ctx, view); err != nil {
		return err
	}
	view.RouteReady = false
	for _, model := range view.Models {
		if model.Status == string(entmodel.StatusActive) && model.RouteReady {
			view.RouteReady = true
			return nil
		}
	}
	return nil
}

func ensureOrganizationMember(ctx context.Context, tx *ent.Tx, organizationID, userID uuid.UUID) error {
	exists, err := tx.OrganizationMember.Query().Where(
		organizationmember.OrganizationIDEQ(organizationID),
		organizationmember.UserIDEQ(userID),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query organization membership", err)
	}
	if exists {
		return nil
	}
	if _, err := tx.OrganizationMember.Create().
		SetOrganizationID(organizationID).
		SetUserID(userID).
		Save(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "create organization membership", err)
	}
	return nil
}

func ensureProjectMember(ctx context.Context, tx *ent.Tx, projectID, userID uuid.UUID) error {
	exists, err := tx.ProjectMember.Query().Where(
		projectmember.ProjectIDEQ(projectID),
		projectmember.UserIDEQ(userID),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project membership", err)
	}
	if exists {
		return nil
	}
	if _, err := tx.ProjectMember.Create().
		SetProjectID(projectID).
		SetUserID(userID).
		Save(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "create project membership", err)
	}
	return nil
}

func (s *Service) modelHasActiveRoute(ctx context.Context, modelID uuid.UUID) (bool, error) {
	return catalog.ModelHasActiveRoute(ctx, s.db, modelID)
}

func modelHasActiveRouteTx(ctx context.Context, tx *ent.Tx, modelID uuid.UUID) (bool, error) {
	return catalog.ModelHasActiveRoute(ctx, tx.Client(), modelID)
}

func credentialIDString(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

func providerView(row *ent.Provider) ProviderView {
	return ProviderView{
		ID: row.ID.String(), Name: row.Name, BaseURL: row.BaseURL,
		CredentialConfigured: false,
		Status:               string(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func providerViewWithCredentials(row *ent.Provider, credentials []*ent.ProviderCredential) ProviderView {
	view := providerView(row)
	view.Credentials = make([]ProviderCredentialView, 0, len(credentials))
	for _, credential := range credentials {
		view.Credentials = append(view.Credentials, providerCredentialView(credential))
		if credential.Status == providercredential.StatusActive && len(credential.CredentialCiphertext) > 0 {
			view.CredentialConfigured = true
		}
	}
	return view
}

func (s *Service) bindingView(ctx context.Context, row *ent.ModelBinding) BindingView {
	publicModelID := ""
	if modelRow, err := s.db.Model.Get(ctx, row.ModelID); err == nil {
		publicModelID = modelRow.ModelID
	}
	view := BindingView{
		ID: row.ID.String(), ModelID: publicModelID, ProviderID: row.ProviderID.String(),
		CredentialID: credentialIDString(row.CredentialID),
		UpstreamModelName: row.UpstreamModelName, Adapter: string(row.Adapter),
		Priority: row.Priority, Status: string(row.Status),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.CredentialID != nil {
		if cred, err := s.db.ProviderCredential.Get(ctx, *row.CredentialID); err == nil {
			view.CredentialLabel = cred.Label
		}
	}
	return view
}

var allowedModelCapabilities = map[string]struct{}{"reasoning": {}, "vision": {}, "tool_calling": {}, "structured_output": {}, "web_search": {}, "coding": {}, "embeddings": {}, "rerank": {}, "realtime": {}, "image_generation": {}, "video_generation": {}, "speech_to_text": {}, "text_to_speech": {}}

func modelCapabilities(values []string) ([]string, error) {
	cleaned := cleanStringList(values)
	for _, value := range cleaned {
		if _, ok := allowedModelCapabilities[value]; !ok {
			return nil, domain.NewError(domain.CodeValidation, "invalid model capability")
		}
	}
	return cleaned, nil
}

func resourceStatus(value *string, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	if normalized != "active" && normalized != "inactive" {
		return "", domain.NewError(domain.CodeValidation, "invalid resource status")
	}
	return normalized, nil
}

func mentorApplicationStatus(value string) (mentorprojectapplication.Status, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(mentorprojectapplication.StatusPending):
		return mentorprojectapplication.StatusPending, nil
	case string(mentorprojectapplication.StatusApproved):
		return mentorprojectapplication.StatusApproved, nil
	case string(mentorprojectapplication.StatusRejected):
		return mentorprojectapplication.StatusRejected, nil
	case string(mentorprojectapplication.StatusCancelled):
		return mentorprojectapplication.StatusCancelled, nil
	default:
		return "", domain.NewError(domain.CodeValidation, "invalid project application status")
	}
}

func modelCategory(value string) (entmodel.Category, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(entmodel.CategoryText):
		return entmodel.CategoryText, nil
	case string(entmodel.CategoryImage):
		return entmodel.CategoryImage, nil
	case string(entmodel.CategoryAudio):
		return entmodel.CategoryAudio, nil
	case string(entmodel.CategoryVideo):
		return entmodel.CategoryVideo, nil
	case string(entmodel.CategoryMultimodal):
		return entmodel.CategoryMultimodal, nil
	case string(entmodel.CategoryEmbedding):
		return entmodel.CategoryEmbedding, nil
	case string(entmodel.CategoryRerank):
		return entmodel.CategoryRerank, nil
	default:
		return "", domain.NewError(domain.CodeValidation, "invalid model category")
	}
}

func modelStatus(value string) (entmodel.Status, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(entmodel.StatusPendingConfiguration):
		return entmodel.StatusPendingConfiguration, nil
	case string(entmodel.StatusActive):
		return entmodel.StatusActive, nil
	case string(entmodel.StatusInactive):
		return entmodel.StatusInactive, nil
	default:
		return "", domain.NewError(domain.CodeValidation, "invalid model status")
	}
}

func validateBindingInput(input BindingInput) error {
	if input.ProviderID == uuid.Nil {
		return domain.NewError(domain.CodeValidation, "provider_id is required")
	}
	if strings.TrimSpace(input.UpstreamModelName) == "" || len(input.UpstreamModelName) > 256 {
		return domain.NewError(domain.CodeValidation, "invalid upstream_model_name")
	}
	if !validBindingAdapter(input.Adapter) {
		return domain.NewError(domain.CodeValidation, "invalid binding adapter")
	}
	if input.Priority < 0 {
		return domain.NewError(domain.CodeValidation, "priority must be non-negative")
	}
	if input.Status != string(modelbinding.StatusActive) && input.Status != string(modelbinding.StatusInactive) {
		return domain.NewError(domain.CodeValidation, "invalid binding status")
	}
	return nil
}

func validBindingAdapter(value string) bool {
	switch modelbinding.Adapter(value) {
	case modelbinding.AdapterOpenaiCompatible,
		modelbinding.AdapterOpenaiResponses,
		modelbinding.AdapterOpenaiEmbeddings,
		modelbinding.AdapterOpenaiImages,
		modelbinding.AdapterOpenaiAudio,
		modelbinding.AdapterOpenaiVideo,
		modelbinding.AdapterOpenaiRealtime,
		modelbinding.AdapterOpenaiModerations,
		modelbinding.AdapterAnthropic,
		modelbinding.AdapterCohereRerankV2,
		modelbinding.AdapterGoogleGeminiV1beta:
		return true
	default:
		return false
	}
}

func normalizeSingleModelID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 256 {
		return "", domain.NewError(domain.CodeValidation, "invalid model ID")
	}
	return value, nil
}

func cleanStringList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
