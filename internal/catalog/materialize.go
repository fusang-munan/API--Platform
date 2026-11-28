package catalog

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/apikeymodel"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/gatewaycall"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelmultiplieraudit"
)

// MaterializeProviderModels upserts platform models from upstream catalog entries.
// When resetGovernance is true (Reset Catalog), multiplier/common/status are cleared.
// Candidate refreshes leave existing governed models and all bindings alone.
func MaterializeProviderModels(ctx context.Context, client *ent.Client, providerID uuid.UUID, entries []ProviderEntry, resetGovernance, touchBindings bool) error {
	return materializeProviderEntries(ctx, client, providerID, uuid.Nil, entries, resetGovernance, touchBindings)
}

// MaterializeCredentialModels applies one Key catalog without merging other Keys.
func MaterializeCredentialModels(ctx context.Context, client *ent.Client, providerID, credentialID uuid.UUID, entries []ProviderEntry, resetGovernance, touchBindings bool) error {
	if credentialID == uuid.Nil {
		return MaterializeProviderModels(ctx, client, providerID, entries, resetGovernance, touchBindings)
	}
	return materializeProviderEntries(ctx, client, providerID, credentialID, entries, resetGovernance, touchBindings)
}

func materializeProviderEntries(ctx context.Context, client *ent.Client, providerID, credentialID uuid.UUID, entries []ProviderEntry, resetGovernance, touchBindings bool) error {
	defaultMultiplier := int64(0)
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ID)
		if modelID == "" || len([]rune(modelID)) > 256 {
			continue
		}
		displayName := ProviderDisplayName(entry)
		upstream := cloneRawEntry(entry.Raw)
		explicit := ParseUpstreamMetadata(upstream)
		description := strings.TrimSpace(entry.Description)
		row, queryErr := client.Model.Query().Where(entmodel.ModelIDEQ(modelID)).Only(ctx)
		if ent.IsNotFound(queryErr) {
			builder := client.Model.Create().
				SetModelID(modelID).
				SetDisplayName(displayName).
				SetUpstreamMetadata(upstream).
				SetStatus(entmodel.StatusPendingConfiguration).
				SetCreditMultiplierMilli(defaultMultiplier)
			applyExplicitMetadataCreate(builder, explicit, description)
			row, queryErr = builder.Save(ctx)
		} else if queryErr != nil {
			return queryErr
		} else {
			if !touchBindings && !resetGovernance && row.Status != entmodel.StatusPendingConfiguration {
				continue
			}
			update := row.Update().
				SetDisplayName(displayName).
				SetUpstreamMetadata(upstream)
			if resetGovernance {
				update.SetCreditMultiplierMilli(defaultMultiplier).SetIsCommon(false).SetStatus(entmodel.StatusPendingConfiguration)
			}
			if row.Status == entmodel.StatusPendingConfiguration {
				applyExplicitMetadataUpdate(update, explicit, description)
				if row.CreditMultiplierMilli == nil && !resetGovernance {
					update.SetCreditMultiplierMilli(defaultMultiplier)
				}
			} else if description != "" {
				update.SetDescription(description)
			}
			row, queryErr = update.Save(ctx)
		}
		if queryErr != nil {
			return queryErr
		}
		if touchBindings {
			if err := syncCredentialBindings(ctx, client, row.ID, providerID, credentialID, modelID, entry); err != nil {
				return err
			}
		}
	}
	if !touchBindings {
		return nil
	}
	if resetGovernance {
		if credentialID != uuid.Nil {
			return purgeRemovedCredentialModels(ctx, client, providerID, credentialID, entries)
		}
		return purgeRemovedProviderModels(ctx, client, providerID, entries)
	}
	if credentialID != uuid.Nil {
		return deactivateRemovedCredentialBindings(ctx, client, providerID, credentialID, entries)
	}
	return deactivateRemovedProviderBindings(ctx, client, providerID, entries)
}

func upstreamNameSet(entries []ProviderEntry) map[string]struct{} {
	activeNames := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ID)
		if modelID == "" {
			continue
		}
		activeNames[strings.ToLower(modelID)] = struct{}{}
	}
	return activeNames
}

func deactivateRemovedProviderBindings(ctx context.Context, client *ent.Client, providerID uuid.UUID, entries []ProviderEntry) error {
	activeNames := upstreamNameSet(entries)
	bindings, err := client.ModelBinding.Query().Where(modelbinding.ProviderIDEQ(providerID)).All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		name := strings.ToLower(strings.TrimSpace(binding.UpstreamModelName))
		if _, ok := activeNames[name]; ok || binding.Status != modelbinding.StatusActive {
			continue
		}
		if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusInactive).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func purgeRemovedProviderModels(ctx context.Context, client *ent.Client, providerID uuid.UUID, entries []ProviderEntry) error {
	activeNames := upstreamNameSet(entries)
	bindings, err := client.ModelBinding.Query().Where(modelbinding.ProviderIDEQ(providerID)).All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		name := strings.ToLower(strings.TrimSpace(binding.UpstreamModelName))
		if _, ok := activeNames[name]; ok {
			continue
		}
		modelUUID := binding.ModelID
		if err := client.ModelBinding.DeleteOneID(binding.ID).Exec(ctx); err != nil {
			return err
		}
		if err := purgeModelIfOrphaned(ctx, client, modelUUID); err != nil {
			return err
		}
	}
	orphaned, err := client.Model.Query().Where(entmodel.Not(entmodel.HasBindings())).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range orphaned {
		if _, ok := activeNames[strings.ToLower(strings.TrimSpace(row.ModelID))]; ok {
			continue
		}
		if err := purgeModelIfOrphaned(ctx, client, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func purgeModelIfOrphaned(ctx context.Context, client *ent.Client, modelUUID uuid.UUID) error {
	hasBindings, err := client.ModelBinding.Query().Where(modelbinding.ModelIDEQ(modelUUID)).Exist(ctx)
	if err != nil {
		return err
	}
	if hasBindings {
		return nil
	}
	if _, err := client.APIKeyModel.Delete().Where(apikeymodel.ModelIDEQ(modelUUID)).Exec(ctx); err != nil {
		return err
	}
	hasCalls, err := client.GatewayCall.Query().Where(gatewaycall.ModelIDEQ(modelUUID)).Exist(ctx)
	if err != nil {
		return err
	}
	if hasCalls {
		return deactivateOrphanModel(ctx, client, modelUUID)
	}
	if _, err := client.ModelMultiplierAudit.Delete().Where(modelmultiplieraudit.ModelIDEQ(modelUUID)).Exec(ctx); err != nil {
		return err
	}
	return client.Model.DeleteOneID(modelUUID).Exec(ctx)
}

func deactivateOrphanModel(ctx context.Context, client *ent.Client, modelUUID uuid.UUID) error {
	row, err := client.Model.Query().Where(entmodel.IDEQ(modelUUID)).Only(ctx)
	if err != nil {
		return err
	}
	if row.Status == entmodel.StatusInactive && !row.IsCommon {
		return nil
	}
	_, err = row.Update().SetStatus(entmodel.StatusInactive).SetIsCommon(false).Save(ctx)
	return err
}

func deactivateRemovedCredentialBindings(ctx context.Context, client *ent.Client, providerID, credentialID uuid.UUID, entries []ProviderEntry) error {
	activeNames := upstreamNameSet(entries)
	bindings, err := client.ModelBinding.Query().
		Where(
			modelbinding.ProviderIDEQ(providerID),
			modelbinding.CredentialIDEQ(credentialID),
		).
		All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		name := strings.ToLower(strings.TrimSpace(binding.UpstreamModelName))
		if _, ok := activeNames[name]; ok || binding.Status != modelbinding.StatusActive {
			continue
		}
		if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusInactive).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func purgeRemovedCredentialModels(ctx context.Context, client *ent.Client, providerID, credentialID uuid.UUID, entries []ProviderEntry) error {
	activeNames := upstreamNameSet(entries)
	bindings, err := client.ModelBinding.Query().
		Where(
			modelbinding.ProviderIDEQ(providerID),
			modelbinding.CredentialIDEQ(credentialID),
		).
		All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		name := strings.ToLower(strings.TrimSpace(binding.UpstreamModelName))
		if _, ok := activeNames[name]; ok {
			continue
		}
		modelUUID := binding.ModelID
		if err := client.ModelBinding.DeleteOneID(binding.ID).Exec(ctx); err != nil {
			return err
		}
		if err := purgeModelIfOrphaned(ctx, client, modelUUID); err != nil {
			return err
		}
	}
	return nil
}

func syncCredentialBindings(
	ctx context.Context,
	client *ent.Client,
	modelUUID uuid.UUID,
	providerID uuid.UUID,
	credentialID uuid.UUID,
	upstreamModelID string,
	entry ProviderEntry,
) error {
	if credentialID == uuid.Nil {
		return syncProviderBindings(ctx, client, modelUUID, providerID, upstreamModelID, entry)
	}
	expected := AdaptersFromUpstream(entry)
	expectedSet := make(map[modelbinding.Adapter]struct{}, len(expected))
	for _, adapter := range expected {
		expectedSet[adapter] = struct{}{}
		exists, err := client.ModelBinding.Query().Where(
			modelbinding.ModelIDEQ(modelUUID),
			modelbinding.ProviderIDEQ(providerID),
			modelbinding.AdapterEQ(adapter),
		).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			create := client.ModelBinding.Create().
				SetModelID(modelUUID).
				SetProviderID(providerID).
				SetCredentialID(credentialID).
				SetUpstreamModelName(upstreamModelID).
				SetAdapter(adapter).
				SetPriority(DefaultBindingPriority(adapter)).
				SetStatus(modelbinding.StatusActive)
			if _, err := create.Save(ctx); err != nil && !ent.IsConstraintError(err) {
				return err
			}
			continue
		}
		update := client.ModelBinding.Update().
			Where(
				modelbinding.ModelIDEQ(modelUUID),
				modelbinding.ProviderIDEQ(providerID),
				modelbinding.AdapterEQ(adapter),
			).
			SetUpstreamModelName(upstreamModelID).
			SetCredentialID(credentialID).
			SetStatus(modelbinding.StatusActive)
		if _, err := update.Save(ctx); err != nil {
			return err
		}
	}
	existing, err := client.ModelBinding.Query().Where(
		modelbinding.ModelIDEQ(modelUUID),
		modelbinding.ProviderIDEQ(providerID),
		modelbinding.CredentialIDEQ(credentialID),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range existing {
		if _, ok := expectedSet[binding.Adapter]; ok || binding.Status != modelbinding.StatusActive {
			continue
		}
		if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusInactive).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func syncProviderBindings(
	ctx context.Context,
	client *ent.Client,
	modelUUID uuid.UUID,
	providerID uuid.UUID,
	upstreamModelID string,
	entry ProviderEntry,
) error {
	expected := AdaptersFromUpstream(entry)
	expectedSet := make(map[modelbinding.Adapter]struct{}, len(expected))
	for _, adapter := range expected {
		expectedSet[adapter] = struct{}{}
		exists, err := client.ModelBinding.Query().Where(
			modelbinding.ModelIDEQ(modelUUID),
			modelbinding.ProviderIDEQ(providerID),
			modelbinding.AdapterEQ(adapter),
		).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			create := client.ModelBinding.Create().
				SetModelID(modelUUID).
				SetProviderID(providerID).
				SetUpstreamModelName(upstreamModelID).
				SetAdapter(adapter).
				SetPriority(DefaultBindingPriority(adapter)).
				SetStatus(modelbinding.StatusActive)
			if credentialID := lookupCatalogCredential(ctx, client, providerID, upstreamModelID); credentialID != nil {
				create.SetCredentialID(*credentialID)
			}
			if _, err := create.Save(ctx); err != nil && !ent.IsConstraintError(err) {
				return err
			}
			continue
		}
		update := client.ModelBinding.Update().
			Where(
				modelbinding.ModelIDEQ(modelUUID),
				modelbinding.ProviderIDEQ(providerID),
				modelbinding.AdapterEQ(adapter),
			).
			SetUpstreamModelName(upstreamModelID).
			SetStatus(modelbinding.StatusActive)
		if credentialID := lookupCatalogCredential(ctx, client, providerID, upstreamModelID); credentialID != nil {
			update.SetCredentialID(*credentialID)
		}
		if _, err := update.Save(ctx); err != nil {
			return err
		}
	}
	existing, err := client.ModelBinding.Query().Where(
		modelbinding.ModelIDEQ(modelUUID),
		modelbinding.ProviderIDEQ(providerID),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range existing {
		if _, ok := expectedSet[binding.Adapter]; ok || binding.Status != modelbinding.StatusActive {
			continue
		}
		if err := client.ModelBinding.UpdateOne(binding).SetStatus(modelbinding.StatusInactive).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func applyExplicitMetadataCreate(builder *ent.ModelCreate, meta UpstreamMetadata, description string) {
	if description != "" {
		builder.SetDescription(description)
	}
	if meta.Category != nil {
		builder.SetCategory(*meta.Category)
	}
	if len(meta.Capabilities) > 0 {
		builder.SetCapabilities(meta.Capabilities)
	}
	if len(meta.InputModalities) > 0 {
		builder.SetInputModalities(meta.InputModalities)
	}
	if len(meta.OutputModalities) > 0 {
		builder.SetOutputModalities(meta.OutputModalities)
	}
	if meta.ContextWindow != nil {
		builder.SetContextWindow(*meta.ContextWindow)
	}
	if meta.MaxInputTokens != nil {
		builder.SetMaxInputTokens(*meta.MaxInputTokens)
	}
	if meta.MaxOutputTokens != nil {
		builder.SetMaxOutputTokens(*meta.MaxOutputTokens)
	}
}

func applyExplicitMetadataUpdate(update *ent.ModelUpdateOne, meta UpstreamMetadata, description string) {
	if description != "" {
		update.SetDescription(description)
	}
	if meta.Category != nil {
		update.SetCategory(*meta.Category)
	}
	if len(meta.Capabilities) > 0 {
		update.SetCapabilities(meta.Capabilities)
	}
	if len(meta.InputModalities) > 0 {
		update.SetInputModalities(meta.InputModalities)
	}
	if len(meta.OutputModalities) > 0 {
		update.SetOutputModalities(meta.OutputModalities)
	}
	if meta.ContextWindow != nil {
		update.SetContextWindow(*meta.ContextWindow)
	}
	if meta.MaxInputTokens != nil {
		update.SetMaxInputTokens(*meta.MaxInputTokens)
	}
	if meta.MaxOutputTokens != nil {
		update.SetMaxOutputTokens(*meta.MaxOutputTokens)
	}
}

func cloneRawEntry(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(raw))
	for key, value := range raw {
		cloned[key] = value
	}
	return cloned
}

func lookupCatalogCredential(ctx context.Context, client *ent.Client, providerID uuid.UUID, upstreamModelID string) *uuid.UUID {
	credentialID, found, err := FindActiveCatalogCredential(ctx, client, providerID, upstreamModelID, uuid.Nil)
	if err != nil || !found {
		return nil
	}
	return credentialID
}
