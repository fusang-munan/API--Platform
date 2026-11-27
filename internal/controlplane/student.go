package controlplane

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeyaudit"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymodel"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymonthcreditbucket"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/mentorprofile"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/organization"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/user"
)

type SubmitAPIKeyInput struct {
	Name                    string
	OrganizationID          uuid.UUID
	ProjectID               uuid.UUID
	RequestedMonthlyCredits int64
	Models                  []RequestedModelInput // Accepted for older callers and ignored.
}

type RequestedModelInput struct {
	ModelID     string
	DisplayName string
}

type ClaimView struct {
	APIKey    string    `json:"api_key"`
	KeyPrefix string    `json:"key_prefix"`
	ClaimedAt time.Time `json:"claimed_at"`
}

func (s *Service) StudentOrganizations(ctx context.Context) ([]OrganizationView, error) {
	rows, err := s.db.Organization.Query().
		Where(organization.StatusEQ(organization.StatusActive)).
		Order(ent.Asc(organization.FieldName)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list organizations", err)
	}
	result := make([]OrganizationView, 0, len(rows))
	for _, row := range rows {
		result = append(result, organizationView(row))
	}
	return result, nil
}

func (s *Service) StudentProjects(ctx context.Context, organizationID uuid.UUID) ([]StudentProjectView, error) {
	orgExists, err := s.db.Organization.Query().Where(
		organization.IDEQ(organizationID),
		organization.StatusEQ(organization.StatusActive),
	).Exist(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query organization", err)
	}
	if !orgExists {
		return nil, domain.NewError(domain.CodeNotFound, "organization not found")
	}
	rows, err := s.db.Project.Query().
		Where(project.OrganizationIDEQ(organizationID)).
		Order(ent.Asc(project.FieldName)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list projects", err)
	}
	projectIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		projectIDs = append(projectIDs, row.ID)
	}
	mentorProjects := make(map[uuid.UUID]bool)
	if len(projectIDs) > 0 {
		members, err := s.db.ProjectMember.Query().Where(
			projectmember.ProjectIDIn(projectIDs...),
			projectmember.HasUserWith(
				user.StatusEQ(user.StatusActive),
				user.HasMentorProfileWith(mentorprofile.StatusEQ(mentorprofile.StatusActive)),
			),
		).All(ctx)
		if err != nil {
			return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query project mentors", err)
		}
		for _, member := range members {
			mentorProjects[member.ProjectID] = true
		}
	}
	result := make([]StudentProjectView, 0, len(rows))
	for _, row := range rows {
		result = append(result, studentProjectView(row, mentorProjects[row.ID]))
	}
	return result, nil
}

func (s *Service) StudentModels(ctx context.Context, studentID uuid.UUID) (StudentModelPlazaView, error) {
	return s.StudentModelPlaza(ctx, studentID)
}

func (s *Service) SubmitAPIKey(ctx context.Context, studentID uuid.UUID, input SubmitAPIKeyInput) (APIKeyView, error) {
	name, err := ValidateName(input.Name, 128)
	if err != nil {
		return APIKeyView{}, err
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "start API key transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	orgExists, err := tx.Organization.Query().Where(
		organization.IDEQ(input.OrganizationID),
		organization.StatusEQ(organization.StatusActive),
	).Exist(ctx)
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query organization", err)
	}
	if !orgExists {
		return APIKeyView{}, domain.NewError(domain.CodeNotFound, "organization not found")
	}
	targetProject, err := tx.Project.Query().Where(
		project.IDEQ(input.ProjectID),
		project.OrganizationIDEQ(input.OrganizationID),
		project.StatusEQ(project.StatusActive),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return APIKeyView{}, domain.NewError(domain.CodeNotFound, "project not found")
	}
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query project", err)
	}
	hasLiveKey, err := tx.APIKey.Query().Where(
		apikey.OwnerUserIDEQ(studentID),
		apikey.ProjectIDEQ(targetProject.ID),
		apikey.StatusIn(
			apikey.StatusPendingMentor,
			apikey.StatusPendingTeacher,
			apikey.StatusApproved,
			apikey.StatusActive,
			apikey.StatusInactive,
		),
	).Exist(ctx)
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query existing API key", err)
	}
	if hasLiveKey {
		return APIKeyView{}, domain.NewError(domain.CodeProjectKeyAlreadyExists, "project already has an active API key application")
	}
	if input.RequestedMonthlyCredits < 0 || input.RequestedMonthlyCredits > targetProject.MonthlyCreditQuotaMilli {
		return APIKeyView{}, domain.NewError(domain.CodeValidation, "requested monthly credits exceed project quota")
	}

	key, err := tx.APIKey.Create().
		SetOwnerUserID(studentID).
		SetProjectID(targetProject.ID).
		SetName(name).
		SetStatus(apikey.StatusPendingMentor).
		SetRequestedMonthlyCreditQuotaMilli(input.RequestedMonthlyCredits).
		Save(ctx)
	if ent.IsConstraintError(err) {
		if strings.Contains(err.Error(), "api_keys_owner_project_live") {
			return APIKeyView{}, domain.NewError(domain.CodeProjectKeyAlreadyExists, "project already has an active API key application")
		}
		return APIKeyView{}, domain.NewError(domain.CodeNameConflict, "API key name is already in use")
	}
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create API key application", err)
	}
	if err := tx.Commit(); err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "commit API key transaction", err)
	}
	view, err := s.keyView(ctx, s.db.APIKey.Query().Where(
		apikey.IDEQ(key.ID),
		apikey.OwnerUserIDEQ(studentID),
	))
	if err == nil {
		s.publishKeyStatus(ctx, studentID, key.ID, domain.KeyPendingMentor, key.UpdatedAt)
	}
	return view, err
}

func (s *Service) StudentAPIKeys(ctx context.Context, studentID uuid.UUID) ([]APIKeyView, error) {
	rows, err := s.db.APIKey.Query().
		Where(apikey.OwnerUserIDEQ(studentID)).
		Order(ent.Desc(apikey.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list API keys", err)
	}
	result := make([]APIKeyView, 0, len(rows))
	models := make([][]ModelView, 0, len(rows))
	for _, row := range rows {
		view, err := s.keyView(ctx, s.db.APIKey.Query().Where(
			apikey.IDEQ(row.ID),
			apikey.OwnerUserIDEQ(studentID),
		))
		if err != nil {
			return nil, err
		}
		if err := s.populateKeyAvailability(ctx, &view); err != nil {
			return nil, err
		}
		// 列表只返回前缀；完整密钥必须通过单把 Key 详情主动查看，避免一次请求解密全部明文。
		result = append(result, view)
		models = append(models, view.Models)
	}
	if err := s.setStudentModelStates(ctx, models...); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) StudentAPIKey(ctx context.Context, studentID, keyID uuid.UUID) (APIKeyView, error) {
	view, err := s.keyView(ctx, s.db.APIKey.Query().Where(
		apikey.IDEQ(keyID),
		apikey.OwnerUserIDEQ(studentID),
	))
	if err != nil {
		return APIKeyView{}, err
	}
	if err := s.populateKeyAvailability(ctx, &view); err != nil {
		return APIKeyView{}, err
	}
	if err := s.setStudentModelStates(ctx, view.Models); err != nil {
		return APIKeyView{}, err
	}
	s.populateStudentAPIKeySecret(ctx, keyID, &view)
	return view, nil
}

func (s *Service) WithdrawAPIKey(ctx context.Context, studentID, keyID uuid.UUID) error {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "start API key withdraw transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	exists, err := tx.APIKey.Query().Where(
		apikey.IDEQ(keyID),
		apikey.OwnerUserIDEQ(studentID),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query API key", err)
	}
	if !exists {
		return domain.NewError(domain.CodeNotFound, "API key not found")
	}
	pending, err := tx.APIKey.Query().Where(
		apikey.IDEQ(keyID),
		apikey.OwnerUserIDEQ(studentID),
		apikey.StatusIn(apikey.StatusPendingMentor, apikey.StatusPendingTeacher),
	).Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query API key", err)
	}
	if !pending {
		return domain.NewError(domain.CodeInvalidTransition, "API key can only be withdrawn before mentor review")
	}
	if err := deleteAPIKeyApplication(ctx, tx, keyID); err != nil {
		return err
	}
	deleted, err := tx.APIKey.Delete().Where(
		apikey.IDEQ(keyID),
		apikey.OwnerUserIDEQ(studentID),
		apikey.StatusIn(apikey.StatusPendingMentor, apikey.StatusPendingTeacher),
	).Exec(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "withdraw API key application", err)
	}
	if deleted != 1 {
		return domain.NewError(domain.CodeInvalidTransition, "API key was reviewed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "commit API key withdraw", err)
	}
	s.publishKeyStatus(ctx, studentID, keyID, "withdrawn", s.now().UTC())
	return nil
}

func deleteAPIKeyApplication(ctx context.Context, tx *ent.Tx, keyID uuid.UUID) error {
	if _, err := tx.APIKeyModel.Delete().Where(apikeymodel.APIKeyIDEQ(keyID)).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete API key models", err)
	}
	if _, err := tx.APIKeyAudit.Delete().Where(apikeyaudit.APIKeyIDEQ(keyID)).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete API key audits", err)
	}
	if _, err := tx.APIKeyMonthCreditBucket.Delete().Where(apikeymonthcreditbucket.APIKeyIDEQ(keyID)).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete API key credit buckets", err)
	}
	return nil
}

func (s *Service) ClaimAPIKey(ctx context.Context, studentID, keyID uuid.UUID) (ClaimView, error) {
	secret, prefix, hash, err := s.security.NewAPIKey()
	if err != nil {
		return ClaimView{}, domain.WrapError("INTERNAL_ERROR", "generate API key", err)
	}
	ciphertext, err := s.security.EncryptAPIKey(secret)
	if err != nil {
		return ClaimView{}, domain.WrapError("INTERNAL_ERROR", "encrypt API key", err)
	}
	claimedAt := s.now().UTC()
	updated, err := s.db.APIKey.Update().
		Where(
			apikey.IDEQ(keyID),
			apikey.OwnerUserIDEQ(studentID),
			apikey.StatusEQ(apikey.StatusApproved),
		).
		SetStatus(apikey.StatusActive).
		SetKeyHash(hash).
		SetKeyPrefix(prefix).
		SetClaimedAt(claimedAt).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return ClaimView{}, domain.NewError(domain.CodeInvalidTransition, "API key could not be claimed")
	}
	if err != nil {
		return ClaimView{}, domain.WrapError(domain.CodeDependencyUnavailable, "claim API key", err)
	}
	if updated == 0 {
		exists, queryErr := s.db.APIKey.Query().Where(
			apikey.IDEQ(keyID),
			apikey.OwnerUserIDEQ(studentID),
		).Exist(ctx)
		if queryErr != nil {
			return ClaimView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query API key", queryErr)
		}
		if !exists {
			return ClaimView{}, domain.NewError(domain.CodeNotFound, "API key not found")
		}
		return ClaimView{}, domain.NewError(domain.CodeKeyAlreadyClaimed, "API key has already been claimed or is not approved")
	}
	if err := s.storeAPIKeyCiphertext(ctx, keyID, ciphertext); err != nil {
		return ClaimView{}, err
	}
	s.publishKeyStatus(ctx, studentID, keyID, domain.KeyActive, claimedAt)
	return ClaimView{APIKey: secret, KeyPrefix: prefix, ClaimedAt: claimedAt}, nil
}

func (s *Service) RegenerateAPIKey(ctx context.Context, studentID, keyID uuid.UUID) (ClaimView, error) {
	row, err := s.db.APIKey.Query().Where(
		apikey.IDEQ(keyID),
		apikey.OwnerUserIDEQ(studentID),
		apikey.StatusIn(apikey.StatusActive, apikey.StatusInactive),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return ClaimView{}, domain.NewError(domain.CodeNotFound, "API key not found")
	}
	if err != nil {
		return ClaimView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query API key", err)
	}
	secret, prefix, hash, err := s.security.NewAPIKey()
	if err != nil {
		return ClaimView{}, domain.WrapError("INTERNAL_ERROR", "generate API key", err)
	}
	ciphertext, err := s.security.EncryptAPIKey(secret)
	if err != nil {
		return ClaimView{}, domain.WrapError("INTERNAL_ERROR", "encrypt API key", err)
	}
	claimedAt := row.ClaimedAt
	if claimedAt == nil {
		now := s.now().UTC()
		claimedAt = &now
	}
	if _, err := s.db.APIKey.UpdateOneID(keyID).
		SetKeyHash(hash).
		SetKeyPrefix(prefix).
		SetClaimedAt(*claimedAt).
		Save(ctx); err != nil {
		return ClaimView{}, domain.WrapError(domain.CodeDependencyUnavailable, "regenerate API key", err)
	}
	if err := s.storeAPIKeyCiphertext(ctx, keyID, ciphertext); err != nil {
		return ClaimView{}, err
	}
	return ClaimView{APIKey: secret, KeyPrefix: prefix, ClaimedAt: *claimedAt}, nil
}

func (s *Service) storeAPIKeyCiphertext(ctx context.Context, keyID uuid.UUID, ciphertext []byte) error {
	if _, err := s.sqlDB.ExecContext(ctx, `UPDATE api_keys SET key_ciphertext = $1 WHERE id = $2`, ciphertext, keyID); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "store API key ciphertext", err)
	}
	return nil
}

func (s *Service) populateStudentAPIKeySecret(ctx context.Context, keyID uuid.UUID, view *APIKeyView) {
	if view == nil || (view.Status != domain.KeyActive && view.Status != domain.KeyInactive) {
		return
	}
	var ciphertext []byte
	err := s.sqlDB.QueryRowContext(ctx, `SELECT key_ciphertext FROM api_keys WHERE id = $1`, keyID).Scan(&ciphertext)
	if err == sql.ErrNoRows || len(ciphertext) == 0 {
		return
	}
	if err != nil {
		return
	}
	secret, err := s.security.DecryptAPIKey(ciphertext)
	if err != nil {
		return
	}
	view.APIKey = &secret
}
