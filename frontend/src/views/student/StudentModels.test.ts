import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import StudentModels from './StudentModels.vue'

const fixture = vi.hoisted(() => {
  const model = (modelID: string, common = true) => ({
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
    models: vi.fn().mockResolvedValue({
      common_models: [fixture.model('gpt-5.3-codex'), fixture.model('claude-sonnet-4-5')],
      library_models: [],
    }),
    keys: vi.fn().mockResolvedValue([fixture.key({})]),
    modelCatalog: vi.fn(),
    addModel: vi.fn(),
    addKeyModels: vi.fn(),
    removeModel: vi.fn(),
    withdraw: vi.fn().mockResolvedValue({ withdrawn: true }),
    submit: vi.fn().mockResolvedValue({}),
  },
}))

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

describe('StudentModels key whitelist', () => {
  it('syncs the model to the selected active key without reapplying', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([
      fixture.key({ id: 'pending' }),
      fixture.key({ status: 'active' }),
      fixture.key({ id: 'second-project', status: 'active', models: [] }),
    ] as never)
    vi.mocked(studentAPI.addKeyModels).mockResolvedValueOnce({
      model_ids: ['gpt-5.3-codex', 'claude-sonnet-4-5'], added_model_ids: ['claude-sonnet-4-5'],
    })
    const wrapper = mountPage()
    await flushPromises()

    const selector = wrapper.get('[data-testid="current-key"]')
    expect(selector.findAll('option').map(option => option.attributes('value'))).toEqual(['key-1', 'second-project'])
    await selector.setValue('second-project')
    await selector.setValue('key-1')
    expect(wrapper.get('[data-testid="sync-key-model"]').text()).toBe('添加到我的 Key')
    await wrapper.get('[data-testid="sync-key-model"]').trigger('click')
    await flushPromises()

    expect(studentAPI.addKeyModels).toHaveBeenLastCalledWith('key-1', ['claude-sonnet-4-5'])
    expect(studentAPI.withdraw).not.toHaveBeenCalled()
    expect(studentAPI.submit).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="sync-key-model"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="model-synced"]')).toHaveLength(2)
    await selector.setValue('second-project')
    expect(wrapper.findAll('[data-testid="model-synced"]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('links to the application page when the student has no key', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([])
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.get('[data-testid="apply-key"]').attributes('href')).toBe('student-api-keys')
    expect(wrapper.get('[data-testid="apply-key"]').text()).toBe('申请密钥')
    expect(wrapper.find('[data-testid="sync-key-model"]').exists()).toBe(false)
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

  it('shows supported protocols and suggests alternatives for a mismatch', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [
        { ...fixture.model('gpt-5.3-codex'), supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...fixture.model('claude-sonnet-4-5'), supported_protocols: ['anthropic'] },
      ],
      library_models: [],
    } as never)
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
})

describe('StudentModels protocol guidance', () => {
  it('reads protocols and the recommended protocol from the model response instead of guessing by name', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.models).mockResolvedValueOnce({
      common_models: [
        { ...fixture.model('claude-sonnet-4-5'), protocols: ['openai_compatible'], recommended_protocol: 'openai_compatible' },
        { ...fixture.model('gpt-5.3-codex'), protocols: ['openai_compatible', 'openai_responses'], recommended_protocol: 'openai_responses' },
        { ...fixture.model('deepseek-chat'), protocols: ['anthropic'], recommended_protocol: 'anthropic' },
        { ...fixture.model('gemini-2.5-pro'), protocols: [], recommended_protocol: null },
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
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([fixture.key({ status: 'active' })] as never)
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
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([fixture.key({ status: 'active' })] as never)
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
  it('shows protocol tags, current key coverage and disables adding an unavailable model', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([fixture.key({ status: 'active' })] as never)
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
    vi.mocked(studentAPI.modelCatalog).mockResolvedValueOnce({
      items: [
        { ...base, model_id: 'gpt-5.3-codex', supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...base, model_id: 'deepseek-chat', supported_protocols: ['openai_compatible', 'openai_responses'] },
        { ...base, model_id: 'claude-opus-4', supported_protocols: ['anthropic'] },
        { ...base, model_id: 'gemini-2.5-pro', supported_protocols: ['google_gemini_v1beta'], route_ready: false },
      ],
      page: 1,
      page_size: 20,
      total: 4,
    } as never)
    const wrapper = mountPage()
    await flushPromises()
    const openButton = wrapper.findAll('button').find(button => button.text() === '检索添加')
    expect(openButton).toBeTruthy()
    await openButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="catalog-protocol-note"]').text()).toContain('网关不做协议转换')
    expect(wrapper.get('[data-testid="catalog-key-note"]').text()).toContain('当前 Key')
    const coverage = wrapper.findAll('[data-testid="catalog-key-coverage"]').map(chip => chip.text())
    expect(coverage).toContain('已同步')
    expect(coverage).toContain('待同步')
    const protocolChips = wrapper.findAll('[data-testid="catalog-protocol-chip"]').map(chip => chip.text())
    expect(protocolChips).toContain('Anthropic')
    expect(protocolChips).toContain('Responses')

    const addButtons = wrapper.findAll('[data-testid="catalog-add"]')
    expect(addButtons).toHaveLength(4)
    expect(addButtons[0].text()).toBe('已添加')
    expect(addButtons[1].text()).toBe('添加到我的 Key')
    expect(addButtons[2].text()).toBe('添加到我的 Key')
    expect(addButtons[3].text()).toBe('暂不可用')
    expect(addButtons[3].attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="catalog-unavailable"]').text()).toContain('等待老师恢复')
    wrapper.unmount()
  })

  it('adds a catalog model to the plaza and shows the authorization failure before a retry', async () => {
    const { studentAPI } = await import('../../api/student')
    const model = fixture.model('deepseek-chat', false)
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([fixture.key({ status: 'active' })] as never)
    vi.mocked(studentAPI.modelCatalog).mockResolvedValueOnce({
      items: [{ ...model, raw_entry: {} }], page: 1, page_size: 20, total: 1,
    } as never)
    vi.mocked(studentAPI.addModel).mockResolvedValueOnce(model as never)
    vi.mocked(studentAPI.addKeyModels)
      .mockRejectedValueOnce(new APIError('model has no usable route for this API key', 409, 'MODEL_NOT_READY'))
      .mockResolvedValueOnce({ model_ids: ['deepseek-chat', 'gpt-5.3-codex'], added_model_ids: ['deepseek-chat'] })
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '检索添加')!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="catalog-add"]').trigger('click')
    await flushPromises()
    expect(studentAPI.addModel).toHaveBeenLastCalledWith('deepseek-chat')
    expect(studentAPI.addKeyModels).toHaveBeenLastCalledWith('key-1', ['deepseek-chat'])
    expect(wrapper.get('[role="alert"]').text()).toContain('该模型暂时没有可用线路')
    expect(wrapper.get('[data-testid="catalog-key-coverage"]').text()).toBe('待同步')
    const libraryWrites = vi.mocked(studentAPI.addModel).mock.calls.length

    await wrapper.get('[data-testid="catalog-add"]').trigger('click')
    await flushPromises()
    expect(studentAPI.addModel).toHaveBeenCalledTimes(libraryWrites)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="catalog-key-coverage"]').text()).toBe('已同步')
    wrapper.unmount()
  })
})
