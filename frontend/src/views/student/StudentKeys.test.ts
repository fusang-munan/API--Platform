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
