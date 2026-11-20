import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../api/client'
import type { SchoolCatalogRow } from '../utils/beichenModels'
import BeichenPriceDrawer from './BeichenPriceDrawer.vue'

const api = vi.hoisted(() => ({ setBeichenModelPrice: vi.fn() }))
vi.mock('../api/teacher', () => ({ teacherAPI: api }))
const Dialog = defineComponent({ props: ['open', 'title'], template: '<section v-if="open" role="dialog" :aria-label="title"><slot /></section>' })
enableAutoUnmount(afterEach)

function row(): SchoolCatalogRow {
  return {
    model: 'lab/model', school: 'lab/model', status: 'active', canEnable: false, canDisable: true,
    routeCount: 2, readyCount: 2, mode: 'follow', priceReady: true,
    credentials: [{ credential_id: 'one', label: '教学一号', mode: 'follow', priceReady: true }, { credential_id: 'two', label: '教学二号', mode: 'follow', priceReady: true }],
    quotes: [{ key: 'quote', mode: 'follow', multiplier: '0.15×', multiplierLabel: '倍率 0.15×', status: 'confirmed', statusLabel: '报价已确认',
      priceReady: true, notes: [], standard: null, actual: null, actualRates: { input: '0.5', output: '125.000001', cache_read: '0', cache_write: null } }],
  }
}
function view(model = row()) {
  return mount(BeichenPriceDrawer, { props: { open: true, providerId: 'north', row: model }, global: { stubs: { AppDialog: Dialog } } })
}
function button(wrapper: ReturnType<typeof view>, label: string) {
  const target = wrapper.findAll('button').find(node => node.text() === label)
  if (!target) throw new Error('没有找到按钮 ' + label)
  return target
}
async function manual(wrapper: ReturnType<typeof view>) {
  await wrapper.get('input[value="manual"]').setValue(true)
}
beforeEach(() => {
  api.setBeichenModelPrice.mockReset().mockResolvedValue({ saved: true, credentials: [{ credential_id: 'one', label: '教学一号' }] })
})

describe('北辰模型手动价', () => {
  it('直接提交积分字符串，保留六位小数，空缓存按零，不在前端乘一百万', async () => {
    const wrapper = view()
    await manual(wrapper)
    await wrapper.get('input[name="input"]').setValue('0.500001')
    await wrapper.get('input[name="output"]').setValue('123.456789')
    await wrapper.get('input[name="cache_read"]').setValue('')
    await wrapper.get('input[name="cache_write"]').setValue('0.000001')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', {
      model: 'lab/model', mode: 'manual', rates: { input: '0.500001', output: '123.456789', cache_read: '0', cache_write: '0.000001' },
    })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('update:open')).toEqual([[false]])
    expect(wrapper.text()).toContain('教学一号')
    expect(wrapper.text()).toContain('教学二号')
  })

  it('恢复跟随官网时完全省略 rates', async () => {
    const model = row()
    model.mode = 'manual'
    model.quotes[0].mode = 'manual'
    const wrapper = view(model)
    await wrapper.get('input[value="follow"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', { model: 'lab/model', mode: 'follow' })
  })

  it('带入官网基础价并精确预览 0.5 积分折合 0.005 元和实扣 0.55 积分', async () => {
    const wrapper = view()
    await manual(wrapper)
    await wrapper.get('input[name="input"]').setValue('9')
    await button(wrapper, '从官网价带入').trigger('click')
    expect(wrapper.get<HTMLInputElement>('input[name="input"]').element.value).toBe('0.5')
    expect(wrapper.get<HTMLInputElement>('input[name="output"]').element.value).toBe('125.000001')
    const hint = wrapper.get('input[name="input"]').element.parentElement!.textContent!
    expect(hint).toContain('约 0.005 元 / 百万 Token')
    expect(hint).toContain('× 1.10 后实扣 0.55 积分')
    expect(wrapper.text()).toContain('历史扣费保持原记账金额')
  })

  it.each(['', '-1', '1.0000001', '1e3', 'NaN', '  '])('拒绝无效的输入价 %s，保留草稿并显示字段错误', async input => {
    const wrapper = view()
    await manual(wrapper)
    await wrapper.get('input[name="input"]').setValue(input)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('input[name="input"]').attributes('aria-invalid')).toBe('true')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('无官网报价时直接进入手动模式，禁用带入，输入输出为必填', async () => {
    const model = row()
    model.priceReady = false
    model.quotes[0].priceReady = false
    model.quotes[0].actualRates = null
    model.quotes[0].status = 'not_listed'
    const wrapper = view(model)
    expect(wrapper.get<HTMLInputElement>('input[value="manual"]').element.checked).toBe(true)
    expect(button(wrapper, '从官网价带入').element.disabled).toBe(true)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('请填写输入价格')
    expect(wrapper.text()).toContain('请填写输出价格')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
  })

  it('保存失败后可修正重试，提交期间禁止重复保存', async () => {
    let reject!: (cause: Error) => void
    api.setBeichenModelPrice.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = view()
    await manual(wrapper)
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(api.setBeichenModelPrice).toHaveBeenCalledTimes(1)
    expect(wrapper.get<HTMLButtonElement>('button[type="submit"]').element.disabled).toBe(true)
    reject(new APIError('同步尚未就绪', 409, 'PRICE_NOT_READY'))
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBeTruthy()
    expect(wrapper.get<HTMLInputElement>('input[name="input"]').element.value).toBe('0.5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })
})
