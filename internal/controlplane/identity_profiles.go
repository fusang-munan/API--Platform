package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/studentprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/user"
)

const (
	IdentityStudent = "student"
	IdentityMentor  = "mentor"
	IdentityTeacher = "teacher"
)

func (s *Service) listIdentities(ctx context.Context, account *ent.User) ([]string, error) {
	if account.Status != user.StatusActive {
		return nil, nil
	}
	if account.Role == user.RoleTeacher {
		return []string{IdentityTeacher}, nil
	}
	identities := make([]string, 0, 2)
	hasStudent, err := s.db.StudentProfile.Query().
		Where(
			studentprofile.UserIDEQ(account.ID),
			studentprofile.StatusEQ(studentprofile.StatusActive),
		).
		Exist(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query student profile", err)
	}
	if hasStudent {
		identities = append(identities, IdentityStudent)
	}
	hasMentor, err := s.db.MentorProfile.Query().
		Where(
			mentorprofile.UserIDEQ(account.ID),
			mentorprofile.StatusEQ(mentorprofile.StatusActive),
		).
		Exist(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor profile", err)
	}
	if hasMentor {
		identities = append(identities, IdentityMentor)
	}
	return identities, nil
}

func (s *Service) validateSessionIdentity(ctx context.Context, account *ent.User, identity string) error {
	if account.Status != user.StatusActive {
		return domain.NewError(domain.CodeAccountDisabled, "account is not active")
	}
	identities, err := s.listIdentities(ctx, account)
	if err != nil {
		return err
	}
	for _, item := range identities {
		if item == identity {
			return nil
		}
	}
	return domain.NewError(domain.CodeForbidden, "identity is not available")
}

func (s *Service) accountIdentities(ctx context.Context, userID uuid.UUID) ([]string, error) {
	account, err := s.db.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, domain.NewError(domain.CodeAuthenticationRequired, "user no longer exists")
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query user", err)
	}
	return s.listIdentities(ctx, account)
}

func (s *Service) EnsureActiveMentorProfile(ctx context.Context, userID uuid.UUID) error {
	_, err := s.requireActiveMentorUser(ctx, userID)
	return err
}

func (s *Service) EnsureActiveStudentProfile(ctx context.Context, userID uuid.UUID) error {
	return s.ensureActiveStudentProfile(ctx, userID)
}

func userViewWithIdentity(account *ent.User, identity string) UserView {
	view := userView(account)
	view.Role = identity
	return view
}
