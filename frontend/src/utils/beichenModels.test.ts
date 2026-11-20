import { describe, expect, it } from 'vitest'
import type { BeichenOverview } from '../api/types'
import { schoolCatalog } from './beichenModels'

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
})
