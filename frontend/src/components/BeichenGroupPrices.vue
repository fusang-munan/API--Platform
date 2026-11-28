<script setup lang="ts">
import { computed } from 'vue'
import { formatPriceAmount } from '../utils/credits'
import { studentPriceLabels } from '../utils/beichenModels'
import type { GroupPriceRow, GroupPriceValues } from '../utils/groupPriceRows'

const props = withDefaults(defineProps<{
  row?: GroupPriceRow | null
  title?: string
  note?: string
  derivedNote?: string
  emptyText?: string
}>(), {
  title: '当前 Key 分组价格',
  emptyText: '未匹配到官网基础价',
})

const priceColumns: Array<{ key: keyof GroupPriceValues; label: string }> = [
  { key: 'input', label: '输入' },
  { key: 'output', label: '输出' },
  { key: 'cache5m', label: '缓存写入(5m)' },
  { key: 'cache1h', label: '缓存写入(1h)' },
  { key: 'cache_read', label: '缓存读取' },
]

const hasOfficial = computed(() => Boolean(props.row && priceColumns.some(column => props.row?.official[column.key] != null)))
const source = computed(() => props.row?.student_source ?? 'none')
const basisNote = computed(() => props.note ?? props.row?.base_note ?? '')
const rateNote = computed(() => props.row?.rate_note ?? '')
const studentStatus = computed(() => props.row?.student_status)

function officialAmount(row: GroupPriceRow, key: keyof GroupPriceValues) {
  return formatPriceAmount(row.official[key], '未列')
}

function studentAmount(row: GroupPriceRow, key: keyof GroupPriceValues) {
  return formatPriceAmount(row.student[key])
}
</script>

<template>
  <section class="group-prices" data-testid="group-prices" :data-price-source="source">
    <header class="group-prices-head">
      <div class="group-prices-title">
        <strong>{{ title }}</strong>
        <small v-if="rateNote" class="group-rate-note" data-testid="rate-note">{{ rateNote }}</small>
      </div>
      <slot name="actions" />
    </header>

    <p v-if="basisNote" class="group-price-basis" data-testid="price-basis">{{ basisNote }}</p>
    <p v-if="!row" class="group-prices-note" role="status">{{ emptyText }}</p>

    <template v-else>
      <p v-if="!hasOfficial" class="group-prices-note" data-testid="price-base-missing" role="status">{{ emptyText }}</p>
      <div class="group-price-table-wrap">
        <table class="group-price-table" data-testid="model-price-table">
          <caption class="sr-only">{{ row.name }}五项价格：上游基础价和学生实价</caption>
          <thead>
            <tr>
              <th scope="col" class="price-kind">价格</th>
              <th v-for="column in priceColumns" :key="column.key" scope="col">{{ column.label }}</th>
            </tr>
          </thead>
          <tbody>
            <tr data-price-row="official">
              <th scope="row">上游基础价<small>（元 / 1M）</small></th>
              <td v-for="column in priceColumns" :key="column.key" class="numeric-cell"
                :data-price="column.key" :data-label="column.label">{{ officialAmount(row, column.key) }}</td>
            </tr>
            <tr data-price-row="student">
              <th scope="row">学生实价<small>（积分 / 1M）</small></th>
              <td v-for="column in priceColumns" :key="column.key" class="numeric-cell"
                :data-price="column.key" :data-label="column.label">{{ studentAmount(row, column.key) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="studentStatus && studentStatus.status !== 'normal' && studentStatus.status !== 'manual'"
        class="group-prices-note" data-testid="student-price-status" :data-status="studentStatus.status">
        {{ studentPriceLabels[studentStatus.status] }} · {{ studentStatus.reason }}
      </p>
      <p v-if="derivedNote" class="group-price-derived" data-testid="derived-reference">{{ derivedNote }}</p>
    </template>
  </section>
</template>

<style scoped>
.group-prices { display: grid; gap: 7px; min-width: 0; container-type: inline-size; }
.group-prices-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; min-width: 0; }
.group-prices-title { display: grid; gap: 2px; min-width: 0; }
.group-prices-head strong { font-size: 13px; overflow-wrap: anywhere; }
.group-rate-note { color: var(--ink-soft); font-size: 11px; }
.group-prices-note { margin: 0; color: var(--ink-soft); font-size: 12px; overflow-wrap: anywhere; }
.group-price-basis, .group-price-derived { margin: 0; color: var(--ink-soft); font-size: 11px; line-height: 1.5; overflow-wrap: anywhere; }
.group-price-table-wrap { min-width: 0; }
.group-price-table { width: 100%; min-width: 0; table-layout: fixed; border-collapse: collapse; }
.group-price-table th, .group-price-table td { padding: 5px 4px; border-bottom: 1px solid rgba(83, 99, 136, .12); text-align: right; vertical-align: baseline; }
.group-price-table thead th { color: var(--ink-soft); font-size: 10px; font-weight: 650; white-space: normal; }
.group-price-table th:first-child { width: 5.8rem; padding-left: 0; text-align: left; }
.group-price-table tbody th { color: var(--ink-soft); font-size: 11px; font-weight: 650; line-height: 1.4; }
.group-price-table tbody th small { display: block; color: var(--ink-soft); font-size: 9px; font-weight: 400; }
.group-price-table td { font-size: 12px; }
.group-price-table tr[data-price-row="student"] td { color: var(--ink); font-weight: 650; }
.numeric-cell {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace;
  font-variant-numeric: tabular-nums;
  text-align: right;
  white-space: normal;
  overflow-wrap: anywhere;
  word-break: break-word;
}
@container (max-width: 34rem) {
  .group-price-table thead { display: none; }
  .group-price-table, .group-price-table tbody, .group-price-table tr, .group-price-table th, .group-price-table td {
    display: block; width: auto;
  }
  .group-price-table tr[data-price-row] {
    display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 10px;
    padding: 7px 0; border-bottom: 1px solid rgba(83, 99, 136, .12);
  }
  .group-price-table tr[data-price-row] > th { grid-column: 1 / -1; padding: 0 0 4px; border: 0; }
  .group-price-table td.numeric-cell {
    display: flex; justify-content: space-between; gap: 8px; min-width: 0; padding: 3px 0;
    border: 0; text-align: right; white-space: normal;
  }
  .group-price-table td.numeric-cell::before {
    content: attr(data-label); color: var(--ink-soft); font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    font-weight: 400; font-size: 10px;
  }
}
@container (max-width: 22rem) {
  .group-price-table tr[data-price-row] { grid-template-columns: minmax(0, 1fr); }
}
</style>
