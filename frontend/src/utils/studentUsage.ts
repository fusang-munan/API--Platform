import type {
  StudentKeyUsage, StudentModelUsage, StudentModelUsageFields, StudentProjectUsageRow, StudentUsageFields,
} from '../api/student'
import { formatPercent, formatTokens, shiftDecimal } from './credits'
import { calculateCredits } from './format'

function isUsageCredit(value: unknown): value is string {
  return typeof value === 'string' && /^\d+(?:\.\d+)?$/.test(value)
}

export function formatUsageCredits(value: unknown): string {
  return isUsageCredit(value) ? shiftDecimal(value, 0) : '—'
}

export function isUsageCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

export function formatUsageCount(value: unknown): string {
  return isUsageCount(value) ? formatTokens(value) : '—'
}

export function formatUsageTokenCount(value: unknown): string {
  if (typeof value === 'string' && /^\d+$/.test(value)) return BigInt(value).toLocaleString('zh-CN')
  return formatUsageCount(value)
}

export function studentKeyAvailable(key?: StudentKeyUsage): string | null {
  const amount = key?.available_effective
  if (isUsageCredit(amount)) return amount
  return isUsageCredit(key?.available) ? key.available : null
}

export function studentInFlightCredits(row: Pick<StudentProjectUsageRow, 'in_flight_credits' | 'pending'>): string | null {
  if (isUsageCredit(row.in_flight_credits)) return row.in_flight_credits
  return isUsageCredit(row.pending) ? row.pending : null
}

export function hasStudentUsageModels(value: unknown): value is StudentModelUsage[] {
  return Array.isArray(value) && value.every(row => typeof row?.id === 'string')
}

export function sumStudentProjectCredits(
  projects: StudentProjectUsageRow[],
  field: 'quota' | 'in_flight_credits' | 'available',
): string | null {
  if (!projects.length) return null
  let total = '0.000'
  for (const project of projects) {
    const amount = field === 'in_flight_credits' ? studentInFlightCredits(project) : project[field]
    if (!isUsageCredit(amount)) return null
    total = calculateCredits(total, amount, 'add')
  }
  return total
}

export function formatUsageCacheRate(value: unknown): string {
  return typeof value === 'number' && value >= 0 && value <= 1 ? formatPercent(value) : '—'
}

export function formatUsageCacheCoverage(metrics: StudentUsageFields): string {
  const samples = metrics.cache_hit_coverage?.sample_calls
  const total = metrics.token_coverage?.calls
  if (!isUsageCount(samples) || !isUsageCount(total)) return ''
  return `覆盖 ${formatTokens(samples)}/${formatTokens(total)} 笔`
}

export function formatUsagePendingReview(metrics?: StudentUsageFields | null): string {
  const count = metrics?.pending_review_calls
  if (!isUsageCount(count)) return metrics?.pending_review === true ? '有待核账记录' : ''
  if (count === 0) return ''
  const calls = `待核 ${formatTokens(count)} 笔`
  const credits = formatUsageCredits(metrics?.pending_review_credits)
  return credits === '—' ? calls : `${calls}（预留 ${credits} 积分）`
}

export function formatUsageModelNotes(model: StudentModelUsageFields): string {
  const notes: string[] = []
  const coverage = model.token_coverage
  if (model.completeness === 'unknown') notes.push('无回报')
  if (model.completeness === 'partial') {
    notes.push(isUsageCount(coverage?.complete_calls) && isUsageCount(coverage?.calls)
      ? `部分回报（${formatTokens(coverage.complete_calls)}/${formatTokens(coverage.calls)} 笔完整）`
      : '部分回报')
  }
  const unclassified = formatUsageTokenCount(coverage?.unclassified_tokens)
  if (unclassified !== '—' && unclassified !== '0') {
    notes.push(`未分类 ${unclassified} Token`)
  }
  if (isUsageCount(model.calls) && model.calls > 0 && formatUsageCredits(model.credits) === '0') notes.push('零额已结算')
  const review = formatUsagePendingReview(model)
  if (review) notes.push(review)
  return notes.join(' · ') || '—'
}

type ModelUsageLabel = Pick<StudentModelUsageFields, 'credits' | 'calls' | 'pending_review_calls'>

export function formatStudentModelUsageParts(row?: ModelUsageLabel) {
  const settledCalls = row?.calls ?? 0
  const pendingCalls = row?.pending_review_calls ?? 0
  const amount = formatUsageCredits(row?.credits)
  let credits = '本月 0.000 积分'
  if (row && amount === '—') credits = '本月积分未知'
  else if (row && amount !== '0') credits = `本月已用 ${amount} 积分`
  else if (isUsageCount(settledCalls) && settledCalls > 0) credits = '本月 0 积分'
  const review = isUsageCount(pendingCalls) && pendingCalls > 0 ? ` · 待核 ${formatTokens(pendingCalls)} 笔` : ''
  return { credits, calls: `${formatUsageCount(settledCalls)} 次${review}` }
}

export function formatStudentModelUsage(row?: ModelUsageLabel): string {
  const { credits, calls } = formatStudentModelUsageParts(row)
  return `${credits} · ${calls}`
}
