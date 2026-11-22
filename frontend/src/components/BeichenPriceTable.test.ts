import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import BeichenPriceTable from './BeichenPriceTable.vue'
import componentSource from './BeichenPriceTable.vue?raw'

const observedAt = '2026-11-21T13:07:28Z'
const website = {
  observed_at: observedAt,
  price_conditions_exact: {
    billing_mode: 'token', input_price: '0.000003', output_price: '0.000015',
    cache_write_price: '0.00000375', cache_read_price: '0.0000003',
  },
}

describe('北辰教师报价表', () => {
  it('表头显示来源、采集时间和单位，单元格先显示积分', () => {
    const wrapper = mount(BeichenPriceTable, { props: { website, groupMultiplier: '0.6' } })
    expect(wrapper.get('header').text()).toContain('官网正式价')
    expect(wrapper.get('header').text()).toContain('采集时间')
    expect(wrapper.get('header').text()).not.toContain('observed_at')
    expect(wrapper.get('header').text()).toContain('每百万 Token')
    expect(wrapper.get('time').attributes('datetime')).toBe(observedAt)
    expect(wrapper.get('time').text()).toContain('21:07')
    expect(wrapper.findAll('th[scope="col"]').map(cell => cell.text()))
      .toEqual(['项目', '官网价', '分组倍率', '结算价', '学生实价'])
    expect(wrapper.findAll('th[scope="row"]').map(cell => cell.text()))
      .toEqual(['输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])

    const cells = wrapper.get('[data-key="cache_write_5m"]').findAll('td')
    expect(cells.map(cell => cell.text())).toEqual(['375 积分（3.75 元）', '0.60×', '225 积分（2.25 元）', '247.5 积分（2.475 元）'])
    expect(wrapper.get('[data-key="cache_write_1h"]').findAll('td').map(cell => cell.text()))
      .toEqual(['— 未列单价', '0.60×', '—', '—'])
    expect(wrapper.text()).not.toContain('缺价')
  })

  it('切换到官网后替换反推来源、金额和观测时间', async () => {
    const derived = {
      observed_at: '2026-11-19T09:00:00Z',
      price_conditions_exact: { billing_mode: 'token', input_price: '0.000008' },
    }
    const wrapper = mount(BeichenPriceTable, { props: { derived, groupMultiplier: '0.6' } })
    expect(wrapper.get('header').text()).toContain('历史流水反推价，待核对')
    expect(wrapper.get('time').attributes('datetime')).toBe(derived.observed_at)
    expect(wrapper.findAll('th[scope="col"]')[1].text()).toBe('反推基础价')
    expect(wrapper.get('[data-key="input"]').findAll('td')[3].text()).toBe('528 积分（5.28 元）')

    await wrapper.setProps({ website })
    expect(wrapper.get('header').text()).toContain('官网正式价')
    expect(wrapper.get('header').text()).not.toContain('反推')
    expect(wrapper.get('time').attributes('datetime')).toBe(observedAt)
    expect(wrapper.findAll('th[scope="col"]')[1].text()).toBe('官网价')
    expect(wrapper.get('[data-key="input"]').findAll('td')[3].text()).toBe('198 积分（1.98 元）')
  })

  it('保留可用键盘滚动的横向表格，并对齐数字', () => {
    const wrapper = mount(BeichenPriceTable, { props: { website } })
    expect(wrapper.get('[role="region"]').attributes('tabindex')).toBe('0')
    expect(wrapper.get('[role="region"]').find('table').exists()).toBe(true)
    expect(componentSource).toMatch(/\.quote-scroll\s*{[^}]*overflow-x:\s*auto/)
    expect(componentSource).toMatch(/\.quote-table thead\s*{[^}]*display:\s*table-header-group/)
    expect(componentSource).toMatch(/\.quote-table th, \.quote-table td\s*{[^}]*display:\s*table-cell[^}]*white-space:\s*nowrap/)
    expect(componentSource).toMatch(/\.quote-number\s*{[^}]*text-align:\s*right[^}]*font-variant-numeric:\s*tabular-nums/)
  })

  it('窄价格卡片改为两列档位布局，完整显示学生实价', () => {
    expect(componentSource).toMatch(/\.beichen-price-quote\s*{[^}]*container-type:\s*inline-size/)
    expect(componentSource).toMatch(/@container\s*\(max-width:\s*30rem\)[\s\S]*?\.quote-table tbody\s*{[^}]*display:\s*grid/)
    expect(componentSource).toMatch(/@container[\s\S]*?\.quote-table td:nth-child\(5\)::before\s*{\s*content:\s*'学生实价'\s*;?\s*}/)
    expect(componentSource).toMatch(/@container[\s\S]*?\.quote-table td strong,[\s\S]*?\.quote-table td small\s*{[^}]*display:\s*block/)
  })
})
