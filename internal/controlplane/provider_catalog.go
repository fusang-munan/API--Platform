package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

// beichenMaterializeMu serializes scheduled materialization with any manual
// catalog refresh so two writers cannot interleave model and binding updates.
var beichenMaterializeMu sync.Mutex

type ProviderCatalogSyncView struct {
	ProviderID string `json:"provider_id"`
	Provider   string `json:"provider_name"`
	Synced     int    `json:"synced"`
	SyncedAt   string `json:"synced_at"`
}

type ProviderCredentialInput struct {
	Label                 string
	GroupHint             *string
	Credential            *string
	Status                *string
	Channel               *string
	ReplacesCredentialID  *uuid.UUID
	ConfirmRetiredChannel *string
}

type ProviderCredentialView struct {
	ID                   string    `json:"id"`
	ProviderID           string    `json:"provider_id"`
	Label                string    `json:"label"`
	GroupHint            *string   `json:"group_hint,omitempty"`
	CredentialConfigured bool      `json:"credential_configured"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (s *Service) providersWithActiveCredentials() *ent.ProviderQuery {
	return s.db.Provider.Query().Where(
		provider.StatusEQ(provider.StatusActive),
		provider.HasCredentialsWith(providercredential.StatusEQ(providercredential.StatusActive)),
	)
}

func (s *Service) ActiveProvider(ctx context.Context) (*ent.Provider, error) {
	rows, err := s.providersWithActiveCredentials().
		Order(ent.Asc(provider.FieldName)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list active providers", err)
	}
	if len(rows) == 0 {
		return nil, domain.NewError(domain.CodeNotFound, "no active provider configured")
	}
	return rows[0], nil
}

func (s *Service) hasActiveProvider(ctx context.Context) (bool, error) {
	return s.providersWithActiveCredentials().Exist(ctx)
}

func (s *Service) ProviderCredentials(ctx context.Context, providerID uuid.UUID) ([]ProviderCredentialView, error) {
	exists, err := s.db.Provider.Query().Where(provider.IDEQ(providerID)).Exist(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if !exists {
		return nil, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	rows, err := s.db.ProviderCredential.Query().
		Where(providercredential.ProviderIDEQ(providerID)).
		Order(ent.Asc(providercredential.FieldLabel)).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "list provider credentials", err)
	}
	result := make([]ProviderCredentialView, 0, len(rows))
	for _, row := range rows {
		result = append(result, providerCredentialView(row))
	}
	return result, nil
}

func (s *Service) CreateProviderCredential(ctx context.Context, providerID uuid.UUID, input ProviderCredentialInput) (ProviderCredentialView, error) {
	row, err := s.db.Provider.Query().Where(provider.IDEQ(providerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ProviderCredentialView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if input.Credential == nil {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "credential is required")
	}
	if input.Channel != nil && !beichen.IsSchoolChannel(*input.Channel) {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "学校渠道须为 CC1、G3、GM1 或 Gemini")
	}
	if input.Channel != nil && input.ReplacesCredentialID != nil {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "请在独立登记和接替原 Key 中选择一种方式")
	}
	label, err := ValidateName(input.Label, 128)
	if err != nil {
		return ProviderCredentialView{}, err
	}
	ciphertext, err := s.security.EncryptCredential(*input.Credential)
	if err != nil {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, err.Error())
	}
	status, err := resourceStatus(input.Status, string(providercredential.StatusActive))
	if err != nil {
		return ProviderCredentialView{}, err
	}
	if catalog.IsBeichenBaseURL(row.BaseURL) {
		edit := beichen.CredentialEdit{
			Label: &label, GroupHint: input.GroupHint, Status: &status, Ciphertext: ciphertext,
			Replaces: input.ReplacesCredentialID,
		}
		if edit.Replaces != nil && *edit.Replaces == uuid.Nil {
			return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "请选择有效的原渠道")
		}
		if input.Channel != nil {
			edit.Channel = *input.Channel
		}
		if edit.Channel != "" || edit.Replaces != nil {
			if !validBeichenInferenceKey(*input.Credential) {
				return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "北辰推理 Key 格式无效")
			}
			fingerprint := sha256.Sum256([]byte(*input.Credential))
			edit.Fingerprint = fingerprint[:]
		}
		return s.saveBeichenCredential(ctx, providerID, uuid.Nil, edit)
	}
	if input.Channel != nil || input.ReplacesCredentialID != nil {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "登记或接替学校渠道仅适用于北辰")
	}
	builder := s.db.ProviderCredential.Create().
		SetProviderID(row.ID).
		SetLabel(label).
		SetCredentialCiphertext(ciphertext).
		SetStatus(providercredential.Status(status))
	if input.GroupHint != nil && strings.TrimSpace(*input.GroupHint) != "" {
		builder.SetGroupHint(strings.TrimSpace(*input.GroupHint))
	}
	cred, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return ProviderCredentialView{}, domain.NewError(domain.CodeNameConflict, "credential label already exists for this provider")
	}
	if err != nil {
		return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "create provider credential", err)
	}
	if cred.Status == providercredential.StatusActive && row.Status == provider.StatusActive {
		if _, syncErr := s.refreshCredentialCatalog(ctx, row, cred); syncErr != nil {
			return ProviderCredentialView{}, syncErr
		}
	}
	return providerCredentialView(cred), nil
}

func (s *Service) UpdateProviderCredential(ctx context.Context, providerID, credentialID uuid.UUID, input ProviderCredentialInput) (ProviderCredentialView, error) {
	if input.Channel != nil || input.ReplacesCredentialID != nil {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "学校渠道登记或接替须通过新建凭据提交")
	}
	row, err := s.db.ProviderCredential.Query().
		Where(
			providercredential.IDEQ(credentialID),
			providercredential.ProviderIDEQ(providerID),
		).
		WithProvider().
		Only(ctx)
	if ent.IsNotFound(err) {
		return ProviderCredentialView{}, domain.NewError(domain.CodeNotFound, "provider credential not found")
	}
	if err != nil {
		return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider credential", err)
	}
	isBeichen := row.Edges.Provider != nil && catalog.IsBeichenBaseURL(row.Edges.Provider.BaseURL)
	if input.ConfirmRetiredChannel != nil && (!isBeichen || input.Credential == nil || *input.ConfirmRetiredChannel != "Gemini") {
		return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "confirm_retired_channel 仅可在为旧 Gemini 渠道提交新 Key 时设为 Gemini")
	}
	if isBeichen {
		edit := beichen.CredentialEdit{GroupHint: input.GroupHint, ConfirmRetiredGemini: input.ConfirmRetiredChannel != nil}
		if input.Label != "" {
			label, err := ValidateName(input.Label, 128)
			if err != nil {
				return ProviderCredentialView{}, err
			}
			edit.Label = &label
		}
		if input.Status != nil {
			status, err := resourceStatus(input.Status, "")
			if err != nil {
				return ProviderCredentialView{}, err
			}
			edit.Status = &status
		}
		if input.Credential != nil {
			if row.Status != providercredential.StatusInactive || edit.Status == nil || *edit.Status != "active" {
				return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "原地更换 Key 仅适用于重新启用停用的 Gemini 渠道，其他渠道请使用接替向导")
			}
			if !validBeichenInferenceKey(*input.Credential) {
				return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "北辰推理 Key 格式无效")
			}
			ciphertext, err := s.security.EncryptCredential(*input.Credential)
			if err != nil {
				return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "加密北辰推理 Key 失败", err)
			}
			fingerprint := sha256.Sum256([]byte(*input.Credential))
			edit.Ciphertext, edit.Fingerprint = ciphertext, fingerprint[:]
		}
		return s.saveBeichenCredential(ctx, providerID, credentialID, edit)
	}
	builder := row.Update()
	if input.Label != "" {
		label, labelErr := ValidateName(input.Label, 128)
		if labelErr != nil {
			return ProviderCredentialView{}, labelErr
		}
		builder.SetLabel(label)
	}
	if input.GroupHint != nil {
		hint := strings.TrimSpace(*input.GroupHint)
		if hint == "" {
			builder.ClearGroupHint()
		} else {
			builder.SetGroupHint(hint)
		}
	}
	if input.Credential != nil {
		ciphertext, encErr := s.security.EncryptCredential(*input.Credential)
		if encErr != nil {
			return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, encErr.Error())
		}
		builder.SetCredentialCiphertext(ciphertext)
	}
	if input.Status != nil {
		status, statusErr := resourceStatus(input.Status, "")
		if statusErr != nil {
			return ProviderCredentialView{}, statusErr
		}
		builder.SetStatus(providercredential.Status(status))
	}
	updated, err := builder.Save(ctx)
	if ent.IsConstraintError(err) {
		return ProviderCredentialView{}, domain.NewError(domain.CodeNameConflict, "credential label already exists for this provider")
	}
	if err != nil {
		return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "update provider credential", err)
	}
	if updated.Status == providercredential.StatusActive && row.Edges.Provider != nil && row.Edges.Provider.Status == provider.StatusActive {
		if _, syncErr := s.refreshCredentialCatalog(ctx, row.Edges.Provider, updated); syncErr != nil {
			return ProviderCredentialView{}, syncErr
		}
	}
	if updated.Status == providercredential.StatusInactive {
		if err := catalog.DeactivateCredentialCatalog(ctx, s.db, credentialID); err != nil {
			return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "deactivate provider catalog", err)
		}
		if err := catalog.ReconcileProviderBindingRoutes(ctx, s.db, providerID); err != nil {
			return ProviderCredentialView{}, domain.WrapError(domain.CodeDependencyUnavailable, "reconcile provider routes", err)
		}
	}
	return providerCredentialView(updated), nil
}

func (s *Service) saveBeichenCredential(ctx context.Context, providerID, credentialID uuid.UUID, edit beichen.CredentialEdit) (ProviderCredentialView, error) {
	if edit.GroupHint != nil {
		hint := strings.TrimSpace(*edit.GroupHint)
		if len([]rune(hint)) > 128 {
			return ProviderCredentialView{}, domain.NewError(domain.CodeValidation, "渠道分组提示最多 128 个字符")
		}
		edit.GroupHint = &hint
	}
	row, err := s.beichen.SaveCredential(ctx, providerID, credentialID, edit)
	if err != nil {
		return ProviderCredentialView{}, err
	}
	return providerCredentialView(row), nil
}

func (s *Service) DeleteProviderCredential(ctx context.Context, providerID, credentialID uuid.UUID) error {
	exists, err := s.db.ProviderCredential.Query().
		Where(
			providercredential.IDEQ(credentialID),
			providercredential.ProviderIDEQ(providerID),
		).
		Exist(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query provider credential", err)
	}
	if !exists {
		return domain.NewError(domain.CodeNotFound, "provider credential not found")
	}
	keyCount, err := s.db.ProviderCredential.Query().
		Where(providercredential.ProviderIDEQ(providerID)).
		Count(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "count provider credentials", err)
	}
	if keyCount <= 1 {
		return domain.NewError(domain.CodeValidation, "provider must keep at least one key")
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "start provider credential delete transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ModelBinding.Delete().
		Where(modelbinding.CredentialIDEQ(credentialID)).
		Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete credential bindings", err)
	}
	if err := tx.ProviderCredential.DeleteOneID(credentialID).Exec(ctx); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "delete provider credential", err)
	}
	owner, err := tx.Provider.Get(ctx, providerID)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "read provider for route reconciliation", err)
	}
	if !catalog.IsBeichenBaseURL(owner.BaseURL) {
		if err := catalog.ReconcileProviderBindingRoutes(ctx, tx.Client(), providerID); err != nil {
			return domain.WrapError(domain.CodeDependencyUnavailable, "reconcile provider routes", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "commit provider credential delete", err)
	}
	return nil
}

func (s *Service) syncProviderCatalog(ctx context.Context, providerID uuid.UUID, resetGovernance bool) (ProviderCatalogSyncView, error) {
	row, err := s.db.Provider.Query().Where(provider.IDEQ(providerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ProviderCatalogSyncView{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return ProviderCatalogSyncView{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if row.Status != provider.StatusActive {
		return ProviderCatalogSyncView{}, domain.NewError(domain.CodeValidation, "provider must be active")
	}
	isBeichen := catalog.IsBeichenBaseURL(row.BaseURL)
	if isBeichen && resetGovernance {
		return ProviderCatalogSyncView{}, domain.NewError(domain.CodeInvalidTransition, "北辰目录支持同步候选，已发布线路请由教师启用或重绑")
	}
	syncedAt := s.now().UTC()
	total, err := s.fetchAndStoreProviderCatalog(ctx, row, syncedAt)
	if err != nil {
		return ProviderCatalogSyncView{}, err
	}
	view := ProviderCatalogSyncView{
		ProviderID: providerID.String(),
		Provider:   row.Name,
		Synced:     total,
		SyncedAt:   syncedAt.Format(time.RFC3339),
	}
	// Each Beichen key may list the same model. Refreshing a catalog must
	// not move a teacher's binding to the last key or publish a model.
	if isBeichen {
		return view, nil
	}
	if err := s.materializeStoredProviderCatalog(ctx, providerID, resetGovernance, true); err != nil {
		return ProviderCatalogSyncView{}, domain.WrapError(domain.CodeDependencyUnavailable, "materialize synced models", err)
	}
	if err := catalog.ReconcileProviderBindingRoutes(ctx, s.db, providerID); err != nil {
		return ProviderCatalogSyncView{}, domain.WrapError(domain.CodeDependencyUnavailable, "reconcile provider routes", err)
	}
	if !resetGovernance {
		if err := catalog.RestoreReadyGovernedModels(ctx, s.db, providerID); err != nil {
			return ProviderCatalogSyncView{}, domain.WrapError(domain.CodeDependencyUnavailable, "restore governed models", err)
		}
	}
	return view, nil
}

func (s *Service) refreshCredentialCatalog(ctx context.Context, providerRow *ent.Provider, credentialRow *ent.ProviderCredential) (int, error) {
	if providerRow == nil || credentialRow == nil {
		return 0, domain.NewError(domain.CodeValidation, "provider credential is unavailable")
	}
	if providerRow.Status != provider.StatusActive || credentialRow.Status != providercredential.StatusActive {
		return 0, nil
	}
	syncedAt := s.now().UTC()
	count, err := s.fetchAndStoreCredentialCatalog(ctx, providerRow, credentialRow, syncedAt)
	if err != nil {
		return 0, err
	}
	if catalog.IsBeichenBaseURL(providerRow.BaseURL) {
		return count, nil
	}
	if err := catalog.ReconcileProviderBindingRoutes(ctx, s.db, providerRow.ID); err != nil {
		return 0, domain.WrapError(domain.CodeDependencyUnavailable, "reconcile provider routes", err)
	}
	if err := catalog.RestoreReadyGovernedModels(ctx, s.db, providerRow.ID); err != nil {
		return 0, domain.WrapError(domain.CodeDependencyUnavailable, "restore governed models", err)
	}
	return count, nil
}

func (s *Service) fetchAndStoreProviderCatalog(ctx context.Context, providerRow *ent.Provider, syncedAt time.Time) (int, error) {
	credentials, err := catalog.ActiveCredentials(ctx, s.db, &providerRow.ID)
	if err != nil {
		return 0, domain.WrapError(domain.CodeDependencyUnavailable, "list active provider credentials", err)
	}
	if len(credentials) == 0 {
		return 0, domain.NewError(domain.CodeValidation, "provider has no active credentials")
	}
	syncer := &catalog.ProviderCatalogSyncer{DB: s.sqlDB}
	total := 0
	for _, credential := range credentials {
		count, _, fetchErr := s.fetchAndStoreCredentialCatalogEntries(ctx, providerRow, credential, syncedAt, syncer)
		if fetchErr != nil {
			return 0, fetchErr
		}
		total += count
	}
	return total, nil
}

func (s *Service) materializeStoredProviderCatalog(ctx context.Context, providerID uuid.UUID, resetGovernance, touchBindings bool) error {
	credentials, err := catalog.ActiveCredentials(ctx, s.db, &providerID)
	if err != nil {
		return err
	}
	for _, credential := range credentials {
		entries, loadErr := catalog.LoadActiveCatalogEntries(ctx, s.sqlDB, credential.ID)
		if loadErr != nil {
			return loadErr
		}
		if err := catalog.MaterializeCredentialModels(ctx, s.db, providerID, credential.ID, entries, resetGovernance, touchBindings); err != nil {
			return err
		}
	}
	return nil
}

// MaterializeBeichenCatalogs makes scanned entries visible as candidates.
// Catalog and probe jobs refresh route evidence. Binding changes remain in the
// teacher activation and rebind flows.
func (s *Service) MaterializeBeichenCatalogs(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	beichenMaterializeMu.Lock()
	defer beichenMaterializeMu.Unlock()
	rows, err := s.db.Provider.Query().Where(provider.StatusEQ(provider.StatusActive)).All(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query active providers", err)
	}
	var failures []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if !catalog.IsBeichenBaseURL(row.BaseURL) {
			continue
		}
		if err := s.materializeStoredProviderCatalog(ctx, row.ID, false, false); err != nil {
			failures = append(failures, fmt.Errorf("provider %s: %w", row.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) fetchAndStoreCredentialCatalog(ctx context.Context, providerRow *ent.Provider, credentialRow *ent.ProviderCredential, syncedAt time.Time) (int, error) {
	count, _, err := s.fetchAndStoreCredentialCatalogEntries(ctx, providerRow, credentialRow, syncedAt, &catalog.ProviderCatalogSyncer{DB: s.sqlDB})
	return count, err
}

func (s *Service) fetchAndStoreCredentialCatalogEntries(
	ctx context.Context,
	providerRow *ent.Provider,
	credentialRow *ent.ProviderCredential,
	syncedAt time.Time,
	syncer *catalog.ProviderCatalogSyncer,
) (int, []catalog.ProviderEntry, error) {
	isBeichen := catalog.IsBeichenBaseURL(providerRow.BaseURL)
	if isBeichen && s.beichen != nil {
		if err := s.beichen.RescanWebsiteCatalog(ctx, providerRow.ID, credentialRow.ID); err != nil {
			return 0, nil, err
		}
		count, err := s.db.ProviderModelCatalog.Query().Where(
			providermodelcatalog.ProviderIDEQ(providerRow.ID),
			providermodelcatalog.CredentialIDEQ(credentialRow.ID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
		).Count(ctx)
		if err != nil {
			return 0, nil, domain.WrapError(domain.CodeDependencyUnavailable, "读取已确认的北辰分组目录失败", err)
		}
		return count, nil, nil
	}
	plain, decryptErr := s.security.DecryptCredential(credentialRow.CredentialCiphertext)
	if decryptErr != nil {
		return 0, nil, domain.NewError(domain.CodeValidation, "provider credential is unavailable")
	}
	entries, fetchErr := catalog.FetchProviderModels(ctx, providerRow.BaseURL, plain, nil)
	if fetchErr != nil {
		upstreamURL := strings.TrimRight(strings.TrimSpace(providerRow.BaseURL), "/") + "/v1/models"
		return 0, nil, domain.WrapError(
			domain.CodeDependencyUnavailable,
			fmt.Sprintf("无法连接上游模型接口 %s，请检查 Base URL、网络连通性和 API Key", upstreamURL),
			fetchErr,
		)
	}
	entries = catalog.FilterProviderEntriesByCredential(credentialRow.Label, credentialRow.GroupHint, providerRow.BaseURL, entries)
	var revision *time.Time
	if isBeichen {
		revision = &credentialRow.UpdatedAt
	}
	count, upsertErr := syncer.UpsertCredentialEntries(ctx, providerRow.ID, credentialRow.ID, entries, syncedAt, revision)
	if upsertErr != nil {
		return 0, nil, domain.WrapError(domain.CodeDependencyUnavailable, "store provider catalog", upsertErr)
	}
	return count, entries, nil
}

func (s *Service) SyncProviderCatalog(ctx context.Context, providerID uuid.UUID) (ProviderCatalogSyncView, error) {
	return s.syncProviderCatalog(ctx, providerID, false)
}

func (s *Service) ResetProviderCatalog(ctx context.Context, providerID uuid.UUID) (ProviderCatalogSyncView, error) {
	return s.syncProviderCatalog(ctx, providerID, true)
}

func (s *Service) SyncActiveProviderCatalog(ctx context.Context) (ProviderCatalogSyncView, error) {
	active, err := s.ActiveProvider(ctx)
	if err != nil {
		return ProviderCatalogSyncView{}, err
	}
	return s.SyncProviderCatalog(ctx, active.ID)
}

func (s *Service) UnionCatalogCandidates(ctx context.Context, providerID *uuid.UUID, query string, page, pageSize int) (ModelCatalogPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}
	builder := s.db.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
			providermodelcatalog.HasCredentialWith(
				providercredential.StatusEQ(providercredential.StatusActive),
			),
		).
		WithProvider().
		WithCredential(func(q *ent.ProviderCredentialQuery) {
			q.WithProvider()
		}).
		Order(
			ent.Asc(providermodelcatalog.FieldModelID),
			ent.Asc(providermodelcatalog.FieldCredentialID),
		)
	if providerID != nil {
		builder = builder.Where(providermodelcatalog.ProviderIDEQ(*providerID))
	} else {
		builder = builder.Where(providermodelcatalog.HasCredentialWith(
			providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive)),
		))
	}
	rows, err := builder.All(ctx)
	if err != nil {
		return ModelCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "list provider catalog", err)
	}
	filtered := make([]*ent.ProviderModelCatalog, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProviderEntry{ID: row.ModelID, DisplayName: derefCatalogString(row.DisplayName), Description: derefCatalogString(row.Description), OwnedBy: derefCatalogString(row.OwnedBy), Object: derefCatalogString(row.UpstreamObject), CreatedAt: derefCatalogString(row.UpstreamCreatedAt), Raw: row.RawEntry}
		if query == "" || catalog.ProviderEntryMatchesQuery(entry, query) {
			filtered = append(filtered, row)
		}
	}
	start := (page - 1) * pageSize
	if start >= len(filtered) {
		return ModelCatalogPage{Items: []ModelCatalogCandidate{}, Page: page, PageSize: pageSize, Total: len(filtered)}, nil
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	items := make([]ModelCatalogCandidate, 0, end-start)
	for _, row := range filtered[start:end] {
		items = append(items, modelCatalogCandidateFromCatalogRow(row))
	}
	if err := s.enrichCatalogCandidatesWithPlatformModels(ctx, items); err != nil {
		return ModelCatalogPage{}, err
	}
	return ModelCatalogPage{Items: items, Page: page, PageSize: pageSize, Total: len(filtered)}, nil
}

func (s *Service) ProviderCatalogCandidates(ctx context.Context, providerID uuid.UUID, query string, page, pageSize int) (ModelCatalogPage, error) {
	row, err := s.db.Provider.Query().Where(provider.IDEQ(providerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ModelCatalogPage{}, domain.NewError(domain.CodeNotFound, "provider not found")
	}
	if err != nil {
		return ModelCatalogPage{}, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if row.Status != provider.StatusActive {
		return ModelCatalogPage{}, domain.NewError(domain.CodeValidation, "provider must be active")
	}
	return s.UnionCatalogCandidates(ctx, &providerID, query, page, pageSize)
}

func (s *Service) ResolveCredentialForUpstream(ctx context.Context, providerID uuid.UUID, upstreamModelName string) (*uuid.UUID, error) {
	upstreamModelName = strings.ToLower(strings.TrimSpace(upstreamModelName))
	baseURL, err := s.db.Provider.Query().Where(provider.IDEQ(providerID)).Select(provider.FieldBaseURL).String(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query provider", err)
	}
	if catalog.IsBeichenBaseURL(baseURL) {
		// Resolve an unspecified key using exactly the same catalog order as
		// the gateway and teacher's candidate preview.
		id, _, err := catalog.FindActiveCatalogCredential(ctx, s.db, providerID, upstreamModelName, uuid.Nil)
		return id, err
	}
	rows, err := s.db.ProviderModelCatalog.Query().
		Where(
			providermodelcatalog.ProviderIDEQ(providerID),
			providermodelcatalog.StatusEQ(providermodelcatalog.StatusActive),
			providermodelcatalog.HasCredentialWith(
				providercredential.StatusEQ(providercredential.StatusActive),
			),
		).
		All(ctx)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDependencyUnavailable, "query provider catalog", err)
	}
	for _, row := range rows {
		if strings.ToLower(strings.TrimSpace(row.ModelID)) == upstreamModelName {
			id := row.CredentialID
			return &id, nil
		}
	}
	return s.resolveCredentialForUpstreamLive(ctx, providerID, upstreamModelName)
}

func (s *Service) resolveCredentialForUpstreamLive(ctx context.Context, providerID uuid.UUID, upstreamModelName string) (*uuid.UUID, error) {
	credentials, err := catalog.ActiveCredentials(ctx, s.db, &providerID)
	if err != nil {
		return nil, err
	}
	for _, credential := range credentials {
		providerRow := credential.Edges.Provider
		if providerRow == nil {
			continue
		}
		plain, decryptErr := s.security.DecryptCredential(credential.CredentialCiphertext)
		if decryptErr != nil {
			continue
		}
		entries, fetchErr := catalog.FetchProviderModels(ctx, providerRow.BaseURL, plain, nil)
		if fetchErr != nil {
			continue
		}
		for _, entry := range entries {
			if strings.ToLower(strings.TrimSpace(entry.ID)) == upstreamModelName {
				id := credential.ID
				return &id, nil
			}
		}
	}
	return nil, nil
}

func providerCredentialView(row *ent.ProviderCredential) ProviderCredentialView {
	return ProviderCredentialView{
		ID:                   row.ID.String(),
		ProviderID:           row.ProviderID.String(),
		Label:                row.Label,
		GroupHint:            row.GroupHint,
		CredentialConfigured: len(row.CredentialCiphertext) > 0,
		Status:               string(row.Status),
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

func modelCatalogCandidateFromCatalogRow(row *ent.ProviderModelCatalog) ModelCatalogCandidate {
	entry := catalog.ProviderEntry{
		ID:          row.ModelID,
		DisplayName: derefCatalogString(row.DisplayName),
		Description: derefCatalogString(row.Description),
		OwnedBy:     derefCatalogString(row.OwnedBy),
		Object:      derefCatalogString(row.UpstreamObject),
		CreatedAt:   derefCatalogString(row.UpstreamCreatedAt),
		Raw:         row.RawEntry,
	}
	item := modelCatalogCandidateFromEntry(entry)
	item.CredentialID = row.CredentialID.String()
	if row.Edges.Credential != nil {
		item.CredentialLabel = row.Edges.Credential.Label
		if row.Edges.Credential.Edges.Provider != nil {
			item.ProviderName = row.Edges.Credential.Edges.Provider.Name
		}
	}
	if item.ProviderName == "" && row.Edges.Provider != nil {
		item.ProviderName = row.Edges.Provider.Name
	}
	return item
}

func (s *Service) enrichCatalogCandidatesWithPlatformModels(ctx context.Context, items []ModelCatalogCandidate) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ModelID)
	}
	rows, err := s.db.Model.Query().Where(entmodel.ModelIDIn(ids...)).All(ctx)
	if err != nil {
		return domain.WrapError(domain.CodeDependencyUnavailable, "query platform models for catalog", err)
	}
	byID := make(map[string]*ent.Model, len(rows))
	for _, row := range rows {
		byID[strings.ToLower(row.ModelID)] = row
	}
	for i := range items {
		row, ok := byID[strings.ToLower(items[i].ModelID)]
		if !ok || row.CreditMultiplierMilli == nil {
			continue
		}
		value := domain.FormatMultiplier(*row.CreditMultiplierMilli)
		items[i].CreditMultiplier = &value
	}
	return nil
}

func derefCatalogString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func modelCatalogCandidateFromEntry(entry catalog.ProviderEntry) ModelCatalogCandidate {
	displayName := catalog.ProviderDisplayName(entry)
	item := ModelCatalogCandidate{
		ModelID:  entry.ID,
		RawEntry: entry.Raw,
	}
	if displayName != "" {
		item.DisplayName = &displayName
	}
	if description := strings.TrimSpace(entry.Description); description != "" {
		item.Description = &description
	}
	if ownedBy := strings.TrimSpace(entry.OwnedBy); ownedBy != "" {
		item.OwnedBy = &ownedBy
	}
	if object := strings.TrimSpace(entry.Object); object != "" {
		item.UpstreamObject = &object
	}
	if createdAt := strings.TrimSpace(entry.CreatedAt); createdAt != "" {
		item.UpstreamCreatedAt = &createdAt
	}
	if item.RawEntry == nil {
		item.RawEntry = map[string]any{}
	}
	return item
}
