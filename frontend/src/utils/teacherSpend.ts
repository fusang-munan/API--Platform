import type { BeichenReceiptSpend, ProjectUsage, UsageSlice } from '../api/types'
import { calculateCredits } from './format'

export type MemberUsage = ProjectUsage['members'][number]

export function receiptSubtotal(rows: BeichenReceiptSpend[]): BeichenReceiptSpend {
  function sum(field: 'beichen_actual_cost' | 'beichen_base_credits' | 'beichen_charged_credits') {
    let total: string | null = null
    for (const row of rows) {
      const amount = row[field]?.trim()
      if (amount) total = total === null ? amount : calculateCredits(total, amount, 'add')
    }
    return total
  }
  const billed = rows.filter(row => row.beichen_base_credits != null || row.beichen_charged_credits != null)
  const factors = new Set(billed.map(row => row.beichen_school_multiplier))
  const sources = new Set(billed.map(row => row.beichen_base_source))
  let unmatched: number | null = 0
  for (const row of rows) {
    unmatched = unmatched === null || row.beichen_unmatched_calls == null
      ? null : unmatched + row.beichen_unmatched_calls
  }
  return {
    beichen_actual_cost: sum('beichen_actual_cost'), beichen_unmatched_calls: unmatched,
    beichen_base_credits: sum('beichen_base_credits'), beichen_charged_credits: sum('beichen_charged_credits'),
    beichen_school_multiplier: factors.size === 1 ? billed[0].beichen_school_multiplier : null,
    beichen_base_source: sources.size === 1 ? billed[0].beichen_base_source : sources.size ? 'mixed' : null,
  }
}

export function memberModelRows(member: MemberUsage) {
  const byModel = new Map<string, UsageSlice[]>()
  function add(model: UsageSlice) {
    const group = byModel.get(model.id)
    if (group) group.push(model)
    else byModel.set(model.id, [model])
  }
  for (const key of member.keys) for (const model of key.models) add(model)
  // Key model counts already include free calls. Older months can have only the free summary.
  const keyModelIds = new Set(byModel.keys())
  for (const model of member.free_models) if (!keyModelIds.has(model.id)) add(model)
  return Array.from(byModel.values(), group => ({
    ...group[0],
    credits: group.reduce((sum, row) => calculateCredits(sum, row.credits, 'add'), '0'),
    calls: group.reduce((sum, row) => sum + (row.calls ?? 0), 0),
    ...receiptSubtotal(group),
  })).sort((a, b) => Number(b.credits) - Number(a.credits) || b.calls - a.calls || a.name.localeCompare(b.name))
}

export function studentSpend(projects: ProjectUsage[]) {
  const students = new Map<string, Array<{ project: ProjectUsage; member: MemberUsage }>>()
  for (const project of projects) {
    for (const member of project.members) {
      if (member.is_student === false) continue
      const entry = { project, member }
      const group = students.get(member.user_id)
      if (group) group.push(entry)
      else students.set(member.user_id, [entry])
    }
  }
  return Array.from(students, ([id, entries]) => {
    const members = entries.map(entry => entry.member)
    return {
      id, name: members[0].user_name, entries,
      credits: members.reduce((sum, member) => calculateCredits(sum, member.credits, 'add'), '0'),
      ...receiptSubtotal(members),
    }
  }).sort((a, b) => a.name.localeCompare(b.name, 'zh-CN') || a.id.localeCompare(b.id))
}
