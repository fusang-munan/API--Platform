import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../api/client'
import type { BeichenCatalogModel, BeichenPreferredRoute } from '../api/types'
import type { SchoolCatalogRow, TeacherPriceModel } from '../utils/beichenModels'
import BeichenPriceDrawer from './BeichenPriceDrawer.vue'

const api = vi.hoisted(() => ({ beichenOverview: vi.fn(), setBeichenModelPrice: vi.fn() }))
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
function overview() {
  const route: BeichenPreferredRoute = {
    school_model: 'lab/model', upstream_model: 'lab/model', adapter: 'openai_compatible',
    provider_id: 'north', credential_id: 'one', credential_label: '首选线路标签', priority: 1, state: 'ready',
  }
  const model_catalog: BeichenCatalogModel[] = [{
    model: 'LAB/Model', credential_ids: ['one', 'three'], key_count: 4, price_state: 'confirmed',
    preferred_routes: [
      route, { ...route, adapter: 'anthropic' }, { ...route, school_model: 'lab/alias', credential_id: 'two' },
      { ...route, provider_id: 'other', credential_id: 'foreign' },
      { ...route, upstream_model: 'lab/other', credential_id: 'unrelated' },
    ],
  }]
  const models: TeacherPriceModel[] = [
    { credential_id: 'one', model: 'lab/model', mode: 'follow', price_status: 'confirmed',
      school_rates: { input: '500000', output: '125000001', cache_read: '0', cache_write: null },
      upstream_rates: { input: '5000', output: '1250000.01', cache_read: '0', cache_write: null } },
    { credential_id: 'four', model: 'lab/model', mode: 'follow', price_status: 'conditional',
      school_rates: null, upstream_rates: null },
  ]
  return {
    credentials: [
      { credential_id: 'one', label: '教学一号' }, { credential_id: 'two', label: '教学二号' },
      { credential_id: 'three', label: '仅目录渠道' }, { credential_id: 'four', label: '仅报价渠道' },
      { credential_id: 'idle', label: '无此模型的渠道' }, { credential_id: 'unrelated', label: '无关模型渠道' },
    ],
    model_catalog, datasets: [], comparison: [], models,
    school_multipliers: { one: '1.10', two: '1.10', three: '1.10', four: '1.10' },
  }
}

function mountDrawer(model = row()) {
  return mount(BeichenPriceDrawer, { props: { open: true, providerId: 'north', row: model }, global: { stubs: { AppDialog: Dialog } } })
}
async function view(model = row()) {
  const wrapper = mountDrawer(model)
  await flushPromises()
  return wrapper
}
type Drawer = ReturnType<typeof mountDrawer>

function button(wrapper: Drawer, label: string) {
  const target = wrapper.findAll('button').find(node => node.text() === label)
  if (!target) throw new Error('没有找到按钮 ' + label)
  return target
}
async function manual(wrapper: Drawer) {
  await wrapper.get('input[value="manual"]').setValue(true)
}
function affected(wrapper: Drawer) {
  return wrapper.findAll('ul[aria-label="本次将影响的全部 Key"] li').map(key => key.text())
}
function summary(wrapper: Drawer) {
  return wrapper.findAll('.confirmation-summary dd').map(cell => cell.text())
}
function priceRows(wrapper: Drawer, group = 0) {
  return wrapper.findAll('tbody')[group].findAll('tr:has(td)')
    .filter(row => !row.classes('billing-mode-row'))
    .map(row => row.findAll('th, td').slice(0, 3).map(cell => cell.text()))
}

beforeEach(() => {
  api.beichenOverview.mockReset().mockResolvedValue(overview())
  api.setBeichenModelPrice.mockReset().mockResolvedValue({
    saved: true, credentials: [], applied_credential_ids: ['four', 'two', 'one', 'three'],
  })
})

it('按条件报价和后端系数显示 55 积分，零价与缺价分别展示', async () => {
  const data = overview()
  data.model_catalog[0].price_state = 'conditional'
  data.models[0] = {
    credential_id: 'one', model: 'lab/model', mode: 'follow', price_status: 'conditional',
    quote_effective_multiplier: '0.5',
    price_conditions_exact: { billing_mode: 'token', intervals: [
      { min_tokens: '500000', max_tokens: null, input_price: '0.000001', output_price: '0', cache_read_price: null },
    ] },
  }
  api.beichenOverview.mockResolvedValue(data)
  const wrapper = await view()
  const tier = wrapper.get('[data-testid="drawer-price-tier"]')
  expect(tier.get('caption').text()).toContain('500,000')
  expect(tier.get('[data-student-price="input"]').text()).toBe('55')
  expect(tier.get('[data-student-price="output"]').text()).toBe('0')
  expect(tier.get('[data-student-price="cache_read"]').text()).toBe('缺价')
  expect(wrapper.text()).toContain('仅高档 · 基础档缺价')
})

it('条件价按默认推理强度中档系数和 12 小时缓存展示，与账本同口径', async () => {
  const data = overview()
  data.model_catalog[0].price_state = 'conditional'
  data.models[0] = {
    credential_id: 'one', model: 'lab/model', mode: 'follow', price_status: 'conditional',
    quote_effective_multiplier: '0.5',
    price_conditions_exact: {
      billing_mode: 'token', input_price: '0.000001', output_price: '0.000001',
      cache_write_12h_price: '0.000001',
      reasoning_effort_multipliers: { medium: '2' },
    },
  }
  api.beichenOverview.mockResolvedValue(data)
  const wrapper = await view()
  expect(wrapper.get('[data-testid="drawer-effort-multiplier"]').text()).toContain('默认推理强度 中 · × 2')
  const tier = wrapper.get('[data-testid="drawer-price-tier"]')
  expect(tier.find('[data-student-price="input"]').text()).toBe('110')
  expect(tier.find('[data-student-price="output"]').text()).toBe('110')
  expect(tier.find('[data-student-price="cache_write_12h"]').text()).toBe('110')
})

it('同一组百万输入的长小数在抽屉与账本均为 550.00000000000000055 积分', async () => {
  const data = overview()
  data.model_catalog[0].price_state = 'conditional'
  data.models[0] = {
    credential_id: 'one', model: 'lab/model', mode: 'follow', price_status: 'conditional',
    quote_effective_multiplier: '2.5',
    price_conditions_exact: {
      billing_mode: 'token', input_price: '0.000001000000000000000001',
      output_price: '0.000001000000000000000001',
      reasoning_effort_multipliers: { medium: '2' },
    },
  }
  api.beichenOverview.mockResolvedValue(data)
  const wrapper = await view()
  expect(wrapper.get('[data-testid="drawer-price-tier"] [data-student-price="input"]').text())
    .toBe('550.00000000000000055')
})

it('手动价按每把 Key 的学校系数预览一次', async () => {
  const data = overview()
  data.school_multipliers.two = '1'
  api.beichenOverview.mockResolvedValue(data)
  const wrapper = await view()
  await manual(wrapper)
  await wrapper.get('input[name="input"]').setValue('50')
  const hint = wrapper.get('.price-input .price-hint')
  expect(hint.text()).toContain('× 1.10 后预估扣 55 积分')
  expect(hint.text()).toContain('教学二号 · × 1 后预估扣 50 积分')
})

it('同名型号的定价抽屉使用当前校内 UUID 和绑定 Key', async () => {
  const data = overview()
  data.model_catalog = [
    { model: 'lab/model', school_model_id: 'first', credential_ids: ['one'], key_count: 1 },
    { model: 'lab/model', school_model_id: 'second', credential_ids: ['four'], key_count: 1 },
  ]
  api.beichenOverview.mockResolvedValue(data)
  const wrapper = await view({ ...row(), schoolModelId: 'second' })
  expect(affected(wrapper)).toEqual(['仅报价渠道'])
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.setBeichenModelPrice).toHaveBeenCalledWith('north', {
    model: 'lab/model', mode: 'follow', scope: 'credentials', credential_ids: ['four'],
  })
})

describe('北辰模型定价范围', () => {
  it('抽屉单独读到作废标志时禁止保存，即使响应仍含历史 Key', async () => {
    const data = overview()
    data.model_catalog[0].retired = true
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = await view()
    expect(wrapper.text()).toContain('该模型已作废，价格无法修改。')
    expect(wrapper.get('button[type="submit"]').attributes()).toHaveProperty('disabled')
    await wrapper.get('form').trigger('submit')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
  })

  it('标注反推来源，并可按正常的手动价格入口保存修正', async () => {
    const data = overview()
    data.model_catalog[0].price_state = 'derived'
    data.models[0].price_source = 'derived_from_usage'
    data.models[0].price_status = 'derived'
    api.beichenOverview.mockResolvedValue(data)
    const model = row()
    model.quotes[0].status = 'derived'
    model.quotes[0].statusLabel = '反推价，待核对'
    const wrapper = await view(model)
    expect(wrapper.get<HTMLInputElement>('input[value="follow"]').element.checked).toBe(true)
    expect(wrapper.text()).toContain('反推价，待核对')
    expect(summary(wrapper)).toEqual(['4', '4', '自动计价'])
    await manual(wrapper)
    await button(wrapper, '从反推价带入').trigger('click')
    await wrapper.get('input[name="input"]').setValue('240')
    await wrapper.get('input[name="output"]').setValue('1200')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledWith('north', expect.objectContaining({
      model: 'lab/model', mode: 'manual', rates: { input: '240', output: '1200', cache_read: '0', cache_write: '0' },
    }))
  })

  it('固定展示计费模式与五项单价，并标注来源和 observed_at', async () => {
    const data = overview()
    data.models[0].price_status = 'derived'
    data.models[0].price_source = 'derived_from_usage'
    data.models[0].price_observed_at = '2026-11-21T12:14:00Z'
    data.models[0].price_conditions_exact = {
      billing_mode: 'token', input_price: '0.000004', output_price: '0.00002',
      cache_write_price: '0.000005', cache_write_1h_price: '0.000008', cache_read_price: '0.0000002',
    }
    api.beichenOverview.mockResolvedValue(data)
    const wrapper = await view()
    const detail = wrapper.get('[data-testid="price-detail"]')
    const text = detail.text()
    expect(text).toContain('计费模式')
    expect(text).toContain('按 Token')
    for (const label of ['输入', '输出', '缓存写入(5m)', '缓存写入(1h)', '缓存读取']) expect(text).toContain(label)
    expect(text).toContain('历史流水反推价')
    expect(text).toContain('observed_at 2026/11/21 20:14')
    expect(text).toContain('4 元')
    expect(text).toContain('20 元')
    expect(text).toContain('8 元')
    expect(text).toContain('0.2 元')
    const amounts = (key: string) => detail.get('[data-key="' + key + '"]')
      .findAll('.price-yuan, .price-credits, .price-unit').map(node => node.text())
    expect(amounts('input')).toEqual(['4 元', '400 积分', '/ 1M Token'])
    expect(amounts('output')).toEqual(['20 元', '2000 积分', '/ 1M Token'])
    expect(amounts('cache_write_1h')).toEqual(['8 元', '800 积分', '/ 1M Token'])
  })

  it('默认确认全部渠道，按模型和 Key 计算影响行数，保存后显示核对一致的 Key 数', async () => {
    const wrapper = await view()
    expect(wrapper.get<HTMLInputElement>('input[value="all"]').element.checked).toBe(true)
    expect(affected(wrapper)).toHaveLength(4)
    expect(affected(wrapper)).toEqual(expect.arrayContaining(['教学一号', '教学二号', '仅目录渠道', '仅报价渠道']))
    expect(wrapper.text()).not.toContain('无此模型的渠道')
    expect(wrapper.text()).not.toContain('无关模型渠道')
    expect(summary(wrapper)).toEqual(['4', '4', '跟随官网'])
    expect(priceRows(wrapper)).toEqual([
      ['输入', '0.005', '0.5'], ['输出', '1.25000001', '125.000001'],
      ['缓存读取', '0', '0'], ['缓存写入', '缺价', '缺价'],
    ])
    expect(priceRows(wrapper, 1)).toEqual([
      ['输入', '缺价', '缺价'], ['输出', '缺价', '缺价'],
      ['缓存读取', '缺价', '缺价'], ['缓存写入', '缺价', '缺价'],
    ])
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', { model: 'lab/model', mode: 'follow', scope: 'all' })
    expect(wrapper.get('[role="status"]').text()).toBe('已保存 4 个 Key 的价格。')
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('update:open')).toBeUndefined()
    await button(wrapper, '完成').trigger('click')
    expect(wrapper.emitted('update:open')).toEqual([[false]])
  })

  it('只有勾选后才可提交子集，确认区与请求都使用选中的两个 Key', async () => {
    api.setBeichenModelPrice.mockResolvedValueOnce({ saved: true, credentials: [], applied_credential_ids: ['four', 'two'] })
    const wrapper = await view()
    await wrapper.get('input[value="credentials"]').setValue(true)
    expect(wrapper.get<HTMLButtonElement>('button[type="submit"]').element.disabled).toBe(true)
    await wrapper.get('form').trigger('submit')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
    await wrapper.get('input[type="checkbox"][value="two"]').setValue(true)
    await wrapper.get('input[type="checkbox"][value="four"]').setValue(true)
    expect(affected(wrapper)).toEqual(['教学二号', '仅报价渠道'])
    expect(summary(wrapper)).toEqual(['2', '2', '跟随官网'])
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', {
      model: 'lab/model', mode: 'follow', scope: 'credentials', credential_ids: ['two', 'four'],
    })
    expect(wrapper.get('[role="status"]').text()).toBe('已保存 2 个 Key 的价格。')
  })

  it('全部范围包含已停用渠道，子集范围按服务端规则禁选停用渠道', async () => {
    const data = overview()
    api.beichenOverview.mockResolvedValueOnce({
      ...data,
      credentials: data.credentials.map(key => key.credential_id === 'three' ? { ...key, status: 'inactive' } : key),
    })
    const wrapper = await view()
    expect(affected(wrapper)).toContain('仅目录渠道（停用）')
    await wrapper.get('input[value="credentials"]').setValue(true)
    expect(wrapper.get<HTMLInputElement>('input[type="checkbox"][value="three"]').element.disabled).toBe(true)
  })

  it('写入数量相同但 Key 不同，保留黄条并列出少写和多写的 Key', async () => {
    api.setBeichenModelPrice.mockResolvedValueOnce({
      saved: true, credentials: [{ credential_id: 'new', label: '新同步渠道' }], applied_credential_ids: ['two', 'new'],
    })
    const wrapper = await view()
    await wrapper.get('input[value="credentials"]').setValue(true)
    await wrapper.get('input[type="checkbox"][value="one"]').setValue(true)
    await wrapper.get('input[type="checkbox"][value="two"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const warning = wrapper.get('[role="alert"]')
    expect(warning.classes()).toContain('price-warning')
    expect(warning.text()).toContain('服务端实际写入范围与选择不同')
    expect(warning.text()).toContain('所选但未写入：教学一号')
    expect(warning.text()).toContain('未选但已写入：新同步渠道')
    expect(wrapper.text()).not.toContain('已保存 2 个 Key')
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('update:open')).toBeUndefined()
  })

  it('旧响应没有实际写入范围时，提示确认失败而不猜测保存的 Key 数', async () => {
    api.setBeichenModelPrice.mockResolvedValueOnce({
      saved: true, credentials: [{ credential_id: 'one', label: '教学一号' }],
    })
    const wrapper = await view()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('服务端未返回实际写入范围')
    expect(wrapper.find('.price-saved').exists()).toBe(false)
  })

  it('读取失败时暂停保存，重新读取成功后展示全部 Key', async () => {
    api.beichenOverview.mockRejectedValueOnce(new Error('获取失败'))
    const wrapper = await view()
    expect(wrapper.get('[role="alert"]').text()).toContain('读取此模型的 Key 失败')
    await wrapper.get('form').trigger('submit')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
    await button(wrapper, '重新读取').trigger('click')
    await flushPromises()
    expect(affected(wrapper)).toHaveLength(4)
    expect(button(wrapper, '保存并跟随官网').element.disabled).toBe(false)
  })

  it('目录声明的 Key 多于已列出的 Key 时暂停全量保存', async () => {
    const data = overview()
    data.model_catalog[0].key_count = 5
    api.beichenOverview.mockResolvedValueOnce(data)
    const wrapper = await view()
    expect(summary(wrapper)).toEqual(['待确认', '待确认', '跟随官网'])
    expect(wrapper.get('[role="alert"]').text()).toContain('Key 清单不完整')
    await wrapper.get('form').trigger('submit')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
  })
})

describe('北辰模型手动价', () => {
  it('直接提交积分字符串，保留六位小数，空缓存按零，不在前端乘一百万', async () => {
    const wrapper = await view()
    await manual(wrapper)
    await wrapper.get('input[name="input"]').setValue('0.500001')
    await wrapper.get('input[name="output"]').setValue('123.456789')
    await wrapper.get('input[name="cache_read"]').setValue('')
    await wrapper.get('input[name="cache_write"]').setValue('0.000001')
    expect(summary(wrapper)).toEqual(['4', '4', '手动价'])
    expect(priceRows(wrapper)).toEqual([
      ['输入', '0.00500001', '0.500001'], ['输出', '1.23456789', '123.456789'],
      ['缓存读取', '0', '0'], ['缓存写入', '0.00000001', '0.000001'],
    ])
    expect(wrapper.text()).toContain('新渠道同步后按此价生效')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', {
      model: 'lab/model', mode: 'manual', scope: 'all',
      rates: { input: '0.500001', output: '123.456789', cache_read: '0', cache_write: '0.000001' },
    })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.text()).toContain('教学一号')
    expect(wrapper.text()).toContain('教学二号')
  })

  it('恢复跟随官网时完全省略 rates', async () => {
    const model = row()
    model.mode = 'manual'
    model.quotes[0].mode = 'manual'
    const wrapper = await view(model)
    await wrapper.get('input[value="follow"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.setBeichenModelPrice).toHaveBeenCalledExactlyOnceWith('north', { model: 'lab/model', mode: 'follow', scope: 'all' })
  })

  it('带入官网基础价并精确预览 0.5 积分折合 0.005 元和实扣 0.55 积分', async () => {
    const wrapper = await view()
    await manual(wrapper)
    await wrapper.get('input[name="input"]').setValue('9')
    await button(wrapper, '从官网价带入').trigger('click')
    expect(wrapper.get<HTMLInputElement>('input[name="input"]').element.value).toBe('0.5')
    expect(wrapper.get<HTMLInputElement>('input[name="output"]').element.value).toBe('125.000001')
    const hint = wrapper.get('input[name="input"]').element.parentElement!.textContent!
    expect(priceRows(wrapper)[0]).toEqual(['输入', '0.005', '0.5'])
    expect(hint).toContain('× 1.10 后预估扣 0.55 积分')
    expect(wrapper.text()).toContain('历史扣费保持原记账金额')
  })

  it.each(['', '-1', '1.0000001', '1e3', 'NaN', '  '])('拒绝无效的输入价 %s，保留草稿并显示字段错误', async input => {
    const wrapper = await view()
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
    const data = overview()
    data.model_catalog[0].price_state = 'missing'
    data.models = data.models.filter(record => record.upstream_rates == null)
    api.beichenOverview.mockResolvedValueOnce(data)
    const wrapper = await view(model)
    expect(wrapper.get<HTMLInputElement>('input[value="manual"]').element.checked).toBe(true)
    expect(button(wrapper, '从官网价带入').element.disabled).toBe(true)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('请填写输入价格')
    expect(wrapper.text()).toContain('请填写输出价格')
    expect(api.setBeichenModelPrice).not.toHaveBeenCalled()
  })

  it('条件价模型直接打开手动定价并预填已保存的现值', async () => {
    const data = overview()
    data.model_catalog[0].price_state = 'conditional'
    data.models[1] = {
      credential_id: 'four', model: 'lab/model', mode: 'manual', price_status: 'conditional',
      school_rates: { input: '750000', output: '1250000', cache_read: '0', cache_write: null },
      upstream_rates: null,
    }
    api.beichenOverview.mockResolvedValueOnce(data)
    const wrapper = await view()
    expect(wrapper.get<HTMLInputElement>('input[value="manual"]').element.checked).toBe(true)
    expect(wrapper.get<HTMLInputElement>('input[name="input"]').element.value).toBe('0.75')
    expect(wrapper.get<HTMLInputElement>('input[name="output"]').element.value).toBe('1.25')
    expect(wrapper.get<HTMLInputElement>('input[name="cache_read"]').element.value).toBe('0')
  })

  it('保存失败后可修正重试，提交期间禁止重复保存', async () => {
    let reject!: (cause: Error) => void
    api.setBeichenModelPrice.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = await view()
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
