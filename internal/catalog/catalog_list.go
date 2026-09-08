package catalog

import (
	"sort"
	"strings"
)

func FilterProviderEntries(entries []ProviderEntry, query string) []ProviderEntry {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return sortProviderEntries(entries)
	}
	filtered := make([]ProviderEntry, 0, len(entries))
	for _, entry := range entries {
		if providerEntryMatchesQuery(entry, query) {
			filtered = append(filtered, entry)
		}
	}
	return sortProviderEntries(filtered)
}

func PaginateProviderEntries(entries []ProviderEntry, page, pageSize int) ([]ProviderEntry, int) {
	total := len(entries)
	if total == 0 || page < 1 || pageSize < 1 {
		return nil, total
	}
	start := (page - 1) * pageSize
	if start >= total {
		return nil, total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return entries[start:end], total
}

func sortProviderEntries(entries []ProviderEntry) []ProviderEntry {
	if len(entries) < 2 {
		return entries
	}
	sorted := append([]ProviderEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		left := strings.ToLower(sorted[i].ID)
		right := strings.ToLower(sorted[j].ID)
		if left == right {
			return sorted[i].ID < sorted[j].ID
		}
		return left < right
	})
	return sorted
}

func ProviderEntryMatchesQuery(entry ProviderEntry, query string) bool {
	return providerEntryMatchesQuery(entry, query)
}

func providerEntryMatchesQuery(entry ProviderEntry, query string) bool {
	for _, value := range []string{
		entry.ID,
		entry.DisplayName,
		entry.Description,
		entry.OwnedBy,
		entry.Object,
		entry.CreatedAt,
	} {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), query) {
			return true
		}
	}
	if entry.Raw != nil {
		for key, value := range entry.Raw {
			if strings.Contains(strings.ToLower(key), query) {
				return true
			}
			if text, ok := value.(string); ok && strings.Contains(strings.ToLower(text), query) {
				return true
			}
		}
	}
	return false
}
