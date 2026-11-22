import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions } from '../api/types'
import { calculateBeichenPriceTable } from './priceCalculation'

const claudeSonnet: BeichenPriceConditions = {
  billing_mode: 'token',
  input_price: '0.000003',
  output_price: '0.000015',
  cache_write_price: '0.00000375',
  cache_write_1h_price: null,
  cache_read_price: '0.0000003',
}

const gptAstra: BeichenPriceConditions = {
  billing_mode: 'token',
  input_price: '0.000008',
  output_price: '0.00004',
  cache_write_price: '0.00001',
  cache_write_1h_price: '0.00001',
  cache_read_price: '0.0000004',
}

const derived = {
  price_conditions_exact: { billing_mode: 'token', input_price: '0.000004', cache_write_1h_price: '0.000005' },
  observed_at: '2026-11-19T09:00:00Z',
}

function amounts(table: ReturnType<typeof calculateBeichenPriceTable>, column: 'base' | 'settlement' | 'student') {
  return table.rows.map(row => {
    const amount = row[column]
    return amount ? [amount.yuan, amount.credits] : null
  })
}

describe('北辰教师报价计算', () => {
  it('按 0.6 倍计算 claude-sonnet-4-6 的结算和学生价，保留未列出的 1h 单价', () => {
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: claudeSonnet, observed_at: '2026-11-21T13:07:28Z' },
      groupMultiplier: '0.6',
      derived,
    })
    expect(table).toMatchObject({ source: 'website', groupMultiplierLabel: '0.60×', observedAt: '2026-11-21T13:07:28Z' })
    expect(amounts(table, 'base')).toEqual([['3', '300'], ['15', '1500'], ['3.75', '375'], null, ['0.3', '30']])
    expect(amounts(table, 'settlement')).toEqual([['1.8', '180'], ['9', '900'], ['2.25', '225'], null, ['0.18', '18']])
    expect(amounts(table, 'student')).toEqual([['1.98', '198'], ['9.9', '990'], ['2.475', '247.5'], null, ['0.198', '19.8']])
  })

  it('读取 gpt-6.2-astra 全部五项报价，两个缓存写入时长同价时各自保留', () => {
    const table = calculateBeichenPriceTable({ website: { price_conditions_exact: gptAstra }, groupMultiplier: 0.16 })
    expect(amounts(table, 'base')).toEqual([['8', '800'], ['40', '4000'], ['10', '1000'], ['10', '1000'], ['0.4', '40']])
    expect(amounts(table, 'settlement')).toEqual([['1.28', '128'], ['6.4', '640'], ['1.6', '160'], ['1.6', '160'], ['0.064', '6.4']])
    expect(amounts(table, 'student')).toEqual([['1.408', '140.8'], ['7.04', '704'], ['1.76', '176'], ['1.76', '176'], ['0.0704', '7.04']])
  })

  it('沿用官网已解析档位继承的 1h 单价，独立于 5m 计算', () => {
    const exact: BeichenPriceConditions = {
      billing_mode: 'token',
      cache_write_price: '0.0000025',
      cache_write_1h_price: '0.000004',
      intervals: [{
        min_tokens: '500000', max_tokens: '500002',
        cache_write_price: '0.000005', cache_write_1h_price: '0.000008',
      }],
    }
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: exact.intervals?.[0] },
      groupMultiplier: '0.6',
    })
    expect(table.rows.find(row => row.key === 'cache_write_5m')?.student).toEqual({ yuan: '3.3', credits: '330' })
    expect(table.rows.find(row => row.key === 'cache_write_1h')).toMatchObject({
      base: { yuan: '8', credits: '800' },
      settlement: { yuan: '4.8', credits: '480' },
      student: { yuan: '5.28', credits: '528' },
    })
  })

  it.each([undefined, null, '', '  '])('官网 1h 单价为空（%s）时保留空项', cacheWrite1h => {
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: { ...claudeSonnet, cache_write_1h_price: cacheWrite1h } },
      groupMultiplier: '0.6',
      derived,
    })
    expect(table.source).toBe('website')
    expect(table.rows.find(row => row.key === 'cache_write_1h')).toMatchObject({
      notApplicable: false, base: null, settlement: null, student: null,
    })
  })

  it.each([undefined, null])('官网整份计价条件缺失（%s）才使用反推基础单价及其时间', conditions => {
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: conditions, observed_at: '2026-11-21T13:00:00Z' },
      groupMultiplier: '1',
      derived,
    })
    expect(table).toMatchObject({ source: 'derived', observedAt: derived.observed_at })
    expect(table.rows[0]).toMatchObject({
      base: { yuan: '4', credits: '400' },
      settlement: { yuan: '4', credits: '400' },
      student: { yuan: '4.4', credits: '440' },
    })
    expect(table.rows[1].student).toBeNull()
  })

  it('官网存在按次报价时不显示反推 Token 价', () => {
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: { billing_mode: 'per_request', per_request_price: '0.5' } },
      groupMultiplier: '0.6',
      derived,
    })
    expect(table).toMatchObject({ source: 'website', observedAt: null })
    expect(table.rows.every(row => row.notApplicable && row.base === null && row.student === null)).toBe(true)
  })

  it.each([null, '', '-0.6', '无效'])('分组倍率未知（%s）时只保留基础价', groupMultiplier => {
    const table = calculateBeichenPriceTable({ website: { price_conditions_exact: gptAstra }, groupMultiplier })
    expect(table.groupMultiplierLabel).toBeNull()
    expect(table.rows[0]).toMatchObject({ base: { yuan: '8', credits: '800' }, settlement: null, student: null })
  })

  it('保留官网明确列出的零和当前分组的零倍率', () => {
    const table = calculateBeichenPriceTable({
      website: { price_conditions_exact: { input_price: gptAstra.input_price, cache_write_1h_price: 0 } },
      groupMultiplier: 0,
      derived,
    })
    const zero = { yuan: '0', credits: '0' }
    expect(table).toMatchObject({ source: 'website', groupMultiplierLabel: '0.00×' })
    expect(table.rows[0]).toMatchObject({ base: { yuan: '8', credits: '800' }, settlement: zero, student: zero })
    expect(table.rows.find(row => row.key === 'cache_write_1h')).toMatchObject({ base: zero, settlement: zero, student: zero })
  })
})
