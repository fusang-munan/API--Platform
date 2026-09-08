<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Model } from '../api/types'
import {
  buildCCSwitchClaudeLink,
  buildCCSwitchCodexLink,
  buildCodexCatalogPatchScript,
  buildStudentHarnessConfig,
  claudeHarnessModels,
  codexHarnessModels,
  defaultHarnessModel,
  downloadTextFile,
  keyHarnessSupport,
} from '../utils/studentHarnessConfig'
import { bindingAdapterMeta } from '../utils/bindingAdapters'
import { useToast } from '../composables/useToast'

const CC_SWITCH_DOWNLOAD = 'https://github.com/farion1231/cc-switch/releases'

const props = defineProps<{
  apiKey: string
  models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready' | 'responses_ready'>[]
  keyPrefix?: string | null
}>()

const toast = useToast()
const selectedCodexModel = ref('')
const selectedClaudeModel = ref('')

const support = computed(() => keyHarnessSupport(props.models))
const dual = computed(() => support.value.codex && support.value.claude)
const codexOptions = computed(() => codexHarnessModels(props.models))
const claudeOptions = computed(() => claudeHarnessModels(props.models))

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

const config = computed(() => buildStudentHarnessConfig(props.apiKey, props.models, undefined, {
  codexModel: selectedCodexModel.value || null,
  claudeModel: selectedClaudeModel.value || null,
}))

const codexLink = computed(() => buildCCSwitchCodexLink(config.value, props.apiKey, codexOptions.value))
const claudeLink = computed(() => buildCCSwitchClaudeLink(config.value, props.apiKey))

function modelLabel(model: Pick<Model, 'model_id' | 'display_name'>) {
  if (model.display_name && model.display_name !== model.model_id) {
    return `${model.display_name} · ${model.model_id}`
  }
  return model.model_id
}

function openCCSwitch(link: string, label: string) {
  window.location.href = link
  toast.success(`正在打开 CC-switch 配置 ${label}`)
}

function downloadCodexScript() {
  downloadTextFile('nebula-codex-catalog.sh', buildCodexCatalogPatchScript(config.value, 'bash'), 'text/x-shellscript;charset=utf-8')
  toast.success('已下载 catalog 补丁脚本，执行后请完全退出并重启 Codex')
}

async function copyGateway(url: string) {
  try {
    await navigator.clipboard.writeText(url)
    toast.success('已复制网关地址')
  } catch {
    toast.error('复制失败')
  }
}
</script>

<template>
  <section class="student-harness">
    <a
      class="student-harness-install"
      :href="CC_SWITCH_DOWNLOAD"
      target="_blank"
      rel="noopener noreferrer"
    >
      <span class="student-harness-install-step">1</span>
      <span class="student-harness-install-text">
        <strong>安装 CC-switch</strong>
        <span class="muted">配置前需先安装客户端</span>
      </span>
      <span class="student-harness-install-action">下载</span>
    </a>

    <p v-if="!support.codex && !support.claude" class="student-harness-empty muted">
      当前 Key 没有可用的 Codex 或 Claude Code 路由。
    </p>

    <div v-else class="student-harness-grid" :class="{ 'is-dual': dual }">
      <article v-if="support.codex" class="student-harness-block">
        <header v-if="dual" class="student-harness-block-title">Codex</header>

        <label class="student-harness-field">
          <span class="student-harness-label">模型</span>
          <select v-model="selectedCodexModel" class="student-harness-select" :disabled="!codexOptions.length">
            <option v-for="model in codexOptions" :key="`codex-${model.model_id}`" :value="model.model_id">
              {{ modelLabel(model) }}
            </option>
          </select>
        </label>

        <div v-if="selectedCodex" class="student-harness-tags">
          <span v-if="selectedCodex.provider_name" class="student-harness-tag">{{ selectedCodex.provider_name }}</span>
          <span class="student-harness-tag accent">{{ bindingAdapterMeta('openai_responses').label }}</span>
        </div>

        <div class="student-harness-endpoint">
          <span class="student-harness-label">网关</span>
          <code>{{ config.gateway_api_base }}</code>
          <button type="button" class="student-harness-copy" @click="copyGateway(config.gateway_api_base)">复制</button>
        </div>

        <button
          type="button"
          class="button primary wide student-harness-cta"
          :disabled="!selectedCodexModel || !selectedCodex?.responses_ready"
          @click="openCCSwitch(codexLink, 'Codex')"
        >
          打开 CC-switch 配置 Codex
        </button>
        <p class="muted student-harness-hint">
          CC-switch 不会写入 model catalog。若 Codex 提示 <code>Model metadata not found</code>，请
          <button type="button" class="button ghost inline" @click="downloadCodexScript">下载 catalog 补丁</button>
          并在 Mac 终端执行（不会覆盖 CC-switch 的 provider 配置），然后完全退出并重启 Codex。
        </p>
      </article>

      <article v-if="support.claude" class="student-harness-block">
        <header v-if="dual" class="student-harness-block-title">Claude Code</header>

        <label class="student-harness-field">
          <span class="student-harness-label">模型</span>
          <select v-model="selectedClaudeModel" class="student-harness-select" :disabled="!claudeOptions.length">
            <option v-for="model in claudeOptions" :key="`claude-${model.model_id}`" :value="model.model_id">
              {{ modelLabel(model) }}
            </option>
          </select>
        </label>

        <div v-if="selectedClaude" class="student-harness-tags">
          <span v-if="selectedClaude.provider_name" class="student-harness-tag">{{ selectedClaude.provider_name }}</span>
          <span class="student-harness-tag accent">{{ bindingAdapterMeta('anthropic').label }}</span>
        </div>

        <div class="student-harness-endpoint">
          <span class="student-harness-label">网关</span>
          <code>{{ config.anthropic_base_url }}</code>
          <button type="button" class="student-harness-copy" @click="copyGateway(config.anthropic_base_url)">复制</button>
        </div>

        <button
          type="button"
          class="button primary wide student-harness-cta"
          :disabled="!selectedClaudeModel || !selectedClaude?.anthropic_ready"
          @click="openCCSwitch(claudeLink, 'Claude Code')"
        >
          打开 CC-switch 配置 Claude Code
        </button>
      </article>
    </div>
  </section>
</template>
