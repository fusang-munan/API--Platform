import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import type { ProjectUsage, UsageSlice } from '../../api/types'
import TeacherProjectSpend from './TeacherProjectSpend.vue'

type Member = ProjectUsage['members'][number]
const firstModel = {
  id: 'm', name: '模型甲', credits: '0.000000000000000001', calls: 3,
  beichen_actual_cost: '9007199254740992.000000000000001', beichen_unmatched_calls: 1,
}
const free: UsageSlice = { id: 'm', name: '模型甲', credits: '0.000', calls: 2 }
const first: Member = {
  user_id: 'student-a', user_name: '同名学生', quota: '10', available: '9', credits: '0.000000000000000002',
  beichen_actual_cost: '9007199254740992.000000000000002', beichen_unmatched_calls: 3,
  keys: [
    { id: 'key-one', name: '甲 Key', status: 'inactive', used: firstModel.credits, models: [firstModel] },
    { id: 'key-two', name: '乙 Key', status: 'active', used: firstModel.credits, models: [
      { ...firstModel, beichen_actual_cost: '0.000000000000001', beichen_unmatched_calls: 2 },
    ] },
  ], free_models: [free],
}
const legacy: Member = {
  user_id: 'student-b', user_name: '同名学生', quota: '20', available: '19', credits: '1.001',
  keys: [], free_models: [{ ...free, name: '历史免费模型' }],
}
function project(id: string, members: Member[]): ProjectUsage {
  return {
    month: '2026-03', project_id: id, project_name: '项目' + id, members, models: [firstModel], free_models: [free],
    quota: '20', allocated: '10', charged: firstModel.credits, pending: '0', available: '10',
  }
}
let widget: VueWrapper
afterEach(() => {
  widget?.unmount()
  vi.unstubAllGlobals()
})

it('按身份合计筛选范围的学生开销，进入对应项目与 Key，并保留回执精度及历史未知值', async () => {
  const projects = [
    project('甲', [first, legacy]),
    project('乙', [{ ...first, credits: '0.000000000000000003', beichen_actual_cost: '0.000000000000001', beichen_unmatched_calls: 0,
      keys: [{ ...first.keys[0], models: [{ ...firstModel, beichen_actual_cost: '0', beichen_unmatched_calls: 0 }] }],
    }]),
  ]
  const request = vi.fn(async (url: string) => {
    if (url === '/api/v1/teacher/organizations') return new Response(JSON.stringify({ data: [] }))
    if (!url.startsWith('/api/v1/teacher/project-spend?month=')) throw Error('不应读取其他接口')
    const data = url.includes('q=') ? projects.slice(1) : projects
    return new Response(JSON.stringify({ data }))
  })
  vi.stubGlobal('fetch', request)
  widget = mount(TeacherProjectSpend, {
    global: { stubs: {
      CreditDonut: true,
      AppDialog: { props: ['open', 'title'], template: '<section v-if="open" role="dialog"><h2>{{ title }}</h2><slot /></section>' },
    } },
  })
  await flushPromises()
  const summary = widget.get('.student-summary')
  const students = summary.findAll('tbody tr')
  expect(students).toHaveLength(2)
  expect(students[0].text()).toContain('0.000000000000000005')
  expect(students[0].text()).toContain('北辰实扣 9007199254740992.000000000000003 余额单位')
  expect(students[0].text()).toContain('待核 3 笔')
  expect(students[1].text()).toContain('北辰实扣 未知')
  expect(students[1].text()).toContain('待核 未知')
  await students[0].get('button').trigger('click')
  expect(widget.get('[role="dialog"] h2').text()).toContain('同名学生 · 项目甲')
  const total = widget.get('.member-model-table tbody tr')
  expect(total.findAll('td').map(cell => cell.text())).toEqual([
    '0.000000000000000002', '6', expect.stringContaining('9007199254740992.000000000000002'),
  ])
  expect(total.text()).toContain('待核 3 笔')
  expect(widget.get('.key-model-table').text()).toContain(firstModel.beichen_actual_cost)

  await students[1].get('button').trigger('click')
  expect(widget.get('.member-model-table').text()).toContain('历史免费模型')
  expect(widget.get('.member-model-table').text()).toContain('待核 未知')
  await widget.get('.project-spend-table button').trigger('click')
  expect(widget.get('.project-model-table').text()).toContain(firstModel.beichen_actual_cost)
  const search = widget.get('input[placeholder="输入项目名称"]')
  await search.setValue('乙')
  await search.trigger('keyup.enter')
  await flushPromises()
  const filtered = widget.get('.student-summary')
  expect(filtered.findAll('tbody tr')).toHaveLength(1)
  expect(filtered.text()).toContain('0.000000000000000003')
  expect(filtered.text()).not.toContain('项目甲')
  expect(widget.find('[role="dialog"]').exists()).toBe(false)
  await filtered.get('button').trigger('click')
  expect(widget.get('.key-model-table').text()).toContain('北辰实扣 0 余额单位')
  expect(widget.get('.key-model-table').text()).toContain('待核 0 笔')
  expect(request.mock.calls.filter(([url]) => url.includes('project-spend'))).toHaveLength(2)
})
