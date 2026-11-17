import codexOfficialIndex from '../data/codex_official_index.json'

export type CodexOfficialReasoningLevel = {
  effort: string
  description: string
}

export type CodexOfficialIndexEntry = {
  slug: string
  supported_reasoning_levels: CodexOfficialReasoningLevel[]
  default_reasoning_level?: string
}

const index = codexOfficialIndex as Record<string, CodexOfficialIndexEntry>

const fallbackReasoningLevels: CodexOfficialReasoningLevel[] = [
  { effort: 'low', description: 'Fast responses with lighter reasoning' },
  { effort: 'medium', description: 'Balances speed and reasoning depth for everyday tasks' },
  { effort: 'high', description: 'Greater reasoning depth for complex problems' },
]

/** Effort ids from OpenAI Codex bundled catalog when known; otherwise low/medium/high. */
export function codexSupportedReasoningEfforts(modelId: string): string[] {
  return codexReasoningLevelsForModel(modelId).map(level => level.effort)
}

export function codexReasoningLevelsForModel(modelId: string): CodexOfficialReasoningLevel[] {
  const key = modelId.trim().toLowerCase()
  const entry = key ? index[key] : undefined
  if (entry?.supported_reasoning_levels?.length) {
    return entry.supported_reasoning_levels
  }
  return fallbackReasoningLevels
}
