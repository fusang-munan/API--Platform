package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

// EnforceSingleActiveBinding keeps one active upstream per model+adapter.
func EnforceSingleActiveBinding(ctx context.Context, client *ent.Client, modelID uuid.UUID, adapter modelbinding.Adapter, keepID uuid.UUID) error {
	_, err := client.ModelBinding.Update().
		Where(
			modelbinding.ModelIDEQ(modelID),
			modelbinding.AdapterEQ(adapter),
			modelbinding.StatusEQ(modelbinding.StatusActive),
			modelbinding.IDNEQ(keepID),
		).
		SetStatus(modelbinding.StatusInactive).
		Save(ctx)
	return err
}
