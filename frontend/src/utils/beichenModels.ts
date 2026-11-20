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
      const multiplier = multiplierLabel(rate, route.state === 'candidate')
      const upstreamName = (route.upstream_model ?? entry.model).toLowerCase()
      const record = prices.get(route.credential_id + ':' + upstreamName)
      const price = local ? unitPrice(record, rate, failures.has('prices:' + route.credential_id)) : null
      quotes.set(JSON.stringify([multiplier, price]), { multiplier, price })
    }
    return { model: entry.model, school, status, canEnable: entry.can_enable, canDisable, quotes: Array.from(quotes.values()) }
  })
}

function unitPrice(record: BeichenModel | undefined, multiplier: unknown, priceError: boolean) {
  if (!record) return null
  const status = String(record.price_status ?? '')
  if (status === 'not_listed') return priceError ? null : '官网未提供同名报价'

  const parsedTerms = priceTerms(record)
  if (status === 'conditional') return conditionalPrices(parsedTerms.terms, multiplier, parsedTerms.precisionUnknown)
  if (status !== 'confirmed' && status !== 'pending_confirmation') return null

  const standard = standardPrice(parsedTerms.terms, status === 'pending_confirmation', parsedTerms.precisionUnknown)
  const current = status === 'confirmed' && !priceError ? currentPrice(record) : null
  return [standard, current].filter(Boolean).join('\n') || null
}

type DecimalValue = { coefficient: bigint; scale: number }
type BillingMode = 'token' | 'per_request' | 'image' | 'video' | 'unknown'
type PriceUnit = 'per_million' | 'per_request' | 'per_second' | 'unknown'
type PricePart = { label: string; value: unknown; unit: PriceUnit }
type PriceTerms = { terms: BeichenPriceConditions; precisionUnknown: boolean }

const priceUnitGroups: Array<{ unit: PriceUnit; raw: string; calculated: string }> = [
  { unit: 'per_million', raw: '每百万 Token 的北辰余额单位', calculated: '每百万 Token 的北辰余额单位' },
  { unit: 'per_request', raw: '北辰余额单位/次', calculated: '北辰余额单位/次' },
  { unit: 'per_second', raw: '北辰余额单位/秒', calculated: '北辰余额单位/秒' },
  { unit: 'unknown', raw: '官网原值（单位未知）', calculated: '官网原值（单位未知）' },
]

const tokenPrices = [
  ['input_price', '输入'], ['output_price', '输出'], ['cache_read_price', '缓存读取'], ['cache_write_price', '缓存写入'],
  ['cache_write_1h_price', '缓存写入（1 小时）'], ['cache_write_12h_price', '缓存写入（12 小时）'],
] as const
const standardTokenPrices = tokenPrices.slice(0, 4)
const durationTokenPrices = tokenPrices.slice(4)
const effortLabels: Record<string, string> = { minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' }

function multiplierLabel(value: unknown, candidate: boolean) {
  if (value == null || value === '') return '待同步'
  const parsed = nonNegativeDecimal(value)
  if (!parsed) return '倍率无效'
  return formatDecimal(parsed) + (candidate ? '× · 待启用' : '×')
}

function priceTerms(record: BeichenModel): PriceTerms {
  const exact = record.price_conditions_exact && typeof record.price_conditions_exact === 'object' && !Array.isArray(record.price_conditions_exact)
    ? record.price_conditions_exact : null
  if (exact) return { terms: exact, precisionUnknown: containsNumber(exact) }
  const raw = record.price_conditions && typeof record.price_conditions === 'object' && !Array.isArray(record.price_conditions)
    ? record.price_conditions : {}
  return { terms: raw, precisionUnknown: containsNumber(raw) }
}

function containsNumber(value: unknown): boolean {
  if (typeof value === 'number') return true
  if (Array.isArray(value)) return value.some(containsNumber)
  if (value && typeof value === 'object') return Object.values(value).some(containsNumber)
  return false
}

function standardPrice(terms: BeichenPriceConditions, pending: boolean, precisionUnknown: boolean) {
  const mode = billingMode(terms.billing_mode)
  let parts = priceParts(terms, mode)
  if (!parts.some(part => nonNegativeDecimal(part.value))) return null
  if (mode === 'token') {
    const core = standardTokenPrices.map(([key, label]) =>
      parts.find(part => part.label === label) ?? { label, value: terms[key], unit: 'per_million' as const })
    const coreLabels = new Set<string>(standardTokenPrices.map(([, label]) => label))
    parts = [...core, ...parts.filter(part => !coreLabels.has(part.label))]
  }
  const lines = priceLines(parts)
  if (!lines.length) return null
  return `官网标准价${pending ? '（待核对）' : ''} · ${billingModeLabel(mode)}` +
    `${precisionUnknown ? ' · 原始精度未知' : ''}\n` + lines.join('\n')
}

function currentPrice(record: BeichenModel) {
  if (!record.upstream_rates || typeof record.upstream_rates !== 'object') return null
  const fields = priceFields.map(({ key, label }) => [key, label, record.upstream_rates?.[key]] as const)
  if (!fields.some(([, , value]) => nonNegativeDecimal(value))) return null
  return '渠道单价（已乘倍率） · 每百万 Token 的北辰余额单位 · ' +
    fields.map(([, label, value]) => label + ' ' + perMillionFromMicros(value)).join(' · ')
}

function conditionNumber(value: unknown) {
  const parsed = nonNegativeDecimal(value)
  return parsed ? formatDecimal(parsed) : '未知'
}

function perMillionFromToken(value: unknown) {
  const parsed = nonNegativeDecimal(value)
  return parsed ? formatDecimal({ coefficient: parsed.coefficient * 1_000_000n, scale: parsed.scale }) : '未知'
}

function perMillionFromMicros(value: unknown) {
  const parsed = nonNegativeDecimal(value)
  return parsed ? formatDecimal({ coefficient: parsed.coefficient, scale: parsed.scale + 6 }) : '未知'
}

function multipliedPrice(value: unknown, multiplier: DecimalValue, perMillion: boolean) {
  const parsed = nonNegativeDecimal(value)
  if (!parsed) return '未知'
  const factor = perMillion ? 1_000_000n : 1n
  return formatDecimal({
    coefficient: parsed.coefficient * multiplier.coefficient * factor,
    scale: parsed.scale + multiplier.scale,
  })
}

function billingMode(value: unknown): BillingMode {
  const mode = typeof value === 'string' ? value.trim().toLowerCase() : ''
  return ['token', 'per_request', 'image', 'video'].includes(mode) ? mode as BillingMode : 'unknown'
}

function billingModeLabel(mode: BillingMode) {
  return { token: '按 Token', per_request: '按次', image: '按图像', video: '按秒', unknown: '计费模式未知' }[mode]
}

function priceParts(tier: BeichenPriceTier, mode: BillingMode): PricePart[] {
  const parts: PricePart[] = []
  const addTokens = (unit: PriceUnit) => {
    for (const [key, label] of tokenPrices) {
      if (key in tier) parts.push({ label, value: tier[key], unit })
    }
  }
  const addImages = (unit: PriceUnit) => {
    for (const [key, label] of [['image_input_price', '图像输入'], ['image_output_price', '图像输出']] as const) {
      if (key in tier) parts.push({ label, value: tier[key], unit })
    }
  }
  if (mode === 'token') {
    addTokens('per_million')
    addImages('per_million')
  } else if (mode === 'per_request') {
    if ('per_request_price' in tier) parts.push({ label: '每次请求', value: tier.per_request_price, unit: 'per_request' })
  } else if (mode === 'image') {
    if ('image_output_price' in tier) parts.push({ label: '图像输出', value: tier.image_output_price, unit: 'per_request' })
    if ('image_input_price' in tier) parts.push({ label: '图像输入', value: tier.image_input_price, unit: 'unknown' })
    if ('per_request_price' in tier) parts.push({ label: '基础价格', value: tier.per_request_price, unit: 'unknown' })
  } else if (mode === 'video') {
    if ('per_request_price' in tier) parts.push({ label: '每秒', value: tier.per_request_price, unit: 'per_second' })
  } else {
    addTokens('unknown')
    if ('per_request_price' in tier) parts.push({ label: '基础价格', value: tier.per_request_price, unit: 'unknown' })
    addImages('unknown')
  }
  return parts
}

function priceLines(parts: PricePart[], multiplier?: DecimalValue) {
  const lines: string[] = []
  for (const group of priceUnitGroups) {
    if (multiplier && group.unit === 'unknown') continue
    const fields = parts.filter(part => part.unit === group.unit)
    if (!fields.length) continue
    const values = fields.map(part => part.label + ' ' + priceLineValue(part, multiplier))
    lines.push((multiplier ? group.calculated : group.raw) + ' · ' + values.join(' · '))
  }
  return lines
}

function priceLineValue(part: PricePart, multiplier?: DecimalValue) {
  if (multiplier) return multipliedPrice(part.value, multiplier, part.unit === 'per_million')
  if (part.unit === 'per_million') return perMillionFromToken(part.value)
  return conditionNumber(part.value)
}

type TokenBoundary = { text: string; value: DecimalValue | null; invalid: boolean }

function tokenBoundary(value: unknown, supplied: boolean, upper: boolean): TokenBoundary {
  if (!supplied) return { text: '未知', value: null, invalid: false }
  if (value == null) return { text: upper ? '无上限' : '未知', value: null, invalid: false }
  const parsed = decimalValue(value)
  if (!parsed) return { text: '精度未知', value: null, invalid: true }
  const normalized = normalizeDecimal(parsed)
  const text = formatDecimal(parsed)
  const invalid = parsed.coefficient < 0n || normalized.scale !== 0
  return { text, value: nonNegativeDecimal(text), invalid }
}

function compareDecimals(left: DecimalValue, right: DecimalValue) {
  const scale = Math.max(left.scale, right.scale)
  const leftValue = left.coefficient * 10n ** BigInt(scale - left.scale)
  const rightValue = right.coefficient * 10n ** BigInt(scale - right.scale)
  return leftValue < rightValue ? -1 : leftValue > rightValue ? 1 : 0
}

function conditionalPrices(terms: BeichenPriceConditions | null | undefined, multiplier: unknown, precisionUnknown: boolean) {
  if (!terms || typeof terms !== 'object') return '按条件计价 · 条件待同步'
  const mode = billingMode(terms.billing_mode)
  const sections: Array<{ label: string; parts: PricePart[]; derivable: boolean }> = [
    { label: '', parts: priceParts(terms, mode), derivable: true },
  ]
  if (Array.isArray(terms.intervals)) {
    for (const tier of terms.intervals) {
      if (!tier || typeof tier !== 'object') continue
      const lower = tokenBoundary(tier.min_tokens, 'min_tokens' in tier, false)
      const upper = tokenBoundary(tier.max_tokens, 'max_tokens' in tier, true)
      const reversed = Boolean(lower.value && upper.value && compareDecimals(lower.value, upper.value) > 0)
      const invalid = lower.invalid || upper.invalid || reversed
      const tierMode = billingMode((tier as Record<string, unknown>).billing_mode ?? terms.billing_mode)
      sections.push({
        label: `上下文分档（Token 下限 ${lower.text}，Token 上限 ${upper.text}）${invalid ? ' · 档位无效' : ''}`,
        parts: priceParts(tier, tierMode),
        derivable: !invalid,
      })
    }
  }

  const lines: string[] = [`官网条件报价 · ${billingModeLabel(mode)}${precisionUnknown ? ' · 原始精度未知' : ''}`]
  for (const section of sections) {
    const prices = priceLines(section.parts)
    if (section.label) lines.push(section.label)
    if (prices.length) lines.push(...prices)
    else if (section.label) lines.push('单价未知')
  }

  const efforts = terms.reasoning_effort_multipliers
  if (efforts && typeof efforts === 'object') {
    for (const [key, value] of Object.entries(efforts)) {
      lines.push('无量纲条件（不计入单价） · 推理强度（' + (effortLabels[key] ?? key) + '） ' + conditionNumber(value) + '×')
    }
  }

  const priced = sections.filter(section => section.parts.length)
  const derivable = sections.filter(section => section.derivable && section.parts.some(part => part.unit !== 'unknown'))
  if (!priced.length && !efforts) lines.push('条件待同步')
  if (!derivable.length) return lines.join('\n')

  const rate = nonNegativeDecimal(multiplier)
  if (!rate) {
    lines.push('当前倍率未知或无效，未生成倍率后单价。')
    return lines.join('\n')
  }

  for (const section of derivable) {
    const prices = priceLines(section.parts, rate)
    if (!prices.length) continue
    lines.push((section.label ? section.label + ' · ' : '') +
      '按当前倍率计算（×' + formatDecimal(rate) + '）')
    lines.push(...prices)
  }
  return lines.join('\n')
}

function decimalValue(value: unknown): DecimalValue | null {
  if (typeof value === 'number' && Number.isInteger(value) && !Number.isSafeInteger(value)) return null
  const text = typeof value === 'string' ? value.trim()
    : typeof value === 'number' && Number.isFinite(value) ? String(value)
    : ''
  if (!text || text.length > 512) return null
  const match = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d+))?$/.exec(text)
  if (!match) return null
  const exponent = match[5] == null ? 0 : Number(match[5])
  if (!Number.isSafeInteger(exponent) || Math.abs(exponent) > 1000) return null
  const fraction = match[3] ?? match[4] ?? ''
  let coefficient = BigInt((match[2] ?? '') + fraction || '0')
  if (match[1] === '-') coefficient = -coefficient
  let scale = fraction.length - exponent
  if (scale < 0) {
    coefficient *= 10n ** BigInt(-scale)
    scale = 0
  }
  return { coefficient, scale }
}

function nonNegativeDecimal(value: unknown) {
  const parsed = decimalValue(value)
  return parsed && parsed.coefficient >= 0n ? parsed : null
}

function normalizeDecimal(value: DecimalValue) {
  let coefficient = value.coefficient
  let scale = value.scale
  while (scale > 0 && coefficient % 10n === 0n) {
    coefficient /= 10n
    scale--
  }
  return { coefficient, scale }
}

function formatDecimal(value: DecimalValue) {
  if (value.coefficient === 0n) return '0'
  const negative = value.coefficient < 0n
  const absolute = (negative ? -value.coefficient : value.coefficient).toString()
  let digits = absolute
  if (value.scale <= 0) digits += '0'.repeat(-value.scale)
  else if (digits.length <= value.scale) digits = '0'.repeat(value.scale - digits.length + 1) + digits
  const split = digits.length - value.scale
  const whole = split > 0 ? digits.slice(0, split) : '0'
  const fraction = (split > 0 ? digits.slice(split) : digits).replace(/0+$/, '')
  return `${negative ? '-' : ''}${whole}${fraction ? '.' + fraction : ''}`
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
