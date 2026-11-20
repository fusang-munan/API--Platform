import type { ApiKeyStatus, ModelStatus, ProgressCurrent, ResourceStatus, Role } from '../api/types'

const roleLabels: Record<Role, string> = { student: '学生', mentor: '导师', teacher: '老师' }
const statusLabels: Record<ApiKeyStatus | ModelStatus | ResourceStatus | string, string> = {
  pending_mentor: '等待导师审核', pending_teacher: '等待老师审核', approved: '等待领取', active: '已启用',
  rejected: '已驳回', inactive: '已停用', withdrawn: '已撤回', pending_configuration: '待配置', ready: '已就绪', pending: '待审核',
  cancelled: '已取消', mentor_review: '导师审核', teacher_review: '老师审核', claim: '等待领取',
  rejected_teacher: '老师已驳回', pending_review: '待核账',
}
export const roleLabel = (role?: Role) => role ? roleLabels[role] : ''
export const statusLabel = (status?: string) => status ? statusLabels[status] ?? status : ''
export const progressLabel = (current: ProgressCurrent) => statusLabel(current)
export function fullTime(value?: string | null): string { return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—' }
export function relativeTime(value?: string | null): string {
  if (!value) return '—'
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000)
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })
  if (Math.abs(seconds) < 60) return formatter.format(seconds, 'second')
  const minutes = Math.round(seconds / 60); if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute')
  const hours = Math.round(minutes / 60); if (Math.abs(hours) < 24) return formatter.format(hours, 'hour')
  return formatter.format(Math.round(hours / 24), 'day')
}

// Align the decimal places before calculating so displayed amounts retain every digit.
export function calculateCredits(left: string, right: string, operation: 'add' | 'subtract'): string {
  const [leftWhole, leftFraction = ''] = left.split('.')
  const [rightWhole, rightFraction = ''] = right.split('.')
  const scale = Math.max(3, leftFraction.length, rightFraction.length)
  const a = BigInt(leftWhole + leftFraction.padEnd(scale, '0'))
  const b = BigInt(rightWhole + rightFraction.padEnd(scale, '0'))
  const amount = operation === 'add' ? a + b : a - b
  if (amount <= 0n) return '0.000'
  const digits = amount.toString().padStart(scale + 1, '0')
  const fraction = digits.slice(-scale).replace(/0+$/, '').padEnd(3, '0')
  return `${digits.slice(0, -scale)}.${fraction}`
}

export const beichenBillingExplanation = '北辰 1 余额单位的充值等值为 1 元，对应 100 基础积分。学校按基础积分 × 1.10 记入已用额度。例如老师分配 100 积分，一次调用消耗 10 基础积分，学校记已用 11 积分，剩余 89 积分。北辰账单用于核对，学校已用额度保持原记账金额。'

export function schoolMonth() {
  return new Intl.DateTimeFormat('sv-SE', { year: 'numeric', month: '2-digit', timeZone: 'Asia/Shanghai' }).format(new Date())
}

export function schoolDay() {
  return new Intl.DateTimeFormat('sv-SE', { year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'Asia/Shanghai' }).format(new Date())
}

export function quotaBalance(quota: string, used: string, available?: string | null) {
  const over = calculateCredits(used, quota, 'subtract')
  if (over !== '0.000') return { label: '已超用', amount: over }
  return { label: '剩余', amount: available ?? calculateCredits(quota, used, 'subtract') }
}
