import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import App from '../App.vue'
import router from '../router'
import { token } from '../api/client'
import { useAuthStore } from '../store/auth'
import type { BeichenModel, BeichenModelUsage, BeichenOverview, BeichenPriceConditions, ProjectUsage, Role } from '../api/types'
import { indexWebsiteKeyGroups, schoolCatalog } from '../utils/beichenModels'
import { schoolDay, schoolMonth } from '../utils/format'

vi.mock('../api/client', async original => ({
  ...await original<typeof import('../api/client')>(), openEvents: vi.fn(),
}))
const month = schoolMonth()
const workspaceUrl = 'https://workspace.example.test/apps/beichen'
const workspaceSection = '[aria-label="校方北辰工作区"]'
const school = { id: 'north', name: '北辰', base_url: 'https://api.beichenapi.com', status: 'active', credential_configured: true, credentials: [
  { id: 'cheap', label: '隐藏的低倍率渠道', credential_configured: true, status: 'active' },
  { id: 'chosen', label: '隐藏的首选渠道', credential_configured: true, status: 'active' },
] }
const other = { ...school, id: 'other', name: '其他供应商', base_url: 'https://provider.example', credentials: [{ id: 'other-key', label: '备用线路', status: 'active', credential_configured: true }] }
const project = { id: 'p', organization_id: 'org', name: '虚构项目', status: 'active', monthly_credit_quota: '500', mentors: [] }
let quota: string
let enabled: boolean
let discoveredEnabled: boolean
let discoveredPriced: boolean
let failQuota: boolean
let keyRate = '2.125000000000000000000001'
let role: Role
let widget: VueWrapper
const writes = vi.fn()
const reply = (data: unknown) => new Response(JSON.stringify({ data }))

function modelUsage(): BeichenModelUsage {
  const totals = { credits: '11', tokens: 1000, input_tokens: 600, output_tokens: 200, cache_read_tokens: 100, cache_write_tokens: 100, calls: 2, pending_calls: 1, students: 1, keys: 1 }
  return { range: 'all', start: null, end: '2026-11-20T12:00:00+08:00', timezone: 'Asia/Shanghai', totals,
    models: [{ model: 'shared', ...totals }],
    students: [{ user_id: 's', user_name: '学生甲', project_id: 'p', project_name: project.name, credits: '11', tokens: 1000, calls: 2, models: [{ model: 'shared', credits: '11', tokens: 1000, calls: 2 }] }],
    keys: [{ api_key_id: 'student-key', api_key_name: '实验', user_id: 's', user_name: '学生甲', project_name: project.name, credits: '11', tokens: 1000, calls: 2 }],
  }
}

function projectUsage(): ProjectUsage {
  const member = { is_student: true, user_id: 's', user_name: '学生甲', quota, credits: '11', available: quota === '100' ? '89' : '0',
    beichen_actual_cost: '0.09999999999999', beichen_unmatched_calls: 1,
    keys: [{ id: 'student-key', name: '实验', status: 'active' as const, used: '11', models: [
      { id: 'shared', name: 'shared', credits: '11', calls: 2, beichen_actual_cost: '0.09999999999999',
        beichen_base_credits: '10', beichen_school_multiplier: '1.10', beichen_charged_credits: '11',
        beichen_base_source: 'gateway_usage_price', beichen_unmatched_calls: 1 },
    ] }], free_models: [] }
  return { month, project_id: 'p', project_name: project.name, quota: '500', allocated: quota, available: '400', charged: '11', pending: '0',
    models: [], free_models: [], members: [member] }
}

async function fetchFixture(url: string, init?: RequestInit) {
  const path = new URL(url, 'https://school.test').pathname
  if (!path.startsWith('/api/v1/' + role + '/')) throw Error('越过角色范围 ' + path)
  if (init?.method === 'PATCH' || init?.method === 'POST') {
    const body = JSON.parse(String(init.body))
    writes(path, body)
    if (path.endsWith('/monthly-credit-quota')) {
      if (failQuota) return new Response(JSON.stringify({ error: { code: 'PROJECT_QUOTA_ALLOCATION_EXCEEDED', message: 'project quota allocation exceeded' } }), { status: 409 })
      quota = body.monthly_credits
      return new Response(null, { status: 204 })
    }
    if (init.method === 'PATCH' && path.endsWith('/models/configuration') && body.model_id === 'school-alias') {
      enabled = body.status === 'active'
      return reply({})
    }
    if (init.method === 'POST' && path === '/api/v1/teacher/providers/north/beichen/models/enable') {
      if (body.model === 'lab/new-model') discoveredEnabled = true
      else if (body.model === 'shared' && body.school_model === 'school-alias') enabled = true
      else throw Error('意外的启用目标')
      return reply({ model_id: '00000000-0000-4000-8000-000000000001', status: 'active' })
    }
    if (path === '/api/v1/teacher/providers/north/beichen/models/enable-batch') {
      const models = body.models as string[]
      if (models.includes('lab/new-model')) discoveredEnabled = true
      if (models.includes('shared')) enabled = true
      return reply({ enabled: models.map(model => ({ model, model_id: 'school-' + model })), skipped: [] })
    }
    if (path === '/api/v1/teacher/providers/north/beichen/models/price') return reply({ saved: true, credentials: [{ credential_id: 'chosen', label: '教学 Key' }] })
    throw Error('未预期的写入 ' + path)
  }
  if (path.endsWith('/providers')) return reply([school, other])
  if (path === '/api/v1/teacher/model-usage') return reply({ ...modelUsage(), range: new URL(url, 'https://school.test').searchParams.get('range') })
  if (path === '/api/v1/teacher/providers/north/beichen/receipts') return reply({ items: [], next_cursor: null })
  if (path === '/api/v1/teacher/providers/north/beichen/keys') return reply({
    status: 'current', last_seen: '2026-11-21T13:00:00Z', keys: [
      { id: 1, name: '首选 Key', key_prefix: null, status: 'active', enabled: true, created_at: null, last_seen: '2026-11-21T13:00:00Z', observed_at: '2026-11-21T13:00:00Z', match_status: 'matched', credential_id: 'chosen', group_id: 11, group_name: '首选分组', rate: keyRate },
      { id: 2, name: '低价 Key', key_prefix: null, status: 'active', enabled: true, created_at: null, last_seen: '2026-11-21T13:00:00Z', observed_at: '2026-11-21T13:00:00Z', match_status: 'matched', credential_id: 'cheap', group_id: 12, group_name: '低价分组', rate: '0.0625' },
      { id: 3, name: '陈旧 Key', key_prefix: null, status: 'active', enabled: true, created_at: null, last_seen: '2026-11-21T13:00:00Z', match_status: 'matched', credential_id: 'stale', group_id: 13, group_name: '陈旧分组', rate: '0.3' },
    ],
  })
  if (path.endsWith('/beichen')) return reply({
    web_login_required: true,
    maintenance_workspace: { launch_url: workspaceUrl },
    website_sync: {
      channels: 4, priced_channels: 3, receipt_checked_channels: 4, receipt_caught_up_channels: 1,
      price_last_seen: '2026-11-01T15:30:00Z', receipt_last_checked: '2026-11-01T16:02:03Z',
    },
    datasets: [
      { kind: 'billing', credential_id: 'chosen', group_id: 11, group_rate: keyRate, group_status: 'assigned',
        billing_state: 'current', effective_multiplier: keyRate, observed_at: '2026-11-21T13:05:00Z' },
      { kind: 'billing', credential_id: 'cheap', group_id: 12, group_rate: '0.0625', group_status: 'assigned',
        billing_state: 'current', effective_multiplier: '0.0625', observed_at: '2026-11-21T13:05:00Z' },
      { kind: 'billing', credential_id: 'stale', last_error: '本轮读取失败', effective_multiplier: 0.3 },
    ],
    models: [
      { credential_id: 'chosen', model: 'shared', price_status: 'confirmed', price_observed_at: '2026-11-21T13:00:00Z', price_conditions: {
        billing_mode: 'token', input_price: '0.0000001', output_price: '0.0000002', cache_read_price: '0', cache_write_price: null,
      }, upstream_rates: {
        input: '0.000000031', output: '2', cache_read: '0', cache_write: null,
      } },
      { credential_id: 'cheap', model: 'shared', price_status: 'conditional', price_observed_at: '2026-11-21T13:00:00Z', upstream_rates: { input: '1', output: '2' }, price_conditions: { cache_write_1h_price: 0.1 },
        price_conditions_exact: { billing_mode: 'token', cache_write_1h_price: '0', cache_write_12h_price: '0.000004', internal_route: '隐藏的渠道',
          intervals: [{ min_tokens: '200000', max_tokens: null, input_price: '0.000008000000000000000019' }],
          reasoning_effort_multipliers: { low: '0.5' },
        } },
      ...(discoveredPriced ? [{ credential_id: 'chosen', model: 'lab/new-model', mode: 'follow', price_status: 'confirmed',
        price_conditions_exact: { billing_mode: 'token', input_price: '0.000001', output_price: '0.000002' },
        upstream_rates: { input: '150000', output: '300000' }, school_rates: { input: '15000000', output: '30000000' },
      }] : []),
    ],
    model_catalog: [
      { model: 'shared', school_model: 'school-alias', school_status: enabled ? 'active' : 'inactive', can_enable: !enabled,
        source: 'both', catalog_state: 'key_listed', credential_ids: ['chosen', 'cheap'], key_count: 2,
        website_locations: [
          { name: 'shared', observed_at: '2026-11-21T13:00:00Z', groups: [{ id: 11, name: '首选分组', rate_multiplier: keyRate }],
            has_price: true, price_complete: true,
            price_conditions_exact: { billing_mode: 'token', input_price: '0.0000001', output_price: '0.0000002', cache_read_price: '0', cache_write_price: null } },
          { name: 'shared', observed_at: '2026-11-21T13:00:00Z', groups: [{ id: 12, name: '低价分组', rate_multiplier: '0.0625' }],
            has_price: true, price_complete: true,
            price_conditions_exact: { billing_mode: 'token', input_price: '0.0000001', output_price: '0.0000002', cache_read_price: '0', cache_write_price: null } },
        ],
        preferred_routes: [
        { school_model: 'school-alias', adapter: 'openai_responses', state: enabled ? 'ready' : 'candidate', provider_id: 'north', credential_id: 'chosen', priority: 10,
          effective_multiplier: 2.125, effective_multiplier_text: '2.125000000000000000000001' },
        { school_model: 'school-alias', adapter: 'anthropic', state: 'candidate', provider_id: 'north', credential_id: 'cheap', priority: 1, effective_multiplier: '0.0625' },
      ] },
      { model: 'lab/new-model', school_status: discoveredEnabled ? 'active' : 'not_configured', can_enable: !discoveredEnabled,
        source: 'both', catalog_state: 'key_listed', credential_ids: ['chosen'], key_count: 1,
        website_locations: discoveredPriced ? [{
          name: 'lab/new-model', observed_at: '2026-11-21T13:00:00Z', groups: [{ id: 11, name: '首选分组', rate_multiplier: keyRate }],
          has_price: true, price_complete: true,
          price_conditions_exact: { billing_mode: 'token', input_price: '0.000001', output_price: '0.000002' },
        }] : [],
        preferred_routes: ['openai_responses', 'openai_compatible'].map(adapter => ({
          school_model: 'lab/new-model', adapter, state: discoveredEnabled ? 'ready' : 'candidate', provider_id: 'north',
          credential_id: 'chosen', effective_multiplier: 0.15,
        })) },
      { model: 'paused', school_status: 'not_configured', can_enable: false, preferred_routes: [
        { school_model: 'paused', adapter: 'anthropic', state: 'candidate', provider_id: 'north', credential_id: 'stale', effective_multiplier: '0.3' },
      ], catalog_state: 'key_listed', credential_ids: ['stale'], key_count: 1 },
      { model: 'no-price', preferred_routes: [], catalog_state: 'key_listed', credential_ids: [], key_count: 0 },
    ],
  })
  if (path.endsWith('/models')) return reply(['cheap', 'chosen'].map(credential_id =>
    ({ model_id: 'shared', display_name: 'shared', status: 'active', provider_id: 'north', credential_id, is_common: false })))
  if (path.endsWith('/projects')) return reply([project])
  if (path.endsWith('/teacher/projects/p/usage')) return reply(projectUsage())
  if (path.endsWith('/project-spend')) return reply([projectUsage()])
  if (path.endsWith('/organizations') || path.endsWith('/mentor-project-applications') || path.endsWith('/project-budgets') || path.endsWith('/api-keys')) return reply([])
  if (path.endsWith('/student/usage')) return reply({ month, projects: [{ project_id: 'p', project_name: project.name, quota, used: '11',
    available: quota === '100' ? '89' : '0', pending: '0', keys: [] }] })
  throw Error('未预期的请求 ' + path)
}
const request = vi.fn(fetchFixture)
beforeEach(() => {
  quota = '10'; enabled = true; discoveredEnabled = false; discoveredPriced = false; failQuota = false
  keyRate = '2.125000000000000000000001'
  writes.mockClear(); request.mockClear()
  vi.stubGlobal('fetch', request)
  vi.stubGlobal('confirm', vi.fn(() => true))
})
afterEach(() => { widget?.unmount(); token.set(null); vi.unstubAllGlobals(); vi.useRealTimers() })

async function enter(identity: Role, path: string) {
  role = identity
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore(pinia)
  auth.user = { id: 'fixture-' + role, name: '测试' + role, email: role + '@example.test', role, status: 'active', created_at: '' }
  auth.identity = role; auth.identities = [role]; auth.ready = true
  token.set('local-test-session')
  await router.push(path)
  widget = mount(App, { attachTo: document.body, global: { plugins: [pinia, router], stubs: { CreditDonut: true } } })
  await flushPromises()
  expect(router.currentRoute.value.meta.role).toBe(identity)
}
function page() { return new DOMWrapper(document.body) }
function button(label: string, root: Pick<DOMWrapper<Element>, 'findAll'> = page()) {
  const b = root.findAll('button').find(item => item.text() === label)
  if (!b) throw Error('没有找到按钮 ' + label)
  return b
}

function visiblePrices(row: Pick<DOMWrapper<Element>, 'findAll'>) {
  return row.findAll('[data-testid="model-price-table"] tbody tr').map(line => {
    const label = line.get('th').text().replace(/（.*$/, '')
    return [label, ...line.findAll('td').map(cell => cell.text())]
  })
}

it('教师在两个入口看到同一份按模型合并的首选报价，可启停已有校内别名，其他供应商仍能维护', async () => {
  await enter('teacher', '/teacher/providers')
  const [north, relay] = widget.findAll('article')
  expect(north.text()).toContain('Key 数 2')
  expect(north.text()).not.toMatch(/隐藏的|编辑|授权/)
  expect(relay.text()).toContain('备用线路')
  await button('编辑', relay).trigger('click')
  expect(page().get('[role="dialog"]').text()).toContain('替换 Key')
  await page().get('button[aria-label="关闭"]').trigger('click')
  await button('查看模型', north).trigger('click')
  await flushPromises()
  const table = page().get('[aria-label="北辰模型目录"]')
  expect(table.findAll('.model-table > tbody > tr > th').map(item => item.text())).toEqual(['shared', 'lab/new-model', 'paused', 'no-price'])
  const [shared, , paused, historical] = table.findAll('.model-table > tbody > tr')
  expect(shared.text()).toContain('2.125000000000000000000001×')
  expect(table.text()).not.toContain('0.1×')
  expect(paused.text()).toContain('官网未报价')
  expect(paused.text()).not.toContain('0.3×')
  const pausedEnable = paused.find('button[aria-label="启用 paused"]')
  expect(pausedEnable.exists()).toBe(true)
  expect(pausedEnable.attributes('disabled')).toBeDefined()
  expect(button('手动定价', paused).exists()).toBe(true)
  expect(historical.text()).toContain('启用条件待同步')
  expect(table.text()).toContain('北辰官网需要学校后台重新授权')
  expect(shared.findAll('[data-testid="model-price-table"]')).toHaveLength(1)
  expect(visiblePrices(shared)).toEqual([
    ['上游基础价', '0.1', '0.2', '未列', '未列', '0'],
    ['学生实价', '23.375000000000000000000011', '46.750000000000000000000022', '—', '—', '0'],
  ])
  expect(shared.get('[data-testid="group-prices"]').attributes('data-price-source')).toBe('website')
  expect(shared.text()).not.toContain('上下文分档')
  await button('查看档位', shared).trigger('click')
  await flushPromises()
  const conditions = page().findAll('[role="dialog"]').at(-1)!
  const details = conditions.findAll('.condition-prices').at(-1)!
  const [standard, actual] = details.findAll('dd').map(item => item.text())
  expect(standard).toEqual([
    '每百万 Token 的积分 · 缓存写入（1 小时） 0 积分（0 元） · 缓存写入（12 小时） 400 积分（4 元）',
    '上下文分档（Token 下限 200000，上限 无上限）',
    '每百万 Token 的积分 · 输入 800.0000000000000019 积分（8.000000000000000019 元）',
    '推理强度（低）倍率 0.5×',
  ].join('\n'))
  expect(actual).toEqual([
    '每百万 Token 的积分 · 缓存写入（1 小时） 0 积分（0 元） · 缓存写入（12 小时） 25 积分（0.25 元）',
    '上下文分档（Token 下限 200000，上限 无上限）',
    '每百万 Token 的积分 · 输入 50.00000000000000011875 积分（0.5000000000000000011875 元）',
  ].join('\n'))
  await conditions.get('button[aria-label="关闭"]').trigger('click')
  expect(shared.find('details.catalog-evidence').exists()).toBe(true)
  expect(table.text()).not.toMatch(/隐藏的|chosen|cheap|school-alias|OpenAI|Anthropic|路由|渠道/i)
  expect(writes).not.toHaveBeenCalled()
  await button('禁用', shared).trigger('click')
  await flushPromises()
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/models/configuration', { model_id: 'school-alias', status: 'inactive' })
  expect(shared.get('.current-group-prices').text()).toContain('0.0625×')
  await button('启用', shared).trigger('click')
  await flushPromises()
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/providers/north/beichen/models/enable', { model: 'shared', school_model: 'school-alias' })
  expect(shared.text()).toContain('已启用')
  await page().get('button[aria-label="关闭"]').trigger('click')
  await widget.get('a[href="/teacher/models"]').trigger('click')
  await flushPromises()
  const models = widget.get('[aria-label="北辰模型目录"]')
  expect(models.findAll('.model-table > tbody > tr')).toHaveLength(4)
  const modelsRow = models.get('[data-model="shared"]')
  expect(visiblePrices(modelsRow)).toEqual(visiblePrices(shared))
  expect(widget.find('[aria-label="近期逐笔对账"]').exists()).toBe(false)
  expect(widget.find('article.panel').exists()).toBe(false)
})

it('教师从目录直接启用未配置模型，失败后可重试，后台成功并重读目录后才显示已启用', async () => {
  await enter('teacher', '/teacher/models')
  const table = widget.get('[aria-label="北辰模型目录"]')
  const row = table.get('[data-model="lab/new-model"]')
  expect(row.text()).toContain('未配置')
  expect(row.get('[data-testid="price-missing"]').text()).toContain('缺价')
  expect(row.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
  expect(button('启用', row).element.disabled).toBe(true)
  // 目录已发现模型但尚无价格时，先等待下一轮补价，再允许启用。
  discoveredPriced = true
  await button('刷新模型', table).trigger('click')
  await flushPromises()
  expect(button('启用', row).element.disabled).toBe(false)
  expect(writes).not.toHaveBeenCalled()
  request.mockImplementationOnce(async () => new Response(JSON.stringify({
    error: { code: 'MODEL_NOT_READY', message: 'model not ready' },
  }), { status: 409 }))
  await button('启用', row).trigger('click')
  await flushPromises()
  expect(table.get('[role="alert"]').text()).toContain('尚未完成可用配置')
  expect(row.text()).toContain('未配置')

  let finish!: () => void
  const submitting = new Promise<void>(resolve => { finish = resolve })
  request.mockImplementationOnce(async (url, init) => { await submitting; return fetchFixture(url, init) })
  const before = request.mock.calls.length
  await button('启用', row).trigger('click')
  expect(row.get<HTMLButtonElement>('button[aria-label="启用 lab/new-model"]').element.disabled).toBe(true)
  expect(row.text()).not.toContain('已启用')
  finish()
  await flushPromises()
  expect(request.mock.calls.slice(before).map(([url, init]) => [init?.method ?? 'GET', url.split('?')[0]])).toEqual([
    ['POST', '/api/v1/teacher/providers/north/beichen/models/enable'],
    ['GET', '/api/v1/teacher/providers/north/beichen'],
    ['GET', '/api/v1/teacher/providers/north/beichen/keys'],
  ])
  expect(writes).toHaveBeenCalledExactlyOnceWith('/api/v1/teacher/providers/north/beichen/models/enable', { model: 'lab/new-model' })
  expect(row.text()).toContain('已启用')
  expect(row.text()).toContain('2.125000000000000000000001×')
  expect(row.text()).not.toMatch(/待启用|未配置|openai|chosen/i)
  expect(button('禁用', row).exists()).toBe(true)
})

const discoveredModel = 'catalog/new-from-key'
type PriceSnapshot = Pick<BeichenModel, 'price_status' | 'price_conditions' | 'price_conditions_exact' | 'upstream_rates'>

function discoveredOverview(price: PriceSnapshot | null, multiplier: string | null = '0.125'): BeichenOverview {
  keyRate = multiplier ?? '0.125'
  const terms = price?.price_conditions_exact
  const complete = Boolean(terms && (price?.price_status === 'confirmed' || price?.price_status === 'conditional'))
  return {
    datasets: [], comparison: [],
    model_catalog: [{
      model: discoveredModel, school_status: 'not_configured', can_enable: true,
      source: 'both', catalog_state: 'key_listed', credential_ids: ['chosen'], key_count: 1,
      website_locations: complete ? [{
        name: discoveredModel, observed_at: '2026-11-21T13:00:00Z',
        groups: [{ id: 11, name: '首选分组', rate_multiplier: keyRate }],
        has_price: true, price_complete: true, price_conditions_exact: terms ?? undefined,
      }] : [],
      preferred_routes: [{
        school_model: discoveredModel, upstream_model: discoveredModel, provider_id: 'north', credential_id: 'chosen',
        adapter: 'openai_responses', priority: 10, state: 'candidate', effective_multiplier_text: multiplier,
      }],
    }],
    models: price ? [{ ...price, model: discoveredModel, credential_id: 'chosen', mode: 'follow' }] : [],
  }
}

function verifiedGroups(multiplier = keyRate) {
  const key = {
    id: 1, name: '首选 Key', key_prefix: null, status: 'active' as const, enabled: true,
    created_at: null, last_seen: '2026-11-21T13:00:00Z', observed_at: '2026-11-21T13:00:00Z',
    match_status: 'matched' as const, credential_id: 'chosen', group_id: 11, group_name: '首选分组', rate: multiplier,
  }
  return indexWebsiteKeyGroups({
    status: 'current', last_seen: '2026-11-21T13:00:00Z', keys: [key],
  })
}

function withVerifiedRate(data: BeichenOverview, multiplier = keyRate) {
  data.datasets = [{ kind: 'billing', credential_id: 'chosen', group_id: 11, group_rate: multiplier,
    group_status: 'assigned', billing_state: 'current', effective_multiplier: multiplier,
    observed_at: '2026-11-21T13:05:00Z' }]
  data.models = (data.models ?? []).map(row => ({ ...row, price_observed_at: '2026-11-21T13:00:00Z' }))
  return data
}

it('教师页自动发现目录里的新模型，等待当前 Key 的报价并更新学生实价', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  await enter('teacher', '/teacher/models')
  const table = widget.get('[aria-label="北辰模型目录"]')
  expect(table.text()).not.toContain(discoveredModel)
  const before = request.mock.calls.length
  const update = async (snapshot: BeichenOverview) => {
    withVerifiedRate(snapshot)
    request.mockResolvedValueOnce(reply(snapshot))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    return table.get('[data-model="' + discoveredModel + '"]')
  }

  const forOtherKey = discoveredOverview(null)
  forOtherKey.models = [{ credential_id: 'cheap', model: discoveredModel, mode: 'follow', price_status: 'not_listed' }]
  const pending = await update(forOtherKey)
  expect(pending.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
  expect(pending.findAll('[data-testid="model-price-table"]')).toHaveLength(1)

  const unlisted = await update(discoveredOverview({ price_status: 'not_listed', price_conditions: null }))
  expect(unlisted.get('[data-testid="price-missing"]').text()).toContain('缺价')
  expect(unlisted.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')

  const price: BeichenPriceConditions = { billing_mode: 'token', input_price: '8e-6', output_price: '0.000016' }
  const confirmed = await update(discoveredOverview({ price_status: 'confirmed', price_conditions_exact: price }))
  expect(confirmed.get('.current-group-prices').text()).toContain('0.125×')
  expect(visiblePrices(confirmed)).toEqual([
    ['上游基础价', '8', '16', '未列', '未列', '未列'],
    ['学生实价', '110', '220', '—', '—', '—'],
  ])
  const changed = await update(discoveredOverview({
    price_status: 'confirmed', price_conditions_exact: { ...price, input_price: '0.000012' },
  }, '0.25'))
  expect(changed.get('.current-group-prices').text()).toContain('0.25×')
  expect(visiblePrices(changed)).toEqual([
    ['上游基础价', '12', '16', '未列', '未列', '未列'],
    ['学生实价', '330', '440', '—', '—', '—'],
  ])
  expect(request.mock.calls.slice(before).map(([url, init]) => [init?.method ?? 'GET', url.split('?')[0]])).toEqual(
    Array.from({ length: 4 }, () => [
      ['GET', '/api/v1/teacher/providers/north/beichen'],
      ['GET', '/api/v1/teacher/providers/north/beichen/keys'],
    ]).flat(),
  )
  expect(writes).not.toHaveBeenCalled()
})

it.each([
  ['per_request', { per_request_price: '0.3' }, '积分/次 · 每次请求 30', '积分/次 · 每次请求 3.75'],
  ['video', { per_request_price: '3e-1' }, '积分/秒 · 每秒 30', '积分/秒 · 每秒 3.75'],
  ['image', { image_output_price: '0.8', image_input_price: '0.4' },
    '积分/次 · 图像输出 80\n官网原值（单位未知） · 图像输入 0.4', '积分/次 · 图像输出 10'],
] as const)('按 %s 的官网单位保留档位入口与结算价', async (billing_mode, fields, standard, actual) => {
  await enter('teacher', '/teacher/models')
  const data = withVerifiedRate(discoveredOverview({
    price_status: 'confirmed', price_conditions_exact: { billing_mode, ...fields, input_price: '999' },
  }))
  expect(schoolCatalog(data, 'north', verifiedGroups('0.125'))[0].quotes[0])
    .toMatchObject({ status: 'confirmed', standard, actual })
  request.mockResolvedValueOnce(reply(data))
  await button('刷新模型', widget.get('[aria-label="北辰模型目录"]')).trigger('click')
  await flushPromises()
  const row = widget.get('[data-model="' + discoveredModel + '"]')
  expect(row.findAll('[data-price-row="official"] td').map(cell => cell.text()))
    .toEqual(['未列', '未列', '未列', '未列', '未列'])
  await button('查看档位', row).trigger('click')
  const details = page().get('[role="dialog"] .condition-prices')
  const [official, settlement] = details.findAll('dd')
  expect(official.text()).toContain(standard.split('\n')[0])
  expect(settlement.text()).toContain(actual.split('\n')[0])
})

it.each(['pending', 'pending_confirmation'] as const)('兼容 %s，保留待核对的官网价', price_status => {
  const data = discoveredOverview({
    price_status, price_conditions: { billing_mode: 'token', input_price: 0.000008 },
    upstream_rates: { input: '1000000' },
  })
  expect(schoolCatalog(data, 'north')[0].quotes[0]).toMatchObject({
    status: 'pending', statusLabel: '官网单价待核对', standard: '每百万 Token 的积分 · 输入 800',
    actual: null, notes: ['官网原始小数精度未知'],
  })
})

it('旧接口极小报价解析为数字零时待核对，官网精确字符串零照常展示', () => {
  const decoded = JSON.parse('{"billing_mode":"token","input_price":1e-400}') as BeichenPriceConditions
  expect(decoded.input_price).toBe(0)
  const legacy = { price_status: 'confirmed', price_conditions: decoded } as const
  const [row] = schoolCatalog(discoveredOverview(legacy), 'north')
  expect(row.quotes[0]).toMatchObject({
    standard: '每百万 Token 的积分 · 输入 待核对',
    actual: null,
    notes: ['官网原始小数精度未知'],
  })

  const exact = discoveredOverview({ ...legacy, price_conditions_exact: { billing_mode: 'token', input_price: '0' } })
  expect(schoolCatalog(exact, 'north')[0].quotes[0]).toMatchObject({
    standard: '每百万 Token 的积分 · 输入 0',
    actual: '每百万 Token 的积分 · 输入 0',
    notes: [],
  })
})

it('官网或倍率同步失败时显示对应错误，并保留上次读取的官网报价', () => {
  const data = discoveredOverview({
    price_status: 'conditional', price_conditions_exact: { billing_mode: 'per_request', per_request_price: '0.3' },
  })
  data.datasets = [{ kind: 'prices', credential_id: 'chosen', last_error: '官网分组映射缺失', effective_multiplier: null }]
  expect(schoolCatalog(data, 'north')[0].quotes[0]).toMatchObject({
    multiplier: '0.125× · 待启用', status: 'conditional', statusLabel: '上次报价状态 · 按条件计价',
    standard: '积分/次 · 每次请求 30', actual: null, notes: ['价格同步错误：官网分组映射缺失'],
  })
  data.datasets = [{ kind: 'billing', credential_id: 'chosen', last_error: '读取倍率失败', effective_multiplier: 0.125 }]
  expect(schoolCatalog(data, 'north')[0].quotes[0]).toMatchObject({
    multiplier: '待同步', standard: '积分/次 · 每次请求 30', actual: null, notes: ['倍率同步失败：读取倍率失败'],
  })
})

it('缺少官网价时仍可展示后台已有报价，并区分零元与未知缓存价', () => {
  const data = discoveredOverview({
    price_status: 'confirmed', upstream_rates: { input: '0.000000031', output: '0', cache_read: 0, cache_write: null },
  })
  expect(schoolCatalog(data, 'north')[0].quotes[0]).toMatchObject({
    standard: null,
    actual: '每百万 Token 的积分 · 输入 0.0000000000031 · 输出 0 · 缓存读取 未知 · 缓存写入 未知',
    notes: ['官网标准价待同步', '后台已有报价小数精度未知，零值待核对', '实际单价来自后台已有报价'],
  })
})

it('保留分档原值和顺序，实际价只使用单位明确、范围有效的分档', () => {
  const data = discoveredOverview({
    price_status: 'conditional', price_conditions_exact: {
      billing_mode: 'token', intervals: [
        { min_tokens: '9007199254740993', max_tokens: '9007199254740992', input_price: '0.000001' },
        { min_tokens: '10', max_tokens: '20', billing_mode: 'unknown', input_price: '0.000002' },
        { min_tokens: '30', max_tokens: null, input_price: '0.000008' },
      ],
    },
  }, '0')
  const quote = schoolCatalog(data, 'north')[0].quotes[0]
  expect(quote.standard).toContain('上下文分档（Token 下限 9007199254740993，上限 9007199254740992） · 档位无效')
  expect(quote.standard).toContain('官网原值（单位未知） · 输入 0.000002')
  expect(quote.actual).toBe('上下文分档（Token 下限 30，上限 无上限）\n每百万 Token 的积分 · 输入 0')
})

it('分档边界未知时保留条件和标准价，只有适用范围已知的分档显示实际价', () => {
  const data = discoveredOverview({
    price_status: 'conditional', price_conditions_exact: {
      billing_mode: 'token', intervals: [
        { max_tokens: '100', input_price: '0.000001' },
        { min_tokens: '100', input_price: '0.000002' },
        { min_tokens: null, max_tokens: null, input_price: '0.000004' },
        { min_tokens: '300', max_tokens: null, input_price: '0.000008' },
      ],
    },
  })
  const quote = schoolCatalog(data, 'north')[0].quotes[0]
  expect(quote.standard).toBe([
    '上下文分档（Token 下限 未知，上限 100） · 适用范围待核对',
    '每百万 Token 的积分 · 输入 100',
    '上下文分档（Token 下限 100，上限 未知） · 适用范围待核对',
    '每百万 Token 的积分 · 输入 200',
    '上下文分档（Token 下限 未知，上限 无上限） · 适用范围待核对',
    '每百万 Token 的积分 · 输入 400',
    '上下文分档（Token 下限 300，上限 无上限）',
    '每百万 Token 的积分 · 输入 800',
  ].join('\n'))
  expect(quote.actual).toBe('上下文分档（Token 下限 300，上限 无上限）\n每百万 Token 的积分 · 输入 100')
})

it('教师从维护抽屉打开校方工作区，授权提示和网页时间仍来自概览，模型主目录隐藏维护入口', async () => {
  await enter('teacher', '/teacher/providers?from=list#north')
  expect(page().find(workspaceSection).exists()).toBe(false)
  const before = request.mock.calls.length
  await button('查看模型', widget.get('article')).trigger('click')
  await flushPromises()
  const dialog = page().get('[role="dialog"]')
  const link = dialog.get(`${workspaceSection} a`)
  expect(link.text()).toBe('打开校方北辰工作区')
  expect(link.attributes()).toMatchObject({ href: workspaceUrl, target: '_blank', rel: 'noopener noreferrer', referrerpolicy: 'no-referrer' })
  const panel = dialog.get('[aria-label="近期逐笔对账"]')
  expect(panel.text()).not.toContain('渠道')
  const row = { occurred_at: '2026-11-01T16:02:03Z', model: '假数据模型', student_name: '测试学生',
    base_credits: '0', school_multiplier: '1.10', charged_credits: '0', actual_cost: '0.0000', difference_credits: '0', matched: true }
  request.mockResolvedValueOnce(reply({ items: [row, { ...row, student_name: null, actual_cost: null, difference_credits: null, matched: false }], next_cursor: null }))
  await button('近期逐笔对账', panel).trigger('click')
  await flushPromises()
  const overviewPath = '/api/v1/teacher/providers/north/beichen'
  const readPaths = () => request.mock.calls.slice(before).map(([url, init]) => [init?.method ?? 'GET', url.split('?')[0]])
  // 触发点击处理，但让测试浏览器留在本地。
  link.element.addEventListener('click', event => event.preventDefault(), { once: true })
  await link.trigger('click')
  await flushPromises()
  expect(dialog.text()).toContain('北辰官网需要学校后台重新授权。')
  expect(readPaths()).toEqual([
    ['GET', overviewPath], ['GET', overviewPath + '/keys'], ['GET', '/api/v1/teacher/model-usage'],
    ['GET', overviewPath + '/receipts'],
  ])
  expect(panel.findAll('dl > div').map(field => [field.get('dt').text(), field.get('dd').text()])).toEqual([
    ['当前有有效网页报价', '3 / 4 个渠道'], ['成功读取过流水页', '4 / 4 个渠道'], ['最近一次扫描已到末页', '1 / 4 个渠道'],
    ['最近记录报价时间', '2026-11-01 23:30:00'], ['最近一页流水读取成功时间', '2026-11-02 00:02:03'],
  ])
  const [zero, pending] = panel.findAll('tbody tr')
  expect(zero.findAll('td').slice(2).map(cell => cell.text())).toEqual(['0.0000', '0', '已匹配'])
  expect(pending.findAll('td').slice(2).map(cell => cell.text())).toEqual(['待核', '待核', '待核'])
  await panel.get('input').setValue('')
  await panel.get('input').setValue('2026-11-02')
  await flushPromises()
  expect(panel.text()).toContain('学校尚未归档所选日期的北辰流水')
  expect(readPaths().slice(4)).toEqual([['GET', overviewPath + '/receipts']])
  await button('刷新对账', panel).trigger('click')
  await flushPromises()
  expect(readPaths().slice(5)).toEqual([
    ['GET', overviewPath], ['GET', overviewPath + '/keys'], ['GET', overviewPath + '/receipts'],
  ])
  await page().get('button[aria-label="关闭"]').trigger('click')
  const beforeCatalog = request.mock.calls.length
  await widget.get('a[href="/teacher/models"]').trigger('click')
  await flushPromises()
  expect(router.currentRoute.value.path).toBe('/teacher/models')
  expect(widget.get('[aria-label="北辰模型目录"]').text()).toContain('2.125000000000000000000001×')
  expect(widget.text()).not.toMatch(/近期逐笔对账|当前有有效网页报价|最近一次扫描已到末页|隐藏的首选渠道|北辰官网需要学校后台重新授权/)
  expect(widget.find(workspaceSection).exists()).toBe(false)
  expect(widget.find(`a[href="${workspaceUrl}"]`).exists()).toBe(false)
  expect(request.mock.calls.slice(beforeCatalog).some(([url]) => url.includes('/beichen/receipts'))).toBe(false)
  expect(writes).not.toHaveBeenCalled()
})

it.each([
  ['工作区为 null', null],
  ['未配置地址', {}],
  ['地址为 null', { launch_url: null }],
  ['地址为空', { launch_url: '' }],
  ['HTTP 地址', { launch_url: 'http://workspace.example.test/apps/beichen' }],
  ['相对地址', { launch_url: '//workspace.example.test/apps/beichen' }],
  ['非网页链接', { launch_url: 'javascript:void(0)' }],
  ['主机无效', { launch_url: 'https://[invalid' }],
  ['带用户信息', { launch_url: 'https://teacher@workspace.example.test/apps/beichen' }],
  ['带查询参数', { launch_url: workspaceUrl + '?from=school' }],
  ['带片段', { launch_url: workspaceUrl + '#school' }],
  ['学校同主机', { launch_url: `https://${window.location.hostname}:9443/apps/beichen` }],
] as const)('维护抽屉刷新概览后撤下原工作区链接（%s）', async (_label, workspace) => {
  await enter('teacher', '/teacher/providers')
  await button('查看模型', widget.get('article')).trigger('click')
  await flushPromises()
  const dialog = page().get('[role="dialog"]')
  expect(dialog.get(`${workspaceSection} a`).attributes('href')).toBe(workspaceUrl)
  request.mockResolvedValueOnce(reply({ datasets: [], models: [], comparison: [], maintenance_workspace: workspace }))
  await button('刷新模型', dialog).trigger('click')
  await flushPromises()
  expect(dialog.find(`${workspaceSection} a`).exists()).toBe(false)
  expect(dialog.findAll(workspaceSection).map(section => section.text())).toEqual(workspace === null ? [] : ['校方尚未配置远程应用地址'])
})

it.each(['student', 'mentor'] as const)('%s 身份打开教师供应商地址时无法进入对账区或发起教师请求', async identity => {
  await enter(identity, '/teacher/providers')
  expect(router.currentRoute.value.path).not.toBe('/teacher/providers')
  expect(page().find('[aria-label="近期逐笔对账"]').exists()).toBe(false)
  expect(page().find(workspaceSection).exists()).toBe(false)
  expect(request.mock.calls.some(([url]) => url.includes('/api/v1/teacher/'))).toBe(false)
})

it('普通教师用学校邮箱密码登录后回到原供应商页面，首次查看北辰只读取目录', async () => {
  role = 'teacher'
  const pinia = createPinia()
  setActivePinia(pinia)
  request.mockResolvedValueOnce(new Response(null, { status: 401 }))
  await router.push('/login')
  await router.push('/teacher/providers')
  expect(router.currentRoute.value.path).toBe('/login')
  expect(router.currentRoute.value.query.redirect).toBe('/teacher/providers')
  widget = mount(App, { attachTo: document.body, global: { plugins: [pinia, router], stubs: { LoginPlanet: true } } })
  const user = { id: 'fixture-teacher', name: '测试老师', email: 'teacher@example.test', role, status: 'active', created_at: '' }
  request.mockResolvedValueOnce(reply({ user, identities: [role] }))
    .mockResolvedValueOnce(reply({ user, identity: role, access_token: 'fixture-session', token_type: 'Bearer', expires_in: 900 }))
    .mockResolvedValueOnce(reply({ user, identity: role, identities: [role] }))
  const login = widget.get('form')
  await login.get('input[type="email"]').setValue(user.email)
  await login.get('input[type="password"]').setValue('fixture-only-password')
  await login.trigger('submit')
  await flushPromises()
  expect(request.mock.calls.slice(0, 4).map(([url]) => url)).toEqual([
    '/api/v1/auth/refresh', '/api/v1/auth/login', '/api/v1/auth/session', '/api/v1/me',
  ])
  expect(JSON.parse(String(request.mock.calls[2][1]?.body))).toEqual({ identity: role })
  expect(router.currentRoute.value.path).toBe('/teacher/providers')
  const before = request.mock.calls.length
  // 旧版学校后台的概览不含官网同步状态和校方工作区。
  request.mockResolvedValueOnce(reply({ datasets: [], models: [], comparison: [] }))
  await button('查看模型', widget.get('article')).trigger('click')
  await flushPromises()
  expect(request.mock.calls.slice(before).map(([url, init]) => [init?.method ?? 'GET', url.split('?')[0]])).toEqual([
    ['GET', '/api/v1/teacher/providers/north/beichen'],
    ['GET', '/api/v1/teacher/providers/north/beichen/keys'],
    ['GET', '/api/v1/teacher/model-usage'],
  ])
  expect(page().get('[aria-label="北辰模型目录"]').find('input').exists()).toBe(false)
  expect(page().get('[role="dialog"]').find(workspaceSection).exists()).toBe(false)
  const receipts = page().get('[aria-label="近期逐笔对账"]')
  expect(button('近期逐笔对账', receipts).attributes('aria-expanded')).toBe('false')
  expect(receipts.text()).not.toContain('渠道')
  request.mockResolvedValueOnce(reply({ items: [], next_cursor: null }))
  await button('近期逐笔对账', receipts).trigger('click')
  await flushPromises()
  expect(request.mock.calls.slice(before + 3).map(([url, init]) => [init?.method ?? 'GET', url])).toEqual([
    ['GET', '/api/v1/teacher/providers/north/beichen/receipts?day=' + schoolDay()],
  ])
  expect(receipts.text()).toContain('官网同步状态未知')
  expect(receipts.text()).toContain('学校尚未归档所选日期的北辰流水')
})

it('教师查看超用、设置学生额度、从服务器刷新余额，历史月份只读', async () => {
  await enter('teacher', '/teacher/projects')
  await button('学生额度').trigger('click')
  await flushPromises()
  let dialog = page().get('.quota-dialog')
  let member = dialog.get('.quota-member[data-member-id="s"]')
  expect(member.text()).toContain('已超用')
  expect(member.text()).toContain('1')
  await member.get('input').setValue('100')
  failQuota = true
  await member.get('form').trigger('submit')
  await flushPromises()
  member = page().get('.quota-member[data-member-id="s"]')
  expect(member.get('[role="alert"]').text()).toContain('超过项目月额度')
  expect(member.text()).toContain('已超用')
  failQuota = false
  await member.get('form').trigger('submit')
  await flushPromises()
  dialog = page().get('.quota-dialog')
  member = dialog.get('.quota-member[data-member-id="s"]')
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/projects/p/members/s/monthly-credit-quota', { monthly_credits: '100' })
  expect(member.text()).toContain('剩余')
  expect(member.text()).toContain('89')
  expect(writes).toHaveBeenCalledTimes(2)
  const historical = '2021-02'
  request.mockImplementation(async (url, init) => {
    if (String(url).includes('month=' + historical)) return reply({ ...projectUsage(), month: historical })
    return fetchFixture(String(url), init)
  })
  await page().get('.quota-dialog input[type="month"]').setValue(historical)
  await flushPromises()
  dialog = page().get('.quota-dialog')
  expect(dialog.text()).toContain('历史月份只读')
  expect(dialog.findAll('button').some(item => item.text() === '保存')).toBe(false)
  request.mockImplementation(fetchFixture)
})

it('教师从项目监控进入学生模型明细，看到学校已记积分和来自独立回执的北辰金额', async () => {
  quota = '100'
  await enter('teacher', '/teacher/project-spend')
  const summary = widget.get('.student-summary')
  await summary.get('summary').trigger('click')
  await summary.get('tbody button').trigger('click')
  const detail = page().get('[role="dialog"]')
  const model = detail.get('.member-model-table tbody tr')
  expect(model.get('th').text()).toBe('shared')
  expect(model.findAll('td')[0].text()).toBe('11.000')
  expect(model.text()).toContain('北辰实扣 0.09999999999999 余额单位')
  expect(model.text()).toContain('待核 1 笔')
  await model.get('summary').trigger('click')
  expect(model.text()).toContain('基础积分 10 · 入账倍率 1.10×')
  expect(model.text()).toContain('学校已记 11 积分')
  expect(writes).not.toHaveBeenCalled()
})

it('学生从个人用量读取 100、11、89，超额时展示精确超用，北辰回执不重算学校余额', async () => {
  quota = '100'
  await enter('student', '/student/usage')
  expect(widget.get('.usage-summary').text()).toContain('剩余89')
  expect(widget.text()).toContain('学校已用 11')
  expect(widget.get('[data-testid="credit-rate-note"]').text()).toBe('100 积分 = 1 元；金额统一用积分显示。')
  expect(widget.text()).not.toContain('基础积分')
  expect(widget.text()).not.toContain('学校倍率')
  quota = '10.999'
  await widget.get('input[type="month"]').setValue('2021-01')
  await flushPromises()
  expect(widget.get('.usage-summary').text()).toContain('已超用0.001')
  expect(writes).not.toHaveBeenCalled()
})
