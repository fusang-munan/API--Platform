import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { StudentCallReceipt, StudentUsageMetrics, StudentUsageModel, StudentUsageReport } from '../../utils/studentUsage'
import StudentUsage from './StudentUsage.vue'

const blankMetrics: StudentUsageMetrics = {
  tokens: { input: null, output: null, cache_read: null, cache_write: null, total: null },
  completeness: 'unknown',
  token_coverage: { calls: 0, known_calls: 0, complete_calls: 0, unclassified_tokens: null },
  cache_hit_rate: null,
  cache_hit_coverage: { sample_calls: 0, cache_read_tokens: null, input_with_cache_read_tokens: null },
  pending_review_calls: 0,
  pending_review_credits: '0.000',
}

const firstModelMetrics: StudentUsageMetrics = {
  ...blankMetrics,
  tokens: { input: '70', output: '40', cache_read: '20', cache_write: '10', total: '140' },
  completeness: 'complete',
  token_coverage: { calls: 1, known_calls: 1, complete_calls: 1, unclassified_tokens: '0' },
  cache_hit_rate: 20 / 90,
  cache_hit_coverage: { sample_calls: 1, cache_read_tokens: '20', input_with_cache_read_tokens: '90' },
}
const firstModel: StudentUsageModel = {
  ...firstModelMetrics,
  id: 'text',
  name: '文本模型',
  credits: '0.00040000000000001',
  calls: 1,
}
const secondModelMetrics: StudentUsageMetrics = {
  ...firstModelMetrics,
  tokens: { input: '30', output: '10', cache_read: '40', cache_write: '20', total: '100' },
  cache_hit_rate: 40 / 70,
  cache_hit_coverage: { sample_calls: 1, cache_read_tokens: '40', input_with_cache_read_tokens: '70' },
}
const secondModel: StudentUsageModel = {
  ...firstModel,
  ...secondModelMetrics,
  credits: '0.00040000000000002',
}
const mergedModelMetrics: StudentUsageMetrics = {
  ...firstModelMetrics,
  tokens: { input: '100', output: '50', cache_read: '60', cache_write: '30', total: '240' },
  token_coverage: { calls: 2, known_calls: 2, complete_calls: 2, unclassified_tokens: '0' },
  cache_hit_rate: 0.375,
  cache_hit_coverage: { sample_calls: 2, cache_read_tokens: '60', input_with_cache_read_tokens: '160' },
}
const mergedModel: StudentUsageModel = {
  ...firstModel,
  ...mergedModelMetrics,
  credits: '0.00080000000000003',
  calls: 2,
}
const pendingModel: StudentUsageModel = {
  ...blankMetrics,
  id: 'review',
  name: '待核模型',
  credits: '0.000',
  tokens: { input: null, output: '4', cache_read: null, cache_write: null, total: '130' },
  completeness: 'partial',
  token_coverage: { calls: 1, known_calls: 1, complete_calls: 0, unclassified_tokens: '126' },
  pending_review_calls: 1,
  pending_review_credits: '10.000',
}
const zeroModelMetrics: StudentUsageMetrics = {
  ...blankMetrics,
  token_coverage: { calls: 1, known_calls: 0, complete_calls: 0, unclassified_tokens: null },
}
const zeroModel: StudentUsageModel = {
  ...zeroModelMetrics,
  id: 'archive',
  name: '历史模型',
  credits: '0.000',
  calls: 1,
}
const firstKeyMetrics: StudentUsageMetrics = {
  ...firstModelMetrics,
  completeness: 'partial',
  tokens: { ...firstModel.tokens, output: '44', total: '270' },
  token_coverage: { calls: 2, known_calls: 2, complete_calls: 1, unclassified_tokens: '126' },
  pending_review_calls: 1,
  pending_review_credits: pendingModel.pending_review_credits,
}

const currentUsage: StudentUsageReport = {
  month: '2026-11',
  models: [mergedModel, pendingModel, zeroModel],
  totals: {
    ...mergedModelMetrics,
    credits: mergedModel.credits,
    calls: 3,
    tokens: { ...mergedModel.tokens, output: '54', total: '370' },
    completeness: 'partial',
    token_coverage: { calls: 4, known_calls: 3, complete_calls: 2, unclassified_tokens: '126' },
    pending_review_calls: 1,
    pending_review_credits: pendingModel.pending_review_credits,
  },
  projects: [
    {
      ...firstKeyMetrics,
      project_id: 'project-a', project_name: '项目一', quota: '100.000',
      used: firstModel.credits, available: '99.62459999999999999',
      pending: '0.375', in_flight_credits: '0.375', calls: 1, pending_review: true,
      keys: [
        {
          ...firstKeyMetrics,
          id: 'key-a', name: '开发 Key', status: 'active', used: firstModel.credits,
          pending: '0.250', in_flight_credits: '0.250', available: '49.74959999999999999',
          calls: 1, pending_calls: 1, pending_review: true, models: [firstModel, pendingModel],
        },
        {
          ...blankMetrics,
          id: 'key-idle', name: '在途 Key', status: 'active', used: '0.000',
          pending: '0.125', in_flight_credits: '0.125', available: '0.875',
          calls: 0, pending_calls: 0, models: [],
        },
      ],
    },
    {
      ...secondModelMetrics,
      project_id: 'project-b', project_name: '项目二', quota: '100.000',
      used: secondModel.credits, available: '99.99959999999999998',
      pending: '0.000', in_flight_credits: '0.000', calls: 2,
      completeness: 'partial',
      token_coverage: { calls: 2, known_calls: 1, complete_calls: 1, unclassified_tokens: '0' },
      keys: [
        {
          ...secondModelMetrics,
          id: 'key-b', name: '测试 Key', status: 'active', used: secondModel.credits,
          pending: '0.000', in_flight_credits: '0.000', available: '49.99959999999999998',
          calls: 1, pending_calls: 0, models: [secondModel],
        },
        {
          ...zeroModelMetrics,
          id: 'key-inactive', name: '停用的 Key', status: 'inactive', used: '0.000',
          pending: '0.000', in_flight_credits: '0.000',
          calls: 1, pending_calls: 0, models: [zeroModel],
        },
      ],
    },
  ],
}

const callReceipt = {
  request_id: 'req+school?11#42',
  model_id: 'text',
  model_name: '文本模型',
  channel: '北辰',
  billing_state: 'charged',
  created_at: '2026-10-31T16:08:10Z',
  charged_credits: '0.00080000000000003',
  receipt_applied: true,
  usage_state: 'partial',
  usage_reason: 'cache_write_subtotal_only',
  input_tokens: '9007199254740993',
  output_tokens: '3',
  cache_read_tokens: '0',
  cache_write_tokens: null,
  total_tokens: '9007199254740996',
} satisfies StudentCallReceipt

const request = vi.fn<typeof fetch>()
let wrapper: VueWrapper

function response(data: unknown) {
  return new Response(JSON.stringify({ data, request_id: 'test-http-response' }))
}

function failure(status = 503) {
  const code = status === 404 ? 'RESOURCE_NOT_FOUND' : 'DEPENDENCY_UNAVAILABLE'
  return new Response(JSON.stringify({ error: { code } }), { status })
}

function emptyUsage(month: string): StudentUsageReport {
  return { month, projects: [], models: [], totals: { ...blankMetrics, credits: '0.000', calls: 0 } }
}

function mountUsage(data?: StudentUsageReport) {
  if (data) request.mockResolvedValueOnce(response(data))
  wrapper = mount(StudentUsage, {
    attachTo: document.body,
    global: {
      stubs: {
        CreditDonut: true,
        RouterLink: { props: ['to'], template: '<a :href="to.name"><slot /></a>' },
      },
    },
  })
  return wrapper
}

function stat(name: string) {
  return wrapper.get(`[data-testid="usage-total-${name}"] strong`).text()
}

function dialog() {
  return new DOMWrapper(document.body).get('[role="dialog"]')
}

async function openReceipt() {
  await wrapper.get('[data-testid="open-receipt"]').trigger('click')
  await flushPromises()
  return dialog()
}

function pendingResponse() {
  let resolve!: (value: Response) => void
  const promise = new Promise<Response>(done => { resolve = done })
  return { promise, resolve }
}

describe('StudentUsage', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-01T00:10:00+08:00'))
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    request.mockReset()
    vi.stubGlobal('fetch', request)
  })

  afterEach(() => {
    wrapper?.unmount()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('renders exact server rollups across projects and Keys and shows real coverage and settlement states', async () => {
    mountUsage(currentUsage)
    await flushPromises()

    expect(request.mock.calls.map(([url]) => url)).toEqual(['/api/v1/student/usage?month=2026-11'])
    expect(wrapper.get('h1').text()).toBe('2026 年 11 月个人用量')
    expect(wrapper.get('input[type="month"]').attributes('max')).toBe('2026-11')
    expect(wrapper.find('.range-switch').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('最近调用')
    expect(stat('quota')).toBe('200')
    expect(stat('used')).toBe('0.00080000000000003')
    expect(stat('pending')).toBe('0.375')
    expect(stat('available')).toBe('199.62419999999999997')
    expect(wrapper.get('a[href="student-models"]').text()).toBe('到模型广场添加模型')

    const table = wrapper.get('[data-testid="student-model-summary"]')
    const rows = table.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].get('th').text()).toContain('文本模型')
    expect(rows[0].findAll('td').slice(0, 3).map(cell => cell.text())).toEqual(['0.00080000000000003', '2', '240'])
    expect(rows[0].get('td[data-label="缓存命中率"]').text()).toContain('37.5%')
    expect(rows[0].get('td[data-label="缓存命中率"] small').text()).toBe('覆盖 2/2 笔')
    expect(rows[1].get('td[data-label="Token"]').text()).toBe('130')
    expect(rows[1].get('td[data-label="调用次数"]').text()).toBe('0')
    expect(rows[1].get('td[data-label="说明"]').text()).toContain('部分回报（0/1 笔完整）')
    expect(rows[1].get('td[data-label="说明"]').text()).toContain('未分类 126 Token')
    expect(rows[1].get('td[data-label="说明"]').text()).toContain('待核 1 笔（预留 10 积分）')
    expect(rows[1].get('td[data-label="缓存命中率"]').text()).toContain('—')
    expect(rows[1].get('td[data-label="缓存命中率"] small').text()).toBe('覆盖 0/1 笔')
    expect(rows[1].text()).not.toContain('零额已结算')
    expect(rows[2].get('td[data-label="Token"]').text()).toBe('—')
    expect(rows[2].get('td[data-label="说明"]').text()).toBe('无回报 · 零额已结算')
    const total = table.get('tfoot tr')
    expect(total.get('td[data-label="已扣积分"]').text()).toBe('0.00080000000000003')
    expect(total.get('td[data-label="调用次数"]').text()).toBe('3')
    expect(total.get('td[data-label="Token"]').text()).toBe('370')
    expect(total.get('td[data-label="缓存命中率"] small').text()).toBe('覆盖 2/4 笔')
  })

  it('keeps idle and inactive Keys visible and expands model details independently for each project and Key', async () => {
    mountUsage(currentUsage)
    await flushPromises()

    const keys = wrapper.findAll('[data-testid="usage-key-row"]')
    expect(keys).toHaveLength(4)
    expect(keys[0].findAll('.usage-key-credits dd').map(cell => cell.text())).toEqual([
      '0.00040000000000001', '0.25', '49.74959999999999999',
    ])
    expect(keys[0].findAll('.usage-key-metrics dd').map(cell => cell.text())).toEqual(['1', '270', '2'])
    expect(keys[0].text()).toContain('待核 1 笔')
    expect(keys[1].text()).toContain('暂无模型记录')
    expect(keys[1].findAll('.usage-key-credits dd').map(cell => cell.text())).toEqual(['0', '0.125', '0.875'])
    expect(keys[1].find('button').exists()).toBe(false)
    expect(keys[3].text()).toContain('已停用')
    expect(keys[3].findAll('.usage-key-credits dd').map(cell => cell.text())).toEqual(['0', '0', '—'])
    expect(keys[3].findAll('.usage-key-metrics dd').map(cell => cell.text())).toEqual(['1', '—', '1'])

    const first = keys[0].get('button')
    const other = keys[2].get('button')
    expect(first.attributes('aria-expanded')).toBe('false')
    expect(first.attributes('aria-controls')).not.toBe(other.attributes('aria-controls'))
    await first.trigger('click')
    expect(first.attributes('aria-expanded')).toBe('true')
    expect(other.attributes('aria-expanded')).toBe('false')
    const details = document.getElementById(first.attributes('aria-controls')!)!
    expect(details.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(details.textContent).toContain('22.2%')
    expect(details.textContent).toContain('部分回报')
    await first.trigger('click')
    expect(first.attributes('aria-expanded')).toBe('false')
    expect(details.querySelector('table')).toBeNull()

    const chart = wrapper.get('.usage-project-heading button')
    expect(chart.attributes('aria-expanded')).toBe('false')
    await chart.trigger('click')
    expect(chart.attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('credit-donut-stub').exists()).toBe(true)
  })

  it('loads a different Beijing ledger month immediately and ignores the older response', async () => {
    const pending = pendingResponse()
    request.mockReturnValueOnce(pending.promise)
    request.mockResolvedValueOnce(response(emptyUsage('2026-10')))
    mountUsage()

    expect(wrapper.find('.skeleton-item').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('所选月暂无模型用量')
    await wrapper.get('input[type="month"]').setValue('2026-10')
    await flushPromises()
    expect(request.mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/student/usage?month=2026-11',
      '/api/v1/student/usage?month=2026-10',
    ])
    expect(wrapper.get('h1').text()).toBe('2026 年 10 月个人用量')
    expect(wrapper.text()).toContain('所选月暂无模型用量')
    expect(wrapper.text()).toContain('所选月暂无项目和 Key 记录')

    pending.resolve(response(currentUsage))
    await flushPromises()
    expect(wrapper.find('[data-testid="student-model-summary"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('所选月暂无模型用量')
  })

  it('keeps project balances readable and reports a missing model rollup as an error', async () => {
    request.mockResolvedValueOnce(response({ month: currentUsage.month, projects: currentUsage.projects }))
    mountUsage()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('模型汇总读取失败')
    expect(wrapper.text()).not.toContain('所选月暂无模型用量')
    expect(stat('used')).toBe('—')
    expect(stat('available')).toBe('199.62419999999999997')
    expect(wrapper.findAll('[data-testid="usage-key-row"]')).toHaveLength(4)
  })

  it('separates a failed read from an empty month and retries the selected month', async () => {
    request.mockResolvedValueOnce(failure())
    mountUsage()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('读取失败')
    expect(wrapper.find('.skeleton-item').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('所选月暂无模型用量')

    request.mockResolvedValueOnce(response(emptyUsage('2026-11')))
    await wrapper.get('.usage-load-error button').trigger('click')
    await flushPromises()
    expect(request.mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/student/usage?month=2026-11',
      '/api/v1/student/usage?month=2026-11',
    ])
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('所选月暂无模型用量')
    expect(stat('used')).toBe('0')
  })

  it('only looks up a receipt when submitted in the dialog and displays its amount, all Token fields and reason', async () => {
    mountUsage(currentUsage)
    await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    const modal = await openReceipt()
    expect(request).toHaveBeenCalledTimes(1)
    expect(modal.get('h2').text()).toBe('查单笔调用')

    request.mockResolvedValueOnce(response(callReceipt))
    await modal.get('input').setValue(` ${callReceipt.request_id} `)
    await modal.get('form').trigger('submit')
    await flushPromises()
    expect(request.mock.lastCall?.[0]).toBe('/api/v1/student/usage/calls/req%2Bschool%3F11%2342')
    const detail = modal.get('[data-testid="receipt-result"]')
    expect(detail.get('time').attributes('datetime')).toBe(callReceipt.created_at)
    expect(detail.get('time').text()).toContain('00:08:10')
    expect(detail.findAll('.usage-receipt-meta dd').map(cell => cell.text()).filter((_, index) => index !== 2)).toEqual([
      '文本模型', callReceipt.request_id, '0.00080000000000003', '已结算', '北辰', '已用回执补记',
    ])
    expect(detail.findAll('.usage-receipt-tokens dd').map(cell => cell.text())).toEqual([
      '9,007,199,254,740,993', '3', '0', '—', '9,007,199,254,740,996',
    ])
    expect(detail.get('.usage-evidence').text()).toContain('部分回报 · 仅回报了部分缓存写入量')

    request.mockResolvedValueOnce(failure(404))
    await modal.get('input').setValue('missing-or-ambiguous')
    await modal.get('form').trigger('submit')
    await flushPromises()
    expect(modal.find('[data-testid="receipt-result"]').exists()).toBe(false)
    expect(modal.get('[role="alert"]').text()).toContain('请核对请求编号，并确认这笔调用经学校网关发往北辰')
    expect(request).toHaveBeenCalledTimes(3)
  })

  it('ignores a receipt response after the dialog has been closed and reopened', async () => {
    mountUsage(emptyUsage('2026-11'))
    await flushPromises()
    const modal = await openReceipt()
    const pending = pendingResponse()
    request.mockReturnValueOnce(pending.promise)
    await modal.get('input').setValue(callReceipt.request_id)
    await modal.get('form').trigger('submit')
    expect(modal.get('[role="status"]').text()).toBe('正在查询调用…')

    await modal.get('[aria-label="关闭"]').trigger('click')
    await flushPromises()
    const reopened = await openReceipt()
    pending.resolve(response(callReceipt))
    await flushPromises()
    expect(reopened.find('[data-testid="receipt-result"]').exists()).toBe(false)
    expect(reopened.get<HTMLInputElement>('input').element.value).toBe('')
    expect(reopened.find('[role="status"]').exists()).toBe(false)
  })

  it('shows pending settlement and missing evidence separately from reported zeros', async () => {
    mountUsage(emptyUsage('2026-11'))
    await flushPromises()
    const modal = await openReceipt()
    request.mockResolvedValueOnce(response({
      ...callReceipt,
      request_id: 'pending-call',
      billing_state: 'pending_review',
      charged_credits: null,
      receipt_applied: null,
      usage_state: undefined,
      usage_reason: 'incomplete_token_breakdown',
      input_tokens: null,
      output_tokens: '4',
      cache_read_tokens: null,
      total_tokens: '130',
    } satisfies StudentCallReceipt))
    await modal.get('input').setValue('pending-call')
    await modal.get('form').trigger('submit')
    await flushPromises()

    const detail = modal.get('[data-testid="receipt-result"]')
    expect(detail.text()).toContain('待核账')
    expect(detail.text()).toContain('暂无匹配回执')
    expect(detail.get('.usage-evidence').text()).toContain('分项用量不完整')
    expect(detail.get('.usage-receipt-meta').text()).toContain('已扣积分—')
    expect(detail.findAll('.usage-receipt-tokens dd').map(cell => cell.text())).toEqual(['—', '4', '—', '—', '130'])
  })
})
