import { DOMWrapper, enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BeichenBatchEnableResult, BeichenCatalogModel, BeichenModelUsage, BeichenOverview, BeichenRebindPlan, BeichenUsageRange, BeichenUsageTotals } from '../../api/types'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import componentSource from './BeichenSchoolModels.vue?raw'

const api = vi.hoisted(() => ({
  beichenOverview: vi.fn(), modelUsage: vi.fn(), enableBeichenModelsBatch: vi.fn(), enableBeichenModel: vi.fn(),
  updateModel: vi.fn(), syncBeichen: vi.fn(), setBeichenModelPrice: vi.fn(),
  requestBeichenProbes: vi.fn(), beichenProbes: vi.fn(),
  previewBeichenRebind: vi.fn(), applyBeichenRebind: vi.fn(), rescanBeichenCatalog: vi.fn(),
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
vi.mock('../../composables/useToast', () => ({ useToast: () => toast }))
const Dialog = defineComponent({ props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><h2>{{ title }}</h2><slot /></section>' })
const stubs = { AppDialog: Dialog, BeichenRecentReceipts: true }
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
function view(props: { query?: string; commonIds?: Set<string> } = {}) {
  return mount(BeichenSchoolModels, { props: { providerId: 'north', ...props }, global: { stubs } })
}
function button(root: Pick<DOMWrapper<Element>, 'findAll'>, label: string) {
  const target = root.findAll('button').find(node => node.text() === label)
  if (!target) throw new Error('没有找到按钮 ' + label)
  return target
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset(); toast.error.mockReset()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.stubGlobal('confirm', vi.fn(() => true))
  api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/a' }]))
  api.modelUsage.mockImplementation(async (range: BeichenUsageRange) => usage(range))
  api.enableBeichenModelsBatch.mockResolvedValue({ enabled: [], skipped: [] })
  api.enableBeichenModel.mockResolvedValue({ model_id: 'school-id', status: 'active' })
  api.updateModel.mockResolvedValue({})
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('北辰模型管理', () => {
  it('仅网页模型留在独立清单，重扫只传所选 Key 后读回目录', async () => {
    const data: BeichenOverview = {
      ...overview([{ model: 'lab/a' }, { model: 'only-web' }]),
      credentials: [
        { credential_id: 'first', label: '渠道一', status: 'active' },
        { credential_id: 'second', label: '渠道二', status: 'active' },
        { credential_id: 'inactive', label: '已停用渠道', status: 'inactive' },
      ],
      website_only_status: 'compared',
      website_only_models: [{ model: 'only-web', channels: [{ credential_id: 'second', label: '渠道二' }] }],
    }
    api.beichenOverview.mockResolvedValueOnce(data).mockResolvedValue({ ...data, website_only_models: [] })
    api.rescanBeichenCatalog.mockResolvedValue({ synced: true })
    const wrapper = view()
    await flushPromises()
    const web = wrapper.get('[aria-label="仅网页可见"]')
    expect(web.get('li').text()).toContain('only-web · 渠道二')
    expect(web.get('select').text()).not.toContain('已停用渠道')
    expect(wrapper.find('[aria-label="启用 only-web"]').exists()).toBe(false)
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('lab/a')
    expect(dialog.text()).not.toContain('only-web')
    await button(dialog, '取消').trigger('click')
    await web.get('select').setValue('second')
    await web.get('form').trigger('submit')
    await flushPromises()
    expect(api.rescanBeichenCatalog).toHaveBeenCalledExactlyOnceWith('north', 'second')
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
    expect(web.text()).toContain('没有仅网页可见的模型')
    expect(wrapper.find('[aria-label="启用 only-web"]').exists()).toBe(true)
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

  it('展示探活日预算，达到上限后禁用立即探活', async () => {
    const budget = { date: '2026-11-21', used: 500, limit: 500, remaining: 0, exhausted: true }
    api.beichenOverview.mockResolvedValue({
      ...overview([{ model: 'lab/a' }]),
      probe_run: { state: 'completed', started_at: '2026-11-21T00:00:00Z', finished_at: '2026-11-21T00:01:00Z', error_code: null, budget },
    } as never)
    const wrapper = view()
    await flushPromises()

    const text = wrapper.get('[data-testid="probe-budget"]').text()
    expect(text).toContain('500/500')
    expect(text).toContain('剩余 0 次')
    expect(text).toContain('已达上限')
    expect(button(wrapper, '今日已达上限').attributes('disabled')).toBeDefined()
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
    expect(wrapper.get('[data-model="lab/dropped"]').classes()).toContain('binding-issue-row')
    expect(wrapper.get('[data-model="lab/dropped"]').text()).toContain('当前 Key 目录里已找不到它')
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
    expect(wrapper.get('[data-model="lab/tiered"]').text()).toContain('可启用')
    expect(button(wrapper, '立即探活').attributes('disabled')).toBeUndefined()
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
    expect(wrapper.findAll('.model-table tbody tr').map(row => row.attributes('data-model'))).toEqual(['lab/manual'])
    await button(wrapper, '一键启用').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('将启用 1 个 · 跳过 0 个')
    expect(wrapper.get('.actual-price').text()).toContain('手动定价')
    expect(wrapper.get('.actual-price .price-value').text()).toContain('110 积分')
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

  it('每 60 秒只轮询模型概览，模型行打开本月学生用量', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const wrapper = view()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
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
    const [active, missing, blocked] = wrapper.findAll('.model-table tbody tr')
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

  it('官网价和学生实价精确换算积分及元，保留字符串零价与缺项', async () => {
    const data = overview([{ model: 'lab/a' }])
    const [model] = data.models!
    model.price_conditions_exact = {
      billing_mode: 'token', input_price: '0.000001234567890123', output_price: '0.000002', cache_read_price: '0', cache_write_price: null,
    }
    const route = data.model_catalog![0].preferred_routes![0]
    route.effective_multiplier_text = '0.125'
    route.effective_multiplier = '2'
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = view()
    await flushPromises()
    expect(wrapper.findAll('.model-table thead th').map(cell => cell.text())).toEqual(['模型', '状态与覆盖', '官网价', '学生实价', '本月用量', '操作'])
    const amounts = (selector: string) => wrapper.findAll(selector + ' .price-value').map(cell => [
      cell.get('.price-credits').text(), cell.element.querySelector('.price-sub')?.textContent ?? null,
    ])
    expect(amounts('.standard-price')).toEqual([['123.4567890123 积分', '1.234567890123 元'], ['200 积分', '2 元'], ['0 积分', '0 元'], ['—', null]])
    expect(amounts('.actual-price')).toEqual([['16.97530848919125 积分', '0.1697530848919125 元'], ['27.5 积分', '0.275 元'], ['0 积分', '0 元'], ['—', null]])
    expect(wrapper.get('.actual-price .quote-caption').text()).toContain('0.125×')
    expect(componentSource).toContain('@container (max-width: 54rem)')
    expect(componentSource).not.toContain('overflow-x: auto')
  })

  it.each([
    ['per_request', { per_request_price: '0.3' }, '积分/次 · 每次请求 30 积分（0.3 元）', '积分/次 · 每次请求 3 积分（0.03 元）'],
    ['video', { per_request_price: '3e-1' }, '积分/秒 · 每秒 30 积分（0.3 元）', '积分/秒 · 每秒 3 积分（0.03 元）'],
    ['image', { image_output_price: '0.8', image_input_price: '0.4' },
      '积分/次 · 图像输出 80 积分（0.8 元）\n官网原值（单位未知） · 图像输入 0.4', '积分/次 · 图像输出 8 积分（0.08 元）'],
  ] as const)('按 %s 的计费单位换算积分和元', async (billing_mode, fields, official, student) => {
    const data = overview([{ model: 'lab/a' }])
    data.models![0].price_conditions_exact = { billing_mode, ...fields }
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = view()
    await flushPromises()
    expect(wrapper.get('.standard-price .price-line').element.textContent).toBe(official)
    expect(wrapper.get('.actual-price .price-line').element.textContent).toBe(student)
    expect(wrapper.find('.price-grid').exists()).toBe(false)
  })

  it('显示五维状态，重复协议只算一次，新字段为 null 时仍能读旧数据', async () => {
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
    const states = wrapper.findAll('.model-state').map(row => row.text())
    expect(states).toEqual([
      '2/3 把 Key 覆盖 · 2 种协议 · 2 条已发布 · 报价已确认 · 可启用',
      '1/3 把 Key 覆盖 · 1 种协议 · 1 条已发布 · 报价已确认 · 已启用',
      '1/3 把 Key 覆盖 · 0 种协议 · 0 条已发布 · 缺少报价 · 未发布',
    ])
    expect(wrapper.get('.model-state [title]').attributes('title')).toBe('4 条候选线路')
    const empty = wrapper.get('[data-model="lab/empty"]')
    expect(empty.findAll('.price-value').map(cell => cell.text())).toEqual(Array(8).fill('—'))
    expect(wrapper.get('[aria-label="本月用量汇总"]').findAll('dd')[2].text()).toBe('—')
    expect(wrapper.text()).not.toMatch(/undefined|NaN/)
  })

  it('用量失败不影响模型目录，可单独重试', async () => {
    api.modelUsage.mockRejectedValueOnce(new Error('读取失败'))
    const wrapper = view()
    await flushPromises()
    expect(wrapper.findAll('.model-table tbody tr')).toHaveLength(1)
    expect(wrapper.get('.usage-feedback').text()).toContain('读取用量失败')
    await button(wrapper, '重试用量').trigger('click')
    await flushPromises()
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(api.modelUsage.mock.calls).toEqual([['month', 'north'], ['month', 'north']])
  })
})
