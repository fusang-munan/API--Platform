package controlplane

import (
	"context"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/studentmodellibrary"
)

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
		RawEntry: map[string]any{
			"protocols": view.Protocols, "available_protocols": view.AvailableProtocols,
			"recommended_protocol": view.RecommendedProtocol, "probe_state": view.ProbeState,
			"availability": view.Availability, "availability_state": view.AvailabilityState,
			"availability_reason": view.AvailabilityReason, "last_probe_at": view.LastProbeAt,
			"route_ready": view.RouteReady,
		},
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
	setStudentModelPrimaryBinding(&item, binding)
	if item.ModelAvailability == nil {
		protocols, err := catalog.LoadPublishedProtocols(ctx, s.db, []uuid.UUID{platformModelID}, s.security)
		if err != nil {
			return item, domain.WrapError(domain.CodeDependencyUnavailable, "query model availability", err)
		}
		item.setAvailability(catalog.SummarizeModelAvailability(item.ModelID, entmodel.Status(item.Status), protocols[platformModelID]))
	}
	return item, nil
}

func setStudentModelPrimaryBinding(item *ModelView, binding *ent.ModelBinding) {
	if binding != nil && binding.Edges.Provider != nil {
		item.ProviderID = binding.Edges.Provider.ID.String()
		item.ProviderName = binding.Edges.Provider.Name
		item.PrimaryAdapter = string(binding.Adapter)
	}
}

func (s *Service) setStudentModelStates(ctx context.Context, groups ...[]ModelView) error {
	ids := make([]string, 0)
	for _, group := range groups {
		for _, item := range group {
			if item.ModelAvailability != nil && item.Status == string(entmodel.StatusActive) {
				ids = append(ids, item.ModelID)
			}
		}
	}
	channels, err := beichen.ReadStudentModelCatalog(ctx, s.sqlDB, ids)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "read student model catalog evidence", err)
	}
	for _, group := range groups {
		for i := range group {
			item := &group[i]
			if item.ModelAvailability != nil {
				item.ModelAvailability.SetStudentState(item.ModelID, entmodel.Status(item.Status), channels[item.ModelID])
			}
		}
	}
	return nil
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
	result = filterStudentVisibleModels(result)
	return result, s.setStudentModelStates(ctx, result)
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
	result = filterStudentVisibleModels(result)
	return result, s.setStudentModelStates(ctx, result)
}

const (
	studentCatalogMaxQueryRunes = 128
	studentCatalogMaxPage       = 10_000
	studentCatalogMaxPageSize   = 50
	studentCatalogProtocolBatch = 1_024
)

type StudentCatalogPage struct {
	ModelCatalogPage
	Directory *studentCatalogDirectory `json:"directory,omitempty"`
}

type studentCatalogDirectory struct {
	Models []studentCatalogDirectoryModel `json:"models"`
}

type studentCatalogDirectoryModel struct {
	ModelCatalogCandidate
	WithGrant    string `json:"with_grant"`
	WithoutGrant string `json:"without_grant"`
}

// The catalog endpoint has no student ID. Publish the two directory outcomes
// without exposing any grants. The Key endpoint supplies the caller's grants.
func studentDirectoryModel(model ModelView) studentCatalogDirectoryModel {
	item := studentCatalogDirectoryModel{ModelCatalogCandidate: modelCatalogCandidateFromPlatformModel(model)}
	switch model.AvailabilityState {
	case catalog.AvailabilityAvailable, catalog.AvailabilityDegraded:
		item.WithGrant, item.WithoutGrant = "callable", "addable"
	case catalog.AvailabilityUnverified:
		item.WithGrant, item.WithoutGrant = "unverified", "unverified"
	default:
		item.WithGrant, item.WithoutGrant = "historical", "maintenance"
	}
	return item
}

func (s *Service) StudentCatalogSearch(ctx context.Context, query string, page, pageSize int) (StudentCatalogPage, error) {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > studentCatalogMaxQueryRunes || strings.ContainsRune(query, 0) {
		return StudentCatalogPage{}, domain.NewError(domain.CodeValidation, "q 须为 128 字以内的有效文本")
	}
	if page < 1 || page > studentCatalogMaxPage {
		return StudentCatalogPage{}, domain.NewError(domain.CodeValidation, "page 须为 1 到 10000 之间的整数")
	}
	if pageSize < 1 || pageSize > studentCatalogMaxPageSize {
		return StudentCatalogPage{}, domain.NewError(domain.CodeValidation, "page_size 须为 1 到 50 之间的整数")
	}
	query = strings.ToLower(strings.TrimSpace(query))
	result := StudentCatalogPage{ModelCatalogPage: ModelCatalogPage{Items: []ModelCatalogCandidate{}, Page: page, PageSize: pageSize}}
	if query == "" && page == 1 {
		result.Directory = &studentCatalogDirectory{Models: []studentCatalogDirectoryModel{}}
	}
	hasProvider, err := s.hasActiveProvider(ctx)
	if err != nil {
		return StudentCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	if !hasProvider {
		return result, nil
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
		return StudentCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "list enabled models", err)
	}
	matches := slices.DeleteFunc(rows, func(row *ent.Model) bool {
		return !modelMatchesStudentQuery(row, query)
	})
	type publishedModel struct {
		id    uuid.UUID
		index int
	}
	published := make([]publishedModel, 0, len(matches))
	models := make([]ModelView, 0, len(matches))
	// Route readiness affects the total, so evaluate it before slicing the page.
	// Keep each batch well within PostgreSQL's bind parameter limit.
	for batch := range slices.Chunk(matches, studentCatalogProtocolBatch) {
		ids := make([]uuid.UUID, 0, len(batch))
		for _, row := range batch {
			ids = append(ids, row.ID)
		}
		protocols, err := catalog.LoadPublishedProtocols(ctx, s.db, ids, s.security)
		if err != nil {
			return StudentCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model availability", err)
		}
		for _, row := range batch {
			view := modelView(row)
			view.setAvailability(catalog.SummarizeModelAvailability(row.ModelID, row.Status, protocols[row.ID]))
			ready := modelViewEnabledForStudents(view)
			if ready {
				published = append(published, publishedModel{id: row.ID, index: len(models)})
			}
			if ready || result.Directory != nil {
				models = append(models, view)
			}
		}
	}
	result.Total = len(published)
	if result.Directory != nil {
		if err := s.setStudentModelStates(ctx, models); err != nil {
			return StudentCatalogPage{}, err
		}
		for _, model := range models {
			result.Directory.Models = append(result.Directory.Models, studentDirectoryModel(model))
		}
	}
	// The validated bounds keep this multiplication below 500,000.
	start := (page - 1) * pageSize
	if start >= result.Total {
		return result, nil
	}
	entries := published[start : start+min(pageSize, result.Total-start)]
	ids := make([]uuid.UUID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.id)
	}
	bindings, err := s.db.ModelBinding.Query().
		Where(modelbinding.ModelIDIn(ids...), modelbinding.StatusEQ(modelbinding.StatusActive)).
		WithProvider().
		Order(ent.Asc(modelbinding.FieldPriority), ent.Asc(modelbinding.FieldID)).
		All(ctx)
	if err != nil {
		return StudentCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model bindings", err)
	}
	primary := make(map[uuid.UUID]*ent.ModelBinding, len(entries))
	for _, binding := range bindings {
		if primary[binding.ModelID] == nil {
			primary[binding.ModelID] = binding
		}
	}
	pageModels := make([]ModelView, 0, len(entries))
	for _, entry := range entries {
		view := models[entry.index]
		setStudentModelPrimaryBinding(&view, primary[entry.id])
		pageModels = append(pageModels, view)
	}
	if result.Directory == nil {
		if err := s.setStudentModelStates(ctx, pageModels); err != nil {
			return StudentCatalogPage{}, err
		}
	}
	for _, model := range pageModels {
		result.Items = append(result.Items, modelCatalogCandidateFromPlatformModel(model))
	}
	return result, nil
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
