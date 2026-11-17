package controlplane

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprojectapplication"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
)

type ProjectBudgetView struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	Month          string `json:"month"`
	Quota          string `json:"quota"`
	Allocated      string `json:"allocated"`
	Available      string `json:"available"`
	Charged        string `json:"charged"`
}

func (s *Service) ListProjectBudgets(ctx context.Context, month time.Time, organizationID, query string) ([]ProjectBudgetView, error) {
	projects, err := s.db.Project.Query().Order(ent.Asc(project.FieldName)).All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list project budgets", err)
	}
	result := make([]ProjectBudgetView, 0, len(projects))
	for _, row := range projects {
		if organizationID != "" && row.OrganizationID.String() != organizationID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(row.Name), strings.ToLower(query)) {
			continue
		}
		usage, err := s.usage.ProjectUsage(ctx, row.ID, month)
		if err != nil {
			continue
		}
		result = append(result, ProjectBudgetView{
			ID:             row.ID.String(),
			OrganizationID: row.OrganizationID.String(),
			Name:           row.Name,
			Status:         string(row.Status),
			Month:          usage.Month,
			Quota:          usage.Quota,
			Allocated:      usage.Allocated,
			Available:      usage.Available,
			Charged:        usage.Charged,
		})
	}
	return result, nil
}

func (s *Service) UpdateProjectBudget(ctx context.Context, actorID, projectID uuid.UUID, quota int64, reason string) (ProjectBudgetView, error) {
	if err := s.usage.UpdateProjectQuota(ctx, actorID, projectID, quota, reason); err != nil {
		var appErr *domain.Error
		if errors.As(err, &appErr) {
			return ProjectBudgetView{}, err
		}
		return ProjectBudgetView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update project budget", err)
	}
	row, err := s.db.Project.Get(ctx, projectID)
	if ent.IsNotFound(err) {
		return ProjectBudgetView{}, domain.NewError(domain.CodeNotFound, "project not found")
	}
	if err != nil {
		return ProjectBudgetView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query project", err)
	}
	usage, err := s.usage.ProjectUsage(ctx, projectID, time.Now())
	if err != nil {
		return ProjectBudgetView{}, domain.WrapError(domain.CodeDependencyUnavailable, "load project budget", err)
	}
	return ProjectBudgetView{
		ID:             row.ID.String(),
		OrganizationID: row.OrganizationID.String(),
		Name:           row.Name,
		Status:         string(row.Status),
		Month:          usage.Month,
		Quota:          usage.Quota,
		Allocated:      usage.Allocated,
		Available:      usage.Available,
		Charged:        usage.Charged,
	}, nil
}

func (s *Service) AssignProjectMentor(ctx context.Context, teacherID, projectID, mentorID uuid.UUID) error {
	target, err := s.db.Project.Query().Where(project.IDEQ(projectID)).Only(ctx)
	if ent.IsNotFound(err) {
		return domain.NewError(domain.CodeNotFound, "project not found")
	}
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project", err)
	}
	if _, err := s.requireActiveMentorUser(ctx, mentorID); err != nil {
		return err
	}
	exists, err := s.db.ProjectMember.Query().Where(
		projectmember.ProjectIDEQ(projectID),
		projectmember.UserIDEQ(mentorID),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query project membership", err)
	}
	if exists {
		return domain.NewError(domain.CodeMentorAlreadyProjectMember, "mentor already belongs to the project")
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "start assign mentor transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureOrganizationMember(ctx, tx, target.OrganizationID, mentorID); err != nil {
		return err
	}
	if err := ensureProjectMember(ctx, tx, projectID, mentorID); err != nil {
		return err
	}
	if _, err := tx.MentorProjectApplication.Update().Where(
		mentorprojectapplication.ProjectIDEQ(projectID),
		mentorprojectapplication.MentorIDEQ(mentorID),
		mentorprojectapplication.StatusEQ(mentorprojectapplication.StatusPending),
	).
		SetStatus(mentorprojectapplication.StatusApproved).
		SetReviewedBy(teacherID).
		SetReviewedAt(s.now().UTC()).
		Save(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "close pending mentor application", err)
	}
	return tx.Commit()
}
