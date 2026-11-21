package controlplane

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/studentmodellibrary"
)

func modelEnabledForStudents(row *ent.Model) bool {
	return studentModelPublicationIssue(row) == ""
}

func studentModelPublicationIssue(row *ent.Model) string {
	if row.Status != entmodel.StatusActive {
		return "model is not enabled for students"
	}
	if row.CreditMultiplierMilli == nil || *row.CreditMultiplierMilli <= 0 {
		return "model is missing a student price"
	}
	return ""
}

func modelViewEnabledForStudents(item ModelView) bool {
	if item.Status != string(entmodel.StatusActive) || !item.RouteReady || item.CreditMultiplier == nil {
		return false
	}
	return true
}

func modelMatchesStudentQuery(row *ent.Model, query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(row.ModelID), query) {
		return true
	}
	if strings.Contains(strings.ToLower(row.DisplayName), query) {
		return true
	}
	if row.Description != nil && strings.Contains(strings.ToLower(*row.Description), query) {
		return true
	}
	return false
}

func modelCatalogCandidateFromPlatformModel(view ModelView) ModelCatalogCandidate {
	item := ModelCatalogCandidate{
		ModelID:          view.ModelID,
		ProviderName:     view.ProviderName,
		CreditMultiplier: view.CreditMultiplier,
		RawEntry:         map[string]any{},
	}
	if view.DisplayName != "" {
		displayName := view.DisplayName
		item.DisplayName = &displayName
	}
	if view.Description != nil && *view.Description != "" {
		item.Description = view.Description
	}
	return item
}

func filterStudentVisibleModels(items []ModelView) []ModelView {
	result := make([]ModelView, 0, len(items))
	for _, item := range items {
		if !modelViewEnabledForStudents(item) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func (s *Service) enrichStudentModelView(ctx context.Context, item ModelView, platformModelID uuid.UUID) (ModelView, error) {
	binding, err := s.db.ModelBinding.Query().
		Where(
			modelbinding.ModelIDEQ(platformModelID),
			modelbinding.StatusEQ(modelbinding.StatusActive),
		).
		WithProvider().
		Order(ent.Asc(modelbinding.FieldPriority), ent.Asc(modelbinding.FieldID)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return item, domain.WrapError(domain.CodeDependencyUnavailable, "query model binding", err)
	}
	if binding != nil && binding.Edges.Provider != nil {
		item.ProviderID = binding.Edges.Provider.ID.String()
		item.ProviderName = binding.Edges.Provider.Name
		item.PrimaryAdapter = string(binding.Adapter)
	}
	item.SupportedProtocols, err = s.modelSupportedProtocols(ctx, platformModelID)
	if err != nil {
		return item, domain.WrapError(domain.CodeDependencyUnavailable, "query supported protocols", err)
	}
	item.AnthropicReady, err = catalog.ModelHasActiveRouteForAdapter(ctx, s.db, platformModelID, modelbinding.AdapterAnthropic)
	if err != nil {
		return item, domain.WrapError(domain.CodeDependencyUnavailable, "query anthropic route", err)
	}
	item.ResponsesReady, err = catalog.ModelHasActiveRouteForAdapter(ctx, s.db, platformModelID, modelbinding.AdapterOpenaiResponses)
	if err != nil {
		return item, domain.WrapError(domain.CodeDependencyUnavailable, "query responses route", err)
	}
	item.RouteReady, err = s.modelHasActiveRoute(ctx, platformModelID)
	if err != nil {
		return item, err
	}
	return item, nil
}

// modelSupportedProtocols is the set of currently routable adapters. The
// student plaza uses it to show which wire protocol each model accepts; the
// gateway never converts between them.
func (s *Service) modelSupportedProtocols(ctx context.Context, platformModelID uuid.UUID) ([]string, error) {
	rows, err := s.db.ModelBinding.Query().
		Where(modelbinding.ModelIDEQ(platformModelID), modelbinding.StatusEQ(modelbinding.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	protocols := make([]string, 0, len(rows))
	for _, row := range rows {
		ready, err := catalog.BindingRouteReady(ctx, s.db, row)
		if err != nil {
			return nil, err
		}
		if !ready {
			continue
		}
		value := string(row.Adapter)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		protocols = append(protocols, value)
	}
	sort.Strings(protocols)
	return protocols, nil
}

func (s *Service) StudentModelPlaza(ctx context.Context, studentID uuid.UUID) (StudentModelPlazaView, error) {
	commonModels, err := s.StudentCommonModels(ctx)
	if err != nil {
		return StudentModelPlazaView{}, err
	}
	libraryModels, err := s.StudentModelLibrary(ctx, studentID)
	if err != nil {
		return StudentModelPlazaView{}, err
	}
	commonIDs := make(map[string]struct{}, len(commonModels))
	for _, item := range commonModels {
		commonIDs[strings.ToLower(item.ModelID)] = struct{}{}
	}
	libraryOnly := make([]ModelView, 0, len(libraryModels))
	for _, item := range libraryModels {
		if _, exists := commonIDs[strings.ToLower(item.ModelID)]; exists {
			continue
		}
		libraryOnly = append(libraryOnly, item)
	}
	return StudentModelPlazaView{
		CommonModels:  commonModels,
		LibraryModels: libraryOnly,
	}, nil
}

func mergeStudentPlazaModels(plaza StudentModelPlazaView) []ModelView {
	seen := make(map[string]struct{}, len(plaza.CommonModels)+len(plaza.LibraryModels))
	result := make([]ModelView, 0, len(plaza.CommonModels)+len(plaza.LibraryModels))
	for _, item := range append(plaza.CommonModels, plaza.LibraryModels...) {
		key := strings.ToLower(item.ModelID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ModelID < result[j].ModelID
	})
	return result
}

func (s *Service) StudentModelLibrary(ctx context.Context, studentID uuid.UUID) ([]ModelView, error) {
	hasProvider, err := s.hasActiveProvider(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	if !hasProvider {
		return []ModelView{}, nil
	}
	links, err := s.db.StudentModelLibrary.Query().
		Where(studentmodellibrary.UserIDEQ(studentID)).
		WithModel().
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list student model library", err)
	}
	result := make([]ModelView, 0, len(links))
	for _, link := range links {
		if link.Edges.Model == nil {
			continue
		}
		item := modelView(link.Edges.Model)
		item, err = s.enrichStudentModelView(ctx, item, link.Edges.Model.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ModelID < result[j].ModelID
	})
	return filterStudentVisibleModels(result), nil
}

func (s *Service) StudentCommonModels(ctx context.Context) ([]ModelView, error) {
	hasProvider, err := s.hasActiveProvider(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	if !hasProvider {
		return []ModelView{}, nil
	}
	rows, err := s.db.Model.Query().
		Where(
			entmodel.StatusEQ(entmodel.StatusActive),
			entmodel.IsCommonEQ(true),
			entmodel.CreditMultiplierMilliNotNil(),
			entmodel.CreditMultiplierMilliGT(0),
		).
		Order(ent.Asc(entmodel.FieldModelID)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list common models", err)
	}
	result := make([]ModelView, 0, len(rows))
	for _, row := range rows {
		item := modelView(row)
		item, err = s.enrichStudentModelView(ctx, item, row.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return filterStudentVisibleModels(result), nil
}

func (s *Service) StudentCatalogSearch(ctx context.Context, query string, page, pageSize int) (ModelCatalogPage, error) {
	hasProvider, err := s.hasActiveProvider(ctx)
	if err != nil {
		return ModelCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	if !hasProvider {
		return ModelCatalogPage{Items: []ModelCatalogCandidate{}, Page: page, PageSize: pageSize, Total: 0}, nil
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}
	rows, err := s.db.Model.Query().
		Where(
			entmodel.StatusEQ(entmodel.StatusActive),
			entmodel.CreditMultiplierMilliNotNil(),
			entmodel.CreditMultiplierMilliGT(0),
		).
		Order(ent.Asc(entmodel.FieldModelID)).
		All(ctx)
	if err != nil {
		return ModelCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "list enabled models", err)
	}
	filtered := make([]ModelCatalogCandidate, 0, len(rows))
	for _, row := range rows {
		if !modelMatchesStudentQuery(row, query) {
			continue
		}
		if !modelEnabledForStudents(row) {
			continue
		}
		view := modelView(row)
		view, err = s.enrichStudentModelView(ctx, view, row.ID)
		if err != nil {
			return ModelCatalogPage{}, err
		}
		if !modelViewEnabledForStudents(view) {
			continue
		}
		filtered = append(filtered, modelCatalogCandidateFromPlatformModel(view))
	}
	total := len(filtered)
	start := (page - 1) * pageSize
	if start >= total {
		return ModelCatalogPage{Items: []ModelCatalogCandidate{}, Page: page, PageSize: pageSize, Total: total}, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return ModelCatalogPage{
		Items:    filtered[start:end],
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}, nil
}

func (s *Service) AddStudentModel(ctx context.Context, studentID uuid.UUID, publicModelID string) (ModelView, error) {
	modelID, err := normalizeSingleModelID(publicModelID)
	if err != nil {
		return ModelView{}, err
	}
	row, err := s.db.Model.Query().Where(entmodel.ModelIDEQ(modelID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ModelView{}, domain.NewError(domain.CodeNotFound, "model not found in platform catalog")
	}
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model", err)
	}
	if issue := studentModelPublicationIssue(row); issue != "" {
		return ModelView{}, domain.NewError(domain.CodeValidation, issue)
	}
	view, err := s.enrichStudentModelView(ctx, modelView(row), row.ID)
	if err != nil {
		return ModelView{}, err
	}
	if !view.RouteReady {
		return ModelView{}, domain.NewError(domain.CodeValidation, "model has no usable route")
	}
	exists, err := studentHasModelInLibrary(ctx, s.db, studentID, row.ID)
	if err != nil {
		return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query student model library", err)
	}
	if !exists {
		if _, err := s.db.StudentModelLibrary.Create().
			SetUserID(studentID).
			SetModelID(row.ID).
			Save(ctx); err != nil && !ent.IsConstraintError(err) {
			return ModelView{}, domain.WrapError(domain.CodeDependencyUnavailable, "add student model", err)
		}
	}
	return view, nil
}

func (s *Service) RemoveStudentModel(ctx context.Context, studentID uuid.UUID, publicModelID string) error {
	modelID, err := normalizeSingleModelID(publicModelID)
	if err != nil {
		return err
	}
	row, err := s.db.Model.Query().Where(entmodel.ModelIDEQ(modelID)).Only(ctx)
	if ent.IsNotFound(err) {
		return domain.NewError(domain.CodeNotFound, "model not found")
	}
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query model", err)
	}
	deleted, err := s.db.StudentModelLibrary.Delete().
		Where(
			studentmodellibrary.UserIDEQ(studentID),
			studentmodellibrary.ModelIDEQ(row.ID),
		).
		Exec(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "remove student model", err)
	}
	if deleted == 0 {
		return domain.NewError(domain.CodeNotFound, "model is not in your library")
	}
	return nil
}

func studentHasModelInLibrary(ctx context.Context, client *ent.Client, studentID, platformModelID uuid.UUID) (bool, error) {
	return client.StudentModelLibrary.Query().
		Where(
			studentmodellibrary.UserIDEQ(studentID),
			studentmodellibrary.ModelIDEQ(platformModelID),
		).
		Exist(ctx)
}

func (s *Service) studentCanSelectModel(ctx context.Context, studentID uuid.UUID, row *ent.Model) (bool, error) {
	if !modelEnabledForStudents(row) {
		return false, nil
	}
	routable, err := s.modelHasActiveRoute(ctx, row.ID)
	if err != nil {
		return false, err
	}
	if !routable {
		return false, nil
	}
	if row.IsCommon {
		return true, nil
	}
	return studentHasModelInLibrary(ctx, s.db, studentID, row.ID)
}
