import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import StudentKeys from './StudentKeys.vue'

vi.mock('../../api/student', () => ({
  studentAPI: {
    organizations: vi.fn().mockResolvedValue([{ id: 'org', name: 'Org', status: 'active' }]),
    models: vi.fn().mockResolvedValue({
      common_models: [{
        model_id: 'common-model',
        display_name: 'Common Model',
        description: null,
        category: 'text',
        capabilities: [],
        input_modalities: ['text'],
        output_modalities: ['text'],
        context_window: null,
        max_input_tokens: null,
        max_output_tokens: null,
        is_common: true,
        status: 'active',
        route_ready: true,
        credit_multiplier: '0.00',
        provider_name: 'gpt',
      }],
      library_models: [],
    }),
    keys: vi.fn().mockResolvedValue([]),
    projects: vi.fn().mockResolvedValue([{ id: 'project', organization_id: 'org', name: 'Project', status: 'active', has_mentor: true }]),
    submit: vi.fn(),
    key: vi.fn(),
    claim: vi.fn(),
  },
}))

describe('StudentKeys', () => {
  it('shows library models and no manual model-id form', async () => {
    const wrapper = mount(StudentKeys, { global: { stubs: { LoadingRegion: { template: '<div><slot/></div>' }, AppDialog: { props: ['open', 'title'], template: '<div v-if="open"><slot/></div>' }, StatusProgress: true, StatusBadge: true, EmptyState: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('Common Model')
    expect(wrapper.text()).not.toContain('从我的模型广场选择')
    expect(wrapper.text()).not.toContain('申请新模型')
    expect(wrapper.text()).not.toContain('模型名称（选填）')
  })

  it('lists active projects without mentor', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.projects).mockResolvedValueOnce([
      { id: 'no-mentor', organization_id: 'org', name: 'No Mentor Project', status: 'active', has_mentor: false },
      { id: 'with-mentor', organization_id: 'org', name: 'With Mentor', status: 'active', has_mentor: true },
    ])
    const wrapper = mount(StudentKeys, { global: { stubs: { LoadingRegion: { template: '<div><slot/></div>' }, AppDialog: true, StatusProgress: true, StatusBadge: true, EmptyState: true } } })
    await flushPromises()
    await wrapper.findAll('select')[0].setValue('org')
    await flushPromises()
    const options = wrapper.findAll('select')[1].findAll('option')
    expect(options.map(option => option.text())).toContain('No Mentor Project')
    expect(options.map(option => option.text())).toContain('With Mentor')
  })
})

describe('model selection', () => {
  it('filters models inline by search query', async () => {
    const wrapper = mount(StudentKeys, { global: { stubs: { LoadingRegion: { template: '<div><slot/></div>' }, AppDialog: true, StatusProgress: true, StatusBadge: true, EmptyState: true } } })
    await flushPromises()
    await wrapper.findAll('select')[0].setValue('org')
    await flushPromises()
    await wrapper.findAll('select')[1].setValue('project')
    await flushPromises()
    const search = wrapper.get('input[placeholder="搜索我的模型"]')
    await search.setValue('missing-model')
    expect(wrapper.text()).not.toContain('Common Model')
    await search.setValue('common')
    expect(wrapper.text()).toContain('Common Model')
  })
})

describe('key visibility', () => {
  it('shows only the prefix in the list while keeping copy and one-click setup in the detail dialog', async () => {
    const { studentAPI } = await import('../../api/student')
    const fullKey = 'neb_sk_abcdefghijklmnopqrstuvwxyz123456'
    const model = {
      model_id: 'gpt-5-codex',
      display_name: 'GPT-5 Codex',
      description: null,
      category: 'text',
      capabilities: ['coding'],
      input_modalities: ['text'],
      output_modalities: ['text'],
      context_window: 128000,
      max_input_tokens: null,
      max_output_tokens: null,
      is_common: false,
      status: 'active',
      route_ready: true,
      credit_multiplier: '1.00',
      provider_name: 'gpt',
      primary_adapter: 'openai_responses',
      responses_ready: true,
    }
    const key = {
      id: 'key-1',
      name: '我的 Key',
      status: 'active',
      route_ready: true,
      progress: { current: 'active', completed_steps: ['submitted', 'mentor_review', 'teacher_review', 'claimed'] },
      organization: { id: 'org', name: 'Org', status: 'active' },
      project: { id: 'project', organization_id: 'org', name: 'Project', status: 'active', has_mentor: true, monthly_credit_quota: '200.000' },
      models: [model],
      key_prefix: 'neb_sk_abcdefgh',
      // Even if a list payload accidentally contains the full secret, the list must still mask it.
      api_key: fullKey,
      requested_monthly_credits: '200.000',
      created_at: '2026-11-20T00:00:00Z',
      updated_at: '2026-11-20T00:00:00Z',
    }
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([key] as never)
    vi.mocked(studentAPI.key).mockResolvedValueOnce({ ...key, api_key: fullKey } as never)
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })

    const wrapper = mount(StudentKeys, {
      global: {
        stubs: {
          LoadingRegion: { template: '<div><slot/></div>' },
          AppDialog: { props: ['open', 'title'], template: '<section v-if="open" :data-title="title"><slot/></section>' },
          StatusProgress: true,
          StatusBadge: true,
          EmptyState: true,
          StudentHarnessConfig: { template: '<div data-testid="harness-config">配置内容</div>' },
        },
      },
    })
    await flushPromises()

    expect(wrapper.get('[data-testid="api-key-mask"]').text()).toBe('neb_sk_abcdefgh••••••')
    expect(wrapper.text()).not.toContain(fullKey)

    const viewButton = wrapper.findAll('button').find(button => button.text() === '查看密钥')
    expect(viewButton).toBeTruthy()
    await viewButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="api-key-detail"]').text()).toBe(fullKey)
    await wrapper.get('[data-testid="copy-api-key"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(fullKey)

    await wrapper.get('[data-testid="configure-api-key"]').trigger('click')
    expect(wrapper.find('[data-testid="harness-config"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('key availability explained in plain language', () => {
  const model = {
    model_id: 'claude-sonnet-4-5',
    display_name: 'Claude Sonnet 4.5',
    description: null,
    category: 'text',
    capabilities: [],
    input_modalities: ['text'],
    output_modalities: ['text'],
    context_window: 200000,
    max_input_tokens: null,
    max_output_tokens: null,
    is_common: false,
    status: 'active',
    route_ready: true,
    credit_multiplier: '1.10',
    provider_name: '北辰',
    supported_protocols: ['anthropic', 'openai_compatible'],
    recommended_protocol: 'openai_compatible',
  }
  const key = (overrides: Record<string, unknown>) => ({
    id: 'key-health',
    name: '健康检查 Key',
    status: 'active',
    route_ready: true,
    progress: { current: 'active', completed_steps: ['submitted', 'mentor_review', 'teacher_review', 'claimed'] },
    organization: { id: 'org', name: 'Org', status: 'active' },
    project: { id: 'project', organization_id: 'org', name: 'Project', status: 'active', has_mentor: true, monthly_credit_quota: '200.000' },
    models: [model],
    key_prefix: 'neb_sk_health',
    requested_monthly_credits: '200.000',
    created_at: '2026-11-20T00:00:00Z',
    updated_at: '2026-11-20T00:00:00Z',
    ...overrides,
  })
  const mountWithKey = async (value: Record<string, unknown>) => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([value] as never)
    const wrapper = mount(StudentKeys, {
      global: {
        stubs: {
          LoadingRegion: { template: '<div><slot/></div>' },
          AppDialog: { props: ['open', 'title'], template: '<section v-if="open"><slot/></section>' },
          StatusProgress: true,
          StatusBadge: true,
          EmptyState: true,
        },
      },
    })
    await flushPromises()
    return wrapper
  }

  it('explains credential, catalog, pricing and probe and lists what protocols the key supports', async () => {
    const wrapper = await mountWithKey(key({}))

    const guide = wrapper.get('[data-testid="key-health-guide"]').text()
    expect(guide).toContain('凭据')
    expect(guide).toContain('目录')
    expect(guide).toContain('报价')
    expect(guide).toContain('探活')
    expect(guide).toContain('不是实时探测')

    const credential = wrapper.get('[data-health="credential"]').text()
    expect(credential).toContain('已签发')
    expect(wrapper.get('[data-health="catalog"]').text()).toContain('已登记 1 个模型')
    const authorized = wrapper.get('[data-testid="authorized-models"]').text()
    expect(authorized).toContain(model.model_id)
    expect(wrapper.get('[data-testid="key-model-protocol"]').text()).toContain('支持协议：OpenAI Chat、Anthropic Messages')
    expect(wrapper.get('[data-testid="key-model-protocol"]').text()).toContain('推荐协议：OpenAI Chat')
    expect(wrapper.get('[data-health="pricing"]').text()).toContain('已配置 1 个模型')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('线路已打通')

    const protocols = wrapper.get('[data-testid="key-protocols"]').text()
    expect(protocols).toContain('Anthropic Messages')
    expect(protocols).toContain('OpenAI Chat')
    expect(wrapper.text()).toContain('网关不做协议转换')
    wrapper.unmount()
  })

  it('shows what is wrong and what to ask the teacher when the key is not usable', async () => {
    const brokenModel = { ...model, status: 'inactive', route_ready: false, credit_multiplier: null }
    const wrapper = await mountWithKey(key({ models: [brokenModel], route_ready: false, key_prefix: null }))

    expect(wrapper.get('[data-health="credential"]').text()).toContain('已生效，待生成')
    expect(wrapper.get('[data-health="catalog"]').text()).toContain('模型全部停用')
    expect(wrapper.get('[data-health="pricing"]').text()).toContain('缺少 1 个模型的报价')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('线路不可用')
    expect(wrapper.get('[data-health="credential"]').text()).toContain('找老师')
    expect(wrapper.get('[data-health="pricing"]').text()).toContain('找老师')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('找老师')
    wrapper.unmount()
  })
})
