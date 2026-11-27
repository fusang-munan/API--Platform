import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'
import type { BeichenWebsiteKeyInventory, Provider } from '../../api/types'
import BeichenWebsiteKeys from './BeichenWebsiteKeys.vue'

const api = vi.hoisted(() => ({ beichenWebsiteKeys: vi.fn(), refreshBeichenWebsiteKeys: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))
beforeEach(() => vi.resetAllMocks())

it('按官网状态说明未匹配的校内 Key，并保留清单各项', async () => {
  const provider: Provider = {
    id: 'beichen', name: '北辰', base_url: 'https://api.beichenapi.com',
    status: 'active', credential_configured: true, created_at: '', updated_at: '',
  }
  const observed = '2026-11-21T02:30:00Z'
  const base = {
    key_prefix: null, group_id: 31, group_name: null, rate: null,
    created_at: null, last_seen: observed, credential_id: null,
  }
  api.refreshBeichenWebsiteKeys.mockResolvedValueOnce({
    status: 'current', last_seen: observed,
    keys: [
      { ...base, id: 1, name: '哈吉米', status: 'active', enabled: true, match_status: 'unmatched' },
      { ...base, id: 2, name: '另一把启用 Key', status: 'active', enabled: true, match_status: 'unmatched' },
      { ...base, id: 3, name: '已停用 Key', status: 'inactive', enabled: false, match_status: 'unmatched' },
      { ...base, id: 4, name: '匹配 Key', status: 'active', enabled: true, match_status: 'matched' },
      { ...base, id: 5, name: '待核对 Key', status: 'active', enabled: true, match_status: 'ambiguous' },
    ],
  } satisfies BeichenWebsiteKeyInventory)
  const wrapper = mount(BeichenWebsiteKeys, { props: { provider } })
  await wrapper.get('button').trigger('click')
  await flushPromises()
  const rows = wrapper.findAll('tbody tr')
  expect(rows[0].text()).toContain('哈吉米')
  expect(rows[0].text()).toContain('#31')
  expect(rows.map(row => row.findAll('td').at(-1)?.text())).toEqual([
    '未匹配校内 Key（官网仍启用）', '未匹配校内 Key（官网仍启用）', '未匹配校内 Key', '已匹配', '匹配不唯一',
  ])
  wrapper.unmount()
})

it('shows the Key group read from the website and retains it after a session expires', async () => {
  const provider: Provider = {
    id: 'beichen', name: '北辰', base_url: 'https://api.beichenapi.com',
    status: 'active', credential_configured: true, created_at: '', updated_at: '',
  }
  const data: BeichenWebsiteKeyInventory = {
    status: 'current', last_seen: '2026-11-21T02:30:00Z',
    keys: [{
      id: 1001, name: 'cc不满血', key_prefix: 'sk-cc2a', group_id: 62, group_name: '[CC2]ClaudeCode高智',
      rate: '0.6', status: 'active', enabled: true, created_at: null, last_seen: '2026-11-21T02:30:00Z',
      credential_id: 'cc-school', match_status: 'matched',
    }],
  }
  api.beichenWebsiteKeys.mockResolvedValue(data)
  api.refreshBeichenWebsiteKeys.mockResolvedValue({ ...data, status: 'session_expired' })
  const wrapper = mount(BeichenWebsiteKeys, { props: { provider } })
  const details = wrapper.get('details')
  details.element.open = true
  await vi.waitFor(() => expect(api.beichenWebsiteKeys).toHaveBeenCalledWith('beichen'))
  await flushPromises()
  expect(api.refreshBeichenWebsiteKeys).not.toHaveBeenCalled()
  expect(wrapper.get('tbody').text()).toContain('cc不满血')
  expect(wrapper.get('tbody').text()).toContain('[CC2]ClaudeCode高智')
  expect(wrapper.get('tbody').text()).toContain('0.6x')
  expect(wrapper.get('tbody').text()).not.toContain('0.15')
  await wrapper.get('button').trigger('click')
  await flushPromises()
  expect(api.refreshBeichenWebsiteKeys).toHaveBeenCalledWith('beichen')
  expect(wrapper.get('[role="status"]').text()).toContain('会话已过期')
  expect(wrapper.get('tbody').text()).toContain('0.6x')
  wrapper.unmount()
})
