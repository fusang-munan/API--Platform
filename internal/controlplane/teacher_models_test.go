package controlplane

import (
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providermodelcatalog"
)

func TestDedupeCatalogRowsKeepsFirstModelPerID(t *testing.T) {
	t.Parallel()
	first := &ent.ProviderModelCatalog{ModelID: "gpt-5", DisplayName: strPtr("GPT 5")}
	second := &ent.ProviderModelCatalog{ModelID: "gpt-5", DisplayName: strPtr("Duplicate")}
	third := &ent.ProviderModelCatalog{ModelID: "claude-sonnet-4-6"}

	rows := dedupeCatalogRows([]*ent.ProviderModelCatalog{first, second, third})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].ModelID != "gpt-5" || rows[0].DisplayName == nil || *rows[0].DisplayName != "GPT 5" {
		t.Fatalf("expected first gpt-5 row to win, got %#v", rows[0])
	}
	if rows[1].ModelID != "claude-sonnet-4-6" {
		t.Fatalf("unexpected second row: %#v", rows[1])
	}
}

func TestTeacherModelLineViewUsesProviderAndKeyLabels(t *testing.T) {
	t.Parallel()
	providerID := uuid.Must(uuid.NewV7())
	credentialID := uuid.Must(uuid.NewV7())
	displayName := "Claude Sonnet"
	catalogRow := &ent.ProviderModelCatalog{
		ModelID:     "claude-sonnet-4-6",
		DisplayName: &displayName,
		Status:      providermodelcatalog.StatusActive,
	}
	credential := &ent.ProviderCredential{
		ID:    credentialID,
		Label: "claude",
	}
	providerRow := &ent.Provider{
		ID:   providerID,
		Name: "NewCli",
	}

	view := teacherModelLineView(catalogRow, credential, providerRow, nil)
	if view.ModelID != "claude-sonnet-4-6" || view.DisplayName != "Claude Sonnet" {
		t.Fatalf("unexpected model fields: %#v", view)
	}
	if view.ProviderID != providerID.String() || view.ProviderName != "NewCli" {
		t.Fatalf("unexpected provider fields: %#v", view)
	}
	if view.CredentialID != credentialID.String() || view.CredentialLabel != "claude" {
		t.Fatalf("unexpected credential fields: %#v", view)
	}
	if view.Status != "pending_configuration" {
		t.Fatalf("expected pending configuration for catalog-only row, got %q", view.Status)
	}
}

func TestShouldSyncProviderCatalogWhenProviderBecomesActive(t *testing.T) {
	t.Parallel()
	before := &ent.Provider{Status: provider.StatusInactive}
	after := &ent.Provider{Status: provider.StatusActive}
	if !shouldSyncProviderCatalog(before, after, ProviderInput{}) {
		t.Fatal("expected catalog sync when provider becomes active")
	}
}

func strPtr(value string) *string {
	return &value
}
