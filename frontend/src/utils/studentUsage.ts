import type { StudentProjectUsage, UsageSlice } from '../api/types'
import { formatCredits, formatTokens } from './credits'

type UsageModelSlice = UsageSlice & {
  tokens?: number | null
  input_tokens?: number | null
  output_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  cache_hit_rate?: number | null
}

export type StudentModelUsageRow = {
  id: string
  name: string
  credits: string
  calls: number
  zeroCost: boolean
  tokens: number | null
  cacheHitRate: number | null
}

type ModelTotal = {
  id: string
  name: string
  milli: number
  calls: number
  tokens: number
  tokenSlices: number
  tokenMissing: number
  cacheWeighted: number
  cacheWeight: number
  cacheSlices: number
  cacheMissing: number
}

/** 积分最多保留三位小数；按字符串拆开算，避免浮点误差。 */
export function parseCreditsMilli(value: unknown): number | null {
  const text = String(value ?? '').trim()
  if (!/^\d+(?:\.\d+)?$/.test(text)) return null
  const [whole, fraction = ''] = text.split('.')
  const digits = (fraction + '000').slice(0, 3)
  let milli = Number(whole) * 1000 + Number(digits)
  if (fraction.length > 3 && Number(fraction[3]) >= 5) milli += 1
  return Number.isFinite(milli) ? milli : null
}

export function formatCreditsMilli(milli: number): string {
  const negative = milli < 0
  const value = Math.abs(Math.trunc(milli))
  return `${negative ? '-' : ''}${Math.trunc(value / 1000)}.${String(value % 1000).padStart(3, '0')}`
}

function sliceTokens(model: UsageModelSlice): number | null {
  if (model.tokens != null) {
    const tokens = Number(model.tokens)
    if (Number.isFinite(tokens)) return Math.max(0, tokens)
  }
  if (model.input_tokens != null && model.output_tokens != null) {
    const input = Number(model.input_tokens)
    const output = Number(model.output_tokens)
    if (Number.isFinite(input) && Number.isFinite(output)) return Math.max(0, input + output)
  }
  return null
}

function sliceCacheHitRate(model: UsageModelSlice): number | null {
  if (model.cache_hit_rate == null) return null
  const rate = Number(model.cache_hit_rate)
  return Number.isFinite(rate) ? rate : null
}

/**
 * 把 /api/v1/student/usage 的项目 → Key → 模型三层结构按模型 ID 合并。
 * credits 只含已成功结算的调用；calls 含已结算与 0 倍率调用，0 次时接口会省略该字段。
 */
export function summarizeStudentModels(
  projects: StudentProjectUsage[] | null | undefined,
  keyID?: string,
): StudentModelUsageRow[] {
  const totals = new Map<string, ModelTotal>()
  for (const project of projects ?? []) {
    for (const key of project.keys ?? []) {
      if (keyID !== undefined && key.id !== keyID) continue
      for (const model of key.models ?? []) {
        const id = String(model.id || model.name || '').trim()
        if (!id) continue
        const mapKey = id.toLowerCase()
        const current = totals.get(mapKey) ?? {
          id,
          name: id,
          milli: 0,
          calls: 0,
          tokens: 0,
          tokenSlices: 0,
          tokenMissing: 0,
          cacheWeighted: 0,
          cacheWeight: 0,
          cacheSlices: 0,
          cacheMissing: 0,
        }
        const slice = model as UsageModelSlice
        const credits = parseCreditsMilli(slice.credits)
        if (credits !== null) current.milli += credits
        const calls = Math.max(0, Math.trunc(Number(slice.calls) || 0))
        current.calls += calls

        const tokens = sliceTokens(slice)
        if (tokens === null) {
          current.tokenMissing += 1
        } else {
          current.tokens += tokens
          current.tokenSlices += 1
        }

        const cacheHitRate = sliceCacheHitRate(slice)
        if (cacheHitRate === null) {
          current.cacheMissing += 1
        } else {
          const weight = calls > 0 ? calls : 1
          current.cacheWeighted += cacheHitRate * weight
          current.cacheWeight += weight
          current.cacheSlices += 1
        }

        const name = String(slice.name || '').trim()
        if (name && (!current.name || current.name === current.id)) current.name = name
        totals.set(mapKey, current)
      }
    }
  }

  return [...totals.values()]
    .map(item => ({
      id: item.id,
      name: item.name,
      credits: formatCreditsMilli(item.milli),
      calls: item.calls,
      zeroCost: item.milli === 0 && item.calls > 0,
      tokens: item.tokenMissing === 0 && item.tokenSlices > 0 ? item.tokens : null,
      cacheHitRate: item.cacheMissing === 0 && item.cacheWeight > 0
        ? item.cacheWeighted / item.cacheWeight
        : null,
    }))
    .sort((a, b) => {
      const difference = Number(b.credits) - Number(a.credits)
      return difference || b.calls - a.calls || a.name.localeCompare(b.name)
    })
}

/** 学生接口只有在项目层级返回在途/待核；这部分尚未分摊到具体模型。 */
export function sumPendingCredits(projects: StudentProjectUsage[] | null | undefined): string {
  let total = 0
  for (const project of projects ?? []) {
    total += parseCreditsMilli(project.pending) ?? 0
  }
  return formatCreditsMilli(total)
}

export function formatStudentModelUsageParts(row?: StudentModelUsageRow | null) {
  if (!row || row.calls <= 0) return { credits: '本月 0.000 积分', calls: '0 次' }
  const calls = `${formatTokens(row.calls)} 次`
  if (row.zeroCost) return { credits: '本月 0 积分', calls: `${calls}（未结算/待核或免费）` }
  return { credits: `本月已用 ${formatCredits(row.credits)} 积分`, calls }
}

export function formatStudentModelUsage(row?: StudentModelUsageRow | null): string {
  const { credits, calls } = formatStudentModelUsageParts(row)
  return `${credits} · ${calls}`
}
