import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BeichenComparison, BeichenOverview, Provider } from '../../api/types'
import TeacherProviders from './TeacherProviders.vue'

const provider: Provider = {
  id: 'beichen',
  name: '北辰测试',
  base_url: 'https://api.beichenapi.com/v1',
  credential_configured: true,
  status: 'active',
  credentials: [
    { id: 'key-1', provider_id: 'beichen', label: '测试凭据', credential_configured: true, status: 'active', created_at: '', updated_at: '' },
    { id: 'key-zero', provider_id: 'beichen', label: '零倍率凭据', credential_configured: true, status: 'active', created_at: '', updated_at: '' },
    { id: 'empty', provider_id: 'beichen', label: '待配置凭据', credential_configured: false, status: 'inactive', created_at: '', updated_at: '' },
  ],
  created_at: '',
  updated_at: '',
}
const comparisonRow: BeichenComparison = {
  credential_id: 'key-1', model: 'test-model', upstream_usage_source: 'usage',
  local_requests: 1, upstream_requests: 0,
  local_input_tokens: 5, upstream_input_tokens: null,
  local_output_tokens: 2, upstream_output_tokens: 3,
  local_cache_read_tokens: 0, upstream_cache_read_tokens: 0,
  local_cache_write_tokens: 0, upstream_cache_write_tokens: 0,
  estimated_upstream_micros: 10, billed_upstream_micros: 0, trial_credit_milli: 3, trial_unpriced_calls: 1,
}
const overview: BeichenOverview = {
  datasets: [
    { credential_id: 'key-1', kind: 'catalog', last_success: '2026-11-17T03:20:00Z', effective_multiplier: null },
    { credential_id: 'key-1', kind: 'billing', last_error: '暂时无法读取倍率', effective_multiplier: 0.125 },
    { credential_id: 'key-zero', kind: 'billing', effective_multiplier: 0 },
    { credential_id: 'empty', kind: 'billing', effective_multiplier: null },
  ],
  models: [{
    credential_id: 'key-1', model: 'test-model', mode: 'follow',
    upstream_rates: { input: 1_200_000, output: 0 },
    school_rates: { input: 100_000, output: 0, cache_read: 1, cache_write: 0 },
  }],
  comparison: [
    comparisonRow,
    { ...comparisonRow, model: 'manual-model', upstream_usage_source: 'csv' },
    {
      ...comparisonRow, model: 'export-model', upstream_usage_source: 'export',
      trial_credits: '0.00000043', charged_credits: '0.000000123456789', pending_review_calls: 2,
    },
    {
      ...comparisonRow, model: 'local-only', upstream_usage_source: null, billed_upstream_micros: null,
      trial_credits: null, charged_credits: null, pending_review_calls: 0,
      upstream_requests: null, upstream_output_tokens: null, upstream_cache_read_tokens: null, upstream_cache_write_tokens: null,
    },
  ],
}
const path = '/api/v1/teacher/providers/beichen/beichen'
const usageExportCsv = [
  'Time,API Key Name,Model,Reasoning Effort,Inbound Endpoint,IP Address,Type,Billing Mode,Input Tokens,Output Tokens,Cache Read Tokens,Cache Creation Tokens,Rate Multiplier,Billed Cost,Original Cost,First Token (ms),Duration (ms)',
  '2026-11-16T12:00:00+08:00,测试凭据,test-model,low,/v1/responses,192.0.2.7,API,Token,5,2,0,0,1,0.00000120,0.00000120,45,120',
].join('\r\n')
const reply = (data: unknown) => new Response(JSON.stringify({ data }))
const request = vi.fn<(input: string, options?: RequestInit) => Promise<Response>>()
let widget: VueWrapper | undefined
let providers: Provider[]

function button(root: Pick<DOMWrapper<Element>, 'findAll'>, text: string) {
  const result = root.findAll('button').find(item => item.text() === text)
  if (!result) throw new Error(`Missing button: ${text}`)
  return result
}

function section(root: DOMWrapper<Element>, text: string) {
  const result = root.findAll('details').find(item => item.get('summary').text() === text)
  if (!result) throw new Error(`Missing section: ${text}`)
  result.element.open = true
  return result
}

async function openPanel() {
  widget = mount(TeacherProviders, { attachTo: document.body })
  await flushPromises()
  const root = new DOMWrapper(widget.element)
  const trigger = button(root, '同步与对账')
  await trigger.trigger('click')
  await flushPromises()
  const dialog = document.body.querySelector('[role="dialog"]')
  if (!dialog) throw new Error('Beichen dialog did not open')
  return new DOMWrapper(dialog)
}

function sent(method: string, suffix: string) {
  return request.mock.calls.filter(([url, options]) => url === path + suffix && options?.method === method)
}

function selectFile(input: Element, csv: string) {
  const bytes = new TextEncoder().encode(csv)
  Object.defineProperty(input, 'files', {
    configurable: true,
    value: [{ size: bytes.byteLength, arrayBuffer: async () => bytes.buffer }],
  })
}

describe('北辰教师面板', () => {
  beforeEach(() => {
    request.mockReset()
    providers = [provider]
    request.mockImplementation(async (url, options) => {
      if (url === '/api/v1/teacher/providers') return reply(providers)
      if (url.startsWith(`${path}?day=`)) return reply(overview)
      if (url === path + '/sync' && options?.method === 'POST') return reply({ synced: true })
      if (url === path + '/price' && options?.method === 'PATCH') return reply({ saved: true })
      if (url === path + '/import' && options?.method === 'POST') return reply({ imported: true })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', request)
  })

  afterEach(() => {
    widget?.unmount()
    widget = undefined
    vi.unstubAllGlobals()
  })

  it('选择北辰预设渠道时复用历史标签，未保存的选择不创建凭据', async () => {
    providers = [{ ...provider, credentials: [{ ...provider.credentials![0], label: '国模' }] }]
    widget = mount(TeacherProviders, { attachTo: document.body })
    await flushPromises()
    await button(new DOMWrapper(widget.element), '编辑').trigger('click')
    const dialog = new DOMWrapper(document.body.querySelector('[role="dialog"]')!)
    const presets = dialog.get('[aria-label="北辰渠道"]')
    expect(presets.findAll('button').map(item => item.text())).toEqual(['Gemini', 'GM1', 'G3', 'CC1'])
    await button(presets, 'G3').trigger('click')
    await button(presets, 'GM1').trigger('click')
    expect(dialog.get('[role="tab"][aria-selected="true"]').text()).toBe('国模')
    await button(presets, 'G3').trigger('click')
    expect(dialog.findAll('[role="tab"]').map(item => item.text())).toEqual(['国模', 'G3'])
    expect(request.mock.calls.every(([, options]) => !options?.method || options.method === 'GET')).toBe(true)
  })

  it('同步入口只接受北辰的 HTTPS 根地址或 /v1，且 URL 没有端口和附加部分', async () => {
    const supported = [
      'https://api.beichenapi.com',
      'https://api.beichenapi.com/',
      'https://api.beichenapi.com/v1',
      'https://API.BEICHENAPI.COM/v1/',
    ]
    const unsupported = [
      'http://api.beichenapi.com',
      'https://api.beichenapi.com.example.test',
      'https://api.beichenapi.com:443/v1',
      'https://api.beichenapi.com:8443',
      'https://api.beichenapi.com/v2',
      'https://api.beichenapi.com/V1',
      'https://api.beichenapi.com/x/../v1',
      'https://api.beichenapi.com/v1?foo=1',
      'https://api.beichenapi.com/#here',
    ]
    providers = [...supported, ...unsupported].map((base_url, i) => ({
      ...provider, id: String(i), name: base_url, base_url, credentials: [], credential_configured: false,
    }))
    widget = mount(TeacherProviders)
    await flushPromises()
    const rows = widget.findAll('article')
    const rowsWithButton = rows.filter(row => row.findAll('button').some(item => item.text() === '同步与对账'))
    expect(rowsWithButton.map(row => row.get('strong').text())).toEqual(supported)
    expect(rows.filter(row => row.text().includes('待配置 Key')).map(row => row.get('strong').text())).toEqual(supported)
  })

  it('北辰零 Key 时显示待配置提示，保留停用状态和对账入口', async () => {
    providers = [{ ...provider, credentials: [], credential_configured: false, status: 'inactive' }]
    request.mockImplementation(async url => {
      if (url === '/api/v1/teacher/providers') return reply(providers)
      if (url.startsWith(`${path}?day=`)) return reply({ datasets: [], models: [], comparison: [] })
      throw new Error(`Unexpected request: ${url}`)
    })

    const dialog = await openPanel()
    const row = widget!.get('article')
    expect(row.text()).toContain('0 个启用 Key / 0 个 Key')
    expect(row.text()).toContain('待配置 Key')
    expect(row.get('.status-badge').classes()).toContain('inactive')
    expect(dialog.text()).toContain('暂无同步记录')
  })

  it('展示同步状态，手动同步并按所选日期展示对账结果', async () => {
    const dialog = await openPanel()
    expect(widget!.get('article').text()).not.toContain('待配置 Key')
    expect(dialog.text()).toContain('模型目录')
    expect(dialog.text()).toContain('计费倍率')
    expect(dialog.text()).toContain('暂时无法读取倍率')
    const multipliers = dialog.findAll('.beichen-multiplier')
    expect(multipliers.map(value => value.text())).toEqual(['0.125×', '0×', '—'])

    const compare = section(dialog, '按日对账')
    await compare.get('input[type="date"]').setValue('2026-11-16')
    await compare.get('form').trigger('submit')
    await flushPromises()
    expect(request).toHaveBeenCalledWith(path + '?day=2026-11-16', expect.anything())
    expect(compare.text()).toContain('账期 2026-11-16')
    expect(compare.text()).toContain('估算 0.00001 单位')
    expect(compare.text()).toContain('账单 0 单位')
    expect(compare.text()).toContain('0.003 credit')
    expect(compare.text()).toContain('北辰 —')
    const rows = compare.findAll('tbody tr')
    expect(rows[2].get('td:nth-last-child(2)').text()).toContain('0.00000043 credit')
    expect(rows[2].get('td:last-child').text()).toContain('0.000000123456789 credit')
    expect(rows[2].get('td:last-child').text()).toContain('待核账 2 条')
    expect(rows[3].get('td:nth-last-child(2)').text()).toContain('—')
    expect(rows[3].get('td:last-child').text()).toContain('待核账 0 条')
    const billCells = rows.map(row => row.get('td:nth-last-child(3)'))
    expect(billCells.slice(0, 3).map(cell => cell.get('small').text())).toEqual(['自动同步', '手工汇总', '导入明细（待核对）'])
    expect(billCells[3].text()).toContain('账单 —')
    expect(billCells[3].find('small').exists()).toBe(false)

    await button(dialog, '手动同步').trigger('click')
    await flushPromises()
    expect(sent('POST', '/sync')).toHaveLength(1)
    expect(sent('POST', '/sync')[0][1]?.body).toBeUndefined()
    expect(dialog.text()).toContain('同步已结束')
    expect(request.mock.lastCall?.[0]).toBe(path + '?day=2026-11-16')

    await dialog.trigger('keydown', { key: 'Escape' })
    await vi.waitFor(() => {
      expect(document.body.querySelector('[role="dialog"]')).toBeNull()
      expect(document.activeElement?.textContent).toBe('同步与对账')
    })
  })

  it('保留手填零值和六位小数，恢复跟随时省略 rates', async () => {
    const dialog = await openPanel()
    const prices = section(dialog, '模型价格')
    expect(prices.text()).toContain('1.2 单位')
    expect(prices.text()).toContain('0.000001 credit')
    expect(prices.text()).toContain('上游 —')
    await button(prices, '编辑价格').trigger('click')
    const editor = prices.get('form')
    const fields = editor.findAll<HTMLInputElement>('input')
    expect(fields.map(field => field.element.value)).toEqual(['0.1', '0', '0.000001', '0'])
    await fields[0].setValue('0.234567')
    await fields[3].setValue('')
    await editor.trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(sent('PATCH', '/price')[0][1]?.body))).toEqual({
      credential_id: 'key-1', model: 'test-model', mode: 'manual',
      rates: { input: 234567, output: 0, cache_read: 1 },
    })

    await button(prices, '编辑价格').trigger('click')
    await prices.get('select').setValue('follow')
    await prices.get('form').trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(sent('PATCH', '/price')[1][1]?.body))).toEqual({
      credential_id: 'key-1', model: 'test-model', mode: 'follow',
    })
  })

  it('核对 CSV 表头并向选中的凭据导入最终价、基础价和用量', async () => {
    const dialog = await openPanel()
    const importer = section(dialog, '导入官方 CSV')
    const [credential, kind] = importer.findAll('select')
    expect(credential.text()).toContain('测试凭据')
    expect(credential.text()).not.toContain('待配置凭据')
    await credential.setValue('key-1')
    const fileInput = importer.get('input[type="file"]').element

    const prices = 'model,input,output,cache_read,cache_write\r\ntest-model,100,200,0,0\r\n'
    await kind.setValue('usage')
    selectFile(fileInput, prices)
    await importer.get('form').trigger('submit')
    await flushPromises()
    expect(importer.text()).toContain('CSV 表头与所选类型不符')
    expect(sent('POST', '/import')).toHaveLength(0)

    await kind.setValue('prices')
    selectFile(fileInput, '\uFEFF' + prices)
    await importer.get('form').trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(sent('POST', '/import')[0][1]?.body))).toEqual({
      credential_id: 'key-1', kind: 'prices', csv: prices,
    })

    await kind.setValue('prices_base')
    expect(importer.text()).toContain('乘一次该渠道的实际倍率')
    expect(importer.text()).toContain('当前凭据实际倍率 0.125×')
    await credential.setValue('key-zero')
    expect(importer.text()).toContain('当前凭据实际倍率 0×')
    await credential.setValue('key-1')
    selectFile(fileInput, prices)
    await importer.get('form').trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(sent('POST', '/import')[1][1]?.body))).toEqual({
      credential_id: 'key-1', kind: 'prices_base', csv: prices,
    })

    const usage = 'date,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros\n2026-11-16,test-model,1,5,2,0,0,0'
    await kind.setValue('usage')
    expect(importer.find('input[type="checkbox"]').exists()).toBe(false)
    selectFile(fileInput, usage)
    await importer.get('form').trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(sent('POST', '/import')[2][1]?.body))).toEqual({
      credential_id: 'key-1', kind: 'usage', csv: usage,
    })
  })

  it('北辰用量明细需要确认，校验官方表头并发送 usage_export', async () => {
    const importer = section(await openPanel(), '导入官方 CSV')
    const [credential, kind] = importer.findAll('select')
    expect(kind.text()).toContain('用量（日汇总）')
    expect(kind.text()).toContain('北辰用量明细')
    await credential.setValue('key-1')
    await kind.setValue('usage_export')
    expect(importer.text()).toContain('已经结束的完整北京时间账日')
    expect(importer.text()).toContain('整日以自动日账为准')
    expect(importer.text()).toContain('已有手工日汇总也优先')
    expect(importer.text()).toContain('后续同步到的自动日账可替代补账')
    const form = importer.get('form')
    const file = importer.get('input[type="file"]')
    const confirm = importer.get('input[type="checkbox"]')
    const submit = button(importer, '导入 CSV')

    selectFile(file.element, usageExportCsv.replace('Time,', 'Timestamp,'))
    expect(submit.attributes('disabled')).toBeDefined()
    await form.trigger('submit')
    expect(sent('POST', '/import')).toHaveLength(0)

    await confirm.setValue(true)
    await form.trigger('submit')
    await flushPromises()
    expect(importer.text()).toContain('CSV 表头与所选类型不符')
    expect(sent('POST', '/import')).toHaveLength(0)

    selectFile(file.element, '\uFEFF' + usageExportCsv)
    await file.trigger('change')
    expect(submit.attributes('disabled')).toBeDefined()
    await confirm.setValue(true)
    await credential.setValue('key-zero')
    expect(submit.attributes('disabled')).toBeDefined()

    await credential.setValue('key-1')
    await confirm.setValue(true)
    await form.trigger('submit')
    await flushPromises()
    expect(sent('POST', '/import')).toHaveLength(1)
    expect(JSON.parse(String(sent('POST', '/import')[0][1]?.body))).toEqual({
      credential_id: 'key-1', kind: 'usage_export', csv: usageExportCsv, full_day_confirmed: true,
    })
    expect(importer.text()).toContain('明细已处理，请查看按日对账')
    expect(submit.attributes('disabled')).toBeDefined()
  })

  it('官方明细校验失败时不展示服务端返回的逐笔数据', async () => {
    const importer = section(await openPanel(), '导入官方 CSV')
    const [credential, kind] = importer.findAll('select')
    await credential.setValue('key-1')
    await kind.setValue('usage_export')
    selectFile(importer.get('input[type="file"]').element, usageExportCsv)
    await importer.get('input[type="checkbox"]').setValue(true)
    request.mockResolvedValueOnce(new Response(JSON.stringify({
      error: { code: 'VALIDATION_ERROR', message: '第 1 行包含虚构地址 192.0.2.7' },
    }), { status: 400 }))

    await importer.get('form').trigger('submit')
    await flushPromises()
    expect(importer.get('[role="alert"]').text()).toContain('北辰用量明细未通过校验')
    expect(importer.text()).not.toContain('192.0.2.7')
  })
})
