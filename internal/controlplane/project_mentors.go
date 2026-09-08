package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/user"
)

type MentorSummaryView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (s *Service) ProjectMentorCandidates(
	ctx context.Context,
	projectID uuid.UUID,
	search string,
	cursor *MentorCandidateCursor,
	limit int,
) ([]MentorCandidateView, *string, error) {
	if limit < 1 || limit > 50 {
		return nil, nil, domain.NewError(domain.CodeValidation, "limit must be between 1 and 50")
	}
	exists, err := s.db.Project.Query().Where(project.IDEQ(projectID)).Exist(ctx)
	if err != nil {
		return nil, nil, domain.WrapError(domain.CodeDependencyUnavailable, "query project", err)
	}
	if !exists {
		return nil, nil, domain.NewError(domain.CodeNotFound, "project not found")
	}
	query := s.db.User.Query().Where(
		user.StatusEQ(user.StatusActive),
		user.HasMentorProfileWith(mentorprofile.StatusEQ(mentorprofile.StatusActive)),
	).Order(ent.Asc(user.FieldName), ent.Asc(user.FieldID))
	if search = strings.TrimSpace(search); search != "" {
		query.Where(user.Or(user.NameContainsFold(search), user.EmailContainsFold(search)))
	}
	if cursor != nil {
		query.Where(user.Or(
			user.NameGT(cursor.Name),
			user.And(user.NameEQ(cursor.Name), user.IDGT(cursor.ID)),
		))
	}
	rows, err := query.Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, nil, domain.WrapError(domain.CodeDependencyUnavailable, "list mentor candidates", err)
	}
	var nextCursor *string
	if len(rows) > limit {
		last := rows[limit-1]
		encoded := encodeMentorCandidateCursor(last.Name, last.ID)
		nextCursor = &encoded
		rows = rows[:limit]
	}
	result := make([]MentorCandidateView, 0, len(rows))
	for _, row := range rows {
		member, err := s.db.ProjectMember.Query().Where(
			projectmember.ProjectIDEQ(projectID),
			projectmember.UserIDEQ(row.ID),
		).Exist(ctx)
		if err != nil {
			return nil, nil, domain.WrapError(domain.CodeDependencyUnavailable, "query project membership", err)
		}
		result = append(result, MentorCandidateView{
			ID: row.ID.String(), Name: row.Name, Email: row.Email, IsMember: member,
		})
	}
	return result, nextCursor, nil
}

func (s *Service) projectMentorsByProjectIDs(ctx context.Context, projectIDs []uuid.UUID) (map[uuid.UUID][]MentorSummaryView, error) {
	result := make(map[uuid.UUID][]MentorSummaryView, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}
	members, err := s.db.ProjectMember.Query().Where(
		projectmember.ProjectIDIn(projectIDs...),
		projectmember.HasUserWith(
			user.StatusEQ(user.StatusActive),
			user.HasMentorProfileWith(mentorprofile.StatusEQ(mentorprofile.StatusActive)),
		),
	).WithUser().Order(ent.Asc(projectmember.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query project mentors", err)
	}
	for _, member := range members {
		if member.Edges.User == nil {
			continue
		}
		u := member.Edges.User
		result[member.ProjectID] = append(result[member.ProjectID], MentorSummaryView{
			ID: u.ID.String(), Name: u.Name, Email: u.Email,
		})
	}
	return result, nil
}

func (s *Service) enrichTeacherProjectViews(ctx context.Context, rows []*ent.Project) ([]ProjectView, error) {
	projectIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		projectIDs = append(projectIDs, row.ID)
	}
	mentors, err := s.projectMentorsByProjectIDs(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	result := make([]ProjectView, 0, len(rows))
	for _, row := range rows {
		projectMentors := mentors[row.ID]
		view := projectView(row, len(projectMentors) > 0)
		view.Mentors = projectMentors
		result = append(result, view)
	}
	return result, nil
}

func (s *Service) RemoveProjectMentor(ctx context.Context, projectID, mentorID uuid.UUID) error {
	exists, err := s.db.Project.Query().Where(project.IDEQ(projectID)).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project", err)
	}
	if !exists {
		return domain.NewError(domain.CodeNotFound, "project not found")
	}
	if _, err := s.requireActiveMentorUser(ctx, mentorID); err != nil {
		return err
	}
	memberExists, err := s.db.ProjectMember.Query().Where(
		projectmember.ProjectIDEQ(projectID),
		projectmember.UserIDEQ(mentorID),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project membership", err)
	}
	if !memberExists {
		return domain.NewError(domain.CodeMentorNotProjectMember, "mentor does not belong to the project")
	}
	var quota int64
	err = s.sqlDB.QueryRowContext(ctx, `SELECT COALESCE(monthly_credit_quota_milli,0) FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, mentorID).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeMentorNotProjectMember, "mentor does not belong to the project")
	}
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query mentor project quota", err)
	}
	if quota > 0 {
		return domain.NewError(domain.CodeInvalidTransition, "mentor has student quota on this project")
	}
	if _, err := s.db.ProjectMember.Delete().Where(
		projectmember.ProjectIDEQ(projectID),
		projectmember.UserIDEQ(mentorID),
	).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "remove project mentor", err)
	}
	return nil
}
