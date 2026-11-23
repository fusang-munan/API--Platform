import { describe, expect, it } from 'vitest'
import type { StudentProjectUsage } from '../api/types'
import {
  formatCreditsMilli,
  formatStudentModelUsage,
  parseCreditsMilli,
  summarizeStudentModels,
  sumPendingCredits,
} from './studentUsage'

function project(
  id: string,
  pending: string,
  keys: Array<Record<string, unknown>>,
): StudentProjectUsage {
  return {
    project_id: id,
    project_name: id,
    quota: '10.000',
    used: '2.212',
    pending,
    available: '7.288',
    keys,
  } as unknown as StudentProjectUsage
}

describe('student usage summary', () => {
  it('adds credits and calls for the same model across projects and keys', () => {
    const rows = summarizeStudentModels([
      project('a', '0.400', [{
        id: 'key-a', name: 'Key A', status: 'active', used: '1.100', pending: '0.400',
        models: [
          { id: 'model-x', name: 'Model X', credits: '1.100', calls: 2 },
          { id: 'free-model', name: 'Free Model', credits: '0.000', calls: 3 },
        ],
      }]),
      project('b', '0.100', [{
        id: 'key-b', name: 'Key B', status: 'active', used: '1.112', pending: '0.100',
        models: [{ id: 'MODEL-X', name: 'Model X', credits: '1.112', calls: 1 }],
      }]),
    ])

    expect(rows).toEqual([
      { id: 'model-x', name: 'Model X', credits: '2.212', calls: 3, zeroCost: false, tokens: null, cacheHitRate: null },
      { id: 'free-model', name: 'Free Model', credits: '0.000', calls: 3, zeroCost: true, tokens: null, cacheHitRate: null },
    ])
  })

  it('treats an omitted calls field as zero and keeps explicit zero credits', () => {
    const rows = summarizeStudentModels([project('a', '0.000', [{
      id: 'key-a', name: 'Key A', status: 'active', used: '0.000', pending: '0.000',
      models: [{ id: 'idle-model', name: 'Idle', credits: '0.000' }],
    }])])

    expect(rows).toEqual([
      { id: 'idle-model', name: 'Idle', credits: '0.000', calls: 0, zeroCost: false, tokens: null, cacheHitRate: null },
    ])
  })

  it('sums token details only when every slice provides them, and weights cache rate by calls', () => {
    const rows = summarizeStudentModels([project('a', '0.000', [
      {
        id: 'key-a', name: 'Key A', status: 'active', used: '9.000', pending: '0.000',
        models: [{ id: 'model-x', name: 'Model X', credits: '6.000', calls: 3, tokens: 1200, cache_hit_rate: 0.2 }],
      },
      {
        id: 'key-b', name: 'Key B', status: 'active', used: '3.000', pending: '0.000',
        models: [{ id: 'model-x', name: 'Model X', credits: '3.000', calls: 1, tokens: 300, cache_hit_rate: 0.6 }],
      },
    ])])

    expect(rows[0].tokens).toBe(1500)
    expect(rows[0].cacheHitRate).toBeCloseTo(0.3)
  })

  it('shows a dash instead of a partial token or cache total', () => {
    const rows = summarizeStudentModels([project('a', '0.000', [{
      id: 'key-a', name: 'Key A', status: 'active', used: '1.000', pending: '0.000',
      models: [
        { id: 'model-x', name: 'Model X', credits: '0.500', calls: 1, tokens: 100, cache_hit_rate: 0.5 },
        { id: 'MODEL-X', name: 'Model X', credits: '0.500', calls: 1 },
      ],
    }])])

    expect(rows).toHaveLength(1)
    expect(rows[0].tokens).toBeNull()
    expect(rows[0].cacheHitRate).toBeNull()
  })

  it('sums pending credits with exact thousandths and formats labels', () => {
    expect(sumPendingCredits([
      project('a', '0.400', []),
      project('b', '0.100', []),
    ])).toBe('0.500')
    expect(formatCreditsMilli(2122)).toBe('2.122')
    expect(formatCreditsMilli(0)).toBe('0.000')
    expect(parseCreditsMilli('12.500')).toBe(12500)
    expect(parseCreditsMilli('not-a-number')).toBeNull()
  })

  it('builds the model plaza labels without claiming an unused model', () => {
    expect(formatStudentModelUsage(undefined)).toBe('本月 0.000 积分 · 0 次')
    expect(formatStudentModelUsage({
      id: 'free-model', name: 'Free', credits: '0.000', calls: 3, zeroCost: true, tokens: null, cacheHitRate: null,
    })).toBe('本月 0 积分 · 3 次（未结算/待核或免费）')
    expect(formatStudentModelUsage({
      id: 'model-x', name: 'Model X', credits: '12.500', calls: 4, zeroCost: false, tokens: null, cacheHitRate: null,
    })).toBe('本月已用 12.5 积分 · 4 次')
  })

  it('ignores rows without a model identifier', () => {
    const rows = summarizeStudentModels([project('a', '0.000', [{
      id: 'key-a', name: 'Key A', status: 'active', used: '0.000', pending: '0.000',
      models: [{ id: '', name: '', credits: '1.000', calls: 1 }],
    }])])
    expect(rows).toEqual([])
  })
})
