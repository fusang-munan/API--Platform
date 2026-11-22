import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { priceDetailRows } from './beichenModels'
import { multiplyDecimal, trimDecimal } from './credits'

/** 五项 Token 单价，按每 1M Token 展示。 */
export type GroupPriceValues = {
  input: string | null
  output: string | null
  cache5m: string | null
  cache1h: string | null
  cache_read: string | null
}

/** /keys 返回的当前 Key 分组；rate 是该 Key 实际生效的分组倍率。 */
export type GroupPriceKeyGroup = {
  id: number
  name?: string | null
  rate?: number | string | null
  account_rate?: number | string | null
}

export type GroupPriceRow = {
  group_id: number
  name: string
  rate: string | null
  account_rate: string | null
  is_current_key_group: boolean
  /** 官网列出的基础价，单位：元 / 1M Token。官网未列时五项均为 null。 */
  official: GroupPriceValues
  /** 学生实价，单位：积分 / 1M Token。官网缺价时可由历史流水反推。 */
  student: GroupPriceValues
  /** 学生价所依据的基础价来源；官网和反推都没有时为 null。 */
  student_source: 'website' | 'derived' | null
  /** 兼容旧调用方：与 student 的五项保持同步。 */
  input: string | null
  output: string | null
  cache5m: string | null
  cache1h: string | null
  cache_read: string | null
}

export type GroupPriceRowOptions = {
  /** /keys 的当前 Key 分组；它的分组倍率优先于官网同组倍率。 */
  keyGroup?: GroupPriceKeyGroup | null
  /** 官网没有该组价时，用这份历史流水反推的基础单价计算学生价。 */
  derived?: BeichenPriceConditions | null
}

type ValueKey = keyof GroupPriceValues

const valueKeys: ValueKey[] = ['input', 'output', 'cache5m', 'cache1h', 'cache_read']

const detailKeys: Record<ValueKey, 'input' | 'output' | 'cache_write_5m' | 'cache_write_1h' | 'cache_read'> = {
  input: 'input',
  output: 'output',
  cache5m: 'cache_write_5m',
  cache1h: 'cache_write_1h',
  cache_read: 'cache_read',
}

type Candidate = {
  row: GroupPriceRow
  rank: number
}

function emptyValues(): GroupPriceValues {
  return { input: null, output: null, cache5m: null, cache1h: null, cache_read: null }
}

function decimalText(value: string | number | null | undefined) {
  if (value == null) return null
  const text = trimDecimal(value, 8)
  return text === '—' ? null : text
}

function groupName(group: BeichenWebsiteGroup, groupID: number) {
  return group.name?.trim() || '分组 ' + groupID
}

function locationRank(location: BeichenWebsiteLocation) {
  return (location.stale ? 0 : 4) + (location.price_complete ? 2 : 0) +
    (location.price_conditions_exact ? 1 : 0) + (location.has_price ? 1 : 0)
}

function priceFactor(rate: string | null, accountRate: string | null) {
  const effectiveRate = accountRate ?? rate
  if (!effectiveRate) return null
  const factor = multiplyDecimal(effectiveRate, '1.10')
  return factor === '—' ? null : factor
}

function pickValues(terms: BeichenPriceConditions | null | undefined, unit: 'official' | 'student',
  rate: string | null, accountRate: string | null): GroupPriceValues {
  if (!terms) return emptyValues()
  const factor = unit === 'student' ? priceFactor(rate, accountRate) : null
  if (unit === 'student' && !factor) return emptyValues()
  const rows = priceDetailRows(terms, factor)
  const values = emptyValues()
  for (const key of valueKeys) {
    const row = rows.find(item => item.key === detailKeys[key])
    if (row && !row.notApplicable) {
      values[key] = decimalText(unit === 'student' ? row.credits : row.yuan)
    }
  }
  return values
}

function hasValues(values: GroupPriceValues) {
  return valueKeys.some(key => values[key] != null)
}

function makeRow(groupID: number, name: string, rate: string | null, accountRate: string | null,
  officialTerms: BeichenPriceConditions | null | undefined,
  derivedTerms: BeichenPriceConditions | null | undefined,
  isCurrent: boolean): GroupPriceRow {
  const official = pickValues(officialTerms, 'official', rate, accountRate)
  // 官网整组缺价时才使用历史流水反推，避免把其他分组的官网价混进来。
  const studentSource: GroupPriceRow['student_source'] = hasValues(official) ? 'website'
    : derivedTerms ? 'derived' : null
  const basis = studentSource === 'derived' ? derivedTerms : officialTerms
  const student = pickValues(basis, 'student', rate, accountRate)
  const row = {
    group_id: groupID,
    name,
    rate,
    account_rate: accountRate,
    is_current_key_group: isCurrent,
    official,
    student,
    student_source: studentSource,
    input: student.input,
    output: student.output,
    cache5m: student.cache5m,
    cache1h: student.cache1h,
    cache_read: student.cache_read,
  } as GroupPriceRow
  // 教师页会直接回写旧扁平五项；用访问器让学生价始终只有一份事实来源。
  for (const key of valueKeys) {
    Object.defineProperty(row, key, {
      enumerable: true,
      configurable: true,
      get: () => row.student[key],
      set: (value: string | null) => { row.student[key] = value },
    })
  }
  return row
}

function withKeyGroup(group: BeichenWebsiteGroup, keyGroup: GroupPriceKeyGroup): BeichenWebsiteGroup {
  return {
    ...group,
    name: keyGroup.name?.trim() || group.name,
    rate_multiplier: keyGroup.rate ?? group.rate_multiplier,
    // /keys 未给账户倍率时清掉官网旧值，避免用过期倍率计算学生价。
    account_rate_multiplier: keyGroup.account_rate ?? undefined,
  }
}

function candidate(location: BeichenWebsiteLocation, group: BeichenWebsiteGroup,
  groupID: number, currentGroupID: number | null,
  derived: BeichenPriceConditions | null): Candidate {
  const rate = decimalText(group.rate_multiplier)
  const accountRate = decimalText(group.account_rate_multiplier)
  return {
    rank: locationRank(location),
    row: makeRow(groupID, groupName(group, groupID), rate, accountRate,
      location.price_conditions_exact ?? null, derived, groupID === currentGroupID),
  }
}

/**
 * 把一个模型的官网分组位置整理成一行一个分组。重复 group_id 只保留
 * 观测更完整的一份，当前 Key 分组排在首位。
 *
 * 当前分组由 /keys 的 group_id 决定；传入 keyGroup 时，它的倍率优先于官网
 * 同组倍率，并且即使官网没有该组也会补出一行，避免当前组消失。
 */
export function buildGroupPriceRows(locations: BeichenWebsiteLocation[],
  currentGroupID?: number | null, options: GroupPriceRowOptions = {}): GroupPriceRow[] {
  const keyGroup = options.keyGroup ?? null
  const derived = options.derived ?? null
  const current = keyGroup?.id ?? currentGroupID ?? null
  const groups = new Map<number, Candidate>()
  for (const location of locations) {
    if (location.stale) continue
    for (const group of location.groups ?? []) {
      if (group.id == null || !Number.isFinite(group.id)) continue
      if (group.status && group.status !== 'active') continue
      const effective = keyGroup && group.id === keyGroup.id ? withKeyGroup(group, keyGroup) : group
      const next = candidate(location, effective, group.id, current, derived)
      const previous = groups.get(group.id)
      if (!previous || next.rank > previous.rank) groups.set(group.id, next)
    }
  }
  if (current != null && !groups.has(current)) {
    groups.set(current, {
      rank: -1,
      row: makeRow(current, keyGroup?.name?.trim() || '分组 ' + current,
        decimalText(keyGroup?.rate), decimalText(keyGroup?.account_rate), null, derived, true),
    })
  }
  const rows = Array.from(groups.values(), item => item.row)
  const own = rows.filter(row => row.is_current_key_group)
  return [...own, ...rows.filter(row => !row.is_current_key_group)]
}

export const groupPriceRows = buildGroupPriceRows
