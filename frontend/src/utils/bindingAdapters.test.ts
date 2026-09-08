import { describe, expect, it } from 'vitest'
import { BINDING_ADAPTER_OPTIONS, defaultBindingAdapter, harnessConfigSnippet, isImageGenerationModel, usesDualChatBindings } from './bindingAdapters'

describe('bindingAdapters', () => {
  it('lists all backend adapter values', () => {
    expect(BINDING_ADAPTER_OPTIONS.map(item => item.value)).toEqual([
      'openai_compatible',
      'openai_responses',
      'openai_embeddings',
      'openai_images',
      'openai_audio',
      'openai_video',
      'openai_realtime',
      'openai_moderations',
      'anthropic',
      'cohere_rerank_v2',
      'google_gemini_v1beta',
    ])
  })

  it('builds Codex / Claude Code harness fields', () => {
    const snippet = harnessConfigSnippet('gpt-5.6-sol', 'openai_compatible', 'https://gateway.example/v1')
    expect(snippet.base_url).toBe('https://gateway.example/v1')
    expect(snippet.model).toBe('gpt-5.6-sol')
    expect(snippet.gateway_path).toContain('/v1/chat/completions')
  })

  it('uses dual chat bindings for text models only', () => {
    expect(usesDualChatBindings('gpt-5.6-luna')).toBe(true)
    expect(isImageGenerationModel('gpt-image-2')).toBe(true)
    expect(usesDualChatBindings('gpt-image-2')).toBe(false)
    expect(defaultBindingAdapter('gpt-image-2')).toBe('openai_images')
    expect(defaultBindingAdapter('gpt-5.6-luna')).toBe('openai_compatible')
  })
})
