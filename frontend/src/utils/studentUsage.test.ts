import { describe, expect, it } from 'vitest'
import { formatStudentModelUsage, formatStudentModelUsageParts } from './studentUsage'

describe('student model usage labels', () => {
  it('keeps the full precision of the selected Key model amount', () => {
    expect(formatStudentModelUsage({
      credits: '0.00040000000000001', calls: 2, pending_review_calls: 0,
    })).toBe('本月已用 0.00040000000000001 积分 · 2 次')
  })

  it('reports settled zero-cost calls and pending review separately', () => {
    expect(formatStudentModelUsageParts({
      credits: '0.000', calls: 3, pending_review_calls: 1,
    })).toEqual({ credits: '本月 0 积分', calls: '3 次 · 待核 1 笔' })
  })

  it('keeps pending-only models visible when the settled call count is omitted', () => {
    expect(formatStudentModelUsage({
      credits: '0.000', pending_review_calls: 2,
    })).toBe('本月 0.000 积分 · 0 次 · 待核 2 笔')
    expect(formatStudentModelUsage(undefined)).toBe('本月 0.000 积分 · 0 次')
  })
})
