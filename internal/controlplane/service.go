package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	netmail "net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/cache"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikey"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeyaudit"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/project"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/projectmember"
	inframail "github.com/starprince1234/Nebula-api/internal/infrastructure/mail"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type Config struct {
	AccessTTL                        time.Duration
	RefreshTTL                       time.Duration
	VerificationTTL                  time.Duration
	SendCooldown                     time.Duration
	MaxAttempts                      int
	Lockout                          time.Duration
	InvitationTTL                    time.Duration
	PublicAppURL                     string
	ExposeVerificationCodeInResponse bool
}

type Service struct {
	db       *ent.Client
	sqlDB    *sql.DB
	usage    *usage.Store
	beichen  *beichen.Service
	cache    *cache.Store
	security *security.Manager
	mailer   inframail.Sender
	config   Config
	now      func() time.Time
}

type Identity struct {
	UserID uuid.UUID
	Role   string
}

func NewService(db *ent.Client, sqlDB *sql.DB, cacheStore *cache.Store, securityManager *security.Manager, mailer inframail.Sender, cfg Config) *Service {
	service := &Service{
		db: db, sqlDB: sqlDB, usage: usage.NewStore(sqlDB), cache: cacheStore, security: securityManager, mailer: mailer,
		config: cfg, now: time.Now,
	}
	service.beichen = beichen.New(sqlDB, securityManager)
	return service
}

func (s *Service) Beichen() *beichen.Service {
	return s.beichen
}

func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := netmail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 {
		return "", domain.NewError(domain.CodeValidation, "invalid email")
	}
	return value, nil
}

func ValidateName(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > maxLength {
		return "", domain.NewError(domain.CodeValidation, "invalid name")
	}
	return value, nil
}

func ValidateComment(action, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 1000 {
		return "", domain.NewError(domain.CodeValidation, "comment is too long")
	}
	if err := domain.ValidateAuditComment(action, value); err != nil {
		return "", err
	}
	return value, nil
}

func ValidateBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", domain.NewError(domain.CodeValidation, "invalid provider base URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", domain.NewError(domain.CodeValidation, "provider base URL must use http or https")
	}
	if strings.EqualFold(parsed.Hostname(), "api.beichenapi.com") {
		if parsed.Scheme != "https" || parsed.Port() != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/v1") {
			return "", domain.NewError(domain.CodeValidation, "北辰接口地址必须为 https://api.beichenapi.com 或 https://api.beichenapi.com/v1")
		}
		return "https://api.beichenapi.com", nil
	}
	return value, nil
}

func UUID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, domain.NewError(domain.CodeValidation, "invalid resource ID")
	}
	return id, nil
}

func (s *Service) keyView(ctx context.Context, query *ent.APIKeyQuery) (APIKeyView, error) {
	key, err := query.
		WithOwner().
		WithProject(func(projectQuery *ent.ProjectQuery) {
			projectQuery.WithOrganization()
		}).
		WithModels(func(modelLinkQuery *ent.APIKeyModelQuery) {
			modelLinkQuery.WithModel()
		}).
		WithAudits(func(auditQuery *ent.APIKeyAuditQuery) {
			auditQuery.Order(ent.Asc(apikeyaudit.FieldCreatedAt)).WithActor()
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return APIKeyView{}, domain.NewError(domain.CodeNotFound, "API key not found")
	}
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query API key", err)
	}
	modelIDs := make([]uuid.UUID, 0, len(key.Edges.Models))
	for _, link := range key.Edges.Models {
		if link.Edges.Model != nil {
			modelIDs = append(modelIDs, link.ModelID)
		}
	}
	protocols, err := catalog.LoadPublishedProtocols(ctx, s.db, modelIDs, s.security)
	if err != nil {
		return APIKeyView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query API key model availability", err)
	}
	return makeAPIKeyView(key, protocols), nil
}

func makeAPIKeyView(key *ent.APIKey, protocols map[uuid.UUID][]catalog.ProtocolCapability) APIKeyView {
	view := APIKeyView{
		ID: key.ID.String(), Name: key.Name, Status: string(key.Status),
		KeyPrefix: key.KeyPrefix, ClaimedAt: key.ClaimedAt,
		CreatedAt: key.CreatedAt, UpdatedAt: key.UpdatedAt,
		RequestedMonthlyCredits: domain.FormatCredits(key.RequestedMonthlyCreditQuotaMilli),
		Models:                  make([]ModelView, 0, len(key.Edges.Models)),
	}
	if key.MentorMonthlyCreditQuotaMilli != nil {
		value := domain.FormatCredits(*key.MentorMonthlyCreditQuotaMilli)
		view.MentorMonthlyCredits = &value
	}
	if key.MonthlyCreditQuotaMilli != nil {
		value := domain.FormatCredits(*key.MonthlyCreditQuotaMilli)
		view.MonthlyCredits = &value
	}
	if key.Edges.Owner != nil {
		view.Applicant = userView(key.Edges.Owner)
	}
	if key.Edges.Project != nil {
		view.Project = projectView(key.Edges.Project, false)
		if key.Edges.Project.Edges.Organization != nil {
			view.Organization = organizationView(key.Edges.Project.Edges.Organization)
		}
	}
	for _, link := range key.Edges.Models {
		if row := link.Edges.Model; row != nil {
			item := modelView(row)
			item.setAvailability(catalog.SummarizeModelAvailability(row.ModelID, row.Status, protocols[row.ID]))
			view.RouteReady = view.RouteReady || item.RouteReady
			view.Models = append(view.Models, item)
		}
	}
	for _, audit := range key.Edges.Audits {
		item := AuditView{
			Action: string(audit.Action), Comment: audit.Comment, CreatedAt: audit.CreatedAt,
		}
		if audit.Edges.Actor != nil {
			item.ActorRole = string(audit.Edges.Actor.Role)
		}
		view.Audits = append(view.Audits, item)
	}
	view.Progress = progressForStatus(view.Status, view.Audits)
	return view
}

func progressForStatus(status string, audits []AuditView) ProgressView {
	switch status {
	case domain.KeyPendingMentor:
		return ProgressView{Current: "mentor_review", CompletedSteps: []string{"submitted"}}
	case domain.KeyPendingTeacher:
		return ProgressView{Current: "mentor_review", CompletedSteps: []string{"submitted"}}
	case domain.KeyApproved:
		return ProgressView{Current: "claim", CompletedSteps: []string{"submitted", "mentor_review"}}
	case domain.KeyActive:
		return ProgressView{Current: "active", CompletedSteps: []string{"submitted", "mentor_review", "claimed"}}
	case domain.KeyRejected:
		for _, audit := range audits {
			if audit.Action == string(apikeyaudit.ActionStudentWithdrawn) {
				return ProgressView{Current: "withdrawn", CompletedSteps: []string{"submitted"}}
			}
			if audit.Action == string(apikeyaudit.ActionTeacherRejected) {
				return ProgressView{Current: "rejected_teacher", CompletedSteps: []string{"submitted", "mentor_review"}}
			}
		}
		return ProgressView{Current: "rejected", CompletedSteps: []string{"submitted"}}
	case domain.KeyInactive:
		return ProgressView{Current: "inactive", CompletedSteps: []string{"submitted", "mentor_review", "claimed"}}
	default:
		return ProgressView{Current: status}
	}
}

func (s *Service) publishKeyStatus(ctx context.Context, ownerID uuid.UUID, keyID uuid.UUID, status string, updatedAt time.Time) {
	if s.cache == nil {
		return
	}
	_ = s.cache.PublishUserEvent(ctx, ownerID.String(), "api_key.status_changed", map[string]any{
		"api_key_id": keyID.String(),
		"status":     status,
		"updated_at": updatedAt.UTC(),
	})
}

func keyStatus(status string) (apikey.Status, error) {
	switch status {
	case domain.KeyPendingMentor:
		return apikey.StatusPendingMentor, nil
	case domain.KeyPendingTeacher:
		return apikey.StatusPendingTeacher, nil
	case domain.KeyApproved:
		return apikey.StatusApproved, nil
	case domain.KeyActive:
		return apikey.StatusActive, nil
	case domain.KeyRejected:
		return apikey.StatusRejected, nil
	case domain.KeyInactive:
		return apikey.StatusInactive, nil
	default:
		return "", fmt.Errorf("unknown API key status %q", status)
	}
}

func (s *Service) StudentUsage(ctx context.Context, userID uuid.UUID, month time.Time) (usage.StudentUsage, error) {
	return s.usage.StudentUsage(ctx, userID, month)
}

func (s *Service) StudentCallReceipt(ctx context.Context, userID uuid.UUID, requestID string) (usage.StudentCallReceipt, error) {
	return s.usage.StudentCallReceipt(ctx, userID, requestID)
}

func (s *Service) StudentRecentCalls(ctx context.Context, userID uuid.UUID, start *time.Time, cursor *usage.Cursor, limit int) (usage.Page[usage.StudentRecentCall], error) {
	return s.usage.StudentRecentCalls(ctx, userID, start, cursor, limit)
}

func (s *Service) MentorProjectUsage(ctx context.Context, mentorID, projectID uuid.UUID, month time.Time) (usage.ProjectUsage, error) {
	responsible, err := s.db.ProjectMember.Query().Where(projectmember.ProjectIDEQ(projectID), projectmember.UserIDEQ(mentorID)).Exist(ctx)
	if err != nil {
		return usage.ProjectUsage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query mentor responsibility", err)
	}
	if !responsible {
		return usage.ProjectUsage{}, domain.NewError(domain.CodeNotFound, "project not found")
	}
	return s.usage.ProjectUsage(ctx, projectID, month)
}

func (s *Service) MentorCallLogs(ctx context.Context, mentorID uuid.UUID, filter usage.LogFilter) (usage.Page[usage.CallLog], error) {
	return s.usage.MentorCallLogs(ctx, mentorID, filter)
}
func (s *Service) MentorCallTurns(ctx context.Context, mentorID uuid.UUID, filter usage.LogFilter) (usage.Page[usage.CallTurn], error) {
	return s.usage.MentorCallTurns(ctx, mentorID, filter)
}
func (s *Service) MentorCallTurn(ctx context.Context, mentorID, turnID uuid.UUID) (usage.CallTurn, error) {
	return s.usage.MentorCallTurn(ctx, mentorID, turnID)
}
func (s *Service) MentorCallLog(ctx context.Context, mentorID, callID uuid.UUID) (usage.CallLog, error) {
	return s.usage.MentorCallLog(ctx, mentorID, callID)
}
func (s *Service) MentorInputs(ctx context.Context, mentorID uuid.UUID, filter usage.InputFilter) (usage.Page[usage.InputMonitorItem], error) {
	return s.usage.MentorInputs(ctx, mentorID, filter)
}
func (s *Service) MentorInput(ctx context.Context, mentorID, callID uuid.UUID) (usage.InputMonitorItem, error) {
	return s.usage.MentorInput(ctx, mentorID, callID)
}

func (s *Service) UpdateMentorKeyQuota(ctx context.Context, mentorID, keyID uuid.UUID, quota int64, reason string) error {
	if quota < 0 || quota > domain.MaxCreditMilli {
		return domain.NewError(domain.CodeValidation, "quota is required")
	}
	if err := s.usage.UpdateKeyQuota(ctx, mentorID, keyID, quota, strings.TrimSpace(reason)); err != nil {
		var appErr *domain.Error
		if errors.As(err, &appErr) {
			return err
		}
		return domain.WrapError(domain.CodeDependencyUnavailable, "update API key quota", err)
	}
	return nil
}

func (s *Service) populateMemberQuota(ctx context.Context, view *APIKeyView) error {
	switch view.Status {
	case domain.KeyPendingMentor, domain.KeyPendingTeacher, domain.KeyRejected:
		return nil
	}
	if view.Applicant.ID == "" || view.Project.ID == "" {
		return nil
	}
	ownerID, err := uuid.Parse(view.Applicant.ID)
	if err != nil {
		return domain.NewError(domain.CodeValidation, "invalid applicant ID")
	}
	projectID, err := uuid.Parse(view.Project.ID)
	if err != nil {
		return domain.NewError(domain.CodeValidation, "invalid project ID")
	}
	if err := s.usage.EnsureCurrentBuckets(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "ensure credit buckets", err)
	}
	quota, charged, pending, err := s.usage.MemberMonthCredits(ctx, projectID, ownerID, time.Now())
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query member quota", err)
	}
	if quota == 0 && charged == 0 && pending == 0 {
		return nil
	}
	formattedQuota := domain.FormatCredits(quota)
	formattedUsed := domain.FormatCredits(charged)
	formattedPending := domain.FormatCredits(pending)
	view.MemberMonthlyCredits = &formattedQuota
	view.MemberUsedCredits = &formattedUsed
	view.MemberPendingCredits = &formattedPending
	return nil
}

func (s *Service) TeacherProjectUsage(ctx context.Context, projectID uuid.UUID, month time.Time) (usage.ProjectUsage, error) {
	return s.usage.ProjectUsage(ctx, projectID, month)
}

func (s *Service) TeacherProjectSpend(ctx context.Context, month time.Time, organizationID, query string) ([]usage.ProjectUsage, error) {
	projects, err := s.db.Project.Query().WithOrganization().Order(ent.Asc(project.FieldName)).All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list project spend", err)
	}
	result := make([]usage.ProjectUsage, 0, len(projects))
	for _, p := range projects {
		if organizationID != "" && p.OrganizationID.String() != organizationID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(query)) {
			continue
		}
		item, err := s.usage.ProjectUsage(ctx, p.ID, month)
		if err != nil {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}
