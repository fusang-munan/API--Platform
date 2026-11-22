<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, BindingAdapter, Model, ModelCatalogCandidate, ModelCategory, ModelStatus, StudentUsage } from '../../api/types'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'
import { capabilityLabel, formatTokenK } from '../../utils/modelCatalog'
import { selectCurrentStudentKey } from '../../utils/studentHarnessConfig'
import { sortModelsByRecency } from '../../utils/modelSort'
import { filterStudentModels } from '../../utils/studentModels'
import { catalogProviderLabel, modelDescriptionText, modelProviderLabel } from '../../utils/modelPlazaDisplay'
import { formatCredits } from '../../utils/credits'
import { calculateCredits } from '../../utils/format'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { APIError } from '../../api/client'

type ModelWithProtocols = Model & { supported_protocols?: string[] }
type PlazaKey = Pick<ApiKey, 'id' | 'status' | 'updated_at' | 'name' | 'project'> & { models: ModelWithProtocols[] }
type CatalogWithProtocols = ModelCatalogCandidate & {
  supported_protocols?: string[]
  route_ready?: boolean | null
  status?: string | null
  availability?: string | null
  availability_reason?: string | null
  protocols?: string[]
  probe_state?: string | null
}
type KeyModelMutation = {
  model_ids?: string[]
  added_model_ids?: string[]
  removed_model_ids?: string[]
  warnings?: Array<string | { code?: string; message?: string; model_id?: string }>
}
type ModelAvailability = { state: string; label: string; available: boolean; addable: boolean; reason: string }
type KeyCoverage = { inKey: boolean; label: string; tone: 'ok' | 'warn' | 'unavailable' | 'muted' }

const commonRows = ref<ModelWithProtocols[]>([])
const libraryRows = ref<ModelWithProtocols[]>([])
const keys = ref<PlazaKey[]>([])
const keysLoaded = ref(false)
const currentKeyID = ref('')
const catalogItems = ref<ModelCatalogCandidate[]>([])
const usage = ref<StudentUsage | null>(null)
const usageLoaded = ref(false)
const usageFailed = ref(false)
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
const removingModelID = ref('')
const actionError = ref('')
const actionNotice = ref('')
let keyListRevision = 0
const loadState = useLoadState()
const toast = useToast()

function modelKey(modelID: string) {
  return modelID.trim().toLowerCase()
}

function uniqueModels(lists: ModelWithProtocols[][]) {
  const seen = new Set<string>()
  const result: ModelWithProtocols[] = []
  for (const list of lists) {
    for (const model of list) {
      const key = modelKey(model.model_id)
      if (!key || seen.has(key)) continue
      seen.add(key)
      result.push(model)
    }
  }
  return result
}

const allRows = computed(() => uniqueModels([
  commonRows.value,
  libraryRows.value,
  ...keys.value.map(key => key.models ?? []),
]))
const filteredModels = computed(() => sortModelsByRecency(
  filterStudentModels(allRows.value, query.value, category.value, status.value),
) as ModelWithProtocols[])
const hasAnyModels = computed(() => filteredModels.value.length > 0)
const catalogPages = computed(() => Math.max(1, Math.ceil(catalogTotal.value / 20)))
const selectableKeys = computed(() => keys.value.filter(key => key.status === 'active'))
const currentKey = computed(() => selectableKeys.value.find(key => key.id === currentKeyID.value) ?? null)
const existingKey = computed(() => selectCurrentStudentKey(keys.value))
const currentKeyModelIDs = computed(() => new Set((currentKey.value?.models ?? []).map(model => modelKey(model.model_id))))
const currentKeyUsage = computed(() => {
  for (const project of usage.value?.projects ?? []) {
    const match = (project.keys ?? []).find(key => key.id === currentKeyID.value)
    if (match) return { key: match, project }
  }
  return null
})
const availableCredits = computed(() => {
  const keyAvailable = currentKeyUsage.value?.key.available
  if (typeof keyAvailable === 'string' && keyAvailable.trim() !== '') return keyAvailable.trim()
  const projects = usage.value?.projects ?? []
  if (!projects.length) return null
  let total = '0.000'
  for (const project of projects) {
    const raw = project.available?.trim()
    if (!raw || !/^\d+(?:\.\d+)?$/.test(raw)) return null
    total = calculateCredits(total, raw, 'add')
  }
  return total
})
const balanceText = computed(() => usageLoaded.value && availableCredits.value != null
  ? `${formatCredits(availableCredits.value)} 积分`
  : '—')
const usedCreditsByModel = computed(() => {
  const totals = new Map<string, string>()
  for (const project of usage.value?.projects ?? []) {
    for (const key of project.keys ?? []) {
      for (const model of key.models ?? []) {
        const modelID = modelKey(model.id || model.name || '')
        const credits = String(model.credits ?? '').trim()
        if (!modelID || !/^\d+(?:\.\d+)?$/.test(credits)) continue
        const previous = totals.get(modelID)
        totals.set(modelID, previous ? calculateCredits(previous, credits, 'add') : credits)
      }
    }
  }
  return totals
})
const protocolOptions = BINDING_ADAPTER_OPTIONS.filter(item =>
  ['openai_compatible', 'openai_responses', 'anthropic', 'google_gemini_v1beta'].includes(item.value))
const selectedProtocolLabel = computed(() => protocol.value ? protocolMeta(protocol.value).label : '')
const busy = computed(() => Boolean(syncingModelID.value || removingModelID.value))

function modelUsageLabel(modelID: string) {
  if (usageFailed.value) return '用量暂不可用'
  if (!usageLoaded.value) return '用量读取中…'
  const credits = usedCreditsByModel.value.get(modelKey(modelID))
  if (!credits || Number(credits) <= 0) return '本月未使用'
  return `本月已用 ${formatCredits(credits)} 积分`
}

function studentSafeReason(reason: string, fallback: string) {
  if (!reason) return fallback
  return /(价格|报价|单价|倍率|官网|反推|基础积分|折后|加价|price|quote|cost|multiplier)/i.test(reason)
    ? fallback
    : reason
}

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

function hasConfiguredPrice(model: { credit_multiplier?: string | number | null }) {
  if (model.credit_multiplier == null) return false
  const value = String(model.credit_multiplier).trim()
  return value !== '' && Number(value) > 0
}

function modelAvailability(model: ModelWithProtocols): ModelAvailability {
  const state = model.availability?.trim() ?? ''
  const reason = model.availability_reason?.trim() ?? ''
  if (state === 'available') {
    if (!hasConfiguredPrice(model)) {
      return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
    }
    return { state, label: '可用', available: true, addable: true, reason: '' }
  }
  if (state === 'unverified') {
    if (!hasConfiguredPrice(model)) {
      return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
    }
    return {
      state, label: '待验证', available: false, addable: true,
      reason: studentSafeReason(reason, '线路尚未完成探活；可以先加入当前 Key，但调用可能失败。'),
    }
  }
  if (state === 'maintenance') {
    return { state, label: '维护中', available: false, addable: false, reason: studentSafeReason(reason, '维护中，等待老师恢复线路。') }
  }
  if (state === 'unavailable') {
    return { state, label: '不可用', available: false, addable: false, reason: studentSafeReason(reason, '当前不可用，等待老师开放。') }
  }
  if (state === 'not_published') {
    return { state, label: '未发布', available: false, addable: false, reason: studentSafeReason(reason, '老师还未发布该模型。') }
  }
  if (model.status !== 'active') {
    return { state: 'maintenance', label: '维护中', available: false, addable: false, reason: '老师已停用该模型，等待老师恢复后才会重新开放。' }
  }
  if (!model.route_ready) {
    return { state: 'maintenance', label: '维护中', available: false, addable: false, reason: '暂无可用线路，等待老师恢复线路。' }
  }
  if (hasProtocolInfo(model) && !protocolsFor(model).length) {
    return { state: 'unverified', label: '待验证', available: false, addable: true, reason: '已有线路但还没有通过探活的协议；可以加入当前 Key，但调用可能失败。' }
  }
  if (!hasConfiguredPrice(model)) {
    return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
  }
  return { state: 'available', label: '可用', available: true, addable: true, reason: '' }
}

function modelUnavailableReason(model: ModelWithProtocols) {
  return modelAvailability(model).reason
}

function availabilityTone(model: ModelWithProtocols) {
  const state = modelAvailability(model).state
  if (state === 'available') return 'ok'
  if (state === 'unverified') return 'warn'
  return 'unavailable'
}

function keyHasModel(modelID: string) {
  return currentKeyModelIDs.value.has(modelKey(modelID))
}

function keyCoverage(model: ModelWithProtocols): KeyCoverage {
  if (!keysLoaded.value) return { inKey: false, label: '状态待加载', tone: 'muted' }
  if (!currentKey.value) return { inKey: false, label: '未加入', tone: 'muted' }
  if (!keyHasModel(model.model_id)) return { inKey: false, label: '未加入', tone: 'muted' }
  const state = modelAvailability(model).state
  if (state === 'available') return { inKey: true, label: '可调用', tone: 'ok' }
  if (state === 'unverified') return { inKey: true, label: '已加入 · 待验证', tone: 'warn' }
  return { inKey: true, label: '已加入 · 维护中', tone: 'unavailable' }
}

function rawCatalogValue(item: CatalogWithProtocols, key: string) {
  const direct = (item as unknown as Record<string, unknown>)[key]
  if (direct !== undefined && direct !== null) return direct
  return item.raw_entry?.[key]
}

function catalogKnownModel(item: CatalogWithProtocols) {
  return allRows.value.find(model => modelKey(model.model_id) === modelKey(item.model_id)) ?? null
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

function catalogAvailability(item: CatalogWithProtocols): ModelAvailability {
  const known = catalogKnownModel(item)
  const rawAvailability = rawCatalogValue(item, 'availability')
  const availability = typeof rawAvailability === 'string' ? rawAvailability : (known?.availability ?? '')
  if (availability) {
    const state = availability
    const rawReason = rawCatalogValue(item, 'availability_reason')
    const reason = typeof rawReason === 'string' && rawReason ? rawReason : (known?.availability_reason ?? '')
    if (state === 'maintenance') {
      return { state, label: '维护中', available: false, addable: false, reason: studentSafeReason(reason, '维护中，等待老师恢复线路。') }
    }
    if (state === 'unavailable' || state === 'not_published') {
      return { state, label: '不可用', available: false, addable: false, reason: studentSafeReason(reason, '当前不可用，等待老师开放。') }
    }
    const itemPrice = known?.credit_multiplier ?? item.credit_multiplier
    if (!hasConfiguredPrice({ credit_multiplier: itemPrice ?? null })) {
      return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
    }
    if (state === 'unverified') {
      return {
        state, label: '待验证', available: false, addable: true,
        reason: studentSafeReason(reason, '线路尚未完成探活；可以先加入当前 Key，但调用可能失败。'),
      }
    }
    return { state, label: '可用', available: true, addable: true, reason: '' }
  }
  const rawStatus = rawCatalogValue(item, 'status')
  const itemStatus = typeof rawStatus === 'string' ? rawStatus : (known?.status ?? '')
  if (itemStatus && itemStatus !== 'active') {
    return {
      state: 'maintenance', label: '维护中', available: false, addable: false,
      reason: itemStatus === 'inactive' ? '老师已停用该模型，等待老师恢复。' : '老师还未完成该模型的配置。',
    }
  }
  const rawRouteReady = rawCatalogValue(item, 'route_ready')
  const routeReady = rawRouteReady === undefined ? known?.route_ready : rawRouteReady
  if (routeReady === false) {
    return { state: 'maintenance', label: '维护中', available: false, addable: false, reason: '暂无可用线路，等待老师恢复。' }
  }
  const rawCredits = rawCatalogValue(item, 'credit_multiplier')
  const credits = (rawCredits ?? known?.credit_multiplier) as string | number | null | undefined
  if (!hasConfiguredPrice({ credit_multiplier: credits ?? null })) {
    return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
  }
  return { state: 'available', label: '可用', available: true, addable: true, reason: '' }
}

function catalogCoverage(item: CatalogWithProtocols): KeyCoverage {
  if (!keysLoaded.value) return { inKey: false, label: '状态待加载', tone: 'muted' }
  if (!currentKey.value) return { inKey: false, label: '未加入', tone: 'muted' }
  if (!keyHasModel(item.model_id)) return { inKey: false, label: '未加入', tone: 'muted' }
  const state = catalogAvailability(item).state
  if (state === 'available') return { inKey: true, label: '可调用', tone: 'ok' }
  if (state === 'unverified') return { inKey: true, label: '已加入 · 待验证', tone: 'warn' }
  return { inKey: true, label: '已加入 · 维护中', tone: 'unavailable' }
}

function canAddCatalog(item: CatalogWithProtocols) {
  if (!keysLoaded.value || busy.value || !currentKey.value || keyHasModel(item.model_id)) return false
  return catalogAvailability(item).addable
}

function canAddModel(model: ModelWithProtocols) {
  return Boolean(currentKey.value && !busy.value && !keyHasModel(model.model_id) && modelAvailability(model).addable)
}

function addModelLabel(model: ModelWithProtocols) {
  if (syncingModelID.value === model.model_id) return '正在添加…'
  if (keyHasModel(model.model_id)) return keyCoverage(model).label
  if (modelAvailability(model).state === 'maintenance') return '维护中，暂不能加入'
  return '添加到我的 Key'
}

function catalogAddLabel(item: CatalogWithProtocols) {
  if (syncingModelID.value === item.model_id) return '正在添加…'
  if (keyHasModel(item.model_id)) return catalogCoverage(item).label
  if (catalogAvailability(item).state === 'maintenance') return '维护中，暂不能加入'
  return catalogAvailability(item).addable ? '添加到我的 Key' : '暂不可加入'
}

function protocolMismatch(model: ModelWithProtocols) {
  if (!protocol.value) return ''
  const supported = protocolsFor(model)
  if (!supported.length || supported.includes(protocol.value)) return ''
  const alternatives = supported.map(value => protocolMeta(value).label)
  return `不支持 ${selectedProtocolLabel.value}；可用替代协议：${alternatives.join('、')}。请在客户端切换协议，网关不做协议转换。`
}

function syncKeyQuery(id: string) {
  if (typeof window === 'undefined') return
  try {
    const url = new URL(window.location.href)
    if (id) url.searchParams.set('key', id)
    else url.searchParams.delete('key')
    if (url.toString() !== window.location.href) window.history.replaceState(window.history.state, '', url.toString())
  } catch {
    // 测试或受限环境没有完整 Location 时，不影响页面内的 Key 选择。
  }
}

function keyFromQuery() {
  try {
    return new URLSearchParams(window.location.search).get('key')?.trim() ?? ''
  } catch {
    return ''
  }
}

function onKeyChange() {
  actionError.value = ''
  actionNotice.value = ''
  syncKeyQuery(currentKeyID.value)
}

watch(catalogQuery, () => { catalogPage.value = 1 })

async function load() {
  const revision = ++keyListRevision
  await loadState.run(async () => {
    try {
      const usageRequest = typeof studentAPI.usage === 'function'
        ? studentAPI.usage()
          .then(payload => {
            if (revision !== keyListRevision) return
            usage.value = payload
            usageLoaded.value = true
            usageFailed.value = false
          })
          .catch(() => {
            if (revision !== keyListRevision) return
            usage.value = null
            usageLoaded.value = false
            usageFailed.value = true
          })
        : Promise.resolve()
      const [plaza, keyList] = await Promise.all([studentAPI.models(), studentAPI.keys(), usageRequest])
      if (revision !== keyListRevision) return
      commonRows.value = plaza.common_models
      libraryRows.value = plaza.library_models
      keys.value = keyList.map(key => ({
        id: key.id, status: key.status, name: key.name, project: key.project, updated_at: key.updated_at,
        models: (key.models ?? []) as ModelWithProtocols[],
      }))
      keysLoaded.value = true
      const requested = keyFromQuery()
      const selected = selectableKeys.value.find(key => key.id === requested) ?? selectCurrentStudentKey(selectableKeys.value)
      currentKeyID.value = selected?.id ?? ''
      syncKeyQuery(currentKeyID.value)
      error.value = ''
    } catch (caught) {
      if (revision === keyListRevision) error.value = caught instanceof APIError ? caught.message : '加载模型和 Key 失败'
    }
  })
}

async function openAdd() {
  addOpen.value = true
  actionError.value = ''
  actionNotice.value = ''
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

function fallbackModelFromCatalog(item: CatalogWithProtocols): ModelWithProtocols {
  return {
    model_id: item.model_id,
    display_name: item.display_name ?? item.model_id,
    description: item.description ?? null,
    category: 'text',
    capabilities: [],
    input_modalities: ['text'],
    output_modalities: ['text'],
    is_common: false,
    status: 'active',
    route_ready: true,
    credit_multiplier: item.credit_multiplier ?? null,
    provider_name: item.provider_name,
    protocols: item.protocols,
    supported_protocols: item.supported_protocols,
    recommended_protocol: item.recommended_protocol,
    availability: item.availability,
    availability_reason: item.availability_reason,
  }
}

function findModelForMutation(modelID: string, catalogItem?: CatalogWithProtocols) {
  return allRows.value.find(model => modelKey(model.model_id) === modelKey(modelID))
    ?? (catalogItem ? fallbackModelFromCatalog(catalogItem) : null)
}

function warningsText(result: KeyModelMutation) {
  const warnings = result.warnings ?? []
  return warnings.map(item => {
    if (typeof item === 'string') return studentSafeReason(item, '')
    const code = (item.code ?? '').toLowerCase()
    if (/unverified|probe/.test(code)) return studentSafeReason(item.message ?? '', '该模型尚未通过探活，已加入当前 Key，但调用可能失败。')
    if (/price|quote/.test(code)) return '该模型暂未开放。'
    if (/maintenance/.test(code)) return studentSafeReason(item.message ?? '', '该模型正在维护，暂不可调用。')
    return studentSafeReason(item.message ?? '', item.code || item.model_id || '')
  }).filter(Boolean).join('；')
}

function keyModelFailureMessage(caught: unknown, modelID: string) {
  if (!(caught instanceof APIError)) return '加入当前 Key 失败，请稍后重试'
  const raw = `${caught.serverMessage} ${caught.code}`.toLowerCase()
  if (/price|quote|报价/.test(raw)) return `「${modelID}」暂未开放，暂时不能加入当前 Key。`
  if (/maintenance|维护|停用|不可用/.test(raw)) {
    return `「${modelID}」当前不能加入：${studentSafeReason(caught.serverMessage, '请等待老师恢复模型。')}`
  }
  if (/unverified|not verified|probe|还没有通过探活|尚未.*探活/.test(raw)) {
    return `「${modelID}」还没有通过探活，暂时不能加入当前 Key。`
  }
  if (/no usable route|no route|route.*(missing|unavailable)|无可用线路/.test(raw)) {
    return `「${modelID}」暂时没有可用线路，请等待老师恢复线路。`
  }
  return studentSafeReason(caught.message, '加入当前 Key 失败，请稍后重试')
}

async function addModel(modelID: string, catalogItem?: CatalogWithProtocols) {
  if (busy.value || !keysLoaded.value || !currentKey.value) return
  const key = currentKey.value
  if (keyHasModel(modelID)) return
  const known = findModelForMutation(modelID, catalogItem)
  if (known && !modelAvailability(known).addable) {
    actionError.value = `${known.model_id} 当前不可加入：${modelAvailability(known).reason}`
    return
  }
  syncingModelID.value = modelID
  actionError.value = ''
  actionNotice.value = ''
  try {
    const result = await studentAPI.addKeyModels(key.id, [modelID]) as KeyModelMutation
    ++keyListRevision
    const model = known ?? findModelForMutation(modelID)
    if (model && !key.models.some(item => modelKey(item.model_id) === modelKey(model.model_id))) {
      key.models = [...key.models, model]
    }
    const warning = warningsText(result)
    if (warning) actionNotice.value = warning
    else if (model && modelAvailability(model).state === 'unverified') {
      actionNotice.value = '已加入当前 Key；该模型尚未通过探活，调用可能失败，等待老师验证后再使用。'
    }
    toast.success('已加入当前 Key')
  } catch (caught) {
    actionError.value = keyModelFailureMessage(caught, modelID)
  } finally {
    syncingModelID.value = ''
  }
}

async function removeModel(modelID: string) {
  if (busy.value || !currentKey.value) return
  const key = currentKey.value
  if (!keyHasModel(modelID)) return
  if (!confirm(`从当前 Key「${key.name}」移除 ${modelID}？`)) return
  removingModelID.value = modelID
  actionError.value = ''
  actionNotice.value = ''
  try {
    const result = await studentAPI.removeKeyModels(key.id, [modelID]) as KeyModelMutation
    ++keyListRevision
    key.models = key.models.filter(model => modelKey(model.model_id) !== modelKey(modelID))
    const warning = warningsText(result)
    if (warning) actionNotice.value = warning
    toast.success('已从当前 Key 移除')
  } catch (caught) {
    actionError.value = caught instanceof APIError ? caught.message : '从当前 Key 移除失败，请稍后重试'
  } finally {
    removingModelID.value = ''
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
        <p class="muted">挑一个可用模型，直接加入当前 Key；个人模型库不再作为授权门槛。</p>
        <p class="muted" data-testid="credit-rate-note">100 积分 = 1 元；金额统一用积分显示。</p>
      </div>
      <button class="button primary" :disabled="!currentKey" @click="openAdd">检索添加</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <p v-if="actionError && !addOpen" class="error banner" role="alert">{{ actionError }}</p>
    <p v-if="actionNotice && !addOpen" class="notice banner" role="status">{{ actionNotice }}</p>

    <section class="panel key-context">
      <template v-if="currentKey">
        <div class="key-context-head">
          <label>当前 Key
            <select v-model="currentKeyID" :disabled="busy" data-testid="current-key" @change="onKeyChange">
              <option v-for="key in selectableKeys" :key="key.id" :value="key.id">
                {{ key.name }} · {{ keyStatusLabel(key.status) }} · {{ key.project.name }}
              </option>
            </select>
          </label>
          <span class="chip ok">{{ keyStatusLabel(currentKey.status) }}</span>
        </div>
        <p class="muted">当前 Key 已加入 {{ currentKey.models.length }} 个模型。这里的添加和移除会立即写入这把 Key 的白名单。</p>
      </template>
      <template v-else-if="keysLoaded">
        <p v-if="existingKey" class="muted">「{{ existingKey.name }}」{{ keyStatusLabel(existingKey.status) }}。Key 生效后才能选择模型。</p>
        <p v-else class="muted">还没有生效的 Key。模型广场只提供申请入口；申请通过并领取 Key 后再回来选择模型。</p>
        <div class="key-context-actions">
          <RouterLink class="button secondary" :to="{ name: 'student-api-keys' }" data-testid="apply-key">
            {{ existingKey ? '查看我的申请' : '申请密钥' }}
          </RouterLink>
        </div>
      </template>
      <p v-else class="muted">正在加载 Key…</p>
    </section>

    <section v-if="currentKey" class="panel protocol-context">
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
            <div>
              <h2>可用模型</h2>
              <p class="muted">共 {{ filteredModels.length }} 个；绿色「可调用」表示已加入当前 Key 并能使用。</p>
            </div>
          </div>
          <div class="plaza-cards">
            <article v-for="model in filteredModels" :key="model.model_id" class="panel is-interactive" :data-model="model.model_id">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge v-if="model.status !== 'active'" :status="model.status" />
              </div>
              <div class="chip-list">
                <span v-if="model.is_common" class="chip preset">预置</span>
                <span class="chip" :class="availabilityTone(model)" data-testid="model-availability">{{ modelAvailability(model).label }}</span>
                <span v-if="keyCoverage(model).inKey" class="chip" :class="keyCoverage(model).tone" data-testid="model-key-status">{{ keyCoverage(model).label }}</span>
                <span class="chip" data-testid="model-used">{{ modelUsageLabel(model.model_id) }}</span>
                <span class="chip" data-testid="model-balance">余额 {{ balanceText }}</span>
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
                <template v-if="currentKey">
                  <button v-if="!keyHasModel(model.model_id)" class="button secondary" data-testid="sync-key-model"
                    :disabled="!canAddModel(model)" @click="addModel(model.model_id)">
                    {{ addModelLabel(model) }}
                  </button>
                  <button v-else class="button ghost danger-text" data-testid="remove-key-model"
                    :disabled="busy" @click="removeModel(model.model_id)">
                    {{ removingModelID === model.model_id ? '正在移除…' : '从当前 Key 移除' }}
                  </button>
                </template>
              </div>
              <Transition name="content-fade">
                <dl v-if="expanded === model.model_id">
                  <dt>模型名</dt><dd>{{ model.model_id }}</dd>
                  <dt>中转厂商</dt><dd>{{ modelProviderLabel(model) }}</dd>
                  <dt>本月已用</dt><dd>{{ modelUsageLabel(model.model_id) }}</dd>
                  <dt>余额</dt><dd>{{ balanceText }}</dd>
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
          <EmptyState v-if="!filteredModels.length" title="没有匹配的可用模型" />
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
          当前 Key 为「{{ currentKey.name }}」；点「添加到我的 Key」会直接写入 Key 白名单，不再先加入个人模型库。
        </p>
        <p v-else-if="keysLoaded" class="muted catalog-note">
          还没有生效的 Key，只能先申请。
          <RouterLink :to="{ name: 'student-api-keys' }">{{ existingKey ? '查看我的申请' : '申请密钥' }}</RouterLink>
        </p>
        <p v-if="actionError" class="error banner" role="alert">{{ actionError }}</p>
        <p v-if="actionNotice" class="notice banner" role="status">{{ actionNotice }}</p>
        <input v-model="catalogQuery" placeholder="搜索模型名" @input="searchCatalog">
        <div class="model-table compact">
          <div class="model-table-head">
            <span>模型名</span>
            <span>中转厂商</span>
            <span>本月已用</span>
            <span>协议</span>
            <span>当前 Key</span>
            <span />
          </div>
          <div v-for="item in catalogItems" :key="item.model_id" class="model-table-row">
            <span class="model-name">{{ item.model_id }}</span>
            <span>{{ catalogProviderLabel(item) }}</span>
            <span>{{ modelUsageLabel(item.model_id) }}</span>
            <span class="catalog-protocols">
              <span v-for="value in catalogProtocols(item)" :key="value" class="chip protocol-chip" data-testid="catalog-protocol-chip">{{ protocolShortLabel(value) }}</span>
              <small v-if="catalogRecommendedLabel(item)" class="muted" data-testid="catalog-recommendation">推荐 {{ catalogRecommendedLabel(item) }}</small>
              <small v-if="!catalogProtocols(item).length" class="muted">协议待同步</small>
            </span>
            <span>
              <span class="chip" :class="catalogCoverage(item).tone" data-testid="catalog-key-coverage">{{ catalogCoverage(item).label }}</span>
              <small v-if="!catalogAvailability(item).addable" class="muted catalog-unavailable" data-testid="catalog-unavailable">{{ catalogAvailability(item).reason }}</small>
              <small v-else-if="catalogAvailability(item).state === 'unverified'" class="muted catalog-unavailable" data-testid="catalog-unverified">{{ catalogAvailability(item).reason }}</small>
            </span>
            <button class="button ghost" :disabled="!canAddCatalog(item)" :title="catalogAvailability(item).reason" data-testid="catalog-add" @click="addModel(item.model_id, item)">
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
.key-context-head { display: flex; align-items: flex-end; gap: 12px; flex-wrap: wrap; }
.key-context-head label { flex: 1 1 320px; }
.key-context-actions { display: flex; gap: 10px; flex-wrap: wrap; }
.notice.banner { border-color: #f0c987; background: #fff8eb; color: #7a4a00; }
.plaza-layout { display: grid; gap: 20px; }
.plaza-heading { margin-bottom: 16px; }
.plaza-heading h2 { margin: 0; font-size: 18px; }
.plaza-heading p { margin: 4px 0 0; }
.plaza-cards { display: grid; gap: 16px; }
.chip.preset { background: #eef3ff; color: #3b5bdb; }
.chip.warn { background: #fff4e5; color: #9a4d00; }
.chip.unavailable { background: #fdecec; color: #a12828; }
.chip.recommend { background: #f3efff; color: #5b3ea8; }
.chip.ok { background: #e8f7ee; color: #1d6b3a; }
.chip.muted { background: #f1f3f7; color: #5f6675; }
.chip.protocol-chip { background: #eef7f2; color: #1d6b52; }
.danger-text { color: #a12828; }
</style>
