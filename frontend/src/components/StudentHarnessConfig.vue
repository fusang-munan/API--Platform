<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Model } from '../api/types'
import {
  buildCCSwitchClaudeLink,
  buildCCSwitchCodexLink,
  buildStudentHarnessConfig,
  claudeHarnessModels,
  claudeHarnessReady,
  codexSupportedReasoningEfforts,
  codexHarnessModels,
  codexHarnessReady,
  defaultHarnessModel,
  gatewayOrigin,
  gatewayOriginIssue,
  keyHarnessSupport,
  STUDENT_GATEWAY_ERROR_GUIDANCE,
} from '../utils/studentHarnessConfig'
import { bindingAdapterMeta } from '../utils/bindingAdapters'
import { useToast } from '../composables/useToast'

const CC_SWITCH_DOWNLOAD = 'https://github.com/farion1231/cc-switch/releases'

const props = defineProps<{
  apiKey: string
  models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready' | 'responses_ready'>[]
  keyPrefix?: string | null
  keyName?: string | null
}>()

const toast = useToast()
const selectedCodexModel = ref('')
const selectedClaudeModel = ref('')
const gatewayAddress = ref(gatewayOrigin())

const codexReasoningHint = computed(() => {
  if (!selectedCodexModel.value) return ''
  return codexSupportedReasoningEfforts(selectedCodexModel.value).join(', ')
})

const support = computed(() => keyHarnessSupport(props.models))
const dual = computed(() => support.value.codex && support.value.claude)
const codexOptions = computed(() => codexHarnessModels(props.models))
const claudeOptions = computed(() => claudeHarnessModels(props.models))
const codexReady = computed(() => codexOptions.value.some(model => codexHarnessReady(model)))
const originIssue = computed(() => gatewayOriginIssue(gatewayAddress.value))

const selectedCodex = computed(() => codexOptions.value.find(model => model.model_id === selectedCodexModel.value))
const selectedClaude = computed(() => claudeOptions.value.find(model => model.model_id === selectedClaudeModel.value))

watch(
  () => [props.models, props.apiKey] as const,
  () => {
    selectedCodexModel.value = defaultHarnessModel(props.models, 'codex')
    selectedClaudeModel.value = defaultHarnessModel(props.models, 'claude')
  },
  { immediate: true, deep: true },
)

const harnessOptions = computed(() => ({
  codexModel: selectedCodexModel.value,
  claudeModel: selectedClaudeModel.value,
}))

const config = computed(() => buildStudentHarnessConfig(props.apiKey, props.models, gatewayAddress.value, harnessOptions.value))
const codexLink = computed(() => buildCCSwitchCodexLink(
  config.value,
  props.apiKey,
  codexOptions.value,
  props.keyName,
  selectedCodexModel.value,
  { includeInlineConfig: true },
))
const claudeLink = computed(() => buildCCSwitchClaudeLink(config.value, props.apiKey, props.keyName))

function modelLabel(model: Pick<Model, 'model_id' | 'display_name'>) {
  if (model.display_name && model.display_name !== model.model_id) {
    return `${model.display_name} · ${model.model_id}`
  }
  return model.model_id
}

function routeStatus(model: Pick<Model, 'route_ready' | 'responses_ready' | 'anthropic_ready'>, kind: 'codex' | 'claude') {
  if (kind === 'codex') {
    if (model.responses_ready) return 'Responses 就绪'
    return model.route_ready ? 'Responses 未就绪（路由已通）' : 'Responses 未就绪'
  }
  if (model.anthropic_ready) return 'Anthropic 就绪'
  return model.route_ready ? 'Anthropic 未就绪（路由已通）' : 'Anthropic 未就绪'
}

function setupCodex() {
  if (originIssue.value) {
    toast.error(originIssue.value)
    return
  }
  window.location.href = codexLink.value
  toast.success('正在打开 CC-switch 配置 Codex')
}

function openClaudeCode() {
  if (originIssue.value) {
    toast.error(originIssue.value)
    return
  }
  window.location.href = claudeLink.value
  toast.success('正在打开 CC-switch 配置 Claude Code')
}

function resetGatewayAddress() {
  gatewayAddress.value = gatewayOrigin()
}

async function copyText(value: string, message: string) {
  try {
    await navigator.clipboard.writeText(value)
    toast.success(message)
  } catch {
    toast.error('复制失败')
  }
}
</script>

<template>
  <section class="student-harness">
    <a
      v-if="support.codex || support.claude"
      class="student-harness-install"
      :href="CC_SWITCH_DOWNLOAD"
      target="_blank"
      rel="noopener noreferrer"
    >
      <span class="student-harness-install-step">1</span>
      <span class="student-harness-install-text">
        <strong>安装 CC-switch</strong>
        <span class="muted">一键导入需要客户端</span>
      </span>
      <span class="student-harness-install-action">下载</span>
    </a>

    <p v-if="!support.codex && !support.claude" class="student-harness-empty muted">
      当前 Key 没有满足协议的 Codex 或 Claude Code 模型。请到模型广场查看是否标有「需重新申领 Key」，撤回后按原审批额度重新申请。
    </p>

    <div class="student-harness-gateway">
      <label class="student-harness-field">
        <span class="student-harness-label">网关地址</span>
        <input v-model="gatewayAddress" class="student-harness-input" placeholder="https://你的网关域名">
      </label>
      <button type="button" class="button ghost" @click="resetGatewayAddress">恢复默认</button>
      <p class="student-harness-hint muted">
        Claude 的 ANTHROPIC_BASE_URL 只写到域名，不带 /v1；Codex 的 base_url 会自动带 /v1。
      </p>
      <p v-if="originIssue" class="error banner">{{ originIssue }}</p>
    </div>

    <div v-if="support.codex || support.claude" class="student-harness-grid" :class="{ 'is-dual': dual }">
      <article v-if="support.codex" class="student-harness-block">
        <header v-if="dual" class="student-harness-block-title">Codex</header>

        <div v-if="codexOptions.length" class="student-harness-section">
          <span class="student-harness-label">Key 可用模型（{{ codexOptions.length }}）</span>
          <div class="student-harness-tags">
            <span
              v-for="model in codexOptions"
              :key="`codex-tag-${model.model_id}`"
              class="student-harness-tag"
              :class="{ accent: model.model_id === selectedCodexModel }"
            >
              {{ model.model_id }}
            </span>
          </div>
        </div>

        <label class="student-harness-field">
          <span class="student-harness-label">默认模型</span>
          <select v-model="selectedCodexModel" class="student-harness-select" :disabled="!codexOptions.length">
            <option v-for="model in codexOptions" :key="`codex-${model.model_id}`" :value="model.model_id">
              {{ modelLabel(model) }}
            </option>
          </select>
        </label>

        <p v-if="codexReasoningHint" class="student-harness-hint muted">
          Supported reasoning effort (change in Codex): {{ codexReasoningHint }}
        </p>

        <div v-if="selectedCodex" class="student-harness-tags">
          <span v-if="selectedCodex.provider_name" class="student-harness-tag">{{ selectedCodex.provider_name }}</span>
          <span class="student-harness-tag accent">{{ bindingAdapterMeta('openai_responses').label }}</span>
          <span class="student-harness-tag">{{ routeStatus(selectedCodex, 'codex') }}</span>
        </div>

        <div class="student-harness-endpoint">
          <span class="student-harness-label">网关</span>
          <code>{{ config.gateway_api_base }}</code>
          <button type="button" class="student-harness-copy" @click="copyText(config.gateway_api_base, '已复制网关地址')">复制</button>
        </div>

        <div class="student-harness-step">
          <span class="student-harness-install-step">2</span>
          <div class="student-harness-step-body">
            <button
              type="button"
              class="button primary wide student-harness-cta"
              :disabled="!codexReady || Boolean(originIssue)"
              @click="setupCodex"
            >
              一键配置 Codex
            </button>
            <p class="student-harness-hint muted">
              导入 Key 上全部 Codex 模型（{{ codexOptions.length }} 个）；确认后请完全退出并重启 Codex。
            </p>
          </div>
        </div>
      </article>

      <article v-if="support.claude" class="student-harness-block">
        <header v-if="dual" class="student-harness-block-title">Claude Code</header>

        <div v-if="claudeOptions.length" class="student-harness-section">
          <span class="student-harness-label">Key 可用模型（{{ claudeOptions.length }}）</span>
          <div class="student-harness-tags">
            <span
              v-for="model in claudeOptions"
              :key="`claude-tag-${model.model_id}`"
              class="student-harness-tag"
              :class="{ accent: model.model_id === selectedClaudeModel }"
            >
              {{ model.model_id }}
            </span>
          </div>
        </div>

        <label class="student-harness-field">
          <span class="student-harness-label">查看模型详情</span>
          <select v-model="selectedClaudeModel" class="student-harness-select" :disabled="!claudeOptions.length">
            <option v-for="model in claudeOptions" :key="`claude-${model.model_id}`" :value="model.model_id">
              {{ modelLabel(model) }}
            </option>
          </select>
        </label>

        <div v-if="selectedClaude" class="student-harness-tags">
          <span v-if="selectedClaude.provider_name" class="student-harness-tag">{{ selectedClaude.provider_name }}</span>
          <span class="student-harness-tag accent">{{ bindingAdapterMeta('anthropic').label }}</span>
          <span class="student-harness-tag">{{ routeStatus(selectedClaude, 'claude') }}</span>
        </div>

        <div class="student-harness-endpoint">
          <span class="student-harness-label">网关</span>
          <code>{{ config.anthropic_base_url }}</code>
          <button type="button" class="student-harness-copy" @click="copyText(config.anthropic_base_url, '已复制网关地址')">复制</button>
        </div>

        <div class="student-harness-step">
          <span class="student-harness-install-step">2</span>
          <button
            type="button"
            class="button primary wide student-harness-cta"
            :disabled="!claudeOptions.some(model => claudeHarnessReady(model)) || Boolean(originIssue)"
            @click="openClaudeCode"
          >
            一键配置 Claude Code
          </button>
        </div>
      </article>
    </div>

    <section class="student-harness-help">
      <p class="student-harness-hint muted">
        模型广场里标有「需重新申领 Key」的模型不在当前 Key 中。请先在模型广场撤回，并按原审批额度重新申请 Key；新 Key 生效后重新打开此面板。
      </p>
      <details>
        <summary>客户端报错时怎么处理</summary>
        <ul>
          <li v-for="item in STUDENT_GATEWAY_ERROR_GUIDANCE" :key="item.key">
            <strong>{{ item.title }}</strong>：{{ item.action }}
          </li>
        </ul>
      </details>
    </section>
  </section>
</template>

<style scoped>
.student-harness-gateway { display: grid; gap: 8px; padding: 12px 14px; border: 1px solid var(--line); border-radius: 14px; background: #fafbfe; }
.student-harness-gateway .button { justify-self: start; }
.student-harness-input { width: 100%; padding: 11px 12px; border: 1px solid var(--line); border-radius: 12px; background: #fff; font-size: 14px; color: var(--ink); margin-top: 0; display: block; }
.student-harness-input:focus { border-color: rgba(108,76,232,.45); outline: 2px solid rgba(108,76,232,.12); outline-offset: 0; }
.student-harness-help { display: grid; gap: 8px; padding: 12px 14px; border: 1px solid rgba(108,76,232,.12); border-radius: 14px; background: #fafbfe; }
.student-harness-help summary { cursor: pointer; font-size: 12px; font-weight: 700; color: var(--primary); }
.student-harness-help ul { margin: 8px 0 0; padding-left: 18px; display: grid; gap: 6px; font-size: 12px; line-height: 1.55; color: var(--muted); }
</style>
