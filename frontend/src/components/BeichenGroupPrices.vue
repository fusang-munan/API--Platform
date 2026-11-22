<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import type { BeichenPriceConditions, BeichenWebsiteLocation } from '../api/types'
import {
  buildGroupPriceRows, type GroupPriceKeyGroup, type GroupPriceRow, type GroupPriceValues,
} from '../utils/groupPriceRows'
import { keyGroupRate } from '../utils/beichenModels'
import { trimDecimal } from '../utils/credits'

const props = withDefaults(defineProps<{
  rows?: GroupPriceRow[]
  locations?: BeichenWebsiteLocation[]
  currentGroupId?: number | null
  keyGroup?: GroupPriceKeyGroup | null
  derivedPrice?: BeichenPriceConditions | null
  title?: string
  emptyText?: string
}>(), {
  title: '分组价格对比',
  emptyText: '暂无分组价格',
})

const priceColumns: Array<{ key: keyof GroupPriceValues; label: string }> = [
  { key: 'input', label: '输入' },
  { key: 'output', label: '输出' },
  { key: 'cache5m', label: '缓存 5m' },
  { key: 'cache1h', label: '缓存 1h' },
  { key: 'cache_read', label: '缓存读取' },
]

const currentId = computed(() => props.keyGroup?.id ?? props.currentGroupId ?? null)

// rows 视为已经构建完成；要让 /keys 倍率覆盖官网，请传 locations + keyGroup。
const sourceRows = computed<GroupPriceRow[]>(() => {
  if (!props.rows) {
    return buildGroupPriceRows(props.locations ?? [], currentId.value, {
      keyGroup: props.keyGroup,
      derived: props.derivedPrice,
    })
  }
  const id = currentId.value
  if (id == null || props.rows.some(row => row.group_id === id)) return props.rows
  const [extra] = buildGroupPriceRows([], id, { keyGroup: props.keyGroup, derived: props.derivedPrice })
  return extra ? [extra, ...props.rows] : props.rows
})

const rows = computed<GroupPriceRow[]>(() => {
  const id = currentId.value
  if (id == null) return sourceRows.value
  return sourceRows.value.map(row => ({ ...row, is_current_key_group: row.group_id === id }))
})

const expanded = ref(new Set<number>())
const uid = useId()
const shape = computed(() => rows.value.map(row => row.group_id + ':' + Number(row.is_current_key_group)).join('|'))

watch(shape, () => {
  const own = rows.value.find(row => row.is_current_key_group)
  expanded.value = new Set(own ? [own.group_id] : [])
}, { immediate: true })

function rateText(value: string | null) {
  if (!value) return '倍率未知'
  return keyGroupRate(value) || '倍率未知'
}

function summary(row: GroupPriceRow) {
  const parts = [row.name, rateText(row.rate)]
  if (row.account_rate) parts.push('账户 ' + rateText(row.account_rate))
  return parts.join(' · ')
}

function isExpanded(row: GroupPriceRow) {
  return expanded.value.has(row.group_id)
}

function toggle(row: GroupPriceRow) {
  const next = new Set(expanded.value)
  if (next.has(row.group_id)) next.delete(row.group_id)
  else next.add(row.group_id)
  expanded.value = next
}

function panelID(row: GroupPriceRow) {
  return 'group-price-' + uid + '-' + row.group_id
}

function amount(value: string | null, empty: string) {
  return value == null ? empty : trimDecimal(value, 8)
}

function valueOf(values: GroupPriceValues | undefined, key: keyof GroupPriceValues) {
  return values?.[key] ?? null
}

function studentValue(row: GroupPriceRow, key: keyof GroupPriceValues) {
  // 教师页可能在分组行生成后回写旧扁平五项，优先使用回写值。
  if (Object.prototype.hasOwnProperty.call(row, key) && row[key] !== undefined) return row[key] ?? null
  return valueOf(row.student, key)
}

function officialAmount(row: GroupPriceRow, key: keyof GroupPriceValues) {
  return amount(valueOf(row.official, key), '未列')
}

function studentAmount(row: GroupPriceRow, key: keyof GroupPriceValues) {
  return amount(studentValue(row, key), '—')
}
</script>

<template>
  <section class="group-prices" data-testid="group-prices">
    <header class="group-prices-head">
      <strong>{{ title }}</strong>
      <slot name="actions" />
    </header>

    <p v-if="!rows.length" class="group-prices-note" role="status">{{ emptyText }}</p>

    <ul v-if="rows.length" class="group-prices-list">
      <li v-for="row in rows" :key="row.group_id" class="group-price-row"
        :class="{ 'is-current': row.is_current_key_group }"
        :data-group-id="row.group_id" :data-current="row.is_current_key_group ? 'true' : 'false'">
        <button type="button" class="group-price-summary" :aria-expanded="isExpanded(row)"
          :aria-controls="panelID(row)" @click="toggle(row)">
          <span class="group-price-title">{{ summary(row) }}</span>
          <span v-if="row.is_current_key_group" class="group-price-current">当前 Key 分组</span>
          <span class="group-price-toggle" aria-hidden="true" />
        </button>

        <div v-if="isExpanded(row)" :id="panelID(row)" class="group-price-panel"
          :data-price-source="row.student_source ?? 'none'">
          <p v-if="row.student_source === 'derived'" class="group-price-derived" data-testid="student-derived">
            官网未列该组价，学生价按历史流水反推。
          </p>
          <div class="group-price-table-wrap">
            <table class="group-price-table">
              <caption class="sr-only">{{ row.name }}五项价格：官网价和学生实价</caption>
              <thead>
                <tr>
                  <th scope="col" class="price-kind">价格</th>
                  <th v-for="column in priceColumns" :key="column.key" scope="col">{{ column.label }}</th>
                </tr>
              </thead>
              <tbody>
                <tr data-price-row="official">
                  <th scope="row">官网价<small>（元 / 1M）</small></th>
                  <td v-for="column in priceColumns" :key="column.key" class="numeric-cell"
                    :data-price="column.key">{{ officialAmount(row, column.key) }}</td>
                </tr>
                <tr data-price-row="student">
                  <th scope="row">学生实价<small>（积分 / 1M）</small></th>
                  <td v-for="column in priceColumns" :key="column.key" class="numeric-cell"
                    :data-price="column.key">{{ studentAmount(row, column.key) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.group-prices { display: grid; gap: 8px; min-width: 0; }
.group-prices-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.group-prices-head strong { font-size: 13px; }
.group-prices-note { margin: 0; color: var(--ink-soft); font-size: 12px; }
.group-prices-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.group-price-row { min-width: 0; border: 1px solid var(--line); border-radius: 10px; background: var(--surface, #fff); }
.group-price-row.is-current { border-color: var(--primary, #6c4ce8); box-shadow: inset 3px 0 var(--primary, #6c4ce8); }
.group-price-summary { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px; border: 0; background: transparent; color: var(--ink); font: inherit; text-align: left; cursor: pointer; }
.group-price-title { min-width: 0; font-size: 12px; font-weight: 650; }
.group-price-current { flex: 0 0 auto; padding: 1px 6px; border-radius: 999px; background: color-mix(in srgb, var(--primary, #6c4ce8) 12%, transparent); color: var(--primary, #6c4ce8); font-size: 11px; font-weight: 700; }
.group-price-toggle { flex: 0 0 auto; margin-left: auto; color: var(--ink-soft); font-size: 15px; line-height: 1; }
.group-price-toggle::after { content: '+'; }
.group-price-summary[aria-expanded="true"] .group-price-toggle::after { content: '−'; }
.group-price-panel { display: grid; gap: 5px; padding: 0 10px 9px; }
.group-price-derived { margin: 0; color: var(--ink-soft); font-size: 11px; }
.group-price-table-wrap { min-width: 0; overflow-x: auto; }
.group-price-table { width: 100%; min-width: 0; border-collapse: collapse; table-layout: fixed; }
.group-price-table th, .group-price-table td { padding: 4px 3px; border-bottom: 1px solid rgba(83, 99, 136, .12); text-align: right; vertical-align: baseline; }
.group-price-table th { color: var(--ink-soft); font-size: 10px; font-weight: 650; white-space: normal; }
.group-price-table th:first-child { width: 4.4rem; padding-left: 0; text-align: left; }
.group-price-table th small { display: block; color: var(--ink-soft); font-size: 9px; font-weight: 400; }
.group-price-table td { font-size: 12px; }
.group-price-table tr[data-price-row="student"] td { font-weight: 650; }
.group-price-table th:last-child, .group-price-table td:last-child { border-right: 0; }
.numeric-cell { font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
