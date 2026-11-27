<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenHealthDimension, BeichenModelUsage, BeichenProbeRun, BeichenUsageRange, BindingAdapter } from '../../api/types'
import type { BeichenCatalogKey, BeichenModel, BeichenPreferredRoute, BeichenPriceConditions, BeichenRebindPlan, BeichenRebindRoute, BeichenWebsiteKeyInventory, BeichenWebsiteLocation } from '../../api/types'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { useToast } from '../../composables/useToast'
import {
  formatObservedAt, formatPriceConditions, indexWebsiteKeyGroups, keyGroupIssue, keyGroupRate, lastKnownKeyGroup,
  lastKnownKeyGroupLabel, matchingSchoolBinding, missingPriceDetailLabels, priceBelongsToWebsiteKeyGroup, priceDetailRows, schoolCatalog,
  schoolPriceRoutes, schoolTokenPriceAt, schoolUsageCredits, schoolUsageModelMap, schoolUsagePendingNote,
  schoolUsageSettlementNote, schoolUsageTokenNote, schoolUsageTokens, usageDispatchedCalls, usagePendingReviewCalls,
  verifiedWebsiteKeyBilling, websiteKeyEffectiveRate, type SchoolCatalogRow, type SchoolQuote,
  type TeacherBoundRoute, type TeacherCatalogModel, type TeacherOverview,
} from '../../utils/beichenModels'
import { bindingAdapterMeta } from '../../utils/bindingAdapters'
import { formatPercent, formatTokens, multiplyDecimal, shiftDecimal } from '../../utils/credits'
import { buildCurrentGroupPriceRow, priceReferenceText } from '../../utils/groupPriceRows'
import { schoolDay, statusLabel } from '../../utils/format'
import AppDialog from '../../components/AppDialog.vue'
import BeichenGroupPrices from '../../components/BeichenGroupPrices.vue'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenModelUsageDrawer from '../../components/BeichenModelUsageDrawer.vue'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'
import BeichenKeyCapabilities from './BeichenKeyCapabilities.vue'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string>; providerMaintenance?: boolean; retiredOnly?: boolean }>()
const emit = defineEmits<{ 'catalog-synced': []; 'retired-change': [info: { total: number; matches: number }] }>()
type UsageState = { data?: BeichenModelUsage; loading?: boolean; error?: string }
type PriceState = NonNullable<TeacherCatalogModel['price_state']>
type BatchOutcome = { model: string; status: 'enabled' | 'skipped' | 'failed'; reason: string }
type ProbeBudget = { date: string; used: number; limit: number; remaining: number; exhausted: boolean }
type ProbeRunWithBudget = BeichenProbeRun & { budget?: ProbeBudget }

const overview = ref<TeacherOverview>()
const websiteKeyInventory = ref<BeichenWebsiteKeyInventory>()
const usageStates = ref<Partial<Record<BeichenUsageRange, UsageState>>>({})
const range = ref<BeichenUsageRange>('month')
const loading = ref(false)
const refreshingWebsiteKeys = ref(false)
const websiteKeyRefreshError = ref('')
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
const convertibleUnits = new Set(['每百万 Token 的积分', '积分/次', '积分/秒'])
const filters = [{ id: 'all', label: '全部' }, { id: 'active', label: '已启用' }, { id: 'pending', label: '待配置' }, { id: 'unpriced', label: '无报价' }, { id: 'manual', label: '手动价' }, { id: 'anomaly', label: '异常' }] as const
function adapterLabel(value: string) { return bindingAdapterMeta(value as BindingAdapter).label }
const filter = ref<typeof filters[number]['id']>('all')
const priceRow = ref<SchoolCatalogRow>()
const conditionsModel = ref<string>()
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
function protocolLabel(value: string) {
  return protocolLabels[value] ?? bindingAdapterMeta(value as BindingAdapter).label
}
function normalizeSignal(value: string) {
  return value.trim().toLowerCase().replace(/[_ /.:]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
}
const rescanKeys = computed(() => (overview.value?.credentials ?? []).filter(key => key.status === 'active'))
const rescanKeyAvailable = computed(() => rescanKeys.value.some(key => key.credential_id === rescanKey.value))
const bindingGroupDenials = computed(() => {
  const groups = new Map<string, number | null>()
  for (const model of overview.value?.model_catalog ?? []) {
    for (const route of model.bound_routes ?? []) {
      if (route.key_enabled && route.credential_id && route.group_status === 'not_allowed') {
        groups.set(route.credential_id, route.group_id)
      }
    }
  }
  return groups
})
const billingByKey = computed(() => new Map((overview.value?.datasets ?? [])
  .filter(dataset => dataset.kind === 'billing').map(dataset => {
    const deniedGroup = bindingGroupDenials.value.get(dataset.credential_id)
    return [dataset.credential_id, deniedGroup === undefined ? dataset : {
      ...dataset, group_status: 'not_allowed' as const, group_id: deniedGroup ?? dataset.group_id,
    }]
  })))
const pricesByKey = computed(() => new Map((overview.value?.datasets ?? [])
  .filter(dataset => dataset.kind === 'prices').map(dataset => [dataset.credential_id, dataset])))
const websiteKeyGroups = computed(() => indexWebsiteKeyGroups(websiteKeyInventory.value))
function confirmedWebsiteKey(credentialId?: string | null) {
  return credentialId && websiteKeyGroups.value.current ? websiteKeyGroups.value.matched.get(credentialId) : undefined
}
const unmatchedWebsiteKeys = computed(() => (websiteKeyInventory.value?.keys ?? [])
  .filter(key => key.enabled === true && (key.match_status !== 'matched' || !key.credential_id)))
const websiteKeysNeedRefresh = computed(() => {
  return !websiteKeyGroups.value.current || rescanKeys.value.some(key => !confirmedWebsiteKey(key.credential_id))
})
const websiteKeysRefreshHint = computed(() => {
  const status = websiteKeyInventory.value?.status
  if (status === 'session_expired') {
    return '北辰网页会话已过期。请在北辰网页登录重新授权，再刷新清单。'
  }
  if (status === 'stale') return '这里显示上次读取的清单。刷新后可重新核对学校 Key 的分组和倍率。'
  if (status !== 'current') return '请从北辰官网刷新 Key 清单，以确认学校 Key 的分组和倍率。'
  if (websiteKeysNeedRefresh.value) {
    return '有 Key 缺少匹配记录。刷新清单可重新核对分组和倍率。'
  }
  return '在北辰官网换组后，可在这里立即核对 Key 的分组和倍率。'
})
const billingRows = computed(() => rescanKeys.value.flatMap(key => {
  const billing = billingByKey.value.get(key.credential_id)
  const websiteKey = confirmedWebsiteKey(key.credential_id)
  const previousGroup = websiteKey?.group_id == null
    ? lastKnownKeyGroup(websiteKeyGroups.value.matched.get(key.credential_id), billing) : undefined
  if (!billing?.billing_state && !websiteKey && !previousGroup) return []
  const currentBilling = websiteKey ? verifiedWebsiteKeyBilling(websiteKey, billing) : undefined
  return [{ ...billing, websiteKey, previousGroup, currentBilling, credential_id: key.credential_id, label: key.label }]
}))
const keyCapabilities = computed(() => (overview.value?.key_capabilities ?? []).map(key => {
  const group = key.website_group
  const websiteKey = confirmedWebsiteKey(key.credential_id)
  const visibleGroup = websiteKey?.group_id != null && websiteKey.group_id === group?.id &&
    rateLabel(websiteKey.rate) === rateLabel(group.rate_multiplier) ? group : null
  if (bindingGroupDenials.value.has(key.credential_id)) {
    return { ...key, website_group: visibleGroup, group_status: 'not_allowed' as const,
      models: [], models_error: '分组无权限' }
  }
  return { ...key, website_group: visibleGroup }
}))
const rebindModelCount = computed(() => new Set(rebindPlan.value?.changes.map(row => row.model_id)).size)

function keyGroupDiagnosis(key?: BeichenCatalogKey) {
  if (!key) return undefined
  const websiteKey = confirmedWebsiteKey(key.credential_id)
  if (websiteKey ? websiteKey.group_id === key.group_id : !key.group_stale) return key.group_diagnosis
}

function currentGroupPrices(locations: BeichenWebsiteLocation[], credentialId: string | undefined,
  key: BeichenCatalogKey | undefined, route: BeichenPreferredRoute | undefined, record: BeichenModel | undefined,
  usesBoundRoutes: boolean, evidence?: TeacherBoundRoute, conditional?: SchoolQuote['conditionalPrice']) {
  const billing = credentialId ? billingByKey.value.get(credentialId) : undefined
  const prices = credentialId ? pricesByKey.value.get(credentialId) : undefined
  const websiteKey = confirmedWebsiteKey(credentialId)
  const groupId = evidence?.group_id ?? websiteKey?.group_id ?? null
  const groupKey = websiteKey?.group_id === groupId ? websiteKey : undefined
  const previousGroup = credentialId && groupId == null
    ? lastKnownKeyGroup(websiteKeyGroups.value.matched.get(credentialId), billing, key?.group_id) : undefined

  const isDerived = record?.price_source === 'derived_from_usage' || (!record?.price_source && record?.price_status === 'derived')
  const legacyTerms = record?.price_conditions
  const terms = record?.price_conditions_exact ?? (legacyTerms && Object.fromEntries(
    Object.entries(legacyTerms).filter(([field, value]) => !field.endsWith('_price') || value !== 0)))
  const priceObservedAt = record?.price_observed_at ?? prices?.last_success
  const canUseRecord = websiteKey ? priceBelongsToWebsiteKeyGroup(websiteKey, priceObservedAt, evidence?.group_id ?? billing?.group_id ?? key?.group_id)
    : !previousGroup && !keyGroupIssue(billing)
  // 历史反推价只作小字参考，不再作为主价。
  const derived = canUseRecord && isDerived && record?.price_status === 'derived' && terms
    ? { price_conditions_exact: terms, observed_at: record.price_observed_at } : null

  const rateUnavailable = Boolean(billing?.last_error || keyGroupIssue(billing) ||
    billing?.billing_state === 'stale' || billing?.billing_state === 'unverified' || key?.group_stale)
  const synchronizedRate = billing?.billing_state === 'current'
    ? billing.effective_multiplier_text ?? billing.effective_multiplier : null
  const legacyRate = synchronizedRate ?? route?.effective_multiplier_text ?? route?.effective_multiplier
  const legacyPriceCurrent = (record?.price_status === 'confirmed' || record?.price_status === 'conditional' || derived) &&
    (!prices?.last_error || prices.official_price_missing)
  const groupMultiplier = websiteKey ? groupKey && websiteKeyEffectiveRate(groupKey, billing)
    : canUseRecord && !rateUnavailable && legacyPriceCurrent ? legacyRate : null
  const groupName = groupKey?.group_name ?? (billing?.group_id === groupId ? billing?.group_name : undefined)
  const baseRate = groupKey?.rate
  const rateForPrices = groupMultiplier == null ? null : baseRate ?? groupMultiplier
  const accountRate = groupMultiplier != null && keyGroupRate(String(rateForPrices)) !== keyGroupRate(String(groupMultiplier))
    ? groupMultiplier : null
  const effectiveRateText = groupMultiplier == null ? '' : keyGroupRate(String(groupMultiplier))

  const current = groupId == null ? null : buildCurrentGroupPriceRow(locations, groupId, {
    keyGroup: { id: groupId, name: groupName, rate: rateForPrices, account_rate: accountRate },
    derived: derived?.price_conditions_exact,
  })
  const currentRoutePrice = evidence?.binding_ready && evidence.price_covered &&
    ['confirmed', 'manual', 'conditional'].includes(evidence.price_state)
  const actualRates = currentRoutePrice && (!isDerived || record?.mode === 'manual') ? record?.school_rates : undefined
  const conditionPrice = evidence?.price_state === 'conditional' && currentRoutePrice && !actualRates ? conditional : undefined
  const baseBand = conditionPrice ? schoolTokenPriceAt(conditionPrice, 0n) : null
  const baseRows = new Map(baseBand?.rows.map(field => [field.key, field]))
  const studentPrice = (value?: string | number | null) => value == null ? null : multiplyDecimal(shiftDecimal(value, -6), '1.10')
  const activePrices = actualRates ? {
    input: studentPrice(actualRates.input),
    output: studentPrice(actualRates.output),
    cache5m: studentPrice(actualRates.cache_write),
    cache1h: record?.mode === 'manual' ? studentPrice(actualRates.cache_write)
      : canUseRecord && groupMultiplier != null ? priceDetailRows(terms, multiplyDecimal(groupMultiplier, '1.10'))
        .find(field => field.key === 'cache_write_1h')?.credits ?? null : null,
    cache_read: studentPrice(actualRates.cache_read),
  } : {
    input: baseRows.get('input')?.student ?? null,
    output: baseRows.get('output')?.student ?? null,
    cache5m: baseRows.get('cache_write_5m')?.student ?? null,
    cache1h: baseRows.get('cache_write_1h')?.student ?? null,
    cache_read: baseRows.get('cache_read')?.student ?? null,
  }
  if (current && usesBoundRoutes) {
    current.student = activePrices
    if (!actualRates) current.student_source = null
  }
  if (current && baseBand) {
    current.official = {
      input: baseRows.get('input')?.yuan ?? null,
      output: baseRows.get('output')?.yuan ?? null,
      cache5m: baseRows.get('cache_write_5m')?.yuan ?? null,
      cache1h: baseRows.get('cache_write_1h')?.yuan ?? null,
      cache_read: baseRows.get('cache_read')?.yuan ?? null,
    }
    current.base_missing = false
    current.base_note = null
    current.student_source = 'website'
    current.price_conditions_exact = record?.price_conditions_exact ?? null
    current.observed_at = conditionPrice?.observedAt ?? null
  }
  const higherBands = conditionPrice?.intervals.filter(tier => tier.minTokens !== '0') ?? []
  const tierNote = higherBands.length === 1 && higherBands[0].maxTokens == null && baseBand
    ? `基础档 · 达 ${BigInt(higherBands[0].minTokens).toLocaleString('zh-CN')} Token 按高档`
    : conditionPrice?.intervals.length ? (baseBand ? '基础档 · 按输入 Token 分档' : '按输入 Token 分档') : ''
  const website = current?.price_conditions_exact
    ? { price_conditions_exact: current.price_conditions_exact, observed_at: current.observed_at }
    : null
  const observedAt = current?.observed_at ?? derived?.observed_at ?? null
  const reference = derived?.price_conditions_exact ? priceReferenceText(derived.price_conditions_exact) : null
  const derivedNote = current?.base_missing && reference
    ? `历史流水参考：${reference}（每 1M Token，未作为主价）`
    : ''
  return {
    current, website, observedAt, actualRates, activePrices, tierNote,
    unmatched: Boolean(credentialId && groupId == null),
    currentTitle: current ? `当前 Key 分组 ${current.group_id} · ${effectiveRateText ? '生效 ' + effectiveRateText : '倍率待核实'}` : '',
    baseNote: current?.base_note ?? '',
    derivedNote,
    observedLabel: formatObservedAt(observedAt),
    officialPriceMissing: prices?.official_price_missing,
  }
}

function boundPriceLabel(route: TeacherBoundRoute) {
  switch (route.price_state) {
    case 'manual': return '手动价'
    case 'confirmed': return '固定价'
    case 'conditional': return '条件价'
    case 'derived': return '反推价待核对'
    case 'missing': return '缺报价'
    default: return '报价待核实'
  }
}

function remainingRouteAvailability(routes: TeacherBoundRoute[], previousRecommendation?: string | null) {
  const passed = routes.filter(route => route.probe_state === 'passed')
  const unprobed = routes.filter(route => route.probe_state === 'unprobed')
  const candidates = passed.length ? passed : unprobed
  const recommended = candidates.find(route => route.protocol === previousRecommendation) ??
    candidates.sort((left, right) => left.priority - right.priority || left.protocol.localeCompare(right.protocol))[0]
  const lastProbeAt = routes.flatMap(route => route.probe ? [route.probe.checked_at] : [])
    .sort((left, right) => Date.parse(right) - Date.parse(left))[0]
  const recentPass = routes.some(route => {
    if (!route.probe?.last_passed_at || route.probe.error_code === 'credential_unreadable') return false
    const age = Date.now() - Date.parse(route.probe.last_passed_at)
    return age >= 0 && age <= 7 * 24 * 60 * 60 * 1000
  })
  const evidence = { recommendedProtocol: recommended?.protocol, lastProbeAt }
  if (passed.length) return { ...evidence, probeState: 'passed' as const, availability: 'available',
    availabilityReason: '', actionHint: '可使用推荐协议接入。' }
  if (!routes.length) return { ...evidence, probeState: 'unprobed' as const, availability: 'unverified',
    availabilityReason: '绑定 Key 的分组已变化，请刷新模型并复核目录。', actionHint: '请用当前 Key 重扫目录并探活。' }
  if (unprobed.length) return { ...evidence, probeState: 'unprobed' as const, availability: 'unverified',
    availabilityReason: '已有绑定线路，当前还没有通过探活的协议。', actionHint: '发起探活以确认已发布协议。' }
  return { ...evidence, probeState: 'failed' as const, availability: recentPass ? 'degraded' : 'maintenance',
    availabilityReason: recentPass ? '最近一次探活失败，过去 7 天内曾探活通过。仍可尝试调用，可能请求失败。'
      : '维护中。当前线路的协议最近一次探活均失败。', actionHint: '请核对剩余线路并重新探活。' }
}

const catalog = computed(() => {
  const data = overview.value
  if (!data) return []
  const changedGroups = new Set<string>()
  const modelCatalog = data.model_catalog?.map(entry => {
    const routes = entry.bound_routes
    if (!routes?.length) return entry
    const changedKeys = new Set<string>()
    const boundRoutes = routes.map(route => {
      const website = confirmedWebsiteKey(route.credential_id)
      if (!route.key_enabled || !route.credential_id || website?.group_id == null || website.group_id === route.group_id) return route
      if (route.group_checked_at && website.last_seen && Date.parse(website.last_seen) <= Date.parse(route.group_checked_at)) return route
      changedKeys.add(route.credential_id)
      return { ...route, group_id: website.group_id, group_status: 'pending', binding_ready: false,
        catalog_state: 'catalog_stale', price_covered: false, price_state: 'unverified', probe_state: 'unprobed', probe: null }
    })
    if (!changedKeys.size) return entry
    changedGroups.add(entry.model.toLowerCase())
    const catalogKeys = entry.catalog_keys?.map(key => changedKeys.has(key.credential_id) ? { ...key, stale: true } : key)
    return { ...entry, bound_routes: boundRoutes, catalog_keys: catalogKeys }
  })
  const entries = new Map((modelCatalog ?? []).map(row => [row.model.toLowerCase(), row]))
  const observedPrices = new Map((data.models ?? []).map(record => [
    record.credential_id + ':' + record.model.toLowerCase(), record,
  ]))
  return schoolCatalog({ ...data, model_catalog: modelCatalog }, props.providerId, websiteKeyGroups.value).map(row => {
    const model = row.model.toLowerCase()
    const entry = entries.get(model) ?? { model: row.model }
    const retired = entry.retired === true
    const source = entry.source
    const websiteOnly = entry.catalog_state === 'website_only'
    const listed = !retired && (entry.catalog_state ? entry.catalog_state === 'key_listed' : entry.credential_ids?.length !== 0)
    const activation = entry.activation
    const preferred = (entry.preferred_routes ?? []).filter(route => route.provider_id === props.providerId && route.credential_id &&
      route.school_model.toLowerCase() === row.school.toLowerCase())
    const changedGroup = changedGroups.has(model)
    const boundRoutes = entry.bound_routes ?? []
    const readyRoutes = boundRoutes.filter(route => route.binding_ready)
    const bindingIssue = retired ? undefined : entry.binding_issue
    const primaryGroupDenial = bindingIssue?.code === 'group_not_allowed'
    const remaining = changedGroup ? remainingRouteAvailability(readyRoutes, entry.route_availability?.recommended_protocol) : undefined
    const legacyBoundIds = preferred.filter(route => route.state === 'ready').map(route => route.credential_id!)
    const boundIds = new Set(entry.bound_routes
      ? boundRoutes.filter(route => route.key_enabled && route.credential_id).map(route => route.credential_id!)
      : legacyBoundIds)
    const catalogKeys = entry.catalog_keys ?? []
    const locations = entry.website_locations ?? []
    const catalogPending = !primaryGroupDenial && (entry.bound_catalog_pending || changedGroup)
    const keyCoverage = changedGroup ? new Set(boundRoutes.filter(route =>
      route.key_enabled && route.credential_id && route.catalog_state === 'key_listed').map(route => route.credential_id)).size
      : entry.covered_bound_key_count ?? boundIds.size
    const readyCount = changedGroup ? new Set(readyRoutes.map(route => route.credential_id + ':' + route.protocol)).size
      : entry.ready_count ?? row.readyCount
    const routeCount = entry.route_count ?? row.routeCount
    const priceStates = new Set(readyRoutes.map(route => route.price_state))
    const priceState = changedGroup ? (priceStates.size > 1 ? 'mixed' : priceStates.values().next().value ?? 'unverified')
      : entry.price_state ?? oldPriceState(row)
    const priceBlocker = changedGroup ? undefined : entry.price_blocker?.message.trim()
    const priceCovered = !retired && !websiteOnly && (changedGroup ? readyRoutes.some(route => route.price_covered)
      : entry.price_covered ?? row.priceReady)
    const routeBlocked = routeCount === 0 || entry.blockers?.some(blocker => blocker.code === 'no_ready_route') === true
    const blockerMessages = (entry.blockers ?? [])
      .filter(blocker => !routeBlocked || (blocker.code !== 'missing_price' && blocker.code !== 'conditional_price'))
      .map(blocker => blocker.message?.trim()).filter(Boolean)
    const hasKnownGroup = (id?: string | null) => confirmedWebsiteKey(id)?.group_id != null
    const priceRoute = schoolPriceRoutes(entry, props.providerId, row.school)
      .sort((left, right) => Number(right.state === 'ready') - Number(left.state === 'ready') ||
        Number(right.school_model === row.school) - Number(left.school_model === row.school) ||
        (!entry.bound_routes ? Number(hasKnownGroup(right.credential_id)) - Number(hasKnownGroup(left.credential_id)) : 0) ||
        left.priority - right.priority)[0]
    const priceEvidence = priceRoute ? matchingSchoolBinding(boundRoutes, priceRoute) : undefined
    const listedKeys = catalogKeys.filter(key => key.state === 'listed' && !key.stale)
    const published = row.status === 'active' || activation === 'active' || boundRoutes.some(route => route.published)
    const credentialId = priceRoute?.credential_id ??
      (!published ? (listedKeys.find(key => confirmedWebsiteKey(key.credential_id)?.group_id != null) ?? listedKeys[0])?.credential_id : undefined)
    const priceKey = catalogKeys.find(key => key.credential_id === credentialId)
    const upstreamModel = (priceRoute?.upstream_model ?? priceKey?.upstream_model ?? row.model).toLowerCase()
    const record = credentialId ? observedPrices.get(credentialId + ':' + upstreamModel) : undefined
    const conditional = row.quotes.find(quote => quote.credentialId === credentialId &&
      quote.upstreamModel === upstreamModel)?.conditionalPrice
    const groupPrices = currentGroupPrices(locations, credentialId ?? undefined, priceKey, priceRoute, record,
      entry.bound_routes != null, priceEvidence, conditional)
    const websiteTerms = groupPrices.website?.price_conditions_exact
    const detailQuotes = row.quotes.filter(quote => hasPriceConditions(quote) || needsPriceDetails(quote.conditions) ||
      (!quote.conditions && quote.status === 'confirmed' && quote.actual))
    const detailWebsite = websiteTerms && needsPriceDetails(websiteTerms) && !detailQuotes.some(quote => quote.conditions === websiteTerms)
      ? formatPriceConditions(websiteTerms, null, true).standard : null
    const boundCatalogKeys = catalogKeys.filter(key => boundIds.has(key.credential_id) &&
      (!key.upstream_model || !entry.bound_routes || boundRoutes.some(route => route.credential_id === key.credential_id &&
        route.upstream_model.toLowerCase() === key.upstream_model?.toLowerCase())))
    const statuses = boundRoutes.map(route => ({ state: route.group_status, id: route.group_id }))
    if (!entry.bound_routes) {
      for (const id of boundIds) {
        const billing = billingByKey.value.get(id)
        statuses.push({ state: billing?.group_status, id: billing?.group_id ?? null })
      }
    }
    const deniedGroups = [...new Set(statuses.filter(group => group.state === 'not_allowed')
      .map(group => group.id == null ? '分组无权限' : '分组无权限 · 分组 ' + group.id))]
    const missingGroup = statuses.some(group => group.state === 'not_found')
    const groupDiagnoses = new Set(boundCatalogKeys.map(keyGroupDiagnosis))
    const groupMismatch = missingGroup || groupDiagnoses.has('model_outside_key_group')
    const groupRateMismatch = groupDiagnoses.has('group_rate_mismatch') ||
      (groupDiagnoses.has('price_outside_key_group') && !priceCovered)
    const availability = remaining?.availability ?? entry.route_availability?.availability
    const anomaly = !retired && !websiteOnly && (Boolean(bindingIssue || groupMismatch || groupRateMismatch || deniedGroups.length || catalogPending) ||
      published && ['maintenance', 'degraded', 'unavailable'].includes(availability ?? ''))
    const active = !retired && !anomaly && published && row.status !== 'pending_configuration' && readyCount > 0
    const availableProtocols = remaining ? readyRoutes.map(route => route.protocol) : entry.route_availability?.protocols ?? []
    const protocolNames = remaining || availableProtocols.length ? availableProtocols : entry.protocols ?? []
    const priceMissing = !retired && !websiteOnly && !priceCovered && priceState !== 'unverified' && priceState !== 'conditional' &&
      (priceState === 'missing' || priceState === 'derived' || Boolean(priceBlocker)) &&
      (entry.price_covered === false || Boolean(priceBlocker) || !websiteTerms ||
        missingPriceDetailLabels(websiteTerms).some(label => !label.startsWith('缓存')))
    return {
      ...row, retired, active, anomaly, published, activation, canDisable: row.canDisable,
      boundRoutes, boundKeyCount: entry.bound_key_count ?? boundIds.size, keyCoverage, catalogPending,
      historicalChannels: entry.historical_channels ?? [],
      routeCount, readyCount, priceState, priceCovered, priceMissing, routeBlocked,
      blockers: [...new Set(blockerMessages)], priceBlocker,
      listed, websiteOnly, source, locations, groupPrices, detailQuotes, detailWebsite,
      currentPriceRoute: priceEvidence?.binding_ready ? priceEvidence : undefined,
      catalogKeys, groupMismatch, deniedGroups, missingGroup, groupRateMismatch,
      protocolLabels: [...new Set(protocolNames)].map(protocolLabel),
      probeState: remaining?.probeState ?? entry.probe_state,
      recommendedProtocol: remaining ? remaining.recommendedProtocol : entry.route_availability?.recommended_protocol,
      availability,
      availabilityReason: primaryGroupDenial ? undefined : remaining?.availabilityReason ?? entry.route_availability?.availability_reason,
      actionHint: remaining?.actionHint ?? entry.action_hint,
      protocolMissing: !primaryGroupDenial && !catalogPending && (routeCount === 0 || entry.protocols?.length === 0 && !availableProtocols.length),
      lastProbeAt: remaining ? remaining.lastProbeAt : entry.last_probe_at,
      bindingIssue, primaryGroupDenial,
    }
  })
})
type CatalogRow = (typeof catalog.value)[number]
const conditionsRow = computed(() => conditionsModel.value == null ? undefined : catalog.value.find(row => row.model === conditionsModel.value))
const currentModels = computed(() => catalog.value.filter(row => !row.retired && !row.websiteOnly &&
  (row.listed || row.anomaly || row.boundKeyCount > 0)))
const listedModels = computed(() => currentModels.value.filter(row => row.listed))
const websiteOnlyModels = computed(() => catalog.value.filter(row => row.websiteOnly && !row.retired))
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
function matchesSearch(row: CatalogRow) {
  return !modelFilter.value || [row.model, row.school, ...row.locations.map(location => location.name),
    ...row.boundRoutes.map(route => route.school_model), ...row.catalogKeys.map(key => key.upstream_model ?? '')]
    .some(name => normalizeSignal(name).includes(modelFilter.value))
}
const historicalModels = computed(() => catalog.value.filter(row => row.retired))
const retiredRows = computed(() => historicalModels.value.filter(matchesSearch))
watch([historicalModels, retiredRows], ([all, matching]) => {
  emit('retired-change', { total: all.length, matches: matching.length })
}, { immediate: true })
const rows = computed(() => props.retiredOnly ? [] : currentModels.value.filter(row => {
  if (!matchesSearch(row) || props.commonIds && !props.commonIds.has(row.model.toLowerCase()) &&
      !props.commonIds.has(row.school.toLowerCase())) return false
  if (filter.value === 'anomaly') return row.anomaly
  if (!row.listed) return false
  return filter.value === 'all' ||
    filter.value === 'active' && row.active ||
    filter.value === 'pending' && !row.active && !row.anomaly ||
    filter.value === 'unpriced' && row.priceMissing ||
    filter.value === 'manual' && (row.mode !== 'follow' || row.priceState === 'manual')
}))
const batchRows = computed(() => rows.value.filter(row => row.listed))
const hasBatchEligible = computed(() => batchRows.value.some(row => !row.active && !canEnableReason(row)))
const monthlyUsage = computed<UsageState>(() => usageStates.value.month ?? {})
const drawerUsage = computed<UsageState>(() => usageStates.value[range.value] ?? {})
const usageByModel = computed(() => schoolUsageModelMap(monthlyUsage.value.data?.models))
const monthlyStats = computed(() => {
  const totals = monthlyUsage.value.data?.totals
  const hitRate = totals?.cache_hit_rate
  const hasHitRate = hitRate != null && hitRate >= 0 && hitRate <= 1
  const dispatched = usageDispatchedCalls(totals)
  let cacheCoverage = ''
  if (dispatched && totals) {
    if (!hasHitRate) cacheCoverage = '暂无可计算样本'
    else if (totals.completeness === 'complete') {
      const calls = formatTokens(dispatched)
      cacheCoverage = `Token 明细覆盖 ${calls}/${calls} 次`
    } else if (totals.completeness === 'partial') cacheCoverage = `样本覆盖部分调用（共 ${formatTokens(dispatched)} 次）`
    else cacheCoverage = '仅统计 Token 明细完整的样本，覆盖笔数未回报'
  }
  const settlementNote = schoolUsageSettlementNote(totals)
  const tokenNote = schoolUsageTokenNote(totals)
  const pendingNote = schoolUsagePendingNote(totals)
  return [
    { label: '已扣积分', value: shiftDecimal(totals?.credits, 0), note: settlementNote },
    { label: 'Token 总数', value: schoolUsageTokens(totals), note: tokenNote },
    { label: '缓存命中率', value: hasHitRate ? formatPercent(hitRate) : '—', note: cacheCoverage },
    { label: '待核笔数', value: formatTokens(usagePendingReviewCalls(totals)), note: pendingNote },
  ]
})

const batchSelection = ref<string[]>()
const batchBusy = ref(false)
const batchProgress = ref<{ completed: number; total: number }>()
const batchResults = ref<BatchOutcome[]>([])
const busy = computed(() => loading.value || refreshingWebsiteKeys.value || syncing.value || rescanning.value || rebinding.value ||
  Boolean(updating.value) || batchBusy.value)
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
const diagnosticsPending = computed(() => websiteKeysNeedRefresh.value || keyHealth.value.some(key => key.count < 4) ||
  Boolean(websiteDirectory.value && websiteDirectory.value.state !== 'fresh') || currentModels.value.some(row => row.anomaly))
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
let websiteKeysRefreshRequest = 0
let initialized = false
let disposed = false
async function load(refreshedInventory?: BeichenWebsiteKeyInventory) {
  const request = ++overviewRequest
  const providerId = props.providerId
  loading.value = true
  error.value = ''
  try {
    const [result, keyInventory] = await Promise.all([
      teacherAPI.beichenOverview(providerId, schoolDay()),
      refreshedInventory ?? teacherAPI.beichenWebsiteKeys?.(providerId).catch(() => undefined),
    ])
    if (disposed || request !== overviewRequest) return
    if (keyInventory) websiteKeyInventory.value = keyInventory
    else if (websiteKeyInventory.value) websiteKeyInventory.value = { ...websiteKeyInventory.value, status: 'unavailable' }
    overview.value = result
    if (priceRow.value && catalogByModel.value.get(priceRow.value.model.toLowerCase())?.retired) priceRow.value = undefined
    if (conditionsModel.value && catalogByModel.value.get(conditionsModel.value.toLowerCase())?.retired) conditionsModel.value = undefined
  } catch (e) {
    if (!disposed && request === overviewRequest) {
      overview.value = undefined
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
  if (row.anomaly && row.published) return '已启用 · 线路待核对'
  if (row.active) return '已启用'
  if (row.activation === 'ready') return '可启用'
  if (row.status === 'not_configured') return '未配置'
  return statusLabel(row.status ?? undefined) || '未发布'
}

const availabilityLabels: Record<string, string> = {
  available: '可用', degraded: '可用 · 探活异常', maintenance: '维护中', unverified: '待核实', unavailable: '不可用', not_published: '未发布', website_only: '仅官网可见',
}
function rowAvailability(row: CatalogRow) {
  if (row.websiteOnly) return 'website_only'
  if (row.catalogPending && !row.readyCount) return 'unverified'
  if (row.availability) return row.availability
  if (row.bindingIssue) return 'maintenance'
  return row.published || row.routeCount > 0 ? 'unverified' : 'not_published'
}
function availabilityLabel(row: CatalogRow) {
  return availabilityLabels[rowAvailability(row)] ?? '状态待同步'
}
function availabilityTone(row: CatalogRow) {
  const state = rowAvailability(row)
  if (state === 'degraded') return 'pending'
  return state === 'available' ? 'active' : state === 'maintenance' || state === 'unavailable' ? 'danger' : 'pending'
}

function keyCoverageLabel(key: BeichenCatalogKey) {
  const channel = key.school_channel || key.label
  const label = channel + (key.replacement ? ' 新 Key' : ' Key')
  const websiteKey = confirmedWebsiteKey(key.credential_id)
  const previousGroup = websiteKey?.group_id == null
    ? lastKnownKeyGroup(websiteKeyGroups.value.matched.get(key.credential_id), billingByKey.value.get(key.credential_id), key.group_id)
    : undefined
  const group = websiteKey?.group_id != null ? ' · 分组 ' + websiteKey.group_id
    : previousGroup ? ' · ' + lastKnownKeyGroupLabel + ' ' + previousGroup.id : ''
  const details = (key.label === channel ? '' : ' · ' + key.label) + group
  const matchLabels = { exact: '', normalized: '名称归一后匹配', alias: '按教师别名匹配' } as const
  switch (key.state) {
    case 'listed':
      return label + (key.stale ? ' 上次目录有' : ' 目录有') + ' · ' + key.upstream_model
        + (key.match && matchLabels[key.match] ? ' · ' + matchLabels[key.match] : '') + details
    case 'missing': return label + details + ' · 该 Key 目录未列出'
    case 'failed': return label + ' 目录读取失败' + details
    case 'pending': return label + ' 目录待同步' + details
    case 'ambiguous': return label + ' · 多个近似型号'
  }
}

function rateLabel(value?: string | number | null) {
  const rate = shiftDecimal(value, 0)
  return rate === '—' ? '未知' : rate + '×'
}

function websiteLocationLabel(location: BeichenWebsiteLocation) {
  const groups = location.groups.map(group => [group.name, group.id != null ? '分组 ' + group.id : ''].filter(Boolean).join(' '))
  return [location.channel || (location.channel_id != null ? '渠道 ' + location.channel_id : ''),
    location.platform, groups.length ? groups.join('、') : '分组待核', location.name,
    location.has_price ? '官网有报价' : '官网无报价',
    location.stale ? '缓存目录' : ''].filter(Boolean).join(' · ')
}

function canEnableReason(row: CatalogRow) {
  if (row.retired) return '型号已作废'
  if (row.published && row.activation !== 'ready') return row.bindingIssue?.message || '已启用'
  if (!row.listed) return '当前没有渠道 Key 列出此模型'
  if (row.catalogPending) return '绑定 Key 的目录待复核'
  if (row.activation === 'ready') return ''
  if (row.activation == null && row.canEnable == null) return '启用条件待同步，请先刷新模型'
  if (row.routeBlocked) return row.blockers.join('；') || '线路未就绪，请刷新模型或检查渠道配置'
  if (row.priceBlocker) return row.priceBlocker
  if (row.blockers.length) return row.blockers.join('；')
  if (!row.priceCovered) {
    if (row.priceState === 'conditional') return '条件价待核实，可先手动定价'
    if (row.groupPrices.website) return '报价尚未生效，请同步价格'
    return '官网未报价，请先手动定价'
  }
  if (row.activation === 'blocked' || !row.canEnable || !row.routeCount) return '线路未就绪，请刷新模型或检查渠道配置'
  return ''
}

function planBatch(models: string[]) {
  const visible = new Set(rows.value.map(row => row.model.toLowerCase()))
  return models.flatMap(model => {
    const name = model.toLowerCase()
    const row = catalogByModel.value.get(name)
    if (row?.retired || !visible.has(name) && !row?.active) return []
    const active = row?.active ?? false
    const reason = active ? '已启用' : row ? canEnableReason(row) : '当前目录中没有此模型'
    return [{ model, active, reason }]
  })
}

async function refreshOverview() {
  if (!busy.value) await load()
}

async function refreshWebsiteKeys() {
  if (busy.value) return
  const request = ++websiteKeysRefreshRequest
  const providerId = props.providerId
  const isCurrent = () => !disposed && request === websiteKeysRefreshRequest && props.providerId === providerId
  refreshingWebsiteKeys.value = true
  websiteKeyRefreshError.value = ''
  if (websiteKeyInventory.value?.status === 'current') {
    websiteKeyInventory.value = { ...websiteKeyInventory.value, status: 'stale' }
  }
  try {
    const inventory = await teacherAPI.refreshBeichenWebsiteKeys(providerId)
    if (isCurrent()) await load(inventory)
  } catch (e) {
    if (isCurrent()) websiteKeyRefreshError.value = apiErrorMessage(e, '刷新官网 Key 失败，请稍后重试')
  } finally {
    if (isCurrent()) refreshingWebsiteKeys.value = false
  }
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

async function rescanCatalog() {
  if (busy.value || !rescanKeyAvailable.value) return
  const credentialId = rescanKey.value
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
  if (probeSubmitting.value || probeInProgress.value) return
  const providerId = props.providerId
  const current = () => !disposed && props.providerId === providerId
  probeSubmitting.value = true
  probeError.value = ''
  try {
    const run = await teacherAPI.requestBeichenProbes(providerId)
    if (current()) {
      probeOverride.value = run
      if (!probeInProgress.value) await load()
    }
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
  if (busy.value || catalogByModel.value.get(row.model.toLowerCase())?.retired || enable && (row.active || canEnableReason(row))) return
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
  return Boolean(quote.conditionalPrice) || quote.status === 'conditional' || /上下文分档|推理强度/u.test((quote.standard ?? '') + (quote.actual ?? ''))
}

function needsPriceDetails(terms?: BeichenPriceConditions | null) {
  return Boolean(terms && (terms.billing_mode && terms.billing_mode !== 'token' || terms.intervals?.length ||
    Object.keys(terms.reasoning_effort_multipliers ?? {}).length || terms.cache_write_12h_price != null ||
    terms.image_input_price != null || terms.image_output_price != null))
}

function hasAdditionalTokenConditions(terms?: BeichenPriceConditions | null) {
  return Boolean(terms?.cache_write_12h_price != null || terms?.intervals?.some(tier => tier.cache_write_12h_price != null) ||
    Object.keys(terms?.reasoning_effort_multipliers ?? {}).length)
}

function withYuan(text: string, factor?: string) {
  return text.split('\n').map(line => {
    const [unit, ...amounts] = line.split(' · ')
    if (!convertibleUnits.has(unit)) return line
    return [unit, ...amounts.map(amount => amount.replace(/ (\d+(?:\.\d+)?)$/, (_match, credits: string) => {
      const value = factor ? multiplyDecimal(credits, factor) : credits
      return ` ${value} 积分（${shiftDecimal(value, -2)} 元）`
    }))].join(' · ')
  }).join('\n')
}

function missingPriceLabel(row: CatalogRow) {
  const terms = row.groupPrices.website?.price_conditions_exact ?? row.detailQuotes[0]?.conditions
  const labels = missingPriceDetailLabels(terms)
  const detail = row.priceBlocker || (row.groupPrices.officialPriceMissing ? '官方价表缺失' : labels.join('、'))
  return detail ? '缺价 · ' + detail : '缺价'
}

function modelUsage(model: string) { return usageByModel.value.get(model.toLowerCase()) }
function modelCredits(model: string) {
  if (monthlyUsage.value.loading) return '读取中…'
  if (monthlyUsage.value.error) return '用量读取失败'
  if (!monthlyUsage.value.data) return '用量待读取'
  const usage = modelUsage(model)
  if (!usage || usageDispatchedCalls(usage) === 0 && !usagePendingReviewCalls(usage)) return '本月无校内调用'
  return schoolUsageCredits(usage, true)
}

watch(() => props.providerId, () => {
  overview.value = undefined
  websiteKeyInventory.value = undefined
  websiteKeysRefreshRequest++
  refreshingWebsiteKeys.value = false
  websiteKeyRefreshError.value = ''
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
  conditionsModel.value = undefined
  usageOpen.value = false
  batchSelection.value = undefined
  batchProgress.value = undefined
  batchResults.value = []
  batchBusy.value = false
  initialized = true
  void Promise.all([load(), loadUsage('month')])
})
watch(() => props.retiredOnly, retired => {
  if (!retired) return
  priceRow.value = undefined
  conditionsModel.value = undefined
  if (!batchBusy.value) batchSelection.value = undefined
  if (!rebinding.value) rebindOpen.value = false
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
  <section v-if="retiredOnly" class="beichen-stack beichen-models" aria-label="已作废型号" :aria-busy="loading">
    <p v-if="error" class="error" role="alert">{{ error }} <button class="button ghost" type="button" @click="refreshOverview">重试</button></p>
    <p v-else-if="loading && !overview" role="status">正在读取已作废型号…</p>
    <div v-if="retiredRows.length" class="model-list">
      <table class="model-table">
        <caption class="sr-only">已作废型号与历史用量</caption>
        <thead><tr><th scope="col">型号</th><th scope="col">原渠道</th><th scope="col">本月已扣积分</th><th scope="col">历史用量</th></tr></thead>
        <tbody>
          <tr v-for="row in retiredRows" :key="row.model" :data-retired-model="row.model">
            <th scope="row" class="model-name">{{ row.model }}</th>
            <td data-label="原渠道">{{ row.historicalChannels.join('、') || '—' }}</td>
            <td data-label="本月已扣积分">{{ modelCredits(row.model) }}</td>
            <td data-label="历史用量"><button type="button" class="usage-link" :aria-label="'查看 ' + row.model + ' 的历史用量'" @click="openUsage(row.model)">查看历史用量</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else-if="overview" class="muted">{{ modelFilter ? '没有匹配的已作废型号' : '暂无已作废型号' }}</p>
    <div v-if="monthlyUsage.error" class="error usage-feedback" role="alert">
      <span>{{ monthlyUsage.error }}</span>
      <button type="button" class="button ghost" @click="loadUsage('month')">重试用量</button>
    </div>
  </section>
  <section v-else class="beichen-stack beichen-models" aria-label="北辰模型目录" :aria-busy="loading || refreshingWebsiteKeys">
    <section class="beichen-card monthly-usage" aria-label="本月用量汇总" :aria-busy="monthlyUsage.loading">
      <div class="beichen-section-head">
        <h2>本月用量</h2>
        <button type="button" class="button ghost" @click="openUsage()">查看全部明细</button>
      </div>
      <dl class="monthly-stats">
        <div v-for="stat in monthlyStats" :key="stat.label">
          <dt>{{ stat.label }}</dt>
          <dd>
            <span>{{ monthlyUsage.loading ? '…' : stat.value }}</span>
            <small v-if="stat.note" class="row-note">{{ stat.note }}</small>
          </dd>
        </div>
      </dl>
      <div v-if="monthlyUsage.error" class="error usage-feedback" role="alert">
        <span>{{ monthlyUsage.error }}</span>
        <button type="button" class="button ghost" @click="loadUsage('month')">重试用量</button>
      </div>
    </section>
    <div class="beichen-summary model-summary" aria-label="模型概况">
      <div class="beichen-stat"><span>可探测模型</span><strong>{{ overview ? formatTokens(listedModels.length) : '—' }}</strong><small>{{ overview ? formatTokens(rescanKeys.length) : '—' }} 把已启用 Key 的当前目录</small></div>
      <div class="beichen-stat"><span>已启用</span><strong>{{ overview ? formatTokens(currentModels.filter(row => row.active).length) : '—' }}</strong></div>
      <div class="beichen-stat"><span>待配置</span><strong>{{ overview ? formatTokens(currentModels.filter(row => !row.active && !row.anomaly).length) : '—' }}</strong></div>
      <div class="beichen-stat"><span>异常</span><strong>{{ overview ? formatTokens(currentModels.filter(row => row.anomaly).length) : '—' }}</strong></div>
      <div class="beichen-stat"><span>官网目录</span><strong>{{ overview ? formatTokens(websiteOnlyModels.length) : '—' }}</strong><small>仅官网可见，不进主表</small></div>
      <div class="beichen-stat"><span>报价覆盖</span><strong>{{ overview ? listedModels.filter(row => row.priceCovered).length + ' / ' + listedModels.length : '—' }}</strong><small>含可计算的 Token 条件价与手动价</small></div>
      <div class="beichen-stat sync-stat"><span>价格最后同步</span><strong>{{ syncTime(lastSync) }}</strong><small>北京时间</small></div>
    </div>
    <details v-if="overview" class="beichen-card model-diagnostics">
      <summary>诊断信息 <span v-if="diagnosticsPending" class="status-pill pending">有项目待核对</span></summary>
      <div class="beichen-stack diagnostic-content">
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
    <section v-if="overview" class="beichen-card" aria-label="官网 Key 清单">
      <div class="beichen-section-head">
        <h3 aria-live="polite">{{ websiteKeysNeedRefresh ? '官网 Key 清单待刷新' : '官网 Key 清单' }}</h3>
        <button type="button" class="button secondary" :disabled="busy" @click="refreshWebsiteKeys">
          {{ refreshingWebsiteKeys ? '刷新中…' : '刷新官网 Key' }}
        </button>
      </div>
      <p class="beichen-note">{{ websiteKeysRefreshHint }}</p>
      <p v-if="websiteKeyRefreshError" class="error" role="alert">{{ websiteKeyRefreshError }}</p>
      <div v-if="unmatchedWebsiteKeys.length" class="beichen-note">
        <strong>官网仍启用、未匹配校内 Key</strong>
        <ul><li v-for="key in unmatchedWebsiteKeys" :key="key.id">{{ key.name }}<template v-if="key.group_id != null"> · 分组 {{ key.group_id }}</template></li></ul>
      </div>
    </section>
    <section v-if="billingRows.length" class="beichen-card" aria-label="北辰 Key 计费倍率">
      <h3>Key 分组与倍率</h3>
      <p v-for="billing in billingRows" :key="billing.credential_id" :role="billing.alert ? 'alert' : undefined" class="beichen-note">
        <strong>{{ billing.label }}</strong>
        <template v-if="billing.websiteKey?.group_id != null">
          · 分组 {{ billing.websiteKey.group_id }}<template v-if="billing.websiteKey.group_name"> · {{ billing.websiteKey.group_name }}</template>
          · 分组倍率 {{ keyGroupRate(billing.websiteKey.rate ?? undefined) || '待同步' }}
          <template v-if="billing.currentBilling">
            · 生效倍率 {{ rateLabel(billing.currentBilling.effective_multiplier_text ?? billing.currentBilling.effective_multiplier) }}
            <template v-if="billing.currentBilling.peak_enabled"> · 高峰系数 {{ rateLabel(billing.currentBilling.peak_rate) }}</template>
          </template>
        </template>
        <template v-else>
          · 当前 Key 分组未匹配
          <template v-if="billing.previousGroup">
            · {{ lastKnownKeyGroupLabel }} {{ billing.previousGroup.id }}<template v-if="billing.previousGroup.name"> · {{ billing.previousGroup.name }}</template>
            <template v-if="billing.previousGroup.rate != null"> · 分组倍率 {{ keyGroupRate(billing.previousGroup.rate) }}</template>
          </template>
          <template v-if="billing.effective_multiplier_text || billing.effective_multiplier != null">
            · 上次生效倍率 {{ rateLabel(billing.effective_multiplier_text ?? billing.effective_multiplier) }}
          </template>
        </template>
        <strong v-if="billing.billing_state === 'stale'">
          · {{ billing.websiteKey?.group_id != null ? '生效倍率待重新核实' : '倍率已陈旧，保留上次记录' }}
        </strong>
        <span v-else-if="billing.billing_state === 'unverified'"> · 计费倍率待核实</span>
        · 上次成功 {{ syncTime(billing.last_success) }}
        <strong v-if="billing.alert"> · 计费信息连续读取失败 {{ billing.failure_count === 6 ? '至少 6' : billing.failure_count }} 次</strong>
        <span v-if="(!billing.websiteKey || billing.websiteKey.group_id === billing.group_id) &&
          (keyGroupIssue(billing) || billing.last_error || billing.group_id == null)">
          · {{ keyGroupIssue(billing) || billing.last_error || '分组待同步' }}
        </span>
      </p>
    </section>
    <BeichenKeyCapabilities v-if="keyCapabilities.length" :key="providerId"
      :provider-id="providerId" :capabilities="keyCapabilities" :disabled="busy" @refresh="refreshOverview" />
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
      </div>
    </details>
    <p v-if="rescanError" class="error" role="alert">{{ rescanError }}</p>
    <div class="beichen-toolbar model-toolbar">
      <div class="beichen-toolbar-group">
        <button type="button" class="button ghost" :disabled="busy" @click="refreshOverview">刷新模型</button>
        <button type="button" class="button secondary" :disabled="busy" @click="syncPrices">{{ syncing ? '同步中…' : '立即同步价格' }}</button>
        <button v-if="probeRun" type="button" class="button secondary" :disabled="probeSubmitting || probeInProgress || !listedModels.length" @click="requestProbes">
          {{ probeSubmitting ? '提交中…' : '立即探活' }}
        </button>
        <button type="button" class="button secondary" :disabled="busy" @click="previewRebind">重建绑定</button>
        <button type="button" class="button primary" :disabled="busy || !hasBatchEligible" @click="previewBatch">一键启用</button>
      </div>
    </div>
    <p v-if="probeRunLabel" class="beichen-note" role="status">{{ probeRunLabel }}</p>
    <p v-if="probeBudget" class="beichen-note" data-testid="probe-budget" role="status">
      今日已探线路协议 {{ probeBudget.used }}/{{ probeBudget.limit }} 条 · 剩余 {{ probeBudget.remaining }} 条<template v-if="probeBudget.exhausted"> · 已达上限，今日已探线路仍可重探</template>
    </p>
    <p v-if="probeError" class="error" role="alert">{{ probeError }}</p>
    <div class="model-filters" role="group" aria-label="模型状态筛选"><button v-for="item in filters" :key="item.id" type="button" :aria-pressed="filter === item.id" @click="filter = item.id">{{ item.label }}</button><small>当前筛选 {{ rows.length }} 个模型</small></div>
    <p class="beichen-note">学生扣费 = 结算价 × 1.10；100 积分 = 1 元</p>
    <p class="beichen-note">官网报价单位为{{ websiteDirectory?.price_unit ?? '元（北辰余额单位）' }}。</p>
    <p v-if="error && !batchSelection" class="error" role="alert">{{ error }}</p>
    <p v-if="providerMaintenance && loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="priceErrors.length" class="error" role="alert">价格同步失败：{{ priceErrors.join('；') }}</p>
    <div v-if="loading && !overview" class="model-skeleton" role="status"><span class="sr-only">正在读取模型…</span><i v-for="index in 3" :key="index" aria-hidden="true" /></div>
    <div v-if="rows.length" class="model-list" data-layout="responsive-cards">
      <table class="model-table">
        <caption class="sr-only">北辰模型覆盖、价格与本月用量</caption>
        <colgroup><col class="model-col"><col class="state-col"><col class="price-col"><col class="usage-col"><col class="action-col"></colgroup>
        <thead>
          <tr><th scope="col">模型</th><th scope="col">状态与覆盖</th><th scope="col">价格</th><th scope="col">本月用量</th><th scope="col">操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="row.model" :data-model="row.model" :class="{ 'binding-issue-row': row.bindingIssue }">
            <th scope="row" class="model-name">{{ row.model }}</th>
            <td data-label="状态与覆盖" class="model-state">
              <div class="model-state-summary">
                <span>Key 覆盖 {{ row.boundKeyCount ? row.keyCoverage + '/' + row.boundKeyCount : '—' }}</span> ·
                <span>已发布线路 {{ row.readyCount }}</span>
                <span v-if="row.catalogPending" class="status-pill pending">目录待复核</span>
              </div>
              <div class="model-capability" data-testid="model-capability">
                <strong :class="row.bindingIssue ? 'binding-issue' : row.active ? 'published-state' : undefined">{{ publicationLabel(row) }}</strong>
                <span class="status-pill" :class="availabilityTone(row)" data-testid="model-availability">{{ availabilityLabel(row) }}</span>
                <span v-if="row.groupMismatch" class="status-pill pending" data-testid="group-mismatch"
                  :title="row.missingGroup ? 'Key 分组不存在' : undefined">分组不匹配</span>
                <span v-for="denied in row.deniedGroups" :key="denied" class="status-pill danger" data-testid="group-denied">{{ denied }}</span>
                <span v-if="row.groupRateMismatch" class="status-pill pending">分组计费规则待核对</span>
                <span v-if="!row.websiteOnly && row.protocolMissing" class="status-pill pending" data-testid="protocol-missing">缺协议</span>
                <span v-if="row.priceMissing" class="status-pill pending" data-testid="price-missing">{{ missingPriceLabel(row) }}</span>
                <span v-if="row.bindingIssue && !row.primaryGroupDenial" class="status-pill danger" data-testid="binding-invalid">绑定待核对</span>
                <span v-if="!row.websiteOnly" class="chip recommend" data-testid="recommended-protocol">{{ row.recommendedProtocol
                  ? '推荐 ' + protocolLabel(row.recommendedProtocol)
                  : '推荐协议待核实' }}</span>
              </div>
              <small v-if="row.source" class="row-note" data-testid="catalog-source">来源 {{ sourceLabels[row.source] }}</small>
              <details v-if="row.locations.length || row.catalogKeys.length || row.boundRoutes.length" class="catalog-evidence">
                <summary>目录来源与分组</summary>
                <ul v-if="row.boundRoutes.length" aria-label="实际绑定线路">
                  <li v-for="(route, routeIndex) in row.boundRoutes" :key="routeIndex">
                    {{ route.school_model }} · {{ route.upstream_model }} · {{ route.credential_label || 'Key 待核实' }} · {{ protocolLabel(route.protocol) }}
                    <template v-if="route.group_id != null"> · 分组 {{ route.group_id }}</template>
                    · {{ route.group_status === 'not_allowed' ? '分组无权限' : route.group_status === 'not_found' ? 'Key 分组不存在'
                      : route.catalog_state === 'key_listed' ? '目录有' : route.catalog_state === 'catalog_stale' || route.catalog_state === 'catalog_unverified' ? '目录待复核' : '目录未列出' }}
                    · {{ boundPriceLabel(route) }} · {{ route.price_covered ? '报价已覆盖' : '报价未覆盖' }}
                    · {{ route.binding_ready ? '已发布' : route.published ? '线路待核对' : '未发布' }} · {{ probeLabels[route.probe_state as keyof typeof probeLabels] ?? '待探活' }}
                  </li>
                </ul>
                <ul>
                  <li v-for="(location, sourceIndex) in row.locations" :key="sourceIndex">
                    {{ websiteLocationLabel(location) }} · {{ syncTime(location.observed_at) }}
                  </li>
                  <li v-for="key in row.catalogKeys" :key="key.credential_id"
                    :data-testid="key.state !== 'listed' ? 'key-catalog-gap' : undefined">{{ keyCoverageLabel(key) }}</li>
                </ul>
              </details>
              <small v-if="!row.websiteOnly && row.availabilityReason" class="row-note" data-testid="availability-reason">{{ row.availabilityReason }}</small>
              <small v-if="!row.websiteOnly && row.actionHint" class="row-note" data-testid="action-hint">下一步：{{ row.actionHint }}</small>
              <small v-if="row.bindingIssue && (!row.primaryGroupDenial || !row.deniedGroups.length)" class="row-note binding-issue">{{ row.bindingIssue.message }}</small>
              <small v-if="row.protocolLabels.length" class="row-note">{{ row.protocolLabels.join(' · ') }}</small>
              <div v-if="!row.websiteOnly && row.probeState" class="model-probe">
                <span class="status-pill" :class="{ active: row.probeState === 'passed', danger: row.probeState === 'failed', pending: row.probeState === 'unprobed' }">{{ probeLabels[row.probeState] }}</span>
                <small v-if="row.lastProbeAt">{{ syncTime(row.lastProbeAt) }}（北京时间）</small>
              </div>
            </td>
            <td data-label="价格" class="model-price">
              <div class="model-price-content">
                <span v-if="row.mode !== 'follow'" class="status-pill manual model-price-mode">
                  {{ row.mode === 'manual' ? '手动价生效' : '部分 Key 使用手动价' }} · 官网报价供参考
                </span>
                <small v-if="row.currentPriceRoute" class="row-note" data-testid="selected-route-price">
                  首选线路 {{ row.currentPriceRoute.credential_label }} · {{ boundPriceLabel(row.currentPriceRoute) }}
                </small>
                <BeichenGroupPrices v-if="row.groupPrices.current" class="current-group-prices"
                  :row="row.groupPrices.current" :title="row.groupPrices.currentTitle"
                  :note="row.groupPrices.baseNote" :derived-note="row.groupPrices.derivedNote">
                  <template #actions>
                    <small v-if="row.groupPrices.observedAt && row.groupPrices.observedLabel" class="group-price-note">
                      采集时间 <time :datetime="row.groupPrices.observedAt">{{ row.groupPrices.observedLabel }}（北京时间）</time>
                    </small>
                  </template>
                </BeichenGroupPrices>
                <small v-else-if="row.groupPrices.actualRates" class="row-note">
                  学生实价（积分 / 1M） · 输入 {{ row.groupPrices.activePrices.input ?? '—' }} · 输出 {{ row.groupPrices.activePrices.output ?? '—' }}
                </small>
                <small v-if="row.groupPrices.tierNote" class="row-note" data-testid="price-tier-note">{{ row.groupPrices.tierNote }}</small>
                <button v-if="row.detailQuotes.length || row.detailWebsite" type="button" class="price-link" @click="conditionsModel = row.model">查看档位</button>
                <p v-if="row.groupPrices.unmatched"
                  class="group-missing-note" data-testid="current-group-missing">当前 Key 分组未匹配</p>
              </div>
            </td>
            <td data-label="本月用量" class="model-usage">
              <button type="button" class="usage-link" :aria-label="'查看 ' + row.model + ' 用量'" @click="openUsage(row.model)">{{ modelCredits(row.model) }}</button>
              <small v-if="modelUsage(row.model) && usageDispatchedCalls(modelUsage(row.model)) !== 0" class="row-note">Token {{ schoolUsageTokens(modelUsage(row.model)) }}</small>
              <small v-if="usagePendingReviewCalls(modelUsage(row.model))" class="row-note">待核 {{ formatTokens(usagePendingReviewCalls(modelUsage(row.model))) }} 笔</small>
            </td>
            <td data-label="操作">
              <div class="model-actions">
                <button v-if="row.listed" type="button" class="button ghost" :disabled="busy" :aria-label="'定价 ' + row.model" @click="priceRow = row">{{ row.mode !== 'follow' ? '调整价格' : '手动定价' }}</button>
                <button v-if="!row.active && !row.websiteOnly && (!row.published || row.activation === 'ready')" type="button" class="button secondary" :disabled="busy || Boolean(canEnableReason(row))"
                  :aria-describedby="canEnableReason(row) ? reasonId + '-' + index : undefined" :aria-label="'启用 ' + row.model"
                  @click="changeStatus(row, true)">{{ updating === row.model ? '启用中…' : '启用' }}</button>
                <button v-else-if="row.canDisable" type="button" class="button ghost" :disabled="busy"
                  :aria-label="'禁用 ' + row.model" @click="changeStatus(row, false)">禁用</button>
                <small v-if="!row.active && !row.published && canEnableReason(row)" :id="reasonId + '-' + index" class="row-note enable-reason">{{ canEnableReason(row) }}</small>
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
  <AppDialog :open="Boolean(conditionsRow)" :title="(conditionsRow?.model ?? '模型') + ' · 计价档位'" description="查看计价条件、结算价及学生价。"
    wide @update:open="open => { if (!open) conditionsModel = undefined }">
    <div class="beichen-stack">
      <p v-if="conditionsRow && (conditionsRow.routeBlocked || conditionsRow.bindingIssue)" class="beichen-note" data-testid="tier-route-reason">
        {{ conditionsRow.bindingIssue?.message ?? (conditionsRow.blockers.join('；') || '线路未就绪，请刷新模型或检查渠道配置') }}
      </p>
      <p v-if="conditionsRow?.priceBlocker" class="beichen-note price-blocker-note" data-testid="price-blocker-message">
        {{ conditionsRow.priceBlocker }}
      </p>
      <div v-if="conditionsRow?.detailWebsite">
        <strong>官网目录参考价</strong>
        <p class="price-detail">{{ withYuan(conditionsRow.detailWebsite) }}</p>
      </div>
      <section v-for="quote in conditionsRow?.detailQuotes ?? []" :key="quote.key" class="condition-quote">
        <h3>{{ [quote.routeLabel, quote.multiplierLabel].filter(Boolean).join(' · ') }}</h3>
        <template v-if="quote.conditionalPrice">
          <section v-for="band in [quote.conditionalPrice.base, ...quote.conditionalPrice.intervals].filter(band => band != null)"
            :key="band.label" class="condition-tier" data-testid="conditional-tier">
            <h4>{{ band.label }}</h4>
            <table class="beichen-table condition-tier-table">
              <caption class="sr-only">{{ band.label }}每百万 Token 单价</caption>
              <thead><tr><th scope="col">分项</th><th scope="col">官网价（元 / 1M）</th>
                <th scope="col">结算价（积分 / 1M）</th><th scope="col">学生价（积分 / 1M）</th></tr></thead>
              <tbody>
                <tr v-for="item in band.rows" :key="item.key">
                  <th scope="row">{{ item.label }}</th>
                  <td class="numeric-cell" data-label="官网价（元 / 1M）">{{ item.yuan ?? '—' }}</td>
                  <td class="numeric-cell" data-label="结算价（积分 / 1M）">{{ item.settlement ?? '—' }}</td>
                  <td class="numeric-cell" data-label="学生价（积分 / 1M）" :data-student-price="item.key">{{ item.student ?? '—' }}</td>
                </tr>
              </tbody>
            </table>
          </section>
          <details v-if="hasAdditionalTokenConditions(quote.conditions)">
            <summary>其他计价条件</summary>
            <p v-if="quote.standard" class="price-detail">{{ withYuan(quote.standard) }}</p>
          </details>
        </template>
        <dl v-else class="condition-prices">
          <div><dt>{{ quote.status === 'derived' ? '反推基础价' : '官网价' }}</dt><dd class="price-detail">{{ quote.standard ? withYuan(quote.standard) : '尚未取得报价明细' }}</dd></div>
          <div><dt>结算价</dt><dd class="price-detail">{{ quote.actual ? withYuan(quote.actual) : '尚无可用单价' }}</dd></div>
          <div><dt>学生价</dt><dd class="price-detail">{{ quote.mode === 'manual' ? '请查看手动定价' : quote.actual ? withYuan(quote.actual, '1.10') : '尚无可用单价' }}</dd></div>
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
.monthly-stats dd small { font-weight: 400; }
.model-summary { grid-template-columns: repeat(auto-fit, minmax(min(100%, 130px), 1fr)); }
.model-toolbar { justify-content: flex-end; }
.model-diagnostics > summary { cursor: pointer; font-weight: 700; }
.model-diagnostics > summary .status-pill { margin-inline-start: 8px; }
.diagnostic-content { margin-top: 12px; }
.model-list { container-type: inline-size; }
.model-table { width: 100%; table-layout: fixed; border-collapse: separate; border-spacing: 0; font-size: 13px; }
.model-col { width: 13%; } .state-col { width: 17%; } .price-col { width: 50%; } .usage-col { width: 9%; } .action-col { width: 11%; }
.model-table > thead > tr > th { color: var(--ink-soft); font-size: 11px; }
.model-table > :is(thead, tbody) > tr > :is(th, td) {
  min-width: 0; padding: 12px 10px; border-bottom: 1px solid var(--line); vertical-align: top; text-align: left;
}
.model-state { font-size: 12px; line-height: 1.75; }
.model-state-summary span { white-space: nowrap; }
.model-price-content { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; }
.model-price-mode { justify-self: start; }
.group-price-note, .group-missing-note { color: var(--ink-soft); font-size: 12px; }
.group-missing-note { margin: 0; }
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
.price-link, .usage-link { padding: 0; border: 0; background: transparent; text-align: left; color: var(--primary-strong); font-weight: 700; font-size: 12px; overflow-wrap: anywhere; }
.price-link { justify-self: start; }
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
.condition-tier h4 { font-size: 13px; margin: 12px 0 4px; }
.condition-tier-table th, .condition-tier-table .numeric-cell { white-space: normal; overflow-wrap: anywhere; }
.condition-prices { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 24px; margin: 0; }
.condition-prices dt { font-weight: 700; font-size: 13px; margin-bottom: 8px; }
.price-detail { white-space: pre-line; overflow-wrap: anywhere; color: var(--ink-soft); font-variant-numeric: tabular-nums; font-size: 13px; margin: 0; line-height: 1.7; }
button:focus-visible, summary:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@container (max-width: 60rem) {
  .model-table { display: block; }
  .model-table > colgroup, .model-table > thead { display: none; }
  .model-table > tbody { display: grid; gap: 16px; }
  .model-table > tbody > tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 10px; padding: 16px 0; border: 1px solid var(--line); border-radius: 14px; }
  .model-table > tbody > tr > :is(th, td) { display: block; width: auto; padding: 0 14px; border: 0; }
  .model-name, .model-state, .model-price { grid-column: 1 / -1; }
  .model-table td[data-label]::before { content: attr(data-label); display: block; margin-bottom: 6px; font-size: 11px; color: var(--ink-soft); }
  .model-actions .row-note { margin-top: 0; }
}
@container (max-width: 29rem) {
  .model-table > tbody > tr { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 900px) {
  .condition-prices { grid-template-columns: minmax(0, 1fr); gap: 16px; }
  .condition-tier-table tbody tr { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .condition-tier-table tbody th { grid-column: 1 / -1; }
  .condition-tier-table td[data-label]::before {
    content: attr(data-label); display: block; color: var(--ink-soft); font-size: 11px;
  }
}
@media (max-width: 600px) {
  .model-filters small { margin-left: 0; flex-basis: 100%; }
  .beichen-toolbar-group { width: 100%; }
  .beichen-toolbar-group > .button { flex: 1; }
}
</style>
