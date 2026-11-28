import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { buildCurrentGroupPriceRow as buildPriceRow, priceReferenceText, type GroupPriceRowOptions } from './groupPriceRows'

function buildCurrentGroupPriceRow(locations: BeichenWebsiteLocation[], group?: number, options?: GroupPriceRowOptions) {
  return buildPriceRow(locations, group, { schoolMultiplier: '1.10', ...options })
}

const claudePrice: BeichenPriceConditions = {
  billing_mode: 'token',
  input_price: '0.000004',
  output_price: '0.00002',
  cache_write_price: '0.000005',
  cache_write_1h_price: '0.000006',
  cache_read_price: '0.0000004',
  intervals: [],
}

function location(group: BeichenWebsiteGroup, price: BeichenPriceConditions | null = claudePrice,
  overrides: Partial<BeichenWebsiteLocation> = {}): BeichenWebsiteLocation {
  return {
    name: 'claude-opus-5-5', platform: 'anthropic', groups: [group], observed_at: '2026-11-21T00:00:00Z',
    has_price: price != null, price_complete: price != null,
    ...(price ? { price_conditions_exact: price } : {}),
    ...overrides,
  }
}

describe('当前 Key 分组价格行', () => {
  it('按后端系数计算 1 元 × 100 × 0.5 × 1.10 并保留零价', () => {
    const locations = [location({ id: 10, rate_multiplier: '0.5' },
      { billing_mode: 'token', input_price: '0.000001', output_price: '0', cache_read_price: null })]
    expect(buildPriceRow(locations, 10, { schoolMultiplier: '1.10' })?.student)
      .toMatchObject({ input: '55', output: '0', cache_read: null })
    expect(buildPriceRow(locations, 10, { schoolMultiplier: '1' })?.student.input).toBe('50')
    expect(buildPriceRow(locations, 10)?.student.input).toBeNull()
  })
  it('当前分组有完整官网价时，直接使用该分组基础价', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 16, name: '[CC2]ClaudeCode高智', status: 'active', rate_multiplier: '0.6' }),
    ], 16, { keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' } })
    expect(row?.official).toEqual({ input: '4', output: '20', cache5m: '5', cache1h: '6', cache_read: '0.4' })
    expect(row?.student).toEqual({ input: '264', output: '1320', cache5m: '330', cache1h: '396', cache_read: '26.4' })
    expect(row?.student_source).toBe('website')
    expect(row?.base_from_other_group).toBe(false)
    expect(row?.base_note).toBeNull()
  })

  it('当前分组没有完整官网价时，取 CC1 同名模型基础价再乘当前 Key 倍率', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 16, name: '[CC2]ClaudeCode高智', status: 'active', rate_multiplier: '0.6' },
        { input_price: '0.000009' }, { price_complete: false }),
      location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' },
        claudePrice, { channel: 'Claude 渠道', price_scope: 'platform' }),
    ], 16, { keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' } })
    expect(row?.official).toEqual({ input: '4', output: '20', cache5m: '5', cache1h: '6', cache_read: '0.4' })
    expect(row?.student).toEqual({ input: '264', output: '1320', cache5m: '330', cache1h: '396', cache_read: '26.4' })
    expect(row?.base_from_other_group).toBe(true)
    expect(row?.base_group_id).toBe(2)
    expect(row?.base_label).toBe('CC1')
    expect(row?.base_note).toBe('基础价来自 CC1 同名模型；当前 Key 分组 16 的倍率为 0.60×')
  })

  it('分组专属价和缺少 price_scope 的旧数据不能跨组作为基础价', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' },
        claudePrice, { channel: 'CC1', price_scope: 'group' }),
      location({ id: 3, name: '分组 3', status: 'active' }, claudePrice, { price_scope: undefined }),
    ], 16, { keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' } })
    expect(row?.base_missing).toBe(true)
    expect(row?.base_from_other_group).toBe(false)
    expect(row?.base_note).toBeNull()
    expect(row?.official).toEqual({ input: null, output: null, cache5m: null, cache1h: null, cache_read: null })
    expect(row?.student).toEqual({ input: null, output: null, cache5m: null, cache1h: null, cache_read: null })
    expect(row?.effective_rate).toBe('0.6')
  })

  it('表头生效倍率优先 account_rate，并单独说明分组倍率差异', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 10, name: 'GPT 分组', status: 'active', rate_multiplier: '0.24' },
        { input_price: '0.000008', output_price: '0.00004' }),
    ], 10, { keyGroup: { id: 10, name: 'GPT 分组', rate: '0.24', account_rate: '0.2' } })
    expect(row?.rate).toBe('0.24')
    expect(row?.account_rate).toBe('0.2')
    expect(row?.effective_rate).toBe('0.2')
    expect(row?.rate_note).toBe('分组 0.24× / 账号 0.20×')
    expect(row?.student.input).toBe('176')
  })

  it('陈旧或未标完整的 location 不能作为基础价', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 2, name: 'CC1', status: 'active' }, claudePrice, { stale: true }),
      location({ id: 3, name: 'CC2', status: 'active' }, claudePrice, { price_complete: false }),
    ], 16, { keyGroup: { id: 16, name: '分组 16', rate: '0.6' } })
    expect(row?.base_missing).toBe(true)
    expect(row?.official).toEqual({ input: null, output: null, cache5m: null, cache1h: null, cache_read: null })
    expect(row?.student).toEqual({ input: null, output: null, cache5m: null, cache1h: null, cache_read: null })
    expect(row?.student_source).toBeNull()
  })

  it('账户倍率优先于分组倍率计算学生价', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.2', account_rate_multiplier: '0.16' }),
    ], 15, { keyGroup: { id: 15, name: '分组 15', rate: '0.2', account_rate: '0.16' } })
    expect(row?.rate).toBe('0.2')
    expect(row?.account_rate).toBe('0.16')
    expect(row?.student.input).toBe('70.4')
    expect(row?.student.output).toBe('352')
  })

  it('没有官网基础价时主价留空，历史反推价只生成参考文字', () => {
    const derived: BeichenPriceConditions = { billing_mode: 'token', input_price: '0.000004', output_price: '0.00002' }
    const row = buildCurrentGroupPriceRow([], 16, {
      keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' }, derived,
    })
    expect(row?.base_missing).toBe(true)
    expect(row?.official.input).toBeNull()
    expect(row?.student.input).toBeNull()
    expect(row?.student_source).toBeNull()
    expect(priceReferenceText(derived)).toBe('输入 4 元、输出 20 元')
  })

  it('扁平五项与学生实价保持同步，兼容旧调用方', () => {
    const row = buildCurrentGroupPriceRow([
      location({ id: 16, name: '分组 16', status: 'active', rate_multiplier: '0.6' }),
    ], 16)
    expect(row?.input).toBe('264')
    row!.input = '999'
    expect(row?.student.input).toBe('999')
  })
})
