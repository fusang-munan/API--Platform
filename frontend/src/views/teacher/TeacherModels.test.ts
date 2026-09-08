import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TeacherModels from './TeacherModels.vue'
import { teacherAPI } from '../../api/teacher'

vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn() }) }))
vi.mock('../../api/teacher', () => ({
  teacherAPI: {
    models: vi.fn(),
    providers: vi.fn(),
    model: vi.fn(),
    providerCatalogCandidates: vi.fn(),
    createModel: vi.fn(),
    updateModel: vi.fn(),
    createBinding: vi.fn(),
    updateBinding: vi.fn(),
  },
}))

const AppDialogStub = { props: ['open', 'title'], emits: ['update:open'], template: '<div v-if="open" role="dialog" :aria-label="title"><h2>{{title}}</h2><slot/></div>' }
const mountView = () => mount(TeacherModels, { global: { stubs: { LoadingRegion: { template: '<div><slot/></div>' }, AppDialog: AppDialogStub, StatusBadge: true } } })

describe('TeacherModels', () => {
  beforeEach(() => {
    const model = {
      model_id: 'm1', display_name: 'M1', description: 'Upstream description', category: 'text' as const,
      capabilities: [], input_modalities: [], output_modalities: [], context_window: null,
      max_input_tokens: null, max_output_tokens: null, is_common: true, status: 'active' as const,
      route_ready: true, credit_multiplier: '0.00',
    }
    vi.mocked(teacherAPI.models).mockResolvedValue([model])
    vi.mocked(teacherAPI.model).mockResolvedValue({ model, bindings: [] })
    vi.mocked(teacherAPI.providers).mockResolvedValue([{ id: 'provider-1', name: 'Relay', base_url: 'https://example.com', status: 'active', credential_configured: true, created_at: '2026-08-31T00:00:00Z', updated_at: '2026-08-31T00:00:00Z' }])
    vi.mocked(teacherAPI.providerCatalogCandidates).mockResolvedValue({ items: [], page: 1, page_size: 20, total: 0 })
  })

  it('opens governance step directly without quick-fill dialog', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '配置')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('治理配置')
    expect(wrapper.text()).toContain('Upstream description')
    expect(wrapper.text()).not.toContain('基础配置已有默认值')
    expect(wrapper.text()).not.toContain('输入模态')
    expect(wrapper.text()).not.toContain('一键配置')
  })

  it('opens route editor from governance step', async () => {
    vi.mocked(teacherAPI.model).mockResolvedValue({
      model: {
        model_id: 'm1', display_name: 'M1', description: 'Upstream description', category: 'text' as const,
        capabilities: [], input_modalities: [], output_modalities: [], context_window: null,
        max_input_tokens: null, max_output_tokens: null, is_common: true, status: 'active' as const,
        route_ready: true, credit_multiplier: '0.00',
      },
      bindings: [{
        id: 'binding-1', model_id: 'm1', provider_id: 'provider-1', upstream_model_name: 'm1',
        adapter: 'openai_compatible', priority: 100, status: 'active', created_at: '2026-08-31T00:00:00Z', updated_at: '2026-08-31T00:00:00Z',
      }],
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '配置')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('路由')
    await wrapper.findAll('button').find(button => button.text().includes('编辑路由'))!.trigger('click')
    const dialogs = wrapper.findAll('[role="dialog"]')
    expect(dialogs.some(dialog => dialog.attributes('aria-label') === '新增路由')).toBe(true)
  })

  it('advances to enable step after save and continue', async () => {
    vi.mocked(teacherAPI.updateModel).mockResolvedValue({
      model_id: 'm1', display_name: 'M1', description: 'Upstream description', category: 'text' as const,
      capabilities: [], input_modalities: [], output_modalities: [], context_window: null,
      max_input_tokens: null, max_output_tokens: null, is_common: true, status: 'pending_configuration' as const,
      route_ready: true, credit_multiplier: '0.00',
    })
    vi.mocked(teacherAPI.model).mockResolvedValue({
      model: {
        model_id: 'm1', display_name: 'M1', description: 'Upstream description', category: 'text' as const,
        capabilities: [], input_modalities: [], output_modalities: [], context_window: null,
        max_input_tokens: null, max_output_tokens: null, is_common: true, status: 'pending_configuration' as const,
        route_ready: true, credit_multiplier: '0.00',
      },
      bindings: [],
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '配置')!.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '保存并继续')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('启用模型')
    expect(wrapper.text()).not.toContain('保存并继续')
  })
})
