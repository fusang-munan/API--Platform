import type { ModelCatalogCandidate } from '../api/types'

export function catalogDisplayName(item: ModelCatalogCandidate): string {
  return item.display_name?.trim() || item.model_id
}

export function catalogFieldEntries(raw: Record<string, unknown> | undefined): Array<{ key: string; value: string }> {
  if (!raw) return []
  return Object.entries(raw)
    .filter(([key]) => key !== 'id')
    .map(([key, value]) => ({ key, value: formatCatalogValue(value) }))
    .sort((a, b) => a.key.localeCompare(b.key))
}

function formatCatalogValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}
