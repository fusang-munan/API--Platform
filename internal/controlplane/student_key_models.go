package controlplane

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeyaudit"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymodel"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/organization"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
)

type StudentAPIKeyModelsView struct {
	ModelIDs      []string `json:"model_ids"`
	AddedModelIDs []string `json:"added_model_ids"`
}

func (s *Service) AddStudentAPIKeyModels(ctx context.Context, studentID, keyID uuid.UUID, modelIDs []string) (StudentAPIKeyModelsView, error) {
	if len(modelIDs) < 1 || len(modelIDs) > 100 {
		return StudentAPIKeyModelsView{}, domain.NewError(domain.CodeValidation, "model_ids must contain 1 to 100 items")
	}
	requested := make([]string, 0, len(modelIDs))
	for _, value := range modelIDs {
		modelID, err := normalizeSingleModelID(value)
		if err != nil {
			return StudentAPIKeyModelsView{}, err
		}
		requested = append(requested, modelID)
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start key model transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Serialize grants for this key and wait for any concurrent Key status change.
	key, err := tx.APIKey.Query().Where(apikey.IDEQ(keyID), apikey.OwnerUserIDEQ(studentID)).ForUpdate().Only(ctx)
	if ent.IsNotFound(err) {
		return StudentAPIKeyModelsView{}, domain.NewError(domain.CodeNotFound, "API key not found")
	}
	if err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "lock API key", err)
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

	links, err := tx.APIKeyModel.Query().Where(apikeymodel.APIKeyIDEQ(keyID)).WithModel().All(ctx)
	if err != nil {
		return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "list authorized API key models", err)
	}
	view := StudentAPIKeyModelsView{
		ModelIDs:      make([]string, 0, len(links)+len(requested)),
		AddedModelIDs: []string{},
	}
	authorized := make(map[uuid.UUID]bool, len(links)+len(requested))
	for _, link := range links {
		authorized[link.ModelID] = true
		view.ModelIDs = append(view.ModelIDs, link.Edges.Model.ModelID)
	}
	for _, modelID := range requested {
		row, err := tx.Model.Query().Where(entmodel.ModelIDEQ(modelID)).ForShare().Only(ctx)
		if ent.IsNotFound(err) {
			return StudentAPIKeyModelsView{}, studentKeyModelError(domain.CodeNotFound, "model is not available in your model plaza", modelID)
		}
		if err != nil {
			return StudentAPIKeyModelsView{}, domain.WrapError(domain.CodeDependencyUnavailable, "resolve key model", err)
		}
		if authorized[row.ID] {
			continue
		}
		if err := s.validateStudentKeyModel(ctx, tx, studentID, row); err != nil {
			return StudentAPIKeyModelsView{}, err
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

func (s *Service) validateStudentKeyModel(ctx context.Context, tx *ent.Tx, studentID uuid.UUID, row *ent.Model) error {
	if issue := studentModelPublicationIssue(row); issue != "" {
		return studentKeyModelError(domain.CodeModelNotReady, issue, row.ModelID)
	}
	if !row.IsCommon {
		inLibrary, err := studentHasModelInLibrary(ctx, tx.Client(), studentID, row.ID)
		if err != nil {
			return domain.WrapError(domain.CodeDependencyUnavailable, "verify student model library", err)
		}
		if !inLibrary {
			return studentKeyModelError(domain.CodeNotFound, "model is not available in your model plaza", row.ModelID)
		}
	}
	bindings, err := tx.ModelBinding.Query().Where(
		modelbinding.ModelIDEQ(row.ID), modelbinding.StatusEQ(modelbinding.StatusActive),
	).All(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query model routes", err)
	}
	ready := false
	for _, binding := range bindings {
		credential, _, err := catalog.ResolveBindingCredential(ctx, tx.Client(), s.security, binding)
		if err != nil {
			return domain.WrapError(domain.CodeDependencyUnavailable, "check model route", err)
		}
		if credential != "" {
			ready = true
			break
		}
	}
	if !ready {
		return studentKeyModelError(domain.CodeModelNotReady, "model has no usable route for this API key", row.ModelID)
	}
	return nil
}

func studentKeyModelError(code, message, modelID string) error {
	return &domain.Error{Code: code, Message: message, Details: map[string]string{"model_id": modelID}}
}
