import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { ApiKey, Model, Organization, Project } from '../../api/types'
import StudentKeys from './StudentKeys.vue'

const studentPath = '/api/v1/student'
const keysPath = `${studentPath}/api-keys`
const timestamp = '2026-11-20T01:00:00Z'
const organization: Organization = { id: 'org', name: 'Org', status: 'active' }
const project: Project = {
  id: 'project', organization_id: organization.id, name: 'Project', status: 'active',
  has_mentor: true, monthly_credit_quota: '200.000',
}
const readyModel: Model = {
  model_id: 'claude-sonnet-4-5', display_name: 'Claude Sonnet 4.5', category: 'text',
  capabilities: [], input_modalities: ['text'], output_modalities: ['text'], is_common: false,
  status: 'active', route_ready: true, credit_multiplier: '1.10', provider_name: '北辰',
  supported_protocols: ['anthropic', 'openai_compatible'], recommended_protocol: 'openai_compatible',
  availability: 'available', probe_state: 'passed', last_probe_at: timestamp,
}

function makeKey(overrides: Partial<ApiKey> = {}): ApiKey {
  return {
    id: 'key-1', name: '研究用 Key', status: 'active', route_ready: true, organization, project,
    progress: { current: 'active', completed_steps: ['submitted', 'mentor_review', 'teacher_review', 'claimed'] },
    models: [readyModel], key_prefix: 'neb_sk_test', requested_monthly_credits: '200.000',
    created_at: timestamp, updated_at: timestamp, ...overrides,
  }
}

function mockStudentRequests(routes: Record<string, unknown> = {}) {
  const requests: Array<{ method: string; path: string; body: unknown }> = []
  const responses = new Map(Object.entries({
    [`GET ${studentPath}/organizations`]: [organization],
    [`GET ${studentPath}/organizations/${organization.id}/projects`]: [project],
    [`GET ${keysPath}`]: [],
    [`GET ${studentPath}/usage`]: { month: '2026-11', projects: [] },
    ...routes,
  }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const path = new URL(input, 'https://nebula.test').pathname
    const method = init?.method ?? 'GET'
    const route = `${method} ${path}`
    requests.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (!responses.has(route)) throw new Error(`Unexpected request ${route}`)
    const data = responses.get(route)
    return data instanceof Response ? data : Response.json({ data })
  }))
  return { requests, responses }
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/student/api-keys', component: StudentKeys },
      { path: '/student/models', name: 'student-models', component: { template: '<div />' } },
    ],
  })
  await router.push('/student/api-keys')
  const wrapper = mount(StudentKeys, {
    global: {
      plugins: [router],
      stubs: {
        LoadingRegion: { template: '<div><slot /></div>' },
        AppDialog: { props: ['open', 'title'], template: '<section v-if="open" :data-title="title"><slot /></section>' },
        StatusProgress: true,
        StudentHarnessConfig: { template: '<div data-testid="harness-config">配置内容</div>' },
      },
    },
  })
  await flushPromises()
  return { wrapper, router }
}

enableAutoUnmount(afterEach)
afterEach(() => vi.unstubAllGlobals())

describe('学生 Key 申请与领取', () => {
  it('只提交组织、项目、名称和额度，携带新 Key ID 跳转模型广场', async () => {
    const created = makeKey({
      id: 'new-key', status: 'pending_mentor', models: [], key_prefix: null, route_ready: false,
      progress: { current: 'mentor_review', completed_steps: ['submitted'] },
    })
    const { requests, responses } = mockStudentRequests({ [`POST ${keysPath}`]: created })
    const { wrapper, router } = await mountPage()
    const form = wrapper.get('form')
    expect(form.find('fieldset').exists()).toBe(false)
    expect(form.findAll('input')).toHaveLength(2)
    expect(form.get('button').attributes()).toHaveProperty('disabled')

    await form.findAll('select')[0].setValue(organization.id)
    await flushPromises()
    await form.findAll('select')[1].setValue(project.id)
    await form.get('input[maxlength]').setValue('新申请')
    await form.get('input[inputmode="decimal"]').setValue('150.500')
    expect(form.get('button').attributes()).not.toHaveProperty('disabled')
    responses.set(`GET ${keysPath}`, [created])
    await form.trigger('submit')
    await flushPromises()

    expect(requests.filter(request => request.method === 'POST')).toEqual([{
      method: 'POST', path: keysPath,
      body: { organization_id: 'org', project_id: 'project', name: '新申请', requested_monthly_credits: '150.500' },
    }])
    expect(requests.some(request => request.path.startsWith(`${studentPath}/models`))).toBe(false)
    const link = wrapper.get('[data-testid="submitted-key-models"]')
    expect(link.text()).toBe('去模型广场选择模型')
    expect(link.attributes('href')).toBe('/student/models?key=new-key')
    await link.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/student/models?key=new-key')
  })

  it('保留没有导师的在用项目', async () => {
    mockStudentRequests({
      [`GET ${studentPath}/organizations/${organization.id}/projects`]: [
        { ...project, id: 'no-mentor', name: 'No Mentor Project', has_mentor: false },
        project,
      ],
    })
    const { wrapper } = await mountPage()
    await wrapper.findAll('select')[0].setValue(organization.id)
    await flushPromises()
    const options = wrapper.findAll('select')[1].findAll('option')
    expect(options.map(option => option.text())).toEqual(['请选择', 'No Mentor Project', 'Project'])
  })

  it('领取成功后展示密钥和指向同一把 Key 的模型广场入口', async () => {
    const approved = makeKey({
      id: 'approved-key', status: 'approved', models: [], key_prefix: null, route_ready: false,
      progress: { current: 'claim', completed_steps: ['submitted', 'mentor_review'] },
    })
    const claimed = { api_key: 'neb_sk_fake_claimed_key', key_prefix: 'neb_sk_fake', claimed_at: timestamp }
    const claimPath = `${keysPath}/${approved.id}/claim`
    const { requests, responses } = mockStudentRequests({
      [`GET ${keysPath}`]: [approved],
      [`POST ${claimPath}`]: claimed,
    })
    const { wrapper } = await mountPage()
    const claimButton = wrapper.findAll('button').find(button => button.text() === '领取并启用')!
    await claimButton.trigger('click')
    // 列表刷新失败时，领取响应仍须提供密钥和入口。
    responses.set(`GET ${keysPath}`, Response.json({}, { status: 503 }))
    await wrapper.get('[data-title="确认领取密钥"] button').trigger('click')
    await flushPromises()

    expect(requests).toContainEqual({ method: 'POST', path: claimPath, body: undefined })
    const detail = wrapper.get('[data-title="申请详情"]')
    expect(detail.get('[data-testid="api-key-detail"]').text()).toBe(claimed.api_key)
    expect(detail.get('[data-testid="detail-key-models"]').attributes('href')).toBe('/student/models?key=approved-key')
    expect(detail.get('[data-testid="detail-key-models"]').text()).toBe('去模型广场选择模型')
    expect(wrapper.get('[data-testid="empty-key-whitelist"]').text()).toBe('已生效，待选择模型')
  })

  it('列表遮盖密钥，详情保留复制和一键配置', async () => {
    const key = makeKey({
      api_key: 'neb_sk_fake_detail_key',
      models: [{
        ...readyModel, model_id: 'gpt-5-codex', display_name: 'GPT-5 Codex',
        primary_adapter: 'openai_responses', responses_ready: true,
      }],
    })
    mockStudentRequests({ [`GET ${keysPath}`]: [key], [`GET ${keysPath}/${key.id}`]: key })
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const { wrapper } = await mountPage()

    expect(wrapper.get('[data-testid="api-key-mask"]').text()).toBe('neb_sk_test••••••')
    expect(wrapper.text()).not.toContain(key.api_key)
    await wrapper.findAll('button').find(button => button.text() === '查看密钥')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="api-key-detail"]').text()).toBe(key.api_key)
    await wrapper.get('[data-testid="copy-api-key"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(key.api_key)
    await wrapper.get('[data-testid="configure-api-key"]').trigger('click')
    expect(wrapper.find('[data-testid="harness-config"]').exists()).toBe(true)
  })
})

describe('Key 白名单', () => {
  it('只显示本月已用积分和余额，不显示单价表', async () => {
    const key = makeKey({ api_key: 'neb_sk_fake_credit_key' })
    mockStudentRequests({
      [`GET ${keysPath}`]: [key],
      [`GET ${keysPath}/${key.id}`]: key,
      [`GET ${studentPath}/usage`]: {
        month: '2026-11',
        projects: [{
          project_id: project.id, project_name: project.name, quota: '200.000', used: '12.500', pending: '0.000', available: '187.500',
          keys: [{
            id: key.id, name: key.name, status: 'active', used: '12.500', pending: '0.000', available: '187.500',
            models: [{ id: readyModel.model_id, name: readyModel.display_name, credits: '12.500', calls: 3 }],
          }],
        }],
      },
    })
    const { wrapper } = await mountPage()

    const card = wrapper.get('.key-card')
    const summary = card.get('[data-testid="key-credit-summary"]').text()
    expect(summary).toContain('本月已用 12.5 积分')
    expect(summary).toContain('余额 187.5 积分')
    expect(card.text()).not.toContain('点/次')
    expect(card.text()).not.toContain('基础积分')
    expect(card.text()).not.toContain('报价')

    await card.findAll('button').find(button => button.text() === '查看密钥')!.trigger('click')
    await flushPromises()
    const detail = wrapper.get('[data-title="申请详情"]')
    expect(detail.text()).toContain('推荐 OpenAI Chat')
    expect(detail.text()).not.toContain('点/次')
  })

  it('保留九条旧授权，显示两条可调用和七条需处理并说明每一行', async () => {
    const models: Model[] = [
      readyModel,
      { ...readyModel, model_id: 'gemini-available' },
      {
        ...readyModel, model_id: 'gpt-unverified', availability: 'unverified',
        availability_reason: '尚未通过探活。', probe_state: 'unprobed', last_probe_at: null,
      },
      ...Array.from({ length: 6 }, (_, index): Model => ({
        ...readyModel, model_id: `maintenance-${index + 1}`, availability: 'maintenance',
        availability_reason: `维护原因 ${index + 1}，最近一次探活失败。`, probe_state: 'failed',
        recommended_protocol: null,
      })),
    ]
    const { requests } = mockStudentRequests({ [`GET ${keysPath}`]: [makeKey({ models })] })
    const { wrapper } = await mountPage()

    expect(wrapper.get('[data-testid="key-whitelist"] h3').text()).toBe('Key 白名单')
    expect(wrapper.get('[data-testid="key-whitelist-summary"]').text()).toBe('已登记 9 · 可调用 2 · 需处理 7')
    expect(wrapper.get('[data-health="catalog"]').text()).toContain('已登记 9 个模型')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('已确认可调用 2 个模型')
    const rows = wrapper.findAll('[data-testid="key-model-row"]')
    expect(rows.map(row => row.attributes('data-model'))).toEqual(models.map(model => model.model_id))
    expect(rows.map(row => row.attributes('data-availability'))).toEqual([
      'available', 'available', 'unverified', ...Array(6).fill('maintenance'),
    ])
    for (const [index, model] of models.entries()) {
      const row = rows[index]
      expect(row.text()).toContain('支持协议：OpenAI Chat、Anthropic Messages')
      expect(row.text()).toContain(`推荐协议：${model.recommended_protocol ? 'OpenAI Chat' : '暂无'}`)
      expect(row.text()).toContain(model.availability_reason ?? '已有可调用线路。')
      expect(row.text()).toContain('最后探活')
      expect(row.get('button').text()).toBe('从当前 Key 移除')
      if (model.last_probe_at) expect(row.get('time').attributes('datetime')).toBe(model.last_probe_at)
    }
    expect(rows[2].text()).toContain('待验证')
    expect(rows[2].text()).toContain('可尝试调用')
    expect(rows[2].text()).toContain('暂无记录')
    expect(rows[3].text()).toContain('维护中')
    expect(requests.every(request => request.method === 'GET')).toBe(true)
  })

  it('把 degraded 授权计为可调用并显示探活异常，而不是维护中', async () => {
    const degraded: Model = {
      ...readyModel, model_id: 'gpt-degraded', availability: 'degraded', probe_state: 'failed',
      availability_reason: '最近一次探活失败，但该模型此前长期可用；仍可尝试调用，请求可能失败。',
    }
    const models = [readyModel, degraded]
    mockStudentRequests({ [`GET ${keysPath}`]: [makeKey({ models })] })
    const { wrapper } = await mountPage()

    expect(wrapper.get('[data-testid="key-whitelist-summary"]').text())
      .toBe('已登记 2 · 可调用 2（探活异常 1） · 需处理 0')
    const rows = wrapper.findAll('[data-testid="key-model-row"]')
    expect(rows.map(row => row.attributes('data-availability'))).toEqual(['available', 'degraded'])
    const row = rows[1]
    expect(row.text()).toContain('可用 · 探活异常')
    expect(row.text()).not.toContain('维护中')
    expect(row.text()).toContain('请求可能失败')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('其中 1 个最近探活异常')
  })

  it('只从所选 Key 移除指定模型，移除最后一个时显示待选择模型', async () => {
    const model = { ...readyModel, availability: 'maintenance', probe_state: 'failed' }
    const key = makeKey({ id: 'target-key', models: [model], route_ready: false })
    const otherKey = makeKey({ id: 'other-key', status: 'inactive', models: [model] })
    const removePath = `${keysPath}/${key.id}/models/remove`
    const { requests } = mockStudentRequests({
      [`GET ${keysPath}`]: [key, otherKey],
      [`POST ${removePath}`]: { model_ids: [], removed_model_ids: [model.model_id] },
    })
    const { wrapper } = await mountPage()
    const [first, second] = wrapper.findAll('.key-card')
    await first.get('[data-testid="key-model-row"] button').trigger('click')
    await flushPromises()

    expect(requests.filter(request => request.method === 'POST')).toEqual([{
      method: 'POST', path: removePath, body: { model_ids: [model.model_id] },
    }])
    expect(first.find('[data-testid="key-model-row"]').exists()).toBe(false)
    expect(first.get('[data-testid="empty-key-whitelist"]').text()).toBe('已生效，待选择模型')
    expect(first.get('[data-testid="key-whitelist-summary"]').text()).toBe('已登记 0 · 可调用 0 · 需处理 0')
    const otherRow = second.get('[data-testid="key-model-row"]')
    expect(otherRow.attributes('data-model')).toBe(model.model_id)
    expect(otherRow.get('button').text()).toBe('从当前 Key 移除')
  })

  it('移除失败时保留原授权和移除按钮', async () => {
    const key = makeKey()
    mockStudentRequests({
      [`GET ${keysPath}`]: [key],
      [`POST ${keysPath}/${key.id}/models/remove`]: Response.json({
        error: { code: 'FORBIDDEN', message: 'forbidden' },
      }, { status: 403 }),
    })
    const { wrapper } = await mountPage()
    await wrapper.get('[data-testid="key-model-row"] button').trigger('click')
    await flushPromises()

    expect(wrapper.get('.error.banner').text()).toContain('没有执行此操作的权限')
    expect(wrapper.get('[data-testid="key-model-row"] button').attributes()).not.toHaveProperty('disabled')
    expect(wrapper.get('[data-testid="key-whitelist-summary"]').text()).toBe('已登记 1 · 可调用 1 · 需处理 0')
  })

  it.each([
    { label: '空数组', models: [] }, { label: '空值', models: null },
  ])('白名单返回$label时仍显示为生效，并引导选择模型', async ({ models }) => {
    const key = { ...makeKey({ route_ready: false }), models }
    mockStudentRequests({
      [`GET ${keysPath}`]: [key],
      [`GET ${keysPath}/${key.id}`]: { ...key, api_key: 'neb_sk_fake_empty_key' },
    })
    const { wrapper } = await mountPage()

    expect(wrapper.get('.status-badge.active').text()).toBe('已启用')
    expect(wrapper.get('[data-testid="empty-key-whitelist"]').text()).toBe('已生效，待选择模型')
    expect(wrapper.get('.key-models-link').attributes('href')).toBe(`/student/models?key=${key.id}`)
    expect(wrapper.find('.key-health-item.blocked').exists()).toBe(false)
    expect(wrapper.find('[data-testid="key-protocols"]').exists()).toBe(false)
    await wrapper.findAll('button').find(button => button.text() === '查看密钥')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="detail-key-models"]').attributes('href')).toBe(`/student/models?key=${key.id}`)
    expect(wrapper.find('[data-testid="configure-api-key"]').exists()).toBe(false)
  })

  it('说明凭据、目录和探活，并展示旧响应里的协议信息', async () => {
    const model = { ...readyModel, availability: undefined, probe_state: undefined, last_probe_at: null }
    mockStudentRequests({ [`GET ${keysPath}`]: [makeKey({ models: [model] })] })
    const { wrapper } = await mountPage()

    const guide = wrapper.get('[data-testid="key-health-guide"]').text()
    for (const label of ['凭据', '目录', '探活', '不是实时探测']) expect(guide).toContain(label)
    expect(guide).not.toContain('报价')
    expect(wrapper.get('[data-health="credential"]').text()).toContain('已签发')
    expect(wrapper.get('[data-health="catalog"]').text()).toContain('已登记 1 个模型')
    expect(wrapper.find('[data-health="pricing"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="credit-rate-note"]').text()).toBe('100 积分 = 1 元；金额统一用积分显示。')
    expect(wrapper.get('[data-testid="key-credit-summary"]').text()).toContain('本月未使用')
    expect(wrapper.get('[data-health="probe"]').text()).toContain('待验证')
    expect(wrapper.get('[data-testid="key-model-row"]').attributes('data-availability')).toBe('unverified')
    const protocols = wrapper.get('[data-testid="key-protocols"]').text()
    expect(protocols).toContain('Anthropic Messages')
    expect(protocols).toContain('OpenAI Chat')
    expect(wrapper.text()).toContain('网关不做协议转换')
  })

  it('显示缺少线路的授权，保留生效 Key 的真实状态', async () => {
    const brokenModel = {
      ...readyModel, status: 'inactive' as const, route_ready: false, credit_multiplier: null,
      availability: undefined, probe_state: undefined, protocols: [],
    }
    mockStudentRequests({ [`GET ${keysPath}`]: [makeKey({ models: [brokenModel], route_ready: false, key_prefix: null })] })
    const { wrapper } = await mountPage()

    expect(wrapper.get('.status-badge.active').text()).toBe('已启用')
    expect(wrapper.get('[data-health="credential"]').text()).toContain('已生效，待生成')
    expect(wrapper.get('[data-testid="key-model-row"]').text()).toContain('老师已停用该模型')
    expect(wrapper.get('[data-testid="key-model-row"]').text()).toContain('支持协议：暂无')
    expect(wrapper.find('[data-health="pricing"]').exists()).toBe(false)
    expect(wrapper.get('[data-health="probe"]').text()).toContain('线路不可用')
  })
})
