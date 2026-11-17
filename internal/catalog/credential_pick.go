package catalog

import (
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

// PickCredentialForUpstreamModel chooses the provider Key line that should own an upstream model id.
// Used when probing without an explicit credential_id (same rules as catalog sync filtering).
func PickCredentialForUpstreamModel(credentials []*ent.ProviderCredential, upstreamModel, baseURL string) *ent.ProviderCredential {
	if len(credentials) == 0 {
		return nil
	}
	want := credentialLineForModelID(upstreamModel)
	if want == "" {
		return credentials[0]
	}
	for _, row := range credentials {
		line := resolveCredentialLine(row.Label, row.GroupHint, baseURL)
		if line == want {
			return row
		}
	}
	return credentials[0]
}

func credentialLineForModelID(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(id, "claude"):
		return "claude"
	case strings.HasPrefix(id, "gemini") || strings.Contains(id, "gemini"):
		return "gemini"
	default:
		return "codex"
	}
}
