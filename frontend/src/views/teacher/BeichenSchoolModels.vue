<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenCapabilities, BeichenCatalogModel, BeichenHealthDimension, BeichenModelCapability, BeichenModelUsage, BeichenOverview, BeichenProbeRun, BeichenUsageRange, BindingAdapter } from '../../api/types'
import type { BeichenCatalogKey, BeichenRebindPlan, BeichenRebindRoute, BeichenWebsiteLocation } from '../../api/types'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { useToast } from '../../composables/useToast'
import { emptyPriceDetail, formatPriceConditions, keyGroupIssue, keyGroupRate, missingPriceDetailLabels, schoolCatalog, schoolQuoteDetail, webPriceDetail, type SchoolCatalogRow, type SchoolQuote } from '../../utils/beichenModels'
import { bindingAdapterMeta } from '../../utils/bindingAdapters'
import { formatPercent, formatTokens, shiftDecimal } from '../../utils/credits'
import { schoolDay, statusLabel } from '../../utils/format'
import AppDialog from '../../components/AppDialog.vue'
import BeichenPriceDetail from '../../components/BeichenPriceDetail.vue'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenModelUsageDrawer from '../../components/BeichenModelUsageDrawer.vue'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'
import BeichenKeyCapabilities from './BeichenKeyCapabilities.vue'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string>; providerMaintenance?: boolean }>()
const emit = defineEmits<{ 'catalog-synced': [] }>()
type UsageState = { data?: BeichenModelUsage; loading?: boolean; error?: string }
type PriceState = NonNullable<BeichenCatalogModel['price_state']>
type BatchOutcome = { model: string; status: 'enabled' | 'skipped' | 'failed'; reason: string }
type ProbeBudget = { date: string; used: number; limit: number; remaining: number; exhausted: boolean }
type ProbeRunWithBudget = BeichenProbeRun & { budget?: ProbeBudget }

const overview = ref<BeichenOverview>()
const capabilities = ref<BeichenCapabilities>()
const usageStates = ref<Partial<Record<BeichenUsageRange, UsageState>>>({})
const range = ref<BeichenUsageRange>('month')
const loading = ref(false)
const updating = ref('')
const syncing = ref(false)
const rescanKey = ref('')
const rescanning = ref(false)
const rescanError = ref('')
const rebindOpen = ref(false)
const rebindPlan = ref<BeichenRebindPlan>()
const rebinding = ref(false)
const rebindError = ref('')
const rebindActions = { add: '新增', update: '调整', enable: '启用', disable: '停用' } as const
const probeSubmitting = ref(false)
const probeOverride = ref<ProbeRunWithBudget>()
const probeError = ref('')
const error = ref('')
const toast = useToast()
const reasonId = useId()
const priceColumns = computed(() => [
  { id: 'official', label: rows.value.some(row => row.pricing.some(quote => quote.status === 'derived')) ? '基础报价' : '官网价' },
  { id: 'student', label: '学生实价' },
] as const)
const priceStateLabels: Record<PriceState, string> = {
  confirmed: '报价已确认', conditional: '按条件计价', derived: '反推价，待核对', manual: '手动价', missing: '缺少报价', mixed: '混合报价',
}
const tokenPriceUnit = '每百万 Token 的积分'
const convertibleUnits = new Set([tokenPriceUnit, '积分/次', '积分/秒'])
const filters = [{ id: 'all', label: '全部' }, { id: 'active', label: '已启用' }, { id: 'pending', label: '待配置' }, { id: 'unpriced', label: '无报价' }, { id: 'manual', label: '手动价' }] as const
function adapterLabel(value: string) { return bindingAdapterMeta(value as BindingAdapter).label }
const filter = ref<typeof filters[number]['id']>('all')
const priceRow = ref<SchoolCatalogRow>()
const conditionsRow = ref<CatalogRow>()
const usageOpen = ref(false)
const initialModel = ref('')
const websiteDirectory = computed(() => overview.value?.website_directory)
const lastCrawl = computed(() => websiteDirectory.value?.last_run)
const sourceLabels = { website: '官网', key: 'Key', both: '官网和 Key' } as const
const protocolLabels: Record<string, string> = {
  openai_compatible: 'OpenAI Chat',
  openai_responses: 'OpenAI Responses',
  anthropic: 'Anthropic Messages',
  google_gemini_v1beta: 'Gemini 原生',
}
type CapabilitySummary = {
  protocols: string[]
  availableProtocols: string[]
  recommendedProtocol?: string
  probeState: 'passed' | 'unprobed' | 'failed' | 'not_applicable'
  availability: 'available' | 'maintenance' | 'unverified' | 'unavailable' | 'not_published'
  availabilityReason: string
  actionHint: string
  priceState?: PriceState
  priceBlocker?: string
  bindingInvalid: boolean
}
const availabilityRank: Record<string, number> = { available: 5, unverified: 4, not_published: 3, maintenance: 2, unavailable: 1 }
const capabilityRows = computed(() => {
  const result = new Map<string, BeichenModelCapability[]>()
  for (const row of capabilities.value?.capabilities ?? []) {
    const model = row.model.trim().toLowerCase()
    if (!model) continue
    const rows = result.get(model) ?? []
    rows.push(row)
    result.set(model, rows)
  }
  return result
})
function protocolLabel(value: string) {
  return protocolLabels[value] ?? bindingAdapterMeta(value as BindingAdapter).label
}
function normalizeSignal(value: string) {
  return value.trim().toLowerCase().replace(/[_ /.:]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
}
function capabilityFamilyRank(model: string, protocol: string) {
  const name = normalizeSignal(model)
  const anthropic = Boolean(name) && !name.startsWith('gpt') && ['claude', 'opus', 'sonnet', 'haiku'].some(token => name.includes(token))
  let preferred = ['openai_compatible', 'openai_responses']
  if (anthropic) preferred = ['anthropic', 'openai_compatible', 'openai_responses']
  else if (name.includes('gemini')) preferred = ['google_gemini_v1beta', 'openai_compatible', 'openai_responses']
  const index = preferred.indexOf(protocol)
  return index >= 0 ? 3 - index : 0
}
function capabilityPriceState(value?: string): PriceState | undefined {
  return value === 'confirmed' || value === 'conditional' || value === 'derived' || value === 'manual' || value === 'missing' || value === 'mixed' ? value : undefined
}
function uniqueProtocols(values: Array<string | null | undefined>) {
  return [...new Set(values.filter((value): value is string => Boolean(value)))].sort()
}
/** 与后端 catalog.SummarizeProtocols 相同的排序规则，保证教师页与 /v1/models 推荐一致。 */
function summarizeCapabilities(model: string): CapabilitySummary | undefined {
  const rows = capabilityRows.value.get(model.trim().toLowerCase())
  if (!rows?.length) return undefined
  const ready = rows.filter(row => row.binding_state === 'ready')
  const published = ready.length > 0
  const listed = rows.filter(row => row.catalog_state === 'key_listed' || row.catalog_state === 'website_only')
  const basis = published ? ready : listed.length ? listed : rows
  const bindingInvalid = rows.some(row => row.binding_state === 'invalid')
  const priceStates = new Set((published ? ready : basis).map(row => capabilityPriceState(row.price_state)).filter(Boolean) as PriceState[])
  const priceState = priceStates.size > 1 ? 'mixed' : [...priceStates][0]
  const priceBlocker = basis.map(row => row.price_blocker?.message?.trim()).find(Boolean)
  const actionHint = basis.map(row => row.action_hint?.trim()).find(Boolean) ?? ''
  const details = basis.flatMap(row => row.protocol_details ?? [])
  if (!details.length) {
    const chosen = [...basis].sort((left, right) =>
      (availabilityRank[right.availability] ?? 0) - (availabilityRank[left.availability] ?? 0))[0]
    const recommended = basis.map(row => row.recommended_protocol?.trim()).find(Boolean)
    return {
      protocols: uniqueProtocols(basis.flatMap(row => row.supported_protocols ?? [])),
      availableProtocols: uniqueProtocols(basis.flatMap(row => row.available_protocols ?? [])),
      recommendedProtocol: recommended || undefined,
      probeState: (chosen?.probe_state ?? 'not_applicable') as CapabilitySummary['probeState'],
      availability: (chosen?.availability ?? 'not_published') as CapabilitySummary['availability'],
      availabilityReason: chosen?.availability_reason?.trim() ?? '',
      actionHint,
      priceState,
      priceBlocker,
      bindingInvalid,
    }
  }
  const evidence = new Map<string, number>()
  for (const detail of details) {
    if (published && !detail.binding_ready) continue
    const rank = detail.probe_state === 'passed' ? 2 : detail.probe_state === 'unprobed' ? 1 : 0
    evidence.set(detail.protocol, Math.max(evidence.get(detail.protocol) ?? -1, rank))
  }
  const routedProtocols = [...evidence.keys()].sort()
  const supportedProtocols = uniqueProtocols(rows.flatMap(row => row.supported_protocols ?? []))
  const protocols = supportedProtocols.length ? supportedProtocols : routedProtocols
  const availableProtocols = [...evidence].filter(([, rank]) => rank === 2).map(([protocol]) => protocol).sort()
  const best = [...evidence.values()].reduce((rank, value) => Math.max(rank, value), 0)
  const recommendedProtocol = best > 0
    ? [...evidence].filter(([, rank]) => rank === best)
      .sort(([left], [right]) => capabilityFamilyRank(model, right) - capabilityFamilyRank(model, left) || left.localeCompare(right))[0][0]
    : undefined
  const probeState: CapabilitySummary['probeState'] = !routedProtocols.length ? 'not_applicable'
    : best === 2 ? 'passed' : best === 1 ? 'unprobed' : 'failed'
  const reasonFor = (state: string) => basis.find(row => row.availability === state && row.availability_reason?.trim())?.availability_reason?.trim() ?? ''
  let availability: CapabilitySummary['availability']
  let availabilityReason = ''
  if (published) {
    if (!routedProtocols.length) {
      availability = 'maintenance'
      availabilityReason = reasonFor('maintenance') || '维护中。当前没有可接入的线路，请联系老师核对渠道 Key 和模型绑定。'
    } else if (probeState === 'failed') {
      availability = 'maintenance'
      availabilityReason = reasonFor('maintenance') || '维护中。所有已配置线路最近一次探活均失败，请联系老师检查渠道 Key 后重新探活。'
    } else if (!availableProtocols.length) {
      availability = 'unverified'
      availabilityReason = reasonFor('unverified') || '已有绑定线路，当前还没有通过探活的协议。'
    } else {
      availability = 'available'
    }
  } else {
    const chosen = [...basis].sort((left, right) =>
      (availabilityRank[right.availability] ?? 0) - (availabilityRank[left.availability] ?? 0))[0]
    availability = (chosen?.availability ?? 'not_published') as CapabilitySummary['availability']
    availabilityReason = chosen?.availability_reason?.trim() ?? ''
    if (bindingInvalid) {
      availability = 'maintenance'
      availabilityReason = rows.find(row => row.binding_state === 'invalid' && row.availability_reason?.trim())?.availability_reason?.trim()
        || availabilityReason
    }
  }
  return {
    protocols, availableProtocols, recommendedProtocol, probeState, availability, availabilityReason,
    actionHint, priceState, priceBlocker, bindingInvalid,
  }
}
const rescanKeys = computed(() => (overview.value?.credentials ?? []).filter(key => key.status === 'active'))
const rescanKeyAvailable = computed(() => rescanKeys.value.some(key => key.credential_id === rescanKey.value))
const billingByKey = computed(() => new Map((overview.value?.datasets ?? [])
  .filter(dataset => dataset.kind === 'billing').map(dataset => [dataset.credential_id, dataset])))
const billingRows = computed(() => rescanKeys.value.flatMap(key => {
  const billing = billingByKey.value.get(key.credential_id)
  return billing?.billing_state ? [{ ...billing, label: key.label }] : []
}))
const rebindModelCount = computed(() => new Set(rebindPlan.value?.changes.map(row => row.model_id)).size)
const catalog = computed(() => {
  const data = overview.value
  if (!data) return []
  const entries = new Map((data.model_catalog ?? []).map(row => [row.model.toLowerCase(), row]))
  const observedKeys = new Map<string, Set<string>>()
  for (const record of data.models ?? []) {
    const model = record.model.toLowerCase()
    const ids = observedKeys.get(model) ?? new Set<string>()
    ids.add(record.credential_id)
    observedKeys.set(model, ids)
  }
  return schoolCatalog(data, props.providerId).map(row => {
    const model = row.model.toLowerCase()
    const entry = entries.get(model)
    const routes = (entry?.preferred_routes ?? []).filter(route => route.provider_id === props.providerId)
    const protocols = new Set(routes.map(route => route.adapter).filter(Boolean))
    const keys = new Set([
      ...(entry?.credential_ids ?? []), ...(observedKeys.get(model) ?? []), ...row.credentials.map(key => key.credential_id),
    ].filter(Boolean))
    const keyCoverage = validCount(entry?.key_count) ?? keys.size
    const routeCount = validCount(entry?.route_count) ?? row.routeCount
    const readyCount = validCount(entry?.ready_count) ?? row.readyCount
    const activation = entry?.activation
    const active = row.status === 'active' || activation === 'active' || Boolean(entry?.binding_issue)
    const source = entry?.source
    const websiteOnly = entry?.catalog_state === 'website_only'
    const listed = entry?.catalog_state ? entry.catalog_state === 'key_listed' : entry?.credential_ids?.length !== 0
    const canDisable = row.canDisable || (activation === 'active' && Boolean(entry?.school_model))
    const capability = summarizeCapabilities(row.model)
    const priceState = capability?.priceState ?? entry?.price_state ?? oldPriceState(row)
    const priceCovered = !websiteOnly && (capability
      ? priceState === 'confirmed' || priceState === 'manual' || (priceState === 'mixed' && row.priceReady)
      : entry?.price_covered ?? (entry?.price_state == null ? row.priceReady
        : priceState === 'confirmed' || priceState === 'manual' || (priceState === 'mixed' && row.priceReady)))
    const blockers = entry?.blockers ?? []
    const routeBlocked = entry?.route_count === 0 || blockers.some(blocker => blocker.code === 'no_ready_route')
    const blockerMessages = blockers
      .filter(blocker => !routeBlocked || (blocker.code !== 'missing_price' && blocker.code !== 'conditional_price'))
      .map(blocker => blocker.message?.trim()).filter(Boolean)
    const catalogKeys = entry?.catalog_keys ?? []
    const groupMismatch = catalogKeys.some(key => key.group_diagnosis)
    const locations = entry?.website_locations ?? []
    const websitePrices = locations.filter(location => location.price_complete && location.price_conditions_exact)
    const showWebsitePrices = websitePrices.length > 0 && !row.quotes.some(quote =>
      (quote.status === 'confirmed' || quote.status === 'conditional') && quote.standard)
    const hasStudentPrice = row.quotes.some(quote => quote.mode === 'manual' || quote.actual)
    const quotedKeys = new Set(row.credentials.map(key => key.credential_id))
    const groupIssues = [...new Set([...keys].filter(id => !quotedKeys.has(id))
      .map(id => keyGroupIssue(billingByKey.value.get(id))).filter(Boolean))]
    const pricing = (row.quotes.length ? row.quotes : [null]).map(quote => ({
      key: quote?.key ?? 'missing',
      hasQuote: Boolean(quote),
      status: quote?.status ?? 'pending',
      statusLabel: groupMismatch && !quote?.issue && (!quote || quote.status === 'pending' || quote.status === 'not_listed')
        ? '分组报价需核对' : quote?.statusLabel ?? '单价待同步',
      multiplierLabel: quote?.multiplierLabel ?? (groupMismatch ? '请查看分组倍率' : undefined),
      notes: quote?.notes ?? [],
      official: displayPrice(quote, false),
      student: displayPrice(quote, true),
    }))
    return {
      ...row, active, activation, canDisable, keyCoverage, routeCount, readyCount, priceState, priceCovered, pricing, routeBlocked,
      blockers: [...new Set(blockerMessages)],
      priceBlocker: capability ? capability.priceBlocker : entry?.price_blocker?.message.trim(),
      listed,
      discovered: Boolean(source) || listed,
      websiteOnly,
      source,
      locations, websitePrices, showWebsitePrices, hasStudentPrice,
      catalogKeys, groupIssues,
      protocolCount: entry?.protocols?.length ?? (capability ? capability.protocols.length : protocols.size || (routeCount === 0 ? 0 : null)),
      protocolLabels: entry?.catalog_state ? entry.protocols?.map(protocolLabel) ?? [] : capability
        ? capability.protocols.map(protocolLabel)
        : entry?.protocols?.map(protocol => bindingAdapterMeta(protocol).label) ?? [],
      probeState: capability ? (capability.probeState === 'not_applicable' ? undefined : capability.probeState) : entry?.probe_state,
      recommendedProtocol: capability?.recommendedProtocol,
      availability: capability?.availability,
      availabilityReason: capability?.availabilityReason,
      actionHint: capability?.actionHint,
      hasCapability: Boolean(capability),
      bindingInvalid: capability?.bindingInvalid ?? false,
      protocolMissing: Boolean(capability && capability.protocols.length === 0),
      lastProbeAt: entry?.last_probe_at,
      bindingIssue: entry?.binding_issue,
    }
  })
})
type CatalogRow = (typeof catalog.value)[number]
const listedModels = computed(() => catalog.value.filter(row => row.listed))
const discoveredModels = computed(() => catalog.value.filter(row => row.discovered))
const probeRun = computed<ProbeRunWithBudget | undefined>(() => probeOverride.value ?? overview.value?.probe_run as ProbeRunWithBudget | undefined)
const probeBudget = computed(() => probeRun.value?.budget)
const probeInProgress = computed(() => probeRun.value?.state === 'queued' || probeRun.value?.state === 'running')
const probeLabels = { passed: '探活通过', failed: '探活失败', unprobed: '待探活' } as const
const probeRunLabel = computed(() => {
  switch (probeRun.value?.state) {
    case 'queued': return '探活已排队'
    case 'running': return '探活进行中'
    case 'completed': return '最近一轮探活已完成'
    case 'failed': return probeRun.value.error_code === 'no_token_protocols'
      ? '当前目录没有可进行 1 Token 探活的模型协议'
      : '探活未完成，可重新提交'
    default: return ''
  }
})
const catalogByModel = computed(() => new Map(catalog.value.map(row => [row.model.toLowerCase(), row])))
const modelFilter = computed(() => normalizeSignal(props.query ?? ''))
const searchedRows = computed(() => catalog.value.filter(row =>
  (!modelFilter.value || [row.model, ...row.locations.map(location => location.name), ...row.catalogKeys.map(key => key.upstream_model ?? '')]
    .some(name => normalizeSignal(name).includes(modelFilter.value))) &&
  (!props.commonIds || props.commonIds.has(row.model.toLowerCase()))))
const rows = computed(() => searchedRows.value.filter(row => filter.value === 'all' ||
  filter.value === 'active' && row.active ||
  filter.value === 'pending' && !row.active ||
  filter.value === 'unpriced' && !row.priceCovered ||
  filter.value === 'manual' && (row.mode !== 'follow' || row.priceState === 'manual')))
const batchRows = computed(() => rows.value.filter(row => row.listed || row.active && !row.websiteOnly))
const monthlyUsage = computed<UsageState>(() => usageStates.value.month ?? {})
const drawerUsage = computed<UsageState>(() => usageStates.value[range.value] ?? {})
const usageByModel = computed(() => new Map((monthlyUsage.value.data?.models ?? []).map(row => [row.model.toLowerCase(), row])))
const monthlyStats = computed(() => {
  const totals = monthlyUsage.value.data?.totals
  const hitRate = totals?.cache_hit_rate
  return [
    { label: '已扣积分', value: shiftDecimal(totals?.credits, 0) },
    { label: 'Token 总数', value: totals?.completeness === 'missing' && totals.tokens === 0 ? '—' : formatTokens(totals?.tokens) },
    { label: '缓存命中率', value: hitRate != null && hitRate >= 0 && hitRate <= 1 ? formatPercent(hitRate) : '—' },
    { label: '待核笔数', value: formatTokens(totals?.pending_calls) },
  ]
})

const batchSelection = ref<string[]>()
const batchBusy = ref(false)
const batchProgress = ref<{ completed: number; total: number }>()
const batchResults = ref<BatchOutcome[]>([])
const busy = computed(() => loading.value || syncing.value || rescanning.value || rebinding.value || Boolean(updating.value) || batchBusy.value)
const batchPlan = computed(() => planBatch(batchSelection.value ?? []))
const batchEligible = computed(() => batchPlan.value.filter(item => !item.reason))
const batchSkipped = computed(() => batchPlan.value.filter(item => item.reason))
const batchCounts = computed(() => ({
  enabled: batchResults.value.filter(row => row.status === 'enabled').length,
  skipped: batchResults.value.filter(row => row.status === 'skipped').length,
  failed: batchResults.value.filter(row => row.status === 'failed').length,
}))
const healthDimensions = [['credential', '凭据'], ['catalog', '目录'], ['pricing', '报价'], ['probe', '探活']] as const
const healthLabels: Record<BeichenHealthDimension['state'], string> = { available: '可用', unavailable: '异常', pending: '待确认' }
const keyHealth = computed(() => (overview.value?.credentials ?? []).flatMap(key => key.health ? [{
  id: key.credential_id, label: key.label, count: key.health.available_dimensions, health: key.health,
}] : []))
const keyCount = computed(() => {
  const observedKeys = new Set([
    ...(overview.value?.model_catalog ?? []).flatMap(row => row.credential_ids ?? []),
    ...(overview.value?.models ?? []).map(row => row.credential_id),
    ...(overview.value?.datasets ?? []).map(row => row.credential_id),
  ])
  const count = overview.value?.credentials?.length ??
    Math.max(validCount(overview.value?.website_sync?.channels) ?? 0, observedKeys.size)
  return catalog.value.reduce((total, row) => Math.max(total, row.keyCoverage), count)
})
const lastSync = computed(() => overview.value?.website_sync?.price_last_seen ??
  (overview.value?.datasets ?? []).filter(row => row.kind === 'prices' && row.last_success).map(row => row.last_success!).sort().at(-1))
const loginNeeded = computed(() => overview.value?.web_login_required ?? overview.value?.datasets?.some(row =>
  /重新授权|重新登录|完成登录验证|(?:官网|网站).*(?:授权|会话)(?:已)?(?:失效|过期)/u.test(row.last_error ?? '')))
const priceErrors = computed(() => [...new Set((overview.value?.datasets ?? [])
  .filter(row => row.kind === 'prices' && row.last_error && !row.official_price_missing &&
    !keyGroupIssue(billingByKey.value.get(row.credential_id)))
  .map(row => row.last_error as string))])
const workspaceUrl = computed(() => {
  const raw = overview.value?.maintenance_workspace?.launch_url
  if (typeof raw !== 'string' || !/^https:\/\/[^/@]+(?:\/|$)/i.test(raw)) return null
  // 只接受原始的主机和路径，拒绝参数、片段及浏览器会自动修正的分隔符。
  if (/[?#\\\s\u0000-\u001f\u007f]/u.test(raw)) return null
  try {
    const target = new URL(raw)
    // 同一主机即使端口不同，也可能收到学校的会话 Cookie。
    return target.hostname === window.location.hostname ? null : target.href
  } catch {
    return null
  }
})

let overviewRequest = 0
let initialized = false
let disposed = false
async function load() {
  const request = ++overviewRequest
  const providerId = props.providerId
  loading.value = true
  error.value = ''
  capabilities.value = undefined
  try {
    const result = await teacherAPI.beichenOverview(providerId, schoolDay())
    if (disposed || request !== overviewRequest) return
    overview.value = result
    if ((result.model_catalog ?? []).length && typeof teacherAPI.beichenCapabilities === 'function') {
      try {
        const capabilityResult = await teacherAPI.beichenCapabilities(providerId)
        if (!disposed && request === overviewRequest) capabilities.value = capabilityResult
      } catch {
        // 旧版后端没有能力接口时回退现有线路字段。
      }
    }
  } catch (e) {
    if (!disposed && request === overviewRequest) {
      overview.value = undefined
      capabilities.value = undefined
      error.value = apiErrorMessage(e, '读取北辰模型失败，请点击刷新模型重试')
    }
  } finally {
    if (!disposed && request === overviewRequest) loading.value = false
  }
}

async function loadUsage(period: BeichenUsageRange) {
  const providerId = props.providerId
  usageStates.value[period] = { loading: true }
  const request = usageStates.value[period]
  const isCurrent = () => !disposed && props.providerId === providerId && usageStates.value[period] === request
  try {
    const data = await teacherAPI.modelUsage(period, providerId)
    if (isCurrent()) usageStates.value[period] = { data }
  } catch (e) {
    if (isCurrent()) usageStates.value[period] = { error: apiErrorMessage(e, '读取用量失败，请重试') }
  }
}

function validCount(value?: number | null) {
  return value != null && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

function oldPriceState(row: SchoolCatalogRow): PriceState {
  const states = new Set<PriceState>(row.quotes.map(quote => {
    if (quote.mode === 'manual') return 'manual'
    if (quote.status === 'derived') return 'derived'
    if (hasPriceConditions(quote)) return 'conditional'
    return quote.status === 'confirmed' || quote.priceReady ? 'confirmed' : 'missing'
  }))
  return states.size > 1 ? 'mixed' : states.values().next().value ?? 'missing'
}

function publicationLabel(row: CatalogRow) {
  if (row.bindingIssue) return '已启用 · 线路异常'
  if (row.active) return '已启用'
  if (row.activation === 'ready') return '可启用'
  if (row.status === 'not_configured') return '未配置'
  return statusLabel(row.status ?? undefined) || '未发布'
}

const availabilityLabels: Record<string, string> = {
  available: '可用', maintenance: '维护中', unverified: '待验证', unavailable: '不可用', not_published: '未发布', website_only: '仅官网可见',
}
function rowAvailability(row: CatalogRow) {
  if (row.websiteOnly) return 'website_only'
  if (row.hasCapability && row.availability) return row.availability
  if (row.bindingIssue || row.probeState === 'failed') return 'maintenance'
  if (row.routeCount === 0) return 'unavailable'
  return row.active ? 'available' : 'not_published'
}
function availabilityLabel(row: CatalogRow) {
  return availabilityLabels[rowAvailability(row)] ?? '状态待同步'
}
function availabilityTone(row: CatalogRow) {
  const state = rowAvailability(row)
  return state === 'available' ? 'active' : state === 'maintenance' || state === 'unavailable' ? 'danger' : 'pending'
}

function keyCoverageLabel(key: BeichenCatalogKey) {
  const channel = key.school_channel || key.label
  const label = channel + (key.replacement ? ' 新 Key' : ' Key')
  const group = key.group_id == null ? '' : (key.group_stale ? ' · 上次分组 ' : ' · 分组 ') + key.group_id
  const details = (key.label === channel ? '' : ' · ' + key.label) + group
  const matchLabels = { exact: '', normalized: '名称归一后匹配', alias: '按教师别名匹配' } as const
  switch (key.state) {
    case 'listed':
      return label + (key.stale ? ' 上次目录有' : ' 目录有') + ' · ' + key.upstream_model
        + (key.match && matchLabels[key.match] ? ' · ' + matchLabels[key.match] : '') + details
    case 'missing': return key.group_diagnosis === 'model_outside_key_group'
      ? label + details + '。Key 分组不含该模型。'
      : '官网有、' + label + ' 无' + details + '。请核查分组后重扫。'
    case 'failed': return label + ' 目录读取失败' + details + '，请重扫。'
    case 'pending': return label + ' 目录待同步' + details + '。'
    case 'ambiguous': return label + ' 有多个近似名称，请核查目录和教师别名。'
  }
}

function rateLabel(value?: string | number | null) {
  const rate = shiftDecimal(value, 0)
  return rate === '—' ? '未知' : rate + '×'
}

function groupPriceComparison(key: BeichenCatalogKey, locations: BeichenWebsiteLocation[]) {
  const billing = billingByKey.value.get(key.credential_id)
  const rulesDiffer = key.group_diagnosis === 'group_rate_mismatch'
  const website = locations.flatMap(location => {
    if (location.stale || !location.has_price) return []
    const price = location.price_conditions_exact
      ? formatPriceConditions(location.price_conditions_exact, null, true).standard : null
    return location.groups
      .filter(group => group.id != null && (!group.status || group.status === 'active') &&
        (rulesDiffer ? group.id === key.group_id : group.id !== key.group_id))
      .map(group => `官网分组 ${group.id} 价 · 倍率 ${rateLabel(group.rate_multiplier)}` +
        (group.account_rate_multiplier != null ? ' · 账户倍率 ' + rateLabel(group.account_rate_multiplier) : '') +
        (group.peak_rate_enabled ? ' · 高峰系数 ' + rateLabel(group.peak_rate_multiplier) : '') +
        (price ? '\n' + withYuan(price) : ' · 单价明细未确认'))
  })
  const own = `Key 分组 ${key.group_id} 价 · 倍率 ${rateLabel(billing?.group_rate ?? billing?.effective_multiplier_text ?? billing?.effective_multiplier)}` +
    (billing?.peak_enabled ? ' · 高峰系数 ' + rateLabel(billing.peak_rate) : '') +
    (rulesDiffer ? ' · 分组计费规则待核对' : ' · 当前分组未列可匹配报价')
  return [...new Set(website), own]
}

function websiteLocationLabel(location: BeichenWebsiteLocation) {
  const groups = location.groups.map(group => [group.name, group.id != null ? '分组 ' + group.id : '',
    rateLabel(group.rate_multiplier), group.account_rate_multiplier != null ? '账户 ' + rateLabel(group.account_rate_multiplier) : ''].filter(Boolean).join(' '))
  return [location.channel || (location.channel_id != null ? '渠道 ' + location.channel_id : ''),
    location.platform, groups.length ? groups.join('、') : '分组待核', location.name,
    location.has_price ? '官网有报价' : '官网无报价',
    location.stale ? '缓存目录' : ''].filter(Boolean).join(' · ')
}

function websiteGroupPrice(location: BeichenWebsiteLocation) {
  if (!location.has_price || !location.price_conditions_exact) return ''
  const base = formatPriceConditions(location.price_conditions_exact, null, true).standard
  return base ? withYuan(base) : ''
}

function websitePriceHeading(location: BeichenWebsiteLocation) {
  const groups = location.groups.flatMap(group => group.id == null ? [] : [group.id])
  const title = location.stale ? '上次官网报价' : '官网报价'
  return groups.length ? title + ' · 分组 ' + groups.join('、') : title
}

function catalogPriceLabel(row: CatalogRow) {
  if (!row.priceCovered && row.catalogKeys.some(key => key.group_diagnosis)) return '分组报价需核对'
  if ((row.websiteOnly || row.priceState === 'missing') && row.locations.some(location => location.has_price)) {
    return '官网有报价，待 Key 确认'
  }
  return row.websiteOnly ? '官网未给出报价' : priceStateLabels[row.priceState] ?? '报价待同步'
}

function canEnableReason(row: CatalogRow) {
  if (!row.listed) return '当前没有渠道 Key 列出此模型'
  if (row.hasCapability) {
    if (row.bindingInvalid) return row.availabilityReason || row.bindingIssue?.message || '绑定失效，请检查渠道 Key 与模型协议后重绑。'
    if (row.availability === 'maintenance') return row.availabilityReason || '维护中，请检查线路后重新探活。'
    if (row.availability === 'unavailable') return row.availabilityReason || '当前没有可用线路，请检查渠道 Key 后重扫目录。'
    if (row.protocolMissing) return '当前没有可用协议，请重扫目录并核对官网支持的协议。'
  }
  if (row.activation === 'ready') return ''
  if (row.routeBlocked) return row.blockers.join('；') || '线路未就绪，请刷新模型或检查渠道配置'
  if (!row.priceCovered && row.websitePrices.length && row.catalogKeys.some(key => key.group_diagnosis)) {
    return '当前 Key 报价待核对，请检查分组。'
  }
  if (row.priceBlocker) return row.priceBlocker
  if (row.blockers.length) return row.blockers.join('；')
  if (row.activation == null && row.canEnable == null) return '启用条件待同步，请先刷新模型'
  if (!row.priceCovered && row.websitePrices.length) return '当前 Key 报价待核对，请检查分组。'
  if (!row.priceCovered) return row.priceState === 'conditional' ? '条件价待核实，可先手动定价' : '官网未报价，请先手动定价'
  if (row.activation === 'blocked' || !row.canEnable || !row.routeCount) return '线路未就绪，请刷新模型或检查渠道配置'
  return ''
}

function planBatch(models: string[]) {
  return models.map(model => {
    const row = catalogByModel.value.get(model.toLowerCase())
    const active = row?.active ?? false
    const canEnable = row?.listed && (row.activation === 'ready' || (row.activation == null && row.canEnable === true))
    const reason = active ? '已启用' : canEnable ? '' : row ? canEnableReason(row) : '当前目录中没有此模型'
    return { model, active, reason }
  })
}

async function refreshOverview() {
  if (!busy.value) await load()
}

async function syncPrices() {
  if (busy.value) return
  syncing.value = true
  error.value = ''
  try {
    await teacherAPI.syncBeichen(props.providerId)
    await load()
  } catch (e) {
    await load()
    error.value = apiErrorMessage(e, '立即同步失败，请稍后重试')
  } finally {
    syncing.value = false
    emit('catalog-synced')
  }
}

async function rescanCatalog(credentialId = rescanKey.value) {
  if (busy.value || !rescanKeys.value.some(key => key.credential_id === credentialId)) return
  const providerId = props.providerId
  const current = () => !disposed && props.providerId === providerId
  rescanning.value = true
  rescanError.value = ''
  try {
    await teacherAPI.syncBeichen(providerId, credentialId)
    if (current()) toast.success('所选渠道分组、倍率和目录已同步')
  } catch (e) {
    if (current()) rescanError.value = apiErrorMessage(e, '所选渠道重扫失败，请稍后重试')
  } finally {
    if (current()) {
      await load()
      if (current()) {
        rescanning.value = false
        emit('catalog-synced')
      }
    }
  }
}

async function previewRebind() {
  if (busy.value) return
  const providerId = props.providerId
  const current = () => !disposed && props.providerId === providerId
  rebindOpen.value = true
  rebinding.value = true
  rebindPlan.value = undefined
  rebindError.value = ''
  try {
    const result = await teacherAPI.previewBeichenRebind(providerId)
    if (current()) rebindPlan.value = result
  } catch (e) {
    if (current()) rebindError.value = apiErrorMessage(e, '生成绑定预览失败，请重试')
  } finally {
    if (current()) rebinding.value = false
  }
}

async function confirmRebind() {
  const plan = rebindPlan.value
  if (busy.value || !plan?.changes.length) return
  const providerId = props.providerId
  const current = () => !disposed && props.providerId === providerId
  rebinding.value = true
  try {
    const result = await teacherAPI.applyBeichenRebind(providerId, plan.preview_token)
    if (!current()) return
    rebindOpen.value = false
    toast.success(`已重建 ${result.changes.length} 条北辰线路`)
    await load()
    if (current()) emit('catalog-synced')
  } catch (e) {
    if (current()) {
      rebindPlan.value = undefined
      rebindError.value = apiErrorMessage(e, '应用绑定失败，请重新预览')
    }
  } finally {
    if (current()) rebinding.value = false
  }
}

function routeDescription(route: BeichenRebindRoute) {
  const channel = route.credential_label ?? route.credential_id ?? '自动选 Key'
  return `${channel} · ${route.upstream_model} · 优先级 ${route.priority} · ${route.status === 'active' ? '启用' : '停用'}`
}

async function requestProbes() {
  if (probeSubmitting.value || probeInProgress.value || probeBudget.value?.exhausted) return
  const providerId = props.providerId
  const current = () => !disposed && props.providerId === providerId
  probeSubmitting.value = true
  probeError.value = ''
  try {
    const run = await teacherAPI.requestBeichenProbes(providerId)
    if (current()) probeOverride.value = run
  } catch (e) {
    if (current()) probeError.value = apiErrorMessage(e, '提交探活失败，请稍后重试')
  } finally {
    if (current()) probeSubmitting.value = false
  }
}

async function pollProbes() {
  if (!probeInProgress.value) return
  const providerId = props.providerId
  try {
    const view = await teacherAPI.beichenProbes(providerId)
    if (disposed || props.providerId !== providerId) return
    probeOverride.value = view.run
    probeError.value = ''
    if (!probeInProgress.value) {
      await load()
      if (!disposed && props.providerId === providerId && overview.value?.probe_run) probeOverride.value = undefined
    }
  } catch (e) {
    if (!disposed && props.providerId === providerId) probeError.value = apiErrorMessage(e, '读取探活进度失败，稍后会自动重试')
  }
}

async function changeStatus(row: CatalogRow, enable: boolean) {
  if (busy.value || enable && (row.active || canEnableReason(row))) return
  if (!enable && !window.confirm(`禁用 ${row.model} 后，学生将无法发起新的调用，确定继续？`)) return
  updating.value = row.model
  error.value = ''
  try {
    if (enable) {
      await teacherAPI.enableBeichenModel(props.providerId, row.model, row.school)
    } else {
      await teacherAPI.updateModel({ model_id: row.school, status: 'inactive' })
    }
    await load()
    toast.success(enable ? '模型已启用' : '模型已禁用')
  } catch (e) {
    error.value = apiErrorMessage(e, '模型状态更新失败')
  } finally {
    updating.value = ''
  }
}

function previewBatch() {
  if (busy.value) return
  batchSelection.value = Array.from(new Map(batchRows.value.map(row => [row.model.toLowerCase(), row.model])).values())
  batchProgress.value = undefined
  batchResults.value = []
}
async function enableBatch(retry = false) {
  const selection = batchSelection.value
  if (!selection || busy.value || (retry ? !batchCounts.value.failed : !batchEligible.value.length || batchProgress.value)) return
  const providerId = props.providerId
  const isCurrent = () => !disposed && props.providerId === providerId && batchSelection.value === selection
  batchBusy.value = true
  try {
    const failures = batchResults.value.filter(item => item.status === 'failed').map(item => item.model)
    if (retry && !planBatch(failures).some(item => !item.reason)) await load()
    if (!isCurrent() || !overview.value) return
    const plan = retry ? planBatch(failures) : batchPlan.value
    const models = plan.filter(item => !item.reason).map(item => item.model)
    const results = new Map(batchResults.value.map(item => [item.model, item]))
    for (const item of plan) {
      if (!item.reason) results.delete(item.model)
      else if (!retry || item.active) results.set(item.model, { model: item.model, status: 'skipped', reason: item.reason })
      else results.set(item.model, { model: item.model, status: 'failed', reason: item.reason })
    }
    batchResults.value = Array.from(results.values())
    batchProgress.value = { completed: 0, total: models.length }
    for (let offset = 0; offset < models.length; offset += 200) {
      const chunk = models.slice(offset, offset + 200)
      let outcomes: BatchOutcome[]
      try {
        const result = await teacherAPI.enableBeichenModelsBatch(providerId, chunk)
        const enabled = new Set((result.enabled ?? []).map(item => item.model.toLowerCase()))
        const skipped = new Map((result.skipped ?? []).map(item => [item.model.toLowerCase(), item]))
        outcomes = chunk.map(model => {
          const key = model.toLowerCase()
          if (enabled.has(key)) return { model, status: 'enabled', reason: '已启用' }
          const item = skipped.get(key)
          if (item?.reason === 'already_active') return { model, status: 'skipped', reason: item.message || '已启用' }
          return { model, status: 'failed', reason: item ? item.message || '线路或价格未就绪' : '服务器未返回结果，请刷新后重试' }
        })
      } catch (cause) {
        const reason = apiErrorMessage(cause, '启用失败，请稍后重试')
        outcomes = chunk.map(model => ({ model, status: 'failed', reason }))
      }
      if (!isCurrent()) return
      for (const outcome of outcomes) results.set(outcome.model, outcome)
      batchResults.value = Array.from(results.values())
      batchProgress.value.completed += chunk.length
    }
    if (models.length) {
      const counts = batchCounts.value
      const message = `已启用 ${counts.enabled} 个，跳过 ${counts.skipped} 个，失败 ${counts.failed} 个`
      if (counts.failed) toast.error(message)
      else toast.success(message)
      await load()
    }
  } finally {
    if (isCurrent()) batchBusy.value = false
  }
}

function openUsage(model = '') {
  initialModel.value = model
  range.value = 'month'
  usageOpen.value = true
}
function changeUsageRange(period: BeichenUsageRange) {
  if (range.value === period) return
  range.value = period
  if (!usageStates.value[period]) void loadUsage(period)
}
async function priceSaved() {
  toast.success('价格已保存，后续调用采用新计价方式')
  await load()
}
function syncTime(value?: string | null) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return '尚未同步'
  return new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}
function hasPriceConditions(quote: SchoolQuote) {
  return quote.status === 'conditional' || /上下文分档|推理强度/u.test((quote.standard ?? '') + (quote.actual ?? ''))
}

function withYuan(text: string) {
  return text.split('\n').map(line => {
    const [unit, ...amounts] = line.split(' · ')
    if (!convertibleUnits.has(unit)) return line
    return [unit, ...amounts.map(amount => amount.replace(/ (\d+(?:\.\d+)?)$/, (_match, credits: string) =>
      ` ${credits} 积分（${shiftDecimal(credits, -2)} 元）`))].join(' · ')
  }).join('\n')
}

function displayPrice(quote: SchoolQuote | null, student: boolean) {
  const manual = student && quote?.mode === 'manual'
  if (quote && hasPriceConditions(quote) && !manual) {
    const terms = quote.conditions
    const showBase = !student && terms?.billing_mode === 'token' &&
      (terms.cache_write_1h_price != null || terms.cache_write_12h_price != null) &&
      !terms.intervals?.length && !Object.keys(terms.reasoning_effort_multipliers ?? {}).length
    return { conditional: true, showBase, detail: schoolQuoteDetail(quote, student), details: '' }
  }
  if (!quote) return { conditional: false, detail: emptyPriceDetail(), details: '' }
  const text = (manual ? '' : student ? quote.actual : quote.standard) ?? ''
  const [first, ...other] = text.split('\n')
  const tokenLine = first.startsWith(tokenPriceUnit) ? first : ''
  return {
    conditional: false,
    detail: schoolQuoteDetail(quote, student),
    details: withYuan(tokenLine ? other.join('\n') : text),
  }
}

function missingPriceLabel(row: CatalogRow) {
  const labels = new Set<string>()
  for (const quote of row.quotes) {
    for (const label of missingPriceDetailLabels(quote.conditions)) labels.add(label)
  }
  const requiredMissing = labels.has('输入') || labels.has('输出') || labels.has('每次请求') || labels.has('每秒') || labels.has('图像单价')
  if (requiredMissing) return '缺价：' + [...labels].join('、')
  if (row.priceBlocker) return '缺价：' + row.priceBlocker
  if (labels.size) return '缺价：' + [...labels].join('、')
  return '缺价：输入、输出、缓存写入(5m)、缓存写入(1h)、缓存读取'
}

function modelUsage(model: string) { return usageByModel.value.get(model.toLowerCase()) }
function modelCredits(model: string) {
  const credits = shiftDecimal(modelUsage(model)?.credits, 0)
  return credits === '—' ? credits : credits + ' 积分'
}
function modelTokens(model: string) {
  const usage = modelUsage(model)
  return usage && !(usage.completeness === 'missing' && usage.tokens === 0) ? formatTokens(usage.tokens) : '—'
}

watch(() => props.providerId, () => {
  overview.value = undefined
  probeOverride.value = undefined
  probeError.value = ''
  probeSubmitting.value = false
  rescanKey.value = ''
  rescanning.value = false
  rescanError.value = ''
  rebindOpen.value = false
  rebindPlan.value = undefined
  rebinding.value = false
  rebindError.value = ''
  usageStates.value = {}
  priceRow.value = undefined
  conditionsRow.value = undefined
  usageOpen.value = false
  batchSelection.value = undefined
  batchProgress.value = undefined
  batchResults.value = []
  batchBusy.value = false
  initialized = true
  void Promise.all([load(), loadUsage('month')])
})
useVisibilityPolling(async () => {
  if (busy.value) return
  if (!initialized) {
    initialized = true
    await Promise.all([load(), loadUsage('month')])
  } else await load()
}, 60_000)
useVisibilityPolling(pollProbes, 5_000)
onBeforeUnmount(() => { disposed = true; overviewRequest++ })
</script>

<template>
  <section v-if="providerMaintenance && overview?.maintenance_workspace" aria-label="校方北辰工作区">
    <a v-if="workspaceUrl" class="button secondary" :href="workspaceUrl" target="_blank"
      rel="noopener noreferrer" referrerpolicy="no-referrer">打开校方北辰工作区</a>
    <p v-else class="muted">校方尚未配置远程应用地址</p>
  </section>
  <BeichenRecentReceipts v-if="providerMaintenance" :provider-id="providerId" :website-sync="overview?.website_sync"
    @refresh-overview="refreshOverview" />
  <section class="beichen-stack beichen-models" aria-label="北辰模型目录" :aria-busy="loading">
    <section class="beichen-card monthly-usage" aria-label="本月用量汇总" :aria-busy="monthlyUsage.loading">
      <div class="beichen-section-head">
        <h2>本月用量</h2>
        <button type="button" class="button ghost" @click="openUsage()">查看全部明细</button>
      </div>
      <dl class="monthly-stats">
        <div v-for="stat in monthlyStats" :key="stat.label">
          <dt>{{ stat.label }}</dt><dd>{{ monthlyUsage.loading ? '…' : stat.value }}</dd>
        </div>
      </dl>
      <div v-if="monthlyUsage.error" class="error usage-feedback" role="alert">
        <span>{{ monthlyUsage.error }}</span>
        <button type="button" class="button ghost" @click="loadUsage('month')">重试用量</button>
      </div>
    </section>
    <div class="beichen-summary model-summary" aria-label="模型概况">
      <div class="beichen-stat"><span>目录模型</span><strong>{{ overview ? formatTokens(discoveredModels.length) : '—' }}</strong><small>已从上游发现</small></div>
      <div class="beichen-stat"><span>已启用</span><strong>{{ overview ? formatTokens(catalog.filter(row => row.active).length) : '—' }}</strong><small>学校模型</small></div>
      <div class="beichen-stat"><span>报价覆盖</span><strong>{{ overview ? discoveredModels.filter(row => row.priceCovered).length + ' / ' + discoveredModels.length : '—' }}</strong><small>含可计算的 Token 条件价与手动价</small></div>
      <div class="beichen-stat sync-stat"><span>价格最后同步</span><strong>{{ syncTime(lastSync) }}</strong><small>北京时间</small></div>
    </div>
    <section v-if="keyHealth.length" class="beichen-card key-health" aria-label="Key 健康">
      <h3>Key 健康</h3>
      <details v-for="key in keyHealth" :key="key.id">
        <summary><strong>{{ key.label }}</strong> <span class="status-pill" :class="key.count === 4 ? 'active' : 'pending'">{{ key.count }}/4 维可用</span></summary>
        <dl>
          <div v-for="[dimension, label] in healthDimensions" :key="dimension" :data-state="key.health[dimension].state">
            <dt>{{ label }} · {{ healthLabels[key.health[dimension].state] }}</dt>
            <dd>{{ key.health[dimension].message }}</dd>
          </div>
        </dl>
      </details>
    </section>
    <section v-if="billingRows.length" class="beichen-card" aria-label="北辰 Key 计费倍率">
      <h3>Key 分组与倍率</h3>
      <p v-for="billing in billingRows" :key="billing.credential_id" :role="billing.alert ? 'alert' : undefined" class="beichen-note">
        <strong>{{ billing.label }}</strong>
        <template v-if="billing.group_id != null"> · {{ billing.billing_state === 'stale' && !billing.group_status ? '上次分组' : '分组' }} {{ billing.group_id }}<template v-if="billing.group_name"> · {{ billing.group_name }}</template></template> · {{ billing.billing_state === 'stale' ? '上次生效倍率' : '生效倍率' }} {{ rateLabel(billing.effective_multiplier_text ?? billing.effective_multiplier) }}
        <template v-if="billing.group_rate != null"> · 分组倍率 {{ billing.group_status ? keyGroupRate(billing.group_rate) : rateLabel(billing.group_rate) }}</template>
        <template v-if="billing.peak_enabled"> · 高峰系数 {{ rateLabel(billing.peak_rate) }}</template>
        <strong v-if="billing.billing_state === 'stale'"> · 倍率已陈旧，保留上次记录</strong>
        <span v-else-if="billing.billing_state === 'unverified'"> · 计费倍率待核实</span>
        · 上次成功 {{ syncTime(billing.last_success) }}
        <strong v-if="billing.alert"> · 计费信息连续读取失败 {{ billing.failure_count === 6 ? '至少 6' : billing.failure_count }} 次</strong>
        <span v-if="keyGroupIssue(billing) || billing.last_error || billing.group_id == null"> · {{ keyGroupIssue(billing) || billing.last_error || '分组待同步' }}</span>
      </p>
    </section>
    <BeichenKeyCapabilities v-if="overview?.key_capabilities?.length" :key="providerId"
      :provider-id="providerId" :capabilities="overview.key_capabilities" :disabled="busy" @refresh="refreshOverview" />
    <section v-if="overview?.website_only_status" class="beichen-card website-catalog" aria-label="官网目录">
      <div class="beichen-section-head">
        <h3>官网目录</h3>
        <form v-if="rescanKeys.length" class="actions" @submit.prevent="rescanCatalog()">
          <label class="sr-only" :for="reasonId + '-rescan-key'">选择要重扫的渠道 Key</label>
          <select :id="reasonId + '-rescan-key'" v-model="rescanKey" :disabled="busy">
            <option value="">选择渠道 Key</option>
            <option v-for="key in rescanKeys" :key="key.credential_id" :value="key.credential_id">{{ key.label }}</option>
          </select>
          <button type="submit" class="button secondary" :disabled="busy || !rescanKeyAvailable">{{ rescanning ? '重扫中…' : '用指定 Key 重扫' }}</button>
        </form>
      </div>
      <p v-if="websiteDirectory?.state === 'stale'" class="beichen-note" role="status">
        {{ websiteDirectory.last_success ? '保留上次官网目录（待更新）' : '官网目录部分待更新' }}<template v-if="websiteDirectory.last_error"> · {{ websiteDirectory.last_error }}</template>
      </p>
      <p v-else-if="websiteDirectory?.state === 'unverified'" class="beichen-note">官网完整目录待同步<template v-if="websiteDirectory.last_error"> · {{ websiteDirectory.last_error }}</template></p>
      <p v-if="websiteDirectory?.last_success" class="beichen-note">最近完整读取 {{ syncTime(websiteDirectory.last_success) }}（北京时间）</p>
      <template v-if="lastCrawl">
        <p class="beichen-note">
          上次抓取 {{ syncTime(lastCrawl.started_at) }}（北京时间），读到 {{ lastCrawl.model_count }} 个模型、{{ lastCrawl.platform_count }} 个平台、{{ lastCrawl.group_count }} 个分组，耗时 {{ lastCrawl.duration_ms }} 毫秒
          <template v-if="lastCrawl.model_count !== lastCrawl.expected_model_count">。响应中能辨认出 {{ lastCrawl.expected_model_count }} 个不同型号。</template>
        </p>
        <ul v-if="lastCrawl.failures.length" class="beichen-note" aria-label="抓取失败项">
          <li v-for="failure in lastCrawl.failures" :key="failure.path">
            {{ [failure.channel || (failure.channel_id ? `渠道 ${failure.channel_id}` : ''), failure.platform].filter(Boolean).join(' / ') || '官网响应' }} · {{ failure.reason }}
            <small v-if="!failure.platform && failure.path.startsWith('data[')">（{{ failure.path }}）</small>
          </li>
        </ul>
        <p v-if="lastCrawl.key_model_error" class="beichen-note" role="status">{{ lastCrawl.key_model_error }}</p>
        <p v-if="lastCrawl.account_rate_error" class="beichen-note" role="status">{{ lastCrawl.account_rate_error }}</p>
        <details v-if="lastCrawl.key_model_comparisons?.length" class="beichen-note">
          <summary>Key 实际型号核对</summary>
          <div v-for="comparison in lastCrawl.key_model_comparisons" :key="comparison.channel">
            <p>
              <strong>{{ comparison.channel }}</strong>
              <template v-if="comparison.group_id != null"> · 分组 {{ comparison.group_id }}</template>
              <template v-if="comparison.state === 'checked'">
                · 匹配 {{ comparison.matched.length }} · 仅 Key {{ comparison.only_key.length }} · 仅官网 {{ comparison.only_channel.length }}
              </template>
              <template v-else> · {{ comparison.error ?? '目录待核实' }}</template>
            </p>
            <ul v-if="comparison.state === 'checked'">
              <li v-for="match in comparison.matched" :key="match.key_model">
                {{ match.key_model }}<template v-if="match.key_model !== match.channel_model"> → {{ match.channel_model }}</template>
                <template v-if="!match.price_complete">（报价待核对）</template>
              </li>
              <li v-if="comparison.only_key.length">仅 Key · {{ comparison.only_key.join('、') }}</li>
              <li v-if="comparison.only_channel.length">仅官网 · {{ comparison.only_channel.join('、') }}</li>
            </ul>
          </div>
        </details>
      </template>
    </section>
    <p v-if="rescanError" class="error" role="alert">{{ rescanError }}</p>
    <div class="beichen-toolbar model-toolbar">
      <div class="beichen-toolbar-group">
        <button type="button" class="button ghost" :disabled="busy" @click="refreshOverview">刷新模型</button>
        <button type="button" class="button secondary" :disabled="busy" @click="syncPrices">{{ syncing ? '同步中…' : '立即同步价格' }}</button>
        <button v-if="probeRun" type="button" class="button secondary" :disabled="probeSubmitting || probeInProgress || probeBudget?.exhausted || !listedModels.length" @click="requestProbes">
          {{ probeSubmitting ? '提交中…' : probeBudget?.exhausted ? '今日已达上限' : '立即探活' }}
        </button>
        <button type="button" class="button secondary" :disabled="busy" @click="previewRebind">重建绑定</button>
        <button type="button" class="button primary" :disabled="busy || !batchRows.length" @click="previewBatch">一键启用</button>
      </div>
    </div>
    <p v-if="probeRunLabel" class="beichen-note" role="status">{{ probeRunLabel }}</p>
    <p v-if="probeBudget" class="beichen-note" data-testid="probe-budget" role="status">
      今日探活 {{ probeBudget.used }}/{{ probeBudget.limit }} 次 · 剩余 {{ probeBudget.remaining }} 次<template v-if="probeBudget.exhausted"> · 已达上限，剩余模型明日继续</template>
    </p>
    <p v-if="probeError" class="error" role="alert">{{ probeError }}</p>
    <div class="model-filters" role="group" aria-label="模型状态筛选"><button v-for="item in filters" :key="item.id" type="button" :aria-pressed="filter === item.id" @click="filter = item.id">{{ item.label }}</button><small>当前筛选 {{ rows.length }} 个模型</small></div>
    <p class="beichen-note">官网报价单位为{{ websiteDirectory?.price_unit ?? '元（北辰余额单位）' }}。Token 单价按每百万 Token 计，1 元 = 100 积分。跟随官网时，学生实价按该线路的倍率计算。</p>
    <p v-if="error && !batchSelection" class="error" role="alert">{{ error }}</p>
    <p v-if="providerMaintenance && loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="priceErrors.length" class="error" role="alert">价格同步失败：{{ priceErrors.join('；') }}</p>
    <div v-if="loading && !overview" class="model-skeleton" role="status"><span class="sr-only">正在读取模型…</span><i v-for="index in 3" :key="index" aria-hidden="true" /></div>
    <div v-if="rows.length" class="model-list" data-layout="responsive-cards">
      <table class="beichen-table model-table">
        <caption class="sr-only">北辰模型覆盖、价格与本月用量</caption>
        <colgroup><col class="model-col"><col class="state-col"><col class="price-col"><col class="price-col"><col class="usage-col"><col class="action-col"></colgroup>
        <thead>
          <tr><th scope="col">模型</th><th scope="col">状态与覆盖</th><th v-for="column in priceColumns" :key="column.id" scope="col">{{ column.label }}</th><th scope="col">本月用量</th><th scope="col">操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="row.model" :data-model="row.model" :class="{ 'binding-issue-row': row.bindingIssue }">
            <th scope="row" class="model-name">{{ row.model }}</th>
            <td data-label="状态与覆盖" class="model-state">
              <span class="model-state-summary">
                <span>{{ row.keyCoverage }}/{{ keyCount }} 把 Key 覆盖</span> ·
                <span :title="row.routeCount + ' 条候选线路'">{{ row.protocolCount == null ? '协议数待同步' : row.protocolCount + ' 种协议' }}</span> ·
                <span>{{ row.readyCount }} 条已发布</span> ·
                <span>{{ catalogPriceLabel(row) }}</span> ·
                <strong :class="row.bindingIssue ? 'binding-issue' : row.active ? 'published-state' : undefined">{{ publicationLabel(row) }}</strong>
              </span>
              <small v-if="row.source" class="row-note" data-testid="catalog-source">来源 {{ sourceLabels[row.source] }}</small>
              <small v-for="key in row.catalogKeys.filter(key => key.state !== 'listed')" :key="key.credential_id"
                class="row-note" data-testid="key-catalog-gap">{{ keyCoverageLabel(key) }}</small>
              <div v-for="key in row.catalogKeys.filter(key => key.group_diagnosis)" :key="'group-' + key.credential_id"
                class="row-note" data-testid="group-price-comparison">
                <div v-for="line in groupPriceComparison(key, row.locations)" :key="line" class="price-detail">{{ line }}</div>
                <p v-if="key.group_diagnosis === 'model_outside_key_group'">请在北辰切换到含该型号的分组，或在
                  <RouterLink :to="{ name: 'teacher-providers' }">供应商管理</RouterLink>换用对应分组的 Key。</p>
                <button type="button" class="button ghost" :disabled="busy" @click="rescanCatalog(key.credential_id)">重扫该 Key</button>
              </div>
              <small v-if="row.keyCoverage === 0 && !row.catalogKeys.length" class="row-note">
                {{ row.websiteOnly ? '暂无匹配的渠道 Key，请根据官网分组核对。' : '当前启用 Key 无目录覆盖，请查看 Key 健康和目录同步状态。' }}
              </small>
              <details v-if="row.locations.length || row.catalogKeys.some(key => key.state === 'listed')" class="catalog-evidence">
                <summary>目录来源与分组</summary>
                <ul>
                  <li v-for="(location, sourceIndex) in row.locations" :key="sourceIndex">
                    {{ websiteLocationLabel(location) }} · {{ syncTime(location.observed_at) }}
                    <div v-for="line in websiteGroupPrice(location).split('\n').filter(Boolean)" :key="line" class="row-note">{{ line }}</div>
                  </li>
                  <li v-for="key in row.catalogKeys.filter(key => key.state === 'listed')" :key="key.credential_id">{{ keyCoverageLabel(key) }}</li>
                </ul>
              </details>
              <div class="model-capability" data-testid="model-capability">
                <span class="status-pill" :class="availabilityTone(row)" data-testid="model-availability">{{ availabilityLabel(row) }}</span>
                <span v-if="!row.websiteOnly" class="chip recommend" data-testid="recommended-protocol">{{ row.recommendedProtocol
                  ? '推荐 ' + protocolLabel(row.recommendedProtocol)
                  : row.hasCapability ? '暂无推荐协议' : '推荐协议待同步' }}</span>
                <span v-if="row.bindingInvalid" class="status-pill danger" data-testid="binding-invalid">绑定失效</span>
                <span v-if="!row.websiteOnly && row.protocolMissing" class="status-pill pending" data-testid="protocol-missing">缺协议</span>
                <span v-if="!row.websiteOnly && row.priceState === 'missing' && !row.websitePrices.length" class="status-pill pending" data-testid="price-missing">{{ missingPriceLabel(row) }}</span>
              </div>
              <small v-for="issue in row.groupIssues" :key="issue" class="row-note">{{ issue }}</small>
              <small v-if="!row.websiteOnly && row.availabilityReason" class="row-note" data-testid="availability-reason">{{ row.availabilityReason }}</small>
              <small v-if="!row.websiteOnly && row.actionHint" class="row-note" data-testid="action-hint">下一步：{{ row.actionHint }}</small>
              <small v-if="row.bindingIssue" class="row-note binding-issue">{{ row.bindingIssue.message }}</small>
              <small v-if="row.protocolLabels.length" class="row-note">{{ row.protocolLabels.join(' · ') }}</small>
              <div v-if="!row.websiteOnly && row.probeState" class="model-probe">
                <span class="status-pill" :class="{ active: row.probeState === 'passed', danger: row.probeState === 'failed', pending: row.probeState === 'unprobed' }">{{ probeLabels[row.probeState] }}</span>
                <small v-if="row.lastProbeAt">{{ syncTime(row.lastProbeAt) }}（北京时间）</small>
              </div>
            </td>
            <td v-for="column in priceColumns" :key="column.id" :data-label="column.label"
              :class="column.id === 'official' ? 'standard-price' : 'actual-price'">
              <template v-if="column.id === 'official' && row.showWebsitePrices">
                <div v-for="(location, index) in row.websitePrices" :key="index" class="quote-row">
                  <BeichenPriceDetail v-if="location.price_conditions_exact?.billing_mode === 'token' &&
                    !location.price_conditions_exact.intervals?.length"
                    :title="websitePriceHeading(location)"
                    :detail="webPriceDetail(location.price_conditions_exact, null, 'website', location.observed_at)" />
                  <template v-else>
                    <span>{{ websitePriceHeading(location) }}</span>
                    <p class="price-line">{{ websiteGroupPrice(location) }}</p>
                  </template>
                </div>
              </template>
              <p v-if="column.id === 'student' && row.showWebsitePrices && !row.hasStudentPrice" class="price-empty">当前 Key 报价待核对</p>
              <div v-else v-for="(quote, quoteIndex) in column.id === 'official' && row.showWebsitePrices
                ? row.pricing.filter(quote => quote.status === 'derived') : row.pricing"
                :key="quote.key" class="quote-row" :data-price-status="quote.status">
                <div class="quote-caption">
                  <span v-if="row.pricing.length > 1">报价 {{ quoteIndex + 1 }}</span>
                  <span v-if="column.id === 'official'" class="status-pill" :class="quote.status">{{ quote.statusLabel }}</span>
                  <span v-else>{{ quote.multiplierLabel ?? '倍率待同步' }}</span>
                  <span v-if="quote.hasQuote" class="status-pill price-source" :data-source="quote[column.id].detail.source">{{ quote[column.id].detail.sourceLabel }}</span>
                  <small v-if="quote.hasQuote" class="price-observed">observed_at {{ quote[column.id].detail.observedLabel }}</small>
                </div>
                <template v-if="quote[column.id].conditional">
                  <button v-if="column.id === 'official'" type="button" class="price-link" @click="conditionsRow = row">查看档位</button>
                  <span v-else class="price-empty">随计价档位变化</span>
                </template>
                <template v-if="quote.hasQuote && quote[column.id].detail && (!quote[column.id].conditional || quote[column.id].showBase)">
                  <p class="billing-mode-line">计费模式：{{ quote[column.id].detail.billingModeLabel }}</p>
                  <dl v-if="quote[column.id].detail.rows.some(row => !row.notApplicable)" class="price-table">
                    <div v-for="cell in quote[column.id].detail.rows" :key="cell.key" class="price-row price-cell"
                      :data-key="cell.key" :data-missing="cell.missing">
                      <dt class="price-label">{{ cell.label }}</dt>
                      <dd class="price-value">
                        <span class="price-amount">
                          <template v-if="cell.notApplicable">
                            <span class="price-figure price-empty">—</span>
                            <small class="price-gap price-not-applicable">不适用：{{ cell.label }}</small>
                          </template>
                          <template v-else-if="cell.missing">
                            <span class="price-figure price-yuan">—</span>
                            <span class="price-figure price-credits">—</span>
                            <span class="price-unit">/ 1M Token</span>
                            <small class="price-gap">{{ cell.key === 'cache_write_1h' ? '未列单价' : '缺价' }}：{{ cell.label }}</small>
                          </template>
                          <template v-else>
                            <span class="price-figure price-yuan">{{ cell.yuan }}<small class="price-unit-mark"> 元</small></span>
                            <span class="price-figure price-credits">{{ cell.credits }}<small class="price-unit-mark"> 积分</small></span>
                            <span class="price-unit">/ 1M Token</span>
                            <small v-if="cell.shared" class="price-shared">与缓存写入(5m)同价</small>
                          </template>
                        </span>
                      </dd>
                    </div>
                  </dl>
                </template>
                <p v-if="quote[column.id].details" class="price-line">{{ quote[column.id].details }}</p>
                <span v-else-if="!quote.hasQuote" class="price-empty">缺价</span>
                <template v-if="column.id === 'official'">
                  <small v-for="note in quote.notes" :key="note" class="row-note">{{ note }}</small>
                </template>
              </div>
            </td>
            <td data-label="本月用量" class="model-usage">
              <button type="button" class="usage-link" :aria-label="'查看 ' + row.model + ' 用量'" @click="openUsage(row.model)">{{ monthlyUsage.loading ? '…' : modelCredits(row.model) }}</button>
              <small class="row-note">Token {{ modelTokens(row.model) }}</small>
              <small v-if="modelUsage(row.model)?.pending_calls" class="row-note">待核 {{ formatTokens(modelUsage(row.model)?.pending_calls) }} 笔</small>
            </td>
            <td data-label="操作">
              <div class="model-actions">
                <button v-if="row.listed" type="button" class="button ghost" :disabled="busy" :aria-label="'定价 ' + row.model" @click="priceRow = row">{{ row.mode !== 'follow' ? '调整价格' : '手动定价' }}</button>
                <button v-if="!row.active && !row.websiteOnly" type="button" class="button secondary" :disabled="busy || Boolean(canEnableReason(row))"
                  :aria-describedby="canEnableReason(row) ? reasonId + '-' + index : undefined" :aria-label="'启用 ' + row.model"
                  @click="changeStatus(row, true)">{{ updating === row.model ? '启用中…' : '启用' }}</button>
                <button v-else-if="row.canDisable" type="button" class="button ghost" :disabled="busy"
                  :aria-label="'禁用 ' + row.model" @click="changeStatus(row, false)">禁用</button>
                <small v-if="!row.active && !row.websiteOnly && canEnableReason(row)" :id="reasonId + '-' + index" class="row-note enable-reason">{{ canEnableReason(row) }}</small>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else-if="!loading && !error" class="beichen-empty"><strong>{{ catalog.length ? '当前筛选没有匹配模型' : '暂无模型记录' }}</strong><span>{{ catalog.length ? '调整搜索、常用模型或状态筛选后再试。' : '后台会自动发现上游模型，也可以立即刷新目录。' }}</span><button type="button" class="button ghost" @click="catalog.length ? filter = 'all' : refreshOverview()">{{ catalog.length ? '查看全部状态' : '刷新模型' }}</button></div>
  </section>

  <BeichenPriceDrawer :open="Boolean(priceRow)" :provider-id="providerId" :row="priceRow" @update:open="open => { if (!open) priceRow = undefined }" @saved="priceSaved" />
  <BeichenModelUsageDrawer v-model:open="usageOpen" :range="range" :usage="drawerUsage.data" :loading="drawerUsage.loading"
    :error="drawerUsage.error" :initial-model="initialModel" @update:range="changeUsageRange" @retry="loadUsage(range)" />
  <AppDialog :open="rebindOpen" title="预览北辰绑定重建" description="按当前渠道目录和模型别名更新线路。逐条确认变化后再执行。" wide
    @update:open="open => { if (!open && !rebinding) rebindOpen = false }">
    <section class="beichen-stack" :aria-busy="rebinding">
      <p v-if="rebinding && !rebindPlan" role="status">正在读取绑定预览…</p>
      <p v-if="rebindError" class="error" role="alert">{{ rebindError }}</p>
      <template v-if="rebindPlan">
        <p v-if="!rebindPlan.changes.length" role="status">当前绑定与目录一致。</p>
        <template v-else>
          <p role="status">将影响 {{ rebindModelCount }} 个校内模型、{{ rebindPlan.changes.length }} 条北辰线路。</p>
          <ul class="rebind-changes">
            <li v-for="row in rebindPlan.changes" :key="row.model_id + '/' + row.adapter">
              <strong>{{ row.school_model }} · {{ adapterLabel(row.adapter) }} · {{ rebindActions[row.action] }}</strong>
              <span v-if="row.before">当前 {{ routeDescription(row.before) }}</span>
              <span>重建后 {{ routeDescription(row.after) }}</span>
            </li>
          </ul>
        </template>
      </template>
      <div class="actions">
        <button type="button" class="button ghost" :disabled="rebinding" @click="rebindOpen = false">关闭预览</button>
        <button v-if="rebindError" type="button" class="button secondary" :disabled="busy" @click="previewRebind">重新预览</button>
        <button v-if="rebindPlan?.changes.length" type="button" class="button primary" :disabled="busy" @click="confirmRebind">
          {{ rebinding ? '重建中…' : '确认重建' }}
        </button>
      </div>
    </section>
  </AppDialog>
  <AppDialog :open="Boolean(batchSelection)" title="一键启用当前筛选模型" description="确认当前筛选结果中的可启用模型，其他模型将跳过。"
    @update:open="open => { if (!open && !batchBusy) batchSelection = undefined }">
    <section class="batch-preview beichen-stack" :aria-busy="batchBusy">
      <template v-if="!batchProgress">
        <p class="batch-count" role="status">将启用 {{ batchEligible.length }} 个 · 跳过 {{ batchSkipped.length }} 个</p>
        <div class="batch-list">
          <h3>将启用</h3>
          <ul v-if="batchEligible.length"><li v-for="item in batchEligible" :key="item.model">{{ item.model }}</li></ul>
          <p v-else class="beichen-note">当前没有可启用的模型。</p>
          <h3 v-if="batchSkipped.length">跳过原因</h3>
          <ul v-if="batchSkipped.length">
            <li v-for="item in batchSkipped" :key="item.model"><strong>{{ item.model }}</strong><span>{{ item.reason }}</span></li>
          </ul>
        </div>
      </template>
      <template v-else>
        <div v-if="batchBusy && batchProgress.total" class="batch-progress">
          <p role="status">已处理 {{ batchProgress.completed }}/{{ batchProgress.total }} 个</p>
          <progress aria-label="批量启用进度" :value="batchProgress.completed" :max="batchProgress.total" />
        </div>
        <p class="batch-count" role="status">已启用 {{ batchCounts.enabled }} 个 · 跳过 {{ batchCounts.skipped }} 个 · 失败 {{ batchCounts.failed }} 个</p>
        <ul v-if="batchResults.length" class="batch-list">
          <li v-for="item in batchResults" :key="item.model" :data-batch-status="item.status">
            <strong>{{ item.model }}</strong>
            <span>{{ item.status === 'failed' ? '失败 · ' : item.status === 'skipped' ? '跳过 · ' : '' }}{{ item.reason }}</span>
          </li>
        </ul>
      </template>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions batch-actions">
        <button type="button" class="button ghost" :disabled="batchBusy" @click="batchSelection = undefined">{{ batchProgress ? '完成' : '取消' }}</button>
        <button v-if="!batchProgress" type="button" class="button primary" :disabled="busy || !batchEligible.length"
          @click="enableBatch()">确认启用 {{ batchEligible.length }} 个</button>
        <button v-else-if="batchCounts.failed" type="button" class="button primary" :disabled="busy"
          @click="enableBatch(true)">重试失败项</button>
      </div>
    </section>
  </AppDialog>
  <AppDialog :open="Boolean(conditionsRow)" :title="(conditionsRow?.model ?? '模型') + ' · 计价档位'" description="查看官网计价条件及按线路倍率换算的学生实价。"
    wide @update:open="open => { if (!open) conditionsRow = undefined }">
    <div class="beichen-stack">
      <p v-if="conditionsRow && (conditionsRow.routeBlocked || conditionsRow.bindingIssue)" class="beichen-note" data-testid="tier-route-reason">
        {{ conditionsRow.bindingIssue?.message ?? (conditionsRow.blockers.join('；') || '线路未就绪，请刷新模型或检查渠道配置') }}
      </p>
      <p v-if="conditionsRow?.priceBlocker" class="beichen-note price-blocker-note" data-testid="price-blocker-message">
        {{ conditionsRow.priceBlocker }}
      </p>
      <section v-for="quote in conditionsRow?.quotes ?? []" :key="quote.key" class="condition-quote">
        <h3>{{ quote.multiplierLabel }}</h3>
        <dl class="condition-prices">
          <div><dt>{{ quote.status === 'derived' ? '反推基础价' : '官网价' }}</dt><dd class="price-detail">{{ quote.standard ? withYuan(quote.standard) : '尚未取得报价明细' }}</dd></div>
          <div><dt>学生实价</dt><dd class="price-detail">{{ quote.mode === 'manual' ? '请查看列表中的手动价' : quote.actual ? withYuan(quote.actual) : '尚无可用单价' }}</dd></div>
        </dl>
        <p v-for="note in quote.notes" :key="note" class="beichen-note">{{ note }}</p>
      </section>
    </div>
  </AppDialog>
</template>

<style scoped>
.website-catalog .actions { flex-wrap: wrap; }
.website-catalog h3, .website-catalog p, .website-catalog ul, .rebind-changes { margin: 0; }
.website-catalog summary { margin: 8px 0; cursor: pointer; }
.website-catalog ul, .rebind-changes { overflow-wrap: anywhere; }
.rebind-changes { padding: 0; list-style: none; }
.rebind-changes li { display: grid; gap: 4px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.beichen-models, .model-list { min-width: 0; max-width: 100%; }
.beichen-models { grid-template-columns: minmax(0, 1fr); overflow-wrap: anywhere; }
.monthly-usage { display: grid; gap: 12px; }
.monthly-usage h2 { font-size: 17px; }
.monthly-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 130px), 1fr)); gap: 10px; margin: 0; }
.monthly-stats > div { min-width: 0; padding: 10px 12px; border-radius: 12px; background: var(--primary-soft); }
.monthly-stats dt { font-size: 12px; color: var(--ink-soft); }
.monthly-stats dd { margin: 4px 0 0; font-size: 20px; font-weight: 700; font-variant-numeric: tabular-nums; }
.model-summary { grid-template-columns: repeat(auto-fit, minmax(min(100%, 130px), 1fr)); }
.model-toolbar { justify-content: flex-end; }
.model-list { container-type: inline-size; }
.model-table { table-layout: fixed; }
.model-col { width: 15%; } .state-col { width: 16%; } .price-col { width: 25%; } .usage-col { width: 8%; } .action-col { width: 11%; }
.model-table thead th { white-space: normal; }
.model-table th, .model-table td { min-width: 0; overflow-wrap: anywhere; }
.model-name { font-size: 13px; }
.model-state { font-size: 12px; line-height: 1.75; }
.model-state-summary { display: inline; }
.catalog-evidence { margin-top: 4px; font-size: 12px; color: var(--ink-soft); }
.catalog-evidence summary { cursor: pointer; }
.catalog-evidence ul { margin: 4px 0; padding-inline-start: 18px; overflow-wrap: anywhere; }
.model-capability { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; margin-top: 8px; }
.model-capability .chip { padding: 2px 8px; border-radius: 999px; font-size: 11px; background: #f1f4f9; color: var(--ink-soft); }
.model-capability .chip.recommend { background: #f3efff; color: #5b3ea8; }
.model-capability .status-pill { white-space: normal; }
.price-blocker-note { color: #835615; }
.published-state { color: #16765e; }
.binding-issue { color: var(--danger, #b42318); }
.binding-issue-row { background: color-mix(in srgb, var(--danger, #b42318) 5%, transparent); }
.model-probe { display: flex; gap: 3px 8px; align-items: center; flex-wrap: wrap; margin-top: 6px; }
.model-probe small { color: var(--ink-soft); }
.beichen-models .button { min-width: 0; max-width: 100%; white-space: normal; }
.model-actions { display: flex; gap: 6px; flex-wrap: wrap; }
.model-actions .button { padding: 7px 10px; }
.model-actions .row-note { flex-basis: 100%; }
.row-note { display: block; color: var(--ink-soft); font-size: 11px; line-height: 1.5; margin-top: 6px; overflow-wrap: anywhere; }
.enable-reason { color: #835615; }
.price-line { white-space: pre-line; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; font-size: 12px; line-height: 1.6; margin: 0; }
.billing-mode-line { margin: 0; color: var(--ink-soft); font-size: 11px; line-height: 1.6; }
.price-observed { color: var(--ink-soft); font-size: 11px; font-weight: 400; overflow-wrap: anywhere; }
.price-source { white-space: normal; }
.quote-row { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; }
.quote-row + .quote-row { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); }
.quote-caption { display: flex; align-items: center; gap: 6px; min-height: 22px; flex-wrap: wrap; font-size: 11px; font-variant-numeric: tabular-nums; }
.quote-caption .status-pill { white-space: normal; }
.price-cell { min-width: 0; }
.price-link, .usage-link { padding: 0; border: 0; background: transparent; text-align: left; color: var(--primary-strong); font-weight: 700; font-size: 12px; overflow-wrap: anywhere; }
.price-link:hover, .usage-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.model-filters { display: flex; gap: 7px; align-items: center; flex-wrap: wrap; }
.model-filters button { border: 1px solid var(--line); background: #fff; color: var(--ink-soft); border-radius: 999px; font-size: 12px; padding: 6px 12px; }
.model-filters button[aria-pressed="true"] { background: var(--primary-soft); border-color: var(--primary); color: var(--primary-strong); }
.model-filters small { margin-left: auto; color: var(--ink-soft); font-size: 11px; }
.sync-stat strong { font-size: 16px; line-height: 1.5; }
.key-health { color: var(--ink-soft); font-size: 12px; }
.key-health h3 { margin: 0 0 10px; color: var(--ink); font-size: 14px; }
.key-health summary { width: fit-content; cursor: pointer; }
.key-health details + details { margin-top: 8px; }
.key-health dl { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 145px), 1fr)); gap: 10px; margin: 8px 0 0; }
.key-health dt { font-weight: 700; }
.key-health [data-state="unavailable"] dt { color: var(--danger, #b42318); }
.key-health dd { margin: 2px 0 0; }
.usage-feedback { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.model-skeleton { display: grid; gap: 12px; }
.model-skeleton i { height: 105px; background: #edf0f7; border-radius: 12px; }
.batch-preview { min-width: 0; font-size: 13px; }
.batch-count { margin: 0; font-size: 17px; font-weight: 700; font-variant-numeric: tabular-nums; }
.batch-progress p { margin: 0 0 5px; }
.batch-progress progress { width: 100%; accent-color: var(--primary); }
.batch-actions { flex-wrap: wrap; }
.batch-list { max-height: 40vh; overflow-y: auto; padding: 0; margin: 0; overflow-wrap: anywhere; }
.batch-list h3 { margin: 16px 0 8px; font-size: 13px; }
.batch-list h3:first-child { margin-top: 0; }
.batch-list ul { list-style: none; padding: 0; margin: 0; }
.batch-list li { display: grid; gap: 4px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.batch-list li span { font-size: 12px; color: var(--ink-soft); }
.batch-list [data-batch-status="failed"] span { color: var(--danger, #b53b55); }
.condition-quote h3 { font-size: 14px; margin-top: 0; }
.condition-prices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; margin: 0; }
.condition-prices dt { font-weight: 700; font-size: 13px; margin-bottom: 8px; }
.price-detail { white-space: pre-line; overflow-wrap: anywhere; color: var(--ink-soft); font-variant-numeric: tabular-nums; font-size: 13px; margin: 0; line-height: 1.7; }
button:focus-visible, summary:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@container (max-width: 60rem) {
  .model-table { display: block; }
  .model-table colgroup, .model-table thead { display: none; }
  .model-table tbody { display: grid; gap: 16px; }
  .model-table tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 10px; padding: 16px 0; border: 1px solid var(--line); border-radius: 14px; }
  .model-table th, .model-table td { display: block; width: auto; padding: 0 14px; border: 0; }
  .model-name, .model-state { grid-column: 1 / -1; }
  .model-table td[data-label]::before { content: attr(data-label); display: block; margin-bottom: 6px; font-size: 11px; color: var(--ink-soft); }
  .model-actions .row-note { margin-top: 0; }
}
@container (max-width: 29rem) {
  .model-table tbody tr { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 900px) {
  .condition-prices { grid-template-columns: minmax(0, 1fr); gap: 16px; }
}
@media (max-width: 600px) {
  .model-filters small { margin-left: 0; flex-basis: 100%; }
  .beichen-toolbar-group { width: 100%; }
  .beichen-toolbar-group > .button { flex: 1; }
}
</style>
