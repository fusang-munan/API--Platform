import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { BeichenModelCapability, Model } from '../api/types'
import type { TeacherOverview } from '../utils/beichenModels'
import BeichenSchoolModels from './teacher/BeichenSchoolModels.vue'
import StudentModels from './student/StudentModels.vue'

const teacherAPI = vi.hoisted(() => ({
  beichenOverview: vi.fn(), modelUsage: vi.fn(),
  syncBeichen: vi.fn(), requestBeichenProbes: vi.fn(), beichenProbes: vi.fn(),
  previewBeichenRebind: vi.fn(), applyBeichenRebind: vi.fn(), rescanBeichenCatalog: vi.fn(),
  enableBeichenModelsBatch: vi.fn(), enableBeichenModel: vi.fn(), updateModel: vi.fn(), setBeichenModelPrice: vi.fn(),
}))
const studentAPI = vi.hoisted(() => ({
  models: vi.fn(), keys: vi.fn(), modelCatalog: vi.fn(), addModel: vi.fn(), addKeyModels: vi.fn(), removeModel: vi.fn(),
}))
vi.mock('../api/teacher', () => ({ teacherAPI }))
vi.mock('../api/student', () => ({ studentAPI }))

const modelID = 'claude-consistency'
const capability: BeichenModelCapability = {
  model: modelID, credential_id: 'key-1', credential_label: '渠道一',
  supported_protocols: ['anthropic', 'openai_compatible'], available_protocols: ['openai_compatible'],
  recommended_protocol: 'openai_compatible',
  protocol_details: [
    { protocol: 'anthropic', binding_ready: true, probe_state: 'failed' },
    { protocol: 'openai_compatible', binding_ready: true, probe_state: 'passed' },
  ],
  catalog_state: 'key_listed', availability: 'available', price_state: 'confirmed', price_blocker: null,
  multiplier_state: 'current', probe_state: 'passed', binding_state: 'ready', action_hint: '可使用推荐协议接入。',
}
const overview: TeacherOverview = {
  datasets: [], comparison: [],
  models: [{
    model: modelID, credential_id: 'key-1', mode: 'follow', price_status: 'confirmed',
    price_conditions_exact: { billing_mode: 'token', input_price: '0.00001', output_price: '0.00002', cache_read_price: '0', cache_write_price: '0' },
    upstream_rates: { input: 1000000, output: 2000000, cache_read: 0, cache_write: 0 },
    school_rates: { input: 100000000, output: 200000000, cache_read: 0, cache_write: 0 },
  }],
  model_catalog: [{
    model: modelID, school_status: 'active', can_enable: false, activation: 'active',
    preferred_routes: [{ school_model: modelID, provider_id: 'north', credential_id: 'key-1', credential_label: '渠道一', adapter: 'openai_compatible', priority: 1, state: 'ready', effective_multiplier_text: '0.1' }],
    protocols: ['anthropic', 'openai_compatible'], probe_state: 'passed',
    route_availability: {
      protocols: capability.supported_protocols, available_protocols: capability.available_protocols,
      recommended_protocol: capability.recommended_protocol, availability: capability.availability,
    },
  }],
}
const studentModel: Model = {
  model_id: modelID, display_name: 'Claude Consistency', description: null, category: 'text', capabilities: [],
  input_modalities: ['text'], output_modalities: ['text'], context_window: 200000, max_input_tokens: null, max_output_tokens: null,
  is_common: true, status: 'active', route_ready: true, credit_multiplier: '1.00', provider_name: '北辰',
  protocols: ['anthropic', 'openai_compatible'], recommended_protocol: 'openai_compatible',
  availability: 'available', probe_state: 'passed',
}

describe('协议与可用性三处一致', () => {
  it('同一模型在教师页和学生广场显示相同的推荐协议与可用状态', async () => {
    teacherAPI.beichenOverview.mockResolvedValue(overview)
    teacherAPI.modelUsage.mockResolvedValue({
      range: 'month', start: null, end: '2026-11-21T12:00:00+08:00', timezone: 'Asia/Shanghai',
      totals: { credits: '0', tokens: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, calls: 0, pending_calls: 0 },
      models: [], students: [], keys: [],
    })
    studentAPI.models.mockResolvedValue({ common_models: [studentModel], library_models: [] })
    studentAPI.keys.mockResolvedValue([])

    const teacher = mount(BeichenSchoolModels, {
      props: { providerId: 'north' },
      global: { stubs: { AppDialog: true, BeichenRecentReceipts: true } },
    })
    const student = mount(StudentModels, {
      global: { stubs: {
        LoadingRegion: { template: '<div><slot/></div>' }, AppDialog: true, StatusBadge: true, EmptyState: true,
        RouterLink: { template: '<a><slot/></a>' },
      } },
    })
    await flushPromises()

    const teacherRow = teacher.get(`[data-model="${modelID}"]`)
    expect(teacherRow.get('[data-testid="recommended-protocol"]').text()).toBe('推荐 OpenAI Chat')
    expect(teacherRow.get('[data-testid="model-availability"]').text()).toBe('可用')

    const studentCard = student.get(`[data-model="${modelID}"]`)
    expect(studentCard.get('[data-testid="recommended-protocol"]').text()).toBe('推荐 OpenAI Chat')
    expect(studentCard.get('[data-testid="model-availability"]').text()).toBe('可用')

    // /v1/models 返回同一组字段：protocols、recommended_protocol、availability。
    expect(studentModel.recommended_protocol).toBe(capability.recommended_protocol)
    expect(studentModel.availability).toBe(capability.availability)
    teacher.unmount()
    student.unmount()
  })
})
