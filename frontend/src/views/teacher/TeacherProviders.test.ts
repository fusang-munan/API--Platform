import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Provider } from '../../api/types'
import BeichenReconcile from './BeichenReconcile.vue'
import TeacherProviders from './TeacherProviders.vue'

const api = vi.hoisted(() => ({ providers: vi.fn(), beichenOverview: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
enableAutoUnmount(afterEach)

function provider(id: string, baseUrl = 'https://api.beichenapi.com'): Provider {
  return {
    id, name: id, base_url: baseUrl, status: 'active', credential_configured: true,
    created_at: '', updated_at: '',
  }
}

function view() {
  return mount(TeacherProviders, {
    attachTo: document.body,
    global: { stubs: { AppDialog: true, BeichenSchoolModels: true, BeichenReconcile: true } },
  })
}

beforeEach(() => {
  api.providers.mockReset()
  api.beichenOverview.mockReset()
})
afterEach(() => vi.useRealTimers())

describe('北辰供应商行', () => {
  it('每个北辰供应商都有对账对比入口，跳到对应的既有对账视图', async () => {
    api.providers.mockResolvedValue([
      provider('north-a'), provider('north-b', 'https://api.beichenapi.com/v1/'), provider('other', 'https://example.test'),
    ])
    api.beichenOverview.mockResolvedValue({ model_catalog: [], comparison: [] })
    const wrapper = view()
    await flushPromises()
    const [first, second, other] = wrapper.findAll('article.provider-row')
    expect(first.get('a').text()).toBe('对账对比')
    const link = second.get('a')
    expect(link.text()).toBe('对账对比')
    expect(link.attributes('href')).toBe('#beichen-reconcile-north-b')
    expect(other.find('a').exists()).toBe(false)
    expect(wrapper.findComponent(BeichenReconcile).exists()).toBe(false)
    await link.trigger('click')
    const region = second.get('#beichen-reconcile-north-b')
    expect(region.findComponent(BeichenReconcile).props('providerId')).toBe('north-b')
    expect(document.activeElement).toBe(region.element)
    expect(first.findComponent(BeichenReconcile).exists()).toBe(false)
    expect(api.beichenOverview.mock.calls.map(([id]) => id)).toEqual(['north-a', 'north-b'])
  })

  it('逐供应商显示北京时间当天待核笔数和去重后的缺价模型数', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-19T17:00:00Z'))
    api.providers.mockResolvedValue([provider('north-a'), provider('north-b')])
    api.beichenOverview.mockResolvedValueOnce({
      comparison: [{ pending_review_calls: 2 }, { pending_review_calls: 3 }, { pending_review_calls: null }],
      model_catalog: [
        { model: 'lab/missing', price_state: 'missing' }, { model: 'LAB/Missing', price_state: 'missing' },
        { model: 'lab/mixed', price_state: 'mixed', blockers: [{ code: 'missing_price' }] },
        { model: 'lab/mixed-active', price_state: 'mixed' },
        { model: 'lab/conditional', price_state: 'conditional' }, { model: 'lab/priced', price_state: 'manual' },
      ],
      models: [
        { model: 'lab/mixed-active', mode: 'follow', price_status: 'confirmed' },
        { model: 'lab/mixed-active', mode: 'follow', price_status: 'not_listed' },
      ],
    }).mockResolvedValueOnce({
      comparison: [{ pending_review_calls: 0 }], model_catalog: [{ model: 'lab/priced', price_state: 'confirmed' }],
    })
    const wrapper = view()
    await flushPromises()
    const [first, second] = wrapper.findAll('[aria-label="北辰供应商概览"]')
    expect(first.text()).toContain('今日待核 5 笔')
    expect(first.text()).toContain('缺价模型 3 个')
    expect(second.text()).toContain('今日待核 0 笔')
    expect(second.text()).toContain('缺价模型 0 个')
    expect(api.beichenOverview.mock.calls).toEqual([['north-a', '2026-11-20'], ['north-b', '2026-11-20']])
  })

  it('摘要缺失或读取失败时只隐藏统计，保留对账入口', async () => {
    api.providers.mockResolvedValue([provider('north-a'), provider('north-b')])
    api.beichenOverview.mockResolvedValueOnce({
      comparison: [{ pending_review_calls: null }], model_catalog: [{ model: 'lab/unknown' }],
    }).mockRejectedValueOnce(new Error('概览暂不可用'))
    const wrapper = view()
    await flushPromises()
    const overviews = wrapper.findAll('[aria-label="北辰供应商概览"]')
    expect(overviews).toHaveLength(2)
    for (const overview of overviews) {
      expect(overview.text()).not.toContain('今日待核')
      expect(overview.text()).not.toContain('缺价模型')
      expect(overview.get('a').text()).toBe('对账对比')
    }
  })
})
