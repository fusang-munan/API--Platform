import { DOMWrapper, enableAutoUnmount, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import type { BeichenModelUsage, BeichenStudentModelUsage, BeichenStudentUsageRow, BeichenUsageTotals } from '../api/types'
import BeichenModelUsageDrawer from './BeichenModelUsageDrawer.vue'

const AppDialog = defineComponent({
  props: ['open', 'title'],
  template: '<section v-if="open" role="dialog" :aria-label="title"><slot /></section>',
})
enableAutoUnmount(afterEach)

function metrics(credits: string, tokens: number, extra: Partial<BeichenUsageTotals> = {}): BeichenUsageTotals {
  return {
    credits, tokens, calls: 2, pending_calls: 0, tokens_missing: 0, completeness: 'complete',
    input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, ...extra,
  }
}

function usage(): BeichenModelUsage {
  const model = 'lab/a'
  const first = metrics('1.0000000000000000001', 120, { pending_calls: 1, completeness: 'partial', tokens_missing: 40 })
  const second = metrics('0.2', 110)
  const missing = metrics('0', 0, { pending_calls: 2, completeness: 'missing', tokens_missing: null })
  const students: Array<BeichenStudentUsageRow & BeichenUsageTotals & { models: Array<BeichenStudentModelUsage & BeichenUsageTotals> }> = [
    { user_id: 's1', user_name: '学生甲', project_id: 'p1', project_name: '教学项目一', ...first, models: [{ model, ...first }] },
    { user_id: 's1', user_name: '学生甲', project_id: 'p2', project_name: '教学项目二', ...second, models: [{ model, ...second }] },
    { user_id: 's2', user_name: '学生乙', project_id: 'p1', project_name: '教学项目一', ...missing, models: [{ model: 'lab/b', ...missing }] },
  ]
  return {
    range: 'month', start: null, end: '2026-11-20T12:00:00+08:00', timezone: 'Asia/Shanghai',
    totals: metrics('1.2000000000000000001', 230, { pending_calls: 3, completeness: 'partial', tokens_missing: 40 }),
    models: [{ model, ...metrics('1.2000000000000000001', 230, { pending_calls: 1, completeness: 'partial', tokens_missing: 40 }) },
      { model: 'lab/b', ...missing }, { model: 'lab/c', ...metrics('0', 0) }],
    students,
    keys: [{ api_key_id: 'key-1', api_key_name: '学校 Key 甲', user_id: 's1', user_name: '学生甲', project_name: '教学项目一', ...first }],
  }
}

function drawer(props: Partial<InstanceType<typeof BeichenModelUsageDrawer>['$props']> = {}) {
  return mount(BeichenModelUsageDrawer, { props: { open: true, range: 'month', ...props }, global: { stubs: { AppDialog } } })
}

function press(root: Pick<DOMWrapper<Element>, 'findAll'>, label: string) {
  const button = root.findAll('button').find(node => node.text() === label)
  if (!button) throw new Error('没有找到按钮 ' + label)
  return button.trigger('click')
}

describe('北辰模型用量明细', () => {
  it('按模型显示完整性和待核，展开后同一账号的项目用量精确合计', async () => {
    const wrapper = drawer({ usage: usage() })
    expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('待核笔数3')
    expect(wrapper.get('td[data-completeness="complete"]').text()).toBe('完整')
    expect(wrapper.get('td[data-completeness="partial"]').text()).toBe('部分缺失')
    const missing = wrapper.get('td[data-completeness="missing"]').element.closest('tr')!
    expect(missing.textContent).toContain('缺失')
    expect(missing.querySelector('[data-label="Token"]')?.textContent?.trim()).toBe('未回报')
    const expand = wrapper.get('button[aria-label="查看 lab/a 的学生用量"]')
    expect(expand.attributes('aria-expanded')).toBe('false')
    await expand.trigger('click')
    expect(expand.attributes('aria-expanded')).toBe('true')
    const [student] = wrapper.findAll('tr.expanded-student')
    expect(wrapper.findAll('tr.expanded-student')).toHaveLength(1)
    expect(student.get('th').text()).toContain('学生甲')
    expect(student.get('th small').text()).toBe('教学项目一 · 教学项目二')
    expect(student.findAll('td').map(cell => cell.text())).toEqual([
      '1.2000000000000000001', '230 未归类 Token 40', '部分缺失', '1 笔',
    ])
    expect(wrapper.get('tbody').text()).not.toContain('学生乙')
    await expand.trigger('click')
    expect(wrapper.find('tr.expanded-student').exists()).toBe(false)
    await press(wrapper, '按学生账号')
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.get('tbody').text()).toContain('学生乙')
    expect(wrapper.get('tbody').text()).toContain('2 笔')
  })

  it('旧响应缺少 Key 分型号明细时保留当前型号汇总，仍可主动看全部', async () => {
    const wrapper = drawer({ usage: usage(), initialModel: 'LAB/B' })
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.get('tr.expanded-student').text()).toContain('学生乙')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('待核笔数2')
    await press(wrapper, '按 Key')
    expect(wrapper.text()).toContain('部分 Key 的分型号明细未回报')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('待核笔数2')
    expect(wrapper.find('tbody').exists()).toBe(false)
    await press(wrapper, '查看全部模型')
    expect(wrapper.get('tbody').text()).toContain('学校 Key 甲')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('待核笔数3')
    await press(wrapper.get('[aria-label="用量时间范围"]'), '当天')
    expect(wrapper.emitted('update:range')).toEqual([['day']])
  })

  it('同名上游型号按校内模型身份选择，不混淆不同模型的费用', () => {
    const data = usage()
    data.models = [
      { ...metrics('1', 100), model: 'same-upstream', model_key: 'school:a', school_model: 'school/a', school_model_id: 'a' },
      { ...metrics('9', 900), model: 'same-upstream', model_key: 'school:b', school_model: 'school/b', school_model_id: 'b' },
    ]
    const wrapper = drawer({ usage: data, initialModel: 'school/b' })
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('本月已扣积分9')
    expect(wrapper.get('tbody').text()).toContain('school/b')
    expect(wrapper.get('tbody').text()).not.toContain('school/a')
  })

  it('加载、读取失败和空集分别展示，旧数据没有核对字段时显示待确认', async () => {
    const wrapper = drawer({ loading: true })
    expect(wrapper.get('[role="status"]').text()).toContain('正在读取用量')
    await wrapper.setProps({ loading: false, error: '用量暂不可用' })
    await press(wrapper, '重试读取用量')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    await wrapper.setProps({ error: '', usage: {
      ...usage(), models: [], students: [],
      totals: metrics('0', 0, { cache_hit_rate: null, completeness: null, tokens_missing: null }),
    } })
    expect(wrapper.text()).toContain('此时段暂无用量')
    expect(wrapper.get('[aria-label="用量汇总"]').text()).toContain('完整性待确认')
    expect(wrapper.text()).not.toMatch(/undefined|NaN/)
  })
})
