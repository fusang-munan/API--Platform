import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import type {
  TeacherStudentUsageDetail, TeacherStudentUsagePage, TeacherStudentUsageRow, TeacherUsageMetrics,
} from '../../api/teacherUsage'
import { schoolMonth } from '../../utils/format'
import TeacherStudentUsage from './TeacherStudentUsage.vue'

const route = '/api/v1/teacher/usage/students'
const preciseCredits = '0.000000000000000001'
const student: TeacherStudentUsageRow = {
  user_id: 'student-a', user_name: '学生甲', user_email: 'a@example.test', project_count: 2, key_count: 3,
  credits: preciseCredits, calls: 3, zero_cost_calls: 1, total_tokens: '9007199254740993', completeness: 'partial',
  pending_review_calls: 2, pending_review_credits: '7.000', last_call_at: '2025-02-01T16:00:00Z',
}
const other: TeacherStudentUsageRow = {
  ...student, user_id: 'student-b', user_name: '学生乙', user_email: null, total_tokens: null, completeness: 'unknown',
  last_call_at: null,
}
const metrics: TeacherUsageMetrics = {
  calls: 3, zero_cost_calls: 1, pending_review_calls: 2, pending_review_credits: '7.000', completeness: 'partial',
  tokens: { total: '130', input: null, output: '4', cache_read: null, cache_write: null },
  token_coverage: { calls: 5, known_calls: 2, complete_calls: 0, unclassified_tokens: '126' },
  cache_hit_rate: null, cache_hit_coverage: { sample_calls: 0, cache_read_tokens: null, input_with_cache_read_tokens: null },
}
const model = { ...metrics, id: 'model-1', name: '模型甲', credits: preciseCredits }
const detail: TeacherStudentUsageDetail = {
  user_id: student.user_id, user_name: student.user_name, user_email: student.user_email,
  month: '2025-02', totals: { ...metrics, credits: preciseCredits, calls: 3 }, models: [model],
  projects: [{
    ...metrics, project_id: 'project-a', project_name: '课程项目', quota: '10.000', used: preciseCredits, pending: '0.000',
    available: '9.999999999999999999', in_flight_credits: '0.000', calls: 3,
    keys: [{
      ...metrics, id: 'key-1', name: '历史 Key', status: 'inactive', used: preciseCredits, pending: '0.000',
      in_flight_credits: '0.000', pending_calls: 2, calls: 3, models: [model],
    }],
  }],
}
const upstream = { rows: 3, debit_rows: 2, balance_debit: '0.000000000000000002' }
const projects = [{ id: 'project-a', name: '课程项目', status: 'inactive' }]

function json(data: unknown) {
  return new Response(JSON.stringify({ data }))
}

function page(url: URL, items: TeacherStudentUsageRow[], total = items.length) {
  const data: TeacherStudentUsagePage = {
    month: url.searchParams.get('month') ?? schoolMonth(), items, total,
    page: Number(url.searchParams.get('page')), page_size: 20, unattributed_upstream: upstream,
  }
  return json(data)
}

function failure(message: string) {
  return new Response(JSON.stringify({ error: { code: 'DEPENDENCY_UNAVAILABLE', message } }), { status: 503 })
}

function endpoint(path: string) {
  return new URL(path, 'http://localhost')
}

let widget: VueWrapper | undefined
afterEach(() => {
  widget?.unmount()
  vi.unstubAllGlobals()
})

it('按已应用条件分页，并以相同的月份和项目参数加载学生、项目、Key、模型明细', async () => {
  const requests = vi.fn(async (path: string) => {
    const url = endpoint(path)
    if (url.pathname === '/api/v1/teacher/projects') return json(projects)
    if (url.pathname === `${route}/${student.user_id}`) {
      expect(Object.fromEntries(url.searchParams)).toEqual({ month: '2025-02', project_id: 'project-a' })
      return json(detail)
    }
    expect(url.pathname).toBe(route)
    if (url.searchParams.get('month') === '2025-02') {
      expect(Object.fromEntries(url.searchParams)).toEqual({
        month: '2025-02', project_id: 'project-a', search: 'A+标签@example.test', sort: 'tokens', page: '1', page_size: '20',
      })
      return page(url, [student])
    }
    return page(url, url.searchParams.get('page') === '1' ? [student] : [other], 21)
  })
  vi.stubGlobal('fetch', requests)
  const view = widget = mount(TeacherStudentUsage)
  expect(view.text()).toContain('正在加载学生用量')
  await flushPromises()
  expect(view.get('.student-row').text()).toContain('学生甲')
  expect(view.get('.student-row').text()).toContain(preciseCredits)
  expect(view.get('.student-row').text()).toContain('9007199254740993')
  expect(view.get('.student-row').text()).toContain('扣费 2 · 零额 1')
  expect(view.get('.student-row').text()).toContain('部分')
  expect(view.get('time').text()).toMatch(/2025\/2\/2.*00:00/)
  expect(view.get('.upstream-summary').text()).toContain(upstream.balance_debit)
  expect(requests.mock.calls.filter(([path]) => endpoint(path).pathname.includes(`${route}/`))).toHaveLength(0)

  const buttons = () => view.get('.usage-pagination').findAll('button')
  expect(buttons()[0].attributes('disabled')).toBeDefined()
  await buttons()[1].trigger('click')
  await flushPromises()
  expect(view.get('.student-row').text()).toContain('学生乙')
  expect(view.get('.student-row').text()).toContain('未知')
  expect(view.get('.usage-pagination').text()).toContain('第 2 / 2 页')
  expect(buttons()[1].attributes('disabled')).toBeDefined()
  await buttons()[0].trigger('click')
  await flushPromises()
  expect(view.get('.student-row').text()).toContain('学生甲')
  await buttons()[1].trigger('click')
  await flushPromises()

  await view.get('input[type="month"]').setValue('2025-02')
  expect(view.findAll('select')[0].text()).toContain('课程项目（已停用）')
  await view.findAll('select')[0].setValue('project-a')
  await view.get('input[type="search"]').setValue(' A+标签@example.test ')
  await view.findAll('select')[1].setValue('tokens')
  await view.get('form').trigger('submit')
  await flushPromises()
  expect(view.get('.usage-pagination').text()).toContain('共 1 位学生 · 第 1 / 1 页')
  const expand = view.get('.student-button')
  expect(expand.attributes('aria-expanded')).toBe('false')
  await view.get('.student-row').trigger('click')
  await flushPromises()
  expect(expand.attributes('aria-expanded')).toBe('true')
  expect(view.get('.project-detail > summary').text()).toContain('课程项目')
  view.get('.project-detail').element.setAttribute('open', '')
  view.get('.key-detail').element.setAttribute('open', '')
  expect(view.get('.key-detail > summary').text()).toContain('历史 Key')
  expect(view.get('.model-list').text()).toContain('模型甲')
  expect(view.get('.model-list').text()).toContain('待核 2 笔')
  expect(view.get('.model-list').text()).toContain('缓存读 —')
  await expand.trigger('click')
  expect(view.find('.detail-row').exists()).toBe(false)
})

it('学生列表与项目加载失败后各自可重试，空结果仍展示全校未确认流水', async () => {
  let listCalls = 0
  let projectCalls = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = endpoint(path)
    if (url.pathname === '/api/v1/teacher/projects') {
      return ++projectCalls === 1 ? failure('项目暂不可用') : json(projects)
    }
    return ++listCalls === 1 ? failure('报表暂不可用') : page(url, [])
  }))
  const view = widget = mount(TeacherStudentUsage)
  await flushPromises()
  expect(view.get('.report-panel [role="alert"]').text()).toContain('报表暂不可用')
  expect(view.get('form [role="alert"]').text()).toContain('项目暂不可用')
  await view.get('form [role="alert"] button').trigger('click')
  await view.get('.report-panel [role="alert"] button').trigger('click')
  await flushPromises()
  expect(view.get('.report-panel').text()).toContain('所选范围内没有学生用量')
  expect(view.find('table').exists()).toBe(false)
  expect(view.get('.upstream-summary').text()).toContain('3 笔已归档未确认流水')
  expect(view.findAll('select')[0].text()).toContain('课程项目')
})

it('明细错误可重试，换筛选时取消并忽略旧明细', async () => {
  let resolveLate: (response: Response) => void = () => {}
  let lateSignal: AbortSignal | null | undefined
  const late = new Promise<Response>(resolve => { resolveLate = resolve })
  let detailCalls = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    const url = endpoint(path)
    if (url.pathname === '/api/v1/teacher/projects') return json(projects)
    if (url.pathname.startsWith(`${route}/`)) {
      if (++detailCalls === 1) return failure('明细暂不可用')
      lateSignal = init?.signal
      return late
    }
    return page(url, url.searchParams.get('search') ? [other] : [student])
  }))
  const view = widget = mount(TeacherStudentUsage)
  await flushPromises()
  await view.get('.student-button').trigger('click')
  await flushPromises()
  expect(view.get('.detail-row [role="alert"]').text()).toContain('明细暂不可用')
  await view.get('.detail-row [role="alert"] button').trigger('click')
  expect(view.get('.detail-row [role="status"]').text()).toContain('正在加载')
  await view.get('input[type="search"]').setValue('学生乙')
  await view.get('form').trigger('submit')
  await flushPromises()
  expect(lateSignal?.aborted).toBe(true)
  resolveLate(json(detail))
  await flushPromises()
  expect(view.get('.student-row').text()).toContain('学生乙')
  expect(view.find('.detail-row').exists()).toBe(false)
})
