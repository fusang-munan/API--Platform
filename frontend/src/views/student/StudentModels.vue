<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, BindingAdapter, Model, ModelCatalogCandidate, ModelCategory, ModelStatus } from '../../api/types'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'
import { capabilityLabel, formatTokenK } from '../../utils/modelCatalog'
import { selectCurrentStudentKey } from '../../utils/studentHarnessConfig'
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
type PlazaKey = Pick<ApiKey, 'id' | 'status' | 'updated_at' | 'name' | 'project'> & { modelIDs: string[] }
type CatalogWithProtocols = ModelCatalogCandidate & {
  supported_protocols?: string[]
  route_ready?: boolean | null
  status?: string | null
}

const commonRows = ref<ModelWithProtocols[]>([])
const libraryRows = ref<ModelWithProtocols[]>([])
const keys = ref<PlazaKey[]>([])
const keysLoaded = ref(false)
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
const syncingModelID = ref('')
const actionError = ref('')
let keyListRevision = 0
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
const selectableKeys = computed(() => keys.value.filter(key => key.status === 'active'))
const currentKey = computed(() => selectableKeys.value.find(key => key.id === currentKeyID.value) ?? null)
const existingKey = computed(() => selectCurrentStudentKey(keys.value))
const currentKeyModelIDs = computed(() => new Set((currentKey.value?.modelIDs ?? []).map(id => id.trim().toLowerCase())))
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

const protocolFriendlyLabels: Record<string, string> = {
  openai_compatible: 'OpenAI Chat',
  openai_responses: 'OpenAI Responses',
  anthropic: 'Anthropic Messages',
  google_gemini_v1beta: 'Gemini 原生',
}

// 字段名与网关 /v1/models 保持一致：protocols、recommended_protocol、availability。
function protocolsFor(model: ModelWithProtocols): string[] {
  const values = new Set((model.protocols ?? model.supported_protocols ?? []).filter(Boolean))
  if (!values.size) {
    if (model.primary_adapter) values.add(model.primary_adapter)
    if (model.responses_ready) values.add('openai_responses')
    if (model.anthropic_ready) values.add('anthropic')
  }
  return BINDING_ADAPTER_OPTIONS.map(item => item.value).filter(value => values.has(value))
}

function hasProtocolInfo(model: ModelWithProtocols) {
  return Array.isArray(model.protocols) || Array.isArray(model.supported_protocols) ||
    Boolean(model.primary_adapter || model.responses_ready || model.anthropic_ready)
}

function protocolShortLabel(value: string) {
  return protocolShortLabels[value] ?? value
}

function protocolMeta(value: string) {
  return bindingAdapterMeta(value as BindingAdapter)
}

function recommendedProtocolLabel(model: ModelWithProtocols) {
  const value = model.recommended_protocol?.trim()
  return value ? protocolFriendlyLabels[value] ?? protocolMeta(value).label : ''
}

type ModelAvailability = { state: string; label: string; available: boolean; reason: string }
function modelAvailability(model: ModelWithProtocols): ModelAvailability {
  const state = model.availability?.trim() ?? ''
  const reason = model.availability_reason?.trim() ?? ''
  if (state === 'available') return { state, label: '可用', available: true, reason: '' }
  if (state === 'maintenance') return { state, label: '维护中', available: false, reason: reason || '维护中，等待老师恢复线路。' }
  if (state === 'unverified') return { state, label: '待验证', available: false, reason: reason || '已有绑定线路，当前还没有通过探活的协议。' }
  if (state === 'unavailable') return { state, label: '不可用', available: false, reason: reason || '当前不可用，等待老师开放。' }
  if (state === 'not_published') return { state, label: '未发布', available: false, reason: reason || '老师还未发布该模型。' }
  if (model.status !== 'active') return { state: 'unavailable', label: '维护中', available: false, reason: '老师已停用该模型，等待老师恢复后才会重新开放。' }
  if (!model.route_ready) return { state: 'maintenance', label: '维护中', available: false, reason: '暂无可用线路，等待老师恢复线路。' }
  if (hasProtocolInfo(model) && !protocolsFor(model).length) {
    return { state: 'unavailable', label: '缺协议', available: false, reason: '当前没有可用协议，等待老师核对线路。' }
  }
  if (model.credit_multiplier == null || model.credit_multiplier.trim() === '' || Number(model.credit_multiplier) <= 0) {
    return { state: 'unavailable', label: '缺价', available: false, reason: '报价尚未配置，等待老师补上学校倍率后才能调用。' }
  }
  return { state: 'available', label: '可用', available: true, reason: '' }
}
function modelUnavailableReason(model: ModelWithProtocols) {
  return modelAvailability(model).reason
}

function keyHasModel(modelID: string) {
  return currentKeyModelIDs.value.has(modelID.trim().toLowerCase())
}

function rawCatalogValue(item: CatalogWithProtocols, key: string) {
  const direct = (item as unknown as Record<string, unknown>)[key]
  if (direct !== undefined && direct !== null) return direct
  return item.raw_entry?.[key]
}

function catalogKnownModel(item: CatalogWithProtocols) {
  return allRows.value.find(model => model.model_id.toLowerCase() === item.model_id.toLowerCase()) ?? null
}

function catalogSupportedProtocols(item: CatalogWithProtocols) {
  const values = new Set<string>()
  const direct = rawCatalogValue(item, 'supported_protocols')
  const rawProtocols = rawCatalogValue(item, 'protocols')
  for (const list of [direct, rawProtocols]) {
    if (Array.isArray(list)) list.forEach(value => { if (typeof value === 'string' && value) values.add(value) })
  }
  if (!values.size) {
    const known = catalogKnownModel(item)
    if (known) protocolsFor(known).forEach(value => values.add(value))
  }
  return BINDING_ADAPTER_OPTIONS.map(option => option.value as string).filter(value => values.has(value))
}

function catalogProtocols(item: CatalogWithProtocols) {
  return catalogSupportedProtocols(item)
}

function catalogRecommendedLabel(item: CatalogWithProtocols) {
  const known = catalogKnownModel(item)
  const raw = rawCatalogValue(item, 'recommended_protocol')
  const value = typeof raw === 'string' && raw ? raw : (known?.recommended_protocol ?? '')
  return value ? protocolFriendlyLabels[value] ?? protocolMeta(value).label : ''
}

function catalogAvailability(item: CatalogWithProtocols) {
  const known = catalogKnownModel(item)
  const rawAvailability = rawCatalogValue(item, 'availability')
  const availability = typeof rawAvailability === 'string' ? rawAvailability : (known?.availability ?? '')
  if (availability) {
    if (availability === 'available') return { available: true, reason: '' }
    const rawReason = rawCatalogValue(item, 'availability_reason')
    const reason = typeof rawReason === 'string' && rawReason ? rawReason : (known?.availability_reason ?? '')
    return { available: false, reason: reason || '当前不可用，等待老师开放。' }
  }
  const rawStatus = rawCatalogValue(item, 'status')
  const status = typeof rawStatus === 'string' ? rawStatus : (known?.status ?? '')
  if (status && status !== 'active') {
    return {
      available: false,
      reason: status === 'inactive' ? '老师已停用该模型，等待老师恢复。' : '老师还未完成该模型的配置。',
    }
  }
  const rawRouteReady = rawCatalogValue(item, 'route_ready')
  const routeReady = rawRouteReady === undefined ? known?.route_ready : rawRouteReady
  if (routeReady === false) return { available: false, reason: '暂无可用线路，等待老师恢复。' }
  const rawCredits = rawCatalogValue(item, 'credit_multiplier')
  const credits = rawCredits ?? known?.credit_multiplier
  if (credits != null && credits !== '' && Number(credits) <= 0) {
    return { available: false, reason: '报价未配置，等待老师补充。' }
  }
  return { available: true, reason: '' }
}

function catalogCoverage(item: CatalogWithProtocols) {
  if (!keysLoaded.value) return { label: '状态待加载', tone: 'warn' }
  if (!currentKey.value) return { label: '暂无生效 Key', tone: 'warn' }
  return keyHasModel(item.model_id) ? { label: '已同步', tone: 'ok' } : { label: '待同步', tone: 'warn' }
}

function canAddCatalog(item: CatalogWithProtocols) {
  if (!keysLoaded.value || syncingModelID.value || !catalogAvailability(item).available) return false
  if (!libraryIDs.value.has(item.model_id.toLowerCase())) return true
  return Boolean(currentKey.value && !keyHasModel(item.model_id))
}

function catalogAddLabel(item: CatalogWithProtocols) {
  if (syncingModelID.value === item.model_id) return '正在添加…'
  if (!catalogAvailability(item).available) return '暂不可用'
  if (currentKey.value && !keyHasModel(item.model_id)) return '添加到我的 Key'
  return libraryIDs.value.has(item.model_id.toLowerCase()) ? '已添加' : '添加到广场'
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
  const revision = ++keyListRevision
  await loadState.run(async () => {
    try {
      const [plaza, keyList] = await Promise.all([studentAPI.models(), studentAPI.keys()])
      if (revision !== keyListRevision) return
      commonRows.value = plaza.common_models
      libraryRows.value = plaza.library_models
      keys.value = keyList.map(key => ({
        id: key.id, status: key.status, name: key.name, project: key.project, updated_at: key.updated_at,
        modelIDs: (key.models ?? []).map(model => model.model_id),
      }))
      keysLoaded.value = true
      if (!currentKey.value) {
        currentKeyID.value = selectCurrentStudentKey(selectableKeys.value)?.id ?? ''
      }
      error.value = ''
    } catch (caught) {
      if (revision === keyListRevision) error.value = caught instanceof APIError ? caught.message : '加载模型和 Key 失败'
    }
  })
}

async function openAdd() {
  addOpen.value = true
  actionError.value = ''
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
  if (syncingModelID.value || !keysLoaded.value) return
  const key = currentKey.value
  const syncToKey = key && !keyHasModel(modelID)
  let addedToPlaza = false
  syncingModelID.value = modelID
  actionError.value = ''
  try {
    if (!libraryIDs.value.has(modelID.toLowerCase())) {
      const model = await studentAPI.addModel(modelID)
      ++keyListRevision
      if (!libraryIDs.value.has(model.model_id.toLowerCase())) {
        libraryRows.value = sortModelsByRecency([...libraryRows.value, model])
      }
      addedToPlaza = true
    }
    if (syncToKey) {
      const result = await studentAPI.addKeyModels(key.id, [modelID])
      ++keyListRevision
      const updatedKey = keys.value.find(item => item.id === key.id)
      if (updatedKey) updatedKey.modelIDs = result.model_ids
      toast.success('模型已同步到我的 Key')
    } else {
      toast.success('已加入我的模型广场')
    }
  } catch (caught) {
    const message = caught instanceof APIError ? caught.message : '添加模型失败，请稍后重试'
    actionError.value = addedToPlaza ? `模型已加入广场，Key 同步失败。${message}` : message
  } finally {
    syncingModelID.value = ''
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
    <p v-if="actionError && !addOpen" class="error banner" role="alert">{{ actionError }}</p>
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
      <template v-if="currentKey">
        <label>当前 Key
          <select v-model="currentKeyID" :disabled="Boolean(syncingModelID)" data-testid="current-key">
            <option v-for="key in selectableKeys" :key="key.id" :value="key.id">
              {{ key.name }} · {{ keyStatusLabel(key.status) }} · {{ key.project.name }}
            </option>
          </select>
        </label>
        <p class="muted">添加成功后，当前 Key 可立即调用这个模型。</p>
      </template>
      <template v-else-if="keysLoaded">
        <p v-if="existingKey" class="muted">「{{ existingKey.name }}」{{ keyStatusLabel(existingKey.status) }}。Key 生效后可添加模型。</p>
        <p v-else class="muted">还没有生效的 Key。可以先把模型加入广场，在申请时选用。</p>
        <div><RouterLink class="button secondary" :to="{ name: 'student-api-keys' }" data-testid="apply-key">
          {{ existingKey ? '查看我的申请' : '申请密钥' }}
        </RouterLink></div>
      </template>
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
            <article v-for="model in filteredCommon" :key="model.model_id" class="panel is-interactive" :data-model="model.model_id">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span class="chip preset">预置</span>
                <span class="chip" :class="modelAvailability(model).available ? 'ok' : 'unavailable'" data-testid="model-availability">{{ modelAvailability(model).label }}</span>
                <span v-if="keyHasModel(model.model_id)" class="chip ok" data-testid="model-synced">已同步</span>
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="value in protocolsFor(model)" :key="value" class="chip protocol-chip" data-testid="protocol-chip">{{ protocolShortLabel(value) }}</span>
                <span v-if="recommendedProtocolLabel(model)" class="chip recommend" data-testid="recommended-protocol">推荐 {{ recommendedProtocolLabel(model) }}</span>
                <span v-if="protocolMismatch(model)" class="chip warn" data-testid="protocol-mismatch">协议不匹配</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p class="protocol-note" data-testid="protocol-note">支持协议与推荐协议由平台线路实时返回；网关不做协议转换，客户端必须直接使用卡片上列出的协议之一。</p>
              <p v-if="modelUnavailableReason(model)" class="protocol-warning" data-testid="model-unavailable-reason">{{ modelUnavailableReason(model) }}</p>
              <p v-if="protocolMismatch(model)" class="protocol-warning" data-testid="protocol-alternatives">{{ protocolMismatch(model) }}</p>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <button v-if="currentKey && !keyHasModel(model.model_id)" class="button secondary" data-testid="sync-key-model"
                  :disabled="Boolean(syncingModelID || !modelAvailability(model).available)" @click="addModel(model.model_id)">
                  {{ syncingModelID === model.model_id ? '正在同步…' : '添加到我的 Key' }}
                </button>
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
                  <dt>推荐协议</dt><dd>{{ recommendedProtocolLabel(model) || '暂无推荐，请以支持协议为准' }}（网关不做协议转换）</dd>
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
            <article v-for="model in filteredLibrary" :key="model.model_id" class="panel is-interactive" :data-model="model.model_id">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span class="chip" :class="modelAvailability(model).available ? 'ok' : 'unavailable'" data-testid="model-availability">{{ modelAvailability(model).label }}</span>
                <span v-if="keyHasModel(model.model_id)" class="chip ok" data-testid="model-synced">已同步</span>
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="value in protocolsFor(model)" :key="value" class="chip protocol-chip" data-testid="protocol-chip">{{ protocolShortLabel(value) }}</span>
                <span v-if="recommendedProtocolLabel(model)" class="chip recommend" data-testid="recommended-protocol">推荐 {{ recommendedProtocolLabel(model) }}</span>
                <span v-if="protocolMismatch(model)" class="chip warn" data-testid="protocol-mismatch">协议不匹配</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p class="protocol-note" data-testid="protocol-note">支持协议与推荐协议由平台线路实时返回；网关不做协议转换，客户端必须直接使用卡片上列出的协议之一。</p>
              <p v-if="modelUnavailableReason(model)" class="protocol-warning" data-testid="model-unavailable-reason">{{ modelUnavailableReason(model) }}</p>
              <p v-if="protocolMismatch(model)" class="protocol-warning" data-testid="protocol-alternatives">{{ protocolMismatch(model) }}</p>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <button v-if="currentKey && !keyHasModel(model.model_id)" class="button secondary" data-testid="sync-key-model"
                  :disabled="Boolean(syncingModelID || !modelAvailability(model).available)" @click="addModel(model.model_id)">
                  {{ syncingModelID === model.model_id ? '正在同步…' : '添加到我的 Key' }}
                </button>
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
                  <dt>推荐协议</dt><dd>{{ recommendedProtocolLabel(model) || '暂无推荐，请以支持协议为准' }}（网关不做协议转换）</dd>
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
        <p class="muted catalog-note" data-testid="catalog-protocol-note">
          协议标签与推荐协议读取平台线路返回的能力信息；缺少时显示「协议待同步」，不再按模型名推测。网关不做协议转换，客户端必须直接使用对应协议。
        </p>
        <p v-if="currentKey" class="muted catalog-note" data-testid="catalog-key-note">
          当前 Key 为「{{ currentKey.name }}」。添加成功后会立即同步。
        </p>
        <p v-else-if="keysLoaded" class="muted catalog-note">
          可以先把模型加到广场。
          <RouterLink :to="{ name: 'student-api-keys' }">{{ existingKey ? '查看我的申请' : '申请密钥' }}</RouterLink>
        </p>
        <p v-if="actionError" class="error banner" role="alert">{{ actionError }}</p>
        <input v-model="catalogQuery" placeholder="搜索模型名" @input="searchCatalog">
        <div class="model-table compact">
          <div class="model-table-head">
            <span>模型名</span>
            <span>中转厂商</span>
            <span>消耗点数</span>
            <span>协议</span>
            <span>当前 Key</span>
            <span />
          </div>
          <div v-for="item in catalogItems" :key="item.model_id" class="model-table-row">
            <span class="model-name">{{ item.model_id }}</span>
            <span>{{ catalogProviderLabel(item) }}</span>
            <span>{{ catalogCreditsLabel(item) }}</span>
            <span class="catalog-protocols">
              <span v-for="value in catalogProtocols(item)" :key="value" class="chip protocol-chip" data-testid="catalog-protocol-chip">{{ protocolShortLabel(value) }}</span>
              <small v-if="catalogRecommendedLabel(item)" class="muted" data-testid="catalog-recommendation">推荐 {{ catalogRecommendedLabel(item) }}</small>
              <small v-if="!catalogProtocols(item).length" class="muted">协议待同步</small>
            </span>
            <span>
              <span class="chip" :class="catalogCoverage(item).tone" data-testid="catalog-key-coverage">{{ catalogCoverage(item).label }}</span>
              <small v-if="!catalogAvailability(item).available" class="muted catalog-unavailable" data-testid="catalog-unavailable">{{ catalogAvailability(item).reason }}</small>
            </span>
            <button class="button ghost" :disabled="!canAddCatalog(item)" :title="catalogAvailability(item).reason" data-testid="catalog-add" @click="addModel(item.model_id)">
              {{ catalogAddLabel(item) }}
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

  </section>
</template>

<style scoped>
.plaza-layout { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.plaza-heading { margin-bottom: 16px; }
.plaza-heading h2 { margin: 0; font-size: 18px; }
.plaza-cards { display: grid; gap: 16px; }
.chip.preset { background: #eef3ff; color: #3b5bdb; }
.chip.warn { background: #fff4e5; color: #9a4d00; }
.chip.unavailable { background: #fdecec; color: #a12828; }
.chip.recommend { background: #f3efff; color: #5b3ea8; }
.chip.ok { background: #e8f7ee; color: #1d6b3a; }
.chip.protocol-chip { background: #eef7f2; color: #1d6b52; }
.protocol-context { display: grid; gap: 10px; margin-top: 16px; }
.protocol-context select { max-width: 520px; }
.protocol-context p { margin: 0; }
.protocol-warning { margin: 8px 0 0; color: #9a4d00; font-size: 12px; line-height: 1.6; }
.protocol-note { margin: 8px 0 0; color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.catalog-note { margin: 0 0 10px; }
.catalog-protocols { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; min-width: 0; }
.catalog-protocols small { flex-basis: 100%; }
.catalog-unavailable { display: block; margin-top: 4px; }
.key-context { display: grid; gap: 10px; margin-top: 16px; }
.key-context select { max-width: 520px; }
.key-context p { margin: 0; }
.model-table { display: grid; gap: 10px; }
.model-table.compact { margin-top: 12px; }
.model-table-head,
.model-table-row {
  display: grid;
  grid-template-columns: minmax(0, 1.3fr) minmax(0, 0.8fr) minmax(0, 0.7fr) minmax(0, 1.2fr) minmax(0, 1fr) auto;
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
