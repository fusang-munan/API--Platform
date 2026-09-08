package catalog

import (
	"strings"
)

func UnionProviderEntries(lists ...[]ProviderEntry) []ProviderEntry {
	seen := make(map[string]ProviderEntry)
	order := make([]string, 0)
	for _, list := range lists {
		for _, entry := range list {
			modelID := strings.TrimSpace(entry.ID)
			if modelID == "" {
				continue
			}
			key := strings.ToLower(modelID)
			if existing, ok := seen[key]; ok {
				seen[key] = mergeProviderEntry(existing, entry)
				continue
			}
			seen[key] = entry
			order = append(order, key)
		}
	}
	result := make([]ProviderEntry, 0, len(order))
	for _, key := range order {
		result = append(result, seen[key])
	}
	return result
}

func mergeProviderEntry(left, right ProviderEntry) ProviderEntry {
	merged := left
	if strings.TrimSpace(merged.DisplayName) == "" {
		merged.DisplayName = right.DisplayName
	}
	if strings.TrimSpace(merged.Description) == "" {
		merged.Description = right.Description
	}
	if strings.TrimSpace(merged.OwnedBy) == "" {
		merged.OwnedBy = right.OwnedBy
	}
	if strings.TrimSpace(merged.Object) == "" {
		merged.Object = right.Object
	}
	if strings.TrimSpace(merged.CreatedAt) == "" {
		merged.CreatedAt = right.CreatedAt
	}
	if len(right.Raw) > len(merged.Raw) {
		merged.Raw = right.Raw
	}
	return merged
}
