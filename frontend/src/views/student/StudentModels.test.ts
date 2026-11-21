import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { studentAPI } from '../../api/student'
import StudentModels from './StudentModels.vue'

const fixture = vi.hoisted(() => {
  const model = (modelID: string, common = true, overrides: Record<string, unknown> = {}) => ({
    model_id: modelID,
    display_name: modelID,
    description: null,
    category: 'text',
    capabilities: [],
    input_modalities: ['text'],
    output_modalities: ['text'],
    context_window: 128000,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    is_common: common,
    status: 'active',
    route_ready: true,
    credit_multiplier: '1.00',
    provider_name: 'gpt',
    ...overrides,
  })
  const key = (overrides: Record<string, unknown>) => ({
    id: 'key-1',
    name: '我的 Key',
    status: 'pending_mentor',
    route_ready: false,
    progress: { current: 'mentor_review', completed_steps: ['submitted'] },
    organization: { id: 'org', name: 'Org', status: 'active' },
    project: { id: 'project', organization_id: 'org', name: 'Project', status: 'active', has_mentor: true, monthly_credit_quota: '500.000' },
    models: [model('gpt-5.3-codex', false)],
    requested_monthly_credits: '200.000',
    mentor_monthly_credits: '250.000',
    created_at: '2026-11-20T00:00:00Z',
    updated_at: '2026-11-20T00:00:00Z',
    ...overrides,
  })
  return { model, key }
})

vi.mock('../../api/student', () => ({
  studentAPI: {
    models: vi.fn(),
    keys: vi.fn(),
    modelCatalog: vi.fn(),
    addModel: vi.fn(),
    addKeyModels: vi.fn(),
    removeKeyModels: vi.fn(),
    removeModel: vi.fn(),
    withdraw: vi.fn(),
    submit: vi.fn(),
  },
}))

function activeKey(models: unknown[] = []) {
  return fixture.key({ status: 'active', models })
}

function mountPage() {
  return mount(StudentModels, {
    global: {
      stubs: {
        LoadingRegion: { template: '<div><slot/></div>' },
        AppDialog: { props: ['open', 'title'], template: '<section v-if="open" :data-title="title"><slot/></section>' },
        StatusBadge: true,
        EmptyState: true,
        RouterLink: { props: ['to'], template: '<a :href="to.name"><slot/></a>' },
      },
    },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  window.history.replaceState({}, '', '/')
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  vi.mocked(studentAPI.models).mockReset().mockResolvedValue({
    common_models: [fixture.model('gpt-5.3-codex'), fixture.model('claude-sonnet-4-5')],
    library_models: [],
  } as never)
  vi.mocked(studentAPI.keys).mockReset().mockResolvedValue([fixture.key({})] as never)
  vi.mocked(studentAPI.modelCatalog).mockReset().mockResolvedValue({ items: [], page: 1, page_size: 20, total: 0 } as never)
  vi.mocked(studentAPI.addModel).mockReset()
  vi.mocked(studentAPI.addKeyModels).mockReset()
  vi.mocked(studentAPI.removeKeyModels).mockReset()
  vi.mocked(studentAPI.removeModel).mockReset()
  vi.mocked(studentAPI.withdraw).mockReset().mockResolvedValue({ withdrawn: true } as never)
  vi.mocked(studentAPI.submit).mockReset().mockResolvedValue({} as never)
})

afterEach(() => {
  vi.restoreAllMocks()
  window.history.replaceState({}, '', '/')
})

describe('StudentModels single model list', () => {
  it('selects the active key from ?key= and remembers key switches', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([
      activeKey([fixture.model('gpt-5.3-codex', false)]),
      fixture.key({ id: 'second-project', name: '第二个 Key', status: 'active', models: [] }),
    ] as never)
    window.history.replaceState({}, '', '/?key=second-project')
    const wrapper = mountPage()
    await flushPromises()

    const selector = wrapper.get('[data-testid="current-key"]')
    expect((selector.element as HTMLSelectElement).value).toBe('second-project')
    expect(wrapper.findAll('.plaza-column')).toHaveLength(1)
    expect(wrapper.get('h2').text()).toBe('可用模型')
    expect(wrapper.find('[data-model="gpt-5.3-codex"] [data-testid="model-key-status"]').exists()).toBe(false)

    await selector.setValue('key-1')
    expect(window.location.search).toBe('?key=key-1')
    expect(wrapper.get('[data-model="gpt-5.3-codex"] [data-testid="model-key-status"]').text()).toBe('可调用')

    await selector.setValue('second-project')
    expect(window.location.search).toBe('?key=second-project')
    expect(wrapper.find('[data-model="gpt-5.3-codex"] [data-testid="model-key-status"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('adds a model directly to the current key without writing the personal library', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([fixture.model('gpt-5.3-codex', false)])] as never)
    vi.mocked(studentAPI.addKeyModels).mockResolvedValueOnce({
      model_ids: ['gpt-5.3-codex', 'claude-sonnet-4-5'], added_model_ids: ['claude-sonnet-4-5'],
    })
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.get('[data-model="claude-sonnet-4-5"]')
    await card.get('[data-testid="sync-key-model"]').trigger('click')
    await flushPromises()

    expect(studentAPI.addKeyModels).toHaveBeenLastCalledWith('key-1', ['claude-sonnet-4-5'])
    expect(studentAPI.addModel).not.toHaveBeenCalled()
    expect(studentAPI.withdraw).not.toHaveBeenCalled()
    expect(studentAPI.submit).not.toHaveBeenCalled()
    expect(card.get('[data-testid="model-key-status"]').text()).toBe('可调用')
    expect(card.get('[data-testid="model-key-status"]').classes()).toContain('ok')
    expect(card.find('[data-testid="sync-key-model"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows only the application entry when there is no active key', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([] as never)
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.get('[data-testid="apply-key"]').attributes('href')).toBe('student-api-keys')
    expect(wrapper.get('[data-testid="apply-key"]').text()).toBe('申请密钥')
    expect(wrapper.find('[data-testid="current-key"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="sync-key-model"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="remove-key-model"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('leaves pending applications intact and links to their status', async () => {
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.get('[data-testid="apply-key"]').text()).toBe('查看我的申请')
    expect(wrapper.text()).toContain('待导师审核')
    expect(wrapper.find('[data-testid="sync-key-model"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('StudentModels key model states', () => {
  it.each([
    ['model has no usable route for this API key', '没有可用线路'],
    ['model route is unverified', '没有通过探活'],
    ['model is missing a student price', '没有配置学校报价'],
  ])('distinguishes adding failure: %s', async (serverMessage, expected) => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    vi.mocked(studentAPI.addKeyModels).mockRejectedValueOnce(new APIError(serverMessage, 409, 'MODEL_NOT_READY'))
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.get('[data-model="claude-sonnet-4-5"] [data-testid="sync-key-model"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain(expected)
    expect(wrapper.find('[data-model="claude-sonnet-4-5"] [data-testid="model-key-status"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('allows an unverified model but shows a warning instead of a green callable badge', async () => {
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [fixture.model('claude-sonnet-4-5', true, {
        availability: 'unverified', availability_reason: '线路还未完成探活。',
      })],
      library_models: [],
    } as never)
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    vi.mocked(studentAPI.addKeyModels).mockResolvedValueOnce({ model_ids: ['claude-sonnet-4-5'], added_model_ids: ['claude-sonnet-4-5'] })
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.get('[data-model="claude-sonnet-4-5"]')
    expect(card.get('[data-testid="model-availability"]').text()).toBe('待验证')
    expect(card.get('[data-testid="model-availability"]').classes()).toContain('warn')
    await card.get('[data-testid="sync-key-model"]').trigger('click')
    await flushPromises()

    expect(studentAPI.addKeyModels).toHaveBeenLastCalledWith('key-1', ['claude-sonnet-4-5'])
    expect(card.get('[data-testid="model-key-status"]').text()).toBe('已加入 · 待验证')
    expect(card.get('[data-testid="model-key-status"]').classes()).toContain('warn')
    expect(wrapper.get('[role="status"]').text()).toContain('尚未通过探活')
    wrapper.unmount()
  })

  it('marks a maintenance model as joined but not callable and never uses green', async () => {
    const maintenance = fixture.model('gpt-5.3-codex', false, {
      availability: 'maintenance', availability_reason: '所有线路探活失败。',
    })
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [maintenance, fixture.model('gemini-2.5-pro', true, { availability: 'maintenance', availability_reason: '维护中。' })],
      library_models: [],
    } as never)
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([maintenance])] as never)
    const wrapper = mountPage()
    await flushPromises()

    const joined = wrapper.get('[data-model="gpt-5.3-codex"]')
    expect(joined.get('[data-testid="model-key-status"]').text()).toBe('已加入 · 维护中')
    expect(joined.get('[data-testid="model-key-status"]').classes()).toContain('unavailable')
    expect(joined.get('[data-testid="model-key-status"]').classes()).not.toContain('ok')
    expect(joined.get('[data-testid="model-availability"]').classes()).not.toContain('ok')
    expect(joined.get('[data-testid="remove-key-model"]').text()).toBe('从当前 Key 移除')
    expect(joined.find('[data-testid="sync-key-model"]').exists()).toBe(false)

    const notJoined = wrapper.get('[data-model="gemini-2.5-pro"]')
    expect(notJoined.get('[data-testid="sync-key-model"]').text()).toBe('维护中，暂不能加入')
    expect(notJoined.get('[data-testid="sync-key-model"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('removes only from the current key and does not delete the personal library entry', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([fixture.model('gpt-5.3-codex', false)])] as never)
    vi.mocked(studentAPI.removeKeyModels).mockResolvedValueOnce({ model_ids: [], removed_model_ids: ['gpt-5.3-codex'] })
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.get('[data-model="gpt-5.3-codex"]')
    await card.get('[data-testid="remove-key-model"]').trigger('click')
    await flushPromises()

    expect(window.confirm).toHaveBeenCalledWith('从当前 Key「我的 Key」移除 gpt-5.3-codex？')
    expect(studentAPI.removeKeyModels).toHaveBeenLastCalledWith('key-1', ['gpt-5.3-codex'])
    expect(studentAPI.removeModel).not.toHaveBeenCalled()
    expect(card.find('[data-testid="remove-key-model"]').exists()).toBe(false)
    expect(card.get('[data-testid="sync-key-model"]').text()).toBe('添加到我的 Key')
    expect(wrapper.text()).toContain('当前 Key 已加入 0 个模型')
    wrapper.unmount()
  })
})

describe('StudentModels protocol guidance', () => {
  it('shows supported protocols and suggests alternatives for a mismatch', async () => {
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [
        { ...fixture.model('gpt-5.3-codex'), supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...fixture.model('claude-sonnet-4-5'), supported_protocols: ['anthropic'] },
      ],
      library_models: [],
    } as never)
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    const wrapper = mountPage()
    await flushPromises()

    const chips = wrapper.findAll('[data-testid="protocol-chip"]').map(chip => chip.text())
    expect(chips).toContain('Chat')
    expect(chips).toContain('Responses')
    expect(chips).toContain('Anthropic')
    let warnings = wrapper.findAll('[data-testid="protocol-alternatives"]')
    expect(warnings).toHaveLength(1)
    expect(warnings[0].text()).toContain('Anthropic Messages')
    expect(warnings[0].text()).toContain('网关不做协议转换')

    await wrapper.get('[data-testid="protocol-select"]').setValue('anthropic')
    warnings = wrapper.findAll('[data-testid="protocol-alternatives"]')
    expect(warnings).toHaveLength(1)
    expect(warnings[0].text()).toContain('OpenAI Chat / Completions')
    wrapper.unmount()
  })

  it('reads protocols and the recommended protocol from the model response instead of guessing by name', async () => {
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [
        { ...fixture.model('claude-sonnet-4-5'), protocols: ['openai_compatible'], recommended_protocol: 'openai_compatible' },
        { ...fixture.model('gpt-5.3-codex'), protocols: ['openai_compatible', 'openai_responses'], recommended_protocol: 'openai_responses' },
        { ...fixture.model('deepseek-chat'), protocols: ['anthropic'], recommended_protocol: 'anthropic' },
        { ...fixture.model('gemini-2.5-pro'), protocols: [], recommended_protocol: null, availability: 'unverified' },
      ],
      library_models: [],
    } as never)
    const wrapper = mountPage()
    await flushPromises()

    const recommendations = wrapper.findAll('[data-testid="recommended-protocol"]').map(chip => chip.text())
    expect(recommendations.sort()).toEqual(['推荐 Anthropic Messages', '推荐 OpenAI Chat', '推荐 OpenAI Responses'])
    const claude = wrapper.get('[data-model="claude-sonnet-4-5"]')
    expect(claude.findAll('[data-testid="protocol-chip"]').map(chip => chip.text())).toEqual(['Chat'])
    expect(wrapper.findAll('[data-testid="protocol-note"]')).toHaveLength(4)
    expect(wrapper.text()).toContain('网关不做协议转换')
    wrapper.unmount()
  })

  it('marks models without a usable route and explains why they cannot be used yet', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [{ ...fixture.model('claude-sonnet-4-5'), route_ready: false }],
      library_models: [],
    } as never)
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.get('[data-testid="model-availability"]').text()).toBe('维护中')
    const reason = wrapper.get('[data-testid="model-unavailable-reason"]').text()
    expect(reason).toContain('等待老师恢复线路')
    const sync = wrapper.get('[data-testid="sync-key-model"]')
    expect(sync.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('shows the backend availability reason and refuses to add a maintenance model', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [{
        ...fixture.model('claude-sonnet-4-5'),
        protocols: ['anthropic'], recommended_protocol: null,
        availability: 'maintenance', availability_reason: '维护中。所有已配置线路最近一次探活均失败。',
      }],
      library_models: [],
    } as never)
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.get('[data-model="claude-sonnet-4-5"]')
    expect(card.get('[data-testid="model-availability"]').text()).toBe('维护中')
    expect(card.get('[data-testid="model-unavailable-reason"]').text()).toBe('维护中。所有已配置线路最近一次探活均失败。')
    expect(card.find('[data-testid="recommended-protocol"]').exists()).toBe(false)
    expect(card.get('[data-testid="sync-key-model"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})

describe('StudentModels catalog dialog', () => {
  const base = {
    display_name: null,
    description: null,
    owned_by: null,
    upstream_object: null,
    upstream_created_at: null,
    provider_name: 'gpt',
    credit_multiplier: '1.00',
    raw_entry: {},
  }

  it('shows callable, unverified and maintenance coverage plus catalogue warnings', async () => {
    const unverified = fixture.model('claude-opus-4', false, { availability: 'unverified', availability_reason: '还没有通过探活。' })
    const maintenance = fixture.model('gemini-2.5-pro', false, { availability: 'maintenance', availability_reason: '维护中。' })
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([
      fixture.model('gpt-5.3-codex', false),
      unverified,
      maintenance,
    ])] as never)
    vi.mocked(studentAPI.modelCatalog).mockResolvedValueOnce({
      items: [
        { ...base, model_id: 'gpt-5.3-codex', supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...base, model_id: 'deepseek-chat', supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...base, model_id: 'claude-opus-4', supported_protocols: ['anthropic'], availability: 'unverified', availability_reason: '还没有通过探活。' },
        { ...base, model_id: 'gemini-2.5-pro', availability: 'maintenance', availability_reason: '维护中。' },
        { ...base, model_id: 'maintenance-new', availability: 'maintenance', availability_reason: '维护中。' },
        { ...base, model_id: 'unverified-new', availability: 'unverified', availability_reason: '还没有通过探活。' },
      ],
      page: 1,
      page_size: 20,
      total: 6,
    } as never)
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '检索添加')!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="catalog-protocol-note"]').text()).toContain('网关不做协议转换')
    expect(wrapper.get('[data-testid="catalog-key-note"]').text()).toContain('不再先加入个人模型库')
    const coverage = wrapper.findAll('[data-testid="catalog-key-coverage"]')
    expect(coverage.map(chip => chip.text())).toEqual([
      '可调用', '未加入', '已加入 · 待验证', '已加入 · 维护中', '未加入', '未加入',
    ])
    expect(coverage[2].classes()).toContain('warn')
    expect(coverage[3].classes()).toContain('unavailable')
    expect(coverage[3].classes()).not.toContain('ok')
    expect(wrapper.get('[data-testid="catalog-unverified"]').text()).toContain('还没有通过探活')
    expect(wrapper.findAll('[data-testid="catalog-unavailable"]').map(item => item.text())).toContain('维护中。')

    const addButtons = wrapper.findAll('[data-testid="catalog-add"]')
    expect(addButtons.map(button => button.text())).toEqual([
      '可调用', '添加到我的 Key', '已加入 · 待验证', '已加入 · 维护中', '维护中，暂不能加入', '添加到我的 Key',
    ])
    expect(addButtons[0].attributes('disabled')).toBeDefined()
    expect(addButtons[1].attributes('disabled')).toBeUndefined()
    expect(addButtons[2].attributes('disabled')).toBeDefined()
    expect(addButtons[3].attributes('disabled')).toBeDefined()
    expect(addButtons[4].attributes('disabled')).toBeDefined()
    expect(addButtons[5].attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('adds directly to the key and explains the authorization failure before a retry', async () => {
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([activeKey([])] as never)
    vi.mocked(studentAPI.modelCatalog).mockResolvedValueOnce({
      items: [{ ...base, model_id: 'deepseek-chat', supported_protocols: ['openai_compatible'] }],
      page: 1, page_size: 20, total: 1,
    } as never)
    vi.mocked(studentAPI.addKeyModels)
      .mockRejectedValueOnce(new APIError('model has no usable route for this API key', 409, 'MODEL_NOT_READY'))
      .mockResolvedValueOnce({ model_ids: ['deepseek-chat'], added_model_ids: ['deepseek-chat'] })
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '检索添加')!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="catalog-add"]').trigger('click')
    await flushPromises()
    expect(studentAPI.addKeyModels).toHaveBeenLastCalledWith('key-1', ['deepseek-chat'])
    expect(studentAPI.addModel).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('没有可用线路')
    expect(wrapper.get('[data-testid="catalog-key-coverage"]').text()).toBe('未加入')

    await wrapper.get('[data-testid="catalog-add"]').trigger('click')
    await flushPromises()
    expect(studentAPI.addModel).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="catalog-key-coverage"]').text()).toBe('可调用')
    wrapper.unmount()
  })
})
