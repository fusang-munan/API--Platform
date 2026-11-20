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
  return mount(StudentUsage, { global: { stubs: { CreditDonut: true } } })
}

function stat(wrapper: ReturnType<typeof mountUsage>, name: string) {
  return wrapper.get(`[data-testid="usage-total-${name}"] strong`).text()
}

describe('StudentUsage', () => {
  beforeEach(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
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
    expect(rows[1].findAll('td').map(cell => cell.text())).toEqual(['0', '—', '—'])
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
    expect(row.findAll('td').map(cell => cell.text())).toEqual(['—', '—', '—'])

    await wrapper.get('[data-testid="usage-range-month"]').trigger('click')
    expect(row.text()).toContain('1,234')
    wrapper.unmount()
  })

  it('looks up one receipt by request id and displays the available fields', async () => {
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
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

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
    wrapper.unmount()
  })

  it('explains a missing receipt instead of showing a stale row', async () => {
    vi.mocked(get).mockRejectedValueOnce(new APIError('call receipt not found', 404, 'RESOURCE_NOT_FOUND'))
    const wrapper = mountUsage(baseUsage)
    await flushPromises()

    await wrapper.get('input[placeholder="输入请求编号"]').setValue('missing-request')
    await wrapper.get('.student-receipt-search').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('未找到该请求编号对应的本人调用')
    wrapper.unmount()
  })
})
