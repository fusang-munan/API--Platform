import type { KeyUsage, StudentProjectUsage, StudentUsage, UsageSlice } from '../api/types'
import { formatCredits, formatTokens, shiftDecimal } from './credits'
import { calculateCredits } from './format'

export type StudentUsageMetrics = {
  tokens: Record<'input' | 'output' | 'cache_read' | 'cache_write' | 'total', string | null>
  completeness: 'complete' | 'partial' | 'unknown'
  token_coverage: {
    calls: number
    known_calls: number
    complete_calls: number
    unclassified_tokens: string | null
  }
  cache_hit_rate: number | null
  cache_hit_coverage: {
    sample_calls: number
    cache_read_tokens: string | null
    input_with_cache_read_tokens: string | null
  }
  pending_review_calls: number
  pending_review_credits: string
}

export type StudentUsageModelMetrics = StudentUsageMetrics & { credits: string; calls?: number }
export type StudentUsageModel = StudentUsageModelMetrics & Pick<UsageSlice, 'id' | 'name'>
type StudentUsageBalance = StudentUsageMetrics & { calls: number; in_flight_credits: string }

export type StudentUsageKey = Omit<KeyUsage, 'models'> & StudentUsageBalance & {
  pending_calls: number
  models: StudentUsageModel[]
}

export type StudentUsageProject = Omit<StudentProjectUsage, 'keys'> & StudentUsageBalance & {
  keys: StudentUsageKey[]
}

export type StudentUsageReport = Omit<StudentUsage, 'projects'> & {
  projects: StudentUsageProject[]
  models: StudentUsageModel[]
  totals: StudentUsageModelMetrics & { calls: number }
}

export type StudentCallReceipt = {
  request_id: string
  model_id: string
  model_name: string
  created_at: string
  channel: string
  billing_state: string
  charged_credits: string | null
  receipt_applied: boolean | null
  usage_state?: string
  usage_reason: string | null
  input_tokens: string | null
  output_tokens: string | null
  cache_read_tokens: string | null
  cache_write_tokens: string | null
  total_tokens: string | null
}

export function formatUsageCredits(value?: string | null): string {
  return shiftDecimal(value, 0)
}

export function formatUsageTokenCount(value?: string | null): string {
  return typeof value === 'string' && /^\d+$/.test(value) ? BigInt(value).toLocaleString('zh-CN') : '—'
}

export function sumStudentProjectCredits(
  projects: StudentUsageProject[],
  field: 'quota' | 'in_flight_credits' | 'available',
): string | null {
  if (!projects.length) return null
  let total = '0.000'
  for (const project of projects) {
    const amount = project[field]
    if (typeof amount !== 'string' || !/^\d+(?:\.\d+)?$/.test(amount)) return null
    total = calculateCredits(total, amount, 'add')
  }
  return total
}

export function formatUsageCacheCoverage(metrics: StudentUsageMetrics): string {
  const samples = formatTokens(metrics.cache_hit_coverage.sample_calls)
  const total = formatTokens(metrics.token_coverage.calls)
  return `覆盖 ${samples}/${total} 笔`
}

export function formatUsagePendingReview(metrics: StudentUsageMetrics): string {
  if (metrics.pending_review_calls <= 0) return ''
  const calls = formatTokens(metrics.pending_review_calls)
  const credits = formatUsageCredits(metrics.pending_review_credits)
  return `待核 ${calls} 笔（预留 ${credits} 积分）`
}

export function formatUsageModelNotes(model: StudentUsageModelMetrics): string {
  const notes: string[] = []
  const coverage = model.token_coverage
  if (model.completeness === 'unknown') notes.push('无回报')
  if (model.completeness === 'partial') {
    notes.push(`部分回报（${formatTokens(coverage.complete_calls)}/${formatTokens(coverage.calls)} 笔完整）`)
  }
  if (coverage.unclassified_tokens && coverage.unclassified_tokens !== '0') {
    notes.push(`未分类 ${formatUsageTokenCount(coverage.unclassified_tokens)} Token`)
  }
  if ((model.calls ?? 0) > 0 && formatUsageCredits(model.credits) === '0') notes.push('零额已结算')
  const review = formatUsagePendingReview(model)
  if (review) notes.push(review)
  return notes.join(' · ') || '—'
}

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
 * 为模型广场的用量标签按模型 ID 合并项目 → Key → 模型明细。
 * credits 只含已成功结算的调用；calls 含已结算与 0 倍率调用，0 次时接口会省略该字段。
 */
export function summarizeStudentModels(
  projects: StudentProjectUsage[] | null | undefined,
): StudentModelUsageRow[] {
  const totals = new Map<string, ModelTotal>()
  for (const project of projects ?? []) {
    for (const key of project.keys ?? []) {
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

/** 汇总模型广场展示的项目在途积分。 */
export function sumPendingCredits(projects: StudentProjectUsage[] | null | undefined): string {
  let total = 0
  for (const project of projects ?? []) {
    total += parseCreditsMilli(project.pending) ?? 0
  }
  return formatCreditsMilli(total)
}

export function formatStudentModelUsage(row?: StudentModelUsageRow | null): string {
  if (!row || row.calls <= 0) return '本月 0.000 积分 · 0 次'
  const calls = `${formatTokens(row.calls)} 次`
  if (row.zeroCost) return `本月 0 积分 · ${calls}（未结算/待核或免费）`
  return `本月已用 ${formatCredits(row.credits)} 积分 · ${calls}`
}
