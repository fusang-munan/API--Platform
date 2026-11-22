import { describe, expect, it } from 'vitest'
import type { BeichenOverview } from '../api/types'
import { indexWebsiteKeyGroups, manualPriceDetailRows, missingPriceDetailLabels, priceDetailRows, schoolCatalog } from './beichenModels'

function overview(model: Record<string, unknown>): BeichenOverview {
  return {
    datasets: [],
    models: [model],
    model_catalog: [{
      model: 'claude-fable-5',
      school_model: 'claude-fable-5',
      school_status: 'active',
      can_enable: true,
      credential_ids: ['cred-1'],
      preferred_routes: [{
        school_model: 'claude-fable-5', upstream_model: 'claude-fable-5', adapter: 'anthropic',
        provider_id: 'provider-1', credential_id: 'cred-1', credential_label: 'G1',
        priority: 100, state: 'ready', effective_multiplier_text: '0.15',
      }],
    }],
  } as unknown as BeichenOverview
}

describe('schoolCatalog credits', () => {
  it('shows website prices and effective prices in credits', () => {
    const rows = schoolCatalog(overview({
      credential_id: 'cred-1', model: 'claude-fable-5', mode: 'follow', school_status: 'active',
      price_status: 'confirmed',
      price_conditions_exact: {
        billing_mode: 'token', input_price: 0.00001, output_price: 0.00005,
        cache_read_price: 0.000001, cache_write_price: 0.0000125,
      },
      upstream_rates: { input: 1500000, output: 7500000, cache_read: 150000, cache_write: 1875000 },
      school_rates: { input: 150000000, output: 750000000, cache_read: 15000000, cache_write: 187500000 },
    }), 'provider-1')
    expect(rows).toHaveLength(1)
    const quote = rows[0].quotes[0]
    expect(quote.standard).toContain('输入 1000')
    expect(quote.standard).toContain('输出 5000')
    expect(quote.actual).toContain('输入 150')
    expect(quote.actual).toContain('缓存写入 187.5')
    expect(quote.actualRates?.input).toBe('150')
    expect(quote.priceReady).toBe(true)
    expect(rows[0].routeCount).toBe(1)
    expect(rows[0].readyCount).toBe(1)
    expect(rows[0].credentials[0].label).toBe('G1')
  })

  it('marks manual prices ready even without a website quote', () => {
    const rows = schoolCatalog(overview({
      credential_id: 'cred-1', model: 'claude-fable-5', mode: 'manual', school_status: 'active',
      price_status: 'not_listed', price_conditions_exact: null,
      upstream_rates: null,
      school_rates: { input: 150000000, output: 750000000, cache_read: 0, cache_write: 0 },
    }), 'provider-1')
    const quote = rows[0].quotes[0]
    expect(quote.mode).toBe('manual')
    expect(quote.multiplierLabel).toBe('手动定价')
    expect(quote.priceReady).toBe(true)
    expect(quote.actualRates?.input).toBe('150')
    expect(rows[0].mode).toBe('manual')
    expect(rows[0].priceReady).toBe(true)
  })

  it('rejects enabling a conditional model without a flat price', () => {
    const rows = schoolCatalog(overview({
      credential_id: 'cred-1', model: 'claude-fable-5', mode: 'follow', school_status: 'active',
      price_status: 'conditional', price_conditions_exact: {
        billing_mode: 'token', intervals: [{ min_tokens: 0, max_tokens: 1000, input_price: 0.00001, output_price: 0.00005 }],
      },
      upstream_rates: null, school_rates: null,
    }), 'provider-1')
    expect(rows[0].quotes[0].priceReady).toBe(false)
    expect(rows[0].priceReady).toBe(false)
  })

  it.each(['current', 'stale'] as const)('keeps a %s billing group as history without a confirmed website Key', billing_state => {
    const data = overview({
      credential_id: 'cred-1', model: 'claude-fable-5', mode: 'follow', school_status: 'active',
      price_status: 'confirmed',
      price_conditions_exact: { billing_mode: 'token', input_price: 0.00001, output_price: 0.00005 },
      upstream_rates: { input: 1500000, output: 7500000 },
      school_rates: { input: 150000000, output: 750000000 },
    })
    data.datasets = [{
      credential_id: 'cred-1', kind: 'billing', group_id: 15, group_name: '旧组', group_rate: '0.15',
      group_status: 'assigned', billing_state, effective_multiplier: '0.15',
    }]
    const quote = schoolCatalog(data, 'provider-1')[0].quotes[0]
    expect(quote.notes).toContain('上次分组（未确认） 15 · 旧组 · 分组倍率 0.15×')
    expect(quote.multiplier).toBe('待同步')
    expect(quote.actual).toBeNull()
    expect(quote.actualRates).toBeNull()
  })

  it('does not apply earlier price terms to the newly confirmed group in the quote details', () => {
    const data = overview({
      credential_id: 'cred-1', model: 'claude-fable-5', mode: 'follow', school_status: 'active',
      price_status: 'confirmed', price_observed_at: '2026-11-21T12:00:00Z',
      price_conditions_exact: { billing_mode: 'token', input_price: '0.000004', output_price: '0.00002' },
    })
    data.datasets = [{
      credential_id: 'cred-1', kind: 'billing', group_id: 2, group_status: 'assigned',
      billing_state: 'current', group_rate: '0.15', effective_multiplier: '0.15',
    }]
    const lastSeen = '2026-11-21T13:00:00Z'
    const key = {
      id: 1, name: '测试 Key', key_prefix: null, group_id: 16, group_name: '新组', rate: '0.6',
      status: 'active', enabled: true, created_at: null, last_seen: lastSeen, observed_at: lastSeen,
      credential_id: 'cred-1', match_status: 'matched',
    } as const
    const groups = indexWebsiteKeyGroups({ status: 'current', last_seen: lastSeen, keys: [key] })

    const oldQuote = schoolCatalog(data, 'provider-1', groups)[0].quotes[0]
    expect(oldQuote).toMatchObject({ multiplier: '待同步', conditions: null, standard: null, actual: null })

    data.models![0].price_observed_at = lastSeen
    const currentQuote = schoolCatalog(data, 'provider-1', groups)[0].quotes[0]
    expect(currentQuote.multiplier).toBe('0.6×')
    expect(currentQuote.actual).toContain('输入 240')
  })
})

describe('price detail fields', () => {
  it('把每 Token 的元价换算成每百万 Token 的元与积分', () => {
    const rows = priceDetailRows({
      billing_mode: 'token', input_price: '0.000004', output_price: '0.00002',
      cache_write_price: '0.000005', cache_write_1h_price: '0.000008', cache_read_price: '0.0000002',
    })
    expect(rows.map(row => [row.label, row.yuan, row.credits])).toEqual([
      ['输入', '4', '400'], ['输出', '20', '2000'],
      ['缓存写入(5m)', '5', '500'], ['缓存写入(1h)', '8', '800'], ['缓存读取', '0.2', '20'],
    ])
  })

  it('缺价时逐项列出具体字段', () => {
    expect(missingPriceDetailLabels(null)).toEqual(['输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
    expect(missingPriceDetailLabels({ billing_mode: 'token', input_price: '0.000004' }))
      .toEqual(['输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
  })

  it('手动价的缓存写入(1h)沿用同一缓存写入价并标注', () => {
    const rows = manualPriceDetailRows({ input: '0.5', output: '2', cache_read: '0', cache_write: '1' })
    expect(rows.find(row => row.key === 'cache_write_1h')).toMatchObject({ credits: '1', yuan: '0.01', shared: true })
  })
})
