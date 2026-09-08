package controlplane

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/studentprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/user"
)

type StudentAccountView struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Email               string     `json:"email"`
	Status              string     `json:"status"`
	HasMentorProfile    bool       `json:"has_mentor_profile"`
	MentorProfileStatus *string    `json:"mentor_profile_status,omitempty"`
	MentorGrantedAt     *time.Time `json:"mentor_granted_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

func (s *Service) TeacherStudents(ctx context.Context, search string) ([]StudentAccountView, error) {
	query := s.db.User.Query().
		Where(
			user.RoleNEQ(user.RoleTeacher),
			user.HasStudentProfileWith(),
		).
		WithStudentProfile().
		WithMentorProfile().
		Order(ent.Asc(user.FieldName), ent.Asc(user.FieldID))
	if search = strings.TrimSpace(search); search != "" {
		query.Where(user.Or(user.NameContainsFold(search), user.EmailContainsFold(search)))
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list students", err)
	}
	result := make([]StudentAccountView, 0, len(rows))
	for _, row := range rows {
		result = append(result, studentAccountView(row))
	}
	return result, nil
}

func studentAccountView(row *ent.User) StudentAccountView {
	view := StudentAccountView{
		ID:        row.ID.String(),
		Name:      row.Name,
		Email:     row.Email,
		Status:    string(row.Status),
		CreatedAt: row.CreatedAt,
	}
	if profile := row.Edges.MentorProfile; profile != nil {
		view.HasMentorProfile = true
		status := string(profile.Status)
		view.MentorProfileStatus = &status
		if profile.GrantedAt != nil {
			view.MentorGrantedAt = profile.GrantedAt
		}
	}
	return view
}

func (s *Service) GrantMentorProfile(ctx context.Context, teacherID, studentUserID uuid.UUID) (StudentAccountView, error) {
	account, err := s.db.User.Query().
		Where(
			user.IDEQ(studentUserID),
			user.RoleNEQ(user.RoleTeacher),
			user.HasStudentProfileWith(studentprofile.StatusEQ(studentprofile.StatusActive)),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return StudentAccountView{}, domain.NewError(domain.CodeNotFound, "student not found")
	}
	if err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query student", err)
	}
	now := s.now().UTC()
	existing, err := s.db.MentorProfile.Query().Where(mentorprofile.UserIDEQ(studentUserID)).Only(ctx)
	if ent.IsNotFound(err) {
		if _, err := s.db.MentorProfile.Create().
			SetUserID(studentUserID).
			SetStatus(mentorprofile.StatusActive).
			SetGrantedBy(teacherID).
			SetGrantedAt(now).
			Save(ctx); err != nil {
			return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create mentor profile", err)
		}
	} else if err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor profile", err)
	} else {
		update := existing.Update().SetStatus(mentorprofile.StatusActive).SetGrantedAt(now).SetGrantedBy(teacherID)
		if _, err := update.Save(ctx); err != nil {
			return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update mentor profile", err)
		}
	}
	_ = account
	row, err := s.db.User.Query().
		Where(user.IDEQ(studentUserID)).
		WithStudentProfile().
		WithMentorProfile().
		Only(ctx)
	if err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "reload student", err)
	}
	return studentAccountView(row), nil
}

func (s *Service) RevokeMentorProfile(ctx context.Context, studentUserID uuid.UUID) (StudentAccountView, error) {
	profile, err := s.db.MentorProfile.Query().
		Where(
			mentorprofile.UserIDEQ(studentUserID),
			mentorprofile.StatusEQ(mentorprofile.StatusActive),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return StudentAccountView{}, domain.NewError(domain.CodeNotFound, "mentor profile not found")
	}
	if err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor profile", err)
	}
	if _, err := profile.Update().SetStatus(mentorprofile.StatusDisabled).Save(ctx); err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "revoke mentor profile", err)
	}
	row, err := s.db.User.Query().
		Where(user.IDEQ(studentUserID)).
		WithStudentProfile().
		WithMentorProfile().
		Only(ctx)
	if ent.IsNotFound(err) {
		return StudentAccountView{}, domain.NewError(domain.CodeNotFound, "student not found")
	}
	if err != nil {
		return StudentAccountView{}, domain.WrapError(domain.CodeDependencyUnavailable, "reload student", err)
	}
	return studentAccountView(row), nil
}

func (s *Service) requireActiveMentorUser(ctx context.Context, mentorID uuid.UUID) (*ent.User, error) {
	row, err := s.db.User.Query().
		Where(
			user.IDEQ(mentorID),
			user.StatusEQ(user.StatusActive),
			user.HasMentorProfileWith(mentorprofile.StatusEQ(mentorprofile.StatusActive)),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, domain.NewError(domain.CodeNotFound, "mentor not found")
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor", err)
	}
	return row, nil
}

func (s *Service) ensureActiveStudentProfile(ctx context.Context, userID uuid.UUID) error {
	exists, err := s.db.StudentProfile.Query().
		Where(
			studentprofile.UserIDEQ(userID),
			studentprofile.StatusEQ(studentprofile.StatusActive),
		).
		Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query student profile", err)
	}
	if !exists {
		return domain.NewError(domain.CodeForbidden, "student profile is required")
	}
	return nil
}
