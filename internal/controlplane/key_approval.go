package controlplane

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
)

// FinalizeKeyApproval allocates project quota and moves a key from
// pending_mentor to approved. Only responsible project mentors may call this.
func (s *Service) FinalizeKeyApproval(ctx context.Context, mentorID, keyID uuid.UUID, quota int64, comment string) (uuid.UUID, error) {
	if quota < 0 || quota > domain.MaxCreditMilli {
		return uuid.Nil, domain.NewError(domain.CodeValidation, "monthly credits are out of range")
	}
	key, err := s.db.APIKey.Query().Where(
		apikey.IDEQ(keyID),
		apikey.StatusEQ(apikey.StatusPendingMentor),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return uuid.Nil, domain.NewError(domain.CodeInvalidTransition, "API key is no longer pending mentor review")
	}
	if err != nil {
		return uuid.Nil, domain.WrapError(domain.CodeDependencyUnavailable, "query API key", err)
	}
	responsible, err := s.db.ProjectMember.Query().Where(
		projectmember.ProjectIDEQ(key.ProjectID),
		projectmember.UserIDEQ(mentorID),
	).Exist(ctx)
	if err != nil {
		return uuid.Nil, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor responsibility", err)
	}
	if !responsible {
		return uuid.Nil, domain.NewError(domain.CodeNotFound, "API key application not found")
	}
	ownerID, approveErr := s.usage.ApproveKey(ctx, mentorID, keyID, quota, comment)
	if approveErr != nil {
		var appErr *domain.Error
		if errors.As(approveErr, &appErr) {
			return uuid.Nil, approveErr
		}
		dependencyErr := domain.WrapError(domain.CodeDependencyUnavailable, "approve API key allocation", approveErr)
		dependencyErr.Details = map[string]any{"operation": "api_key_approval", "state_changed": false}
		return uuid.Nil, dependencyErr
	}
	return ownerID, nil
}
