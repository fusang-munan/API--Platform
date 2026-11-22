import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { buildGroupPriceRows, type GroupPriceRow } from '../utils/groupPriceRows'
import BeichenGroupPrices from './BeichenGroupPrices.vue'
import componentSource from './BeichenGroupPrices.vue?raw'

const claudePrice: BeichenPriceConditions = {
  billing_mode: 'token',
  input_price: '0.000004',
  output_price: '0.00002',
  cache_write_price: '0.000005',
  cache_write_1h_price: '0.000006',
  cache_read_price: '0.0000004',
  intervals: [],
}

function location(group: BeichenWebsiteGroup,
  price: BeichenPriceConditions | null = claudePrice): BeichenWebsiteLocation {
  return {
    name: 'claude-opus-5-5', platform: 'anthropic', groups: [group], observed_at: '2026-11-21T00:00:00Z',
    has_price: price != null, price_complete: price != null,
    ...(price ? { price_conditions_exact: price } : {}),
  }
}

function claudeRows(): GroupPriceRow[] {
  return buildGroupPriceRows([
    location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' }),
  ], null, {
    keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' },
    derived: claudePrice,
  })
}

function rowsWithoutCurrent(): GroupPriceRow[] {
  return buildGroupPriceRows([
    location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' }),
  ], null)
}

describe('北辰分组价格组件', () => {
  it('默认只展开当前 Key 分组，其它分组折叠', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    const own = wrapper.get('[data-group-id="16"]')
    expect(own.attributes('data-current')).toBe('true')
    expect(own.classes()).toContain('is-current')
    expect(own.find('table').exists()).toBe(true)
    expect(wrapper.get('[data-group-id="2"]').find('table').exists()).toBe(false)
    expect(wrapper.get('[data-group-id="2"] .group-price-summary').text())
      .toBe('[CC1]Claudecode-kiro-015 · 0.15×')
  })

  it('分组摘要与当前分组标题使用相同的两位小数倍率', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    expect(wrapper.get('[data-group-id="16"] .group-price-title').text())
      .toBe('[CC2]ClaudeCode高智 · 0.60×')
  })

  it('展开分组后固定两排：官网价和学生实价', async () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    const summary = wrapper.get('[data-group-id="2"] .group-price-summary')
    expect(summary.attributes('aria-expanded')).toBe('false')
    await summary.trigger('click')
    const row = wrapper.get('[data-group-id="2"]')
    expect(row.find('table').exists()).toBe(true)
    expect(summary.attributes('aria-expanded')).toBe('true')
    expect(row.findAll('thead th').map(node => node.text()))
      .toEqual(['价格', '输入', '输出', '缓存 5m', '缓存 1h', '缓存读取'])
    const official = row.get('[data-price-row="official"]')
    expect(official.findAll('td').map(node => node.text())).toEqual(['4', '20', '5', '6', '0.4'])
    const student = row.get('[data-price-row="student"]')
    expect(student.findAll('td').map(node => node.text())).toEqual(['66', '330', '82.5', '99', '6.6'])
    await summary.trigger('click')
    expect(row.find('table').exists()).toBe(false)
  })

  it('官网缺该组价时官网价显示未列，学生价用历史流水反推', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    const row = wrapper.get('[data-group-id="16"]')
    const official = row.get('[data-price-row="official"]')
    expect(official.findAll('td').map(node => node.text()))
      .toEqual(['未列', '未列', '未列', '未列', '未列'])
    const student = row.get('[data-price-row="student"]')
    expect(student.findAll('td').map(node => node.text()))
      .toEqual(['264', '1320', '330', '396', '26.4'])
    expect(row.get('[data-testid="student-derived"]').text()).toContain('历史流水反推')
  })

  it('表头只保留标题，不显示分组数量', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    expect(wrapper.get('header').text()).toBe('分组价格对比')
    expect(componentSource).not.toContain('共 {{ rows.length }} 个分组')
  })

  it('当前组高亮，并显示唯一的当前 Key 分组标记', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: claudeRows() } })
    expect(wrapper.findAll('.group-price-current')).toHaveLength(1)
    expect(wrapper.get('.group-price-current').text()).toBe('当前 Key 分组')
    expect(wrapper.get('[data-group-id="16"]').classes()).toContain('is-current')
  })

  it('没有当前组时仍列出分组，不误报未匹配', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rowsWithoutCurrent() } })
    expect(wrapper.findAll('[data-group-id]')).toHaveLength(1)
    expect(wrapper.findAll('table')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('未匹配')
    expect(wrapper.find('[data-testid="current-group-missing"]').exists()).toBe(false)
  })

  it('以 currentGroupId 标记并默认展开当前组', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rowsWithoutCurrent(), currentGroupId: 2 } })
    expect(wrapper.get('[data-group-id="2"]').attributes('data-current')).toBe('true')
    expect(wrapper.get('[data-group-id="2"]').find('table').exists()).toBe(true)
  })

  it('直接传官网分组、/keys 当前组和历史反推价时，当前组默认展开', () => {
    const wrapper = mount(BeichenGroupPrices, {
      props: {
        locations: [location({ id: 2, name: '[CC1]Claudecode-kiro-015', status: 'active', rate_multiplier: '0.15' })],
        keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' },
        derivedPrice: claudePrice,
      },
    })
    const own = wrapper.get('[data-group-id="16"]')
    expect(own.attributes('data-current')).toBe('true')
    expect(own.find('table').exists()).toBe(true)
    expect(own.get('[data-price-row="student"] [data-price="input"]').text()).toBe('264')
  })

  it('行里缺少当前组时补出当前组，不显示未匹配', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rowsWithoutCurrent(), currentGroupId: 16 } })
    const own = wrapper.get('[data-group-id="16"]')
    expect(own.attributes('data-current')).toBe('true')
    expect(own.find('table').exists()).toBe(true)
    expect(own.get('[data-price-row="official"] [data-price="input"]').text()).toBe('未列')
    expect(own.get('[data-price-row="student"] [data-price="input"]').text()).toBe('—')
    expect(wrapper.text()).not.toContain('未匹配')
  })

  it('教师页回写扁平学生价后，表内仍显示回写值', () => {
    const rows = claudeRows()
    const current = rows.find(row => row.is_current_key_group)
    expect(current).toBeDefined()
    current!.input = '999'
    const wrapper = mount(BeichenGroupPrices, { props: { rows } })
    expect(wrapper.get('[data-group-id="16"] [data-price-row="student"] [data-price="input"]').text())
      .toBe('999')
  })

  it('组件只提供一个外部操作插槽，不重复重扫提示', () => {
    const wrapper = mount(BeichenGroupPrices, {
      props: { rows: claudeRows() }, slots: { actions: '<button class="external-action">外部操作</button>' },
    })
    expect(wrapper.findAll('.external-action')).toHaveLength(1)
    expect(componentSource).toContain('<slot name="actions" />')
    expect(componentSource).not.toContain('重扫该 Key')
  })

  it('价格数字右对齐并使用等宽数字', () => {
    expect(componentSource).toContain('text-align: right')
    expect(componentSource).toContain('font-variant-numeric: tabular-nums')
    expect(componentSource).toContain('class="numeric-cell"')
  })

  it('分组五项价格表在窄卡片内缩放而不横向溢出', () => {
    expect(componentSource).toMatch(/\.group-price-table\s*{[^}]*min-width:\s*0/)
    expect(componentSource).not.toContain('min-width: 20rem')
    expect(componentSource).toMatch(/\.group-price-table th[^}]*white-space:\s*normal/)
  })
})
