import type { BeichenPriceConditions, BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { keyGroupRate, priceDetailRows } from './beichenModels'
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

/** 每个模型只保留一行：当前 Key 分组的上游基础价和学生实价。 */
export type GroupPriceRow = {
  group_id: number
  name: string
  rate: string | null
  account_rate: string | null
  is_current_key_group: true
  /** 官网列出的基础价，单位：元 / 1M Token。找不到完整官网价时五项均为 null。 */
  official: GroupPriceValues
  /** 学生实价，单位：积分 / 1M Token。没有官网基础价时五项均为 null。 */
  student: GroupPriceValues
  /** 学生价所依据的基础价来源；没有官网基础价时为 null。 */
  student_source: 'website' | null
  /** 基础价来自哪个官网分组。当前分组自己的完整报价时与 group_id 相同。 */
  base_group_id: number | null
  base_group_name: string | null
  /** 页面提示用的来源简称，如 CC1；优先取分组名，分组名为空才用渠道名。 */
  base_label: string | null
  base_from_other_group: boolean
  base_missing: boolean
  /** 基础价来自其他分组时的整句提示。 */
  base_note: string | null
  /** account_rate 优先的生效倍率。 */
  effective_rate: string | null
  /** 分组倍率与账号倍率不同时的简短说明。 */
  rate_note: string | null
  /** 基础价对应官网 location 的采集时间。 */
  observed_at: string | null
  /** 基础价原始条件，供“查看档位”使用。 */
  price_conditions_exact: BeichenPriceConditions | null
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
  /** 没有官网基础价时，只用于页面小字参考，不参与主价计算。 */
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

function emptyValues(): GroupPriceValues {
  return { input: null, output: null, cache5m: null, cache1h: null, cache_read: null }
}

function decimalText(value: string | number | null | undefined) {
  if (value == null) return null
  const text = trimDecimal(value, 30)
  return text === '—' ? null : text
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

/** 只有非陈旧、确实有价且官网判定完整的 location 才能作为基础价。 */
function completeLocation(location: BeichenWebsiteLocation) {
  return !location.stale && location.has_price && location.price_complete === true &&
    Boolean(location.price_conditions_exact)
}

function locationTime(location: BeichenWebsiteLocation) {
  const time = Date.parse(location.observed_at)
  return Number.isFinite(time) ? time : 0
}

function activeGroup(group: BeichenWebsiteGroup | undefined) {
  if (!group) return undefined
  return !group.status || group.status === 'active' ? group : undefined
}

function chooseComplete(locations: BeichenWebsiteLocation[], groupID: number | null, platformOnly = false) {
  return locations
    .filter(completeLocation)
    // 跨组回退只能使用平台级基础价；旧数据没有 price_scope 时按分组专属价处理。
    .filter(location => !platformOnly || location.price_scope === 'platform')
    .filter(location => groupID == null || location.groups.some(group => group.id === groupID && activeGroup(group)))
    .sort((left, right) => locationTime(right) - locationTime(left) || (left.name || '').localeCompare(right.name || ''))[0]
}

function bracketLabel(name: string | null | undefined) {
  const text = name?.trim() ?? ''
  const match = /^\[([^\]]+)\]/.exec(text)
  return (match ? match[1] : text).trim()
}

function sourceLabel(location: BeichenWebsiteLocation) {
  const group = location.groups.map(item => item.name).find(name => Boolean(name?.trim()))
  if (group) return bracketLabel(group)
  const channel = location.channel?.trim()
  if (channel) return bracketLabel(channel)
  return location.name.trim() || '官网'
}

function sourceGroup(location: BeichenWebsiteLocation) {
  return location.groups.find(group => Boolean(group.name?.trim()) && activeGroup(group)) ?? location.groups[0]
}

/**
 * 为一个模型生成唯一价格行。
 *
 * 1. 先找当前 Key 分组的完整官网价；
 * 2. 找不到时，只用平台级（price_scope=platform）的完整官网 location 作模型级基础价；
 *    分组专属价或缺少 price_scope 的旧数据不能跨组使用；
 * 3. 没有官网基础价时不使用历史反推价，只把反推价留给页面作小字参考。
 */
export function buildCurrentGroupPriceRow(locations: BeichenWebsiteLocation[],
  currentGroupID?: number | null, options: GroupPriceRowOptions = {}): GroupPriceRow | null {
  const keyGroup = options.keyGroup ?? null
  const groupID = keyGroup?.id ?? currentGroupID ?? null
  if (groupID == null) return null

  const websiteGroup = locations.flatMap(location => location.groups)
    .find(group => group.id === groupID && activeGroup(group))
  // keyGroup 存在时以 /keys 为准；rate 为 null 表示当前倍率不可用，不能回退到官网旧值。
  const rate = keyGroup ? decimalText(keyGroup.rate) : decimalText(websiteGroup?.rate_multiplier)
  // /keys 没有给账户倍率时，不沿用官网里可能过期的账户倍率。
  const accountRate = decimalText(keyGroup?.account_rate ?? (keyGroup ? null : websiteGroup?.account_rate_multiplier))
  const name = keyGroup?.name?.trim() || websiteGroup?.name?.trim() || '分组 ' + groupID

  const current = chooseComplete(locations, groupID)
  const fallback = current ? undefined : chooseComplete(locations, null, true)
  const source = current ?? fallback
  const official = pickValues(source?.price_conditions_exact, 'official', null, null)
  const student = source ? pickValues(source.price_conditions_exact, 'student', rate, accountRate) : emptyValues()
  const baseGroup = source ? sourceGroup(source) : undefined
  const baseFromOtherGroup = Boolean(source && !current)
  const effectiveRate = accountRate ?? rate
  const effectiveRateText = effectiveRate ? keyGroupRate(effectiveRate) : ''
  const rateNote = rate && accountRate && keyGroupRate(rate) !== keyGroupRate(accountRate)
    ? `分组 ${keyGroupRate(rate)} / 账号 ${keyGroupRate(accountRate)}`
    : null
  const row = {
    group_id: groupID,
    name,
    rate,
    account_rate: accountRate,
    is_current_key_group: true as const,
    official,
    student,
    student_source: hasValues(official) ? 'website' as const : null,
    base_group_id: source ? baseGroup?.id ?? groupID : null,
    base_group_name: baseGroup?.name?.trim() || null,
    base_label: baseFromOtherGroup && source ? sourceLabel(source) : null,
    base_from_other_group: baseFromOtherGroup,
    base_missing: !hasValues(official),
    base_note: baseFromOtherGroup && source
      ? effectiveRateText
        ? `基础价来自 ${sourceLabel(source)} 同名模型；当前 Key 分组 ${groupID} 的倍率为 ${effectiveRateText}`
        : `基础价来自 ${sourceLabel(source)} 同名模型；当前 Key 分组 ${groupID} 的倍率待核实`
      : null,
    effective_rate: effectiveRate,
    rate_note: rateNote,
    observed_at: source?.observed_at ?? null,
    price_conditions_exact: source?.price_conditions_exact ?? null,
    input: student.input,
    output: student.output,
    cache5m: student.cache5m,
    cache1h: student.cache1h,
    cache_read: student.cache_read,
  } as GroupPriceRow
  // 兼容旧调用方：扁平五项始终跟随 student，不保留第二份事实来源。
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

/** 历史反推价只用于小字参考，不参与主价。 */
export function priceReferenceText(terms: BeichenPriceConditions | null | undefined) {
  const rows = priceDetailRows(terms, null)
    .filter(row => !row.notApplicable && row.yuan != null)
    .map(row => `${row.label} ${row.yuan} 元`)
  return rows.length ? rows.join('、') : null
}
