import type { BeichenCatalogModel, BeichenModel, BeichenOverview, BeichenPriceConditions, BeichenPriceTier } from '../api/types'

export function isBeichenProvider(baseUrl: string) {
  const address = baseUrl.trim()
  const origin = 'https://api.beichenapi.com'
  return address.slice(0, origin.length).toLowerCase() === origin
    && ['', '/', '/v1', '/v1/'].includes(address.slice(origin.length))
}

export function schoolCatalog(overview: BeichenOverview, providerId: string) {
  const records = new Map<string, BeichenModel[]>()
  const prices = new Map<string, BeichenModel>()
  for (const row of overview.models ?? []) {
    const key = row.model.toLowerCase()
    prices.set(row.credential_id + ':' + key, row)
    const group = records.get(key)
    if (group) group.push(row)
    else records.set(key, [row])
  }
  const failures = new Set(overview.datasets?.filter(row => row.last_error).map(row => row.kind + ':' + row.credential_id))
  const catalog: BeichenCatalogModel[] = overview.model_catalog ??
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
    const quotes = new Map<string, { multiplier: string; price: string | null }>()
    for (const route of selected) {
      const local = route.provider_id === providerId && Boolean(route.credential_id)
      const reported = route.effective_multiplier_text === undefined ? route.effective_multiplier : route.effective_multiplier_text
      const rate = local && !failures.has('billing:' + route.credential_id) ? reported : null
      const multiplier = rate == null || rate === '' ? '待同步' : rate + (route.state === 'candidate' ? '× · 待启用' : '×')
      const upstreamName = (route.upstream_model ?? entry.model).toLowerCase()
      const record = prices.get(route.credential_id + ':' + upstreamName)
      const price = local && !failures.has('prices:' + route.credential_id) ? unitPrice(record) : null
      quotes.set(JSON.stringify([multiplier, price]), { multiplier, price })
    }
    return { model: entry.model, school, status, canEnable: entry.can_enable, canDisable, quotes: Array.from(quotes.values()) }
  })
}

function unitPrice(record?: BeichenModel) {
  if (record?.price_status === 'conditional') return conditionalPrices(record.price_conditions_exact ?? record.price_conditions)
  if (record?.price_status !== 'confirmed') return null
  return '每百万 Token 的北辰余额单位 · ' +
    priceFields.map(({ key, label }) => label + ' ' + (scaledNumber(record.upstream_rates?.[key], 6) ?? '未知')).join(' · ')
}

const tokenPrices = [
  ['input_price', '输入'], ['output_price', '输出'], ['cache_read_price', '缓存读取'], ['cache_write_price', '缓存写入'],
  ['cache_write_1h_price', '缓存写入（1 小时）'], ['cache_write_12h_price', '缓存写入（12 小时）'],
] as const
const effortLabels = { minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' }

function conditionNumber(value: unknown) {
  if (typeof value !== 'string' && typeof value !== 'number') return '未知'
  return /^\d+(?:\.\d+)?(?:e[+-]?\d+)?$/i.test(String(value)) ? String(value) : '未知'
}

function panelPrices(tier: BeichenPriceTier) {
  const lines: string[] = []
  const token = tokenPrices.filter(([key]) => key in tier).map(([key, label]) => label + ' ' + conditionNumber(tier[key]))
  if (token.length) lines.push('每 Token · ' + token.join(' · '))
  if ('per_request_price' in tier) lines.push('每次请求 ' + conditionNumber(tier.per_request_price))
  for (const [key, label] of [['image_input_price', '图像输入'], ['image_output_price', '图像输出']] as const) {
    if (key in tier) lines.push(label + ' ' + conditionNumber(tier[key]) + '（官网原价）')
  }
  return lines
}

function conditionalPrices(terms?: BeichenPriceConditions | null) {
  if (!terms || typeof terms !== 'object') return '按条件计价 · 条件待同步'
  const lines = panelPrices(terms)
  if (Array.isArray(terms.intervals)) {
    for (const tier of terms.intervals) {
      if (!tier || typeof tier !== 'object') continue
      const prices = panelPrices(tier)
      lines.push('阶梯（Token 下限 ' + conditionNumber(tier.min_tokens) + '，上限 ' + conditionNumber(tier.max_tokens) + '） · ' +
        (prices.join(' · ') || '单价待同步'))
    }
  }
  const efforts = terms.reasoning_effort_multipliers
  if (efforts) {
    for (const [key, label] of Object.entries(effortLabels)) {
      if (efforts[key] != null) lines.push('推理强度（' + label + '）倍率 ' + conditionNumber(efforts[key]) + '×')
    }
  }
  return ['官网条件报价（北辰余额单位）', ...(lines.length ? lines : ['条件待同步'])].join('\n')
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
