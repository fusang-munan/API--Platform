import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { teacherAPI } from '../../api/teacher'
import { APIError } from '../../api/client'
import type { Project, ProjectUsage } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import TeacherProjects from './TeacherProjects.vue'
import TeacherProjectQuotaDialog from './TeacherProjectQuotaDialog.vue'

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

beforeEach(() => {
  vi.resetAllMocks()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-11-20T08:00:00Z'))
  snapshot = fixture()
  vi.mocked(teacherAPI.projectUsage).mockImplementation(async (_id, month) => ({ ...snapshot, month: month ?? currentMonth }))
  vi.mocked(teacherAPI.updateMemberQuota).mockResolvedValue(undefined)
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
  if (!found) throw new Error(`找不到按钮：${text}`)
  return found
}

async function edit(wrapper: Pick<VueWrapper, 'get'>) {
  await wrapper.get('[data-member-id="student-1"] button').trigger('click')
  return wrapper.get('.quota-editor')
}

describe('学生额度弹窗', () => {
  it('用 xwide 打开，汇总与分配图显示积分，单成员保存后发事件并重读数据', async () => {
    const wrapper = await render()
    expect(wrapper.get('[role="dialog"]').attributes('data-size')).toBe('xwide')
    expect(wrapper.get('[aria-label="项目额度汇总"]').text()).toContain('1,000')
    expect(wrapper.get('[aria-label="项目额度汇总"]').text()).not.toContain('.000')
    expect(wrapper.findComponent(stubs.CreditDonut).props('segments')).toEqual([
      { name: '学生甲', value: 100, displayValue: '100 积分' },
      { name: '可再分配', value: 900, color: '#dfe3ec', displayValue: '900 积分' },
    ])
    const editor = await edit(wrapper)
    expect(wrapper.findAll('[role="dialog"]')).toHaveLength(2)
    expect(editor.get('input').element.value).toBe('100')
    await editor.get('input').setValue('150.125')
    expect(editor.get('.quota-preview').text()).toContain('115.125 积分')
    vi.mocked(teacherAPI.updateMemberQuota).mockImplementationOnce(async () => {
      snapshot.members[0] = { ...snapshot.members[0], quota: '150.125', available: '115.125' }
      snapshot.allocated = '150.125'
    })
    await editor.trigger('submit')
    await flushPromises()
    expect(teacherAPI.updateMemberQuota).toHaveBeenCalledExactlyOnceWith(project.id, 'student-1', '150.125')
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(teacherAPI.projectUsage).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.quota-editor').exists()).toBe(false)
    expect(wrapper.get('[data-member-id="student-1"]').text()).toContain('150.125')
  })

  it('新额度低于已用与待核合计时禁用保存，直接提交也不能调用接口', async () => {
    const wrapper = await render()
    const editor = await edit(wrapper)
    await editor.get('input').setValue('34.999')
    expect(editor.get('[role="alert"]').text()).toContain('至少需 35 积分')
    expect(editor.get('button[type="submit"], button.primary').attributes('disabled')).toBeDefined()
    await editor.trigger('submit')
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
    await editor.get('input').setValue('35')
    expect(editor.get('button.primary').attributes('disabled')).toBeUndefined()
    expect(editor.get('.quota-preview').text()).toContain('0 积分')
  })

  it.each(['-1', '10.1234', '1e3', '', 'NaN'])('拒绝非法额度 %s', async value => {
    const wrapper = await render()
    const editor = await edit(wrapper)
    await editor.get('input').setValue(value)
    expect(editor.get('button.primary').attributes('disabled')).toBeDefined()
    await editor.trigger('submit')
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
  })

  it('快捷按钮累加当前输入，补足已用会覆盖待核并向上取足三位小数', async () => {
    snapshot.members[0].credits = '25.000000000001'
    const wrapper = await render()
    const editor = await edit(wrapper)
    await button(wrapper, '+100').trigger('click')
    expect(editor.get('input').element.value).toBe('200.000')
    await button(wrapper, '+500').trigger('click')
    expect(editor.get('input').element.value).toBe('700.000')
    await button(wrapper, '+1000').trigger('click')
    expect(editor.get('input').element.value).toBe('1700.000')
    await editor.get('input').setValue('35')
    expect(editor.get('button.primary').attributes('disabled')).toBeDefined()
    await button(wrapper, '补足已用').trigger('click')
    expect(editor.get('input').element.value).toBe('35.001')
    expect(editor.get('button.primary').attributes('disabled')).toBeUndefined()
  })

  it('超过项目可分配额度时提示，后端拒绝后保留输入且不发成功事件', async () => {
    const wrapper = await render()
    const editor = await edit(wrapper)
    await editor.get('input').setValue('1000.001')
    expect(editor.get('[role="status"]').text()).toContain('超过项目可再分配的 900 积分')
    expect(editor.get('button.primary').attributes('disabled')).toBeUndefined()
    vi.mocked(teacherAPI.updateMemberQuota).mockRejectedValueOnce(new APIError('额度超过项目可分配额度', 409, 'PROJECT_QUOTA_ALLOCATION_EXCEEDED'))
    await editor.trigger('submit')
    await flushPromises()
    expect(editor.text()).toContain('超过项目月额度')
    expect(editor.get('input').element.value).toBe('1000.001')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(teacherAPI.projectUsage).toHaveBeenCalledTimes(1)
  })

  it('历史月份只读，导师在本月也不可编辑', async () => {
    const historical = await render({ initialMonth: '2026-10' })
    expect(historical.text()).toContain('历史月份只读')
    expect(historical.find('[data-member-id="student-1"] button').exists()).toBe(false)
    const current = await render()
    expect(current.get('[data-member-id="mentor-1"]').text()).toContain('导师只读')
    expect(current.find('[data-member-id="mentor-1"] button').exists()).toBe(false)
    expect(current.find('[data-member-id="student-1"] button').exists()).toBe(true)
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
  })

  it('停用项目只读，未标明学生身份的成员也不能调整', async () => {
    snapshot.members[0].is_student = undefined
    const wrapper = await render({ project: { ...project, status: 'inactive' } })
    expect(wrapper.text()).toContain('项目已停用')
    expect(wrapper.find('.quota-member button').exists()).toBe(false)
    expect(teacherAPI.updateMemberQuota).not.toHaveBeenCalled()
  })

  it('月份由弹窗独立持有，切换只重读本项目用量且不触发页面事件', async () => {
    const wrapper = await render()
    await wrapper.get('input[type="month"]').setValue('2026-10')
    await flushPromises()
    expect(teacherAPI.projectUsage).toHaveBeenLastCalledWith(project.id, '2026-10')
    expect(teacherAPI.projectUsage).toHaveBeenCalledTimes(2)
    expect(wrapper.props('initialMonth')).toBe(currentMonth)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(teacherAPI.projects).not.toHaveBeenCalled()
    expect(teacherAPI.projectBudgets).not.toHaveBeenCalled()
  })

  it('挂到项目页后，切月不刷新页面，保存才刷新页面，关闭可重开', async () => {
    const page = mount(TeacherProjects, { global: { stubs } })
    wrappers.push(page)
    await flushPromises()
    await button(page, '学生额度').trigger('click')
    await flushPromises()
    const dialog = page.getComponent(TeacherProjectQuotaDialog)
    await dialog.get('input[type="month"]').setValue('2026-10')
    await flushPromises()
    expect(page.get<HTMLInputElement>('.page-heading input[type="month"]').element.value).toBe(currentMonth)
    expect(teacherAPI.projects).toHaveBeenCalledTimes(1)
    expect(teacherAPI.projectBudgets).toHaveBeenCalledTimes(1)
    await dialog.get('input[type="month"]').setValue(currentMonth)
    await flushPromises()
    const editor = await edit(dialog)
    await editor.get('input').setValue('200')
    await editor.trigger('submit')
    await flushPromises()
    expect(teacherAPI.projects).toHaveBeenCalledTimes(2)
    expect(teacherAPI.projectBudgets).toHaveBeenCalledTimes(2)
    await dialog.get('[aria-label="关闭"]').trigger('click')
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
    expect(wrapper.get('[aria-label="项目额度汇总"]').text()).toContain('1,000')
    expect(wrapper.get('[aria-label="项目额度汇总"]').text()).not.toContain('9,999')
    expect(wrapper.find('.quota-member button').exists()).toBe(false)
  })

  it('零分配显示尚未分配而不挂载灰环，读取失败后可重试', async () => {
    snapshot.members[0].quota = '0'
    snapshot.allocated = '0'
    vi.mocked(teacherAPI.projectUsage).mockRejectedValueOnce(new Error('测试读取失败'))
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
