import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import type { Provider, ProviderCredential } from '../../api/types'
import BeichenReconcile from './BeichenReconcile.vue'
import TeacherProviders from './TeacherProviders.vue'

const api = vi.hoisted(() => ({
  providers: vi.fn(), beichenOverview: vi.fn(), beichenAliases: vi.fn(), putBeichenAlias: vi.fn(),
  createProviderCredential: vi.fn(), updateProviderCredential: vi.fn(), requestBeichenProbes: vi.fn(),
}))
const channelAPI = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
vi.mock('../../api/client', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/client')>(),
  ...channelAPI,
}))
const Dialog = defineComponent({ props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><slot /></section>' })
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
    global: { stubs: { AppDialog: Dialog, BeichenSchoolModels: true, BeichenReconcile: true } },
  })
}

beforeEach(() => {
  for (const mock of Object.values({ ...api, ...channelAPI })) mock.mockReset()
})

describe('渠道切换向导', () => {
  const credential = (id: string, label: string, status: ProviderCredential['status']) => ({
    id, provider_id: 'north', label, status, credential_configured: true, created_at: '', updated_at: '',
  })
  const oldKey = credential('old', 'Claude 原渠道', 'inactive')
  const newKey = credential('next', 'Claude 新渠道', 'active')
  const sync = (state: string) => ({ catalog: state, prices: state, billing: state, usage: state })
  const model = {
    model: 'claude-fable-5', ready_count: 0, losing_route_count: 0, loses_last_route: false,
    routes: [{ adapter: 'anthropic', upstream_model: 'claude-fable-5', credential_id: 'old', credential_label: oldKey.label, ready: false, stale: true }],
  }

  it('保存新 Key 后自动读取预览，显示缺价和协议原因，等确认后才换线', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    api.providers.mockResolvedValue([{ ...provider('north'), credentials: [oldKey] }])
    api.beichenOverview.mockResolvedValue({ model_catalog: [], comparison: [], credentials: [
      { credential_id: 'next', label: newKey.label, status: 'active', health: {
        available_dimensions: 2,
        credential: { state: 'available', message: '凭据正常' },
        catalog: { state: 'available', message: '目录正常' },
        pricing: { state: 'unavailable', message: '缺价', action: 'sync' },
        probe: { state: 'pending', message: '等待首次探活', action: 'probe' },
      } },
      { credential_id: 'old', label: oldKey.label, status: 'inactive', health: { available_dimensions: 0 } },
    ] })
    let added = false
    let synced = false
    let confirmed = false
    const route = { upstream_model: model.model, priority: 7, status: 'active' }
    const changes = [{
      action: 'update', adapter: 'anthropic', binding_id: 'binding', school_model: model.model,
      before: { ...route, credential_id: 'old', credential_label: oldKey.label },
      after: { ...route, credential_id: 'next', credential_label: newKey.label },
    }]
    channelAPI.get.mockImplementation((url: string) => ({
      credentials: [
        { credential_id: 'old', label: oldKey.label, status: oldKey.status, sync: sync('inactive') },
        ...(added ? [{ credential_id: 'next', label: newKey.label, status: newKey.status, sync: sync(synced ? 'ready' : 'pending') }] : []),
      ],
      counts: { enabled_keys: added ? 1 : 0, enabled_models: 1, maintenance_models: confirmed ? 0 : 1, stale_routes: confirmed ? 0 : 1 },
      models: confirmed ? [] : [model],
      preview: synced && url.includes('to_credential_id=next') ? {
        preview: true, preview_token: 'preview-token', changes: confirmed ? [] : changes,
        blockers: [
          { school_model: 'other', adapter: 'anthropic', code: 'missing_price', message: '接替渠道缺少可计算报价。' },
          { school_model: 'other', adapter: 'openai_responses', code: 'missing_protocol', message: '接替渠道未登记这条线路使用的协议。' },
        ],
      } : null,
    }))
    api.createProviderCredential.mockImplementation(async () => {
      added = true
      return newKey
    })
    channelAPI.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/sync')) return { synced: true }
      confirmed = true
      return { preview: false, changes }
    })
    const wrapper = view()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '渠道切换向导')!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[aria-label="渠道切换向导"]')
    const button = (label: string) => dialog.findAll('button').find(element => element.text() === label)!
    expect(dialog.get('[aria-label="受影响模型"]').text().replace(/\s+/gu, ' ')).toContain('claude-fable-5 · 0 条可用线路 · 维护中')
    await button('添加接替 Key').trigger('click')
    const form = dialog.get('form')
    const [name, key] = form.findAll('input')
    await name.setValue(newKey.label)
    await key.setValue('sk-local-fixture')
    await form.trigger('submit')
    await flushPromises()
    expect(api.createProviderCredential).toHaveBeenCalledWith('north', {
      label: newKey.label, credential: 'sk-local-fixture', status: 'active', replaces_credential_id: 'old',
    })
    expect(dialog.text()).toContain('等待同步')
    expect(dialog.find('[aria-label="重绑预览"]').exists()).toBe(false)
    expect(channelAPI.post).not.toHaveBeenCalled()
    synced = true
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    const preview = dialog.get('[aria-label="重绑预览"]')
    expect(preview.text()).toContain('Claude 原渠道 → Claude 新渠道')
    expect(preview.text()).toContain('接替渠道缺少可计算报价')
    expect(preview.text()).toContain('接替渠道未登记这条线路使用的协议')
    expect(dialog.get('summary').text()).toBe('Key 健康 0/1 把')
    await button('一键同步').trigger('click')
    await flushPromises()
    expect(channelAPI.post).toHaveBeenCalledExactlyOnceWith('/api/v1/teacher/providers/north/beichen/sync', { credential_id: 'next' })
    await button('确认重绑').trigger('click')
    await flushPromises()
    expect(channelAPI.post).toHaveBeenLastCalledWith('/api/v1/teacher/providers/north/beichen/channel-switch', {
      from_credential_id: 'old', to_credential_id: 'next', preview_token: 'preview-token',
    })
    expect(dialog.text()).toContain('已更新 1 条线路')
    expect(dialog.text()).toContain('维护中 0 个')
  })

  it('请求停用前读取并列出会失去最后一条线路的模型', async () => {
    const activeOld = { ...oldKey, status: 'active' as const }
    api.providers.mockResolvedValue([{ ...provider('north'), credentials: [activeOld] }])
    api.beichenOverview.mockResolvedValue({ model_catalog: [], comparison: [] })
    channelAPI.get.mockImplementation((url: string) => ({
      credentials: [{ credential_id: 'old', label: oldKey.label, status: 'active', sync: sync('ready') }],
      counts: { enabled_keys: 1, enabled_models: 2, maintenance_models: 0, stale_routes: 0 },
      models: url.includes('from_credential_id=old') ? [
        { ...model, ready_count: 1, losing_route_count: 1, loses_last_route: true },
        { ...model, model: 'has-backup', ready_count: 2, losing_route_count: 1 },
      ] : [],
      preview: null,
    }))
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true)
    api.updateProviderCredential.mockResolvedValue(oldKey)
    const wrapper = view()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '渠道切换向导')!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[aria-label="渠道切换向导"]')
    const retire = dialog.findAll('button').find(button => button.text() === '停用原 Key')!
    await retire.trigger('click')
    await flushPromises()
    expect(confirm.mock.calls[0][0]).toContain('进入维护的模型\nclaude-fable-5')
    expect(confirm.mock.calls[0][0]).toContain('少一条或多条北辰线路的模型\nhas-backup')
    expect(api.updateProviderCredential).not.toHaveBeenCalled()
    await retire.trigger('click')
    await flushPromises()
    expect(api.updateProviderCredential).toHaveBeenCalledWith('north', 'old', { status: 'inactive' })
  })
})
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

describe('北辰供应商行', () => {
  it('按渠道创建、更新和移除模型别名', async () => {
    api.providers.mockResolvedValue([{ ...provider('north'), credentials: [
      { id: 'first', provider_id: 'north', label: '渠道一', status: 'active', credential_configured: true, created_at: '', updated_at: '' },
    ] }])
    api.beichenOverview.mockResolvedValue({ model_catalog: [], comparison: [] })
    const original = { credential_id: 'first', upstream_model: 'upstream/a', school_model: 'school/a' }
    api.beichenAliases.mockResolvedValueOnce([]).mockResolvedValueOnce([original])
      .mockResolvedValueOnce([{ ...original, school_model: 'school/b' }]).mockResolvedValueOnce([])
    api.putBeichenAlias.mockResolvedValue({ saved: true })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = view()
    await flushPromises()
    await wrapper.findAll('button').find(node => node.text() === '模型别名')!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[aria-label="北辰模型别名"]')
    const form = dialog.get('form')
    expect(form.get('select').element.value).toBe('first')
    const [upstream, school] = form.findAll('input')
    await upstream.setValue('upstream/a')
    await school.setValue('school/a')
    await form.trigger('submit')
    await flushPromises()
    expect(dialog.get('.alias-list').text()).toContain('upstream/a → school/a')
    await dialog.findAll('button').find(node => node.text() === '编辑')!.trigger('click')
    expect(upstream.element.disabled).toBe(true)
    await school.setValue('school/b')
    await form.trigger('submit')
    await flushPromises()
    expect(dialog.get('.alias-list').text()).toContain('upstream/a → school/b')
    await dialog.findAll('button').find(node => node.text() === '移除')!.trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('还没有模型别名')
    expect(api.putBeichenAlias.mock.calls).toEqual([
      ['north', original], ['north', { ...original, school_model: 'school/b' }], ['north', { ...original, school_model: null }],
    ])
  })

  it('分 Key 列出目录差异并提示换 Key 和补分组', async () => {
    api.providers.mockResolvedValue([provider('north')])
    api.beichenOverview.mockResolvedValue({ model_catalog: [
      { model: 'tiered', price_state: 'conditional', price_covered: true },
    ], comparison: [], catalog_differences: [
      { credential_id: 'first', label: '渠道一', state: 'compared', group_id: 7,
        checked_at: '2026-11-20T00:00:00Z', website_only: ['only-on-website'], key_only: ['only-on-key'] },
      { credential_id: 'second', label: '渠道二', state: 'unverified', group_id: null,
        checked_at: null, website_only: [], key_only: [] },
    ] })
    const wrapper = view()
    await flushPromises()
    const panel = wrapper.get('.catalog-differences')
    expect(panel.get('summary').text()).toBe('目录差异')
    expect(panel.text()).toContain('网页有 Key 无（1）')
    expect(panel.text()).toContain('Key 有 网页无（1）')
    expect(panel.text()).toContain('更换为能访问这些模型的 Key')
    expect(panel.text()).toContain('补齐分组')
    expect(panel.text()).toContain('待核对')
    expect(panel.findAll('li').map(item => item.text())).toEqual(['only-on-website', 'only-on-key'])
    expect(wrapper.get('[aria-label="北辰供应商概览"]').text()).toContain('缺价模型 0 个')
  })

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
