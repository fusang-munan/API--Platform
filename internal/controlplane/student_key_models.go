package controlplane

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeyaudit"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymodel"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/organization"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
)

type StudentAPIKeyModelsView struct {
	ModelIDs      []string                    `json:"model_ids"`
	AddedModelIDs []string                    `json:"added_model_ids"`
	Warnings      []StudentAPIKeyModelWarning `json:"warnings"`
}

type StudentAPIKeyModelWarning struct {
	ModelID string `json:"model_id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type StudentAPIKeyModelRemovalView struct {
	ModelIDs        []string `json:"model_ids"`
	RemovedModelIDs []string `json:"removed_model_ids"`
}

func (s *Service) AddStudentAPIKeyModels(ctx context.Context, studentID, keyID uuid.UUID, modelIDs []string) (StudentAPIKeyModelsView, error) {
	requested, err := normalizeStudentKeyModelIDs(modelIDs)
	if err != nil {
		return StudentAPIKeyModelsView{}, err
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start key model transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	key, err := lockStudentAPIKey(ctx, tx, studentID, keyID)
	if err != nil {
		return StudentAPIKeyModelsView{}, err
	}
	if key.Status != apikey.StatusActive {
		return StudentAPIKeyModelsView{}, domain.NewError(domain.CodeInvalidTransition, "API key must be active to add models")
	}
	projectActive, err := tx.Project.Query().Where(
		project.IDEQ(key.ProjectID),
		project.StatusEQ(project.StatusActive),
		project.HasOrganizationWith(organization.StatusEQ(organization.StatusActive)),
		project.HasMembershipsWith(projectmember.UserIDEQ(studentID)),
	).Exist(ctx)
	if err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "verify API key project access", err)
	}
	if !projectActive {
		return StudentAPIKeyModelsView{}, domain.NewError(domain.CodeInvalidTransition, "API key project access is inactive")
	}

	links, err := studentAPIKeyModelLinks(ctx, tx, keyID)
	if err != nil {
		return StudentAPIKeyModelsView{}, err
	}
	view := StudentAPIKeyModelsView{
		ModelIDs:      make([]string, 0, len(links)+len(requested)),
		AddedModelIDs: []string{},
		Warnings:      []StudentAPIKeyModelWarning{},
	}
	authorized := make(map[uuid.UUID]bool, len(links)+len(requested))
	for _, link := range links {
		authorized[link.ModelID] = true
		view.ModelIDs = append(view.ModelIDs, link.Edges.Model.ModelID)
	}
	for _, modelID := range requested {
		row, err := tx.Model.Query().Where(entmodel.ModelIDEQ(modelID)).ForShare().Only(ctx)
		if ent.IsNotFound(err) {
			return StudentAPIKeyModelsView{}, studentKeyModelError(domain.CodeNotFound, "model not found in platform catalog", modelID)
		}
		if err != nil {
			return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "resolve key model", err)
		}
		if authorized[row.ID] {
			continue
		}
		protocols, err := catalog.LoadPublishedProtocols(ctx, tx.Client(), []uuid.UUID{row.ID}, s.security)
		if err != nil {
			return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query model availability", err)
		}
		availability := catalog.SummarizeModelAvailability(row.ModelID, row.Status, protocols[row.ID])
		if availability.Availability == catalog.AvailabilityMaintenance {
			return StudentAPIKeyModelsView{}, studentKeyModelError(domain.CodeModelNotReady, availability.AvailabilityReason, row.ModelID)
		}
		if issue := studentModelPublicationIssue(row); issue != "" {
			return StudentAPIKeyModelsView{}, studentKeyModelError(domain.CodeModelNotReady, issue, row.ModelID)
		}
		if availability.Availability == catalog.AvailabilityUnverified {
			view.Warnings = append(view.Warnings, StudentAPIKeyModelWarning{
				ModelID: row.ModelID, Code: "MODEL_UNVERIFIED", Message: availability.AvailabilityReason,
			})
		}
		if err := tx.APIKeyModel.Create().SetAPIKeyID(keyID).SetModelID(row.ID).Exec(ctx); err != nil {
			return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "authorize API key model", err)
		}
		if err := tx.APIKeyAudit.Create().SetAPIKeyID(keyID).SetActorUserID(studentID).
			SetAction(apikeyaudit.ActionStudentModelAdded).SetComment(row.ModelID).Exec(ctx); err != nil {
			return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "audit API key model authorization", err)
		}
		authorized[row.ID] = true
		view.ModelIDs = append(view.ModelIDs, row.ModelID)
		view.AddedModelIDs = append(view.AddedModelIDs, row.ModelID)
	}
	sort.Strings(view.ModelIDs)
	if err := tx.Commit(); err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit key model authorization", err)
	}
	return view, nil
}

func (s *Service) RemoveStudentAPIKeyModels(ctx context.Context, studentID, keyID uuid.UUID, modelIDs []string) (StudentAPIKeyModelRemovalView, error) {
	requested, err := normalizeStudentKeyModelIDs(modelIDs)
	if err != nil {
		return StudentAPIKeyModelRemovalView{}, err
	}
	remove := make(map[string]bool, len(requested))
	for _, modelID := range requested {
		remove[strings.ToLower(modelID)] = true
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return StudentAPIKeyModelRemovalView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start key model removal", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockStudentAPIKey(ctx, tx, studentID, keyID); err != nil {
		return StudentAPIKeyModelRemovalView{}, err
	}
	links, err := studentAPIKeyModelLinks(ctx, tx, keyID)
	if err != nil {
		return StudentAPIKeyModelRemovalView{}, err
	}
	view := StudentAPIKeyModelRemovalView{
		ModelIDs:        make([]string, 0, len(links)),
		RemovedModelIDs: []string{},
	}
	for _, link := range links {
		modelID := link.Edges.Model.ModelID
		if !remove[strings.ToLower(modelID)] {
			view.ModelIDs = append(view.ModelIDs, modelID)
			continue
		}
		if err := tx.APIKeyModel.DeleteOneID(link.ID).Exec(ctx); err != nil {
			return StudentAPIKeyModelRemovalView{}, domain.WrapError(domain.CodeDependencyUnavailable, "remove API key model authorization", err)
		}
		if err := tx.APIKeyAudit.Create().SetAPIKeyID(keyID).SetActorUserID(studentID).
			SetAction(apikeyaudit.ActionStudentModelRemoved).SetComment(modelID).Exec(ctx); err != nil {
			return StudentAPIKeyModelRemovalView{}, domain.WrapError(domain.CodeDependencyUnavailable, "audit API key model removal", err)
		}
		view.RemovedModelIDs = append(view.RemovedModelIDs, modelID)
	}
	sort.Strings(view.ModelIDs)
	sort.Strings(view.RemovedModelIDs)
	if err := tx.Commit(); err != nil {
		return StudentAPIKeyModelRemovalView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit key model removal", err)
	}
	return view, nil
}

func normalizeStudentKeyModelIDs(modelIDs []string) ([]string, error) {
	if len(modelIDs) < 1 || len(modelIDs) > 100 {
		return nil, domain.NewError(domain.CodeValidation, "model_ids must contain 1 to 100 items")
	}
	requested := make([]string, 0, len(modelIDs))
	for _, value := range modelIDs {
		modelID, err := normalizeSingleModelID(value)
		if err != nil {
			return nil, err
		}
		requested = append(requested, modelID)
	}
	return requested, nil
}

func lockStudentAPIKey(ctx context.Context, tx *ent.Tx, studentID, keyID uuid.UUID) (*ent.APIKey, error) {
	// Serialize model changes and coordinate with changes to the key's status.
	key, err := tx.APIKey.Query().Where(apikey.IDEQ(keyID), apikey.OwnerUserIDEQ(studentID)).ForUpdate().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, domain.NewError(domain.CodeNotFound, "API key not found")
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "lock API key", err)
	}
	return key, nil
}

func studentAPIKeyModelLinks(ctx context.Context, tx *ent.Tx, keyID uuid.UUID) ([]*ent.APIKeyModel, error) {
	links, err := tx.APIKeyModel.Query().Where(apikeymodel.APIKeyIDEQ(keyID)).WithModel().All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list authorized API key models", err)
	}
	return links, nil
}

func studentKeyModelError(code, message, modelID string) error {
	return &domain.Error{Code: code, Message: message, Details: map[string]string{"model_id": modelID}}
}
