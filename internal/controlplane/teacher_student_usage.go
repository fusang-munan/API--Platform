package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func (s *Service) ListTeacherStudentUsage(ctx context.Context, q usage.TeacherStudentUsageQuery) (usage.TeacherStudentUsagePage, error) {
	result, err := s.usage.ListTeacherStudentUsage(ctx, q)
	if err != nil && domain.ErrorCode(err) != domain.CodeValidation {
		return result, domain.WrapError(domain.CodeDependencyUnavailable, "读取学生用量报表失败", err)
	}
	return result, err
}

func (s *Service) TeacherStudentUsage(ctx context.Context, userID uuid.UUID, scope usage.TeacherStudentUsageScope) (usage.TeacherStudentUsageDetail, error) {
	result, err := s.usage.TeacherStudentUsage(ctx, userID, scope)
	if err != nil && domain.ErrorCode(err) != domain.CodeNotFound {
		return result, domain.WrapError(domain.CodeDependencyUnavailable, "读取学生用量明细失败", err)
	}
	return result, err
}
