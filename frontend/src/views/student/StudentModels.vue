<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, BindingAdapter, Model, ModelCatalogCandidate, ModelCategory, ModelStatus } from '../../api/types'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'
import { capabilityLabel, formatTokenK } from '../../utils/modelCatalog'
import { buildKeyReapplyModels, keyCanWithdraw, keyReapplyQuota, selectCurrentStudentKey } from '../../utils/studentHarnessConfig'
import { sortModelsByRecency } from '../../utils/modelSort'
import { filterStudentModels } from '../../utils/studentModels'
import { catalogCreditsLabel, catalogProviderLabel, modelCreditsLabel, modelDescriptionText, modelProviderLabel } from '../../utils/modelPlazaDisplay'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { APIError } from '../../api/client'

type ModelWithProtocols = Model & { supported_protocols?: string[] }

const commonRows = ref<ModelWithProtocols[]>([])
const libraryRows = ref<ModelWithProtocols[]>([])
const keys = ref<ApiKey[]>([])
const currentKeyID = ref('')
const catalogItems = ref<ModelCatalogCandidate[]>([])
const query = ref('')
const category = ref<ModelCategory | ''>('')
const status = ref<ModelStatus | ''>('')
const protocol = ref('openai_compatible')
const expanded = ref('')
const addOpen = ref(false)
const catalogQuery = ref('')
const catalogPage = ref(1)
const catalogTotal = ref(0)
const error = ref('')
const reapplyModel = ref<Model | null>(null)
const reapplyOpen = ref(false)
const reapplyBusy = ref(false)
const loadState = useLoadState()
const toast = useToast()

const libraryIDs = computed(() => new Set([
  ...commonRows.value.map(model => model.model_id.toLowerCase()),
  ...libraryRows.value.map(model => model.model_id.toLowerCase()),
]))
const filteredCommon = computed(() => sortModelsByRecency(filterStudentModels(commonRows.value, query.value, category.value, status.value)) as ModelWithProtocols[])
const filteredLibrary = computed(() => sortModelsByRecency(filterStudentModels(libraryRows.value, query.value, category.value, status.value)) as ModelWithProtocols[])
const hasAnyModels = computed(() => filteredCommon.value.length > 0 || filteredLibrary.value.length > 0)
const catalogPages = computed(() => Math.max(1, Math.ceil(catalogTotal.value / 20)))
const allRows = computed(() => [...commonRows.value, ...libraryRows.value])
const selectableKeys = computed(() => keys.value.filter(key => key.status !== 'rejected'))
const currentKey = computed(() => keys.value.find(key => key.id === currentKeyID.value) ?? null)
const currentKeyModelIDs = computed(() => new Set((currentKey.value?.models ?? []).map(model => model.model_id.trim().toLowerCase())))
const missingKeyCount = computed(() => allRows.value.filter(model => requiresNewKey(model)).length)
const reapplyQuota = computed(() => currentKey.value ? keyReapplyQuota(currentKey.value) : '')
const reapplyModels = computed(() => currentKey.value && reapplyModel.value
  ? buildKeyReapplyModels(currentKey.value, reapplyModel.value.model_id)
  : [])
const canReapply = computed(() => Boolean(currentKey.value && keyCanWithdraw(currentKey.value)))
const protocolOptions = BINDING_ADAPTER_OPTIONS.filter(item =>
  ['openai_compatible', 'openai_responses', 'anthropic', 'google_gemini_v1beta'].includes(item.value))
const selectedProtocolLabel = computed(() => protocol.value ? protocolMeta(protocol.value).label : '')

const protocolShortLabels: Record<string, string> = {
  openai_compatible: 'Chat',
  openai_responses: 'Responses',
  anthropic: 'Anthropic',
  google_gemini_v1beta: 'Gemini',
  openai_embeddings: 'Embeddings',
  openai_images: 'Images',
  openai_audio: 'Audio',
  openai_video: 'Video',
  openai_realtime: 'Realtime',
  openai_moderations: 'Moderations',
  cohere_rerank_v2: 'Rerank',
}

function protocolsFor(model: ModelWithProtocols): string[] {
  const values = new Set((model.supported_protocols ?? []).filter(Boolean))
  if (model.primary_adapter) values.add(model.primary_adapter)
  if (model.responses_ready) values.add('openai_responses')
  if (model.anthropic_ready) values.add('anthropic')
  return BINDING_ADAPTER_OPTIONS.map(item => item.value).filter(value => values.has(value))
}

function protocolShortLabel(value: string) {
  return protocolShortLabels[value] ?? value
}

function protocolMeta(value: string) {
  return bindingAdapterMeta(value as BindingAdapter)
}

function protocolMismatch(model: ModelWithProtocols) {
  if (!protocol.value) return ''
  const supported = protocolsFor(model)
  if (!supported.length || supported.includes(protocol.value)) return ''
  const alternatives = supported.map(value => protocolMeta(value).label)
  return `不支持 ${selectedProtocolLabel.value}；可用替代协议：${alternatives.join('、')}。请在客户端切换协议，网关不做协议转换。`
}

watch(catalogQuery, () => { catalogPage.value = 1 })

async function load() {
  await loadState.run(async () => {
    try {
      const [plaza, keyList] = await Promise.all([
        studentAPI.models(),
        studentAPI.keys().catch(() => [] as ApiKey[]),
      ])
      commonRows.value = plaza.common_models
      libraryRows.value = plaza.library_models
      keys.value = keyList
      if (!keyList.some(key => key.id === currentKeyID.value)) {
        currentKeyID.value = selectCurrentStudentKey(keyList)?.id ?? ''
      }
      error.value = ''
    } catch (caught) {
      error.value = caught instanceof APIError ? caught.message : '加载模型失败'
    }
  })
}

async function openAdd() {
  addOpen.value = true
  catalogQuery.value = ''
  catalogPage.value = 1
  try {
    await searchCatalog()
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载模型目录失败'
  }
}

async function searchCatalog() {
  try {
    const page = await studentAPI.modelCatalog(catalogQuery.value, catalogPage.value, 20)
    catalogItems.value = page.items
    catalogTotal.value = page.total
    error.value = ''
  } catch (caught) {
    catalogItems.value = []
    catalogTotal.value = 0
    error.value = caught instanceof APIError ? caught.message : '搜索模型目录失败'
  }
}

async function addModel(modelID: string) {
  try {
    const model = await studentAPI.addModel(modelID)
    if (!libraryRows.value.some(item => item.model_id.toLowerCase() === model.model_id.toLowerCase())
      && !commonRows.value.some(item => item.model_id.toLowerCase() === model.model_id.toLowerCase())) {
      libraryRows.value = sortModelsByRecency([...libraryRows.value, model])
    }
    toast.success('已加入我的模型广场')
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '添加模型失败'
  }
}

async function removeModel(modelID: string) {
  if (!confirm(`从模型广场移除 ${modelID}？`)) return
  try {
    await studentAPI.removeModel(modelID)
    libraryRows.value = libraryRows.value.filter(model => model.model_id !== modelID)
    toast.success('已从模型广场移除')
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '移除模型失败'
  }
}

function refresh() { void load() }

function requiresNewKey(model: Model) {
  return Boolean(currentKey.value) && !currentKeyModelIDs.value.has(model.model_id.trim().toLowerCase())
}

function openReapply(model: Model) {
  reapplyModel.value = model
  reapplyOpen.value = true
}

function keyStatusLabel(status: ApiKey['status']) {
  const labels: Record<ApiKey['status'], string> = {
    pending_mentor: '待导师审核',
    pending_teacher: '待老师终审',
    approved: '已审批待领取',
    active: '已生效',
    inactive: '已停用',
    rejected: '已驳回',
  }
  return labels[status] ?? status
}

function caughtMessage(caught: unknown, fallback: string) {
  return caught instanceof APIError ? caught.message : fallback
}

async function confirmReapply() {
  const key = currentKey.value
  const model = reapplyModel.value
  if (!key || !model || reapplyBusy.value || !canReapply.value) return
  reapplyBusy.value = true
  try {
    await studentAPI.withdraw(key.id)
    try {
      await studentAPI.submit({
        name: key.name,
        organization_id: key.organization.id,
        project_id: key.project.id,
        models: buildKeyReapplyModels(key, model.model_id),
        requested_monthly_credits: keyReapplyQuota(key),
      })
      toast.success(`已撤回并重新申请，沿用原审批额度 ${keyReapplyQuota(key)} credits`)
      reapplyOpen.value = false
      reapplyModel.value = null
      await load()
    } catch (caught) {
      error.value = `原申请已撤回，但新申请提交失败：${caughtMessage(caught, '请到「申请密钥」页重新提交')}`
      await load()
    }
  } catch (caught) {
    error.value = caughtMessage(caught, '撤回失败，请稍后重试')
  } finally {
    reapplyBusy.value = false
  }
}

onMounted(() => {
  void load()
  window.addEventListener('nebula:refresh', refresh)
})
onBeforeUnmount(() => window.removeEventListener('nebula:refresh', refresh))
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">STUDENT</p>
        <h1>模型广场</h1>
      </div>
      <button class="button primary" @click="openAdd">检索添加</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel">
      <div class="grid two">
        <label>搜索<input v-model="query" placeholder="名称、模型 ID 或能力"></label>
        <div class="row">
          <label>类别<select v-model="category"><option value="">全部类别</option><option v-for="value in ['text','image','audio','video','multimodal','embedding','rerank']" :key="value">{{ value }}</option></select></label>
          <label>状态<select v-model="status"><option value="">全部状态</option><option value="active">已启用</option><option value="inactive">已停用</option><option value="pending_configuration">待配置</option></select></label>
        </div>
      </div>
    </section>

    <section class="panel key-context">
      <template v-if="selectableKeys.length">
        <label>当前 Key
          <select v-model="currentKeyID" data-testid="current-key">
            <option v-for="key in selectableKeys" :key="key.id" :value="key.id">
              {{ key.name }} · {{ keyStatusLabel(key.status) }} · {{ key.project.name }}
            </option>
          </select>
        </label>
        <p class="muted">
          模型是否可调用以当前 Key 的审批白名单为准。
          <template v-if="missingKeyCount">有 {{ missingKeyCount }} 个模型不在当前 Key 中，需重新申领 Key。</template>
        </p>
        <p v-if="currentKey && !canReapply && missingKeyCount" class="muted">
          当前 Key 状态为「{{ keyStatusLabel(currentKey.status) }}」，学生端不能直接撤回；请先在「申请密钥」页等待审核，或联系导师停用后再重新申请。
        </p>
      </template>
      <p v-else class="muted">还没有 Key。先到「申请密钥」页申领，模型才会与 Key 白名单同步。</p>
    </section>

    <section class="panel protocol-context">
      <label>准备使用的调用协议
        <select v-model="protocol" data-testid="protocol-select">
          <option v-for="item in protocolOptions" :key="item.value" :value="item.value">
            {{ item.label }}（{{ item.gatewayPath }}）
          </option>
        </select>
      </label>
      <p class="muted">
        每个模型卡片会列出当前可用线路支持的协议。协议不匹配时会提示可用替代协议；请把客户端切到对应协议，网关不会自动转换协议。
      </p>
    </section>

    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="cards" label="正在加载模型" style="margin-top:20px">
      <div v-if="hasAnyModels" class="plaza-layout">
        <section class="plaza-column">
          <div class="plaza-heading">
            <h2>常用模型</h2>
          </div>
          <div v-if="filteredCommon.length" class="plaza-cards">
            <article v-for="model in filteredCommon" :key="model.model_id" class="panel is-interactive">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span class="chip preset">预置</span>
                <span v-if="requiresNewKey(model)" class="chip warn" data-testid="needs-new-key">需重新申领 Key</span>
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="value in protocolsFor(model)" :key="value" class="chip protocol-chip" data-testid="protocol-chip">{{ protocolShortLabel(value) }}</span>
                <span v-if="protocolMismatch(model)" class="chip warn" data-testid="protocol-mismatch">协议不匹配</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p v-if="protocolMismatch(model)" class="protocol-warning" data-testid="protocol-alternatives">{{ protocolMismatch(model) }}</p>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <button v-if="requiresNewKey(model)" class="button secondary" data-testid="reapply-key" @click="openReapply(model)">撤回并重新申请</button>
              </div>
              <Transition name="content-fade">
                <dl v-if="expanded === model.model_id">
                  <dt>模型名</dt><dd>{{ model.model_id }}</dd>
                  <dt>中转厂商</dt><dd>{{ modelProviderLabel(model) }}</dd>
                  <dt>消耗点数</dt><dd>{{ modelCreditsLabel(model) }}</dd>
                  <dt>支持协议</dt><dd>
                    <template v-if="protocolsFor(model).length">
                      <span v-for="(value, index) in protocolsFor(model)" :key="value">{{ index ? '、' : '' }}{{ protocolMeta(value).label }}（{{ protocolMeta(value).gatewayPath }}）</span>
                    </template>
                    <template v-else>协议信息待同步</template>
                  </dd>
                  <dt>能力</dt><dd>{{ model.capabilities.map(capabilityLabel).join('、') || '—' }}</dd>
                  <dt>输入 / 输出</dt><dd>{{ model.input_modalities.join('、') || '—' }} / {{ model.output_modalities.join('、') || '—' }}</dd>
                  <dt>上下文 / 最大输入 / 最大输出</dt><dd>{{ formatTokenK(model.context_window) }} / {{ formatTokenK(model.max_input_tokens) }} / {{ formatTokenK(model.max_output_tokens) }}</dd>
                </dl>
              </Transition>
            </article>
          </div>
          <EmptyState v-else title="暂无常用模型" />
        </section>

        <section class="plaza-column">
          <div class="plaza-heading">
            <h2>我添加的模型</h2>
          </div>
          <div v-if="filteredLibrary.length" class="plaza-cards">
            <article v-for="model in filteredLibrary" :key="model.model_id" class="panel is-interactive">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span v-if="requiresNewKey(model)" class="chip warn" data-testid="needs-new-key">需重新申领 Key</span>
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="value in protocolsFor(model)" :key="value" class="chip protocol-chip" data-testid="protocol-chip">{{ protocolShortLabel(value) }}</span>
                <span v-if="protocolMismatch(model)" class="chip warn" data-testid="protocol-mismatch">协议不匹配</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p v-if="protocolMismatch(model)" class="protocol-warning" data-testid="protocol-alternatives">{{ protocolMismatch(model) }}</p>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <button v-if="requiresNewKey(model)" class="button secondary" data-testid="reapply-key" @click="openReapply(model)">撤回并重新申请</button>
                <button class="button secondary" @click="removeModel(model.model_id)">移除</button>
              </div>
              <Transition name="content-fade">
                <dl v-if="expanded === model.model_id">
                  <dt>模型名</dt><dd>{{ model.model_id }}</dd>
                  <dt>中转厂商</dt><dd>{{ modelProviderLabel(model) }}</dd>
                  <dt>消耗点数</dt><dd>{{ modelCreditsLabel(model) }}</dd>
                  <dt>支持协议</dt><dd>
                    <template v-if="protocolsFor(model).length">
                      <span v-for="(value, index) in protocolsFor(model)" :key="value">{{ index ? '、' : '' }}{{ protocolMeta(value).label }}（{{ protocolMeta(value).gatewayPath }}）</span>
                    </template>
                    <template v-else>协议信息待同步</template>
                  </dd>
                  <dt>能力</dt><dd>{{ model.capabilities.map(capabilityLabel).join('、') || '—' }}</dd>
                  <dt>输入 / 输出</dt><dd>{{ model.input_modalities.join('、') || '—' }} / {{ model.output_modalities.join('、') || '—' }}</dd>
                  <dt>上下文 / 最大输入 / 最大输出</dt><dd>{{ formatTokenK(model.context_window) }} / {{ formatTokenK(model.max_input_tokens) }} / {{ formatTokenK(model.max_output_tokens) }}</dd>
                </dl>
              </Transition>
            </article>
          </div>
          <EmptyState v-else title="还没有自行添加的模型" />
        </section>
      </div>
      <EmptyState v-else title="还没有可用模型" />
    </LoadingRegion>

    <AppDialog :open="addOpen" title="检索添加模型" wide @update:open="addOpen = $event">
      <section>
        <input v-model="catalogQuery" placeholder="搜索模型名" @input="searchCatalog">
        <div class="model-table compact">
          <div class="model-table-head">
            <span>模型名</span>
            <span>中转厂商</span>
            <span>消耗点数</span>
            <span />
          </div>
          <div v-for="item in catalogItems" :key="item.model_id" class="model-table-row">
            <span class="model-name">{{ item.model_id }}</span>
            <span>{{ catalogProviderLabel(item) }}</span>
            <span>{{ catalogCreditsLabel(item) }}</span>
            <button class="button ghost" :disabled="libraryIDs.has(item.model_id.toLowerCase())" @click="addModel(item.model_id)">
              {{ libraryIDs.has(item.model_id.toLowerCase()) ? '已添加' : '添加' }}
            </button>
          </div>
        </div>
        <EmptyState v-if="!catalogItems.length" title="暂无匹配模型" />
        <div class="pagination">
          <button class="button ghost" :disabled="catalogPage <= 1" @click="catalogPage--; searchCatalog()">←</button>
          <span>{{ catalogPage }} / {{ catalogPages }}</span>
          <button class="button ghost" :disabled="catalogPage >= catalogPages" @click="catalogPage++; searchCatalog()">→</button>
        </div>
      </section>
    </AppDialog>

    <AppDialog :open="reapplyOpen" title="撤回并按原审批额度重新申请" @update:open="reapplyOpen = $event">
      <template v-if="reapplyModel && currentKey">
        <p>
          模型 <strong>{{ reapplyModel.model_id }}</strong> 不在当前 Key「{{ currentKey.name }}」中，无法直接用于调用。
        </p>
        <p>
          将撤回当前申请，并按原审批额度 <strong>{{ reapplyQuota }} credits</strong> 重新提交；模型白名单会包含：
          {{ reapplyModels.map(model => model.model_id).join('、') }}。
        </p>
        <p v-if="!canReapply" class="error banner">
          当前 Key 状态为「{{ keyStatusLabel(currentKey.status) }}」，学生端不能直接撤回。请先在「申请密钥」页等待审核，或联系导师停用该 Key 后再重新申请。
        </p>
        <button
          v-if="canReapply"
          class="button primary"
          data-testid="confirm-reapply"
          :disabled="reapplyBusy"
          @click="confirmReapply"
        >
          {{ reapplyBusy ? '正在重新申请…' : '确认撤回并重新申请' }}
        </button>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.plaza-layout { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.plaza-heading { margin-bottom: 16px; }
.plaza-heading h2 { margin: 0; font-size: 18px; }
.plaza-cards { display: grid; gap: 16px; }
.chip.preset { background: #eef3ff; color: #3b5bdb; }
.chip.warn { background: #fff4e5; color: #9a4d00; }
.chip.protocol-chip { background: #eef7f2; color: #1d6b52; }
.protocol-context { display: grid; gap: 10px; margin-top: 16px; }
.protocol-context select { max-width: 520px; }
.protocol-context p { margin: 0; }
.protocol-warning { margin: 8px 0 0; color: #9a4d00; font-size: 12px; line-height: 1.6; }
.key-context { display: grid; gap: 10px; margin-top: 16px; }
.key-context select { max-width: 520px; }
.key-context p { margin: 0; }
.model-table { display: grid; gap: 10px; }
.model-table.compact { margin-top: 12px; }
.model-table-head,
.model-table-row {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) minmax(0, 0.8fr) auto;
  gap: 12px;
  align-items: center;
}
.model-table-head {
  padding: 0 14px;
  font-size: 12px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.model-table-row { padding: 8px 14px; }
.model-name { font-weight: 650; word-break: break-word; }
@media (max-width: 960px) {
  .plaza-layout { grid-template-columns: 1fr; }
  .model-table-head { display: none; }
  .model-table-row {
    grid-template-columns: 1fr;
    gap: 6px;
  }
}
</style>
