package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

// ModelGovernanceConfigured reports whether a teacher already set pricing/governance.
func ModelGovernanceConfigured(row *ent.Model) bool {
	if row == nil {
		return false
	}
	return row.CreditMultiplierMilli != nil && *row.CreditMultiplierMilli > 0
}

// RestoreReadyGovernedModels re-activates models left in pending_configuration even though
// governance and routes are already in place. This heals stale state after Key maintenance
// and earlier sync bugs; it never downgrades or touches inactive models.
func RestoreReadyGovernedModels(ctx context.Context, client *ent.Client, providerID uuid.UUID) error {
	rows, err := client.Model.Query().
		Where(
			entmodel.StatusEQ(entmodel.StatusPendingConfiguration),
			entmodel.HasBindingsWith(modelbinding.ProviderIDEQ(providerID)),
		).
		All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if !ModelGovernanceConfigured(row) {
			continue
		}
		ready, err := ModelHasActiveRoute(ctx, client, row.ID)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		if _, err := row.Update().SetStatus(entmodel.StatusActive).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}
