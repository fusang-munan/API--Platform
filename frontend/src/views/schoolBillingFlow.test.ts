import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import App from '../App.vue'
import router from '../router'
import { token } from '../api/client'
import { useAuthStore } from '../store/auth'
import type { ProjectUsage, Role } from '../api/types'
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
let failQuota: boolean
let role: Role
let widget: VueWrapper
const writes = vi.fn()
const reply = (data: unknown) => new Response(JSON.stringify({ data }))

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
    throw Error('未预期的写入 ' + path)
  }
  if (path.endsWith('/providers')) return reply([school, other])
  if (path === '/api/v1/teacher/providers/north/beichen/receipts') return reply({ items: [], next_cursor: null })
  if (path.endsWith('/beichen')) return reply({
    web_login_required: true,
    maintenance_workspace: { launch_url: workspaceUrl },
    website_sync: {
      channels: 4, priced_channels: 3, receipt_checked_channels: 4, receipt_caught_up_channels: 1,
      price_last_seen: '2026-11-01T15:30:00Z', receipt_last_checked: '2026-11-01T16:02:03Z',
    },
    datasets: [
      { kind: 'billing', credential_id: 'cheap', effective_multiplier: 0.1 },
      { kind: 'billing', credential_id: 'stale', last_error: '本轮读取失败', effective_multiplier: 0.3 },
    ],
    models: [
      { credential_id: 'chosen', model: 'shared', price_status: 'confirmed', upstream_rates: {
        input: '0.000000031', output: '2', cache_read: '0', cache_write: null,
      } },
      { credential_id: 'cheap', model: 'shared', price_status: 'conditional', price_conditions: { cache_write_1h_price: 0.1 },
        price_conditions_exact: { cache_write_1h_price: '0', cache_write_12h_price: '0.000004', internal_route: '隐藏的渠道',
          intervals: [{ min_tokens: '200000', max_tokens: null, input_price: '0.000008000000000000000019' }],
        } },
    ],
    model_catalog: [
      { model: 'shared', school_model: 'school-alias', school_status: enabled ? 'active' : 'inactive', can_enable: !enabled, preferred_routes: [
        { school_model: 'school-alias', adapter: 'openai_responses', state: enabled ? 'ready' : 'candidate', provider_id: 'north', credential_id: 'chosen', priority: 10,
          effective_multiplier: 2.125, effective_multiplier_text: '2.125000000000000000000001' },
        { school_model: 'school-alias', adapter: 'anthropic', state: 'candidate', provider_id: 'north', credential_id: 'cheap', priority: 1, effective_multiplier: '0.0625' },
      ] },
      { model: 'lab/new-model', school_status: discoveredEnabled ? 'active' : 'not_configured', can_enable: !discoveredEnabled,
        preferred_routes: ['openai_responses', 'openai_compatible'].map(adapter => ({
          school_model: 'lab/new-model', adapter, state: discoveredEnabled ? 'ready' : 'candidate', provider_id: 'north',
          credential_id: 'chosen', effective_multiplier: 0.15,
        })) },
      { model: 'paused', school_status: 'not_configured', can_enable: false, preferred_routes: [
        { school_model: 'paused', adapter: 'anthropic', state: 'candidate', provider_id: 'north', credential_id: 'stale', effective_multiplier: '0.3' },
      ] },
      { model: 'no-price', preferred_routes: [] },
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
  quota = '10'; enabled = true; discoveredEnabled = false; failQuota = false
  writes.mockClear(); request.mockClear()
  vi.stubGlobal('fetch', request)
})
afterEach(() => { widget?.unmount(); token.set(null); vi.unstubAllGlobals() })

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

it('教师在两个入口看到同一份按模型合并的首选报价，可启停已有校内别名，其他供应商仍能维护', async () => {
  await enter('teacher', '/teacher/providers')
  const [north, relay] = widget.findAll('article')
  expect(north.text()).not.toMatch(/渠道|Key|编辑|授权/)
  expect(relay.text()).toContain('备用线路')
  await button('编辑', relay).trigger('click')
  expect(page().get('[role="dialog"]').text()).toContain('替换 Key')
  await page().get('button[aria-label="关闭"]').trigger('click')
  await button('查看模型', north).trigger('click')
  await flushPromises()
  const table = page().get('[aria-label="北辰模型目录"]')
  expect(table.findAll('tbody th').map(item => item.text())).toEqual(['shared', 'lab/new-model', 'paused', 'no-price'])
  const [shared, , paused, historical] = table.findAll('tbody tr')
  expect(shared.text()).toContain('2.125000000000000000000001×')
  expect(table.text()).not.toContain('0.1×')
  expect(shared.text()).toContain('0.0625× · 待启用')
  expect(paused.text()).toContain('暂不可启用')
  expect(paused.text()).not.toContain('0.3×')
  expect(paused.find('button').exists()).toBe(false)
  expect(historical.text()).toContain('启用条件待同步')
  expect(table.text()).toContain('北辰官网需要学校后台重新授权')
  await shared.get('summary').trigger('click')
  expect(shared.get('details').text()).toContain('输入 0.000000000000031 · 输出 0.000002 · 缓存读取 0 · 缓存写入 未知')
  const conditional = shared.findAll('details')[1]
  await conditional.get('summary').trigger('click')
  expect(conditional.text()).toContain('每 Token · 缓存写入（1 小时） 0 · 缓存写入（12 小时） 0.000004')
  expect(conditional.text()).toContain('阶梯（Token 下限 200000，上限 未知） · 每 Token · 输入 0.000008000000000000000019')
  expect(table.text()).not.toMatch(/隐藏的|chosen|cheap|school-alias|OpenAI|Anthropic|协议|路由|渠道/i)
  expect(writes).not.toHaveBeenCalled()
  await button('禁用', shared).trigger('click')
  await flushPromises()
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/models/configuration', { model_id: 'school-alias', status: 'inactive' })
  expect(shared.text()).toContain('2.125000000000000000000001× · 待启用')
  await button('启用', shared).trigger('click')
  await flushPromises()
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/providers/north/beichen/models/enable', { model: 'shared', school_model: 'school-alias' })
  expect(shared.text()).toContain('已启用')
  await page().get('button[aria-label="关闭"]').trigger('click')
  await widget.get('a[href="/teacher/models"]').trigger('click')
  await flushPromises()
  expect(widget.get('[aria-label="北辰模型目录"]').findAll('tbody tr')).toHaveLength(4)
  expect(widget.find('[aria-label="近期逐笔对账"]').exists()).toBe(false)
  expect(widget.find('article.panel').exists()).toBe(false)
})

it('教师从目录直接启用未配置模型，失败后可重试，后台成功并重读目录后才显示已启用', async () => {
  await enter('teacher', '/teacher/models')
  const table = widget.get('[aria-label="北辰模型目录"]')
  const row = table.findAll('tbody tr').find(item => item.get('th').text() === 'lab/new-model')!
  expect(row.text()).toContain('未配置')
  expect(row.text().match(/0\.15× · 待启用/g)).toHaveLength(1)
  expect(row.text()).toContain('单价待同步')
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
  expect(button('启用', row).element.disabled).toBe(true)
  expect(row.text()).not.toContain('已启用')
  finish()
  await flushPromises()
  expect(request.mock.calls.slice(before).map(([url, init]) => [init?.method ?? 'GET', url.split('?')[0]])).toEqual([
    ['POST', '/api/v1/teacher/providers/north/beichen/models/enable'], ['GET', '/api/v1/teacher/providers/north/beichen'],
  ])
  expect(writes).toHaveBeenCalledExactlyOnceWith('/api/v1/teacher/providers/north/beichen/models/enable', { model: 'lab/new-model' })
  expect(row.text()).toContain('已启用')
  expect(row.text()).toContain('0.15×')
  expect(row.text()).not.toMatch(/待启用|未配置|openai|chosen/i)
  expect(button('禁用', row).exists()).toBe(true)
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
  expect(readPaths()).toEqual([['GET', overviewPath], ['GET', overviewPath + '/receipts']])
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
  expect(readPaths().slice(2)).toEqual([['GET', overviewPath + '/receipts']])
  await button('刷新对账', panel).trigger('click')
  await flushPromises()
  expect(readPaths().slice(3)).toEqual([['GET', overviewPath], ['GET', overviewPath + '/receipts']])
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
  ])
  expect(page().get('[aria-label="北辰模型目录"]').find('input').exists()).toBe(false)
  expect(page().get('[role="dialog"]').find(workspaceSection).exists()).toBe(false)
  const receipts = page().get('[aria-label="近期逐笔对账"]')
  expect(button('近期逐笔对账', receipts).attributes('aria-expanded')).toBe('false')
  expect(receipts.text()).not.toContain('渠道')
  request.mockResolvedValueOnce(reply({ items: [], next_cursor: null }))
  await button('近期逐笔对账', receipts).trigger('click')
  await flushPromises()
  expect(request.mock.calls.slice(before + 1).map(([url, init]) => [init?.method ?? 'GET', url])).toEqual([
    ['GET', '/api/v1/teacher/providers/north/beichen/receipts?day=' + schoolDay()],
  ])
  expect(receipts.text()).toContain('官网同步状态未知')
  expect(receipts.text()).toContain('学校尚未归档所选日期的北辰流水')
})

it('教师查看超用、设置学生额度、从服务器刷新余额，历史月份只读', async () => {
  await enter('teacher', '/teacher/projects')
  await button('学生额度').trigger('click')
  await flushPromises()
  let table = page().get('.member-quota-table')
  expect(table.text()).toContain('已超用 1.000')
  await button('设置月额度', table).trigger('click')
  await table.get('input').setValue('100')
  failQuota = true
  await table.get('form').trigger('submit')
  await flushPromises()
  expect(page().get('[role="alert"]').text()).toContain('超过项目月额度')
  expect(table.text()).toContain('已超用 1.000')
  failQuota = false
  await table.get('form').trigger('submit')
  await flushPromises()
  table = page().get('.member-quota-table')
  expect(writes).toHaveBeenLastCalledWith('/api/v1/teacher/projects/p/members/s/monthly-credit-quota', { monthly_credits: '100' })
  expect(table.text()).toContain('剩余 89')
  expect(table.get('tbody tr').findAll('td').slice(0, 2).map(cell => cell.text())).toEqual(['100', '11'])
  expect(writes).toHaveBeenCalledTimes(2)
  const historical = '2021-02'
  request.mockImplementationOnce(async () => reply({ ...projectUsage(), month: historical }))
  await page().get('[role="dialog"] input[type="month"]').setValue(historical)
  await flushPromises()
  expect(page().get('.member-quota-table').text()).not.toContain('设置月额度')
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
  expect(widget.text()).toContain('10 基础积分')
  quota = '10.999'
  await widget.get('input[type="month"]').setValue('2021-01')
  await flushPromises()
  expect(widget.get('.usage-summary').text()).toContain('已超用0.001')
  expect(writes).not.toHaveBeenCalled()
})
