import { describe, expect, it } from 'vitest'
import { compareModelsByRecency, modelVersionScore, sortModelsByRecency } from './modelSort'

describe('modelSort', () => {
  it('sorts higher version numbers first', () => {
    expect(compareModelsByRecency('gpt-5.6-sol', 'gpt-5.3-codex')).toBeLessThan(0)
    expect(compareModelsByRecency('gpt-5.3-codex', 'gpt-5.6-sol')).toBeGreaterThan(0)
  })

  it('sorts model list with newest first', () => {
    const sorted = sortModelsByRecency([
      { model_id: 'gpt-5.3-codex' },
      { model_id: 'gpt-5.6-sol' },
      { model_id: 'gpt-4.1' },
    ])
    expect(sorted.map(item => item.model_id)).toEqual(['gpt-5.6-sol', 'gpt-5.3-codex', 'gpt-4.1'])
  })

  it('ignores date suffixes when scoring versions', () => {
    expect(compareModelsByRecency('gpt-5.6-sol', 'gpt-5.4-mini-2026-8-11')).toBeLessThan(0)
    expect(modelVersionScore('gpt-5.4-mini-2026-8-11')).toBe(modelVersionScore('gpt-5.4-mini'))
  })
})
