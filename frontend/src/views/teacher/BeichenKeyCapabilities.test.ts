import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { expect, it, vi } from 'vitest'
import type { BeichenKeyCapability } from '../../api/types'
import BeichenKeyCapabilities from './BeichenKeyCapabilities.vue'

const refreshUsage = vi.hoisted(() => vi.fn())
vi.mock('../../api/teacher', () => ({ teacherAPI: { refreshBeichenKeyUsage: refreshUsage } }))

const Dialog = defineComponent({
  props: ['open', 'title'],
  emits: ['update:open'],
  template: '<div><div @click="$emit(\'update:open\', true)"><slot name="trigger" /></div><section v-if="open" role="dialog" :aria-label="title"><slot /></section></div>',
})
const at = '2026-11-21T06:00:00Z'
const cc2: BeichenKeyCapability = {
    credential_id: 'cc-channel', label: 'cc不满血', models: ['claude-opus-4-6', 'claude-sonnet-4-6'],
    website_key_id: 42, website_group: { id: 602, name: '[CC2]ClaudeCode高智', rate_multiplier: '0.6' },
    group_status: 'assigned',
    has_claude: true, models_state: 'fresh', models_fetched_at: at, models_error: null,
    usage_state: 'fresh', usage_fetched_at: at, usage_error: null, balance: '4.974', unit: 'USD',
    day_unit: 'USD', day_fetched_at: at, model_stats: [], model_stats_day: null, model_stats_fetched_at: null,
    daily_usage: {
      date: '2026-11-21', requests: 2, total_tokens: null, input_tokens: 1000, output_tokens: 62,
      cache_read_tokens: null, cache_write_tokens: 0, cost: '0.11', actual_cost: '0.066',
      account_cost: null, observed_multiplier: '0.6', multiplier_diff: false,
    },
    stored_multiplier: '0.6', target_multiplier: '0.6', expect_claude: true, group_check: 'target_observed',
}

function mountKeys(capabilities: BeichenKeyCapability[]) {
  return mount(BeichenKeyCapabilities, {
    props: { providerId: 'provider', capabilities },
    global: { stubs: { AppDialog: Dialog } },
  })
}

it('展示 CC2 的官网分组和单 Key 流水，在弹层按倍率核对选中渠道', async () => {
  refreshUsage.mockResolvedValueOnce({ synced: true })
  const view = mountKeys([cc2])

  expect(view.get('section').attributes('aria-label')).toBe('Key 实际能力')
  expect(view.text()).toContain('2 个模型')
  expect(view.findAll('.key-models li').map(item => item.text())).toEqual(cc2.models)
  expect(view.text()).toContain('[CC2]ClaudeCode高智 · 分组 602 · 0.6×')
  expect(view.text()).toContain('北辰 Key 42')
  // 账号余额属于账号级数据，不能出现在单 Key 能力卡片里。
  expect(view.text()).not.toContain('4.974')
  expect(view.text()).toContain('单日原价 0.11 · 实付 0.066（USD）')
  expect(view.text()).toContain('缓存读取 — · 缓存写入 0')
  expect(view.get('a').text()).toBe('打开北辰核对分组')
  expect(view.find('form').exists()).toBe(false)
  await view.get('button').trigger('click')
  const dialog = view.get('[role="dialog"]')
  expect(dialog.attributes('aria-label')).toBe('核对这个 Key · cc不满血')
  expect(dialog.get('input[name="target"]').element).toHaveProperty('value', '0.6')
  expect(dialog.find('input[type="checkbox"]').exists()).toBe(false)
  await dialog.get('form').trigger('submit')
  await flushPromises()
  expect(refreshUsage).toHaveBeenCalledWith('provider', {
    credential_id: 'cc-channel', target_multiplier: '0.6', expect_claude: false,
  })
  expect(view.emitted('refresh')).toHaveLength(1)

  await view.setProps({ capabilities: [{ ...cc2, expect_claude: false }] })
  expect(dialog.text()).toContain('官网分组倍率符合设定的目标')

  await view.setProps({ capabilities: [{ ...cc2, usage_state: 'stale', group_check: 'pending', usage_error: '北辰接口返回 HTTP 401' }] })
  expect(view.text()).toContain('已陈旧')
  expect(view.text()).toContain('实付 0.066（USD）')
  expect(view.find('[role="alert"]').exists()).toBe(false)
  view.unmount()
})

it('Gemini 保留分组名并显示权限缺失，G3 显示已核实的倍率和型号', () => {
  const denied = '账号无 group 5 权限'
  const gemini: BeichenKeyCapability = {
    ...cc2, credential_id: 'gemini-key', label: 'Gemini', group_status: 'not_allowed',
    website_group: { id: 5, name: '[Gemini]国外豆包', rate_multiplier: '0.30' },
    models: ['gemini-old'], models_state: 'stale', models_error: denied, usage_error: denied, daily_usage: null,
  }
  const g3: BeichenKeyCapability = {
    ...cc2, credential_id: 'g3-key', label: 'G3', models: ['gpt-6.2-sol'], has_claude: false,
    website_group: { id: 10, name: '[G3]GPT', rate_multiplier: '0.20' },
    stored_multiplier: '0.2', daily_usage: null, group_check: 'mismatch',
  }
  const view = mountKeys([gemini, g3])
  const [geminiCard, g3Card] = view.findAll('article')
  expect(geminiCard.text()).toContain('[Gemini]国外豆包 · 分组 5 · 0.30×')
  expect(geminiCard.get('[role="status"]').text()).toBe(denied)
  expect(geminiCard.text()).not.toMatch(/找不到|分组不存在|gemini-old/)
  expect(g3Card.text()).toContain('0.20×')
  expect(g3Card.text()).toContain('1 个模型')
  expect(g3Card.text()).toContain('gpt-6.2-sol')
  expect(g3Card.text()).not.toMatch(/Claude|目标分组|有差异/)
  expect(view.findAll('input')).toHaveLength(0)
  view.unmount()
})

it.each([
  ['pending', '分组待同步'],
  ['not_found', 'Key 分组不存在'],
] as const)('按 %s 展示分组状态', (group_status, message) => {
  const view = mountKeys([{ ...cc2, group_status, website_group: null, models: [], models_fetched_at: null }])
  expect(view.get('[role="status"]').text()).toBe(message)
  view.unmount()
})
