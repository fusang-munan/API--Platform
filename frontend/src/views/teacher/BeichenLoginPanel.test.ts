import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { APIError } from '../../api/client'
import type { BeichenAccount, Provider } from '../../api/types'
import BeichenLoginPanel from './BeichenLoginPanel.vue'

const api = vi.hoisted(() => ({
  beichenAccount: vi.fn(),
  loginBeichenAccount: vi.fn(),
  verifyBeichenAccount: vi.fn(),
  clearBeichenAccount: vi.fn(),
  crawlBeichen: vi.fn(),
}))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))

const Dialog = defineComponent({
  props: { open: Boolean, title: String },
  template: '<section><slot name="trigger" /><div v-if="open" role="dialog" :aria-label="title"><slot /></div></section>',
})
enableAutoUnmount(afterEach)

function provider(): Provider {
  return {
    id: 'north', name: '北辰', base_url: 'https://api.beichenapi.com', status: 'active',
    credential_configured: true, created_at: '', updated_at: '',
  }
}

function account(overrides: Partial<BeichenAccount> = {}): BeichenAccount {
  return {
    configured: true,
    mode: 'password',
    email: 'teacher@school.edu',
    status: 'active',
    expires_at: '2026-12-01T00:00:00Z',
    last_verified_at: '2026-11-21T02:00:00Z',
    last_error: null,
    crawl: {
      last_run_at: '2026-11-21T01:00:00Z',
      platforms: 3,
      models: 87,
      failures: 0,
    },
    ...overrides,
  }
}

function view() {
  return mount(BeichenLoginPanel, {
    props: { provider: provider() },
    attachTo: document.body,
    global: { stubs: { AppDialog: Dialog } },
  })
}

async function opened() {
  const wrapper = view()
  await wrapper.get('button').trigger('click')
  await flushPromises()
  return wrapper
}

function button(wrapper: ReturnType<typeof view>, label: string) {
  return wrapper.findAll('button').find(element => element.text() === label)!
}

beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('北辰网页登录', () => {
  it.each([
    ['none', false, '未配置', '还没有保存北辰网页登录凭证'],
    ['active', true, '正常', '抓取会自动使用这份凭证'],
    ['expired', true, '已过期', '网页会话已过期'],
    ['error', true, '错误', '最近一次登录或验证失败'],
  ] as const)('显示 %s 状态及抓取摘要', async (status, configured, label, hint) => {
    const value = account({
      status,
      configured,
      email: configured ? 'teacher@school.edu' : '',
      last_error: status === 'error' ? '北辰返回：登录状态已失效' : null,
    })
    if (!configured) value.crawl = { last_run_at: null, platforms: null, models: null, failures: null }
    api.beichenAccount.mockResolvedValue(value)

    const wrapper = await opened()
    const panel = wrapper.get('[aria-label="网页登录状态"]')
    expect(panel.get('.status-pill').text()).toBe(label)
    expect(panel.text()).toContain(hint)
    expect(panel.text()).toContain(configured ? 'teacher@school.edu' : '—')
    if (status === 'error') expect(panel.text()).toContain('登录状态已失效')

    const crawlPanel = wrapper.get('[aria-label="最近抓取"]')
    if (configured) {
      expect(crawlPanel.text()).toContain('平台 3 个')
      expect(crawlPanel.text()).toContain('模型 87 个')
      expect(crawlPanel.text()).toContain('失败 0 条')
    } else {
      expect(crawlPanel.text()).toContain('还没有抓取记录')
    }

    const disabled = configured ? undefined : ''
    expect(button(wrapper, '立即验证').attributes('disabled')).toBe(disabled)
    expect(button(wrapper, '测试抓取').attributes('disabled')).toBe(disabled)
    expect(button(wrapper, '清除凭证').attributes('disabled')).toBe(disabled)
  })

  it('账号密码提交只发送一次密码，提交后清空输入框', async () => {
    api.beichenAccount.mockResolvedValue(account({ configured: false, status: 'none', email: '' }))
    api.loginBeichenAccount.mockResolvedValue(account())
    const wrapper = await opened()
    const form = wrapper.get('form')
    const email = form.get('input[type="email"]')
    const password = form.get('input[name="password"]')
    await email.setValue(' teacher@school.edu ')
    await password.setValue('super-secret-password')

    await form.trigger('submit')
    await flushPromises()

    expect(api.loginBeichenAccount).toHaveBeenCalledExactlyOnceWith('north', {
      mode: 'password', email: 'teacher@school.edu', password: 'super-secret-password',
    })
    expect((form.get('input[name="password"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('super-secret-password')
    expect(wrapper.get('[aria-label="网页登录状态"]').text()).toContain('正常')
    expect(wrapper.get('[role="status"]').text()).toContain('已保存，服务器爬虫可直接使用')
  })

  it('粘贴 Token 模式只提交会话，不在页面回显', async () => {
    api.beichenAccount.mockResolvedValue(account())
    api.loginBeichenAccount.mockResolvedValue(account({ mode: 'token', email: 'teacher@school.edu' }))
    const wrapper = await opened()
    const radios = wrapper.findAll('input[type="radio"]')
    await radios[1].setValue()
    const token = wrapper.get('input[name="token"]')
    await token.setValue('session-secret-token')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(api.loginBeichenAccount).toHaveBeenCalledExactlyOnceWith('north', {
      mode: 'token', token: 'session-secret-token',
    })
    expect((wrapper.get('input[name="token"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('session-secret-token')
  })

  it('登录一次即可测试抓取并显示平台、模型和失败详情', async () => {
    api.beichenAccount.mockResolvedValue(account({ configured: false, status: 'none', email: '' }))
    api.loginBeichenAccount.mockResolvedValue(account())
    api.crawlBeichen.mockResolvedValue({
      last_run_at: '2026-11-21T03:00:00Z',
      platforms: 2,
      models: 120,
      failures: 1,
      failure_details: [{ platform: 'Codex', path: 'data[0].platforms[1]', reason: '缺少 models 数组' }],
    })
    const wrapper = await opened()
    const form = wrapper.get('form')
    await form.get('input[type="email"]').setValue('teacher@school.edu')
    await form.get('input[name="password"]').setValue('test-only-password')
    await form.trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('已保存，服务器爬虫可直接使用')

    await button(wrapper, '测试抓取').trigger('click')
    await flushPromises()
    expect(api.crawlBeichen).toHaveBeenCalledExactlyOnceWith('north')
    const summary = wrapper.get('[aria-label="最近抓取"]').text()
    expect(summary).toContain('平台 2 个')
    expect(summary).toContain('模型 120 个')
    expect(summary).toContain('失败 1 条')
    expect(summary).toContain('data[0].platforms[1]')
    expect(summary).toContain('缺少 models 数组')
  })

  it('立即验证显示过期原因', async () => {
    api.beichenAccount.mockResolvedValue(account())
    api.verifyBeichenAccount.mockResolvedValue(account({
      status: 'expired', last_error: '北辰返回：网页会话已经过期',
    }))
    const wrapper = await opened()

    await button(wrapper, '立即验证').trigger('click')
    await flushPromises()
    expect(api.verifyBeichenAccount).toHaveBeenCalledWith('north')
    expect(wrapper.findAll('[role="alert"]').map(item => item.text()).join(' ')).toContain('网页会话已经过期')
  })

  it('全部抓取失败时重新读取本轮失败摘要', async () => {
    api.beichenAccount.mockResolvedValueOnce(account()).mockResolvedValueOnce(account({
      status: 'expired',
      crawl: {
        last_run_at: '2026-11-21T03:00:00Z', platforms: 0, models: 0, failures: 1,
        failure_details: [{ path: 'response', reason: '北辰网页 Token 已过期' }],
      },
    }))
    api.crawlBeichen.mockRejectedValue(new APIError('北辰网页 Token 已过期', 503, 'DEPENDENCY_UNAVAILABLE'))
    const wrapper = await opened()

    await button(wrapper, '测试抓取').trigger('click')
    await flushPromises()

    const summary = wrapper.get('[aria-label="最近抓取"]').text()
    expect(summary).toContain('平台 0 个')
    expect(summary).toContain('模型 0 个')
    expect(summary).toContain('失败 1 条')
    expect(summary).toContain('北辰网页 Token 已过期')
  })

  it('清除凭证前确认，成功后回到未配置状态', async () => {
    api.beichenAccount.mockResolvedValue(account())
    api.clearBeichenAccount.mockResolvedValue(null)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = await opened()

    await button(wrapper, '清除凭证').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('清除已保存的北辰网页登录凭证'))
    expect(api.clearBeichenAccount).toHaveBeenCalledWith('north')
    expect(wrapper.get('[aria-label="网页登录状态"] .status-pill').text()).toBe('未配置')
    expect(wrapper.get('[role="status"]').text()).toContain('凭证已清除')
  })

  it('登录失败显示后端中文原因，且不保留密码', async () => {
    api.beichenAccount.mockResolvedValue(account({ configured: false, status: 'none', email: '' }))
    api.loginBeichenAccount.mockRejectedValue(new APIError('北辰账号或密码不正确', 400, 'VALIDATION_ERROR'))
    const wrapper = await opened()
    const form = wrapper.get('form')
    await form.get('input[type="email"]').setValue('teacher@school.edu')
    await form.get('input[name="password"]').setValue('wrong-password')

    await form.trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('北辰账号或密码不正确')
    expect((form.get('input[name="password"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('wrong-password')
  })
})
