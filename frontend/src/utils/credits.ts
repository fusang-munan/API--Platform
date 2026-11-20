// Shared display helpers for credits, tokens and yuan. Credits are stored as
// decimal strings, so keep the integer/fraction split instead of rounding
// through floating point when converting 100 credits to one yuan.

type DecimalParts = { sign: string; whole: string; fraction: string }

function parts(value: string | number | null | undefined): DecimalParts | null {
  if (value == null) return null
  const raw = String(value).trim()
  if (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/.test(raw)) return null
  const sign = raw.startsWith('-') ? '-' : ''
  const body = raw.replace(/^[+-]/, '')
  const [whole = '0', fraction = ''] = body.split('.')
  return { sign, whole: whole || '0', fraction }
}

export function trimDecimal(value: string | number | null | undefined, maxFraction = 3): string {
  const parsed = parts(value)
  if (!parsed) return '—'
  const fraction = parsed.fraction.slice(0, Math.max(0, maxFraction)).replace(/0+$/, '')
  const whole = parsed.whole.replace(/^0+(?=\d)/, '')
  const zero = whole === '0' && !fraction
  return (zero ? '' : parsed.sign) + whole + (fraction ? '.' + fraction : '')
}

function group(whole: string): string {
  return whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

export function formatCredits(value: string | number | null | undefined, maxFraction = 3): string {
  const parsed = parts(value)
  if (!parsed) return '—'
  if (parsed.whole.length > 15) return trimDecimal(value, maxFraction)
  const number = Number(String(value))
  if (!Number.isFinite(number)) return trimDecimal(value, maxFraction)
  return number.toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: maxFraction })
}

export function formatTokens(value: number | string | null | undefined): string {
  if (value == null || value === '') return '—'
  const number = Number(value)
  if (!Number.isFinite(number)) return '—'
  return number.toLocaleString('zh-CN', { maximumFractionDigits: 0 })
}

export function shiftDecimal(value: string | number | null | undefined, places: number): string {
  const parsed = parts(value)
  if (!parsed) return '—'
  // Keep leading zeros: they carry the decimal point position.
  const digits = parsed.whole + parsed.fraction
  const point = parsed.whole.length + places
  let whole: string
  let fraction: string
  if (point <= 0) {
    whole = '0'
    fraction = '0'.repeat(-point) + digits
  } else if (point >= digits.length) {
    whole = digits + '0'.repeat(point - digits.length)
    fraction = ''
  } else {
    whole = digits.slice(0, point)
    fraction = digits.slice(point)
  }
  whole = whole.replace(/^0+(?=\d)/, '')
  fraction = fraction.replace(/0+$/, '')
  const zero = whole === '0' && !fraction
  return (zero ? '' : parsed.sign) + whole + (fraction ? '.' + fraction : '')
}

// 100 credits = 1 yuan.
export function creditsToYuan(value: string | number | null | undefined, maxFraction = 2): string {
  return formatYuan(shiftDecimal(value, -2), maxFraction)
}

export function formatYuan(value: string | number | null | undefined, maxFraction = 2): string {
  if (value == null || value === '') return '—'
  const number = Number(value)
  if (!Number.isFinite(number)) return String(value)
  const digits = Math.abs(number) > 0 && Math.abs(number) < 0.01 ? Math.max(4, maxFraction) : maxFraction
  return number.toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: digits })
}

export function formatPercent(value: string | number | null | undefined, digits = 1): string {
  if (value == null || value === '') return '—'
  const number = Number(value)
  if (!Number.isFinite(number)) return '—'
  return (number * 100).toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: digits }) + '%'
}

export function multiplyDecimal(value: string | number | null | undefined, factor: string | number): string {
  const parsed = parts(value)
  const multiplier = parts(factor)
  if (!parsed || !multiplier) return '—'
  const left = BigInt(parsed.whole + parsed.fraction)
  const right = BigInt(multiplier.whole + multiplier.fraction)
  const scale = parsed.fraction.length + multiplier.fraction.length
  const product = left * right
  const negative = parsed.sign !== multiplier.sign
  const digits = product.toString().padStart(scale + 1, '0')
  const whole = digits.slice(0, -scale).replace(/^0+(?=\d)/, '')
  const fraction = (scale ? digits.slice(-scale) : '').replace(/0+$/, '')
  const zero = whole === '0' && !fraction
  return (zero ? '' : negative ? '-' : '') + whole + (fraction ? '.' + fraction : '')
}
