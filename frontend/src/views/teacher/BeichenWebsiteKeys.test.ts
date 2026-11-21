import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import type { BeichenWebsiteKeyInventory, Provider } from '../../api/types'
import BeichenWebsiteKeys from './BeichenWebsiteKeys.vue'

const api = vi.hoisted(() => ({ beichenWebsiteKeys: vi.fn(), refreshBeichenWebsiteKeys: vi.fn() }))
vi.mock('../../api/teacher', () => ({ teacherAPI: api }))

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
