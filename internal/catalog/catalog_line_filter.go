package catalog

import "strings"

// Beichen returns the visible catalog for the authenticated key. Other
// providers may expose a shared list that needs filtering by key line.
func FilterProviderEntriesByCredential(label string, groupHint *string, baseURL string, entries []ProviderEntry) []ProviderEntry {
	if IsBeichenBaseURL(baseURL) {
		return entries
	}
	switch resolveCredentialLine(label, groupHint, baseURL) {
	case "claude":
		return filterProviderEntries(entries, func(modelID string) bool {
			id := strings.ToLower(strings.TrimSpace(modelID))
			return strings.HasPrefix(id, "claude")
		})
	case "codex":
		return filterProviderEntries(entries, func(modelID string) bool {
			id := strings.ToLower(strings.TrimSpace(modelID))
			return !strings.HasPrefix(id, "claude")
		})
	case "gemini":
		return filterProviderEntries(entries, func(modelID string) bool {
			id := strings.ToLower(strings.TrimSpace(modelID))
			return strings.HasPrefix(id, "gemini") || strings.Contains(id, "gemini")
		})
	default:
		return entries
	}
}

func resolveCredentialLine(label string, groupHint *string, baseURL string) string {
	for _, token := range []string{label, derefString(groupHint)} {
		token = strings.ToLower(strings.TrimSpace(token))
		switch {
		case strings.Contains(token, "claude"):
			return "claude"
		case strings.Contains(token, "codex"), strings.Contains(token, "gpt"), strings.Contains(token, "openai"):
			return "codex"
		case strings.Contains(token, "gemini"):
			return "gemini"
		}
	}
	return resolveLineFromBaseURL(baseURL)
}

func resolveLineFromBaseURL(baseURL string) string {
	path := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case strings.Contains(path, "/codex"):
		return "codex"
	case strings.Contains(path, "/claude"):
		return "claude"
	case strings.Contains(path, "/gemini"):
		return "gemini"
	default:
		return ""
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// FilterProviderEntriesByLine is kept for callers that only know Base URL.
func FilterProviderEntriesByLine(baseURL string, entries []ProviderEntry) []ProviderEntry {
	return FilterProviderEntriesByCredential("", nil, baseURL, entries)
}

func filterProviderEntries(entries []ProviderEntry, keep func(string) bool) []ProviderEntry {
	if len(entries) == 0 {
		return entries
	}
	filtered := make([]ProviderEntry, 0, len(entries))
	for _, entry := range entries {
		if keep(entry.ID) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
