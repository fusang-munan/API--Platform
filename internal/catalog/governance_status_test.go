package catalog

import (
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestModelGovernanceConfigured(t *testing.T) {
	t.Parallel()
	multiplier := int64(500)
	if !ModelGovernanceConfigured(&ent.Model{CreditMultiplierMilli: &multiplier}) {
		t.Fatal("expected configured model")
	}
	zero := int64(0)
	if ModelGovernanceConfigured(&ent.Model{CreditMultiplierMilli: &zero}) {
		t.Fatal("expected fresh model")
	}
}
