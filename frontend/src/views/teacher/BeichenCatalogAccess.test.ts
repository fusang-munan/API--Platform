// @vitest-environment jsdom
// @vitest-environment-options {"url":"https://school.example/"}
import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import { teacherAPI } from '../../api/teacher'
import BeichenCatalogAccess from './BeichenCatalogAccess.vue'

it('在 HTTPS 控制台绑定数字 Key ID，保存后清空面板令牌', async () => {
  vi.spyOn(teacherAPI, 'beichenPanelStatus').mockResolvedValue({ configured: true, group_id: 2, upstream_key_id: 17 })
  const update = vi.spyOn(teacherAPI, 'setBeichenPanelAccess')
    .mockResolvedValueOnce({ configured: true, group_id: 2, upstream_key_id: 19 })
    .mockResolvedValueOnce({ configured: false })
  const view = mount(BeichenCatalogAccess, { props: {
    providerId: 'provider', credentials: [{ id: 'channel', provider_id: 'provider', label: 'G3',
      status: 'active', credential_configured: true, created_at: '', updated_at: '' }],
  } })
  try {
    await view.get('select').setValue('channel')
    await flushPromises()
    const keyID = view.findAll<HTMLInputElement>('input[type="number"]')[1]
    expect(keyID.element.value).toBe('17')
    await keyID.setValue(19)
    const token = view.get<HTMLInputElement>('input[type="password"]')
    await token.setValue('local-fixture')
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledWith('provider', 'channel', {
      enabled: true, group_id: 2, upstream_key_id: 19, token: 'local-fixture',
    })
    expect(token.element.value).toBe('')
    await view.get('button[type="button"]').trigger('click')
    await flushPromises()
    expect(update).toHaveBeenLastCalledWith('provider', 'channel', { enabled: false })
    expect(keyID.element.value).toBe('')
  } finally {
    view.unmount()
    vi.restoreAllMocks()
  }
})
