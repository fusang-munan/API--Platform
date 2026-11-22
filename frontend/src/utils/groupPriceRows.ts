import type { BeichenWebsiteGroup, BeichenWebsiteLocation } from '../api/types'
import { priceDetailRows } from './beichenModels'
import { multiplyDecimal, trimDecimal } from './credits'

/** 分组价格表中的学生实价，单位是积分 / 1M Token。 */
export type GroupPriceRow = {
  group_id: number
  name: string
  rate: string | null
  account_rate: string | null
  is_current_key_group: boolean
  input: string | null
  output: string | null
  cache5m: string | null
  cache1h: string | null
  cache_read: string | null
}

type Candidate = {
  row: GroupPriceRow
  rank: number
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

function priceValues(location: BeichenWebsiteLocation, rate: string | null, accountRate: string | null) {
  const empty = { input: null, output: null, cache5m: null, cache1h: null, cache_read: null }
  const terms = location.price_conditions_exact
  const factor = priceFactor(rate, accountRate)
  if (!terms || !factor) return empty
  const rows = priceDetailRows(terms, factor)
  const credits = (key: 'input' | 'output' | 'cache_write_5m' | 'cache_write_1h' | 'cache_read') => {
    const row = rows.find(item => item.key === key)
    return row && !row.notApplicable ? decimalText(row.credits) : null
  }
  return {
    input: credits('input'),
    output: credits('output'),
    cache5m: credits('cache_write_5m'),
    cache1h: credits('cache_write_1h'),
    cache_read: credits('cache_read'),
  }
}

function candidate(location: BeichenWebsiteLocation, group: BeichenWebsiteGroup,
  groupID: number, currentGroupID: number | null): Candidate {
  const rate = decimalText(group.rate_multiplier)
  const accountRate = decimalText(group.account_rate_multiplier)
  return {
    rank: locationRank(location),
    row: {
      group_id: groupID,
      name: groupName(group, groupID),
      rate,
      account_rate: accountRate,
      is_current_key_group: groupID === currentGroupID,
      ...priceValues(location, rate, accountRate),
    },
  }
}

/**
 * 把一个模型的官网分组位置整理成一行一个分组。重复 group_id 只保留
 * 观测更完整的一份，当前 Key 分组排在首位；当前分组始终由外部明确传入。
 */
export function buildGroupPriceRows(locations: BeichenWebsiteLocation[],
  currentGroupID?: number | null): GroupPriceRow[] {
  const current = currentGroupID ?? null
  const groups = new Map<number, Candidate>()
  for (const location of locations) {
    if (location.stale) continue
    for (const group of location.groups ?? []) {
      if (group.id == null || !Number.isFinite(group.id)) continue
      if (group.status && group.status !== 'active') continue
      const next = candidate(location, group, group.id, current)
      const previous = groups.get(group.id)
      if (!previous || next.rank > previous.rank) groups.set(group.id, next)
    }
  }
  const rows = Array.from(groups.values(), item => item.row)
  const own = rows.filter(row => row.is_current_key_group)
  return [...own, ...rows.filter(row => !row.is_current_key_group)]
}

export const groupPriceRows = buildGroupPriceRows
