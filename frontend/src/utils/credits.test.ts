import { describe, expect, it } from 'vitest'
import { creditsToYuan, formatCredits, formatPercent, formatTokens, formatYuan, multiplyDecimal, shiftDecimal, trimDecimal } from './credits'

describe('credit display helpers', () => {
  it('trims and groups credit strings', () => {
    expect(trimDecimal('20000.000')).toBe('20000')
    expect(trimDecimal('0.1500')).toBe('0.15')
    expect(formatCredits('20000.000')).toBe('20,000')
    expect(formatCredits('1234.5678')).toBe('1,234.568')
    expect(formatCredits(null)).toBe('—')
  })

  it('converts 100 credits to one yuan exactly', () => {
    expect(shiftDecimal('123.456', -2)).toBe('1.23456')
    expect(creditsToYuan('123.456')).toBe('1.23')
    expect(creditsToYuan('0.5')).toBe('0.005')
    expect(formatYuan('0.005')).toBe('0.005')
    expect(formatYuan('1.2')).toBe('1.2')
  })

  it('formats tokens and percentages', () => {
    expect(formatTokens(1234567)).toBe('1,234,567')
    expect(formatPercent('0.42')).toBe('42%')
    expect(formatPercent('0.426')).toBe('42.6%')
    expect(formatPercent(null)).toBe('—')
  })

  it('multiplies decimals without floating point drift', () => {
    expect(multiplyDecimal('150', '1.10')).toBe('165')
    expect(multiplyDecimal('0.15', '1.1')).toBe('0.165')
    expect(multiplyDecimal('187.5', '1.10')).toBe('206.25')
  })
})
