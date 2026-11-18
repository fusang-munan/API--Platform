import { describe, expect, it } from 'vitest'
import { calculateCredits, statusLabel } from './format'

describe('credit display', () => {
  it('keeps exact amounts during additions and subtraction', () => {
    expect(calculateCredits('1.000', '0.00000000000001', 'subtract')).toBe('0.99999999999999')
    expect(calculateCredits('0.99999999999999', '0.00000000000002', 'add')).toBe('1.00000000000001')
    expect(calculateCredits('1.200', '0.100', 'subtract')).toBe('1.100')
    expect(calculateCredits('1.000', '1.00000000000001', 'subtract')).toBe('0.000')
  })

  it('labels a billing review as pending', () => {
    expect(statusLabel('pending_review')).toBe('待核账')
  })
})
