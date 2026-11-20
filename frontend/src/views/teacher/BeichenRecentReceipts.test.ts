import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { BeichenReceiptItem, BeichenReceiptPage, BeichenWebsiteSync } from '../../api/types'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'

enableAutoUnmount(afterEach)
const fetchPage = vi.fn<(url: string, init: RequestInit) => Promise<Response>>()
const record: BeichenReceiptItem = {
  occurred_at: '2026-11-19T16:01:02Z', model: '示例模型', student_name: '学生甲',
  base_credits: '9007199254740993.00100', school_multiplier: '1.100',
  charged_credits: '88.00001', actual_cost: '0.00000000000000001900',
  difference_credits: '-0.00000000000000000100', matched: true,
}
const sync: BeichenWebsiteSync = {
  channels: 4, priced_channels: 3, price_last_seen: '2026-11-19T15:59:58Z',
  receipt_checked_channels: 2, receipt_last_checked: '2026-11-19T17:08:09Z', receipt_caught_up_channels: 0,
}

function reply(items: BeichenReceiptItem[], next_cursor: string | null = null) {
  const page: BeichenReceiptPage = { items, next_cursor }
  return new Response(JSON.stringify({ data: page }))
}

function pendingResponse() {
  let finish!: (value: Response) => void
  const promise = new Promise<Response>(resolve => { finish = resolve })
  return { promise, finish }
}

function mountView(websiteSync?: BeichenWebsiteSync) {
  return mount(BeichenRecentReceipts, { props: { providerId: 'school/test', websiteSync } })
}

function button(view: ReturnType<typeof mountView>, label: string) {
  const target = view.findAll('button').find(node => node.text() === label)
  if (!target) throw Error('未找到按钮 ' + label)
  return target
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-11-19T17:30:00Z'))
  fetchPage.mockReset()
  vi.stubGlobal('fetch', fetchPage)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it('按北京日期读取教师流水，显示实际覆盖数和最近一次采集时间，保留金额精度、符号和未知值', async () => {
  fetchPage.mockResolvedValueOnce(reply([
    record,
    { ...record, student_name: null, base_credits: null, school_multiplier: null, charged_credits: null,
      actual_cost: null, difference_credits: null, matched: true },
    { ...record, student_name: null, charged_credits: null, actual_cost: '0.0000', difference_credits: null, matched: false },
    { ...record, charged_credits: '0.0010', actual_cost: '0', difference_credits: '+0.0010' },
    { ...record, actual_cost: '', difference_credits: ' ' },
  ]))
  const view = mountView(sync)
  expect(fetchPage).not.toHaveBeenCalled()
  await button(view, '近期逐笔对账').trigger('click')
  await flushPromises()
  expect(fetchPage.mock.calls.map(([url, init]) => [init.method ?? 'GET', url])).toEqual([
    ['GET', '/api/v1/teacher/providers/school%2Ftest/beichen/receipts?day=2026-11-20'],
  ])
  expect(view.get('input').element.value).toBe('2026-11-20')
  expect(view.findAll('dl > div').map(field => [field.get('dt').text(), field.get('dd').text()])).toEqual([
    ['当前有有效网页报价', '3 / 4 个渠道'], ['成功读取过流水页', '2 / 4 个渠道'], ['最近一次扫描已到末页', '0 / 4 个渠道'],
    ['最近记录报价时间', '2026-11-19 23:59:58'], ['最近一页流水读取成功时间', '2026-11-20 01:08:09'],
  ])
  const [matched, missing, unmatched, zero, blank] = view.findAll('tbody tr')
  expect(matched.get('time').text()).toBe('2026-11-20 00:01:02')
  expect(matched.get('th').text()).toContain('示例模型学生 学生甲')
  expect(matched.text()).toContain('已扣积分 88.00001')
  expect(matched.text()).toContain('基础积分 9007199254740993.00100')
  expect(matched.text()).toContain('学校倍率 1.100')
  const amounts = (row: typeof matched) => row.findAll('td').slice(2).map(cell => cell.text())
  expect(amounts(matched)).toEqual([record.actual_cost, record.difference_credits, '已匹配'])
  expect(missing.text()).toContain('学生 待核')
  expect(missing.text()).toContain('已扣积分 待核')
  expect(amounts(missing)).toEqual(['待核', '待核', '待核'])
  expect(amounts(unmatched)).toEqual(['0.0000', '待核', '待核'])
  expect(amounts(zero)).toEqual(['0', '+0.0010', '已匹配'])
  expect(amounts(blank)).toEqual(['待核', '待核', '待核'])
})

it('按原游标追加同一时刻的不同流水，刷新回到当日第一页', async () => {
  const cursor = 'next+/=&part'
  fetchPage.mockResolvedValueOnce(reply([record], cursor))
    .mockResolvedValueOnce(reply([{ ...record, student_name: '学生乙' }]))
    .mockResolvedValueOnce(reply([{ ...record, student_name: '学生丙' }]))
  const view = mountView({ ...sync, price_last_seen: null, receipt_last_checked: null })
  await button(view, '近期逐笔对账').trigger('click')
  await flushPromises()
  expect(view.findAll('dl dd').slice(-2).map(node => node.text())).toEqual(['未知', '未知'])
  await button(view, '加载更多').trigger('click')
  await flushPromises()
  expect(view.findAll('tbody th').map(node => node.text())).toEqual(['示例模型学生 学生甲', '示例模型学生 学生乙'])
  expect(view.text()).not.toContain('加载更多')
  await button(view, '刷新对账').trigger('click')
  await flushPromises()
  const urls = fetchPage.mock.calls.map(([url]) => new URL(url, 'https://school.test'))
  expect(urls.map(url => url.searchParams.get('cursor'))).toEqual([null, cursor, null])
  expect(urls.every(url => url.pathname.endsWith('/beichen/receipts') && url.searchParams.get('day') === '2026-11-20')).toBe(true)
  expect(view.findAll('tbody th').map(node => node.text())).toEqual(['示例模型学生 学生丙'])
})

it('换日和折叠使在途响应失效，旧页或旧错误不会覆盖新日期', async () => {
  const more = pendingResponse()
  const newDay = pendingResponse()
  const lateRefresh = pendingResponse()
  fetchPage.mockResolvedValueOnce(reply([record], 'old-page')).mockReturnValueOnce(more.promise)
    .mockReturnValueOnce(newDay.promise).mockReturnValueOnce(lateRefresh.promise)
  const view = mountView()
  await button(view, '近期逐笔对账').trigger('click')
  await flushPromises()
  await button(view, '加载更多').trigger('click')
  expect(button(view, '加载更多').element.disabled).toBe(true)
  await view.get('input').setValue('2026-11-17')
  expect(fetchPage.mock.calls[1][1].signal?.aborted).toBe(true)
  expect(fetchPage.mock.calls[2][0]).toMatch(/\?day=2026-11-17$/)
  expect(view.find('tbody').exists()).toBe(false)
  more.finish(new Response(null, { status: 500 }))
  await flushPromises()
  expect(view.find('[role="alert"]').exists()).toBe(false)
  expect(view.get('[role="status"]').text()).toContain('正在读取')
  newDay.finish(reply([{ ...record, model: '新日期模型' }]))
  await flushPromises()
  expect(view.get('tbody').text()).toContain('新日期模型')
  await button(view, '刷新对账').trigger('click')
  await button(view, '近期逐笔对账').trigger('click')
  expect(fetchPage.mock.calls[3][1].signal?.aborted).toBe(true)
  lateRefresh.finish(reply([{ ...record, model: '过时流水' }]))
  await flushPromises()
  expect(view.text()).not.toContain('过时流水')
  fetchPage.mockResolvedValueOnce(reply([]))
  await button(view, '近期逐笔对账').trigger('click')
  await flushPromises()
  expect(view.get('input').element.value).toBe('2026-11-17')
  expect(view.text()).toContain('学校尚未归档所选日期的北辰流水')
  await view.get('input').setValue('')
  expect(fetchPage).toHaveBeenCalledTimes(5)
  expect(button(view, '刷新对账').element.disabled).toBe(true)
})

it('更换供应商后只接受新供应商的页面，即使旧请求更晚成功', async () => {
  const previous = pendingResponse()
  fetchPage.mockReturnValueOnce(previous.promise).mockResolvedValueOnce(reply([{ ...record, student_name: '新供应商学生' }]))
  const view = mountView()
  await button(view, '近期逐笔对账').trigger('click')
  await view.setProps({ providerId: 'other' })
  await flushPromises()
  expect(fetchPage.mock.calls[0][1].signal?.aborted).toBe(true)
  expect(fetchPage.mock.calls[1][0]).toContain('/teacher/providers/other/beichen/receipts?day=')
  previous.finish(reply([record], 'stale'))
  await flushPromises()
  expect(view.get('tbody').text()).toContain('新供应商学生')
  expect(view.text()).not.toMatch(/学生甲|加载更多/)
})

it('追加失败可用原游标重试，权限被拒时清除已展示的学生流水', async () => {
  fetchPage.mockResolvedValueOnce(reply([record], 'retry')).mockRejectedValueOnce(new Error('模拟离线'))
    .mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'FORBIDDEN' } }), { status: 403 }))
  const view = mountView()
  await button(view, '近期逐笔对账').trigger('click')
  await flushPromises()
  await button(view, '加载更多').trigger('click')
  await flushPromises()
  expect(view.get('[role="alert"]').text()).toContain('失败')
  expect(view.get('tbody').text()).toContain('学生甲')
  await button(view, '加载更多').trigger('click')
  await flushPromises()
  expect(fetchPage.mock.calls[1][0]).toBe(fetchPage.mock.calls[2][0])
  expect(view.get('[role="alert"]').text()).toContain('权限')
  expect(view.text()).not.toMatch(/学生甲|加载更多|尚未归档/)
  expect(fetchPage).toHaveBeenCalledTimes(3)
})
