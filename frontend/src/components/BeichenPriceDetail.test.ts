import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { PriceDetailView } from '../utils/beichenModels'
import BeichenPriceDetail from './BeichenPriceDetail.vue'
import componentSource from './BeichenPriceDetail.vue?raw'

const priceCss = readFileSync('src/styles-beichen.css', 'utf8')

function detail(): PriceDetailView {
  return {
    billingModeLabel: '按 Token（每 1M Token 计价）',
    source: 'website',
    sourceLabel: '官网正式价',
    observedAt: null,
    observedLabel: '未记录',
    rows: [
      { key: 'input', label: '输入', yuan: '3', credits: '300', missing: false, notApplicable: false },
      { key: 'output', label: '输出', yuan: '15', credits: '1500', missing: false, notApplicable: false },
      { key: 'cache_write_5m', label: '缓存写入(5m)', yuan: '3.75', credits: '375', missing: false, notApplicable: false },
      { key: 'cache_write_1h', label: '缓存写入(1h)', yuan: null, credits: null, missing: true, notApplicable: false },
      { key: 'cache_read', label: '缓存读取', yuan: '0.3', credits: '30', missing: false, notApplicable: false },
    ],
  }
}

function amounts(wrapper: ReturnType<typeof mount>, key: string) {
  return wrapper.get('[data-key="' + key + '"]')
    .findAll('.price-yuan, .price-credits, .price-unit').map(node => node.text())
}

describe('北辰价格详情排版', () => {
  it('五项单价固定横排，元、积分、每百万 Token 单位按顺序读完', () => {
    const wrapper = mount(BeichenPriceDetail, { props: { detail: detail() } })
    expect(amounts(wrapper, 'input')).toEqual(['3 元', '300 积分', '/ 1M Token'])
    expect(amounts(wrapper, 'output')).toEqual(['15 元', '1500 积分', '/ 1M Token'])
    expect(amounts(wrapper, 'cache_write_5m')).toEqual(['3.75 元', '375 积分', '/ 1M Token'])
    expect(amounts(wrapper, 'cache_read')).toEqual(['0.3 元', '30 积分', '/ 1M Token'])
    expect(amounts(wrapper, 'input').join(' ')).toBe('3 元 300 积分 / 1M Token')
    expect(wrapper.findAll('.price-detail-item').map(row => row.get('.price-label').text()))
      .toEqual(['计费模式', '输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取'])
  })

  it('缺价显示破折号并写出具体字段，不隐藏单位', () => {
    const wrapper = mount(BeichenPriceDetail, { props: { detail: detail() } })
    const row = wrapper.get('[data-key="cache_write_1h"]')
    expect(row.attributes('data-missing')).toBe('true')
    expect(row.findAll('.price-yuan, .price-credits, .price-unit').map(node => node.text()))
      .toEqual(['—', '—', '/ 1M Token'])
    expect(row.get('.price-gap').text()).toBe('未列单价：缓存写入(1h)')
  })

  it('数值不逐字换行，窄容器改为标签单独一行', () => {
    expect(componentSource).toContain('class="price-amount"')
    const figure = priceCss.match(/\.price-figure\{[^}]*\}/)?.[0] ?? ''
    expect(figure).toContain('white-space:nowrap')
    expect(figure).toContain('font-variant-numeric:tabular-nums')
    const value = priceCss.match(/\.price-value\{[^}]*\}/)?.[0] ?? ''
    expect(value).toContain('text-align:right')
    expect(value).not.toContain('overflow-wrap')
    const table = priceCss.match(/\.price-table\{[^}]*\}/)?.[0] ?? ''
    expect(table).toContain('grid-template-columns:minmax(0,1fr)')
    expect(priceCss).toContain('@container price (max-width:14rem)')
    expect(priceCss).toContain('.price-row{grid-template-columns:minmax(0,1fr);gap:1px}')
  })
})
