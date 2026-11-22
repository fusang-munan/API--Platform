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

function location(group: BeichenWebsiteGroup, priced = true): BeichenWebsiteLocation {
  return {
    name: 'glm-5.3', platform: 'openai', groups: [group], observed_at: '2026-11-21T00:00:00Z',
    has_price: priced, price_complete: priced,
    ...(priced ? { price_conditions_exact: glmPrice } : {}),
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
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }, false),
      location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }),
    ], 15)
    expect(rows).toHaveLength(1)
    expect(rows[0].input).toBe('140.8')
  })
})
