package catalog

import (
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestPickCredentialForUpstreamModel(t *testing.T) {
	claudeID := uuid.New()
	gptID := uuid.New()
	credentials := []*ent.ProviderCredential{
		{ID: claudeID, Label: "claude"},
		{ID: gptID, Label: "gpt"},
	}
	base := "https://api.llmapi.site"

	picked := PickCredentialForUpstreamModel(credentials, "gpt-5.6-luna", base)
	if picked == nil || picked.ID != gptID {
		t.Fatalf("expected gpt credential for luna, got %#v", picked)
	}
	picked = PickCredentialForUpstreamModel(credentials, "claude-sonnet-5", base)
	if picked == nil || picked.ID != claudeID {
		t.Fatalf("expected claude credential for claude model, got %#v", picked)
	}
}
