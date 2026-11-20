import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BindingAdapter } from '../api/types'
import StudentHarnessConfig from './StudentHarnessConfig.vue'

function model(modelID: string, overrides: Record<string, unknown> = {}) {
  return {
    model_id: modelID,
    display_name: modelID,
    provider_name: 'Relay',
    primary_adapter: 'openai_responses' as BindingAdapter,
    route_ready: true,
    ...overrides,
  }
}

describe('StudentHarnessConfig', () => {
  it('does not treat route_ready as a Codex or Claude protocol route', () => {
    const wrapper = mount(StudentHarnessConfig, {
      props: {
        apiKey: 'neb_sk_test',
        models: [
          model('gpt-5.6-luna', { responses_ready: false }),
          model('claude-sonnet-4-5', { primary_adapter: 'anthropic', anthropic_ready: false }),
        ],
      },
    })
    expect(wrapper.text()).toContain('没有满足协议')
    expect(wrapper.text()).not.toContain('一键配置 Codex')
    expect(wrapper.text()).not.toContain('一键配置 Claude Code')
    wrapper.unmount()
  })

  it('blocks one-click setup when the gateway address repeats /v1', async () => {
    const wrapper = mount(StudentHarnessConfig, {
      props: {
        apiKey: 'neb_sk_test',
        models: [model('gpt-5.6-luna', { responses_ready: true })],
      },
    })
    const button = wrapper.findAll('button').find(item => item.text() === '一键配置 Codex')
    expect(button).toBeTruthy()
    expect(button!.attributes('disabled')).toBeUndefined()

    await wrapper.get('.student-harness-input').setValue('https://gateway.example/v1')
    expect(wrapper.text()).toContain('不要包含 /v1')
    expect(button!.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
