import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { buildGroupPriceRows, type GroupPriceRow } from '../utils/groupPriceRows'
import BeichenGroupPrices from './BeichenGroupPrices.vue'
import componentSource from './BeichenGroupPrices.vue?raw'

const glmPrice: BeichenPriceConditions = {
  billing_mode: 'token', input_price: '0.000008', output_price: '0.000028',
  cache_write_price: '0', cache_write_1h_price: null, cache_read_price: '0.000002', intervals: [],
}

function location(group: BeichenWebsiteGroup): BeichenWebsiteLocation {
  return {
    name: 'glm-5.3', platform: 'openai', groups: [group], observed_at: '2026-11-21T00:00:00Z',
    has_price: true, price_complete: true, price_conditions_exact: glmPrice,
  }
}

function rows(): GroupPriceRow[] {
  return buildGroupPriceRows([
    location({ id: 15, name: '分组 15', status: 'active', rate_multiplier: '0.16' }),
    location({ id: 14, name: '分组 14', status: 'active', rate_multiplier: '0.15' }),
    location({ id: 6, name: '分组 6', status: 'active', rate_multiplier: '0.1' }),
  ], 15)
}

describe('北辰分组价格组件', () => {
  it('默认只展开当前 Key 分组，其余分组显示折叠行', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rows() } })
    const own = wrapper.get('[data-group-id="15"]')
    expect(own.attributes('data-current')).toBe('true')
    expect(own.classes()).toContain('is-current')
    expect(own.find('table').exists()).toBe(true)
    expect(wrapper.get('[data-group-id="14"]').find('table').exists()).toBe(false)
    expect(wrapper.get('[data-group-id="14"] .group-price-summary').text())
      .toBe('分组 14 · 0.15× · 展开对比')
    expect(wrapper.get('[data-group-id="6"] .group-price-summary').text())
      .toBe('分组 6 · 0.1× · 展开对比')
  })

  it('点击其它分组展开五项价格小表，再点一次收起', async () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rows() } })
    const summary = wrapper.get('[data-group-id="14"] .group-price-summary')
    expect(summary.attributes('aria-expanded')).toBe('false')
    await summary.trigger('click')
    const row = wrapper.get('[data-group-id="14"]')
    expect(row.find('table').exists()).toBe(true)
    expect(summary.attributes('aria-expanded')).toBe('true')
    expect(row.findAll('th').map(node => node.text())).toEqual(['输入', '输出', '缓存 5m', '缓存 1h', '缓存读取'])
    expect(row.get('[data-price="input"]').text()).toBe('132')
    expect(row.get('[data-price="cache1h"]').text()).toBe('—')
    await summary.trigger('click')
    expect(row.find('table').exists()).toBe(false)
  })

  it('当前组高亮，并显示唯一的当前 Key 分组标记', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { rows: rows() } })
    expect(wrapper.findAll('.group-price-current')).toHaveLength(1)
    expect(wrapper.get('.group-price-current').text()).toBe('当前 Key 分组')
    expect(wrapper.get('[data-group-id="15"]').classes()).toContain('is-current')
  })

  it('没有当前组时全部折叠并提示未匹配', () => {
    const withoutCurrent = rows().map(row => ({ ...row, is_current_key_group: false }))
    const wrapper = mount(BeichenGroupPrices, { props: { rows: withoutCurrent } })
    expect(wrapper.findAll('table')).toHaveLength(0)
    expect(wrapper.get('[data-testid="current-group-missing"]').text()).toBe('当前 Key 分组未匹配')
  })

  it('组件只提供一个外部操作插槽，不重复重扫提示', () => {
    const wrapper = mount(BeichenGroupPrices, {
      props: { rows: rows() }, slots: { actions: '<button class="external-action">外部操作</button>' },
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
