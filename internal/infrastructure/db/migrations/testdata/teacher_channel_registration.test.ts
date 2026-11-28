import { flushPromises, mount } from '@vue/test-utils'
import TeacherProviders from '../../../../../frontend/src/views/teacher/TeacherProviders.vue'

const mocks = vi.hoisted(() => ({
  providers: vi.fn(), beichenOverview: vi.fn(), createProviderCredential: vi.fn(),
  get: vi.fn(), post: vi.fn(),
}))
vi.mock('../../../../../frontend/src/api/teacher', async original => ({
  ...await original(),
  teacherAPI: mocks,
}))
vi.mock('../../../../../frontend/src/api/client', async original => ({
  ...await original(), get: mocks.get, post: mocks.post,
}))

it('creates a Gemini school channel without selecting a predecessor', async () => {
  mocks.providers.mockResolvedValue([{
    id: 'north', name: '北辰', base_url: 'https://api.beichenapi.com', status: 'active',
    credentials: [
      { id: '1', label: 'CC1', status: 'active' }, { id: '2', label: 'G3', status: 'active' },
      { id: '3', label: 'GM1', status: 'active' },
    ],
  }])
  mocks.beichenOverview.mockResolvedValue({ model_catalog: [], comparison: [] })
  mocks.get.mockResolvedValue({
    credentials: ['CC1', 'G3', 'GM1'].map((label, index) => ({
      credential_id: String(index + 1), label, status: 'active', school_channel: label,
    })),
    counts: { enabled_keys: 3, enabled_models: 0, maintenance_models: 0, stale_routes: 0 },
    models: [], preview: null,
  })
  mocks.createProviderCredential.mockResolvedValue({ id: 'gemini', provider_id: 'north', label: '哈吉米', status: 'active' })
  const wrapper = mount(TeacherProviders, {
    global: { stubs: {
      AppDialog: { props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><slot /></section>' },
      BeichenSchoolModels: true, BeichenReconcile: true,
    } },
  })
  try {
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '渠道切换向导')!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[aria-label="渠道切换向导"]')
    expect((dialog.get('select').element as HTMLSelectElement).value).toBe('')
    await dialog.findAll('button').find(button => button.text() === '新建独立学校渠道')!.trigger('click')
    const form = dialog.get('form')
    expect(form.text()).toContain('1.10')
    await form.get('select').setValue('Gemini')
    const [label, key] = form.findAll('input')
    await label.setValue('哈吉米')
    await key.setValue('sk-local-frontend-fixture')
    await form.trigger('submit')
    await flushPromises()
    expect(mocks.createProviderCredential).toHaveBeenCalledExactlyOnceWith('north', {
      label: '哈吉米', credential: 'sk-local-frontend-fixture', status: 'active', channel: 'Gemini',
    })
    expect(mocks.post).not.toHaveBeenCalled()
    expect(dialog.text()).toContain('学校渠道已登记')
  } finally {
    wrapper.unmount()
  }
})
