import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
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
      },
    },
  })
}

describe('StudentModels key whitelist', () => {
  it('marks models outside the current key and re-applies with the approved quota', async () => {
    const { studentAPI } = await import('../../api/student')
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.text()).toContain('需重新申领 Key')
    expect(wrapper.findAll('[data-testid="needs-new-key"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-testid="reapply-key"]')).toHaveLength(1)
    const button = wrapper.get('[data-testid="reapply-key"]')
    await button.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('claude-sonnet-4-5')
    expect(wrapper.text()).toContain('250.000 credits')
    await wrapper.get('[data-testid="confirm-reapply"]').trigger('click')
    await flushPromises()

    expect(studentAPI.withdraw).toHaveBeenCalledWith('key-1')
    expect(studentAPI.submit).toHaveBeenCalledWith({
      name: '我的 Key',
      organization_id: 'org',
      project_id: 'project',
      models: [{ model_id: 'gpt-5.3-codex' }, { model_id: 'claude-sonnet-4-5' }],
      requested_monthly_credits: '250.000',
    })
    wrapper.unmount()
  })

  it('explains why an active key cannot be withdrawn from the student page', async () => {
    const { studentAPI } = await import('../../api/student')
    vi.mocked(studentAPI.keys).mockResolvedValueOnce([fixture.key({ status: 'active', monthly_credits: '300.000' })] as never)
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.get('[data-testid="reapply-key"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('学生端不能直接撤回')
    expect(wrapper.find('[data-testid="confirm-reapply"]').exists()).toBe(false)
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
