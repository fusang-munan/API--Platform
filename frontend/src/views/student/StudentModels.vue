<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { studentAPI } from '../../api/student'
import type {
  StudentAvailabilityState, StudentModel, StudentModelCandidate, StudentModelCatalogPage, StudentUsageSnapshot,
} from '../../api/student'
import type { ApiKey, BindingAdapter, ModelCategory, ModelStatus } from '../../api/types'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'
import { capabilityLabel, formatTokenK } from '../../utils/modelCatalog'
import { selectCurrentStudentKey } from '../../utils/studentHarnessConfig'
import { sortModelsByRecency } from '../../utils/modelSort'
import { filterStudentModels } from '../../utils/studentModels'
import { catalogProviderLabel, modelDescriptionText, modelProviderLabel } from '../../utils/modelPlazaDisplay'
import {
  formatStudentModelUsage,
  formatStudentModelUsageParts,
  formatUsageCredits,
  formatUsagePendingReview,
  hasStudentUsageModels,
  studentKeyAvailable,
  sumStudentProjectCredits,
} from '../../utils/studentUsage'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { APIError, request } from '../../api/client'

type PlazaKey = Pick<ApiKey, 'id' | 'status' | 'updated_at' | 'name' | 'project'> & { models: StudentModel[] }
type KeyModelMutation = {
  model_ids?: string[]
  added_model_ids?: string[]
  removed_model_ids?: string[]
  warnings?: Array<string | { code?: string; message?: string; model_id?: string }>
}
type ModelAvailability = { state: string; label: string; available: boolean; addable: boolean; reason: string }
type KeyCoverage = { inKey: boolean; label: string; tone: 'ok' | 'warn' | 'unavailable' | 'muted' }
type ModelGroup = 'callable' | 'unverified' | 'historical' | 'maintenance' | 'addable'
type DirectoryModel = StudentModelCandidate & { with_grant: ModelGroup; without_grant: ModelGroup }
type DirectoryResponse = { directory?: { models: DirectoryModel[] } }

const commonRows = ref<StudentModel[]>([])
const libraryRows = ref<StudentModel[]>([])
const keys = ref<PlazaKey[]>([])
const keysLoaded = ref(false)
const currentKeyID = ref('')
const catalogResult = ref<StudentModelCatalogPage | null>(null)
const catalogLoading = ref(false)
const catalogError = ref('')
const catalogScroll = ref<HTMLElement | null>(null)
const directory = ref<DirectoryModel[] | null>(null)
const directoryLoading = ref(false)
const directoryError = ref(false)
const usage = ref<StudentUsageSnapshot | null>(null)
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
const error = ref('')
const syncingModelID = ref('')
const removingModelID = ref('')
const cleaningMaintenance = ref(false)
const modelGroup = ref<ModelGroup>('callable')
const modelGroupChosen = ref(false)
const actionError = ref('')
const actionNotice = ref('')
let keyListRevision = 0
let catalogTimer: ReturnType<typeof setTimeout> | undefined
let catalogRequest: AbortController | undefined
let directoryRequest: AbortController | undefined
const catalogPageSize = 20
const loadState = useLoadState()
const toast = useToast()

function modelKey(modelID: string) {
  return modelID.trim().toLowerCase()
}

function uniqueModels(lists: StudentModel[][]) {
  const seen = new Set<string>()
  const result: StudentModel[] = []
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
const modelGroupOptions: Array<{ value: ModelGroup; label: string }> = [
  { value: 'callable', label: '可调用' },
  { value: 'addable', label: '可添加' },
  { value: 'unverified', label: '待核验' },
  { value: 'historical', label: '历史授权' },
  { value: 'maintenance', label: '暂不可用' },
]
const searchModels = computed(() => sortModelsByRecency(
  filterStudentModels(allRows.value, query.value, category.value, status.value),
) as StudentModel[])
const groupedModels = computed<Record<ModelGroup, StudentModel[]>>(() => {
  const buckets: Record<ModelGroup, StudentModel[]> = {
    callable: [], unverified: [], historical: [], maintenance: [], addable: [],
  }
  const key = currentKey.value
  if (!directory.value || !key) {
    for (const model of searchModels.value) buckets[modelGroupFor(model)].push(model)
    return buckets
  }
  const directoryIDs = new Set<string>()
  for (const item of directory.value) {
    directoryIDs.add(modelKey(item.model_id))
    const group = keyHasModel(item.model_id) ? item.with_grant : item.without_grant
    buckets[group].push(modelFromCatalog(item))
  }
  for (const model of key.models) {
    if (!directoryIDs.has(modelKey(model.model_id))) buckets.historical.push(model)
  }
  return buckets
})
const filteredModels = computed(() => groupedModels.value[modelGroup.value])
const hasAnyModels = computed(() => searchModels.value.length > 0 || currentKey.value != null)
const maintenanceInKeyModels = computed(() => groupedModels.value.historical)
const maintenanceInKeyModelIDs = computed(() => maintenanceInKeyModels.value.map(model => model.model_id))
const currentGroupEmptyTitle = computed(() => {
  switch (modelGroup.value) {
    case 'callable': return '当前 Key 还没有可调用的模型'
    case 'unverified': return '当前没有待核验的模型'
    case 'historical': return '当前 Key 没有历史授权'
    case 'maintenance': return '当前没有暂不可用的模型'
    case 'addable': return '没有可添加的新模型'
  }
})
const catalogPages = computed(() => {
  const result = catalogResult.value
  return result ? Math.max(1, Math.ceil(result.total / result.page_size)) : 1
})
const catalogRange = computed(() => {
  const result = catalogResult.value
  if (!result) return '—'
  if (!result.total) return '共 0 条'
  const start = (result.page - 1) * result.page_size + 1
  return `第 ${start}–${start + result.items.length - 1} 条 / 共 ${result.total} 条`
})
const selectableKeys = computed(() => keys.value.filter(key => key.status === 'active'))
const currentKey = computed(() => selectableKeys.value.find(key => key.id === currentKeyID.value) ?? null)
const existingKey = computed(() => selectCurrentStudentKey(keys.value))
// 有生效 Key 时默认看「可调用」；还没有 Key 时「可调用」必然为空，先展示可添加的目录模型。
watch([currentKey, modelGroupChosen], () => {
  if (!modelGroupChosen.value) modelGroup.value = currentKey.value ? 'callable' : 'addable'
}, { immediate: true })
const currentKeyModelIDs = computed(() => new Set((currentKey.value?.models ?? []).map(model => modelKey(model.model_id))))
const currentKeyUsage = computed(() => {
  const keys = usage.value?.projects.find(project => project.project_id === currentKey.value?.project.id)?.keys
  return Array.isArray(keys) ? keys.find(key => key.id === currentKeyID.value) : undefined
})
const keyAvailableCredits = computed(() => {
  const available = studentKeyAvailable(currentKeyUsage.value)
  return available === null ? null : formatUsageCredits(available)
})
const keyProjectMemberCredits = computed(() => {
  const key = currentKeyUsage.value
  const effective = formatUsageCredits(key?.available_effective)
  const member = formatUsageCredits(key?.project_member_available)
  return effective !== '—' && member !== '—' && effective !== member ? member : null
})
const projectAvailableCredits = computed(() => {
  const total = sumStudentProjectCredits(usage.value?.projects ?? [], 'available')
  return total === null ? null : formatUsageCredits(total)
})
const modelUsageByID = computed(() => {
  const models = currentKeyUsage.value?.models
  return hasStudentUsageModels(models) ? new Map(models.map(row => [modelKey(row.id), row])) : null
})
const inFlightCredits = computed(() => {
  const total = sumStudentProjectCredits(usage.value?.projects ?? [], 'in_flight_credits')
  return total === null || total === '0.000' ? '' : formatUsageCredits(total)
})
const pendingReview = computed(() => formatUsagePendingReview(usage.value?.totals))
const protocolOptions = BINDING_ADAPTER_OPTIONS.filter(item =>
  ['openai_compatible', 'openai_responses', 'anthropic', 'google_gemini_v1beta'].includes(item.value))
const selectedProtocolLabel = computed(() => protocol.value ? protocolMeta(protocol.value).label : '')
const busy = computed(() => Boolean(syncingModelID.value || removingModelID.value || cleaningMaintenance.value))

function usagePlaceholder() {
  if (!currentKey.value) return '暂无生效 Key'
  if (usageFailed.value || (usageLoaded.value && !modelUsageByID.value)) return '用量暂不可用'
  return usageLoaded.value ? '' : '用量读取中…'
}

function modelUsageLabel(modelID: string) {
  return usagePlaceholder() || formatStudentModelUsage(modelUsageByID.value?.get(modelKey(modelID)))
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
function protocolsFor(model: StudentModel): string[] {
  const values = new Set((model.protocols ?? model.supported_protocols ?? []).filter(Boolean))
  if (!values.size) {
    if (model.primary_adapter) values.add(model.primary_adapter)
    if (model.responses_ready) values.add('openai_responses')
    if (model.anthropic_ready) values.add('anthropic')
  }
  return BINDING_ADAPTER_OPTIONS.map(item => item.value).filter(value => values.has(value))
}

function hasProtocolInfo(model: StudentModel) {
  return Array.isArray(model.protocols) || Array.isArray(model.supported_protocols) ||
    Boolean(model.primary_adapter || model.responses_ready || model.anthropic_ready)
}

function protocolShortLabel(value: string) {
  return protocolShortLabels[value] ?? value
}

function protocolMeta(value: string) {
  return bindingAdapterMeta(value as BindingAdapter)
}

function recommendedProtocolLabel(model: StudentModel) {
  const value = model.recommended_protocol?.trim()
  return value ? protocolFriendlyLabels[value] ?? protocolMeta(value).label : ''
}

function hasConfiguredPrice(model: { credit_multiplier?: string | null }) {
  if (model.credit_multiplier == null) return false
  const value = String(model.credit_multiplier).trim()
  return value !== '' && Number(value) > 0
}

function modelAvailability(model: StudentModel): ModelAvailability {
  const inferredState = !model.route_ready ? 'maintenance'
    : hasProtocolInfo(model) && !protocolsFor(model).length ? 'unverified' : 'available'
  const state = model.availability_state ?? (
    model.status === 'inactive' ? 'disabled'
      : model.status === 'pending_configuration' ? 'not_published'
        : model.availability?.trim() || inferredState
  )
  const reason = model.availability_reason?.trim() ?? ''
  const open = state === 'available' || state === 'degraded' || state === 'unverified'
  if (open && !hasConfiguredPrice(model)) {
    return { state: 'unavailable', label: '暂不可用', available: false, addable: false, reason: '该模型暂未开放，请等待老师开放。' }
  }
  const blocked = (label: string, fallback: string) => ({
    state, label, available: false, addable: false, reason: studentSafeReason(reason, fallback),
  })
  switch (state) {
    case 'available': return { state, label: '可用', available: true, addable: true, reason: '' }
    case 'degraded': return {
      state, label: '可用 · 探活异常', available: true, addable: true,
      reason: studentSafeReason(reason, '最近一次探活失败，仍可尝试调用。'),
    }
    case 'unverified': return {
      ...blocked('待核验', '线路尚未完成连通测试，调用可能失败。'),
      addable: model.route_ready && model.availability !== 'maintenance',
    }
    case 'channel_unavailable':
      return blocked(keyHasModel(model.model_id) ? '当前渠道未开放 · 历史授权' : '当前渠道未开放', '当前渠道目录未列出该模型。')
    case 'probe_failed': return blocked('暂时维护 · 线路故障', '当前线路连通测试失败，请等待老师恢复。')
    case 'maintenance': return blocked('暂时维护', '暂无可用线路，请等待老师恢复线路。')
    case 'disabled': return blocked('老师停用', '老师已停用该模型。')
    case 'not_published': return blocked('老师未开放', '老师尚未开放该模型。')
    default: return blocked('暂不可用', '当前不可用，请等待老师开放。')
  }
}

function availabilityTone(state: string) {
  if (state === 'available') return 'ok'
  if (state === 'degraded') return 'warn'
  if (state === 'unverified') return 'warn'
  return 'unavailable'
}

function keyHasModel(modelID: string) {
  return currentKeyModelIDs.value.has(modelKey(modelID))
}

function modelGroupFor(model: StudentModel): ModelGroup {
  const state = modelAvailability(model).state
  const inKey = keyHasModel(model.model_id)
  if (state === 'available' || state === 'degraded') return inKey ? 'callable' : 'addable'
  if (state === 'unverified') return 'unverified'
  return inKey ? 'historical' : 'maintenance'
}

function keyCoverage(model: StudentModel): KeyCoverage {
  if (!keysLoaded.value) return { inKey: false, label: '状态待加载', tone: 'muted' }
  if (!currentKey.value) return { inKey: false, label: '未加入', tone: 'muted' }
  if (!keyHasModel(model.model_id)) return { inKey: false, label: '未加入', tone: 'muted' }
  const state = modelAvailability(model).state
  if (state === 'available') return { inKey: true, label: '可调用', tone: 'ok' }
  if (state === 'degraded') return { inKey: true, label: '已加入 · 探活异常', tone: 'warn' }
  if (state === 'unverified') return { inKey: true, label: '已加入 · 待核验', tone: 'warn' }
  return { inKey: true, label: '已加入', tone: 'muted' }
}

function modelFromCatalog(item: StudentModelCandidate): StudentModel {
  const known = allRows.value.find(model => modelKey(model.model_id) === modelKey(item.model_id))
  const field = (name: string) => {
    const value = item[name as keyof StudentModelCandidate]
    return value === undefined ? item.raw_entry?.[name] : value
  }
  const stringField = (name: string) => {
    const value = field(name)
    return typeof value === 'string' ? value : undefined
  }
  const protocolLists = [field('protocols'), field('supported_protocols')].filter(Array.isArray)
  const ready = field('route_ready')
  const recommended = field('recommended_protocol')
  const credit = field('credit_multiplier') ?? known?.credit_multiplier
  return {
    ...known,
    model_id: item.model_id,
    display_name: item.display_name?.trim() || known?.display_name || item.model_id,
    description: item.description ?? known?.description ?? null,
    category: known?.category ?? 'text',
    capabilities: known?.capabilities ?? [],
    input_modalities: known?.input_modalities ?? ['text'],
    output_modalities: known?.output_modalities ?? ['text'],
    is_common: known?.is_common ?? false,
    status: (stringField('status') as ModelStatus | undefined) ?? known?.status ?? 'active',
    credit_multiplier: credit == null ? null : String(credit),
    provider_name: item.provider_name ?? known?.provider_name,
    route_ready: typeof ready === 'boolean' ? ready : known?.route_ready ?? true,
    protocols: protocolLists.length
      ? protocolLists.flat().filter((value): value is string => typeof value === 'string')
      : known?.protocols,
    recommended_protocol: typeof recommended === 'string' || recommended === null ? recommended : known?.recommended_protocol,
    availability: stringField('availability') ?? known?.availability,
    availability_state: (stringField('availability_state') as StudentAvailabilityState | undefined) ?? known?.availability_state,
    availability_reason: stringField('availability_reason') ?? known?.availability_reason,
  }
}

const catalogRows = computed(() => (catalogResult.value?.items ?? []).map(item => {
  const model = modelFromCatalog(item)
  const availability = modelAvailability(model)
  const joined = keyHasModel(model.model_id)
  const placeholder = usagePlaceholder()
  const usage = placeholder ? { credits: placeholder, calls: '' }
    : formatStudentModelUsageParts(modelUsageByID.value?.get(modelKey(model.model_id)))
  return {
    item, model, availability, joined, usage,
    protocols: protocolsFor(model),
    status: availability.state === 'available'
      ? joined ? '可调用' : '未加入'
      : joined && availability.state === 'degraded' ? '可调用 · 探活异常' : availability.label,
    tone: availability.state === 'available' && !joined ? 'muted' : availabilityTone(availability.state),
    addable: Boolean(keysLoaded.value && currentKey.value && !joined && availability.addable),
  }
}))

function canAddModel(model: StudentModel) {
  return Boolean(currentKey.value && !busy.value && !keyHasModel(model.model_id) && modelAvailability(model).addable)
}

function addModelLabel(model: StudentModel) {
  if (syncingModelID.value === model.model_id) return '正在添加…'
  if (keyHasModel(model.model_id)) return keyCoverage(model).label
  return modelAvailability(model).addable ? '添加到我的 Key' : '暂不可加入'
}

function protocolMismatch(model: StudentModel) {
  if (!protocol.value) return ''
  const availability = modelAvailability(model)
  if (availability.state !== 'available' && availability.state !== 'degraded' && availability.state !== 'unverified') return ''
  const supported = protocolsFor(model)
  if (!supported.length || supported.includes(protocol.value)) return ''
  const alternatives = supported.map(value => protocolMeta(value).label)
  return `此模型不支持 ${selectedProtocolLabel.value}。可在客户端改用 ${alternatives.join('、')}。`
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

function selectModelGroup(group: ModelGroup) {
  modelGroupChosen.value = true
  modelGroup.value = group
}

async function loadDirectory() {
  directoryRequest?.abort()
  const controller = directoryRequest = new AbortController()
  directoryLoading.value = true
  directoryError.value = false
  try {
    const result = await request<DirectoryResponse>('/api/v1/student/model-catalog?page_size=1', { signal: controller.signal })
    if (controller.signal.aborted) return
    if (!Array.isArray(result.directory?.models)) throw new Error('Missing student model directory')
    directory.value = result.directory.models
  } catch {
    if (!controller.signal.aborted) {
      directory.value = null
      directoryError.value = true
    }
  } finally {
    if (!controller.signal.aborted) directoryLoading.value = false
  }
}

async function load() {
  const revision = ++keyListRevision
  await loadState.run(async () => {
    try {
      const usageRequest = typeof studentAPI.usage === 'function'
        ? studentAPI.usage()
          .then(payload => {
            if (revision !== keyListRevision) return
            if (!Array.isArray(payload?.projects)) throw new Error('Invalid student usage report')
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
        models: key.models ?? [],
      }))
      keysLoaded.value = true
      const requested = keyFromQuery()
      const selected = selectableKeys.value.find(key => key.id === requested) ?? selectCurrentStudentKey(selectableKeys.value)
      currentKeyID.value = selected?.id ?? ''
      syncKeyQuery(currentKeyID.value)
      if (selected) void loadDirectory()
      error.value = ''
    } catch (caught) {
      if (revision === keyListRevision) error.value = caught instanceof APIError ? caught.message : '加载模型和 Key 失败'
    }
  })
}

function openAdd() {
  actionError.value = ''
  actionNotice.value = ''
  catalogQuery.value = ''
  addOpen.value = true
  void searchCatalog(1)
}

function cancelCatalogSearch() {
  clearTimeout(catalogTimer)
  catalogTimer = undefined
  catalogRequest?.abort()
  catalogRequest = undefined
}

function setAddOpen(open: boolean) {
  addOpen.value = open
  if (!open) cancelCatalogSearch()
}

watch(catalogQuery, () => {
  if (!addOpen.value) return
  cancelCatalogSearch()
  catalogPage.value = 1
  catalogLoading.value = true
  catalogError.value = ''
  catalogResult.value = null
  catalogTimer = setTimeout(() => { void searchCatalog(1) }, 275)
}, { flush: 'sync' })

async function searchCatalog(page = catalogPage.value) {
  cancelCatalogSearch()
  const request = new AbortController()
  catalogRequest = request
  catalogPage.value = page
  catalogLoading.value = true
  catalogError.value = ''
  catalogResult.value = null
  if (catalogScroll.value) catalogScroll.value.scrollTop = 0
  try {
    const result = await studentAPI.modelCatalog(catalogQuery.value.trim(), page, catalogPageSize, request.signal)
    if (request.signal.aborted) return
    const lastPage = Math.max(1, Math.ceil(result.total / result.page_size))
    if (page > lastPage) {
      void searchCatalog(lastPage)
      return
    }
    catalogResult.value = result
  } catch (caught) {
    if (!request.signal.aborted) {
      catalogError.value = caught instanceof APIError ? caught.message : '模型目录读取失败，请重试。'
    }
  } finally {
    if (!request.signal.aborted) {
      catalogLoading.value = false
      catalogRequest = undefined
    }
  }
}

function warningsText(result: KeyModelMutation) {
  const warnings = result.warnings ?? []
  return warnings.map(item => {
    if (typeof item === 'string') return studentSafeReason(item, '')
    const code = (item.code ?? '').toLowerCase()
    if (/degraded|探活异常/.test(code)) return studentSafeReason(item.message ?? '', '该模型最近一次探活异常，已加入当前 Key，调用可能失败。')
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

async function addModel(modelID: string, catalogModel?: StudentModel) {
  if (busy.value || !keysLoaded.value || !currentKey.value) return
  const key = currentKey.value
  if (keyHasModel(modelID)) return
  const model = catalogModel ?? allRows.value.find(item => modelKey(item.model_id) === modelKey(modelID))
  if (model && !modelAvailability(model).addable) {
    actionError.value = `${model.model_id} 当前不可加入：${modelAvailability(model).reason}`
    return
  }
  syncingModelID.value = modelID
  actionError.value = ''
  actionNotice.value = ''
  try {
    const result = await studentAPI.addKeyModels(key.id, [modelID]) as KeyModelMutation
    ++keyListRevision
    if (model && !key.models.some(item => modelKey(item.model_id) === modelKey(model.model_id))) {
      key.models = [...key.models, model]
    }
    const warning = warningsText(result)
    if (warning) actionNotice.value = warning
    else if (model && modelAvailability(model).state === 'degraded') {
      actionNotice.value = '已加入当前 Key；该模型最近一次探活异常，仍可尝试调用，请求可能失败。'
    } else if (model && modelAvailability(model).state === 'unverified') {
      actionNotice.value = `已加入当前 Key。 ${modelAvailability(model).reason}`
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

async function cleanupUnavailable() {
  if (busy.value || !currentKey.value) return
  const key = currentKey.value
  const modelIDs = maintenanceInKeyModelIDs.value
  if (!modelIDs.length) return
  if (!confirm(`从当前 Key「${key.name}」清理 ${modelIDs.length} 个不可用授权？`)) return
  cleaningMaintenance.value = true
  actionError.value = ''
  actionNotice.value = ''
  try {
    const result = await studentAPI.removeKeyModels(key.id, modelIDs) as KeyModelMutation
    const removed = new Set((result.removed_model_ids?.length ? result.removed_model_ids : modelIDs).map(modelKey))
    key.models = key.models.filter(model => !removed.has(modelKey(model.model_id)))
    ++keyListRevision
    const warning = warningsText(result)
    if (warning) actionNotice.value = warning
    toast.success('已清理不可用授权')
    // 以服务端返回的 Key 为准重新拉取，避免本地列表和后端白名单不一致。
    await load()
  } catch (caught) {
    actionError.value = caught instanceof APIError ? caught.message : '清理不可用授权失败，请稍后重试'
  } finally {
    cleaningMaintenance.value = false
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
onBeforeUnmount(() => {
  cancelCatalogSearch()
  directoryRequest?.abort()
  window.removeEventListener('nebula:refresh', refresh)
})
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">STUDENT</p>
        <h1>模型广场</h1>
        <p class="muted">挑一个可用模型，直接加入当前 Key；个人模型库不再作为授权门槛。</p>
        <p class="muted" data-testid="credit-rate-note">100 积分 = 1 元；金额统一用积分显示。</p>
        <p class="muted" data-testid="usage-scope-note">本月模型用量按所选 Key 显示。积分为已结算金额。</p>
      </div>
      <button class="button primary" :disabled="!currentKey" @click="openAdd">检索添加</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <p v-if="actionError && !addOpen" class="error banner" role="alert">{{ actionError }}</p>
    <p v-if="actionNotice && !addOpen" class="notice banner" role="status">{{ actionNotice }}</p>
    <p v-if="inFlightCredits || pendingReview" class="notice banner usage-pending-note" data-testid="usage-pending-note" role="status">
      <span v-if="inFlightCredits">全部项目在途占用 {{ inFlightCredits }} 积分。</span>
      <span v-if="pendingReview">全部项目{{ pendingReview }}。</span>
    </p>

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
          <div class="key-balances">
            <p v-if="keyAvailableCredits !== null" data-testid="key-balance">
              本项目个人可用积分 <strong>{{ keyAvailableCredits }}</strong>
            </p>
            <p v-else class="muted">{{ usageLoaded || usageFailed ? '本项目个人可用积分暂不可用' : '正在读取本项目个人可用积分…' }}</p>
            <p v-if="keyProjectMemberCredits !== null" class="muted" data-testid="key-project-member-balance">
              项目内个人余额 <strong>{{ keyProjectMemberCredits }}</strong>
            </p>
            <p v-if="projectAvailableCredits !== null" class="muted" data-testid="project-balance">
              各项目个人余额合计 <strong>{{ projectAvailableCredits }}</strong>
            </p>
          </div>
        </div>
        <p class="muted" data-testid="model-summary">当前 Key 已授权 {{ currentKey.models.length }} 个模型</p>
        <p class="muted key-note" data-testid="v1-models-note">
          DSH 或 Codex 的模型选择器通过 /v1/models 读取当前可调用的模型。
        </p>
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
      <p v-if="!currentKey && projectAvailableCredits !== null" class="muted" data-testid="project-balance">
        各项目个人余额合计 <strong>{{ projectAvailableCredits }}</strong>
      </p>
    </section>

    <section v-if="currentKey" class="panel protocol-context">
      <label>准备使用的调用协议
        <select v-model="protocol" data-testid="protocol-select">
          <option v-for="item in protocolOptions" :key="item.value" :value="item.value">
            {{ item.label }}（{{ item.gatewayPath }}）
          </option>
        </select>
      </label>
      <p class="muted" data-testid="protocol-note">网关不做协议转换，请在客户端选择模型支持的协议。</p>
    </section>

    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="cards" label="正在加载模型" style="margin-top:20px">
      <div v-if="hasAnyModels" class="plaza-layout">
        <section class="plaza-column">
          <div class="plaza-heading">
            <h2>我的模型</h2>
            <button v-if="modelGroup === 'historical' && maintenanceInKeyModels.length"
              class="button ghost danger-text" data-testid="cleanup-maintenance"
              :disabled="busy" @click="cleanupUnavailable">
              {{ cleaningMaintenance ? '正在清理…' : '清理全部不可用授权' }}
            </button>
          </div>
          <div class="segmented model-groups" role="tablist" aria-label="模型分组">
            <button v-for="option in modelGroupOptions" :key="option.value" role="tab"
              :aria-selected="modelGroup === option.value" :class="{ active: modelGroup === option.value }"
              :data-testid="`model-group-${option.value}`" @click="selectModelGroup(option.value)">
              {{ option.label }}<span v-if="currentKey && directory" class="group-count">{{ groupedModels[option.value].length }}</span>
            </button>
          </div>
          <p v-if="currentKey && directoryLoading" class="muted" role="status">正在读取完整目录…</p>
          <div v-else-if="currentKey && directoryError" class="muted">
            完整目录计数暂不可用 <button class="button ghost" type="button" @click="loadDirectory">重试</button>
          </div>
          <div class="plaza-cards">
            <article v-for="model in filteredModels" :key="model.model_id" class="panel is-interactive" :data-model="model.model_id">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
              </div>
              <div class="chip-list">
                <span v-if="model.is_common" class="chip preset">预置</span>
                <span class="chip" :class="availabilityTone(modelAvailability(model).state)" data-testid="model-availability">{{ modelAvailability(model).label }}</span>
                <span v-if="keyCoverage(model).inKey" class="chip" :class="keyCoverage(model).tone" data-testid="model-key-status">{{ keyCoverage(model).label }}</span>
                <span class="chip" data-testid="model-used">{{ modelUsageLabel(model.model_id) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="value in protocolsFor(model)" :key="value" class="chip protocol-chip" data-testid="protocol-chip">{{ protocolShortLabel(value) }}</span>
                <span v-if="recommendedProtocolLabel(model)" class="chip recommend" data-testid="recommended-protocol">推荐 {{ recommendedProtocolLabel(model) }}</span>
                <span v-if="protocolMismatch(model)" class="chip warn" data-testid="protocol-mismatch">协议不匹配</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p v-if="modelAvailability(model).reason" class="protocol-warning" data-testid="model-unavailable-reason">{{ modelAvailability(model).reason }}</p>
              <p v-if="protocolMismatch(model)" class="protocol-warning" data-testid="protocol-alternatives">{{ protocolMismatch(model) }}</p>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <template v-if="currentKey">
                  <button v-if="!keyHasModel(model.model_id)" class="button secondary" data-testid="sync-key-model"
                    :disabled="!canAddModel(model)" @click="addModel(model.model_id, model)">
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
                  <dt>本月用量</dt><dd>{{ modelUsageLabel(model.model_id) }}</dd>
                  <dt>支持协议</dt><dd>
                    <template v-if="protocolsFor(model).length">
                      <span v-for="(value, index) in protocolsFor(model)" :key="value">{{ index ? '、' : '' }}{{ protocolMeta(value).label }}（{{ protocolMeta(value).gatewayPath }}）</span>
                    </template>
                    <template v-else>协议信息待同步</template>
                  </dd>
                  <dt>推荐协议</dt><dd>{{ recommendedProtocolLabel(model) || '暂无推荐' }}</dd>
                  <dt>能力</dt><dd>{{ model.capabilities.map(capabilityLabel).join('、') || '—' }}</dd>
                  <dt>输入 / 输出</dt><dd>{{ model.input_modalities.join('、') || '—' }} / {{ model.output_modalities.join('、') || '—' }}</dd>
                  <dt>上下文 / 最大输入 / 最大输出</dt><dd>{{ formatTokenK(model.context_window) }} / {{ formatTokenK(model.max_input_tokens) }} / {{ formatTokenK(model.max_output_tokens) }}</dd>
                </dl>
              </Transition>
            </article>
          </div>
          <EmptyState v-if="!filteredModels.length" :title="currentGroupEmptyTitle" />
        </section>
      </div>
      <EmptyState v-else title="还没有模型" />
    </LoadingRegion>

    <AppDialog :open="addOpen" title="检索添加模型" description="搜索模型并管理当前 Key 的新增授权。" wide @update:open="setAddOpen">
      <section class="student-model-catalog">
        <div class="catalog-search">
          <p v-if="currentKey" class="muted" data-testid="catalog-key-note">当前 Key <strong>{{ currentKey.name }}</strong></p>
          <input v-model="catalogQuery" type="search" aria-label="搜索模型名或型号" placeholder="搜索模型名或型号">
          <p v-if="actionError" class="error banner" role="alert">{{ actionError }}</p>
          <p v-if="actionNotice" class="notice banner" role="status">{{ actionNotice }}</p>
        </div>
        <div ref="catalogScroll" class="catalog-results" role="region" aria-label="模型搜索结果" :aria-busy="catalogLoading" tabindex="0">
          <p v-if="catalogLoading" class="catalog-message muted" role="status">正在读取模型目录…</p>
          <div v-else-if="catalogError" class="catalog-message">
            <p class="error" role="alert">{{ catalogError }}</p>
            <button class="button secondary" type="button" @click="searchCatalog()">重试</button>
          </div>
          <p v-else-if="!catalogRows.length" class="catalog-message muted" role="status">暂无匹配模型</p>
          <ul v-else class="catalog-list" aria-label="模型目录">
            <li v-for="row in catalogRows" :key="row.model.model_id" :data-catalog-model="row.model.model_id" class="catalog-row">
              <div class="catalog-row-heading">
                <div class="catalog-model">
                  <strong>{{ row.model.display_name }}</strong>
                  <small v-if="row.model.display_name !== row.model.model_id" class="muted">{{ row.model.model_id }}</small>
                </div>
                <button v-if="row.addable" type="button" class="button secondary catalog-action" :disabled="busy"
                  :aria-label="`加入 ${row.model.model_id} 到当前 Key`" data-testid="catalog-add"
                  @click="addModel(row.model.model_id, row.model)">
                  {{ syncingModelID === row.model.model_id ? '正在加入…' : '加入当前 Key' }}
                </button>
              </div>
              <dl class="catalog-row-meta">
                <div>
                  <dt>厂商</dt>
                  <dd>{{ catalogProviderLabel(row.item) }}</dd>
                </div>
                <div class="catalog-usage">
                  <dt>本月用量</dt>
                  <dd>
                    <span>{{ row.usage.credits }}</span>
                    <small v-if="row.usage.calls" class="muted">{{ row.usage.calls }}</small>
                  </dd>
                </div>
                <div class="catalog-protocol">
                  <dt>协议</dt>
                  <dd v-if="row.protocols.length" class="catalog-chips">
                    <span v-for="value in row.protocols" :key="value" class="chip protocol-chip"
                      :class="{ recommend: value === row.model.recommended_protocol }"
                      :title="protocolMeta(value).label" data-testid="catalog-protocol-chip">
                      {{ protocolShortLabel(value) }}<template v-if="value === row.model.recommended_protocol"> · 推荐</template>
                    </span>
                  </dd>
                  <dd v-else class="muted">协议待同步</dd>
                </div>
                <div>
                  <dt>状态</dt>
                  <dd class="catalog-chips">
                    <span v-if="row.joined" class="chip muted" data-testid="catalog-joined">已加入</span>
                    <span class="chip" :class="row.tone" :title="row.availability.reason"
                      data-testid="catalog-key-coverage">{{ row.status }}</span>
                  </dd>
                  <dd v-if="row.availability.reason" class="muted">{{ row.availability.reason }}</dd>
                </div>
              </dl>
            </li>
          </ul>
        </div>
        <nav class="catalog-pagination" aria-label="模型目录分页">
          <span class="muted" aria-live="polite">{{ catalogRange }}</span>
          <div class="catalog-page-controls">
            <button class="button ghost" type="button" :disabled="!catalogResult || catalogPage <= 1"
              @click="searchCatalog(catalogPage - 1)">上一页</button>
            <button class="button ghost" type="button" :disabled="!catalogResult || catalogPage >= catalogPages"
              @click="searchCatalog(catalogPage + 1)">下一页</button>
          </div>
        </nav>
      </section>
    </AppDialog>
  </section>
</template>

<style scoped>
.key-context-head { display: flex; align-items: flex-end; gap: 12px; flex-wrap: wrap; }
.key-context-head label { flex: 1 1 320px; }
.key-balances { flex: 0 1 auto; display: grid; gap: 3px; font-variant-numeric: tabular-nums; }
.key-balances p { margin: 0; overflow-wrap: anywhere; }
.key-context-actions { display: flex; gap: 10px; flex-wrap: wrap; }
.notice.banner { border-color: #f0c987; background: #fff8eb; color: #7a4a00; }
.usage-pending-note { margin: 0; }
.plaza-layout { display: grid; gap: 20px; }
.plaza-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 4px; }
.plaza-heading h2 { margin: 0; font-size: 18px; }
.plaza-heading p { margin: 4px 0 0; }
.model-groups { margin: 12px 0 16px; }
.model-groups button { display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.group-count { padding: 1px 6px; border-radius: 999px; background: rgba(101, 66, 220, .1); font-size: 11px; }
.model-groups button.active .group-count { background: rgba(101, 66, 220, .16); }
.key-note { font-size: 12px; margin-top: 6px; }
.plaza-cards { display: grid; gap: 16px; }
.chip.preset { background: #eef3ff; color: #3b5bdb; }
.chip.warn { background: #fff4e5; color: #9a4d00; }
.chip.unavailable { background: #fdecec; color: #a12828; }
.chip.recommend { background: #f3efff; color: #5b3ea8; }
.chip.ok { background: #e8f7ee; color: #1d6b3a; }
.chip.muted { background: #f1f3f7; color: #5f6675; }
.chip.protocol-chip:not(.recommend) { background: #eef7f2; color: #1d6b52; }
.danger-text { color: #a12828; }

:global(.dialog-content:has(> .student-model-catalog)) {
  display: flex;
  flex-direction: column;
  width: min(1040px, calc(100% - 28px));
  height: min(760px, calc(100dvh - 28px));
  max-height: calc(100dvh - 28px);
  padding: clamp(14px, 3vw, 24px);
  overflow: hidden;
}
:global(.dialog-content:has(> .student-model-catalog) .dialog-close) {
  min-width: 44px;
  min-height: 44px;
}
.student-model-catalog {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
}
.catalog-search { display: grid; flex: none; gap: 8px; padding-bottom: 12px; }
.catalog-search p { margin: 0; overflow-wrap: anywhere; }
.catalog-search input { width: 100%; min-height: 44px; margin: 0; }
.catalog-results {
  flex: 1;
  min-height: 0;
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  border-block: 1px solid #e5e9f1;
}
.catalog-results:focus-visible { outline: 2px solid var(--primary); outline-offset: -2px; }
.catalog-message { padding: 20px 12px; text-align: center; }
.catalog-list { margin: 0; padding: 0; list-style: none; }
.catalog-row { padding: 12px; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
.catalog-row:last-child { border-bottom: 0; }
.catalog-row-heading {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: start;
  gap: 8px 12px;
}
.catalog-model strong, .catalog-row small { display: block; }
.catalog-row small { margin-top: 3px; font-size: 12px; line-height: 1.35; }
.catalog-row-meta {
  display: grid;
  grid-template-columns: minmax(0, .8fr) minmax(0, 1fr) repeat(2, minmax(0, 1.5fr));
  gap: 8px 12px;
  margin: 8px 0 0;
  font-size: 13px;
}
.catalog-row-meta dt { margin-bottom: 3px; color: var(--ink-soft); font-size: 12px; }
.catalog-row-meta dd { margin: 0; }
.catalog-row-meta dd + dd { margin-top: 4px; }
.catalog-usage,
.catalog-pagination { font-variant-numeric: tabular-nums; }
.catalog-chips { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; }
.catalog-chips .chip {
  display: inline-block;
  min-width: 0;
  max-width: 100%;
  padding: 4px 7px;
  line-height: 1.3;
  white-space: normal;
  overflow-wrap: anywhere;
}
.catalog-action { min-height: 44px; padding-inline: 9px; white-space: nowrap; }
.catalog-pagination {
  display: flex;
  flex: none;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 12px;
  font-size: 13px;
}
.catalog-page-controls { display: flex; gap: 8px; margin-inline-start: auto; }
.catalog-pagination .button,
.catalog-message .button { min-height: 44px; }

@media (max-width: 800px) {
  .catalog-results { scrollbar-gutter: auto; }
  .catalog-row, .catalog-row:last-child {
    margin-block: 8px;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: var(--surface);
  }
  .catalog-row-meta { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

@media (prefers-reduced-motion: reduce) {
  :global(.dialog-content:has(> .student-model-catalog)) { transform: translate(-50%, -50%) !important; }
}
</style>
