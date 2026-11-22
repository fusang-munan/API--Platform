import type { BeichenPriceConditions } from '../api/types'
import { billingModeLabel, formatObservedAt, keyGroupRate, priceDetailRows, type PriceDetailRow } from './beichenModels'
import { multiplyDecimal, shiftDecimal } from './credits'

type PriceObservation = {
  price_conditions_exact?: BeichenPriceConditions | null
  observed_at?: string | null
}

export type BeichenPriceTableInput = {
  website?: PriceObservation | null
  groupMultiplier?: number | string | null
  /** 仅供教师查看。传入未乘 Key 分组倍率的历史流水反推基础单价。 */
  derived?: PriceObservation | null
}

function selectObservation(website: BeichenPriceTableInput['website'], derived: BeichenPriceTableInput['derived']) {
  // 以整份报价为单位选择来源。官网未列出的单项保留为空。
  if (website?.price_conditions_exact != null) return { source: 'website' as const, observation: website }
  if (derived?.price_conditions_exact != null) return { source: 'derived' as const, observation: derived }
  return { source: 'unlisted' as const, observation: website }
}

function priceAmount(row?: PriceDetailRow) {
  if (row?.yuan == null || row.credits == null) return null
  return { yuan: row.yuan, credits: row.credits }
}

/** 读取官网基础价或已解析的单个档位。输入为每 Token 的元价，表中按每百万 Token 计价。 */
export function calculateBeichenPriceTable({ website, groupMultiplier, derived }: BeichenPriceTableInput) {
  const { source, observation } = selectObservation(website, derived)
  const conditions = observation?.price_conditions_exact
  const observedAt = observation?.observed_at ?? null
  const rateText = groupMultiplier == null ? undefined : String(groupMultiplier)
  const groupMultiplierLabel = keyGroupRate(rateText) || null
  const baseRows = priceDetailRows(conditions)
  const settlementRows = groupMultiplierLabel ? priceDetailRows(conditions, rateText) : []

  return {
    source,
    observedAt,
    observedLabel: formatObservedAt(observedAt),
    groupMultiplierLabel,
    unitLabel: baseRows.some(row => row.notApplicable) ? billingModeLabel(conditions?.billing_mode) : '每百万 Token',
    rows: baseRows.map((row, index) => {
      const settlement = priceAmount(settlementRows[index])
      const studentYuan = settlement ? multiplyDecimal(settlement.yuan, '1.10') : null
      return {
        key: row.key,
        label: row.label,
        notApplicable: row.notApplicable,
        base: priceAmount(row),
        settlement,
        student: studentYuan === null ? null : { yuan: studentYuan, credits: shiftDecimal(studentYuan, 2) },
      }
    }),
  }
}
