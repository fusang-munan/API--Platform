import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { buildGroupPriceRows } from './groupPriceRows'

const glmPrice: BeichenPriceConditions = {
  billing_mode: 'token',
  input_price: '0.000008',
  output_price: '0.000028',
  cache_write_price: '0',
  cache_write_1h_price: null,
  cache_read_price: '0.000002',
  intervals: [],
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

function location(group: BeichenWebsiteGroup,
  price: BeichenPriceConditions | null = glmPrice): BeichenWebsiteLocation {
  return {
    name: 'glm-5.3', platform: 'openai', groups: [group], observed_at: '2026-11-21T00:00:00Z',
    has_price: price != null, price_complete: price != null,
    ...(price ? { price_conditions_exact: price } : {}),
  }
}

function glmLocations() {
  return [
    location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }),
    location({ id: 14, name: '分组 14', status: 'active', rate_multiplier: 0.15 }),
    location({ id: 6, name: '分组 6', status: 'active', rate_multiplier: '0.1' }),
    location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }),
  ]
}

function claudeLocations() {
  return [
    location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' },
      claudePrice),
  ]
}

describe('北辰分组价格行', () => {
  it('glm-5.3 的分组 15、14、6 各一行并按 group_id 去重', () => {
    const rows = buildGroupPriceRows(glmLocations(), 15)
    expect(rows.map(row => row.group_id)).toEqual([15, 14, 6])
    expect(rows.map(row => row.rate)).toEqual(['0.16', '0.15', '0.1'])
    expect(rows.filter(row => row.group_id === 15)).toHaveLength(1)
  })

  it('五项学生价按分组倍率和 1.10 换算成积分 / 1M Token', () => {
    const [own] = buildGroupPriceRows(glmLocations(), 15)
    expect(own.is_current_key_group).toBe(true)
    expect(own.input).toBe('140.8')
    expect(own.output).toBe('492.8')
    expect(own.cache5m).toBe('0')
    expect(own.cache1h).toBeNull()
    expect(own.cache_read).toBe('35.2')
    const [other] = buildGroupPriceRows(glmLocations(), 6).filter(row => row.group_id === 14)
    expect(other.input).toBe('132')
  })

  it('官网价与学生价都固定包含五项', () => {
    const [row] = buildGroupPriceRows(claudeLocations(), 2)
    const keys = ['input', 'output', 'cache5m', 'cache1h', 'cache_read']
    expect(Object.keys(row.official).sort()).toEqual([...keys].sort())
    expect(Object.keys(row.student).sort()).toEqual([...keys].sort())
    expect(row.official).toEqual({
      input: '4', output: '20', cache5m: '5', cache1h: '6', cache_read: '0.4',
    })
  })

  it('CC1 0.15 与 group16 0.6 各自显示两排价格', () => {
    const rows = buildGroupPriceRows(claudeLocations(), null, {
      keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' },
      derived: claudePrice,
    })
    expect(rows.map(row => row.group_id)).toEqual([16, 2])
    const [group16, cc1] = rows
    expect(cc1.official).toEqual({
      input: '4', output: '20', cache5m: '5', cache1h: '6', cache_read: '0.4',
    })
    expect(cc1.student).toEqual({
      input: '66', output: '330', cache5m: '82.5', cache1h: '99', cache_read: '6.6',
    })
    expect(cc1.student_source).toBe('website')
    expect(group16.official).toEqual({
      input: null, output: null, cache5m: null, cache1h: null, cache_read: null,
    })
    expect(group16.student).toEqual({
      input: '264', output: '1320', cache5m: '330', cache1h: '396', cache_read: '26.4',
    })
    expect(group16.student_source).toBe('derived')
    expect(group16.rate).toBe('0.6')
    expect(group16.is_current_key_group).toBe(true)
  })

  it('官网缺该组价时学生价用历史流水反推', () => {
    const [row] = buildGroupPriceRows(claudeLocations(), null, {
      keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' },
      derived: claudePrice,
    })
    expect(row.official.input).toBeNull()
    expect(row.student.input).toBe('264')
    expect(row.student_source).toBe('derived')
  })

  it('/keys 的 group_id/rate 覆盖官网同组倍率', () => {
    const [row] = buildGroupPriceRows(claudeLocations(), null, {
      keyGroup: { id: 2, name: '[CC1]Claudecode-kiro-015', rate: '0.2', account_rate: '0.18' },
    })
    expect(row.is_current_key_group).toBe(true)
    expect(row.rate).toBe('0.2')
    expect(row.account_rate).toBe('0.18')
    expect(row.student.input).toBe('79.2')
  })

  it('官网完全没有该组时也保留当前组行', () => {
    const rows = buildGroupPriceRows(claudeLocations(), null, {
      keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' },
    })
    expect(rows[0]).toMatchObject({
      group_id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6',
      is_current_key_group: true, student_source: null,
    })
    expect(rows[0].official.input).toBeNull()
    expect(rows[0].student.input).toBeNull()
  })

  it('旧扁平五项与学生实价保持同步，兼容教师页回写', () => {
    const [row] = buildGroupPriceRows(glmLocations(), 15)
    expect(row.input).toBe('140.8')
    row.input = '999'
    expect(row.student.input).toBe('999')
  })

  it('账户倍率覆盖分组倍率：0.1 / 0.08 按 0.08 计算', () => {
    const [row] = buildGroupPriceRows([
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.1', account_rate_multiplier: '0.08' }),
    ], 15)
    expect(row.rate).toBe('0.1')
    expect(row.account_rate).toBe('0.08')
    expect(row.input).toBe('70.4')
  })

  it('账户倍率覆盖分组倍率：0.15 / 0.14 按 0.14 计算', () => {
    const [row] = buildGroupPriceRows([
      location({ id: 14, name: '分组 14', status: 'active', rate_multiplier: '0.15', account_rate_multiplier: '0.14' }),
    ], 14)
    expect(row.rate).toBe('0.15')
    expect(row.account_rate).toBe('0.14')
    expect(row.input).toBe('123.2')
  })

  it('同组缺价记录不会覆盖已有的完整报价', () => {
    const rows = buildGroupPriceRows([
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }, null),
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }),
    ], 15)
    expect(rows).toHaveLength(1)
    expect(rows[0].input).toBe('140.8')
    expect(rows[0].official.input).toBe('8')
  })
})
