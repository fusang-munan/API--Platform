import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BeichenReconcile as ReconcileData, BeichenReconcileSide, BeichenUsageRange, Provider } from '../../api/types'
import { creditsToYuan } from '../../utils/credits'
import CompareBars from '../../components/CompareBars.vue'
import BeichenReconcile from './BeichenReconcile.vue'
import TeacherProviders from './TeacherProviders.vue'

const mocks = vi.hoisted(() => ({
  reconciliation: vi.fn(),
  providers: vi.fn(),
  charts: [] as Array<{ setOption: ReturnType<typeof vi.fn>; resize: ReturnType<typeof vi.fn>; dispose: ReturnType<typeof vi.fn> }>,
}))
vi.mock('../../api/teacher', () => ({ teacherAPI: {
  beichenReconciliation: mocks.reconciliation,
  providers: mocks.providers,
} }))
vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: vi.fn(() => {
    const chart = { setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn() }
    mocks.charts.push(chart)
    return chart
  }),
}))
vi.mock('echarts/charts', () => ({ BarChart: {}, PieChart: {} }))
vi.mock('echarts/components', () => ({ GridComponent: {}, TooltipComponent: {}, LegendComponent: {}, AriaComponent: {} }))
vi.mock('echarts/renderers', () => ({ SVGRenderer: {} }))

const wrappers: VueWrapper[] = []
let observers: Array<{ observe: ReturnType<typeof vi.fn>; disconnect: ReturnType<typeof vi.fn> }>
type FixtureSide = BeichenReconcileSide & {
  requests: number
  completeness: 'complete' | 'partial' | 'missing'
  tokens_missing: number
  charged_tokens_missing_calls: number
}
type FixtureLine = { local: FixtureSide; upstream: FixtureSide; margin_available: boolean }
type ReconcileFixture = Omit<ReconcileData, 'totals' | 'models' | 'keys'> & {
  coverage: ReconcileData['coverage'] & { comparable_days: number; verified_matched_complete_token_calls: number }
  totals: Pick<FixtureLine, 'local' | 'upstream'> & { money: ReconcileData['totals']['money'] & { margin_available: boolean } }
  models: (ReconcileData['models'][number] & FixtureLine & { credential_id: string; label: string | null })[]
  keys: (ReconcileData['keys'][number] & FixtureLine)[]
}

function side(tokens = 0, credits = '0', overrides: Partial<FixtureSide> = {}): FixtureSide {
  return {
    tokens, credits, input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0,
    requests: 1, completeness: 'complete', tokens_missing: 0, charged_tokens_missing_calls: 0, ...overrides,
  }
}

function absentSide() {
  return side(0, '0', { requests: 0, completeness: 'missing', source: null })
}

function response(range: BeichenUsageRange = 'all'): ReconcileFixture {
  return {
    range, start: range === 'all' ? null : '2026-11-19T16:00:00Z', end: '2026-11-20T10:00:00Z', timezone: 'Asia/Shanghai',
    comparable_only: null, unmatched_upstream: [],
    coverage: {
      comparable: true, eligible_dates: [], excluded_days: [], days_expected: 2, days_present: 2, comparable_days: 1, missing_days: [],
      pending_calls: 3, unpriced_calls: 2, multiplier_mixed: true, coverage_kind: 'full',
      verified_matched_calls: 0, verified_matched_complete_token_calls: 0,
    },
    totals: {
      local: side(3000, '165'), upstream: side(2000, '0', { requests: 4 }),
      money: { student_yuan: '1.65', base_yuan: '1.5', actual_yuan: '1.25', margin_yuan: '0.4', margin_rate: '0.242424', margin_available: true },
    },
    models: [
      {
        model: '同名模型', credential_id: 'channel-1', label: '测试渠道', match_status: 'matched', completeness: 'complete', local: side(2000, '110'), upstream: side(1800),
        student_yuan: '1.1', base_yuan: '1', actual_yuan: '1', margin_yuan: '0.1', margin_available: true,
        local_share: '0.666667', upstream_share: '0.9', school_absorbed_credits: '2.5',
      },
      {
        model: '同名模型', credential_id: 'channel-2', label: '备用渠道', match_status: 'local_only', completeness: 'partial', local: side(1000, '55'), upstream: absentSide(),
        student_yuan: '0.55', base_yuan: '0.5', actual_yuan: null, margin_yuan: null, margin_available: false,
        local_share: '0.333333', upstream_share: '0', school_absorbed_credits: '0',
      },
      {
        model: '北辰独有模型', credential_id: 'channel-1', label: '测试渠道', match_status: 'upstream_only', completeness: 'partial', local: absentSide(), upstream: side(200),
        student_yuan: '0', base_yuan: '0', actual_yuan: '0.25', margin_yuan: '-0.25', margin_available: true,
        local_share: '0', upstream_share: '0.1', school_absorbed_credits: null,
      },
    ],
    keys: [{
      credential_id: 'channel-1', label: '测试渠道', completeness: 'complete', local: side(3000, '165'), upstream: side(2000),
      student_yuan: '1.65', base_yuan: '1.5', actual_yuan: '1.25', margin_yuan: '0.4', margin_available: true, school_absorbed_credits: '2.5',
    }],
  }
}

function emptyResponse(): ReconcileFixture {
  return {
    ...response(), models: [], keys: [],
    totals: {
      local: absentSide(), upstream: absentSide(),
      money: { student_yuan: '0', base_yuan: '0', actual_yuan: null, margin_yuan: null, margin_rate: null, margin_available: false },
    },
    coverage: {
      comparable: false, eligible_dates: [], excluded_days: [], days_expected: 0, days_present: 0, missing_days: [], pending_calls: 0, unpriced_calls: 0, multiplier_mixed: false,
      coverage_kind: 'none', comparable_days: 0, verified_matched_calls: 0, verified_matched_complete_token_calls: 0,
    },
  }
}

function button(view: VueWrapper, text: string) {
  const found = view.findAll('button').find(item => item.text() === text)
  if (!found) throw new Error(`没有找到按钮：${text}`)
  return found
}

function mountView() {
  const view = mount(BeichenReconcile, { props: { providerId: 'beichen' } })
  wrappers.push(view)
  return view
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

it('用公开日账对账时保留 USD 金额、来源和模型覆盖率', async () => {
  const data = response()
  const upstream = side(32598, '0', { requests: 13, unit: 'USD', actual_cost: '0.004670784', source: 'key_usage' })
  data.totals.upstream = upstream
  Object.assign(data.totals.money, { actual_yuan: null, margin_yuan: null, margin_available: false })
  data.keys[0].upstream = upstream
  data.models[0].upstream = { ...upstream, actual_cost: '0.0032' }
  Object.assign(data.coverage, { key_usage_days: 2, key_usage_model_days: 1, stale_key_usage_days: 1 })
  mocks.reconciliation.mockResolvedValueOnce(data)
  const view = mountView()
  await flushPromises()
  const amount = view.get('[data-kpi="actual-yuan"]')
  expect(amount.text()).toContain('0.004670784')
  expect(amount.text()).toContain('USD')
  expect(amount.text()).not.toContain('元')
  expect(amount.attributes('data-source')).toBe('key_usage')
  expect(view.get('.model-details tbody').text()).toContain('0.0032 USD')
  expect(view.get('.key-details tbody').text()).toContain('0.004670784 USD')
  expect(view.get('[aria-label="日账覆盖"]').text()).toContain('1 / 2（50%）')
  expect(button(view, '金额对比').attributes('disabled')).toBeDefined()
})

beforeEach(() => {
  mocks.reconciliation.mockReset().mockImplementation(async (_id: string, range: BeichenUsageRange) => response(range))
  mocks.providers.mockReset().mockResolvedValue([])
  mocks.charts.length = 0
  observers = []
  vi.stubGlobal('ResizeObserver', class {
    observe = vi.fn()
    disconnect = vi.fn()
    constructor() { observers.push(this) }
  })
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
})

afterEach(() => {
  wrappers.splice(0).forEach(view => view.unmount())
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('北辰区间对账', () => {
  it('默认读取总计，显示五项汇总、积分折合金额和按渠道明细', async () => {
    const view = mountView()
    expect(view.get('[role="status"]').text()).toContain('正在读取北辰对账')
    await flushPromises()
    expect(mocks.reconciliation).toHaveBeenCalledWith('beichen', 'all')
    expect(button(view, '总计').attributes('aria-pressed')).toBe('true')
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('3,000')
    const upstream = view.get('[data-kpi="upstream-tokens"]')
    expect(upstream.get('span').text()).toBe('北辰 Token 总量')
    expect(upstream.get('strong').text()).toBe('2,000')
    expect(upstream.get('small').text()).toBe('北辰回报 4 笔调用')
    expect(view.get('[data-kpi="student-yuan"] strong').text()).toBe('1.65')
    expect(view.get('[data-kpi="student-yuan"] small').text()).toContain('165 积分 ÷ 100 = 1.65 元')
    expect(view.get('[data-kpi="actual-yuan"] strong').text()).toBe('1.25')
    expect(view.get('[data-kpi="margin-yuan"] strong').text()).toBe('+0.4')
    expect(view.get('[data-kpi="margin-yuan"] strong').classes()).toContain('positive')
    expect(view.get('.key-details tbody').text()).toContain('测试渠道')
    expect(view.text()).toContain('全部已记录数据')
    expect(view.text()).toContain('基础成本 1.5 元（北辰余额单位）')
    expect(view.get('[aria-label="日账覆盖"]').text()).toContain('差额率 24.2%')
  })

  it('切换当天和本月时重新请求并显示北京时间区间', async () => {
    const view = mountView()
    await flushPromises()
    await button(view, '当天').trigger('click')
    await flushPromises()
    expect(mocks.reconciliation).toHaveBeenLastCalledWith('beichen', 'day')
    expect(button(view, '当天').attributes('aria-pressed')).toBe('true')
    expect(view.text()).toContain('2026-11-20 起')
    await button(view, '本月').trigger('click')
    await flushPromises()
    expect(mocks.reconciliation).toHaveBeenLastCalledWith('beichen', 'month')
    expect(button(view, '本月').attributes('aria-pressed')).toBe('true')
  })

  it('晚到的旧范围响应和错误不能覆盖新的选择', async () => {
    const old = deferred<ReconcileData>()
    mocks.reconciliation.mockReturnValueOnce(old.promise)
    const view = mountView()
    await button(view, '当天').trigger('click')
    await flushPromises()
    old.resolve({ ...response(), totals: { ...response().totals, local: side(9999) } })
    await flushPromises()
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('3,000')
    const lateError = deferred<ReconcileData>()
    mocks.reconciliation.mockReturnValueOnce(lateError.promise)
    await button(view, '本月').trigger('click')
    await button(view, '总计').trigger('click')
    await flushPromises()
    lateError.reject(new Error('过期错误'))
    await flushPromises()
    expect(view.find('[role="alert"]').exists()).toBe(false)
    expect(button(view, '总计').attributes('aria-pressed')).toBe('true')
  })

  it('切换供应商时清除旧数据并请求对应的供应商', async () => {
    const view = mountView()
    await flushPromises()
    const next = deferred<ReconcileData>()
    mocks.reconciliation.mockReturnValueOnce(next.promise)
    await view.setProps({ providerId: 'another' })
    expect(mocks.reconciliation).toHaveBeenLastCalledWith('another', 'all')
    expect(view.find('[data-kpi="local-tokens"]').exists()).toBe(false)
    next.resolve(response())
    await flushPromises()
    expect(view.find('[data-kpi="local-tokens"]').exists()).toBe(true)
  })

  it('同名模型按渠道分行，区分双方和单边记录', async () => {
    const view = mountView()
    await flushPromises()
    const rows = view.findAll('.model-details tbody tr')
    expect(rows.map(row => row.get('.status-pill').text())).toEqual(['两边均有记录', '仅本地有记录', '仅北辰有记录'])
    expect(rows[0].get('th').text()).toBe('同名模型测试渠道')
    expect(rows[1].get('th').text()).toBe('同名模型备用渠道')
    expect(view.getComponent(CompareBars).props('categories').slice(0, 2)).toEqual(['同名模型 · 测试渠道', '同名模型 · 备用渠道'])
    expect(view.text()).not.toContain('已匹配')
    expect(view.text()).toContain('可逐笔关联 0 笔')
    expect(rows[0].classes()).not.toContain('unmatched-row')
    expect(rows[1].classes()).toContain('unmatched-row')
    expect(rows[1].get('.status-pill').classes()).toContain('local_only')
    expect(rows[2].get('.status-pill').classes()).toContain('upstream_only')
    expect(rows[0].get('[data-label="记录情况"]').text()).toContain('Token 回报完整')
    expect(rows[1].get('[data-label="记录情况"]').text()).toContain('Token 部分回报')
    expect(rows[0].get('.positive').text()).toBe('+0.1')
    expect(rows[2].get('.negative').text()).toBe('-0.25')
  })

  it('列出全部缺失日期，区间差额不可比，逐笔关联单列', async () => {
    const result = response()
    Object.assign(result.coverage, {
      days_expected: 12, days_present: 2, coverage_kind: 'partial',
      missing_days: Array.from({ length: 10 }, (_, index) => `2026-11-${index + 10}`),
      verified_matched_calls: 7, verified_matched_complete_token_calls: 5,
    })
    result.totals.local.requests = 8
    result.totals.money.margin_available = false
    result.models.forEach(row => { row.margin_available = false })
    result.keys[0].margin_available = false
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    const card = view.get('[aria-label="日账覆盖"]')
    const [completeDays, channelDays] = card.findAll('dl > div')
    expect(completeDays.get('dt').text()).toBe('完整账日')
    expect(completeDays.get('dd').text()).toContain('1 天')
    expect(channelDays.get('dt').text()).toBe('渠道账日')
    expect(channelDays.get('dd').text()).toContain('2 / 12')
    const coverage = card.text()
    expect(coverage).toContain('部分同步')
    expect(coverage).toContain('2026-11-10')
    expect(coverage).toContain('2026-11-19')
    expect(coverage).not.toContain('可逐笔关联')
    expect(coverage).toContain('当前区间差额不可比')
    expect(coverage).not.toContain('%')
    expect(coverage).not.toContain('差额率')
    expect(coverage).toContain('不能统一按 1.10 倍推算差额')
    const calls = view.get('[aria-label="调用核对"]').text()
    expect(calls).toContain('可逐笔关联 7 笔，其中本地 Token 完整 5 笔')
    expect(calls).toContain('待核账请求 3 笔')
    expect(calls).toContain('无报价调用 2 笔')
    expect(view.get('[data-kpi="margin-yuan"] strong').text()).toBe('不可比')
    expect(view.findAll('[data-label="差额"]').map(cell => cell.text())).toEqual(Array(4).fill('不可比'))
  })

  it('在柱状图下方按模型显示两侧的 Token 百分比和差额', async () => {
    const view = mountView()
    await flushPromises()
    const donuts = view.findAllComponents({ name: 'CreditDonut' })
    expect(donuts).toHaveLength(2)
    expect(donuts[0].props('segments')[0].displayValue).toBe('66.7%')
    expect(donuts[1].props('segments')[0].displayValue).toBe('90%')
    const table = view.get('table.bars-data')
    expect(table.element.parentElement?.classList.contains('sr-only')).toBe(true)
    expect(table.get('caption').text()).toContain('Token')
    expect(table.findAll('tbody tr')).toHaveLength(3)
    expect(table.findAll('tbody tr')[0].text()).toContain('2,000')
    const [first, , third] = view.findAll('.model-details tbody tr')
    expect(first.get('[data-label="本地占比"].numeric-cell').text()).toBe('66.7%')
    expect(first.get('[data-label="北辰占比"].numeric-cell').text()).toBe('90%')
    expect(third.get('[data-label="本地占比"].numeric-cell').text()).toBe('—')
    expect(third.get('[data-label="北辰占比"].numeric-cell').text()).toBe('10%')
    expect(first.get('[data-label="差额"].numeric-cell').text()).toBe('+0.1')
  })

  it('学校承担区分超限金额、未触顶和历史未统计', async () => {
    const result = response()
    result.models[0].school_absorbed_credits = '0.000000000001'
    result.keys[0].school_absorbed_credits = result.models[0].school_absorbed_credits
    result.models[2] = { ...result.models[2], match_status: 'matched', local: side(200, '25') }
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    expect(view.get('.model-details thead').text()).toContain('学校承担')
    expect(view.get('.key-details thead').text()).toContain('学校承担')
    expect(view.findAll('.model-details [data-label="学校承担"].numeric-cell').map(cell => cell.text())).toEqual(['0.00000000000001', '未触顶', '未统计'])
    expect(view.get('.key-details [data-label="学校承担"].numeric-cell').text()).toBe('0.00000000000001')
    expect(view.text()).toContain('学校承担只统计超过学生额度上限的部分')
  })

  it('金额切换使用元，不重复将后端金额除以一百', async () => {
    const view = mountView()
    await flushPromises()
    expect(view.getComponent(CompareBars).props('left')).toEqual([2000, 1000, null])
    expect(view.getComponent(CompareBars).props('right')).toEqual([1800, null, 200])
    await button(view, '金额对比').trigger('click')
    const bars = view.getComponent(CompareBars)
    expect(bars.props('unit')).toBe('元（北辰余额单位）')
    expect(bars.props('left')).toEqual([1.1, 0.55, 0])
    expect(bars.props('right')).toEqual([1, null, 0.25])
    expect(view.get('table.bars-data caption').text()).toContain('学生已扣与北辰实扣')
    expect(view.get('table.bars-data tbody').text()).toContain('0.25')
    await button(view, 'Token 对比').trigger('click')
    expect(bars.props('unit')).toBe('Token')
    expect(mocks.reconciliation).toHaveBeenCalledTimes(1)
  })

  it('两边均有记录时仍区分真实零 Token 和已扣款但缺项', async () => {
    const result = response()
    result.models[0].local = side()
    result.models[0].upstream = side()
    const incomplete = side(0, '55', { completeness: 'missing', charged_tokens_missing_calls: 1, tokens_missing: 37 })
    result.models[1] = { ...result.models[1], match_status: 'matched', local: incomplete, upstream: side(100) }
    result.totals.local = { ...incomplete, requests: 2, completeness: 'partial' }
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    expect(view.getComponent(CompareBars).props('left')).toEqual([0, null, null])
    expect(view.getComponent(CompareBars).props('right')).toEqual([0, 100, 200])
    const [reported, missing] = view.findAll('.model-details tbody tr')
    expect(reported.get('[data-label="本地 Token"]').text()).toBe('0')
    expect(missing.get('[data-label="本地 Token"]').text()).toBe('—')
    expect(missing.get('[data-label="记录情况"]').text()).toContain('已扣款但 Token 不完整 1 笔')
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('—')
    expect(view.get('[aria-label="调用核对"]').text()).toContain('已计入本地总量、待归类 37 Token')
  })

  it('407,505 个待归类 Token 已含在 598,363 中，待核账请求单独计数', async () => {
    const result = response()
    result.totals.local = side(598363, '0', {
      input_tokens: 190858, tokens_missing: 407505, completeness: 'partial',
    })
    result.coverage.pending_calls = 15
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('598,363')
    const calls = view.get('[aria-label="调用核对"]')
    expect(calls.get('p').text()).toBe('已计入本地总量、待归类 407,505 Token。')
    expect(calls.get('.coverage-facts').text()).toContain('待核账请求 15 笔')
  })

  it('按所选账日同时切换汇总、模型和未关联流水，并保留未知与零', async () => {
    const result = response()
    const dates = ['2026-11-19']
    result.coverage.eligible_dates = dates
    result.comparable_only = {
      coverage: { ...result.coverage, eligible_dates: dates, pending_calls: 0 },
      totals: { ...result.totals, local: side(150) }, models: result.models.slice(0, 1), keys: result.keys,
    }
    const receipt = {
      upstream_user_id: '1', row_id: '9', occurred_at: '2026-11-19T23:59:59+08:00', model: '原模型',
      credential_id: 'channel-1', label: '测试渠道', input_tokens: 123, output_tokens: null,
      cache_read_tokens: 0, cache_write_tokens: null, request_id: 'client:123', receipt_id: null,
    }
    result.unmatched_upstream = [receipt, { ...receipt, row_id: '10', occurred_at: '2026-11-20T00:00:00+08:00' }]
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    const card = view.get('[aria-label="北辰有、本地无"]')
    expect(card.findAll('tbody tr')).toHaveLength(2)
    await button(view, '仅完整可比账日').trigger('click')
    expect(card.findAll('tbody tr')).toHaveLength(1)
    expect(card.get('tbody').text()).toContain('2026-11-19 23:59:59')
    expect(card.get('[data-label="输出"]').text()).toBe('—')
    expect(card.get('[data-label="缓存读"]').text()).toBe('0')
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('150')
    expect(view.findAll('.model-details tbody tr')).toHaveLength(1)
    expect(view.get('[aria-label="调用核对"]').text()).toContain('待核账请求 0 笔')
    await button(view, '全部已观察').trigger('click')
    expect(card.findAll('tbody tr')).toHaveLength(2)
  })

  it('严格视图没有入选日期时显示不可比', async () => {
    const result = response()
    result.coverage.excluded_days = [{ date: '2026-11-20', reasons: ['open_day', 'incompatible_unit'] }]
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    await button(view, '仅完整可比账日').trigger('click')
    expect(view.text()).toContain('当前范围无完整可比账日，差额不可比')
    expect(view.get('details').text()).toContain('2026-11-20 · 账日尚未结束、金额单位不可比')
    expect(view.find('[data-kpi="margin-yuan"]').exists()).toBe(false)
  })

  it('小于一积分的扣费正确换算为元，负差额使用红色', async () => {
    const result = response()
    result.totals.local.credits = '0.5'
    result.totals.money = { student_yuan: '0.005', base_yuan: '0.004545', actual_yuan: '0.006', margin_yuan: '-0.001', margin_rate: '-0.2', margin_available: true }
    mocks.reconciliation.mockResolvedValueOnce(result)
    const view = mountView()
    await flushPromises()
    expect(creditsToYuan('0.5')).toBe('0.005')
    expect(view.get('[data-kpi="student-yuan"] strong').text()).toBe('0.005')
    expect(view.get('[data-kpi="student-yuan"] small').text()).toContain('0.5 积分 ÷ 100 = 0.005 元')
    expect(view.get('[data-kpi="margin-yuan"] strong').text()).toBe('-0.001')
    expect(view.get('[data-kpi="margin-yuan"] strong').classes()).toContain('negative')
  })

  it('读取失败给出重试，重试成功后清除错误', async () => {
    mocks.reconciliation.mockRejectedValueOnce(new Error('测试失败'))
    const view = mountView()
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toContain('北辰对账读取失败')
    expect(view.find('[data-kpi="local-tokens"]').exists()).toBe(false)
    await button(view, '重试').trigger('click')
    await flushPromises()
    expect(view.find('[role="alert"]').exists()).toBe(false)
    expect(view.find('[data-kpi="local-tokens"]').exists()).toBe(true)
  })

  it('空记录给出先同步后刷新的指引，未知金额保留空值', async () => {
    mocks.reconciliation.mockResolvedValueOnce(emptyResponse())
    const view = mountView()
    await flushPromises()
    expect(view.text()).toContain('当前范围暂无对账记录')
    expect(view.get('[role="status"]').text()).toContain('先到「查看模型」同步北辰数据，完成后返回并点击「刷新对账」')
    expect(view.get('[data-kpi="local-tokens"] strong').text()).toBe('0')
    expect(view.get('[data-kpi="margin-yuan"] strong').text()).toBe('不可比')
    expect(view.text()).toContain('暂无已同步账日')
    expect(mocks.charts).toHaveLength(0)
  })
})

describe('分组柱状图', () => {
  it('使用 SVG 与受限提示框，响应 resize 并在卸载时释放图表', async () => {
    const remove = vi.spyOn(window, 'removeEventListener')
    const view = mount(CompareBars, { props: { categories: ['模型一'], left: [1234], right: [1200], leftLabel: '本地', rightLabel: '北辰', unit: 'Token' } })
    wrappers.push(view)
    const chart = mocks.charts[0]
    expect(chart.setOption).toHaveBeenCalledWith(expect.objectContaining({
      aria: expect.objectContaining({ enabled: true }),
      tooltip: expect.objectContaining({ confine: true }),
      series: [expect.objectContaining({ type: 'bar', name: '本地', data: [1234] }), expect.objectContaining({ type: 'bar', name: '北辰', data: [1200] })],
    }), true)
    const before = chart.resize.mock.calls.length
    window.dispatchEvent(new Event('resize'))
    expect(chart.resize).toHaveBeenCalledTimes(before + 1)
    await view.setProps({ unit: '元', left: [0.005], valueFormatter: (value: number) => String(value) })
    expect(view.get('table caption').text()).toContain('元')
    expect(view.get('tbody td').text()).toBe('0.005')
    view.unmount()
    wrappers.splice(wrappers.indexOf(view), 1)
    expect(chart.dispose).toHaveBeenCalledOnce()
    expect(observers[0].disconnect).toHaveBeenCalledOnce()
    expect(remove).toHaveBeenCalledWith('resize', expect.any(Function))
  })

  it('空数据不初始化图表，有数据后创建图表且缺失值不显示为零', async () => {
    const view = mount(CompareBars, { props: { categories: [], left: [], right: [], leftLabel: '本地', rightLabel: '北辰' } })
    wrappers.push(view)
    expect(view.get('[role="status"]').text()).toContain('暂无可比较的数据')
    expect(mocks.charts).toHaveLength(0)
    await view.setProps({ categories: ['缺失模型'], left: [null], right: [10] })
    await flushPromises()
    expect(mocks.charts).toHaveLength(1)
    expect(view.findAll('tbody td').map(cell => cell.text())).toEqual(['—', '10'])
    await view.setProps({ categories: [], left: [], right: [] })
    await flushPromises()
    expect(mocks.charts[0].dispose).toHaveBeenCalledOnce()
  })
})

describe('供应商行集成', () => {
  const provider: Provider = {
    id: 'beichen', name: '北辰', base_url: 'https://api.beichenapi.com', status: 'active', credential_configured: true, created_at: '', updated_at: '',
    credentials: [
      { id: 'channel-1', provider_id: 'beichen', label: '测试一', status: 'active', credential_configured: true, created_at: '', updated_at: '' },
      { id: 'channel-2', provider_id: 'beichen', label: '测试二', status: 'inactive', credential_configured: true, created_at: '', updated_at: '' },
    ],
    last_catalog_sync: { provider_id: 'beichen', provider_name: '北辰', synced: 12, synced_at: '2026-11-20T09:00:00Z' },
  }
  function mountProviders() {
    const view = mount(TeacherProviders, { global: { stubs: {
      AppDialog: { props: ['open'], template: '<div><slot v-if="open" /></div>' },
      BeichenSchoolModels: { template: '<p>模型面板</p>' },
    } } })
    wrappers.push(view)
    return view
  }

  it('概览复用供应商字段，对账按需展开，查看模型入口仍可使用', async () => {
    mocks.providers.mockResolvedValueOnce([provider])
    const view = mountProviders()
    await flushPromises()
    expect(view.get('[aria-label="北辰供应商概览"]').text()).toContain('Key 数 2')
    expect(view.get('[aria-label="北辰供应商概览"]').text()).toContain('启用 Key 1')
    expect(view.get('[aria-label="北辰供应商概览"]').text()).toContain('17:00')
    expect(mocks.reconciliation).not.toHaveBeenCalled()
    await button(view, '北辰对账').trigger('click')
    await flushPromises()
    expect(button(view, '北辰对账').attributes('aria-expanded')).toBe('true')
    expect(mocks.reconciliation).toHaveBeenCalledWith('beichen', 'all')
    expect(view.get('.provider-reconciliation').findComponent(BeichenReconcile).exists()).toBe(true)
    await button(view, '查看模型').trigger('click')
    expect(view.text()).toContain('模型面板')
    await button(view, '北辰对账').trigger('click')
    expect(view.findComponent(BeichenReconcile).exists()).toBe(false)
  })

  it('缺少供应商概览数据时显示未知，不伪造零或报价覆盖率', async () => {
    mocks.providers.mockResolvedValueOnce([{ ...provider, credentials: undefined, last_catalog_sync: undefined }])
    const view = mountProviders()
    await flushPromises()
    const text = view.get('[aria-label="北辰供应商概览"]').text()
    expect(text).toContain('Key 数 —')
    expect(text).toContain('启用 Key —')
    expect(text).toContain('最近目录同步 —')
    expect(mocks.reconciliation).not.toHaveBeenCalled()
  })
})
