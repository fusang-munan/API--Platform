import { DOMWrapper, enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BeichenModelUsage, BeichenOverview, BeichenUsageRange, BeichenUsageTotals } from '../../api/types'
import BeichenUsageDrawer from '../../components/BeichenUsageDrawer.vue'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import componentSource from './BeichenSchoolModels.vue?raw'

const api = vi.hoisted(() => ({
  beichenOverview: vi.fn(), modelUsage: vi.fn(), enableBeichenModelsBatch: vi.fn(), enableBeichenModel: vi.fn(),
  updateModel: vi.fn(), syncBeichen: vi.fn(), setBeichenModelPrice: vi.fn(),
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
vi.mock('../../composables/useToast', () => ({ useToast: () => toast }))
const Dialog = defineComponent({ props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><h2>{{ title }}</h2><slot /></section>' })
const stubs = { AppDialog: Dialog, BeichenRecentReceipts: true }
enableAutoUnmount(afterEach)
type Entry = { model: string; active?: boolean; priced?: boolean; canEnable?: boolean; manual?: boolean }
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
    })),
  }
}
function total(credits = '11', tokens = 1000): BeichenUsageTotals {
  return { credits, tokens, input_tokens: tokens - 400, output_tokens: 200, cache_read_tokens: 100, cache_write_tokens: 100, calls: 4, pending_calls: 1 }
}
function usage(range: BeichenUsageRange = 'all'): BeichenModelUsage {
  return {
    range, start: null, end: '2026-11-20T12:00:00+08:00', timezone: 'Asia/Shanghai', totals: { ...total('33', 2000), students: 2, keys: 2 },
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
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
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
  it('一键启用只处理当前搜索及常用筛选，预览明确列出启用和跳过原因', async () => {
    const entries: Entry[] = [{ model: 'lab/active', active: true }, { model: 'lab/ready' }, { model: 'lab/missing', priced: false }, { model: 'lab/not-ready', canEnable: false }, { model: 'outside/model' }]
    api.beichenOverview.mockResolvedValue(overview(entries))
    api.enableBeichenModelsBatch.mockResolvedValue({ enabled: [{ model: 'lab/ready', model_id: 'school-id' }], skipped: [] })
    const wrapper = view({ query: 'lab/', commonIds: new Set(entries.map(entry => entry.model)) })
    await flushPromises()
    await button(wrapper, '一键启用').trigger('click')
    const dialog = wrapper.get('[role="dialog"][aria-label="一键启用当前筛选模型"]')
    expect(dialog.text()).toContain('将启用 1 个 · 跳过 3 个')
    expect(dialog.text()).toContain('已启用')
    expect(dialog.text()).toContain('官网未报价，请先手动定价')
    expect(dialog.text()).toContain('线路未就绪')
    expect(dialog.text()).not.toContain('outside/model')
    expect(api.enableBeichenModelsBatch).not.toHaveBeenCalled()
    await button(dialog, '确认启用 1 个').trigger('click')
    await flushPromises()
    expect(api.enableBeichenModelsBatch).toHaveBeenCalledExactlyOnceWith('north', ['lab/ready'])
    expect(dialog.text()).toContain('已启用 1 个 · 跳过 3 个 · 失败 0 个')
    expect(toast.success).toHaveBeenCalledWith('已启用 1 个，跳过 3 个，失败 0 个')
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
  })

  it('状态筛选会改变一键启用范围，手动价不再被当作无报价', async () => {
    api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/active', active: true }, { model: 'lab/manual', manual: true }, { model: 'lab/missing', priced: false }]))
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '手动价').trigger('click')
    expect(wrapper.findAll('.model-table tbody tr').map(row => row.attributes('data-model'))).toEqual(['lab/manual'])
    await button(wrapper, '一键启用').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('将启用 1 个 · 跳过 0 个')
    expect(wrapper.get('.model-multiplier').text()).toBe('手动定价')
  })

  it('超过 200 个时分批调用，服务器跳过和失败逐项保留', async () => {
    const entries = Array.from({ length: 201 }, (_, index) => ({ model: 'batch/' + index }))
    api.beichenOverview.mockResolvedValue(overview(entries))
    api.enableBeichenModelsBatch.mockImplementationOnce(async (_provider: string, models: string[]) => ({
      enabled: models.slice(1).map(model => ({ model, model_id: model })), skipped: [{ model: models[0], reason: 'not_ready', message: '后台线路已变化' }],
    })).mockRejectedValueOnce(new Error('服务暂不可用'))
    const wrapper = view()
    await flushPromises()
    await button(wrapper, '一键启用').trigger('click')
    await button(wrapper.get('[role="dialog"]'), '确认启用 201 个').trigger('click')
    await flushPromises()
    expect(api.enableBeichenModelsBatch.mock.calls.map(call => call[1].length)).toEqual([200, 1])
    expect(api.enableBeichenModelsBatch.mock.calls[1]).toEqual(['north', ['batch/200']])
    expect(wrapper.get('[role="dialog"]').text()).toContain('已启用 199 个 · 跳过 1 个 · 失败 1 个')
    expect(wrapper.get('[role="dialog"]').text()).toContain('后台线路已变化')
    expect(toast.error).toHaveBeenCalled()
  })

  it('首次并行读取概览和总计用量，切换范围只读取用量且旧响应不能覆盖新范围', async () => {
    const firstOverview = deferred<BeichenOverview>()
    api.beichenOverview.mockReturnValueOnce(firstOverview.promise)
    const wrapper = view()
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(api.modelUsage).toHaveBeenCalledWith('all', 'north')
    firstOverview.resolve(overview([{ model: 'lab/a' }]))
    await flushPromises()
    const month = deferred<BeichenModelUsage>()
    api.modelUsage.mockReturnValueOnce(month.promise)
    await button(wrapper.get('[aria-label="模型用量时间范围"]'), '本月').trigger('click')
    await button(wrapper.get('[aria-label="模型用量时间范围"]'), '当天').trigger('click')
    await flushPromises()
    month.resolve({ ...usage('month'), totals: total('999') })
    await flushPromises()
    expect(api.modelUsage.mock.calls).toEqual([['all', 'north'], ['month', 'north'], ['day', 'north']])
    expect(api.beichenOverview).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[aria-label="模型与用量概况"]').text()).toContain('当天已扣积分33')
    expect(wrapper.get('[aria-label="模型与用量概况"]').text()).not.toContain('999')
  })

  it('每 60 秒只轮询概览，保留定价与使用抽屉的范围', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const wrapper = view()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.beichenOverview).toHaveBeenCalledTimes(2)
    expect(api.modelUsage).toHaveBeenCalledTimes(1)
    await button(wrapper, '用量总览').trigger('click')
    await button(wrapper.get('[role="dialog"] [aria-label="用量时间范围"]'), '本月').trigger('click')
    await flushPromises()
    expect(wrapper.get('[aria-label="模型用量时间范围"] button[aria-pressed="true"]').text()).toBe('本月')
    expect(api.modelUsage).toHaveBeenLastCalledWith('month', 'north')
  })

  it('禁用必须确认，无报价不可直接启用，手动定价入口始终可见', async () => {
    api.beichenOverview.mockResolvedValue(overview([{ model: 'lab/a', active: true }, { model: 'lab/missing', priced: false }]))
    const wrapper = view()
    await flushPromises()
    const rows = wrapper.findAll('.model-table tbody tr')
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(rows[0], '禁用').trigger('click')
    expect(api.updateModel).not.toHaveBeenCalled()
    await button(rows[0], '禁用').trigger('click')
    await flushPromises()
    expect(api.updateModel).toHaveBeenCalledWith({ model_id: 'lab/a', status: 'inactive' })
    expect(button(rows[1], '启用').element.disabled).toBe(true)
    await button(rows[1], '手动定价').trigger('click')
    expect(wrapper.get('[role="dialog"]').attributes('aria-label')).toBe('lab/missing · 价格设置')
  })

  it('价格按四项结构展示，保留零价与基础价，使用响应式卡片而非横向滚动', async () => {
    const wrapper = view()
    await flushPromises()
    expect(wrapper.findAll('.standard-price .price-grid > div')).toHaveLength(4)
    expect(wrapper.findAll('.actual-price .price-grid > div')).toHaveLength(4)
    expect(wrapper.get('.actual-price').text()).toContain('110 积分')
    expect(wrapper.get('.actual-price').text()).toContain('基础 100 积分')
    expect(wrapper.get('.standard-price').text()).toContain('缓存读取0 积分')
    expect(wrapper.find('[data-layout="responsive-cards"]').exists()).toBe(true)
    expect(wrapper.find('.model-scroll').exists()).toBe(false)
    expect(componentSource).toContain('@media (max-width: 900px)')
    expect(componentSource).not.toContain('overflow-x: auto')
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
    expect(api.modelUsage).toHaveBeenCalledTimes(2)
  })
})

describe('北辰用量抽屉', () => {
  it('单模型入口按学生列出该模型用量，切到 Key 明确恢复全模型合计', async () => {
    const wrapper = mount(BeichenUsageDrawer, { props: { open: true, range: 'all', usage: usage(), initialModel: 'lab/a' }, global: { stubs } })
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('已扣积分11')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('使用学生1')
    expect(wrapper.get('tbody').text()).toContain('学生甲')
    expect(wrapper.get('tbody').text()).not.toContain('学生乙')
    expect(wrapper.get('tbody').text()).toContain('100%')
    expect(wrapper.text()).toContain('待核账 1 笔')
    expect(wrapper.text()).toContain('总 Token = 未缓存输入 + 输出 + 缓存读取 + 缓存写入')
    await button(wrapper, '按 Key').trigger('click')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('已扣积分33')
    expect(wrapper.text()).toContain('全部模型合计')
    expect(wrapper.get('tbody').text()).toContain('学校 Key 甲')
    expect(wrapper.get('select').element.disabled).toBe(true)
  })

  it('按模型可下钻到学生，用量范围通过事件与页面共享', async () => {
    const wrapper = mount(BeichenUsageDrawer, { props: { open: true, range: 'all', usage: usage() }, global: { stubs } })
    await wrapper.get('button[aria-label="查看 lab/a 的学生用量"]').trigger('click')
    expect(wrapper.get('[role="tab"][aria-selected="true"]').text()).toBe('按学生')
    expect(wrapper.get('tbody').text()).toContain('学生甲')
    await button(wrapper.get('[aria-label="用量时间范围"]'), '当天').trigger('click')
    expect(wrapper.emitted('update:range')).toEqual([['day']])
  })

  it('清楚区分加载、错误、空集，并提供重试', async () => {
    const wrapper = mount(BeichenUsageDrawer, { props: { open: true, range: 'all', loading: true }, global: { stubs } })
    expect(wrapper.get('[role="status"]').text()).toContain('正在读取用量')
    await wrapper.setProps({ loading: false, error: '用量暂不可用' })
    await button(wrapper, '重试读取用量').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    await wrapper.setProps({ error: '', usage: { ...usage(), models: [], students: [], keys: [], totals: total('0', 0) } })
    expect(wrapper.text()).toContain('此范围暂无用量')
    expect(wrapper.find('table').exists()).toBe(false)
  })
})
