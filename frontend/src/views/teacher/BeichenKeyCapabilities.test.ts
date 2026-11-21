import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import type { BeichenKeyCapability } from '../../api/types'
import BeichenKeyCapabilities from './BeichenKeyCapabilities.vue'

const refreshUsage = vi.hoisted(() => vi.fn())
vi.mock('../../api/teacher', () => ({ teacherAPI: { refreshBeichenKeyUsage: refreshUsage } }))

it('展示 CC2 官网分组和账号级参考流水，按选中渠道核对', async () => {
  refreshUsage.mockResolvedValueOnce({ synced: true })
  const at = '2026-11-21T06:00:00Z'
  const cc2: BeichenKeyCapability = {
    credential_id: 'cc-channel', label: 'cc不满血', models: ['claude-opus-4-6', 'claude-sonnet-4-6'],
    website_key_id: 42, website_group: { id: 602, name: '[CC2]ClaudeCode高智', rate_multiplier: '0.6' },
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
  const view = mount(BeichenKeyCapabilities, { props: { providerId: 'provider', capabilities: [cc2] } })

  expect(view.text()).toContain('2 个模型 · 含 Claude')
  expect(view.findAll('.key-models li').map(item => item.text())).toEqual(cc2.models)
  expect(view.text()).toContain('[CC2]ClaudeCode高智 · 分组 602 · 0.6×')
  expect(view.text()).toContain('北辰 Key 42')
  // 账号余额属于账号级数据，不能出现在单 Key 能力卡片里。
  expect(view.text()).not.toContain('4.974')
  expect(view.text()).toContain('单日原价 0.11 · 实付 0.066（USD）')
  expect(view.text()).toContain('缓存读取 — · 缓存写入 0')
  expect(view.get('[role="status"]').text()).toContain('官网分组符合设定的目标')
  expect(view.get('a').text()).toBe('打开北辰核对分组')
  await view.get('form').trigger('submit')
  await flushPromises()
  expect(refreshUsage).toHaveBeenCalledWith('provider', {
    credential_id: 'cc-channel', target_multiplier: '0.6', expect_claude: true,
  })
  expect(view.emitted('refresh')).toHaveLength(1)

  await view.setProps({ capabilities: [{ ...cc2, usage_state: 'stale', group_check: 'pending', usage_error: '北辰接口返回 HTTP 401' }] })
  expect(view.text()).toContain('已陈旧')
  expect(view.text()).toContain('实付 0.066（USD）')
  expect(view.find('[role="alert"]').exists()).toBe(false)
  view.unmount()
})

it('标注 /v1/usage 账号级参考，并预留分组数据替换位置', () => {
  const view = mount(BeichenKeyCapabilities, { props: { providerId: 'provider', capabilities: [] } })

  expect(view.get('h3').text()).toContain('账号级参考')
  expect(view.get('section').attributes('aria-label')).toBe('Key 实际能力（账号级参考）')
  expect(view.get('section').attributes('data-scope')).toBe('account-reference')
  expect(view.get('section').attributes('data-replace-with')).toBe('key-group-data')
  const scope = view.get('[data-testid="key-capability-scope"]')
  expect(scope.text()).toContain('/v1/usage')
  expect(scope.text()).toContain('账号级参考')
  expect(scope.text()).toContain('不能单独当作单把 Key 的事实')
  view.unmount()
})
