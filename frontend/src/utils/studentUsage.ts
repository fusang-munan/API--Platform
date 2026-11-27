import type { StudentUsageMetrics, StudentUsageModelMetrics, StudentUsageProject } from '../api/types'
import { formatTokens, shiftDecimal } from './credits'
import { calculateCredits } from './format'

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

type ModelUsageLabel = Pick<StudentUsageModelMetrics, 'credits' | 'calls' | 'pending_review_calls'>

export function formatStudentModelUsageParts(row?: ModelUsageLabel) {
  const settledCalls = row?.calls ?? 0
  const pendingCalls = row?.pending_review_calls ?? 0
  const amount = formatUsageCredits(row?.credits)
  let credits = '本月 0.000 积分'
  if (settledCalls > 0) {
    credits = amount === '0' ? '本月 0 积分' : `本月已用 ${amount} 积分`
  }
  const review = pendingCalls > 0 ? ` · 待核 ${formatTokens(pendingCalls)} 笔` : ''
  return { credits, calls: `${formatTokens(settledCalls)} 次${review}` }
}

export function formatStudentModelUsage(row?: ModelUsageLabel): string {
  const { credits, calls } = formatStudentModelUsageParts(row)
  return `${credits} · ${calls}`
}
