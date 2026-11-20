package controlplane

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Service) UpdateTeacherMemberQuota(ctx context.Context, teacherID, projectID, userID uuid.UUID, quota int64, reason string, increaseOnly bool) error {
	update := s.usage.UpdateStudentQuotaAsTeacher
	if increaseOnly {
		update = s.usage.IncreaseStudentQuotaAsTeacher
	}
	err := update(ctx, teacherID, projectID, userID, quota, reason)
	var appErr *domain.Error
	if err != nil && !errors.As(err, &appErr) {
		return domain.WrapError(domain.CodeDependencyUnavailable, "update student monthly quota", err)
	}
	return err
}
