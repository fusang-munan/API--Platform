import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BeichenPriceConditions, BeichenWebsiteLocation } from '../api/types'
import { buildCurrentGroupPriceRow } from '../utils/groupPriceRows'
import BeichenGroupPrices from './BeichenGroupPrices.vue'
import componentSource from './BeichenGroupPrices.vue?raw'

const claudePrice: BeichenPriceConditions = {
  billing_mode: 'token', input_price: '0.000004', output_price: '0.00002',
  cache_write_price: '0.000005', cache_write_1h_price: '0.000006', cache_read_price: '0.0000004',
}

function location(id: number, name: string, price: BeichenPriceConditions | null = claudePrice,
  overrides: Partial<BeichenWebsiteLocation> = {}): BeichenWebsiteLocation {
  return {
    name: 'claude-opus-5-5', platform: 'anthropic', groups: [{ id, name, status: 'active' }],
    observed_at: '2026-11-21T00:00:00Z', has_price: price != null, price_complete: price != null,
    ...(price ? { price_conditions_exact: price } : {}), ...overrides,
  }
}

function currentRow() {
  return buildCurrentGroupPriceRow([
    location(2, '[CC1]Claudecode-kiro-015', claudePrice, { channel: 'Claude 渠道', price_scope: 'platform' }),
  ], 16, { keyGroup: { id: 16, name: '[CC2]ClaudeCode高智', rate: '0.6' } })
}

describe('北辰模型价格表', () => {
  it('每个模型只有一张表：五个价格列、上游基础价和学生实价两排', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { row: currentRow(), title: '当前 Key 分组 16 · 生效 0.60×' } })
    expect(wrapper.findAll('table')).toHaveLength(1)
    expect(wrapper.get('.group-prices-head strong').text()).toBe('当前 Key 分组 16 · 生效 0.60×')
    expect(wrapper.findAll('thead th').map(node => node.text()))
      .toEqual(['价格', '输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
    expect(wrapper.findAll('tbody [data-price-row] > th').map(node => node.text()))
      .toEqual(['上游基础价（元 / 1M）', '学生实价（积分 / 1M）'])
    expect(wrapper.findAll('[data-price-row="official"] td').map(node => node.text())).toEqual(['4', '20', '5', '6', '0.4'])
    expect(wrapper.findAll('[data-price-row="student"] td').map(node => node.text())).toEqual(['264', '1320', '330', '396', '26.4'])
    expect(wrapper.findAll('.group-price-summary')).toHaveLength(0)
    expect(componentSource).not.toContain('other-groups')
  })

  it('基础价来自其他分组时显示来源和当前分组倍率，不显示基础价分组倍率', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { row: currentRow(), title: '当前 Key 分组 16 · 生效 0.60×' } })
    expect(wrapper.get('[data-testid="price-basis"]').text())
      .toBe('基础价来自 CC1 同名模型；当前 Key 分组 16 的倍率为 0.60×')
    expect(wrapper.text()).not.toContain('0.15×')
    expect(wrapper.text()).not.toContain('Claudecode-kiro-015 ·')
  })

  it('没有官网基础价时显示未匹配，并把历史反推价放在小字里', () => {
    const row = buildCurrentGroupPriceRow([], 16, { keyGroup: { id: 16, name: '分组 16', rate: '0.6' } })
    const wrapper = mount(BeichenGroupPrices, { props: {
      row, title: '当前 Key 分组 16 · 生效 0.60×', derivedNote: '历史流水参考：输入 4 元、输出 20 元（每 1M Token，未作为主价）',
    } })
    expect(wrapper.get('[data-testid="price-base-missing"]').text()).toBe('未匹配到官网基础价')
    expect(wrapper.get('[data-testid="derived-reference"]').text()).toContain('历史流水参考')
    expect(wrapper.findAll('[data-price-row="official"] td').map(node => node.text())).toEqual(['未列', '未列', '未列', '未列', '未列'])
    expect(wrapper.findAll('[data-price-row="student"] td').map(node => node.text())).toEqual(['—', '—', '—', '—', '—'])
  })

  it('没有当前分组价格行时只显示提示', () => {
    const wrapper = mount(BeichenGroupPrices, { props: { row: null } })
    expect(wrapper.text()).toContain('未匹配到官网基础价')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('账号倍率优先显示为生效倍率，并在表头说明分组与账号差异', () => {
    const row = buildCurrentGroupPriceRow([
      location(10, 'GPT 分组', { billing_mode: 'token', input_price: '0.000008', output_price: '0.00004' }),
    ], 10, { keyGroup: { id: 10, name: 'GPT 分组', rate: '0.24', account_rate: '0.2' } })
    const wrapper = mount(BeichenGroupPrices, { props: { row, title: '当前 Key 分组 10 · 生效 0.20×' } })
    expect(wrapper.get('[data-testid="rate-note"]').text()).toBe('分组 0.24× / 账号 0.20×')
    expect(wrapper.findAll('[data-price-row="student"] td').map(node => node.text())).toEqual(['176', '880', '—', '—', '—'])
  })

  it('价格数字右对齐、使用等宽字体和等宽数字', () => {
    expect(componentSource).toContain('text-align: right')
    expect(componentSource).toContain('font-variant-numeric: tabular-nums')
    expect(componentSource).toContain('ui-monospace')
    expect(componentSource).toContain('class="numeric-cell"')
  })

  it('五个价格列在窄容器内分组堆叠，不横向溢出', () => {
    expect(componentSource).toContain('@container (max-width: 34rem)')
    expect(componentSource).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(componentSource).not.toContain('overflow-x: auto')
    expect(componentSource).not.toContain('min-width: 20rem')
  })

  it('只提供一个外部操作插槽', () => {
    const wrapper = mount(BeichenGroupPrices, {
      props: { row: currentRow() }, slots: { actions: '<button class="external-action">采集时间</button>' },
    })
    expect(wrapper.findAll('.external-action')).toHaveLength(1)
    expect(componentSource).toContain('<slot name="actions" />')
  })
})
