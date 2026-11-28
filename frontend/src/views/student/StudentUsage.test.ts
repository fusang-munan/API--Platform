import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { StudentCallReceipt, StudentRecentCall, StudentUsageSnapshot } from '../../api/student'
import type { StudentUsageMetrics, StudentUsageModel } from '../../api/types'
import StudentUsage from './StudentUsage.vue'
import studentUsageSource from './StudentUsage.vue?raw'

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

const currentUsage: StudentUsageSnapshot = {
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
          pending: '0.250', in_flight_credits: '0.250', available: '0.000',
          available_effective: '49.74959999999999999', project_member_available: '99.62459999999999999',
          calls: 1, pending_calls: 1, pending_review: true, models: [firstModel, pendingModel],
        },
        {
          ...blankMetrics,
          id: 'key-idle', name: '在途 Key', status: 'active', used: '0.000',
          pending: '0.125', in_flight_credits: '0.125',
          available_effective: '49.74959999999999999', project_member_available: '99.62459999999999999',
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
          available_effective: '0.000', project_member_available: '99.99959999999999998',
          calls: 1, pending_calls: 0, models: [secondModel],
        },
        {
          ...zeroModelMetrics,
          id: 'key-inactive', name: '停用的 Key', status: 'inactive', used: '0.000',
          pending: '0.000', in_flight_credits: '0.000',
          available_effective: '0.000', project_member_available: '99.99959999999999998',
          calls: 1, pending_calls: 0, models: [zeroModel],
        },
      ],
    },
  ],
}

const legacyUsage: StudentUsageSnapshot = {
  month: '2026-11',
  projects: [{
    project_id: 'legacy', project_name: '旧项目', quota: '100.000',
    used: '7.250', pending: '1.375', available: '91.375', pending_review: true,
    keys: [{
      id: 'legacy-key', name: '旧版 Key', status: 'active',
      used: '7.250', pending: '1.375', available: '35.000', pending_review: true,
      models: [{ id: 'text', name: '旧版模型', credits: '7.250', calls: 3 }],
    }],
  }],
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

const recentCall = {
  id: '01800000-0000-7000-8000-000000000001',
  request_id: 'recent-gateway-req',
  model_id: 'gpt-5',
  model_name: 'GPT-5',
  project_name: '项目一',
  input_tokens: 100,
  output_tokens: 20,
  cache_read_tokens: 0,
  cache_write_tokens: null,
  credits: '1.250',
  status: 'succeeded',
  billing_state: 'charged',
  created_at: '2026-10-31T16:08:10Z',
} satisfies StudentRecentCall

const request = vi.fn<typeof fetch>()
let wrapper: VueWrapper

function response(data: unknown) {
  return new Response(JSON.stringify({ data, request_id: 'test-http-response' }))
}

function failure(status = 503) {
  const code = status === 404 ? 'RESOURCE_NOT_FOUND' : 'DEPENDENCY_UNAVAILABLE'
  return new Response(JSON.stringify({ error: { code } }), { status })
}

function emptyUsage(month: string): StudentUsageSnapshot {
  return { month, projects: [], models: [], totals: { ...blankMetrics, credits: '0.000', calls: 0 } }
}

function mountUsage(data?: unknown) {
  if (data !== undefined) request.mockResolvedValueOnce(response(data))
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

async function openCallDialog(kind: 'recent' | 'receipt') {
  await wrapper.get(`[data-testid="open-${kind}"]`).trigger('click')
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
    expect(wrapper.find('.usage-recent-list').exists()).toBe(false)
    expect(stat('quota')).toBe('200')
    expect(stat('used')).toBe('0.00080000000000003')
    expect(stat('pending')).toBe('0.375')
    expect(stat('available')).toBe('199.62419999999999997')
    expect(wrapper.get('a[href="student-models"]').text()).toBe('到模型广场添加模型')

    const table = wrapper.get('[data-testid="student-model-summary"]')
    expect(table.element.closest('details')?.open).toBe(false)
    expect(wrapper.get('.usage-key-list').element.compareDocumentPosition(table.element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const rows = table.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].get('th').text()).toContain('文本模型')
    expect(rows[0].findAll('td').slice(0, 2).map(cell => cell.text())).toEqual(['0.00080000000000003', '2'])
    expect(rows[0].get('td[data-label="Token"]').text()).toContain('240')
    expect(rows[0].get('td[data-label="Token"] small').text()).toBe('完整')
    expect(rows[0].get('td[data-label="缓存命中率"]').text()).toContain('37.5%')
    expect(rows[0].get('td[data-label="缓存命中率"] small').text()).toBe('完整 · 覆盖 2/2 笔')
    expect(rows[1].get('td[data-label="Token"]').text()).toContain('130')
    expect(rows[1].get('td[data-label="调用次数"]').text()).toBe('0')
    expect(rows[1].get('td[data-label="Token"] small').text()).toBe('部分回报（0/1 笔完整）')
    expect(rows[1].get('td[data-label="说明"]').text()).toContain('未分类 126 Token')
    expect(rows[1].get('td[data-label="说明"]').text()).toContain('待核 1 笔（预留 10 积分）')
    expect(rows[1].get('td[data-label="缓存命中率"]').text()).toContain('无样本')
    expect(rows[1].get('td[data-label="缓存命中率"] small').text()).toBe('覆盖 0/1 笔')
    expect(rows[1].text()).not.toContain('零额已结算')
    expect(rows[2].get('td[data-label="Token"]').text()).toBe('无样本')
    expect(rows[2].get('td[data-label="说明"]').text()).toBe('零额已结算')
    const total = table.get('tfoot tr')
    expect(total.get('td[data-label="已扣积分"]').text()).toBe('0.00080000000000003')
    expect(total.get('td[data-label="调用次数"]').text()).toBe('3')
    expect(total.get('td[data-label="Token"]').text()).toContain('370')
    expect(total.get('td[data-label="缓存命中率"] small').text()).toBe('部分回报 · 覆盖 2/4 笔')
  })

  it('keeps idle and inactive Keys visible and expands model details independently for each project and Key', async () => {
    mountUsage(currentUsage)
    await flushPromises()

    const keys = wrapper.findAll('[data-testid="usage-key-row"]')
    expect(keys).toHaveLength(4)
    expect(keys.map(row => row.findAll('.usage-key-totals dd').map(cell => cell.text()))).toEqual([
      ['项目一', '0.00040000000000001', '1'],
      ['项目一', '0', '0'],
      ['项目二', '0.00040000000000002', '1'],
      ['项目二', '0', '1'],
    ])
    expect(wrapper.find('.usage-key-models table').exists()).toBe(false)
    expect(keys[3].text()).toContain('已停用')

    const first = keys[0].get('button')
    const other = keys[2].get('button')
    expect(first.attributes('aria-expanded')).toBe('false')
    expect(first.attributes('aria-controls')).not.toBe(other.attributes('aria-controls'))
    await first.trigger('click')
    expect(first.attributes('aria-expanded')).toBe('true')
    expect(other.attributes('aria-expanded')).toBe('false')
    const details = document.getElementById(first.attributes('aria-controls')!)!
    expect(details.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(details.querySelector('td[data-label="已扣积分"]')?.textContent?.trim()).toBe('0.00040000000000001')
    expect(details.textContent).toContain('本项目个人可用积分')
    expect(details.textContent).toContain('49.74959999999999999')
    expect(details.textContent).toContain('待核 1 笔')
    expect(details.textContent).toContain('22.2%')
    expect(details.textContent).toContain('部分回报')
    await first.trigger('click')
    expect(first.attributes('aria-expanded')).toBe('false')
    expect(details.querySelector('table')).toBeNull()
    await other.trigger('click')
    const otherDetail = document.getElementById(other.attributes('aria-controls')!)!
    expect(otherDetail.querySelectorAll('tbody tr')).toHaveLength(1)
    expect(otherDetail.querySelector('td[data-label="已扣积分"]')?.textContent?.trim()).toBe('0.00040000000000002')
    await keys[1].get('button').trigger('click')
    const idle = document.getElementById(keys[1].get('button').attributes('aria-controls')!)!
    expect(idle.textContent).toContain('暂无模型记录')
    expect(idle.textContent).toContain('0.125')

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
    expect(wrapper.text()).toContain('所选月暂无 Key 记录')

    pending.resolve(response(currentUsage))
    await flushPromises()
    expect(wrapper.find('[data-testid="student-model-summary"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('所选月暂无模型用量')
  })

  it('renders real legacy project and Key data without the new metrics or model rollup', async () => {
    mountUsage(legacyUsage)
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('模型汇总读取失败')
    expect(wrapper.text()).not.toContain('所选月暂无模型用量')
    expect(stat('used')).toBe('—')
    expect(stat('pending')).toBe('1.375')
    expect(stat('available')).toBe('91.375')
    const row = wrapper.get('[data-testid="usage-key-row"]')
    expect(row.findAll('.usage-key-totals dd').map(cell => cell.text())).toEqual(['旧项目', '7.25', '未回报'])

    await row.get('button').trigger('click')
    expect(wrapper.get('.usage-key-models').text()).toContain('有待核账记录')
    expect(wrapper.get('.usage-key-details').findAll('dd').map(cell => cell.text())).toEqual(['1.375', '35', '无样本', '无样本'])
    const model = wrapper.get('.usage-key-models tbody tr')
    expect(model.get('th').text()).toContain('旧版模型')
    expect(model.findAll('td').map(cell => cell.text())).toEqual(['7.25', '3', '无样本', '无样本', ''])
  })

  it('handles mistyped optional fields and keeps an available totals amount when models are missing', async () => {
    const project = legacyUsage.projects[0]
    const key = project.keys[0]
    mountUsage({
      ...legacyUsage,
      totals: currentUsage.totals,
      models: { unexpected: true },
      projects: [{
        ...project,
        keys: [{
          ...key, available_effective: [], tokens: null, token_coverage: null,
          in_flight_credits: false, calls: true, models: null,
        }],
      }],
    })
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('模型汇总读取失败')
    expect(stat('used')).toBe('0.00080000000000003')
    const row = wrapper.get('[data-testid="usage-key-row"]')
    expect(row.findAll('.usage-key-totals dd').map(cell => cell.text())).toEqual(['旧项目', '7.25', '未回报'])
    await row.get('button').trigger('click')
    expect(wrapper.get('.usage-key-details').findAll('dd').map(cell => cell.text())).toEqual(['1.375', '35', '无样本', '无样本'])
    expect(wrapper.get('.usage-key-models').text()).toContain('模型明细暂不可用')
  })

  it('keeps an expanded Key open during and after refreshing the same month', async () => {
    mountUsage(currentUsage)
    await flushPromises()
    const button = wrapper.get('[data-testid="usage-key-row"] button')
    await button.trigger('click')
    const detail = document.getElementById(button.attributes('aria-controls')!)!
    const refresh = pendingResponse()
    request.mockReturnValueOnce(refresh.promise)
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(button.attributes('aria-expanded')).toBe('true')
    expect(detail.querySelectorAll('tbody tr')).toHaveLength(2)

    refresh.resolve(response(currentUsage))
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(2)
    expect(button.attributes('aria-expanded')).toBe('true')
    expect(detail.querySelectorAll('tbody tr')).toHaveLength(2)
  })

  it('shows a reported zero cache hit rate separately from total-only and ledger-only rows', async () => {
    const zeroHit = { ...firstModel, cache_hit_rate: 0, cache_hit_coverage: {
      sample_calls: 1, cache_read_tokens: '0', input_with_cache_read_tokens: '100',
    } }
    const totalOnly = { ...pendingModel, tokens: { ...blankMetrics.tokens, total: '130' } }
    mountUsage({ ...currentUsage, models: [zeroHit, totalOnly, zeroModel] })
    await flushPromises()
    const rows = wrapper.get('[data-testid="student-model-summary"]').findAll('tbody tr')
    expect(rows[0].get('[data-label="缓存命中率"]').text()).toContain('0%')
    expect(rows[0].get('[data-label="缓存命中率"] small').text()).toContain('完整')
    expect(rows[1].get('[data-label="Token"]').text()).toContain('130')
    expect(rows[1].get('[data-label="Token"]').text()).toContain('部分回报')
    expect(rows[1].get('[data-label="缓存命中率"]').text()).toContain('无样本')
    expect(rows[2].get('[data-label="Token"]').text()).toBe('无样本')
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

  it('opens recent calls on demand, expands a record and keeps the current list while retrying pagination', async () => {
    mountUsage(emptyUsage('2026-11'))
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(1)
    expect(document.querySelector('[role="dialog"]')).toBeNull()

    const cursor = 'eyJjcmVhdGVkX2F0IjoiMjAyNi0xMC0zMVQxNjowODoxMFoifQ'
    request.mockResolvedValueOnce(response({ items: [recentCall], next_cursor: cursor }))
    const modal = await openCallDialog('recent')
    expect(modal.get('h2').text()).toBe('最近调用')
    expect(modal.get('[data-testid="recent-range-today"]').attributes('aria-pressed')).toBe('true')
    expect(request.mock.lastCall?.[0]).toBe('/api/v1/student/usage/recent?range=today&limit=20')

    const row = modal.get('.usage-recent-row')
    expect(row.attributes('aria-expanded')).toBe('false')
    expect(row.text()).toContain('1.25 积分')
    expect(row.get('time').text()).toContain('00:08:10')
    await row.trigger('click')
    expect(row.attributes('aria-expanded')).toBe('true')
    expect(modal.get('.usage-recent-detail code').text()).toBe(recentCall.request_id)
    expect(modal.get('.usage-recent-detail').text()).toContain('项目一')
    expect(modal.get('.usage-recent-detail').text()).toContain('成功')
    expect(modal.findAll('.usage-receipt-tokens dd').map(cell => cell.text())).toEqual(['100', '20', '0', '无样本'])
    expect(request).toHaveBeenCalledTimes(2)

    request.mockResolvedValueOnce(failure())
    const more = modal.get('.usage-recent-more')
    await more.trigger('click')
    await flushPromises()
    expect(modal.get('[role="alert"]').text()).toBeTruthy()
    expect(more.text()).toBe('重试')
    expect(row.attributes('aria-expanded')).toBe('true')

    request.mockResolvedValueOnce(response({
      items: [{ ...recentCall, id: '01800000-0000-7000-8000-000000000002', request_id: 'older-call' }],
      next_cursor: null,
    }))
    await more.trigger('click')
    await flushPromises()
    expect(request.mock.calls.slice(2).map(([url]) => url)).toEqual([
      `/api/v1/student/usage/recent?range=today&limit=20&cursor=${cursor}`,
      `/api/v1/student/usage/recent?range=today&limit=20&cursor=${cursor}`,
    ])
    expect(modal.findAll('.usage-recent-row')).toHaveLength(2)
    expect(modal.find('.usage-recent-more').exists()).toBe(false)
    expect(modal.find('[role="alert"]').exists()).toBe(false)
  })

  it('resets the cursor on range changes and ignores responses from a former range or a closed dialog', async () => {
    mountUsage(emptyUsage('2026-11'))
    await flushPromises()
    const today = pendingResponse()
    request.mockReturnValueOnce(today.promise)
    const modal = await openCallDialog('recent')
    expect(modal.get('[role="status"]').text()).toBe('正在读取最近调用…')

    request.mockResolvedValueOnce(response({ items: [], next_cursor: null }))
    await modal.get('[data-testid="recent-range-month"]').trigger('click')
    await flushPromises()
    expect(request.mock.calls[1][1]?.signal?.aborted).toBe(true)
    expect(request.mock.lastCall?.[0]).toBe('/api/v1/student/usage/recent?range=month&limit=20')
    today.resolve(response({ items: [recentCall], next_cursor: 'old-cursor' }))
    await flushPromises()
    expect(modal.get('p.usage-recent-empty[role="status"]').text()).toBe('所选范围暂无调用记录')
    expect(modal.find('.usage-recent-list').exists()).toBe(false)
    expect(modal.find('.usage-recent-more').exists()).toBe(false)

    const all = pendingResponse()
    request.mockReturnValueOnce(all.promise)
    await modal.get('[data-testid="recent-range-all"]').trigger('click')
    expect(request.mock.lastCall?.[0]).toBe('/api/v1/student/usage/recent?range=all&limit=20')
    await modal.get('[aria-label="关闭"]').trigger('click')
    await flushPromises()
    expect(request.mock.lastCall?.[1]?.signal?.aborted).toBe(true)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(document.querySelector('.usage-recent-empty')).toBeNull()

    request.mockResolvedValueOnce(response({ items: [], next_cursor: null }))
    const reopened = await openCallDialog('recent')
    all.resolve(response({ items: [recentCall], next_cursor: null }))
    await flushPromises()
    expect(reopened.get('[data-testid="recent-range-today"]').attributes('aria-pressed')).toBe('true')
    expect(reopened.get('[role="status"]').text()).toBe('所选范围暂无调用记录')
  })

  it('defines a narrow layout for the Key row and receipt form at 360px', () => {
    const documentSource = new DOMParser().parseFromString(studentUsageSource, 'text/html')
    const style = document.createElement('style')
    style.textContent = documentSource.querySelector('style[scoped]')!.textContent
    document.head.append(style)
    try {
      // jsdom exposes the layout rules, but does not calculate viewport geometry.
      const narrow = Array.from(style.sheet!.cssRules).find((rule): rule is CSSMediaRule =>
        rule instanceof CSSMediaRule && rule.conditionText === '(max-width: 560px)')
      expect(narrow).toBeDefined()
      const rules = Array.from(narrow!.cssRules).filter((rule): rule is CSSStyleRule => rule instanceof CSSStyleRule)
      expect(rules.find(rule => rule.selectorText === '.usage-key-row')?.style.getPropertyValue('grid-template-areas'))
        .toBe("'name action' 'totals totals'")
      expect(rules.find(rule => rule.selectorText === '.usage-receipt-form')?.style.getPropertyValue('grid-template-columns'))
        .toBe('minmax(0, 1fr)')
    } finally {
      style.remove()
    }
  })

  it('only looks up a receipt when submitted in the dialog and displays its amount, all Token fields and reason', async () => {
    mountUsage(currentUsage)
    await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    const modal = await openCallDialog('receipt')
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
      '9,007,199,254,740,993', '3', '0', '无样本', '9,007,199,254,740,996',
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
    const modal = await openCallDialog('receipt')
    const pending = pendingResponse()
    request.mockReturnValueOnce(pending.promise)
    await modal.get('input').setValue(callReceipt.request_id)
    await modal.get('form').trigger('submit')
    expect(modal.get('[role="status"]').text()).toBe('正在查询调用…')

    await modal.get('[aria-label="关闭"]').trigger('click')
    await flushPromises()
    const reopened = await openCallDialog('receipt')
    pending.resolve(response(callReceipt))
    await flushPromises()
    expect(reopened.find('[data-testid="receipt-result"]').exists()).toBe(false)
    expect(reopened.get<HTMLInputElement>('input').element.value).toBe('')
    expect(reopened.find('[role="status"]').exists()).toBe(false)
  })

  it('shows pending settlement and missing evidence separately from reported zeros', async () => {
    mountUsage(emptyUsage('2026-11'))
    await flushPromises()
    const modal = await openCallDialog('receipt')
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
    expect(detail.findAll('.usage-receipt-tokens dd').map(cell => cell.text())).toEqual(['无样本', '4', '无样本', '无样本', '130'])
  })
})
