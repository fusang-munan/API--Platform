import type { BeichenCatalogModel, BeichenDataset, BeichenModel, BeichenOverview, BeichenPreferredRoute, BeichenPriceConditions, BeichenPriceTier, BeichenProtocolCapability, BeichenUsageTotals, BeichenWebsiteKey, BeichenWebsiteKeyInventory } from '../api/types'
import { formatTokens, multiplyDecimal, shiftDecimal } from './credits'

export function isBeichenProvider(baseUrl: string) {
  const address = baseUrl.trim()
  const origin = 'https://api.beichenapi.com'
  return address.slice(0, origin.length).toLowerCase() === origin
    && ['', '/', '/v1', '/v1/'].includes(address.slice(origin.length))
}

export type SchoolQuote = {
  key: string
  multiplier: string
  multiplierLabel: string
  standard: string | null
  actual: string | null
  actualRates: { input: string | null; output: string | null; cache_read: string | null; cache_write: string | null } | null
  status: 'confirmed' | 'conditional' | 'derived' | 'not_listed' | 'pending'
  statusLabel: string
  issue?: string
  notes: string[]
  mode: 'follow' | 'manual'
  priceReady: boolean
  conditions?: BeichenPriceConditions | null
  rateValue?: string | null
  officialSource?: PriceSource
  schoolSource?: PriceSource
  officialObservedAt?: string | null
  schoolObservedAt?: string | null
  credentialId?: string
  upstreamModel?: string
  routeLabel?: string
  conditionalPrice?: SchoolConditionalPrice
}

export type PriceSource = 'website' | 'derived' | 'manual'
export type PriceDetailKey = 'input' | 'output' | 'cache_write_5m' | 'cache_write_1h' | 'cache_read'
export type PriceDetailRow = {
  key: PriceDetailKey
  label: string
  yuan: string | null
  credits: string | null
  missing: boolean
  notApplicable: boolean
  shared?: boolean
}
export type PriceDetailView = {
  billingModeLabel: string
  rows: PriceDetailRow[]
  source: PriceSource
  sourceLabel: string
  observedAt: string | null
  observedLabel: string
}

type SchoolTokenPriceBand = {
  label: string
  rows: Array<Pick<PriceDetailRow, 'key' | 'label' | 'yuan'> & { settlement: string | null; student: string | null }>
}
type SchoolConditionalPrice = {
  base: SchoolTokenPriceBand | null
  intervals: Array<SchoolTokenPriceBand & { minTokens: string; maxTokens: string | null }>
  observedAt: string | null
}

export const priceSourceLabels: Record<PriceSource, string> = {
  website: '官网正式价',
  derived: '历史流水反推价',
  manual: '手动价',
}

export const priceDetailFields = [
  { key: 'input', label: '输入', priceKey: 'input_price', rateKey: 'input' },
  { key: 'output', label: '输出', priceKey: 'output_price', rateKey: 'output' },
  { key: 'cache_write_5m', label: '缓存写入(5m)', priceKey: 'cache_write_price', rateKey: 'cache_write' },
  { key: 'cache_write_1h', label: '缓存写入(1h)', priceKey: 'cache_write_1h_price', rateKey: 'cache_write' },
  { key: 'cache_read', label: '缓存读取', priceKey: 'cache_read_price', rateKey: 'cache_read' },
] as const

const billingModeLabels: Record<string, string> = {
  token: '按 Token（每 1M Token 计价）',
  per_request: '按次',
  video: '按视频秒数',
  image: '按图像',
  unknown: '单位未知',
}

export function billingModeLabel(value?: string | null) {
  const mode = (value ?? '').trim().toLowerCase()
  if (!mode) return '按 Token（官网未声明）'
  return billingModeLabels[mode] ?? '其他（' + value + '）'
}

export function formatObservedAt(value?: string | null) {
  if (!value) return null
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return null
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}
export type SchoolCredentialPrice = {
  credential_id: string
  label: string
  mode: 'follow' | 'manual'
  priceReady: boolean
}
export type SchoolCatalogRow = {
  model: string
  school: string
  status?: string | null
  canEnable?: boolean | null
  canDisable: boolean
  routeCount: number
  readyCount: number
  priceReady: boolean
  mode: 'follow' | 'manual' | 'mixed'
  credentials: SchoolCredentialPrice[]
  quotes: SchoolQuote[]
}

export type TeacherBoundRoute = BeichenProtocolCapability & {
  school_model: string
  upstream_model: string
  credential_id: string | null
  credential_label: string
  priority: number
  published: boolean
  key_enabled: boolean
  group_id: number | null
  group_status?: string
  group_checked_at?: string
  catalog_state: string
  price_state: string
  price_covered: boolean
}

export type TeacherCatalogModel = Omit<BeichenCatalogModel, 'price_state'> & {
  price_state?: BeichenCatalogModel['price_state'] | 'unverified'
  bound_routes?: TeacherBoundRoute[]
  bound_key_count?: number
  covered_bound_key_count?: number
  bound_catalog_pending?: boolean
  route_availability?: {
    protocols: string[]
    available_protocols: string[]
    recommended_protocol: string | null
    availability: string
    availability_reason?: string
  }
  action_hint?: string
  historical_channels?: string[]
}

export type TeacherOverview = Omit<BeichenOverview, 'model_catalog'> & { model_catalog?: TeacherCatalogModel[] | null }

export type SchoolUsageMetrics = {
  credits?: string | null
  tokens?: number | null
  calls?: number | null
  pending_calls?: number | null
} & Pick<BeichenUsageTotals, 'completeness' | 'tokens_missing'>

export function schoolUsageCredits(metrics: SchoolUsageMetrics, withUnit = false) {
  const credits = shiftDecimal(metrics.credits, 0)
  if (credits === '—') return '积分未报告'
  if (credits === '0' && metrics.calls && metrics.pending_calls === 0) {
    return `0 积分 · ${formatTokens(metrics.calls)} 次（零额）`
  }
  if (credits === '0' && metrics.pending_calls && metrics.pending_calls === metrics.calls) return '暂无已扣积分'
  return withUnit ? credits + ' 积分' : credits
}

export function schoolUsageTokens(metrics?: SchoolUsageMetrics) {
  if (!metrics) return '—'
  if (metrics.tokens == null || metrics.tokens === 0 && metrics.completeness === 'missing' && metrics.calls !== 0) {
    return '未回报'
  }
  if (metrics.tokens === 0 && metrics.completeness == null && metrics.calls !== 0) return '待确认'
  return formatTokens(metrics.tokens)
}

type WebsiteKeyRateVersion = BeichenWebsiteKey & { observed_at?: string | null }
type WebsiteKeyGroups = {
  current: boolean
  matched: ReadonlyMap<string, WebsiteKeyRateVersion>
}

export const lastKnownKeyGroupLabel = '上次分组（未确认）'

export function indexWebsiteKeyGroups(inventory?: BeichenWebsiteKeyInventory): WebsiteKeyGroups {
  const matched = new Map<string, WebsiteKeyRateVersion>()
  for (const key of inventory?.keys ?? []) {
    if (key.match_status === 'matched' && key.credential_id) matched.set(key.credential_id, key)
  }
  return { current: inventory?.status === 'current', matched }
}

export function lastKnownKeyGroup(key?: WebsiteKeyRateVersion, billing?: BeichenDataset, catalogGroupId?: number | null) {
  const id = key?.group_id ?? billing?.group_id ?? catalogGroupId
  if (id == null) return undefined
  const sameWebsiteGroup = key?.group_id === id
  const sameBillingGroup = billing?.group_id === id
  return {
    id,
    name: (sameWebsiteGroup ? key.group_name : undefined) ?? (sameBillingGroup ? billing.group_name : undefined),
    rate: (sameWebsiteGroup ? key.rate : undefined) ?? (sameBillingGroup ? billing.group_rate : undefined),
  }
}

export function verifiedWebsiteKeyBilling(key: WebsiteKeyRateVersion, billing?: BeichenDataset) {
  // 概览里的分组字段可能已更新，生效倍率仍须有同组、同版本的计费观测。
  // 同倍率重复核实不会新增倍率版本，observed_at 会停留在首次观测；
  // last_success 才是最近一次计费读取成功的时间，优先用它判断核实时间。
  const verifiedAt = billing?.last_success || billing?.observed_at
  if (key.group_id != null && billing?.billing_state === 'current' && !billing.last_error && !keyGroupIssue(billing) &&
    billing.group_id === key.group_id && keyGroupRate(billing.group_rate) === keyGroupRate(key.rate ?? undefined) &&
    key.observed_at && verifiedAt && Date.parse(verifiedAt) >= Date.parse(key.observed_at)) {
    return billing
  }
}

export function websiteKeyEffectiveRate(key: WebsiteKeyRateVersion, billing?: BeichenDataset) {
  if (key.group_id == null || (billing?.group_id === key.group_id && billing.group_status === 'not_allowed')) return null
  const verified = verifiedWebsiteKeyBilling(key, billing)
  // 官网 Key 的分组倍率只是参考值；没有同组、同版本的计费观测时不得当作生效倍率。
  return verified?.effective_multiplier_text ?? verified?.effective_multiplier ?? null
}

export function priceBelongsToWebsiteKeyGroup(key: WebsiteKeyRateVersion, observedAt?: string | null, groupId?: number | null) {
  if (key.group_id == null || groupId != null && groupId !== key.group_id) return false
  if (key.observed_at) return Boolean(observedAt && Date.parse(observedAt) >= Date.parse(key.observed_at))
  return true
}

export function schoolPriceRoutes(entry: TeacherCatalogModel, providerId: string, school: string): BeichenPreferredRoute[] {
  const preferred = (entry.preferred_routes ?? []).filter(route => route.provider_id === providerId &&
    route.school_model.toLowerCase() === school.toLowerCase() && route.credential_id)
  const bindings = entry.bound_routes
  if (!bindings) return preferred
  const published = entry.school_status === 'active' || bindings.some(route => route.published)
  const allowed = bindings.filter(route => route.credential_id && (published ? route.binding_ready :
    route.key_enabled && route.catalog_state === 'key_listed' && !['pending', 'not_allowed', 'not_found'].includes(route.group_status ?? '')))
  if (!bindings.length && !published) return preferred
  return allowed.map(route => ({
    school_model: route.school_model, upstream_model: route.upstream_model, adapter: route.protocol,
    provider_id: providerId, credential_id: route.credential_id, credential_label: route.credential_label,
    priority: route.priority, state: route.binding_ready ? 'ready' : 'candidate',
  }))
}

export function matchingSchoolBinding(routes: TeacherBoundRoute[] | undefined, route: BeichenPreferredRoute) {
  return routes?.find(binding => binding.credential_id === route.credential_id && binding.school_model === route.school_model &&
    binding.upstream_model === route.upstream_model && binding.protocol === route.adapter)
}

export function schoolCatalog(overview: TeacherOverview, providerId: string, websiteGroups?: WebsiteKeyGroups): SchoolCatalogRow[] {
  const records = new Map<string, BeichenModel[]>()
  const prices = new Map<string, BeichenModel>()
  for (const row of overview.models ?? []) {
    const key = row.model.toLowerCase()
    prices.set(row.credential_id + ':' + key, row)
    const group = records.get(key)
    if (group) group.push(row)
    else records.set(key, [row])
  }
  const datasets = new Map(overview.datasets?.map(row => [row.kind + ':' + row.credential_id, row]))
  const catalog: TeacherCatalogModel[] = overview.model_catalog ??
    Array.from(records.values(), rows => ({ model: rows[0].model }))
  return catalog.map(entry => {
    const rows = records.get(entry.model.toLowerCase()) ?? []
    const routes = entry.preferred_routes ?? []
    const names = new Set(routes.map(route => route.school_model))
    const legacy = entry.can_enable == null && entry.school_status == null
    const school = entry.school_model ?? (legacy && names.size === 1 ? routes[0].school_model : entry.model)
    const selected = routes.filter(route => route.school_model.toLowerCase() === school.toLowerCase())
    const statuses = new Set(rows.map(row => row.school_status))
    const ready = selected.some(route => route.state === 'ready')
    const ambiguous = !entry.school_model && names.size > 1 && !selected.length
    const status = ambiguous ? undefined
      : entry.school_status ?? (ready ? 'active' : statuses.size === 1 ? rows[0].school_status : undefined)
    const canDisable = status === 'active' && (entry.school_status === 'active' || selected.length > 0)
    const quotes = new Map<string, ReturnType<typeof schoolQuote>>()
    const credentials = new Map<string, { credential_id: string; label: string; mode: 'follow' | 'manual'; priceReady: boolean }>()
    const catalogGroups = new Map(entry.catalog_keys?.map(key => [key.credential_id, key.group_id]))
    for (const route of schoolPriceRoutes(entry, providerId, school)) {
      if (!route.credential_id) continue
      const upstreamName = (route.upstream_model ?? entry.model).toLowerCase()
      const record = prices.get(route.credential_id + ':' + upstreamName)
      const billing = datasets.get('billing:' + route.credential_id)
      const priceDataset = datasets.get('prices:' + route.credential_id)
      const matchedKey = websiteGroups?.matched.get(route.credential_id)
      const websiteKey = websiteGroups?.current ? matchedKey : undefined
      const previousGroup = lastKnownKeyGroup(websiteKey?.group_id != null ? undefined : matchedKey,
        billing, catalogGroups.get(route.credential_id))
      const binding = matchingSchoolBinding(entry.bound_routes, route)
      const quote = schoolQuote(record, route.effective_multiplier_text ?? route.effective_multiplier,
        route.state === 'candidate', billing, priceDataset, websiteKey, previousGroup, binding)
      quotes.set(JSON.stringify(quote), quote)
      credentials.set(route.credential_id, {
        credential_id: route.credential_id,
        label: route.credential_label ?? '未命名 Key',
        mode: quote.mode,
        priceReady: quote.priceReady,
      })
    }
    const quoteList = Array.from(quotes, ([key, quote]) => ({ key, ...quote }))
    const modes = new Set(quoteList.map(quote => quote.mode))
    const mode = modes.size > 1 ? 'mixed' : modes.has('manual') ? 'manual' : 'follow'
    return {
      model: entry.model, school, status, canEnable: entry.can_enable, canDisable,
      routeCount: selected.length,
      readyCount: selected.filter(route => route.state === 'ready').length,
      priceReady: quoteList.length > 0 && quoteList.every(quote => quote.priceReady),
      mode,
      credentials: Array.from(credentials.values()),
      quotes: quoteList,
    }
  })
}

const priceStatusLabels = {
  confirmed: '报价已确认',
  conditional: '按条件计价',
  derived: '反推价，待核对',
  not_listed: '官网未提供同名报价',
  pending: '单价待同步',
}

export function keyGroupIssue(billing?: Pick<BeichenDataset, 'group_status' | 'group_id'>) {
  if (billing?.group_status === 'pending') return '分组待同步'
  if (billing?.group_status === 'not_found') return 'Key 分组不存在'
  if (billing?.group_status === 'not_allowed' && billing.group_id != null) return `账号无 group ${billing.group_id} 权限`
  return ''
}

export function keyGroupRate(rate: string | undefined) {
  const value = nonNegativeDecimal(rate)
  if (!value) return ''
  const [whole, fraction = ''] = formatDecimal(value).split('.')
  return whole + '.' + fraction.padEnd(2, '0') + '×'
}

export type PriceRateValues = Partial<Record<'input' | 'output' | 'cache_read' | 'cache_write', string | number | null>>

function nonTokenPriceLabel(mode: string) {
  if (mode === 'per_request') return '每次请求'
  if (mode === 'video') return '每秒'
  if (mode === 'image') return '图像单价'
  return '基础单价'
}

function scaleDecimal(value: string | number | null | undefined, factor: string) {
  const amount = nonNegativeDecimal(value)
  const multiplier = nonNegativeDecimal(factor)
  if (!amount || !multiplier) return null
  return formatDecimal({ coefficient: amount.coefficient * multiplier.coefficient, scale: amount.scale + multiplier.scale })
}

/** 固定展示的五项 Token 单价，统一换算成每 1M Token 的元与积分。 */
export function priceDetailRows(terms: BeichenPriceConditions | null | undefined, rateText?: string | null): PriceDetailRow[] {
  const mode = terms?.billing_mode?.trim().toLowerCase() ?? ''
  if (mode && mode !== 'token') {
    return priceDetailFields.map(field => ({
      key: field.key, label: field.label, yuan: null, credits: null, missing: false, notApplicable: true,
    }))
  }
  const rate = rateText == null || rateText === '' ? null : nonNegativeDecimal(rateText)
  return priceDetailFields.map(field => {
    const raw = terms?.[field.priceKey]
    const amount = nonNegativeDecimal(raw)
    if (!amount) return { key: field.key, label: field.label, yuan: null, credits: null, missing: true, notApplicable: false }
    const coefficient = amount.coefficient * (rate?.coefficient ?? 1n)
    const scale = amount.scale + (rate?.scale ?? 0)
    return {
      key: field.key, label: field.label,
      yuan: formatDecimal({ coefficient, scale: scale - 6 }),
      credits: formatDecimal({ coefficient, scale: scale - 8 }),
      missing: false, notApplicable: false,
    }
  })
}

/** 手动价与旧接口的已生效倍率都以积分/百万 Token 保存。 */
export function manualPriceDetailRows(rates: PriceRateValues | null | undefined, factor = '1'): PriceDetailRow[] {
  const multiplier = nonNegativeDecimal(factor)
  return priceDetailFields.map(field => {
    const shared = field.key === 'cache_write_1h'
    const raw = rates?.[field.rateKey] ?? (shared ? rates?.cache_write : null)
    const amount = nonNegativeDecimal(raw)
    if (!amount) return { key: field.key, label: field.label, yuan: null, credits: null, missing: true, notApplicable: false, shared }
    const coefficient = amount.coefficient * (multiplier?.coefficient ?? 1n)
    const scale = amount.scale + (multiplier?.scale ?? 0)
    return {
      key: field.key, label: field.label,
      credits: formatDecimal({ coefficient, scale }),
      yuan: formatDecimal({ coefficient, scale: scale + 2 }),
      missing: false, notApplicable: false, shared,
    }
  })
}

const detailTextLabels: Record<PriceDetailKey, string[]> = {
  input: ['输入'],
  output: ['输出'],
  cache_write_5m: ['缓存写入(5m)', '缓存写入'],
  cache_write_1h: ['缓存写入(1h)', '缓存写入（1 小时）'],
  cache_read: ['缓存读取'],
}

function priceDetailRowsFromText(text: string | null | undefined, factor = '1'): PriceDetailRow[] {
  const line = (text ?? '').split('\n').find(row => row.startsWith(unitLabels.token))
  const amounts = line?.split(' · ').slice(1) ?? []
  return priceDetailFields.map(field => {
    let amount: string | undefined
    for (const label of detailTextLabels[field.key]) {
      const found = amounts.find(row => row.startsWith(label + ' '))
      if (found) {
        amount = found.slice(label.length + 1)
        break
      }
    }
    const credits = scaleDecimal(amount, factor)
    if (credits == null) return { key: field.key, label: field.label, yuan: null, credits: null, missing: true, notApplicable: false }
    return { key: field.key, label: field.label, credits, yuan: shiftDecimal(credits, -2), missing: false, notApplicable: false }
  })
}

export function missingPriceDetailLabels(terms: BeichenPriceConditions | null | undefined): string[] {
  if (!terms) return priceDetailFields.map(field => field.label)
  const mode = terms.billing_mode?.trim().toLowerCase() ?? ''
  if (mode && mode !== 'token') return terms.per_request_price == null ? [nonTokenPriceLabel(mode)] : []
  if (Array.isArray(terms.intervals) && terms.intervals.length) return []
  return priceDetailFields.filter(field => terms[field.priceKey] == null).map(field => field.label)
}

export function webPriceDetail(terms: BeichenPriceConditions | null | undefined, rateText: string | null | undefined,
  source: PriceSource, observedAt?: string | null): PriceDetailView {
  return {
    billingModeLabel: billingModeLabel(terms?.billing_mode),
    rows: priceDetailRows(terms, rateText),
    source,
    sourceLabel: priceSourceLabels[source],
    observedAt: observedAt ?? null,
    observedLabel: formatObservedAt(observedAt) ?? '未记录',
  }
}

export function emptyPriceDetail(source: PriceSource = 'website', observedAt?: string | null): PriceDetailView {
  return {
    billingModeLabel: '未提供报价',
    rows: priceDetailFields.map(field => ({
      key: field.key, label: field.label, yuan: null, credits: null, missing: true, notApplicable: false,
    })),
    source,
    sourceLabel: priceSourceLabels[source],
    observedAt: observedAt ?? null,
    observedLabel: formatObservedAt(observedAt) ?? '未记录',
  }
}

export function ratesPriceDetail(rates: PriceRateValues | null | undefined, source: PriceSource,
  observedAt?: string | null, factor = '1'): PriceDetailView {
  return {
    billingModeLabel: '按 Token（已应用倍率）',
    rows: manualPriceDetailRows(rates, factor),
    source,
    sourceLabel: priceSourceLabels[source],
    observedAt: observedAt ?? null,
    observedLabel: formatObservedAt(observedAt) ?? '未记录',
  }
}

export function manualPriceDetail(rates: PriceRateValues | null | undefined, factor = '1', observedAt?: string | null): PriceDetailView {
  return {
    ...ratesPriceDetail(rates, 'manual', observedAt, factor),
    billingModeLabel: '按 Token（手动价）',
  }
}

function textPriceDetail(text: string | null | undefined, source: PriceSource, observedAt?: string | null,
  factor = '1'): PriceDetailView {
  return {
    billingModeLabel: '按 Token（报价明细）',
    rows: priceDetailRowsFromText(text, factor),
    source,
    sourceLabel: priceSourceLabels[source],
    observedAt: observedAt ?? null,
    observedLabel: formatObservedAt(observedAt) ?? '未记录',
  }
}

export function schoolQuoteDetail(quote: SchoolQuote, student: boolean): PriceDetailView {
  const source = (student ? quote.schoolSource : quote.officialSource) ?? 'website'
  const observedAt = (student ? quote.schoolObservedAt : quote.officialObservedAt) ?? null
  if (student && quote.mode === 'manual') return manualPriceDetail(quote.actualRates, '1.10', observedAt)
  if (quote.conditions) {
    if (student && quote.rateValue == null) {
      return quote.actual ? textPriceDetail(quote.actual, source, observedAt, '1.10') : ratesPriceDetail(quote.actualRates, source, observedAt, '1.10')
    }
    const rate = student ? scaleDecimal(quote.rateValue ?? '1', '1.10') : null
    return webPriceDetail(quote.conditions, rate, source, observedAt)
  }
  if (student) {
    return quote.actual ? textPriceDetail(quote.actual, source, observedAt, '1.10')
      : ratesPriceDetail(quote.actualRates, source, observedAt, '1.10')
  }
  return textPriceDetail(quote.standard, source, observedAt)
}

function schoolQuote(record: BeichenModel | undefined, reported: string | number | null | undefined,
  candidate: boolean, billing?: BeichenDataset, priceDataset?: BeichenDataset, websiteKey?: WebsiteKeyRateVersion,
  previousGroup?: ReturnType<typeof lastKnownKeyGroup>, binding?: TeacherBoundRoute) {
  const status: SchoolQuote['status'] = record?.price_status === 'confirmed' || record?.price_status === 'conditional' ||
    record?.price_status === 'derived' || record?.price_status === 'not_listed' ? record.price_status : 'pending'
  const derived = record?.price_source === 'derived_from_usage' || status === 'derived'
  const mode: 'follow' | 'manual' = record?.mode === 'manual' ? 'manual' : 'follow'
  const billingError = billing?.last_error
  const priceError = priceDataset?.last_error
  const groupIssue = keyGroupIssue(billing)
  const missingOfficial = priceDataset?.official_price_missing
  const stale = billing?.billing_state === 'stale'
  const billingRate = billing?.effective_multiplier_text ?? billing?.effective_multiplier
  const hasGroupEvidence = Boolean(websiteKey || previousGroup)
  const priceObservedAt = record?.price_observed_at ?? priceDataset?.last_success
  const matchingQuote = websiteKey && priceBelongsToWebsiteKeyGroup(websiteKey,
    priceObservedAt, binding?.group_id ?? billing?.group_id ?? previousGroup?.id)
  const observedRate = hasGroupEvidence
    ? matchingQuote ? websiteKeyEffectiveRate(websiteKey, billing) : null
    : billing?.billing_state === 'current' ? billingRate : reported
  const rateUnverified = Boolean(websiteKey && !verifiedWebsiteKeyBilling(websiteKey, billing))
  const rate = !hasGroupEvidence && (groupIssue || billingError || stale) ? null : nonNegativeDecimal(observedRate)
  const lastRate = !hasGroupEvidence && stale ? nonNegativeDecimal(billingRate) : null
  const multiplier = lastRate ? formatDecimal(lastRate) + '× · 已陈旧'
    : rate ? formatDecimal(rate) + (candidate ? '× · 待启用' : '×')
      : rateUnverified ? '待核实'
        : groupIssue || billingError || observedRate == null || observedRate === '' ? '待同步' : '倍率无效'
  const source = record?.price_conditions_exact ?? record?.price_conditions
  const terms = (websiteKey?.group_id == null || matchingQuote) && status !== 'not_listed' &&
    source && typeof source === 'object' && !Array.isArray(source) ? source : null
  const canCalculate = !priceError && (status === 'confirmed' || status === 'conditional' || status === 'derived') ? rate : null
  // Coverage and exact terms come from the current bound-route snapshot.
  // The Key inventory and billing evidence must still agree with that route.
  const covered = binding?.binding_ready && binding.price_covered
  const conditionalPrice = covered && binding.price_state === 'conditional' && binding.group_id != null &&
    mode === 'follow' && !record?.school_rates && record?.price_covered !== false && !derived &&
    matchingQuote && canCalculate && terms && terms === record?.price_conditions_exact
    ? schoolConditionalPrice(terms, formatDecimal(canCalculate), priceObservedAt) : undefined
  const displayRate = binding && mode === 'follow' && (!covered || derived ||
    binding.price_state === 'conditional' && !conditionalPrice && !record?.school_rates) ? null : canCalculate
  const prices = terms ? formatPriceConditions(terms, displayRate, true) : null
  const standard = prices?.standard ?? null
  // Older confirmed quotes can have effective token rates without a website pricing object.
  const legacy = !terms && displayRate && status === 'confirmed' ? legacyEffectivePrice(record?.upstream_rates) : null
  const actual = prices?.actual ?? legacy?.actual ?? null
  const schoolRates = mode === 'follow' && (stale || hasGroupEvidence &&
    (!matchingQuote || !verifiedWebsiteKeyBilling(websiteKey, billing))) ? null : record?.school_rates
  const actualRates = schoolRates ? {
    input: scaledNumber(schoolRates.input, 6), output: scaledNumber(schoolRates.output, 6),
    cache_read: scaledNumber(schoolRates.cache_read, 6), cache_write: scaledNumber(schoolRates.cache_write, 6),
  } : null
  const priceReady = record?.price_covered ?? (mode === 'manual'
    ? Boolean(actualRates?.input != null && actualRates?.output != null)
    : !priceError && Boolean(record?.upstream_rates?.input != null && record?.upstream_rates?.output != null))
  const issue = groupIssue || (missingOfficial && status === 'pending' ? '官方价表缺失' : '')
  const statusLabel = issue || (derived && status === 'pending' ? '反推价待重新核实' : priceError
    ? (status === 'pending' ? '价格同步失败' : '上次报价状态 · ' + priceStatusLabels[status])
    : (status === 'pending' && standard ? '官网单价待核对' : priceStatusLabels[status]))
  const officialSource: PriceSource = derived ? 'derived' : 'website'
  const schoolSource: PriceSource = mode === 'manual' ? 'manual' : officialSource
  const websiteObservedAt = priceDataset?.last_success ?? null
  const officialObservedAt = derived ? record?.price_observed_at ?? websiteObservedAt : websiteObservedAt
  const schoolObservedAt = mode === 'manual' ? record?.price_observed_at ?? null : officialObservedAt
  const notes: string[] = []
  const group = websiteKey?.group_id == null ? previousGroup
    : { id: websiteKey.group_id, name: websiteKey.group_name, rate: websiteKey.rate }
  if (group) {
    const groupRate = keyGroupRate(group.rate ?? undefined)
    const groupLabel = websiteKey?.group_id == null ? lastKnownKeyGroupLabel : 'Key 分组'
    notes.push([`${groupLabel} ${group.id}`, group.name, groupRate && '分组倍率 ' + groupRate].filter(Boolean).join(' · '))
  }
  if (derived) {
    notes.push('输入、输出单价由历史流水反推。缓存单价缺证据时，使用缓存的调用待核账。')
    if (record?.price_observed_at) {
      const at = new Date(record.price_observed_at)
      if (Number.isFinite(at.getTime())) notes.push('反推时间 ' + at.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) + '（北京时间）')
    }
  }
  if (priceError && !groupIssue && !missingOfficial) notes.push('价格同步错误：' + priceError)
  if (billingError && !groupIssue) notes.push('倍率同步失败：' + billingError)
  if (status === 'confirmed' && !standard && !priceError) notes.push('官网标准价待同步')
  if (status === 'conditional' && !standard && !priceError) notes.push('条件明细待同步')
  if (prices?.precisionUnknown) notes.push('官网原始小数精度未知')
  if (legacy?.zeroUncertain) notes.push('后台已有报价小数精度未知，零值待核对')
  if (legacy?.actual) notes.push('实际单价来自后台已有报价')
  return { multiplier, multiplierLabel: mode === 'manual' ? '手动定价' : '倍率 ' + multiplier,
    standard, actual, actualRates, status, statusLabel, issue: issue || undefined, notes, mode, priceReady,
    conditions: terms, rateValue: rate ? formatDecimal(rate) : null,
    officialSource, schoolSource, officialObservedAt, schoolObservedAt, conditionalPrice,
    credentialId: binding?.credential_id ?? undefined, upstreamModel: binding?.upstream_model.toLowerCase(),
    routeLabel: binding?.credential_label }
}

const tokenPrices = [
  ['input_price', '输入'], ['output_price', '输出'], ['cache_read_price', '缓存读取'], ['cache_write_price', '缓存写入'],
  ['cache_write_1h_price', '缓存写入（1 小时）'], ['cache_write_12h_price', '缓存写入（12 小时）'],
] as const
const imagePrices = [['image_input_price', '图像输入'], ['image_output_price', '图像输出']] as const
const videoPrices = [['video_input_price', '视频输入'], ['video_output_price', '视频输出']] as const
const effortLabels = new Map([
  ['minimal', '极低'], ['low', '低'], ['medium', '中'], ['high', '高'], ['xhigh', '极高'], ['max', '最高'],
])
const unitLabels = {
  token: '每百万 Token 的积分',
  request: '积分/次',
  second: '积分/秒',
  unknown: '官网原值（单位未知）',
}

type PriceUnit = keyof typeof unitLabels
type PriceField = readonly [(typeof tokenPrices | typeof imagePrices | typeof videoPrices)[number][0] | 'per_request_price', string]
type PriceGroup = readonly [PriceUnit, readonly PriceField[]]

function priceGroups(mode: BeichenPriceTier['billing_mode']): PriceGroup[] {
  switch (mode) {
    case 'token': return [['token', [...tokenPrices, ...imagePrices]]]
    case 'per_request': return [['request', [['per_request_price', '每次请求']]]]
    case 'video': return [['second', [['per_request_price', '每秒']]], ['unknown', videoPrices]]
    case 'image': return [
      ['request', [imagePrices[1], ['per_request_price', '按次基础价']]],
      ['unknown', [imagePrices[0]]],
    ]
    default: return [['unknown', [...tokenPrices, ...imagePrices, ...videoPrices, ['per_request_price', '基础价格']]]]
  }
}

function priceLines(tier: BeichenPriceTier, mode: BeichenPriceTier['billing_mode'], rate: Decimal | null = null, credits = false) {
  if (!mode && tier.per_request_price == null && tier.image_input_price == null && tier.image_output_price == null &&
    tier.video_input_price == null && tier.video_output_price == null) mode = 'token'
  const lines: string[] = []
  for (const [unit, fields] of priceGroups(mode)) {
    if (rate && unit === 'unknown') continue
    const factor = credits && unit !== 'unknown' ? 100n : 1n
    const values: string[] = []
    for (const [key, label] of fields) {
      const raw = tier[key]
      // 旧接口的极小报价可能在 JSON 解析时变成数字 0。
      if (raw === 0) {
        if (!rate) values.push(label + ' 待核对')
        continue
      }
      const value = nonNegativeDecimal(raw)
      if (!value) continue
      const coefficient = value.coefficient * (rate?.coefficient ?? 1n) * factor
      const scale = value.scale + (rate?.scale ?? 0) - (unit === 'token' ? 6 : 0)
      values.push(label + ' ' + formatDecimal({ coefficient, scale }))
    }
    if (values.length) lines.push(unitLabels[unit] + ' · ' + values.join(' · '))
  }
  return lines
}

function hasNumericPrice(tier: BeichenPriceTier) {
  return Object.entries(tier).some(([key, value]) => key.endsWith('_price') && typeof value === 'number')
}

function tokenBoundary(raw: number | string | null | undefined, upper = false) {
  if (raw == null) return { label: upper && raw === null ? '无上限' : '未知', tokens: null, invalid: false }
  const parsed = decimal(raw)
  if (!parsed) return { label: '无效', tokens: null, invalid: true }
  const label = formatDecimal(parsed)
  const invalid = parsed.coefficient < 0n || label.includes('.')
  return { label, tokens: invalid ? null : BigInt(label), invalid }
}

function schoolTokenPriceBand(terms: BeichenPriceTier, rate: string, label: string): SchoolTokenPriceBand | null {
  const base = priceDetailRows(terms)
  if (base[0].yuan == null || base[1].yuan == null) return null
  const settlement = priceDetailRows(terms, rate)
  return { label, rows: base.map(({ key, label, yuan }, index) => {
    const credits = settlement[index].credits
    return { key, label, yuan, settlement: credits, student: credits == null ? null : multiplyDecimal(credits, '1.10') }
  }) }
}

function schoolConditionalPrice(terms: BeichenPriceConditions, rate: string, observedAt?: string | null): SchoolConditionalPrice | undefined {
  const intervals = (Array.isArray(terms.intervals) ? terms.intervals : []).flatMap(tier => {
    if (!tier || typeof tier !== 'object' || Array.isArray(tier)) return []
    const min = tokenBoundary(tier.min_tokens)
    const max = tokenBoundary(tier.max_tokens ?? null, true)
    if (min.tokens == null || max.invalid || max.tokens != null && max.tokens <= min.tokens) return []
    const from = min.tokens.toLocaleString('zh-CN')
    const to = max.tokens?.toLocaleString('zh-CN')
    const label = min.tokens === 0n && to ? `基础档 · 输入 Token < ${to}`
      : to ? `${from} ≤ 输入 Token < ${to}` : `输入 Token ≥ ${from}`
    const band = schoolTokenPriceBand({ ...tier, billing_mode: tier.billing_mode ?? terms.billing_mode }, rate, label)
    return band ? [{ ...band, minTokens: min.label, maxTokens: max.tokens == null ? null : max.label }] : []
  })
  const base = intervals.some(tier => tier.minTokens === '0') ? null : schoolTokenPriceBand(terms, rate, '基础档')
  return base || intervals.length ? { base, intervals, observedAt: observedAt ?? null } : undefined
}

export function schoolTokenPriceAt(price: SchoolConditionalPrice, inputTokens: bigint): SchoolTokenPriceBand | null {
  return price.intervals.find(tier => inputTokens >= BigInt(tier.minTokens) &&
    (tier.maxTokens == null || inputTokens < BigInt(tier.maxTokens))) ?? price.base
}

export function formatPriceConditions(terms: BeichenPriceConditions, rate: Decimal | null, credits = false) {
  const standard = priceLines(terms, terms.billing_mode, null, credits)
  const actual = rate ? priceLines(terms, terms.billing_mode, rate, credits) : []
  let precisionUnknown = hasNumericPrice(terms)
  for (const tier of Array.isArray(terms.intervals) ? terms.intervals : []) {
    if (!tier || typeof tier !== 'object' || Array.isArray(tier)) continue
    precisionUnknown ||= hasNumericPrice(tier)
    const from = tokenBoundary(tier.min_tokens)
    const to = tokenBoundary(tier.max_tokens, true)
    const invalid = from.invalid || to.invalid || (from.tokens != null && to.tokens != null && from.tokens > to.tokens)
    const boundsUnknown = tier.min_tokens == null || tier.max_tokens === undefined
    const label = '上下文分档（Token 下限 ' + from.label + '，上限 ' + to.label + '）'
    const warning = invalid ? ' · 档位无效' : boundsUnknown ? ' · 适用范围待核对' : ''
    const mode = tier.billing_mode ?? terms.billing_mode
    const tierStandard = priceLines(tier, mode, null, credits)
    standard.push(label + warning, ...(tierStandard.length ? tierStandard : ['分档单价未知']))
    if (rate && !invalid && !boundsUnknown) {
      const tierActual = priceLines(tier, mode, rate, credits)
      if (tierActual.length) actual.push(label, ...tierActual)
    }
  }

  for (const [effort, value] of Object.entries(terms.reasoning_effort_multipliers ?? {})) {
    const parsed = nonNegativeDecimal(value)
    if (parsed) {
      standard.push('推理强度（' + (effortLabels.get(effort) ?? effort) + '）倍率 ' + formatDecimal(parsed) + '×')
    }
  }
  return { standard: standard.join('\n') || null, actual: actual.join('\n') || null, precisionUnknown }
}

function legacyEffectivePrice(rates: BeichenModel['upstream_rates']) {
  const formatted = priceFields.map(({ key, label }) => {
    const value = rates?.[key]
    const zeroUncertain = value === 0
    return { label, amount: !zeroUncertain && nonNegativeDecimal(value) ? scaledNumber(value, 4) : null, zeroUncertain }
  })
  const actual = formatted.some(({ amount }) => amount !== null)
    ? unitLabels.token + ' · ' + formatted.map(({ label, amount }) => label + ' ' + (amount ?? '未知')).join(' · ')
    : null
  return { actual, zeroUncertain: formatted.some(field => field.zeroUncertain) }
}

type Decimal = { coefficient: bigint; scale: number }

function decimal(value: unknown): Decimal | null {
  if (typeof value === 'number' && (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value)))) return null
  const raw = typeof value === 'number' || typeof value === 'string' ? String(value).trim() : ''
  if (!raw || raw.length > 512) return null
  const match = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:e([+-]?\d+))?$/i.exec(raw)
  if (!match) return null
  const exponent = Number(match[5] ?? '0')
  if (Math.abs(exponent) > 1000) return null
  const fraction = match[3] ?? match[4] ?? ''
  const coefficient = BigInt((match[2] ?? '') + fraction)
  return { coefficient: match[1] === '-' ? -coefficient : coefficient, scale: fraction.length - exponent }
}

function nonNegativeDecimal(value: unknown) {
  const amount = decimal(value)
  return amount && amount.coefficient >= 0n ? amount : null
}

function formatDecimal({ coefficient, scale }: Decimal) {
  if (coefficient === 0n) return '0'
  const sign = coefficient < 0n ? '-' : ''
  const digits = (coefficient < 0n ? -coefficient : coefficient).toString()
  if (scale <= 0) return sign + digits + '0'.repeat(-scale)
  const padded = digits.padStart(scale + 1, '0')
  const fraction = padded.slice(-scale).replace(/0+$/, '')
  return sign + padded.slice(0, -scale) + (fraction ? '.' + fraction : '')
}

export const priceFields = [
  { key: 'input', label: '输入' },
  { key: 'output', label: '输出' },
  { key: 'cache_read', label: '缓存读取' },
  { key: 'cache_write', label: '缓存写入' },
] as const

// Keep fractional millionths intact when the API supplies a decimal string.
export function scaledNumber(value: number | string | null | undefined, digits: number, group = false) {
  if (value == null || (typeof value === 'number' && !Number.isSafeInteger(value))) return null
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(String(value))
  if (!match) return null
  const absolute = match[2].padStart(digits + 1, '0')
  const whole = BigInt(absolute.slice(0, -digits))
  const fraction = (absolute.slice(-digits) + (match[3] ?? '')).replace(/0+$/, '')
  return `${match[1]}${group ? whole.toLocaleString('zh-CN') : whole}${fraction ? '.' + fraction : ''}`
}
