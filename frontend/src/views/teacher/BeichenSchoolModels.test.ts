import { DOMWrapper, enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BeichenBatchEnableResult, BeichenCatalogModel, BeichenModelCapability, BeichenModelUsage, BeichenOverview, BeichenPriceConditions, BeichenRebindPlan, BeichenUsageRange, BeichenUsageTotals, BeichenWebsiteKey, BeichenWebsiteKeyInventory } from '../../api/types'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import componentSource from './BeichenSchoolModels.vue?raw'

const api = vi.hoisted(() => ({
  beichenOverview: vi.fn(), beichenWebsiteKeys: vi.fn(), refreshBeichenWebsiteKeys: vi.fn(),
  modelUsage: vi.fn(), enableBeichenModelsBatch: vi.fn(), enableBeichenModel: vi.fn(),
  updateModel: vi.fn(), syncBeichen: vi.fn(), setBeichenModelPrice: vi.fn(),
  requestBeichenProbes: vi.fn(), beichenProbes: vi.fn(),
  previewBeichenRebind: vi.fn(), applyBeichenRebind: vi.fn(),
  beichenCapabilities: vi.fn(),
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
vi.mock('../../composables/useToast', () => ({ useToast: () => toast }))
const Dialog = defineComponent({ props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><h2>{{ title }}</h2><slot /></section>' })
const stubs = { AppDialog: Dialog, BeichenRecentReceipts: true, RouterLink: true }
enableAutoUnmount(afterEach)
type Entry = { model: string; active?: boolean; priced?: boolean; canEnable?: boolean; manual?: boolean; catalog?: Partial<BeichenCatalogModel> }
function overview(entries: Entry[]): BeichenOverview {
  return {
    datasets: [], comparison: [],
    models: entries.map(entry => ({
      model: entry.model, credential_id: 'key-' + entry.model, mode: entry.manual ? 'manual' : 'follow',
      price_status: entry.priced === false ? 'not_listed' : 'confirmed',
      price_conditions_exact: entry.priced === false ? null : { billing_mode: 'token', input_price: '0.00001', output_price: '0.00002', cache_read_price: '0', cache_write_price: '0' },
      upstream_rates: entry.priced === false ? null : { input: 1000000, output: 2000000, cache_read: 0, cache_write: 0 },
      school_rates: entry.priced === false ? null : { input: 100000000, output: 200000000, cache_read: 0, cache_write: 0 },
    })),
    model_catalog: entries.map(entry => ({
      model: entry.model, school_status: entry.active ? 'active' : 'not_configured', can_enable: entry.canEnable ?? !entry.active,
      preferred_routes: [{ school_model: entry.model, provider_id: 'north', credential_id: 'key-' + entry.model,
        credential_label: '教学 Key', adapter: 'openai_responses', priority: 1, state: entry.active ? 'ready' : 'candidate', effective_multiplier_text: '0.1' }],
      ...entry.catalog,
    })),
  }
}
type WebsiteKeyGroup = Pick<BeichenWebsiteKey, 'credential_id' | 'group_id' | 'group_name' | 'rate'> & {
  observed_at?: string
  match_status?: BeichenWebsiteKey['match_status']
}
function websiteKeys(...groups: WebsiteKeyGroup[]): BeichenWebsiteKeyInventory {
  const lastSeen = '2026-11-21T13:08:00Z'
  return {
    status: 'current', last_seen: lastSeen,
    keys: groups.map((group, index) => ({
      id: index + 1, name: '官网测试 Key', key_prefix: null, status: 'active', enabled: true,
      created_at: null, last_seen: lastSeen, match_status: 'matched', ...group,
    })),
  }
}
function total(credits = '11', tokens = 1000): BeichenUsageTotals {
  return { credits, tokens, input_tokens: tokens - 400, output_tokens: 200, cache_read_tokens: 100, cache_write_tokens: 100, calls: 4, pending_calls: 1 }
}
function usage(range: BeichenUsageRange = 'month'): BeichenModelUsage {
  return {
    range, start: null, end: '2026-11-20T12:00:00+08:00', timezone: 'Asia/Shanghai',
    totals: { ...total('33.00000000000000000001', 2000), cache_hit_rate: 0.125, pending_calls: 3, completeness: 'partial', students: 2, keys: 2 },
    models: [{ model: 'lab/a', ...total() }, { model: 'lab/b', ...total('22', 1000) }],
    students: [
      { user_id: 'student-a', user_name: '学生甲', project_id: 'project', project_name: '教学项目', credits: '33', tokens: 2000, calls: 8,
        models: [{ model: 'lab/a', credits: '11', tokens: 1000, calls: 4 }, { model: 'lab/b', credits: '22', tokens: 1000, calls: 4 }] },
      { user_id: 'student-b', user_name: '学生乙', project_id: 'project', project_name: '教学项目', credits: '0', tokens: 0, calls: 1,
        models: [{ model: 'lab/b', credits: '0', tokens: 0, calls: 1 }] },
    ],
    keys: [{ api_key_id: 'school-key', api_key_name: '学校 Key 甲', user_id: 'student-a', user_name: '学生甲', project_name: '教学项目', credits: '33', tokens: 2000, calls: 8 }],
  }
}
function pricedLocation(id: number, name: string, rate: string, input: string, output: string,
  channel?: string, complete = true, priceScope?: 'platform' | 'group') {
  return {
    name: 'model', platform: 'test', observed_at: '2026-11-21T12:00:00Z',
    groups: [{ id, name, status: 'active', rate_multiplier: rate }],
    has_price: true, price_complete: complete,
    price_conditions_exact: { billing_mode: 'token', input_price: input, output_price: output },
    ...(channel ? { channel } : {}),
    ...(priceScope ? { price_scope: priceScope } : {}),
  }
}
function view(props: { query?: string; commonIds?: Set<string> } = {}) {
  return mount(BeichenSchoolModels, { props: { providerId: 'north', ...props }, global: { stubs } })
}
function button(root: Pick<DOMWrapper<Element>, 'findAll'>, label: string) {
  const target = root.findAll('button').find(node => node.text() === label)
  if (!target) throw new Error('没有找到按钮 ' + label)
  return target
}
function currentGroupAmounts(row: Pick<DOMWrapper<Element>, 'get'>, key: string) {
  return row.get('.current-group-prices table').findAll(`tbody [data-price="${key}"]`).map(cell => cell.text())
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
function capability(model: string, overrides: Partial<BeichenModelCapability> = {}): BeichenModelCapability {
  return {
    model, credential_id: 'key-' + model, credential_label: '渠道一',
    supported_protocols: ['openai_compatible'], available_protocols: [], recommended_protocol: null,
    protocol_details: [{ protocol: 'openai_compatible', binding_ready: true, probe_state: 'unprobed' }],
    catalog_state: 'key_listed', availability: 'unverified', price_state: 'confirmed', price_blocker: null,
    multiplier_state: 'current', probe_state: 'unprobed', binding_state: 'ready', action_hint: '',
    ...overrides,
  }
}
beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset(); toast.error.mockReset()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.stubGlobal('confirm', vi.fn(() => true))
  api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/a' }]))
  api.beichenWebsiteKeys.mockResolvedValue({ status: 'unsynced', last_seen: null, keys: [] })
  api.beichenCapabilities.mockResolvedValue({ capabilities: [], gaps: [] })
  api.modelUsage.mockImplementation(async (range: BeichenUsageRange) => usage(range))
  api.enableBeichenModelsBatch.mockResolvedValue({ enabled: [], skipped: [] })
  api.enableBeichenModel.mockResolvedValue({ model_id: 'school-id', status: 'active' })
  api.updateModel.mockResolvedValue({})
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('北辰模型管理', () => {
  it('每个模型只渲染一份同组官网价，优先官网并换算五项费用', async () => {
    const observedAt = '2026-11-21T13:07:28Z'
    const claude = 'claude-sonnet-4-6'
    const gpt = 'gpt-6.2-astra'
    const claudePrice = {
      billing_mode: 'token', input_price: '0.000003', output_price: '0.000015',
      cache_write_price: '0.00000375', cache_write_1h_price: null, cache_read_price: '0.0000003',
    }
    const gptPrice = {
      billing_mode: 'token', input_price: '0.000008', output_price: '0.00004',
      cache_write_price: '0.00001', cache_write_1h_price: '0.000012', cache_read_price: '0.0000004',
    }
    const data = overview([
      { model: claude, catalog: {
        price_state: 'derived', price_covered: true,
        website_locations: [
          { name: claude, observed_at: observedAt, groups: [{ id: 2, rate_multiplier: '0.15' }],
            has_price: true, price_complete: true, price_conditions_exact: { ...claudePrice, input_price: '0.000009' } },
          { name: claude, observed_at: observedAt, groups: [{ id: 16, rate_multiplier: '0.6' }],
            has_price: true, price_complete: false, price_conditions_exact: { input_price: '0.000008' } },
          { name: claude, observed_at: observedAt, groups: [{ id: 16, rate_multiplier: '0.6' }],
            has_price: true, price_complete: true, price_conditions_exact: claudePrice },
        ],
      } },
      { model: gpt, priced: false, canEnable: false, catalog: {
        source: 'both', catalog_state: 'key_listed', key_count: 1, price_state: 'missing', price_covered: false,
        catalog_keys: [{ credential_id: 'key-' + gpt, label: '教学 Key', group_id: 11, state: 'listed', upstream_model: gpt }],
        website_locations: [{ name: gpt, observed_at: observedAt,
          groups: [{ id: 11, rate_multiplier: '0.2', account_rate_multiplier: '0.16' }, { id: 8, rate_multiplier: '0.1' }],
          has_price: true, price_complete: true, price_conditions_exact: gptPrice }],
      } },
    ])
    data.datasets = [
      { kind: 'billing', credential_id: 'key-' + claude, group_id: 16, group_rate: '0.6', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: 0.4, effective_multiplier_text: '0.6', observed_at: observedAt },
      { kind: 'billing', credential_id: 'key-' + gpt, group_id: 11, group_status: 'assigned', group_rate: '0.2',
        billing_state: 'current', effective_multiplier: '0.16', observed_at: observedAt },
    ]
    Object.assign(data.models![0], {
      price_status: 'derived', price_source: 'derived_from_usage', price_observed_at: '2026-11-21T09:00:00Z',
      price_conditions_exact: { ...claudePrice, input_price: '0.000005', cache_write_1h_price: '0.00009' },
    })
    data.model_catalog![1].preferred_routes![0].effective_multiplier_text = null
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys(
      { credential_id: 'key-' + claude, group_id: 16, group_name: 'Claude 分组', rate: '0.6', observed_at: observedAt },
      { credential_id: 'key-' + gpt, group_id: 11, group_name: 'GPT 分组', rate: '0.2', observed_at: observedAt },
    ))
    const wrapper = view()
    await flushPromises()

    const claudeRow = wrapper.get('[data-model="' + claude + '"]')
    expect(claudeRow.findAll('.current-group-prices table')).toHaveLength(1)
    expect(claudeRow.findAll('[data-price-source="website"]')).toHaveLength(1)
    expect(claudeRow.find('[data-price-source="derived"]').exists()).toBe(false)
    expect(claudeRow.get('.current-group-prices time').attributes('datetime')).toBe(observedAt)
    expect(currentGroupAmounts(claudeRow, 'input')).toEqual(['3', '198'])
    expect(currentGroupAmounts(claudeRow, 'cache1h')).toEqual(['未列', '—'])

    const gptRow = wrapper.get('[data-model="' + gpt + '"]')
    expect(gptRow.findAll('.current-group-prices table')).toHaveLength(1)
    expect(currentGroupAmounts(gptRow, 'cache5m')).toEqual(['10', '176'])
    expect(currentGroupAmounts(gptRow, 'cache1h')).toEqual(['12', '211.2'])
    expect(currentGroupAmounts(gptRow, 'cache_read')).toEqual(['0.4', '7.04'])
    expect(gptRow.find('[data-testid="price-missing"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('学生扣费 = 结算价 × 1.10；100 积分 = 1 元')
    expect(wrapper.text()).not.toContain('当前 Key 报价待核对')

    expect(gptRow.findAll('.current-group-prices table')).toHaveLength(1)
    expect(gptRow.find('.other-groups').exists()).toBe(false)
    expect(gptRow.findAll('.group-price-summary')).toHaveLength(0)
    expect(gptRow.text()).not.toContain('0.15×')
  })

  it('主表只显示四把 Key 探测到的 10 个模型，官网-only 和异常默认不出现', async () => {
    const families = [
      { model: 'gpt-6.2-astra', key: 'key-gpt', group: 11, groupName: 'GPT 分组', rate: '0.2',
        input: '0.000008', output: '0.00004' },
      { model: 'glm-5.3', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.00000016', output: '0.00000064' },
      { model: 'deepseek-v4.1', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000002', output: '0.0000008' },
      { model: 'kimi-k2.5', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000003', output: '0.0000012' },
      { model: 'qwen3.5-max', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000004', output: '0.0000016' },
      { model: 'gemini-3.7-pro', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000005', output: '0.000002' },
      { model: 'grok-4.5', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000006', output: '0.0000024' },
      { model: 'doubao-2.5-pro', key: 'key-cn', group: 20, groupName: '国模分组', rate: '1',
        input: '0.0000007', output: '0.0000028' },
    ]
    const claudeLocations = [
      pricedLocation(2, '[CC1]Claudecode-kiro-015', '0.15', '0.000004', '0.00002', 'CC1', true, 'platform'),
      pricedLocation(16, '[CC2]ClaudeCode高智', '0.6', '0.000009', '0.00002', undefined, false),
    ]
    const entries: Entry[] = [
      { model: 'claude-opus-5-5', catalog: {
        source: 'both', catalog_state: 'key_listed', credential_ids: ['key-cc2'], key_count: 1,
        route_count: 1, ready_count: 1, activation: 'ready', protocols: ['anthropic'],
        preferred_routes: [{ school_model: 'claude-opus-5-5', provider_id: 'north', credential_id: 'key-cc2',
          adapter: 'anthropic', priority: 1, state: 'ready', effective_multiplier_text: '0.6' }],
        catalog_keys: [{ credential_id: 'key-cc2', label: 'CC2', state: 'listed', group_id: 16, upstream_model: 'claude-opus-5-5' }],
        website_locations: claudeLocations,
      } },
      { model: 'claude-sonnet-5-5', catalog: {
        source: 'both', catalog_state: 'key_listed', credential_ids: ['key-cc2'], key_count: 1,
        route_count: 1, ready_count: 1, activation: 'ready', protocols: ['anthropic'],
        preferred_routes: [{ school_model: 'claude-sonnet-5-5', provider_id: 'north', credential_id: 'key-cc2',
          adapter: 'anthropic', priority: 1, state: 'ready', effective_multiplier_text: '0.6' }],
        catalog_keys: [{ credential_id: 'key-cc2', label: 'CC2', state: 'listed', group_id: 16, upstream_model: 'claude-sonnet-5-5' }],
        website_locations: [
          pricedLocation(2, '[CC1]Claudecode-kiro-015', '0.15', '0.000002', '0.00001', 'CC1', true, 'platform'),
          pricedLocation(16, '[CC2]ClaudeCode高智', '0.6', '0.000003', '0.000015', undefined, false),
        ],
      } },
      ...families.map((family): Entry => ({ model: family.model, catalog: {
        source: 'both', catalog_state: 'key_listed', credential_ids: [family.key], key_count: 1,
        route_count: 1, ready_count: 1, activation: 'ready', protocols: ['openai_compatible'],
        preferred_routes: [{ school_model: family.model, provider_id: 'north', credential_id: family.key,
          adapter: 'openai_compatible', priority: 1, state: 'ready', effective_multiplier_text: family.rate }],
        catalog_keys: [{ credential_id: family.key, label: 'Key', state: 'listed', group_id: family.group, upstream_model: family.model }],
        website_locations: [pricedLocation(family.group, family.groupName, family.rate, family.input, family.output)],
      } })),
    ]
    const data = overview(entries)
    data.credentials = [
      { credential_id: 'key-cc1', label: 'CC1', status: 'active' },
      { credential_id: 'key-cc2', label: 'CC2', status: 'active' },
      { credential_id: 'key-gpt', label: 'GPT', status: 'active' },
      { credential_id: 'key-cn', label: '国模', status: 'active' },
    ]
    const keyFor = new Map(entries.map(entry => [entry.model, entry.catalog?.preferred_routes?.[0]?.credential_id ?? '']))
    data.models = data.models!.map(row => ({ ...row, credential_id: keyFor.get(row.model) ?? row.credential_id }))
    data.model_catalog!.push({
      model: 'claude-fable-5', source: 'website', catalog_state: 'website_only', key_count: 0,
      route_count: 0, protocols: [], can_enable: false,
      website_locations: [pricedLocation(2, '[CC1]Claudecode-kiro-015', '0.15', '0.000004', '0.00002', 'CC1', true, 'platform')],
    })
    data.model_catalog!.push({
      model: 'claude-opus-5-legacy', school_status: 'active', source: 'key', catalog_state: 'catalog_missing',
      credential_ids: [], key_count: 0, route_count: 0, ready_count: 0, protocols: [], can_enable: false,
      binding_issue: { code: 'catalog_missing', message: '已发布，但四把 Key 当前目录都没有它。' },
    })
    data.datasets = [
      { kind: 'billing', credential_id: 'key-cc1', group_id: 2, group_rate: '0.15', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '0.15', observed_at: '2026-11-21T12:05:00Z' },
      { kind: 'billing', credential_id: 'key-cc2', group_id: 16, group_rate: '0.6', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '0.6', observed_at: '2026-11-21T12:05:00Z' },
      { kind: 'billing', credential_id: 'key-gpt', group_id: 11, group_rate: '0.2', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '0.2', observed_at: '2026-11-21T12:05:00Z' },
      { kind: 'billing', credential_id: 'key-cn', group_id: 20, group_rate: '1', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '1', observed_at: '2026-11-21T12:05:00Z' },
    ]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys(
      { credential_id: 'key-cc1', group_id: 2, group_name: '[CC1]Claudecode-kiro-015', rate: '0.15', observed_at: '2026-11-21T12:00:00Z' },
      { credential_id: 'key-cc2', group_id: 16, group_name: '[CC2]ClaudeCode高智', rate: '0.6', observed_at: '2026-11-21T12:00:00Z' },
      { credential_id: 'key-gpt', group_id: 11, group_name: 'GPT 分组', rate: '0.2', observed_at: '2026-11-21T12:00:00Z' },
      { credential_id: 'key-cn', group_id: 20, group_name: '国模分组', rate: '1', observed_at: '2026-11-21T12:00:00Z' },
    ))
    const wrapper = view()
    await flushPromises()

    const rows = wrapper.findAll('.model-table > tbody > tr')
    expect(rows).toHaveLength(10)
    expect(rows.map(row => row.attributes('data-model'))).not.toContain('claude-fable-5')
    expect(wrapper.text()).not.toContain('claude-fable-5')
    expect(wrapper.get('.model-summary').text()).toContain('可探测模型10')
    expect(wrapper.get('.model-summary').text()).toContain('官网目录1')
    expect(wrapper.text()).not.toContain('[CC1]Claudecode-kiro-015 · 0.15×')
    for (const price of wrapper.findAll('.current-group-prices')) expect(price.text()).not.toContain('0.15×')
    expect(wrapper.find('.other-groups').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="model-price-table"]')).toHaveLength(10)
    for (const row of rows) {
      expect(row.findAll('[data-testid="model-price-table"]')).toHaveLength(1)
      expect(row.findAll('[data-testid="model-price-table"] thead th')).toHaveLength(6)
      expect(row.findAll('[data-testid="model-price-table"] tbody [data-price-row]')).toHaveLength(2)
    }
    const opus = wrapper.get('[data-model="claude-opus-5-5"]')
    expect(currentGroupAmounts(opus, 'input')).toEqual(['4', '264'])
    expect(currentGroupAmounts(opus, 'output')).toEqual(['20', '1320'])
    expect(opus.get('[data-testid="price-basis"]').text()).toBe('基础价来自 CC1 同名模型；当前 Key 分组 16 的倍率为 0.60×')
    const sonnet = wrapper.get('[data-model="claude-sonnet-5-5"]')
    expect(currentGroupAmounts(sonnet, 'input')).toEqual(['2', '132'])
    expect(currentGroupAmounts(sonnet, 'output')).toEqual(['10', '660'])
    const gpt = wrapper.get('[data-model="gpt-6.2-astra"]')
    expect(currentGroupAmounts(gpt, 'input')).toEqual(['8', '176'])
    expect(currentGroupAmounts(gpt, 'output')).toEqual(['40', '880'])
    const domestic = wrapper.get('[data-model="glm-5.3"]')
    expect(currentGroupAmounts(domestic, 'input')).toEqual(['0.16', '17.6'])
    expect(currentGroupAmounts(domestic, 'output')).toEqual(['0.64', '70.4'])

    expect(wrapper.find('[data-model="claude-opus-5-legacy"]').exists()).toBe(false)
    await button(wrapper, '异常').trigger('click')
    expect(wrapper.findAll('.model-table > tbody > tr').map(row => row.attributes('data-model')))
      .toEqual(['claude-opus-5-legacy'])
    expect(button(wrapper, '一键启用').element.disabled).toBe(true)
    expect(wrapper.find('[data-model="claude-fable-5"]').exists()).toBe(false)
    await button(wrapper, '全部').trigger('click')
    expect(wrapper.findAll('.model-table > tbody > tr')).toHaveLength(10)
  })

  it('表头显示生效倍率：分组 0.24× 但账号 0.20× 时学生价按 0.20× 计算', async () => {
    const model = 'gpt-6.2-astra'
    const data = overview([{ model, catalog: {
      source: 'both', catalog_state: 'key_listed', credential_ids: ['key-gpt'], key_count: 1,
      website_locations: [pricedLocation(10, 'GPT 分组', '0.24', '0.000008', '0.00004')],
      preferred_routes: [{ school_model: model, provider_id: 'north', credential_id: 'key-gpt',
        adapter: 'openai_compatible', priority: 1, state: 'ready', effective_multiplier_text: '0.24' }],
      catalog_keys: [{ credential_id: 'key-gpt', label: 'GPT', state: 'listed', group_id: 10, upstream_model: model }],
    } }])
    data.datasets = [{ credential_id: 'key-gpt', kind: 'billing', group_id: 10, group_rate: '0.24',
      group_status: 'assigned', billing_state: 'current', effective_multiplier: 0.2, effective_multiplier_text: '0.2',
      observed_at: '2026-11-21T13:00:00Z' }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({
      credential_id: 'key-gpt', group_id: 10, group_name: 'GPT 分组', rate: '0.24', observed_at: '2026-11-21T12:00:00Z',
    }))
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="' + model + '"]')
    expect(row.get('.group-prices-head strong').text()).toBe('当前 Key 分组 10 · 生效 0.20×')
    expect(row.get('[data-testid="rate-note"]').text()).toBe('分组 0.24× / 账号 0.20×')
    expect(currentGroupAmounts(row, 'input')).toEqual(['8', '176'])
    expect(currentGroupAmounts(row, 'output')).toEqual(['40', '880'])
  })

  it('在模型报价中区分分组权限、分组不存在、官方缺价和历史反推', async () => {
    const gemini = 'gemini-3.7-flash'
    const claude = 'claude-sonnet-5-5'
    const models = [gemini, 'lab/no-group', 'lab/no-price', claude]
    const data = overview(models.map(model => ({ model, priced: false })))
    data.credentials = models.map(model => ({ credential_id: 'key-' + model, label: model, status: 'active' }))
    for (const row of data.models!) row.price_status = 'pending_confirmation'
    data.models![3] = {
      ...data.models![3], price_status: 'derived', price_source: 'derived_from_usage',
      price_conditions_exact: { billing_mode: 'token', input_price: '0.000002', output_price: '0.00001' },
      price_observed_at: '2026-11-21T12:14:00Z', price_covered: true,
    }
    data.datasets = [
      { credential_id: 'key-' + gemini, kind: 'billing', effective_multiplier: '0.3', billing_state: 'stale',
        group_status: 'not_allowed', group_id: 5, group_name: '[Gemini]国外豆包', group_rate: '0.3', last_error: '账号无 group 5 权限' },
      { credential_id: 'key-' + gemini, kind: 'prices', effective_multiplier: null, last_error: '账号无 group 5 权限' },
      { credential_id: 'key-lab/no-group', kind: 'billing', effective_multiplier: null, group_status: 'not_found' },
      { credential_id: 'key-lab/no-price', kind: 'billing', effective_multiplier: '0.5', billing_state: 'current',
        group_status: 'assigned', group_id: 17, group_rate: '0.5', observed_at: '2026-11-21T12:14:00Z' },
      { credential_id: 'key-lab/no-price', kind: 'prices', effective_multiplier: null, official_price_missing: true,
        last_error: '北辰渠道页没有当前 Key 的有效模型报价' },
      { credential_id: 'key-' + claude, kind: 'billing', effective_multiplier: '0.6', billing_state: 'current',
        group_status: 'assigned', group_id: 16, group_rate: '0.6', observed_at: '2026-11-21T12:14:00Z' },
    ]
    data.model_catalog![0].protocols = []
    data.model_catalog![0].website_locations = [{
      name: gemini, observed_at: '2026-11-21T12:14:00Z', has_price: true, price_complete: true,
      groups: [{ id: 5, rate_multiplier: '0.3' }],
      price_conditions_exact: { input_price: '0.000001', output_price: '0.000002' },
    }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys(
      { credential_id: 'key-' + gemini, group_id: 5, group_name: '[Gemini]国外豆包', rate: '0.3' },
      { credential_id: 'key-lab/no-group', group_id: null, group_name: null, rate: null },
      { credential_id: 'key-lab/no-price', group_id: 17, group_name: '分组 17', rate: '0.5', observed_at: '2026-11-21T12:10:00Z' },
      { credential_id: 'key-' + claude, group_id: 16, group_name: '分组 16', rate: '0.6', observed_at: '2026-11-21T12:10:00Z' },
    ))
    const wrapper = view()
    await flushPromises()
    const geminiRow = wrapper.get(`[data-model="${gemini}"]`)
    expect(geminiRow.get('[data-testid="group-denied"]').text()).toBe('分组无权限 · 分组 5')
    expect(geminiRow.get('[data-testid="protocol-missing"]').text()).toBe('缺协议')
    expect(currentGroupAmounts(geminiRow, 'input')).toEqual(['1', '—'])
    expect(wrapper.text()).not.toContain('价格同步失败')
    const noGroup = wrapper.get('[data-model="lab/no-group"]')
    expect(noGroup.get('[data-testid="group-mismatch"]').attributes('title')).toBe('Key 分组不存在')
    expect(noGroup.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
    const noPrice = wrapper.get('[data-model="lab/no-price"]')
    expect(noPrice.get('[data-testid="price-missing"]').text()).toBe('缺价 · 官方价表缺失')
    expect(noPrice.get('.current-group-prices .group-prices-head strong').text()).toBe('当前 Key 分组 17 · 生效 0.50×')
    expect(noPrice.find('[data-testid="current-group-missing"]').exists()).toBe(false)
    expect(currentGroupAmounts(noPrice, 'input')).toEqual(['未列', '—'])
    const claudeRow = wrapper.get(`[data-model="${claude}"]`)
    expect(claudeRow.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
    expect(claudeRow.get('[data-testid="derived-reference"]').text()).toContain('历史流水参考')
    expect(claudeRow.get('.current-group-prices .group-prices-head strong').text()).toBe('当前 Key 分组 16 · 生效 0.60×')
  })

  it('官网模型计入主表，换 Claude Key 缺目录时显示分组与原因，重扫后恢复', async () => {
    const model = 'claude-fable-5'
    const location = {
      name: 'Claude Fable 5', platform: 'anthropic', channel: 'Claude 渠道',
      groups: [{ id: 24, name: 'CC1 分组' }], observed_at: '2026-11-21T00:00:00Z', has_price: true, stale: true,
    }
    const candidate = { credential_id: 'second', label: 'Claude 接替', school_channel: 'CC1', replacement: true, group_id: 24 }
    const keyOnly = overview([{ model: 'lab/a', catalog: { source: 'key', catalog_state: 'key_listed', key_count: 1 } }])
    const data: BeichenOverview = {
      ...keyOnly,
      credentials: [
        { credential_id: 'first', label: '渠道一', status: 'active' },
        { credential_id: 'second', label: 'Claude 接替', status: 'active' },
      ],
      website_directory: {
        state: 'stale', last_success: location.observed_at, last_error: '官网请求失败',
        last_run: {
          started_at: '2026-11-21T01:30:00Z', duration_ms: 87, platform_count: 8, group_count: 4,
          model_count: 17, expected_model_count: 20,
          failures: [{ channel: 'Claude 渠道', platform: 'anthropic', path: 'data[0].platforms[0]', reason: 'models 不是模型数组' }],
        },
      },
      website_only_status: 'stale',
      model_catalog: [...keyOnly.model_catalog!, { model, source: 'website', catalog_state: 'website_only', key_count: 0,
        route_count: 0, protocols: [], can_enable: false, website_locations: [location],
        catalog_keys: [{ ...candidate, state: 'missing' }] }],
    }
    const restored = overview([{ model: 'lab/a' }, {
      model, catalog: { source: 'both', catalog_state: 'key_listed', key_count: 1, protocols: ['anthropic'],
        website_locations: [location], catalog_keys: [{ ...candidate, state: 'listed', upstream_model: model, match: 'normalized' }] },
    }])
    api.beichenOverview.mockResolvedValueOnce(data).mockResolvedValue({
      ...restored, website_only_status: 'stale', credentials: data.credentials,
    })
    api.beichenCapabilities.mockResolvedValue({
      capabilities: [capability(model, { supported_protocols: [], availability: 'maintenance', binding_state: 'invalid' })],
    })
    api.syncBeichen.mockResolvedValue({ synced: true })
    const wrapper = view()
    await flushPromises()
    const web = wrapper.get('[aria-label="官网目录"]')
    expect(web.text()).toContain('保留上次官网目录（待更新）')
    expect(web.text()).toContain('读到 17 个模型、8 个平台、4 个分组，耗时 87 毫秒')
    expect(web.text()).toContain('响应中能辨认出 20 个不同型号')
    expect(web.get('[aria-label="抓取失败项"]').text()).toContain('Claude 渠道 / anthropic · models 不是模型数组')
    expect(wrapper.find('[data-model="' + model + '"]').exists()).toBe(false)
    expect(wrapper.get('.model-summary').text()).toContain('可探测模型1')
    expect(wrapper.get('.model-summary').text()).toContain('官网目录1')
    expect(wrapper.get('.beichen-stat strong').text()).toBe('1')
    expect(wrapper.find('[aria-label="启用 ' + model + '"]').exists()).toBe(false)
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('lab/a')
    expect(dialog.text()).not.toContain(model)
    await button(dialog, '取消').trigger('click')
    await web.get('select').setValue('second')
    await web.get('form').trigger('submit')
    await flushPromises()
    expect(api.syncBeichen).toHaveBeenCalledExactlyOnceWith('north', 'second')
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
    const claude = wrapper.get('[data-model="' + model + '"]')
    expect(claude.get('[data-testid="catalog-source"]').text()).toBe('来源 官网和 Key')
    expect(claude.text()).toContain('Claude 渠道 · anthropic · CC1 分组 分组 24')
    expect(claude.get('.model-state-summary').text()).toContain('Key 覆盖 1/2 · 协议 1')
    expect(claude.find('[data-testid="key-catalog-gap"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="启用 ' + model + '"]').exists()).toBe(true)
  })

  it('重扫立即显示新分组的倍率，并显示报价差异及陈旧告警', async () => {
    const model = 'claude-sonnet-5'
    const opus = 'claude-opus-5'
    const key = 'key-' + model
    const checked = '2026-11-21T00:35:00Z'
    const current = overview([{ model, active: true }])
    current.credentials = [{ credential_id: key, label: 'CC1', status: 'active' }]
    current.website_only_status = 'compared'
    current.model_catalog![0].website_locations = [15, 60].map(id => ({
      name: model, observed_at: checked, groups: [{ id }], has_price: true, price_complete: true,
      price_conditions_exact: { input_price: id === 15 ? '0.000001' : '0.000002', output_price: '0.000004' },
    }))
    const c = { credential_id: key, label: 'CC1', school_channel: 'CC1', group_id: 60 } as const
    current.model_catalog!.push({
      model: opus, source: 'both', catalog_state: 'key_listed', credential_ids: [key], key_count: 1,
      catalog_keys: [{ ...c, state: 'missing', group_diagnosis: 'model_outside_key_group' }],
      website_locations: [{
        name: opus, platform: 'anthropic', groups: [{ id: 15, rate_multiplier: 0.15 }], has_price: true,
        price_complete: true, observed_at: checked,
        price_conditions_exact: { billing_mode: 'token', input_price: '0.000004', output_price: '0.000008' },
      }],
    })
    const before = { ...current, model_catalog: current.model_catalog!.slice(0, 1), datasets: [{
      credential_id: key, kind: 'billing', group_id: 15, group_rate: '0.15', effective_multiplier: '0.15',
      billing_state: 'current' as const, observed_at: checked,
    }] }
    const newBilling = { ...before.datasets[0], group_id: 60, group_rate: '0.6', effective_multiplier: '0.6', last_success: checked }
    const switched = { ...current, datasets: [newBilling] }
    const stale = { ...switched, datasets: [{ ...newBilling, billing_state: 'stale' as const,
      failure_count: 3, alert: true, last_error: '北辰接口返回 HTTP 503' }] }
    api.beichenOverview.mockResolvedValueOnce(before).mockResolvedValueOnce(switched).mockResolvedValue(stale)
    api.beichenWebsiteKeys
      .mockResolvedValueOnce(websiteKeys({ credential_id: key, group_id: 15, group_name: '原分组', rate: '0.15', observed_at: checked }))
      .mockResolvedValueOnce(websiteKeys({ credential_id: key, group_id: 60, group_name: '新分组', rate: '0.6', observed_at: checked }))
      .mockRejectedValue(new Error('Key 清单暂时读取失败'))
    api.syncBeichen.mockResolvedValueOnce({ synced: true }).mockRejectedValueOnce(new Error('同步未完成'))
    const wrapper = view()
    await flushPromises()
    const tariff = wrapper.get('[aria-label="北辰 Key 计费倍率"]')
    expect(tariff.text()).toContain('分组 15 · 原分组 · 分组倍率 0.15×')
    const modelRow = wrapper.get('[data-model="' + model + '"]')
    expect(currentGroupAmounts(modelRow, 'input')).toEqual(['1', '16.5'])
    expect(modelRow.get('.current-group-prices .group-prices-head strong').text()).toContain('分组 15')
    const directory = wrapper.get('[aria-label="官网目录"]')
    await directory.get('select').setValue(key)
    await directory.get('form').trigger('submit')
    await flushPromises()
    expect(tariff.text()).toContain('分组 60 · 新分组 · 分组倍率 0.60×')
    expect(currentGroupAmounts(modelRow, 'input')).toEqual(['2', '132'])
    expect(modelRow.get('.current-group-prices .group-prices-head strong').text()).toContain('分组 60')
    expect(modelRow.find('.other-groups').exists()).toBe(false)
    const mismatch = wrapper.get('[data-model="' + opus + '"]')
    expect(mismatch.get('[data-testid="group-mismatch"]').text()).toBe('分组不匹配')
    expect(mismatch.find('[data-testid="current-group-missing"]').exists()).toBe(false)
    expect(mismatch.find('.other-groups').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('重扫该 Key')
    await directory.get('form').trigger('submit')
    await flushPromises()
    expect(api.syncBeichen).toHaveBeenNthCalledWith(2, 'north', key)
    const alert = tariff.get('[role="alert"]').text()
    expect(alert).toContain('上次分组（未确认） 60 · 新分组 · 分组倍率 0.60×')
    expect(alert).toContain('倍率已陈旧')
    expect(alert).toContain('计费信息连续读取失败 3 次')
    expect(alert).toContain('上次成功 11/21 08:35')
    expect(wrapper.text()).toContain('官网 Key 清单待刷新')
    expect(modelRow.find('.current-group-prices').exists()).toBe(false)
    expect(modelRow.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
    expect(modelRow.find('.other-groups').exists()).toBe(false)
    expect(api.beichenWebsiteKeys.mock.calls).toEqual([['north'], ['north'], ['north']])
  })

  it('陈旧的 0.15 只在其它分组，刷新官网 Key 后使用分组 16 基础价但倍率待核实', async () => {
    const model = 'claude-sonnet-5'
    const credential_id = 'key-' + model
    const observed_at = '2026-11-21T13:08:00Z'
    const data = overview([{ model, catalog: { website_locations: [2, 16].map(id => ({
      name: model, observed_at, groups: [{ id, rate_multiplier: id === 2 ? '0.15' : '0.6' }],
      has_price: true, price_complete: true,
      price_conditions_exact: { input_price: id === 2 ? '0.000003' : '0.000004', output_price: '0.00002' },
    })) } }])
    data.credentials = [{ credential_id, label: '教学 Key', status: 'active' }]
    data.datasets = [{
      credential_id, kind: 'billing', group_id: 2, group_rate: '0.15', group_status: 'assigned',
      billing_state: 'stale', effective_multiplier: '0.15',
    }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue({ ...websiteKeys({
      credential_id, group_id: 2, group_name: '旧组', rate: '0.15',
    }), status: 'stale' })
    const refresh = deferred<BeichenWebsiteKeyInventory>()
    api.refreshBeichenWebsiteKeys.mockReturnValueOnce(refresh.promise)

    const wrapper = view()
    await flushPromises()
    const inventory = wrapper.get('[aria-label="官网 Key 清单"]')
    const modelRow = wrapper.get(`[data-model="${model}"]`)
    expect(inventory.text()).toContain('官网 Key 清单待刷新')
    expect(inventory.text()).toContain('重新核对学校 Key 的分组和倍率')
    expect(modelRow.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
    expect(modelRow.find('.current-group-prices').exists()).toBe(false)
    expect(modelRow.find('.other-groups').exists()).toBe(false)

    await button(inventory, '刷新官网 Key').trigger('click')
    expect(api.refreshBeichenWebsiteKeys).toHaveBeenCalledExactlyOnceWith('north')
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(button(inventory, '刷新中…').attributes('disabled')).toBeDefined()

    refresh.resolve(websiteKeys({ credential_id, group_id: 16, group_name: '新组', rate: '0.6', observed_at }))
    await flushPromises()
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
    expect(api.beichenWebsiteKeys).toHaveBeenCalledTimes(1)
    expect(api.syncBeichen).not.toHaveBeenCalled()
    expect(api.requestBeichenProbes).not.toHaveBeenCalled()
    expect(inventory.text()).not.toContain('官网 Key 清单待刷新')
    const current = modelRow.get('.current-group-prices')
    expect(current.text()).toContain('当前 Key 分组 16 · 倍率待核实')
    expect(current.text()).not.toContain('0.15×')
    expect(currentGroupAmounts(modelRow, 'input')).toEqual(['4', '—'])
    expect(modelRow.find('.other-groups').exists()).toBe(false)
  })

  it('官网 Key 刷新返回会话过期时，提示重新授权并把原分组保留为历史', async () => {
    const credential_id = 'key-lab/a'
    const before = websiteKeys({ credential_id, group_id: 2, group_name: '旧组', rate: '0.15' })
    const data = overview([{ model: 'lab/a', catalog: { website_locations: [{
      name: 'lab/a', observed_at: '2026-11-21T00:00:00Z', groups: [{ id: 2, name: '旧组', rate_multiplier: '0.15' }],
      has_price: true, price_complete: true,
      price_conditions_exact: { input_price: '0.000004', output_price: '0.00002' },
    }] } }])
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(before)
    api.refreshBeichenWebsiteKeys.mockResolvedValueOnce({ ...before, status: 'session_expired' })
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.get('.current-group-prices').text()).toContain('当前 Key 分组 2 · 倍率待核实')
    const inventory = wrapper.get('[aria-label="官网 Key 清单"]')
    await button(inventory, '刷新官网 Key').trigger('click')
    await flushPromises()

    expect(inventory.text()).toContain('官网 Key 清单待刷新')
    expect(inventory.text()).toContain('请在北辰网页登录重新授权')
    expect(button(inventory, '刷新官网 Key').attributes('disabled')).toBeUndefined()
    expect(row.find('.current-group-prices').exists()).toBe(false)
    expect(row.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
  })

  it.each(['missing', 'unmatched', 'ambiguous'] as const)(
    '官网 Key 清单 %s 时，已分配且有效的计费记录也只作上次分组', async matchStatus => {
      const data = overview([{ model: 'lab/a' }])
      const credential_id = 'key-lab/a'
      data.datasets = [{ credential_id, kind: 'billing', group_id: 2, group_status: 'assigned',
        group_rate: '0.15', billing_state: 'current', effective_multiplier: '0.15' }]
      const inventory = matchStatus === 'missing' ? websiteKeys()
        : websiteKeys({ credential_id, group_id: 16, group_name: '未匹配组', rate: '0.6', match_status: matchStatus })
      api.beichenWebsiteKeys.mockResolvedValue(inventory)
      api.beichenOverview.mockResolvedValue(data)
      const wrapper = view()
      await flushPromises()

      expect(wrapper.get('[aria-label="官网 Key 清单"]').text()).toContain('官网 Key 清单待刷新')
      const row = wrapper.get('[data-model="lab/a"]')
      expect(row.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
      expect(row.find('.current-group-prices').exists()).toBe(false)
      expect(row.find('.other-groups').exists()).toBe(false)
      expect(row.text()).not.toContain('0.15×')
    },
  )

  it('按该模型的线路筛选匹配的 Key，再按 ready 和优先级取同一 Key 的分组和报价', async () => {
    const model = 'lab/a'
    const data = overview([{ model, active: true }])
    const entry = data.model_catalog![0]
    const [unmatched] = entry.preferred_routes!
    const route = { ...unmatched, credential_id: 'confirmed', upstream_model: 'lab/a-verified' }
    entry.preferred_routes = [
      unmatched,
      { ...route, credential_id: 'candidate', state: 'candidate', priority: 0 },
      { ...route, credential_id: 'lower-priority', priority: 20 },
      { ...route, priority: 10 },
      { ...route, credential_id: 'other-model', school_model: 'lab/b', priority: 0 },
    ]
    const observed_at = '2026-11-21T13:08:00Z'
    const [record] = data.models!
    data.models = [{ ...record, price_observed_at: observed_at }, {
      ...record, credential_id: 'confirmed', model: 'lab/a-verified', price_status: 'derived',
      price_source: 'derived_from_usage', price_observed_at: observed_at,
      price_conditions_exact: { input_price: '0.000004', output_price: '0.00002' },
    }]
    entry.website_locations = [{
      name: 'lab/a-verified', channel: 'CC1', observed_at,
      groups: [{ id: 2, name: '[CC1]Claudecode-kiro-015', rate_multiplier: '0.15' }],
      has_price: true, price_complete: true, price_scope: 'platform',
      price_conditions_exact: { input_price: '0.000004', output_price: '0.00002' },
    }]
    data.datasets = [{ kind: 'billing', credential_id: 'confirmed', group_id: 16, group_rate: '0.6',
      group_status: 'assigned', billing_state: 'current', effective_multiplier: '0.6', observed_at }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys(
      { credential_id: 'confirmed', group_id: 16, group_name: '已匹配分组', rate: '0.6', observed_at },
      { credential_id: 'candidate', group_id: 7, group_name: null, rate: '0.7', observed_at },
      { credential_id: 'lower-priority', group_id: 17, group_name: null, rate: '0.5', observed_at },
      { credential_id: 'other-model', group_id: 4, group_name: null, rate: '0.8', observed_at },
    ))
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.get('.current-group-prices .group-prices-head strong').text()).toBe('当前 Key 分组 16 · 生效 0.60×')
    expect(row.get('[data-testid="price-basis"]').text()).toContain('基础价来自 CC1 同名模型')
    expect(currentGroupAmounts(row, 'input')).toEqual(['4', '264'])
    expect(row.find('[data-testid="current-group-missing"]').exists()).toBe(false)
  })

  it('展示重建的旧线路和新线路，预览变化后需要再次确认', async () => {
    const plan: BeichenRebindPlan = { preview: true, preview_token: 'first-preview', changes: [{
      model_id: 'model-id', school_model: 'school/a', adapter: 'openai_responses', action: 'update',
      before: { credential_id: 'first', credential_label: '渠道一', upstream_model: 'old', priority: 10, status: 'active' },
      after: { credential_id: 'second', credential_label: '渠道二', upstream_model: 'new', priority: 10, status: 'active' },
    }] }
    api.previewBeichenRebind.mockResolvedValueOnce(plan).mockResolvedValueOnce({ ...plan, preview_token: 'second-preview' })
    api.applyBeichenRebind.mockRejectedValueOnce(new Error('预览已过期')).mockResolvedValueOnce({ ...plan, preview: false })
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '重建绑定').trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"][aria-label="预览北辰绑定重建"]')
    expect(dialog.text()).toContain('将影响 1 个校内模型、1 条北辰线路')
    expect(dialog.text()).toContain('渠道一 · old · 优先级 10')
    expect(dialog.text()).toContain('渠道二 · new · 优先级 10')
    expect(api.applyBeichenRebind).not.toHaveBeenCalled()
    await button(dialog, '确认重建').trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('应用绑定失败，请重新预览')
    expect(dialog.findAll('button').some(node => node.text() === '确认重建')).toBe(false)
    await button(dialog, '重新预览').trigger('click')
    await flushPromises()
    await button(dialog, '确认重建').trigger('click')
    await flushPromises()
    expect(api.applyBeichenRebind.mock.calls).toEqual([['north', 'first-preview'], ['north', 'second-preview']])
    expect(wrapper.find('[aria-label="预览北辰绑定重建"]').exists()).toBe(false)
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
  })

  it('展示探活日预算，达到上限后仍可重新探活', async () => {
    const budget = { date: '2026-11-21', used: 500, limit: 500, remaining: 0, exhausted: true }
    api.beichenOverview.mockResolvedValue({
      ...overview([{ model: 'lab/a' }]),
      probe_run: { state: 'completed', started_at: '2026-11-21T00:00:00Z', finished_at: '2026-11-21T00:01:00Z', error_code: null, budget },
    } as never)
    const wrapper = view()
    await flushPromises()

    const text = wrapper.get('[data-testid="probe-budget"]').text()
    expect(text).toContain('500/500')
    expect(text).toContain('剩余 0 条')
    expect(text).toContain('今日已探线路仍可重探')
    expect(button(wrapper, '立即探活').attributes('disabled')).toBeUndefined()
  })

  it('分别展示四维健康、报价覆盖、线路异常和手动探活结果', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const initial: BeichenOverview = {
      ...overview([
        { model: 'lab/tiered', priced: false, catalog: {
          price_state: 'conditional', price_covered: true, credential_ids: ['first'], activation: 'ready',
          protocols: ['openai_responses'], probe_state: 'unprobed', ready_count: 0,
        } },
        { model: 'lab/dropped', active: true, priced: false, catalog: {
          credential_ids: [], protocols: [], ready_count: 0, probe_state: 'unprobed',
          binding_issue: { code: 'catalog_missing', message: '当前 Key 目录里已找不到它。' },
        } },
      ]),
      credentials: [{ credential_id: 'first', label: '渠道一', health: {
        available_dimensions: 3, credential: { state: 'available', message: '已配置' },
        catalog: { state: 'available', message: '目录已读取' },
        pricing: { state: 'available', message: '可计算条件价' },
        probe: { state: 'pending', message: '等待首次探活' },
      } }],
      probe_run: { state: 'idle', started_at: null, finished_at: null, error_code: null },
    }
    const completed = { state: 'completed', started_at: '2026-11-20T00:00:00Z', finished_at: '2026-11-20T00:01:00Z', error_code: null } as const
    api.beichenOverview.mockResolvedValueOnce(initial).mockResolvedValue({
      ...initial, probe_run: completed, model_catalog: initial.model_catalog?.map(row => row.model === 'lab/tiered'
        ? { ...row, probe_state: 'failed', last_probe_at: '2026-11-20T00:01:00Z' } : row),
    })
    api.requestBeichenProbes.mockResolvedValue({ ...initial.probe_run, state: 'queued' })
    api.beichenProbes.mockResolvedValue({ run: completed, results: [] })
    const wrapper = view()
    await flushPromises()
    const health = wrapper.get('.key-health')
    expect(health.get('summary').text()).toContain('3/4 维可用')
    expect(health.findAll('dt').map(node => node.text())).toEqual(['凭据 · 可用', '目录 · 可用', '报价 · 可用', '探活 · 待确认'])
    expect(wrapper.get('.model-summary').text()).toContain('报价覆盖1 / 1')
    expect(wrapper.find('[data-model="lab/dropped"]').exists()).toBe(false)
    await button(wrapper, '异常').trigger('click')
    expect(wrapper.get('[data-model="lab/dropped"]').classes()).toContain('binding-issue-row')
    expect(wrapper.get('[data-model="lab/dropped"]').text()).toContain('当前 Key 目录里已找不到它')
    await button(wrapper, '全部').trigger('click')
    expect(wrapper.get('[data-model="lab/tiered"]').text()).toContain('OpenAI Responses')
    expect(wrapper.get('[data-model="lab/tiered"] .model-probe').text()).toContain('待探活')

    await button(wrapper, '立即探活').trigger('click')
    await flushPromises()
    expect(api.requestBeichenProbes).toHaveBeenCalledWith('north')
    expect(button(wrapper, '立即探活').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('探活已排队')
    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(api.beichenProbes).toHaveBeenCalledWith('north')
    expect(wrapper.get('[data-model="lab/tiered"]').text()).toContain('探活失败')
    expect(wrapper.get('[data-model="lab/tiered"] .model-probe').text()).toContain('11/20 08:01')
    expect(wrapper.get('[data-model="lab/tiered"]').text()).toContain('可启用')
    expect(button(wrapper, '立即探活').attributes('disabled')).toBeUndefined()
  })

  it('优先展示 capabilities 的推荐协议、可用性、下一步和绑定问题', async () => {
    api.beichenOverview.mockResolvedValue(overview([
      { model: 'lab/ready', active: true, catalog: { activation: 'active', protocols: ['openai_compatible'], probe_state: 'unprobed' } },
      { model: 'lab/down', active: true, catalog: { activation: 'active', protocols: ['anthropic'], probe_state: 'failed' } },
    ]))
    api.beichenCapabilities.mockResolvedValue({
      capabilities: [
        capability('lab/ready', {
          supported_protocols: ['openai_compatible', 'openai_responses'], available_protocols: ['openai_responses'],
          recommended_protocol: 'openai_responses', probe_state: 'passed', availability: 'available',
          protocol_details: [
            { protocol: 'openai_compatible', binding_ready: true, probe_state: 'unprobed' },
            { protocol: 'openai_responses', binding_ready: true, probe_state: 'passed' },
          ],
          action_hint: '可使用推荐协议接入。',
        }),
        capability('lab/down', {
          supported_protocols: ['anthropic'], available_protocols: [], recommended_protocol: null, probe_state: 'failed',
          availability: 'maintenance', availability_reason: '维护中。当前线路的协议最近一次探活均失败。',
          protocol_details: [{ protocol: 'anthropic', binding_ready: true, probe_state: 'failed' }],
          action_hint: '请检查渠道是否启用，使用当前 Key 重扫目录。',
        }),
      ],
      gaps: [],
    })
    const wrapper = view()
    await flushPromises()

    const ready = wrapper.get('[data-model="lab/ready"]')
    expect(ready.get('[data-testid="model-availability"]').text()).toBe('可用')
    expect(ready.get('[data-testid="recommended-protocol"]').text()).toBe('推荐 OpenAI Responses')
    expect(ready.get('[data-testid="action-hint"]').text()).toContain('可使用推荐协议接入')
    expect(ready.text()).toContain('OpenAI Chat')

    const down = wrapper.get('[data-model="lab/down"]')
    expect(down.get('[data-testid="model-availability"]').text()).toBe('维护中')
    expect(down.get('[data-testid="recommended-protocol"]').text()).toBe('暂无推荐协议')
    expect(down.get('[data-testid="availability-reason"]').text()).toContain('探活均失败')
    expect(down.get('[data-testid="action-hint"]').text()).toContain('重扫目录')
  })

  it('把 degraded 显示为可用 · 探活异常，并用最新 checked_at 显示探活失败时间', async () => {
    api.beichenOverview.mockResolvedValue(overview([{
      model: 'lab/degraded', active: true,
      catalog: { activation: 'active', protocols: ['anthropic'], probe_state: 'failed', last_probe_at: '2026-11-22T16:00:00Z' },
    }]))
    api.beichenCapabilities.mockResolvedValue({
      capabilities: [capability('lab/degraded', {
        supported_protocols: ['anthropic'], available_protocols: [], recommended_protocol: null,
        probe_state: 'failed', availability: 'degraded',
        availability_reason: '最近一次探活失败，但该模型此前长期可用；仍可尝试调用，请求可能失败。',
        protocol_details: [{
          protocol: 'anthropic', binding_ready: true, probe_state: 'failed',
          probe: { checked_at: '2026-11-22T16:54:00Z', status_code: 500 },
        }],
        action_hint: '请检查渠道是否启用，使用当前 Key 重扫目录。',
      })],
      gaps: [],
    })
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/degraded"]')
    expect(row.get('[data-testid="model-availability"]').text()).toBe('可用 · 探活异常')
    expect(row.get('[data-testid="model-availability"]').classes()).toContain('pending')
    expect(row.text()).not.toContain('维护中')
    expect(row.get('[data-testid="availability-reason"]').text()).toContain('请求可能失败')
    expect(row.text()).toContain('探活失败')
    expect(row.text()).toContain('11/23 00:54')
    expect(row.text()).not.toContain('11/23 00:00')
  })

  it('capabilities 读取失败时回退现有线路字段，不按模型名猜推荐', async () => {
    api.beichenCapabilities.mockRejectedValueOnce(new Error('404'))
    api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/a', catalog: {
      activation: 'active', protocols: ['anthropic'], probe_state: 'passed',
    } }]))
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.get('[data-testid="model-availability"]').text()).toBe('可用')
    expect(row.get('[data-testid="recommended-protocol"]').text()).toBe('推荐协议待同步')
    expect(row.text()).toContain('Anthropic Messages')
    expect(row.text()).toContain('探活通过')
  })

  it('查看档位优先展示 capabilities 的 price_blocker.message 与缺价状态', async () => {
    const data = overview([{ model: 'lab/tiered', catalog: {
      activation: 'blocked', price_state: 'missing', price_covered: false,
      price_blocker: { code: 'old_blocker', message: '旧报价阻塞原因' },
    } }])
    const [model] = data.models!
    model.price_status = 'conditional'
    model.upstream_rates = null
    model.price_conditions_exact = { intervals: [
      { min_tokens: '0', max_tokens: null, input_price: '0.000001', output_price: '0.000002' },
    ] }
    api.beichenOverview.mockResolvedValue(data)
    api.beichenCapabilities.mockResolvedValue({
      capabilities: [capability('lab/tiered', {
        price_state: 'missing', availability: 'available', probe_state: 'passed',
        price_blocker: { code: 'price_snapshot_expired', message: '该模型的官网报价快照已过期。' },
      })],
      gaps: [],
    })
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/tiered"]')
    expect(row.get('[data-testid="price-missing"]').text()).toBe('缺价 · 该模型的官网报价快照已过期。')
    await button(row, '查看档位').trigger('click')
    const dialog = wrapper.get('[aria-label="lab/tiered · 计价档位"]')
    expect(dialog.get('[data-testid="price-blocker-message"]').text()).toBe('该模型的官网报价快照已过期。')
  })

  it('一键启用只提交当前筛选中可启用的新旧模型', async () => {
    const entries: Entry[] = [
      { model: 'lab/active', active: true, catalog: { activation: 'ready' } },
      { model: 'lab/active-new', catalog: { activation: 'active' } },
      { model: 'lab/ready', canEnable: false, catalog: { activation: 'ready' } },
      { model: 'lab/legacy', priced: false, catalog: { activation: null, can_enable: true } },
      { model: 'lab/blocked', catalog: { activation: 'blocked', blockers: [{ code: 'sync_failed', message: '请先同步该渠道报价' }] } },
      { model: 'lab/not-ready', canEnable: false },
      { model: 'lab/not-common' },
      { model: 'outside/model' },
    ]
    api.beichenOverview.mockResolvedValue(overview(entries))
    api.enableBeichenModelsBatch.mockResolvedValue({
      enabled: ['lab/ready', 'lab/legacy'].map(model => ({ model, model_id: model })), skipped: [],
    })
    const wrapper = view({ query: 'lab/', commonIds: new Set(entries.filter(entry => entry.model !== 'lab/not-common').map(entry => entry.model)) })
    await flushPromises()
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"][aria-label="一键启用当前筛选模型"]')
    expect(dialog.text()).toContain('将启用 2 个 · 跳过 4 个')
    expect(dialog.text()).toContain('线路未就绪')
    expect(dialog.text()).toContain('请先同步该渠道报价')
    expect(dialog.text()).not.toMatch(/outside\/model|lab\/not-common/)
    expect(api.enableBeichenModelsBatch).not.toHaveBeenCalled()
    await button(dialog, '确认启用 2 个').trigger('click')
    await flushPromises()
    expect(api.enableBeichenModelsBatch).toHaveBeenCalledExactlyOnceWith('north', ['lab/ready', 'lab/legacy'])
    expect(dialog.text()).toContain('已启用 2 个 · 跳过 4 个 · 失败 0 个')
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
  })

  it('状态筛选会改变一键启用范围，手动价不会被当作无报价', async () => {
    api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/active', active: true }, { model: 'lab/manual', manual: true }, { model: 'lab/missing', priced: false }]))
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '手动价').trigger('click')
    expect(wrapper.findAll('.model-table > tbody > tr').map(row => row.attributes('data-model'))).toEqual(['lab/manual'])
    await button(wrapper, '一键启用').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('将启用 1 个 · 跳过 0 个')
    expect(wrapper.get('[data-model="lab/manual"] .status-pill.manual').text()).toBe('手动价生效 · 官网报价供参考')
    expect(wrapper.get('[aria-label="定价 lab/manual"]').text()).toBe('调整价格')
  })

  it('缺价徽章逐项写出缺失字段，不再只写缺价', async () => {
    api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/missing', priced: false,
      catalog: { price_state: 'missing', price_covered: false } }]))
    const wrapper = view()
    await flushPromises()
    expect(wrapper.get('[data-testid="price-missing"]').text())
      .toBe('缺价 · 输入、输出、缓存写入(5m)、缓存写入(1h)、缓存读取')
  })

  it('价格表使用当前 Key 分组的完整官网价和采集时间', async () => {
    const data = overview([{ model: 'lab/a', catalog: { website_locations: [{
      name: 'lab/a', observed_at: '2026-11-21T11:30:00Z', groups: [{ id: 2, name: '分组 2', rate_multiplier: '0.1' }],
      has_price: true, price_complete: true,
      price_conditions_exact: { input_price: '0.000004', output_price: '0.00002' },
    }] } }])
    data.datasets = [
      { credential_id: 'key-lab/a', kind: 'prices', last_success: '2026-11-21T11:30:00Z', effective_multiplier: null },
      { credential_id: 'key-lab/a', kind: 'billing', group_id: 2, group_rate: '0.1', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '0.1', observed_at: '2026-11-21T11:35:00Z' },
    ]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({ credential_id: 'key-lab/a', group_id: 2, group_name: '分组 2', rate: '0.1',
      observed_at: '2026-11-21T11:30:00Z' }))
    const wrapper = view()
    await flushPromises()
    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.get('[data-price-source="website"]')).toBeTruthy()
    expect(row.get('.current-group-prices .group-prices-head strong').text()).toBe('当前 Key 分组 2 · 生效 0.10×')
    expect(row.get('.current-group-prices time').attributes('datetime')).toBe('2026-11-21T11:30:00Z')
    expect(row.get('.current-group-prices time').text()).toContain('2026/11/21 19:30')
  })

  it('按 200 个分批显示进度，展示逐项原因，重试只发送失败项', async () => {
    const entries = Array.from({ length: 201 }, (_, index) => ({ model: 'batch/' + index }))
    const first = deferred<BeichenBatchEnableResult>()
    const second = deferred<BeichenBatchEnableResult>()
    api.beichenOverview.mockResolvedValue(overview(entries))
    api.enableBeichenModelsBatch.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
      .mockImplementationOnce(async (_provider: string, models: string[]) => ({
        enabled: models.map(model => ({ model, model_id: model })), skipped: [],
      }))
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await button(dialog, '确认启用 201 个').trigger('click')
    expect(dialog.get('progress').attributes()).toMatchObject({ value: '0', max: '201' })
    first.resolve({
      enabled: entries.slice(2, 200).map(({ model }) => ({ model, model_id: model })),
      skipped: [
        { model: 'batch/0', reason: 'already_active', message: '后台已启用' },
        { model: 'batch/1', reason: 'enable_failed', message: '后台线路已变化' },
      ],
    })
    await flushPromises()
    expect(dialog.get('progress').attributes('value')).toBe('200')
    second.reject(new Error('服务暂不可用'))
    await flushPromises()
    expect(api.enableBeichenModelsBatch.mock.calls.map(call => call[1].length)).toEqual([200, 1])
    expect(api.enableBeichenModelsBatch.mock.calls[1]).toEqual(['north', ['batch/200']])
    expect(dialog.text()).toContain('已启用 198 个 · 跳过 1 个 · 失败 2 个')
    expect(dialog.findAll('[data-batch-status="failed"]').map(row => row.text())).toEqual([
      'batch/1失败 · 后台线路已变化', 'batch/200失败 · 启用失败，请稍后重试',
    ])
    expect(toast.error).toHaveBeenCalled()
    await button(dialog, '重试失败项').trigger('click')
    await flushPromises()
    expect(api.enableBeichenModelsBatch.mock.calls[2]).toEqual(['north', ['batch/1', 'batch/200']])
    expect(dialog.text()).toContain('已启用 200 个 · 跳过 1 个 · 失败 0 个')
    expect(dialog.findAll('[data-batch-status="failed"]')).toHaveLength(0)
    expect(api.beichenOverview).toHaveBeenCalledTimes(3)
  })

  it('批次响应缺项时列出原因，重试前跳过刷新后已启用的行', async () => {
    const entries = [{ model: 'lab/committed' }, { model: 'lab/retry' }]
    api.beichenOverview.mockResolvedValueOnce(overview(entries))
      .mockResolvedValue(overview([{ model: 'lab/committed', active: true }, entries[1]]))
    api.enableBeichenModelsBatch.mockResolvedValueOnce({ enabled: [], skipped: [] })
      .mockResolvedValueOnce({ enabled: [{ model: 'lab/retry', model_id: 'school-id' }], skipped: [] })
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await button(dialog, '确认启用 2 个').trigger('click')
    await flushPromises()
    expect(dialog.findAll('[data-batch-status="failed"]')).toHaveLength(2)
    expect(dialog.text()).toContain('服务器未返回结果')
    await button(dialog, '重试失败项').trigger('click')
    await flushPromises()
    expect(api.enableBeichenModelsBatch.mock.calls[1]).toEqual(['north', ['lab/retry']])
    expect(dialog.text()).toContain('已启用 1 个 · 跳过 1 个 · 失败 0 个')
  })

  it('本月汇总和行用量固定为本月，抽屉切换时段不覆盖本月用量', async () => {
    const firstOverview = deferred<BeichenOverview>()
    const day = deferred<BeichenModelUsage>()
    api.beichenOverview.mockReturnValueOnce(firstOverview.promise)
    api.modelUsage.mockImplementation(async (period: BeichenUsageRange) => {
      if (period === 'day') return day.promise
      if (period === 'all') return { ...usage(period), totals: total('999') }
      return usage()
    })
    const wrapper = view()
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(api.modelUsage).toHaveBeenCalledExactlyOnceWith('month', 'north')
    firstOverview.resolve(overview([{ model: 'lab/a' }]))
    await flushPromises()
    const summary = wrapper.get('[aria-label="本月用量汇总"]')
    expect(summary.findAll('dd').map(cell => cell.text())).toEqual(['33.00000000000000000001', '2,000', '12.5%', '3'])
    expect(wrapper.get('.model-usage').text()).toBe('11 积分Token 1,000待核 1 笔')
    await button(wrapper, '查看全部明细').trigger('click')
    const dialog = wrapper.get('[role="dialog"][aria-label="北辰 · 用量明细"]')
    expect(dialog.get('[aria-pressed="true"]').text()).toBe('本月')
    await button(dialog.get('[aria-label="用量时间范围"]'), '当天').trigger('click')
    await button(dialog.get('[aria-label="用量时间范围"]'), '总计').trigger('click')
    await flushPromises()
    day.resolve({ ...usage('day'), totals: total('500') })
    await flushPromises()
    expect(api.modelUsage.mock.calls).toEqual([['month', 'north'], ['day', 'north'], ['all', 'north']])
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(dialog.get('[aria-label="用量汇总"]').text()).toContain('总计已扣积分999')
    expect(summary.findAll('dd').map(cell => cell.text())).toEqual(['33.00000000000000000001', '2,000', '12.5%', '3'])
  })

  it('每 60 秒轮询模型概览和 Key 清单，模型行打开本月学生用量', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const wrapper = view()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
    expect(api.beichenWebsiteKeys).toHaveBeenCalledTimes(2)
    expect(api.modelUsage).toHaveBeenCalledTimes(1)
    await wrapper.get('button[aria-label="查看 lab/a 用量"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.get('button[aria-expanded="true"]').text()).toContain('lab/a')
    expect(dialog.get('tbody').text()).toContain('学生甲')
    expect(dialog.get('tbody').text()).not.toMatch(/学生乙|lab\/b/)
  })

  it('禁用已启用模型需确认，启用按钮旁显示下一步并可打开现有定价入口', async () => {
    api.beichenOverview.mockResolvedValue(overview([
      { model: 'lab/a', active: true },
      { model: 'lab/missing', priced: false },
      { model: 'lab/blocked', catalog: {
        activation: 'blocked',
        blockers: [{ code: 'no_ready_route', message: '请先配置发布线路' }, { code: 'sync_failed', message: '再同步渠道目录' }],
      } },
    ]))
    const wrapper = view()
    await flushPromises()
    const [active, missing, blocked] = wrapper.findAll('.model-table > tbody > tr')
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(active, '禁用').trigger('click')
    expect(api.updateModel).not.toHaveBeenCalled()
    await button(active, '禁用').trigger('click')
    await flushPromises()
    expect(api.updateModel).toHaveBeenCalledWith({ model_id: 'lab/a', status: 'inactive' })
    expect(button(blocked, '启用').element.disabled).toBe(true)
    expect(blocked.get('.enable-reason').text()).toBe('请先配置发布线路；再同步渠道目录')
    expect(button(blocked, '启用').attributes('aria-describedby')).toBe(blocked.get('.enable-reason').attributes('id'))
    expect(button(missing, '启用').element.disabled).toBe(true)
    expect(missing.get('.enable-reason').text()).toBe('官网未报价，请先手动定价')
    await button(missing, '手动定价').trigger('click')
    const drawer = wrapper.getComponent(BeichenPriceDrawer)
    expect(drawer.props('open')).toBe(true)
    expect(drawer.props('row')?.model).toBe('lab/missing')
  })

  it.each([false, true])('条件价行和档位弹窗共用原因，线路不可用为 %s', async noRoute => {
    const priceBlocker = { code: 'tier_missing_output', message: '第 2 档缺少输出 Token 单价' }
    const routeMessage = '没有可启用的北辰线路，请检查渠道状态和线路配置'
    const data = overview([{ model: 'lab/tiered', catalog: {
      activation: 'blocked', price_state: 'conditional', price_covered: false, price_blocker: priceBlocker,
      route_count: noRoute ? 0 : 1,
      blockers: [
        { code: 'conditional_price', message: '线路按条件计价，请先设置手动价' },
        ...(noRoute ? [{ code: 'no_ready_route', message: routeMessage }] : []),
      ],
    } }])
    const [model] = data.models!
    model.price_status = 'conditional'
    model.upstream_rates = null
    model.price_conditions_exact = { intervals: [
      { min_tokens: '0', max_tokens: '100', input_price: '0.000001', output_price: '0.000002' },
      { min_tokens: '100', max_tokens: null, input_price: '0.000002' },
    ] }
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = view()
    await flushPromises()
    const row = wrapper.get('[data-model="lab/tiered"]')
    const reason = noRoute ? routeMessage : priceBlocker.message
    expect(button(row, '启用').element.disabled).toBe(true)
    expect(row.get('.enable-reason').text()).toBe(reason)
    await button(row, '查看档位').trigger('click')
    const dialog = wrapper.get('[aria-label="lab/tiered · 计价档位"]')
    expect(dialog.get('.beichen-note').text()).toBe(reason)
    expect(dialog.get('.price-detail').text()).toContain('每百万 Token 的积分')
    expect(dialog.get('[data-testid="price-blocker-message"]').text()).toBe(priceBlocker.message)
    if (noRoute) expect(dialog.get('[data-testid="tier-route-reason"]').text()).toBe(routeMessage)
    else expect(dialog.find('[data-testid="tier-route-reason"]').exists()).toBe(false)
    expect(dialog.text()).not.toContain('请先设置手动价')
  })

  it('官网价和学生实价精确换算积分及元，保留字符串零价与缺项', async () => {
    const data = overview([{ model: 'lab/a' }])
    const [model] = data.models!
    const exact: BeichenPriceConditions = {
      billing_mode: 'token', input_price: '0.000001234567890123', output_price: '0.000002', cache_read_price: '0', cache_write_price: null,
    }
    model.price_conditions_exact = exact
    data.model_catalog![0].website_locations = [{
      name: 'lab/a', observed_at: '2026-11-21T11:30:00Z', groups: [{ id: 2, name: '分组 2', rate_multiplier: '0.125' }],
      has_price: true, price_complete: true, price_conditions_exact: exact,
    }]
    const route = data.model_catalog![0].preferred_routes![0]
    route.effective_multiplier_text = '0.125'
    route.effective_multiplier = '2'
    data.datasets = [{ credential_id: 'key-lab/a', kind: 'billing', group_id: 2, group_rate: '0.125',
      group_status: 'assigned', billing_state: 'current', effective_multiplier: '0.125',
      observed_at: '2026-11-21T11:35:00Z' }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({ credential_id: 'key-lab/a', group_id: 2, group_name: '分组 2', rate: '0.125',
      observed_at: '2026-11-21T11:30:00Z' }))
    const wrapper = view()
    await flushPromises()
    expect(wrapper.findAll('.model-table > thead th').map(cell => cell.text())).toEqual(['模型', '状态与覆盖', '价格', '本月用量', '操作'])
    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.findAll('[data-testid="model-price-table"] thead th').map(cell => cell.text()))
      .toEqual(['价格', '输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
    expect(currentGroupAmounts(row, 'input')).toEqual(['1.234567890123', '16.97530848919125'])
    expect(currentGroupAmounts(row, 'output')).toEqual(['2', '27.5'])
    expect(currentGroupAmounts(row, 'cache5m')).toEqual(['未列', '—'])
    expect(currentGroupAmounts(row, 'cache1h')).toEqual(['未列', '—'])
    expect(currentGroupAmounts(row, 'cache_read')).toEqual(['0', '0'])
    expect(row.find('[data-testid="current-group-missing"]').exists()).toBe(false)
    expect(componentSource).toContain('@container (max-width: 60rem)')
    expect(componentSource).not.toContain('overflow-x: auto')
  })

  it.each([
    ['per_request', { per_request_price: '0.3' }, '积分/次 · 每次请求 30 积分（0.3 元）',
      '积分/次 · 每次请求 3 积分（0.03 元）', '积分/次 · 每次请求 3.3 积分（0.033 元）'],
    ['video', { per_request_price: '3e-1' }, '积分/秒 · 每秒 30 积分（0.3 元）',
      '积分/秒 · 每秒 3 积分（0.03 元）', '积分/秒 · 每秒 3.3 积分（0.033 元）'],
    ['image', { image_output_price: '0.8', image_input_price: '0.4' },
      '积分/次 · 图像输出 80 积分（0.8 元）\n官网原值（单位未知） · 图像输入 0.4',
      '积分/次 · 图像输出 8 积分（0.08 元）', '积分/次 · 图像输出 8.8 积分（0.088 元）'],
  ] as const)('通过档位入口保留 %s 的价格明细', async (billing_mode, fields, official, settlement, student) => {
    const data = overview([{ model: 'lab/a' }])
    const terms: BeichenPriceConditions = { billing_mode, ...fields }
    data.models![0].price_conditions_exact = terms
    data.models![0].price_observed_at = '2026-11-21T00:05:00Z'
    data.model_catalog![0].website_locations = [{
      name: 'lab/a', observed_at: '2026-11-21T00:00:00Z', groups: [{ id: 2, name: '分组 2', rate_multiplier: '0.1' }],
      has_price: true, price_complete: true, price_conditions_exact: terms,
    }]
    data.datasets = [{ credential_id: 'key-lab/a', kind: 'billing', group_id: 2, group_rate: '0.1',
      group_status: 'assigned', billing_state: 'current', effective_multiplier: '0.1',
      observed_at: '2026-11-21T00:05:00Z' }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({ credential_id: 'key-lab/a', group_id: 2, group_name: '分组 2', rate: '0.1',
      observed_at: '2026-11-21T00:00:00Z' }))
    const wrapper = view()
    await flushPromises()
    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.findAll('[data-price-row="official"] td').map(cell => cell.text()))
      .toEqual(['未列', '未列', '未列', '未列', '未列'])
    expect(row.findAll('[data-price-row="student"] td').map(cell => cell.text()))
      .toEqual(['—', '—', '—', '—', '—'])
    await button(row, '查看档位').trigger('click')
    const dialog = wrapper.get('[aria-label="lab/a · 计价档位"]')
    expect(dialog.findAll('.condition-prices dt').map(cell => cell.text())).toEqual(['官网价', '结算价', '学生价'])
    expect(dialog.findAll('.condition-prices dd').map(cell => cell.element.textContent)).toEqual([official, settlement, student])
  })

  it('把历史流水报价标为待核对并保留手动修正入口', async () => {
    const data = overview([{ model: 'claude-opus-5-5', catalog: { price_state: 'derived', price_covered: true } }])
    data.models![0].price_status = 'derived'
    data.models![0].price_source = 'derived_from_usage'
    data.models![0].price_observed_at = '2026-11-21T12:14:00Z'
    data.models![0].price_conditions_exact = { billing_mode: 'token', input_price: '0.000004', output_price: '0.00002' }
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({
      credential_id: 'key-claude-opus-5-5', group_id: 16, group_name: '分组 16', rate: '0.6',
    }))
    const wrapper = view()
    await flushPromises()
    const model = wrapper.get('[data-model="claude-opus-5-5"]')
    expect(model.findAll('[data-testid="model-price-table"]')).toHaveLength(1)
    expect(model.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
    expect(model.get('[data-testid="derived-reference"]').text()).toContain('输入 4 元、输出 20 元')
    expect(model.get('.current-group-prices time').text()).toContain('2026/11/21 20:14')
    expect(currentGroupAmounts(model, 'input')).toEqual(['未列', '—'])
    expect(model.get('[aria-label="定价 claude-opus-5-5"]').text()).toBe('手动定价')
  })

  it.each([
    [2, '倍率待核实', ['—', '—', '—', '—', '—']],
    [16, '生效 0.60×', ['264', '1320', '—', '—', '—']],
  ] as const)('当前 Key 为分组 16 时使用 CC1 基础价，不采用概览分组 %s 的旧倍率',
    async (billingGroupId, rateLabel, student) => {
    const model = 'claude-opus-5-5'
    const credentialId = 'key-' + model
    const cc1 = '[CC1]Claudecode-kiro-015'
    const cc2 = '[CC2]ClaudeCode高智'
    const data = overview([{ model, catalog: {
      catalog_keys: [{ credential_id: credentialId, label: 'cc不满血', group_id: 2,
        state: 'listed', upstream_model: model, group_diagnosis: 'price_outside_key_group' }],
      website_locations: [
        { name: model, observed_at: '2026-11-21T00:00:00Z', groups: [{ id: 2, name: cc1, rate_multiplier: '0.15' }],
          has_price: true, price_complete: true, channel: 'CC1', price_scope: 'platform',
          price_conditions_exact: { input_price: '0.000004', output_price: '0.00002' } },
        { name: model, observed_at: '2026-11-21T00:00:00Z', groups: [{ id: 17, rate_multiplier: '0.5' }], has_price: false },
      ],
    } }])
    data.credentials = [{ credential_id: credentialId, label: 'cc不满血', status: 'active' }]
    data.datasets = [
      { kind: 'billing', credential_id: credentialId, group_id: billingGroupId, group_name: billingGroupId === 2 ? cc1 : cc2,
        group_rate: billingGroupId === 2 ? '0.15' : '0.60', effective_multiplier: billingGroupId === 2 ? '0.15' : '0.6',
        billing_state: 'current', observed_at: '2026-11-21T13:00:00Z', group_status: 'assigned' },
      { kind: 'prices', credential_id: credentialId, effective_multiplier: null, official_price_missing: true,
        last_error: '北辰渠道页没有当前 Key 的有效模型报价' },
    ]
    Object.assign(data.models![0], {
      price_status: 'derived', price_source: 'derived_from_usage', price_observed_at: '2026-11-21T12:14:00Z',
      price_conditions_exact: { billing_mode: 'token', input_price: '0.000004', output_price: '0.00002' },
    })
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({
      credential_id: credentialId, group_id: 16, group_name: cc2, rate: '0.6', observed_at: '2026-11-21T12:00:00Z',
    }))
    const wrapper = view()
    await flushPromises()

    expect(api.beichenWebsiteKeys).toHaveBeenCalledExactlyOnceWith('north')
    const row = wrapper.get(`[data-model="${model}"]`)
    const current = row.get('.current-group-prices')
    expect(current.get('.group-prices-head strong').text()).toBe('当前 Key 分组 16 · ' + rateLabel)
    expect(current.get('[data-testid="price-basis"]').text())
      .toBe(rateLabel === '倍率待核实'
        ? '基础价来自 CC1 同名模型；当前 Key 分组 16 的倍率待核实'
        : '基础价来自 CC1 同名模型；当前 Key 分组 16 的倍率为 0.60×')
    expect(current.text()).not.toContain('0.15×')
    expect(current.text()).not.toContain(cc1)
    expect(wrapper.get('[aria-label="北辰 Key 计费倍率"]').text()).not.toContain('0.15×')
    expect(row.find('[data-testid="current-group-missing"]').exists()).toBe(false)
    expect(row.find('[data-testid="group-mismatch"]').exists()).toBe(false)
    expect(current.get('time').attributes('datetime')).toBe('2026-11-21T00:00:00Z')
    const table = current.get('[data-testid="model-price-table"]')
    expect(table.findAll('thead th').map(cell => cell.text()))
      .toEqual(['价格', '输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
    expect(table.findAll('[data-price-row] > th').map(cell => cell.text()))
      .toEqual(['上游基础价（元 / 1M）', '学生实价（积分 / 1M）'])
    expect(table.findAll('[data-price-row="official"] td').map(cell => cell.text()))
      .toEqual(['4', '20', '未列', '未列', '未列'])
    expect(table.findAll('[data-price-row="student"] td').map(cell => cell.text()))
      .toEqual(student)
    expect(row.find('.other-groups').exists()).toBe(false)
  })

  it.each([
    [undefined, ['—', '—', '—', '—', '—']],
    ['2026-11-21T13:07:00Z', ['13.2', '26.4', '16.5', '26.4', '0']],
  ])('分组 2 展示完整的官网及学生报价，计费倍率观测时间为 %s', async (billingObservedAt, student) => {
    const model = 'claude-sonnet-5-5'
    const credentialId = 'key-' + model
    const groupName = '[CC1]Claudecode-kiro-015'
    const data = overview([{ model, catalog: {
      website_locations: [{
        name: model, observed_at: '2026-11-21T13:07:28Z', groups: [{ id: 2, name: groupName, rate_multiplier: '0.2' }],
        has_price: true, price_complete: true,
        price_conditions_exact: { billing_mode: 'token', input_price: '0.000001', output_price: '0.000002',
          cache_write_price: '0.00000125', cache_write_1h_price: '0.000002', cache_read_price: '0' },
      }],
    } }])
    data.datasets = [{ credential_id: credentialId, kind: 'billing', group_id: 2, group_rate: '0.15',
      billing_state: 'current', effective_multiplier: '0.12', observed_at: billingObservedAt }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({
      credential_id: credentialId, group_id: 2, group_name: groupName, rate: '0.15', observed_at: '2026-11-21T13:00:00Z',
    }))
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get(`[data-model="${model}"]`)
    const current = row.get('.current-group-prices')
    expect(current.get('.group-prices-head strong').text())
      .toBe('当前 Key 分组 2 · ' + (billingObservedAt ? '生效 0.12×' : '倍率待核实'))
    if (billingObservedAt) {
      expect(current.get('[data-testid="rate-note"]').text()).toBe('分组 0.15× / 账号 0.12×')
    } else {
      expect(current.find('[data-testid="rate-note"]').exists()).toBe(false)
    }
    expect(row.find('.other-groups').exists()).toBe(false)
    expect(current.attributes('data-price-source')).toBe('website')
    const quote = current.get('[data-testid="model-price-table"]')
    expect(quote.findAll('[data-price-row="official"] td').map(cell => cell.text())).toEqual(['1', '2', '1.25', '2', '0'])
    expect(quote.findAll('[data-price-row="student"] td').map(cell => cell.text())).toEqual(student)
  })

  it('分组 16 不使用分组 2 的专属 Sonnet 价，只有平台级价可作跨组基础价', async () => {
    const groupOnly = 'claude-sonnet-5-5'
    const platform = 'claude-sonnet-5-5-platform'
    const cc1 = '[CC1]Claudecode-kiro-015'
    const data = overview([
      { model: groupOnly, catalog: {
        website_locations: [pricedLocation(2, cc1, '0.15', '0.000002', '0.00001', 'CC1', true, 'group')],
      } },
      { model: platform, catalog: {
        website_locations: [pricedLocation(2, cc1, '0.15', '0.000002', '0.00001', 'CC1', true, 'platform')],
      } },
    ])
    data.datasets = [groupOnly, platform].map(model => ({
      kind: 'billing' as const, credential_id: 'key-' + model, group_id: 16, group_rate: '0.6',
      group_status: 'assigned' as const, billing_state: 'current' as const, effective_multiplier: '0.6',
      observed_at: '2026-11-21T12:05:00Z',
    }))
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys(
      { credential_id: 'key-' + groupOnly, group_id: 16, group_name: '分组 16', rate: '0.6', observed_at: '2026-11-21T12:00:00Z' },
      { credential_id: 'key-' + platform, group_id: 16, group_name: '分组 16', rate: '0.6', observed_at: '2026-11-21T12:00:00Z' },
    ))
    const wrapper = view()
    await flushPromises()

    const exclusive = wrapper.get('[data-model="' + groupOnly + '"]')
    expect(exclusive.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
    expect(exclusive.findAll('[data-price-row="official"] td').map(cell => cell.text()))
      .toEqual(['未列', '未列', '未列', '未列', '未列'])
    expect(exclusive.findAll('[data-price-row="student"] td').map(cell => cell.text()))
      .toEqual(['—', '—', '—', '—', '—'])

    const shared = wrapper.get('[data-model="' + platform + '"]')
    expect(shared.find('[data-testid="price-base-missing"]').exists()).toBe(false)
    expect(currentGroupAmounts(shared, 'input')).toEqual(['2', '132'])
    expect(currentGroupAmounts(shared, 'output')).toEqual(['10', '660'])
  })

  it('Key 清单明确未分组时提示未匹配，保留上次分组供比较', async () => {
    const model = 'lab/a'
    const credentialId = 'key-' + model
    const data = overview([{ model, catalog: {
      website_locations: [{
        name: model, observed_at: '2026-11-21T13:07:28Z', groups: [{ id: 2, rate_multiplier: '0.15' }],
        has_price: true, price_conditions_exact: { input_price: '0.000004' },
      }],
    } }])
    data.datasets = [{ credential_id: credentialId, kind: 'billing', group_id: 2, group_rate: '0.15',
      billing_state: 'current', effective_multiplier: '0.15' }]
    api.beichenOverview.mockResolvedValue(data)
    api.beichenWebsiteKeys.mockResolvedValue(websiteKeys({ credential_id: credentialId, group_id: null, group_name: null, rate: null }))
    const wrapper = view()
    await flushPromises()

    const row = wrapper.get('[data-model="lab/a"]')
    expect(row.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
    expect(row.find('.current-group-prices').exists()).toBe(false)
    expect(row.find('.other-groups').exists()).toBe(false)
  })

  it('显示四路核对的型号差异与官网报价单位', async () => {
    const data = overview([{ model: 'claude-fable-5' }])
    data.website_only_status = 'compared'
    data.website_directory = {
      state: 'fresh', last_success: '2026-11-21T12:00:00Z', last_error: null, price_unit: '元（北辰余额单位）',
      last_run: {
        started_at: '2026-11-21T12:00:00Z', duration_ms: 40, platform_count: 2, group_count: 2,
        model_count: 2, expected_model_count: 2, failures: [],
        key_model_comparisons: [
          { channel: 'CC1', group_id: 2, state: 'checked', only_key: ['local-key-model'], only_channel: ['site-model'],
            matched: [{ key_model: 'CLAUDE-FABLE-5', channel_model: 'claude-fable-5', match: 'normalized', price_complete: true }] },
          { channel: 'Gemini', group_id: 5, state: 'unavailable', error: '北辰接口返回 HTTP 403', matched: [], only_key: [], only_channel: [] },
        ],
      },
    }
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = view()
    await flushPromises()
    const compare = wrapper.get('[aria-label="官网目录"] details')
    expect(compare.text()).toContain('CLAUDE-FABLE-5 → claude-fable-5')
    expect(compare.text()).toContain('仅 Key · local-key-model')
    expect(compare.text()).toContain('仅官网 · site-model')
    expect(compare.text()).toContain('北辰接口返回 HTTP 403')
    expect(wrapper.text()).toContain('官网报价单位为元（北辰余额单位）')
  })

  it('覆盖状态独立显示，协议去重且兼容旧数据', async () => {
    const data = overview([
      { model: 'lab/new', catalog: { key_count: 2, route_count: 4, ready_count: 2, activation: 'ready', price_state: 'confirmed' } },
      { model: 'lab/old', active: true, catalog: { key_count: null, route_count: null, ready_count: null, activation: null, price_state: null, blockers: null } },
      { model: 'lab/empty', priced: false, catalog: { preferred_routes: null, can_enable: null, school_status: null, blockers: null } },
    ])
    const routes = data.model_catalog![0].preferred_routes!
    routes.push({ ...routes[0], credential_id: 'key-lab/old' }, { ...routes[0], adapter: 'anthropic' })
    api.beichenOverview.mockResolvedValue({
      ...data,
      credentials: ['key-lab/new', 'key-lab/old', 'key-lab/empty'].map(credential_id => ({ credential_id, label: '渠道' })),
    })
    api.modelUsage.mockResolvedValue({ ...usage(), totals: { ...total(), cache_hit_rate: null, completeness: null }, models: [], students: [] })
    const wrapper = view()
    await flushPromises()
    const states = wrapper.findAll('.model-state-summary').map(row => row.text())
    expect(states).toEqual([
      'Key 覆盖 2/3 · 协议 2 · 已发布 2',
      'Key 覆盖 1/3 · 协议 1 · 已发布 1',
      'Key 覆盖 1/3 · 协议 0 · 已发布 0',
    ])
    expect(wrapper.get('.model-state-summary [title]').attributes('title')).toBe('4 条候选线路')
    const empty = wrapper.get('[data-model="lab/empty"]')
    expect(empty.find('[data-testid="model-price-table"]').exists()).toBe(false)
    expect(empty.get('[data-testid="protocol-missing"]').text()).toBe('缺协议')
    expect(empty.get('[data-testid="price-missing"]').text()).toBe('缺价 · 输入、输出、缓存写入(5m)、缓存写入(1h)、缓存读取')
    expect(wrapper.get('[aria-label="本月用量汇总"]').findAll('dd')[2].text()).toBe('—')
    expect(wrapper.text()).not.toMatch(/undefined|NaN/)
  })

  it('用量失败不影响模型目录，可单独重试', async () => {
    api.modelUsage.mockRejectedValueOnce(new Error('读取失败'))
    const wrapper = view()
    await flushPromises()
    expect(wrapper.findAll('.model-table > tbody > tr')).toHaveLength(1)
    expect(wrapper.get('.usage-feedback').text()).toContain('读取用量失败')
    await button(wrapper, '重试用量').trigger('click')
    await flushPromises()
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(api.modelUsage.mock.calls).toEqual([['month', 'north'], ['month', 'north']])
  })
})
