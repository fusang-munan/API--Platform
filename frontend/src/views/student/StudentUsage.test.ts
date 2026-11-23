import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import StudentUsage from './StudentUsage.vue'
import { studentAPI } from '../../api/student'
import { APIError, get } from '../../api/client'
import type { StudentUsage as StudentUsagePayload } from '../../api/types'

vi.mock('../../api/student', () => ({
  studentAPI: {
    usage: vi.fn(),
  },
}))

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, get: vi.fn() }
})

const baseUsage = {
  month: '2026-11',
  projects: [{
    project_id: 'project-1',
    project_name: '项目一',
    quota: '100.000',
    used: '12.500',
    pending: '1.250',
    available: '86.250',
    keys: [{
      id: 'key-1',
      name: '我的 Key',
      status: 'active',
      used: '12.500',
      pending: '1.250',
      models: [
        { id: 'gpt-5', name: 'GPT-5', credits: '10.000', calls: 3, tokens: 1234, cache_hit_rate: 0.25 },
        { id: 'claude', name: 'Claude', credits: '0.000', calls: 0 },
      ],
    }],
  }],
} as unknown as StudentUsagePayload

function mountUsage(payload: StudentUsagePayload) {
  vi.mocked(studentAPI.usage).mockResolvedValue(payload)
  return mount(StudentUsage, {
    global: {
      stubs: {
        CreditDonut: true,
        RouterLink: { props: ['to'], template: '<a :href="to.name" data-testid="usage-model-plaza-link"><slot/></a>' },
      },
    },
  })
}

function stat(wrapper: ReturnType<typeof mountUsage>, name: string) {
  return wrapper.get(`[data-testid="usage-total-${name}"] strong`).text()
}

describe('StudentUsage', () => {
  beforeEach(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    vi.mocked(get).mockResolvedValue({ items: [], next_cursor: null })
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('shows monthly totals, explicit zero, and per-model credits, tokens and cache hit rate', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

    expect(stat(wrapper, 'quota')).toBe('100')
    expect(stat(wrapper, 'used')).toBe('12.5')
    expect(stat(wrapper, 'pending')).toBe('1.25')
    expect(stat(wrapper, 'available')).toBe('86.25')

    const rows = wrapper.findAll('.student-model-table tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('GPT-5')
    expect(rows[0].text()).toContain('10')
    expect(rows[0].text()).toContain('1,234')
    expect(rows[0].text()).toContain('25%')
    expect(rows[0].findAll('td').map(cell => cell.text())).toEqual(['10', '3 次', '1,234', '25%'])
    expect(rows[1].findAll('td').map(cell => cell.text())).toEqual(['0', '0 次', '—', '—'])
    wrapper.unmount()
  })

  it('merges the same model across projects and keys into one summary row', async () => {
    const payload = {
      month: '2026-11',
      projects: [{
        project_id: 'project-1',
        project_name: '项目一',
        quota: '100.000',
        used: '12.500',
        pending: '0.000',
        available: '87.500',
        keys: [
          {
            id: 'key-1', name: '我的 Key', status: 'active', used: '10.000', pending: '0.000',
            models: [{ id: 'gpt-5', name: 'GPT-5', credits: '10.000', calls: 3, tokens: 1200, cache_hit_rate: 0.2 }],
          },
          {
            id: 'key-2', name: '第二把 Key', status: 'active', used: '2.500', pending: '0.000',
            models: [{ id: 'GPT-5', name: 'GPT-5', credits: '2.500', calls: 1, tokens: 300, cache_hit_rate: 0.6 }],
          },
        ],
      }],
    } as unknown as StudentUsagePayload
    const wrapper = mountUsage(payload)
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="student-model-summary"] tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('GPT-5')
    expect(rows[0].findAll('td').map(cell => cell.text())).toEqual(['12.5', '4 次', '1,500', '30%', '—'])
    // 原有按 Key 明细保留两行
    expect(wrapper.findAll('.student-model-table tbody tr')).toHaveLength(2)
    wrapper.unmount()
  })

  it('keeps the summary view honest when the selected range is not returned by the API', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

    await wrapper.get('[data-testid="usage-range-all"]').trigger('click')
    const rows = wrapper.findAll('[data-testid="student-model-summary"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].findAll('td').map(cell => cell.text())).toEqual(['—', '—', '—', '—', '—'])
    wrapper.unmount()
  })

  it('points students to the model plaza to add models to the current key', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

    const link = wrapper.get('[data-testid="usage-model-plaza-link"]')
    expect(link.attributes('href')).toBe('student-models')
    expect(link.text()).toBe('到模型广场把模型加入当前 Key')
    expect(wrapper.text()).not.toContain('重新申领')
    wrapper.unmount()
  })

  it('shows a dash for missing data and keeps explicit zero visible', async () => {
    const payload = {
      month: '2026-11',
      projects: [{
        project_id: 'project-2',
        project_name: '项目二',
        quota: '0.000',
        used: '12.500',
        pending: '0.000',
        available: '',
        keys: [],
      }],
    } as unknown as StudentUsagePayload
    const wrapper = mountUsage(payload)
    await flushPromises()

    expect(stat(wrapper, 'quota')).toBe('0')
    expect(stat(wrapper, 'pending')).toBe('0')
    expect(stat(wrapper, 'available')).toBe('—')
    expect(stat(wrapper, 'used')).toBe('12.5')
    wrapper.unmount()
  })

  it('keeps today/all switches visible but marks the unavailable range', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()
    const row = wrapper.findAll('.student-model-table tbody tr')[0]
    expect(row.text()).toContain('1,234')

    await wrapper.get('[data-testid="usage-range-day"]').trigger('click')
    expect(wrapper.text()).toContain('只返回本月汇总')
    expect(row.text()).not.toContain('1,234')
    expect(row.findAll('td').map(cell => cell.text())).toEqual(['—', '—', '—', '—'])

    await wrapper.get('[data-testid="usage-range-month"]').trigger('click')
    expect(row.text()).toContain('1,234')
    wrapper.unmount()
  })

  it('loads recent calls for the selected range and appends the next cursor page', async () => {
    vi.mocked(get).mockResolvedValueOnce({
      items: [
        { id: 'call-2', request_id: 'req-2', model_id: 'gpt-5', model_name: 'GPT-5', project_name: '项目一',
          input_tokens: 120, output_tokens: 30, cache_read_tokens: 80, cache_write_tokens: 10,
          credits: '1.250', status: 'succeeded', billing_state: 'charged', created_at: '2026-11-21T10:00:00Z' },
        { id: 'call-1', request_id: 'req-1', model_id: 'claude', model_name: 'Claude', project_name: '项目一',
          input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null,
          credits: '0.000', status: 'pending_review', billing_state: 'pending_review', created_at: '2026-11-21T09:00:00Z' },
      ],
      next_cursor: 'cursor-1',
    })
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

    expect(get).toHaveBeenCalledWith('/api/v1/student/usage/recent?range=today&limit=20')
    let rows = wrapper.findAll('.student-recent-table tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('GPT-5')
    expect(rows[0].text()).toContain('120')
    expect(rows[0].text()).toContain('1.25')
    expect(rows[1].text()).toContain('待核账')
    expect(rows[1].findAll('td').map(cell => cell.text()).slice(2, 6)).toEqual(['—', '—', '—', '—'])

    vi.mocked(get).mockResolvedValueOnce({
      items: [{ id: 'call-0', request_id: 'req-0', model_id: 'gpt-5', model_name: 'GPT-5', project_name: '项目一',
        input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0,
        credits: '0.010', status: 'upstream_failed', billing_state: 'failed', created_at: '2026-11-21T08:00:00Z' }],
      next_cursor: null,
    })
    await wrapper.get('.student-recent-actions button').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/api/v1/student/usage/recent?range=today&limit=20&cursor=cursor-1')
    rows = wrapper.findAll('.student-recent-table tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[2].text()).toContain('上游失败')
    expect(wrapper.text()).toContain('已显示全部')

    vi.mocked(get).mockResolvedValueOnce({ items: [], next_cursor: null })
    await wrapper.get('[data-testid="recent-range-month"]').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/api/v1/student/usage/recent?range=month&limit=20')
    expect(wrapper.findAll('.student-recent-table tbody tr')).toHaveLength(0)
    wrapper.unmount()
  })

  it('looks up one receipt by request id and displays the available fields', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()
    vi.mocked(get).mockResolvedValue({
      request_id: 'req-123',
      model_id: 'gpt-5',
      channel: '北辰',
      billing_state: 'charged',
      base_credits: '10.000',
      base_source: 'gateway_usage_price',
      school_multiplier: '1.10',
      charged_credits: '11.000',
      actual_cost: '0.1100',
      receipt_applied: true,
    })

    await wrapper.get('input[placeholder="输入请求编号"]').setValue('req-123')
    await wrapper.get('.student-receipt-search').trigger('submit')
    await flushPromises()

    expect(get).toHaveBeenCalledWith('/api/v1/student/usage/calls/req-123')
    const callRow = wrapper.get('.student-call-table tbody tr')
    expect(callRow.text()).toContain('gpt-5')
    expect(callRow.text()).toContain('11')
    expect(callRow.text()).toContain('已扣')
    expect(wrapper.text()).toContain('req-123')
    expect(wrapper.text()).toContain('已回执')
    expect(wrapper.get('[data-testid="credit-rate-note"]').text()).toBe('100 积分 = 1 元；金额统一用积分显示。')
    expect(wrapper.text()).not.toContain('基础积分')
    expect(wrapper.text()).not.toContain('学校倍率')
    expect(wrapper.text()).not.toContain('实际成本')
    expect(wrapper.text()).not.toContain('0.1100 元')
    wrapper.unmount()
  })

  it('explains a missing receipt instead of showing a stale row', async () => {
    const wrapper = mountUsage(baseUsage)
    await flushPromises()
    vi.mocked(get).mockRejectedValueOnce(new APIError('call receipt not found', 404, 'RESOURCE_NOT_FOUND'))

    await wrapper.get('input[placeholder="输入请求编号"]').setValue('missing-request')
    await wrapper.get('.student-receipt-search').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('未找到该请求编号对应的本人调用')
    wrapper.unmount()
  })
})
