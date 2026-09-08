import type { BindingAdapter } from '../api/types'

export type BindingAdapterMeta = {
  value: BindingAdapter
  label: string
  gatewayPath: string
}

/** 与后端 model_binding.adapter 枚举一致 */
export const BINDING_ADAPTER_OPTIONS: BindingAdapterMeta[] = [
  { value: 'openai_compatible', label: 'OpenAI Chat / Completions', gatewayPath: 'POST /v1/chat/completions' },
  { value: 'openai_responses', label: 'OpenAI Responses', gatewayPath: 'POST /v1/responses' },
  { value: 'openai_embeddings', label: 'OpenAI Embeddings', gatewayPath: 'POST /v1/embeddings' },
  { value: 'openai_images', label: 'OpenAI Images', gatewayPath: 'POST /v1/images/*' },
  { value: 'openai_audio', label: 'OpenAI Audio', gatewayPath: 'POST /v1/audio/*' },
  { value: 'openai_video', label: 'OpenAI Video', gatewayPath: 'POST /v1/videos' },
  { value: 'openai_realtime', label: 'OpenAI Realtime', gatewayPath: 'WS /v1/realtime' },
  { value: 'openai_moderations', label: 'OpenAI Moderations', gatewayPath: 'POST /v1/moderations' },
  { value: 'anthropic', label: 'Anthropic Messages', gatewayPath: 'POST /v1/messages' },
  { value: 'cohere_rerank_v2', label: 'Cohere Rerank v2', gatewayPath: 'POST /v2/rerank' },
  { value: 'google_gemini_v1beta', label: 'Google Gemini v1beta', gatewayPath: 'POST /v1beta/models/*' },
]

const byValue = new Map(BINDING_ADAPTER_OPTIONS.map(item => [item.value, item]))

export function bindingAdapterMeta(adapter: BindingAdapter): BindingAdapterMeta {
  return byValue.get(adapter) ?? { value: adapter, label: adapter, gatewayPath: '—' }
}

export function isImageGenerationModel(modelId: string) {
  const normalized = modelId.toLowerCase().replace(/[_/.]/g, '-')
  return normalized.includes('gpt-image') || normalized.includes('dall-e') || normalized.includes('dalle')
}

export function usesDualChatBindings(modelId: string) {
  return Boolean(modelId.trim()) && !isImageGenerationModel(modelId)
}

export function defaultBindingAdapter(modelId: string): BindingAdapter {
  return isImageGenerationModel(modelId) ? 'openai_images' : 'openai_compatible'
}

export function harnessConfigSnippet(modelId: string, adapter: BindingAdapter, baseUrl: string) {
  const meta = bindingAdapterMeta(adapter)
  const root = baseUrl.replace(/\/$/, '')
  const apiBase = root.endsWith('/v1') ? root : `${root}/v1`
  return {
    base_url: apiBase,
    model: modelId,
    gateway_path: meta.gatewayPath,
  }
}
