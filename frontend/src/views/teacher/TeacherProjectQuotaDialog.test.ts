import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, patch } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { Project, ProjectUsage } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import { calculateCredits } from '../../utils/format'
import TeacherProjects from './TeacherProjects.vue'
import TeacherProjectQuotaDialog from './TeacherProjectQuotaDialog.vue'

vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  patch: vi.fn(),
}))
vi.mock('../../api/teacher', () => ({ teacherAPI: {
  projectUsage: vi.fn(), updateMemberQuota: vi.fn(), organizations: vi.fn(), projects: vi.fn(),
  mentorApplications: vi.fn(), projectBudgets: vi.fn(),
} }))
vi.mock('../../composables/useToast', () => ({ useToast: () => ({ success: vi.fn() }) }))

const currentMonth = '2026-11'
const project: Project = {
  id: 'project-1', name: '教学实验', organization_id: 'org-1', status: 'active',
  has_mentor: true, monthly_credit_quota: '1000', mentors: [],
}
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'size'], emits: ['update:open'],
    template: '<section v-if="open" role="dialog" :data-size="size"><h2>{{ title }}</h2><button aria-label="关闭" @click="$emit(\'update:open\', false)">关闭</button><slot /></section>',
  },
  LoadingRegion: { template: '<div><slot /></div>' },
  CreditDonut: { props: ['segments', 'total'], template: '<div data-donut="true" />' },
}
let snapshot: ProjectUsage
const wrappers: VueWrapper[] = []

function fixture(): ProjectUsage {
  return {
    project_id: project.id, project_name: project.name, month: currentMonth,
    quota: '1000', allocated: '100', available: '900', charged: '25', pending: '10', pending_review: true,
    members: [
      { user_id: 'student-1', user_name: '学生甲', is_student: true, quota: '100', credits: '25', pending: '10',
        available: '65', pending_review: true, keys: [], free_models: [] },
      { user_id: 'mentor-1', user_name: '导师乙', is_student: false, quota: '0', credits: '0', pending: '0',
        available: '0', keys: [], free_models: [] },
    ], models: [], free_models: [],
  }
}

function addStudent(quota = '0') {
  snapshot.members.push({
    user_id: 'student-2', user_name: '学生乙', is_student: true, quota, available: quota, credits: '0', keys: [], free_models: [],
  })
  snapshot.allocated = calculateCredits(snapshot.allocated, quota, 'add')
  snapshot.available = calculateCredits(snapshot.quota, snapshot.allocated, 'subtract')
}

function saveFixtureQuota(id: string, quota: string) {
  const member = snapshot.members.find(item => item.user_id === id)!
  const otherQuota = calculateCredits(snapshot.allocated, member.quota, 'subtract')
  snapshot.allocated = calculateCredits(otherQuota, quota, 'add')
  snapshot.available = calculateCredits(snapshot.quota, snapshot.allocated, 'subtract')
  member.quota = quota
  const minimum = calculateCredits(member.credits, member.pending ?? '0', 'add')
  member.available = calculateCredits(quota, minimum, 'subtract')
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-11-20T08:00:00Z'))
  snapshot = fixture()
  vi.mocked(teacherAPI.projectUsage).mockImplementation(async (_id, month) => ({ ...structuredClone(snapshot), month: month ?? currentMonth }))
  vi.mocked(teacherAPI.updateMemberQuota).mockImplementation(async (_projectID, id, quota) => saveFixtureQuota(id, quota))
  vi.mocked(patch).mockResolvedValue(undefined)
  vi.mocked(teacherAPI.organizations).mockResolvedValue([])
  vi.mocked(teacherAPI.projects).mockResolvedValue([project])
  vi.mocked(teacherAPI.mentorApplications).mockResolvedValue([])
  vi.mocked(teacherAPI.projectBudgets).mockResolvedValue([])
})

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.useRealTimers()
})

async function render(props: { project?: Project; initialMonth?: string } = {}) {
  const wrapper = mount(TeacherProjectQuotaDialog, {
    props: { project, initialMonth: currentMonth, ...props }, global: { stubs },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

function button(wrapper: VueWrapper, text: string) {
  const found = wrapper.findAll('button').find(item => item.text() === text)
  if (!found) throw new Error(`找不到按钮 ${text}`)
  return found
}

function memberRow(wrapper: Pick<VueWrapper, 'get'>, id = 'student-1') {
  return wrapper.get(`.quota-member[data-member-id="${id}"]`)
}

async function batchFill(wrapper: VueWrapper, value: string) {
  const batch = wrapper.get('.quota-batch')
  await batch.get('input').setValue(value)
  await batch.trigger('submit')
}

describe('学生额度弹窗', () => {
  it('两栏内容位于独立滚动区，语义化成员表与底部操作区没有内嵌横向滚动容器', async () => {
    const wrapper = await render()
    expect(wrapper.get('[role="dialog"]').attributes('data-size')).toBe('xwide')
    expect(wrapper.findAll('[role="dialog"]')).toHaveLength(1)
    const scroll = wrapper.get('.quota-dialog > .quota-scroll[tabindex="0"]')
    const overview = scroll.get('.quota-layout > .quota-overview')
    expect(overview.get('.quota-summary').findAll('dt').map(term => term.text()))
      .toEqual(['项目月额度', '已分配', '可再分配', '本月已消耗', '待核'])
    const table = scroll.get('.quota-layout > .quota-members > table.quota-table')
    expect(table.findAll('thead th[scope="col"]').map(cell => cell.text()))
      .toEqual(['成员', '已分配', '已用', '待核', '剩余', '本月新额度'])
    const row = memberRow(wrapper)
    expect(row.get('th[scope="row"]').text()).toContain('学生甲')
    expect(row.findAll('.quota-cell-label').map(label => label.text()))
      .toEqual(['已分配', '已用', '待核', '剩余', '本月新额度'])
    expect(row.get('.quota-entry > .quota-row-form').find('input[inputmode="decimal"]').exists()).toBe(true)
    expect(wrapper.get('.quota-dialog > .quota-footer').find('.quota-save-batch').exists()).toBe(true)
    expect(scroll.find('.quota-footer').exists()).toBe(false)
  })

  it('项目汇总和占比环图显示积分，行内保存后重读额度并通知父页面', async () => {
    const wrapper = await render()
    expect(wrapper.get('.quota-summary').text()).toContain('1,000')
    expect(wrapper.get('.quota-summary').text()).not.toContain('.000')
    expect(wrapper.findComponent(stubs.CreditDonut).props('segments')).toEqual([
      { name: '学生甲', value: 100000, displayValue: '100' },
      { name: '可再分配', value: 900000, color: '#dfe3ec', displayValue: '900' },
    ])
    const row = memberRow(wrapper)
    expect(row.get('input').element.value).toBe('100')
    await row.get('input').setValue('150.125')
    await row.get('form').trigger('submit')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenCalledExactlyOnceWith(project.id, 'student-1', '150.125')
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(teacherAPI.projectUsage).toHaveBeenCalledTimes(2)
    expect(row.text()).toContain('115.125')
    expect(row.get('[role="status"]').text()).toBe('已保存')
  })

  it('允许手动下调至覆盖已用和待核的额度，拦住少于下限的请求', async () => {
    const wrapper = await render()
    const row = memberRow(wrapper)
    await row.get('input').setValue('34.999')
    expect(row.get('[role="alert"]').text()).toContain('至少需 35 积分')
    expect(row.get('.quota-save-row').attributes('disabled')).toBeDefined()
    await row.get('form').trigger('submit')
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
    await row.get('input').setValue('35')
    await row.get('form').trigger('submit')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenCalledExactlyOnceWith(project.id, 'student-1', '35')
  })

  it.each(['-1', '10.1234', '1e3', '', 'NaN'])('行内提示非法额度 %s', async value => {
    const row = memberRow(await render())
    await row.get('input').setValue(value)
    expect(row.get('input').attributes('aria-invalid')).toBe('true')
    expect(row.get('[role="alert"]').text()).toContain('非负积分')
    expect(row.get('.quota-save-row').attributes('disabled')).toBeDefined()
    await row.get('form').trigger('submit')
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
  })

  it('增加量超过项目可再分配额度时在本行拦截，输入可以继续修改', async () => {
    const row = memberRow(await render())
    await row.get('input').setValue('1000.001')
    expect(row.get('[role="alert"]').text()).toContain('超过项目可再分配的 900 积分')
    expect(row.get('.quota-save-row').attributes('disabled')).toBeDefined()
    await row.get('form').trigger('submit')
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
    expect(row.get('input').element.value).toBe('1000.001')
    await row.get('input').setValue('1000')
    expect(row.get('.quota-save-row').attributes('disabled')).toBeUndefined()
  })

  it('额度已覆盖已用与待核时补足预览保持原额度', async () => {
    const row = memberRow(await render())
    await row.get('input').setValue('35')
    await row.get('.quota-top-up').trigger('click')
    expect(row.get('input').element.value).toBe('100')
    expect(row.get('.quota-preview').text()).toContain('100 → 100 积分')
    expect(row.get('.quota-preview').text()).toContain('无需补足')
    expect(row.get('.quota-save-row').attributes('disabled')).toBeDefined()
    await row.get('form').trigger('submit')
    expect(patch).not.toHaveBeenCalled()
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
  })

  it('补足已用覆盖待核并向上取足三位小数，预览后才提交只升不降的写入', async () => {
    snapshot.members[0].credits = '95.000000000001'
    vi.mocked(patch).mockImplementationOnce(async () => saveFixtureQuota('student-1', '105.001'))
    const row = memberRow(await render())
    await row.get('.quota-top-up').trigger('click')
    expect(row.get('input').element.value).toBe('105.001')
    expect(row.get('.quota-preview').text()).toContain('100 → 105.001 积分')
    expect(patch).not.toHaveBeenCalled()
    await row.get('form').trigger('submit')
    await flushPromises()
    expect(patch).toHaveBeenCalledExactlyOnceWith(
      '/api/v1/teacher/projects/project-1/members/student-1/monthly-credit-quota',
      { monthly_credits: '105.001', increase_only: true },
    )
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
    expect(row.get('[role="status"]').text()).toBe('已保存')
  })

  it('补足与服务端更新冲突后保留原输入，重读已提高的额度并要求重新预览', async () => {
    snapshot.members[0].credits = '95'
    vi.mocked(patch).mockImplementationOnce(async () => {
      saveFixtureQuota('student-1', '120')
      throw new APIError('member quota is already higher', 409, 'INVALID_STATE_TRANSITION')
    })
    const wrapper = await render()
    const row = memberRow(wrapper)
    await row.get('.quota-top-up').trigger('click')
    await row.get('form').trigger('submit')
    await flushPromises()
    expect(row.get('input').element.value).toBe('105.000')
    expect(row.get('[role="alert"]').text()).toContain('当前额度已提高')
    expect(row.find('.quota-preview').exists()).toBe(false)
    expect(row.get('.quota-save-row').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('saved')).toBeUndefined()
    await row.get('.quota-top-up').trigger('click')
    expect(row.get('input').element.value).toBe('120')
    expect(row.get('.quota-preview').text()).toContain('120 → 120 积分')
  })

  it('待核列分别显示积分和 Token，并区分确认的零值、未知和有调用待核', async () => {
    addStudent()
    Object.assign(snapshot, { tokens_missing: 1250 })
    Object.assign(snapshot.members[0], { pending: '0.000', tokens_missing: 1210 })
    Object.assign(snapshot.members[1], { tokens_missing: 0 })
    const wrapper = await render()
    expect(wrapper.get('.quota-summary .quota-pending').text()).toContain('积分 10')
    expect(wrapper.get('.quota-summary .quota-pending').text()).toContain('Token 1,250')
    const unpriced = memberRow(wrapper).get('.quota-pending')
    expect(unpriced.text()).toContain('积分 —')
    expect(unpriced.text()).toContain('Token 1,210')
    expect(unpriced.text()).toContain('有调用待核')
    expect(memberRow(wrapper, 'mentor-1').get('.quota-pending').text()).toContain('积分 0')
    expect(memberRow(wrapper, 'mentor-1').get('.quota-pending').text()).toContain('Token 0')
    const missing = memberRow(wrapper, 'student-2').get('.quota-pending')
    expect(missing.text()).toContain('积分 —')
    expect(missing.text()).toContain('Token —')
  })

  it('批量填入只写学生输入，逐行报告保存状态，失败行可用原输入重试', async () => {
    addStudent()
    let finishFirst!: () => void
    vi.mocked(teacherAPI.updateMemberQuota)
      .mockImplementationOnce(async (_projectID, id, quota) => {
        await new Promise<void>(resolve => { finishFirst = resolve })
        saveFixtureQuota(id, quota)
      })
      .mockRejectedValueOnce(new Error('模拟保存失败'))
    const wrapper = await render()
    const first = memberRow(wrapper)
    const second = memberRow(wrapper, 'student-2')
    await batchFill(wrapper, '180.125')
    expect(first.get('input').element.value).toBe('180.125')
    expect(second.get('input').element.value).toBe('180.125')
    expect(memberRow(wrapper, 'mentor-1').find('input').exists()).toBe(false)
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()

    await wrapper.get('.quota-save-batch').trigger('click')
    expect(first.get('[role="status"]').text()).toBe('正在保存…')
    expect(second.get('[role="status"]').text()).toBe('待保存')
    expect(teacherAPI.updateMemberQuota).toHaveBeenCalledTimes(1)
    finishFirst()
    await flushPromises()
    expect(first.get('[role="status"]').text()).toBe('已保存')
    expect(second.get('[role="alert"]').text()).toContain('保存失败')
    expect(second.get('input').element.value).toBe('180.125')
    expect(second.get('.quota-save-row').text()).toBe('重试')
    expect(wrapper.emitted('saved')).toHaveLength(1)

    await second.get('form').trigger('submit')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenNthCalledWith(2, project.id, 'student-2', '180.125')
    expect(teacherAPI.updateMemberQuota).toHaveBeenNthCalledWith(3, project.id, 'student-2', '180.125')
    expect(second.get('[role="status"]').text()).toBe('已保存')
    expect(wrapper.emitted('saved')).toHaveLength(2)
  })

  it('批量保存每成功一行就扣减可再分配额度，后续超额行留在表内提示', async () => {
    snapshot.quota = '250'
    addStudent()
    const wrapper = await render()
    await batchFill(wrapper, '140')
    expect(memberRow(wrapper, 'student-2').find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('.quota-save-batch').trigger('click')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenCalledExactlyOnceWith(project.id, 'student-1', '140')
    const second = memberRow(wrapper, 'student-2')
    expect(second.get('[role="alert"]').text()).toContain('超过项目可再分配的 110 积分')
    expect(second.get('input').element.value).toBe('140')
  })

  it('批量保存先释放确认下调的额度，再使用可再分配额度', async () => {
    snapshot.quota = '400'
    addStudent('250')
    const wrapper = await render()
    await batchFill(wrapper, '200')
    expect(memberRow(wrapper).get('[role="alert"]').text()).toContain('50 积分')
    await wrapper.get('.quota-save-batch').trigger('click')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenNthCalledWith(1, project.id, 'student-2', '200')
    expect(teacherAPI.updateMemberQuota).toHaveBeenNthCalledWith(2, project.id, 'student-1', '200')
    expect(memberRow(wrapper).get('[role="status"]').text()).toBe('已保存')
  })

  it('历史月份与停用项目只读，导师和未知身份在本月也不可编辑', async () => {
    const historical = await render({ initialMonth: '2026-10' })
    expect(historical.text()).toContain('历史月份只读')
    expect(memberRow(historical).find('input').exists()).toBe(false)
    const inactive = await render({ project: { ...project, status: 'inactive' } })
    expect(inactive.text()).toContain('项目已停用')
    expect(memberRow(inactive).find('input').exists()).toBe(false)
    snapshot.members[0].is_student = undefined
    const current = await render()
    expect(memberRow(current, 'mentor-1').text()).toContain('导师只读')
    expect(memberRow(current, 'mentor-1').find('input').exists()).toBe(false)
    expect(memberRow(current).find('input').exists()).toBe(false)
    expect(current.find('.quota-batch').exists()).toBe(false)
  })

  it('挂到项目页后切月只刷新弹窗，保存刷新项目页，关闭后可以重开', async () => {
    const page = mount(TeacherProjects, { global: { stubs } })
    wrappers.push(page)
    await flushPromises()
    await button(page, '学生额度').trigger('click')
    await flushPromises()
    const dialog = page.getComponent(TeacherProjectQuotaDialog)
    await dialog.get('input[type="month"]').setValue('2026-10')
    await flushPromises()
    expect(teacherAPI.projectUsage).toHaveBeenLastCalledWith(project.id, '2026-10')
    expect(page.get<HTMLInputElement>('.page-heading input[type="month"]').element.value).toBe(currentMonth)
    expect(teacherAPI.projects).toHaveBeenCalledTimes(1)
    expect(teacherAPI.projectBudgets).toHaveBeenCalledTimes(1)
    await dialog.get('input[type="month"]').setValue(currentMonth)
    await flushPromises()
    const row = memberRow(dialog)
    await row.get('input').setValue('200')
    await row.get('form').trigger('submit')
    await flushPromises()
    expect(teacherAPI.projects).toHaveBeenCalledTimes(2)
    expect(teacherAPI.projectBudgets).toHaveBeenCalledTimes(2)
    await dialog.get('.quota-footer button').trigger('click')
    expect(page.findComponent(TeacherProjectQuotaDialog).exists()).toBe(false)
    await button(page, '学生额度').trigger('click')
    await flushPromises()
    expect(page.getComponent(TeacherProjectQuotaDialog).get<HTMLInputElement>('input[type="month"]').element.value).toBe(currentMonth)
  })

  it('较早月份的慢响应不能覆盖后来选中的月份', async () => {
    let finish!: (value: ProjectUsage) => void
    vi.mocked(teacherAPI.projectUsage).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const wrapper = await render()
    await wrapper.get('input[type="month"]').setValue('2026-10')
    await flushPromises()
    finish({ ...snapshot, quota: '9999' })
    await flushPromises()
    expect(wrapper.get('.quota-summary').text()).toContain('1,000')
    expect(wrapper.get('.quota-summary').text()).not.toContain('9,999')
    expect(wrapper.find('.quota-row-form').exists()).toBe(false)
  })

  it('读取失败后可重试，零分配显示尚未分配', async () => {
    snapshot.members[0].quota = '0'
    snapshot.allocated = '0'
    vi.mocked(teacherAPI.projectUsage).mockRejectedValueOnce(new Error('模拟读取失败'))
    const wrapper = await render()
    expect(wrapper.get('[role="alert"]').text()).toContain('读取学生额度失败')
    await button(wrapper, '重新加载').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('尚未分配')
    expect(wrapper.find('[data-donut]').exists()).toBe(false)
  })
})

it('AppDialog 新尺寸优先，原 wide 属性仍兼容', async () => {
  const wrapper = mount(AppDialog, {
    props: { open: true, title: '尺寸测试', wide: true },
    global: { stubs: {
      DialogRoot: { template: '<div><slot /></div>' },
      DialogPortal: { template: '<div><slot /></div>' },
      DialogContent: { template: '<div><slot /></div>' },
      DialogTitle: { template: '<h2><slot /></h2>' },
      DialogOverlay: true, DialogClose: true,
    } },
  })
  wrappers.push(wrapper)
  expect(wrapper.get('.dialog-content').classes()).toContain('wide')
  await wrapper.setProps({ size: 'xwide' })
  expect(wrapper.get('.dialog-content').classes()).toContain('xwide')
  expect(wrapper.get('.dialog-content').classes()).not.toContain('wide')
  await wrapper.setProps({ size: 'default' })
  expect(wrapper.get('.dialog-content').classes()).not.toContain('wide')
  expect(wrapper.get('.dialog-content').classes()).not.toContain('xwide')
})
