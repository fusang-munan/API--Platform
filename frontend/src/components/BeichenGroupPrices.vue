<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import type { BeichenWebsiteLocation } from '../api/types'
import { buildGroupPriceRows, type GroupPriceRow } from '../utils/groupPriceRows'
import { trimDecimal } from '../utils/credits'

const props = withDefaults(defineProps<{
  rows?: GroupPriceRow[]
  locations?: BeichenWebsiteLocation[]
  currentGroupId?: number | null
  title?: string
  emptyText?: string
}>(), {
  title: '分组价格对比',
  emptyText: '暂无分组价格',
})

const rows = computed<GroupPriceRow[]>(() => {
  const source = props.rows ?? buildGroupPriceRows(props.locations ?? [], props.currentGroupId ?? null)
  if (props.currentGroupId == null) return source
  return source.map(row => ({ ...row, is_current_key_group: row.group_id === props.currentGroupId }))
})

const expanded = ref(new Set<number>())
const uid = useId()
const shape = computed(() => rows.value.map(row => row.group_id + ':' + Number(row.is_current_key_group)).join('|'))

watch(shape, () => {
  const own = rows.value.find(row => row.is_current_key_group)
  expanded.value = new Set(own ? [own.group_id] : [])
}, { immediate: true })

const currentRow = computed(() => rows.value.find(row => row.is_current_key_group))

function rateText(value: string | null) {
  return value ? trimDecimal(value, 6) + '×' : '倍率未知'
}

function summary(row: GroupPriceRow) {
  return [row.name, rateText(row.rate), isExpanded(row) ? '收起对比' : '展开对比'].join(' · ')
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

function amount(value: string | null) {
  return value == null ? '—' : trimDecimal(value, 8)
}
</script>

<template>
  <section class="group-prices" data-testid="group-prices">
    <header class="group-prices-head">
      <div>
        <strong>{{ title }}</strong>
        <small v-if="rows.length">共 {{ rows.length }} 个分组</small>
      </div>
      <slot name="actions" />
    </header>

    <p v-if="!rows.length" class="group-prices-note" role="status">{{ emptyText }}</p>
    <p v-else-if="!currentRow" class="group-prices-note" data-testid="current-group-missing" role="status">
      当前 Key 分组未匹配
    </p>

    <ul v-if="rows.length" class="group-prices-list">
      <li v-for="row in rows" :key="row.group_id" class="group-price-row"
        :class="{ 'is-current': row.is_current_key_group }"
        :data-group-id="row.group_id" :data-current="row.is_current_key_group ? 'true' : 'false'">
        <button type="button" class="group-price-summary" :aria-expanded="isExpanded(row)"
          :aria-controls="panelID(row)" @click="toggle(row)">
          <span class="group-price-title">{{ summary(row) }}</span>
          <span v-if="row.account_rate" class="group-price-account">账户覆盖 {{ rateText(row.account_rate) }}</span>
          <span v-if="row.is_current_key_group" class="group-price-current">当前 Key 分组</span>
          <span class="group-price-toggle" aria-hidden="true" />
        </button>

        <div v-if="isExpanded(row)" :id="panelID(row)" class="group-price-panel">
          <p class="group-price-unit">学生价 · 积分 / 1M Token</p>
          <div class="group-price-table-wrap">
            <table class="group-price-table">
              <caption class="sr-only">{{ row.name }}五项学生单价</caption>
              <thead>
                <tr><th v-for="label in ['输入', '输出', '缓存 5m', '缓存 1h', '缓存读取']" :key="label" scope="col">{{ label }}</th></tr>
              </thead>
              <tbody>
                <tr>
                  <td class="numeric-cell" data-price="input">{{ amount(row.input) }}</td>
                  <td class="numeric-cell" data-price="output">{{ amount(row.output) }}</td>
                  <td class="numeric-cell" data-price="cache5m">{{ amount(row.cache5m) }}</td>
                  <td class="numeric-cell" data-price="cache1h">{{ amount(row.cache1h) }}</td>
                  <td class="numeric-cell" data-price="cache_read">{{ amount(row.cache_read) }}</td>
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
.group-prices-head > div { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.group-prices-head strong { font-size: 13px; }
.group-prices-head small, .group-prices-note { color: var(--ink-soft); font-size: 12px; }
.group-prices-note { margin: 0; }
.group-prices-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.group-price-row { min-width: 0; border: 1px solid var(--line); border-radius: 10px; background: var(--surface, #fff); }
.group-price-row.is-current { border-color: var(--primary, #6c4ce8); box-shadow: inset 3px 0 var(--primary, #6c4ce8); }
.group-price-summary { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px; border: 0; background: transparent; color: var(--ink); font: inherit; text-align: left; cursor: pointer; }
.group-price-title { min-width: 0; font-size: 12px; font-weight: 650; }
.group-price-account, .group-price-current { flex: 0 0 auto; font-size: 11px; }
.group-price-account { color: var(--ink-soft); }
.group-price-current { padding: 1px 6px; border-radius: 999px; background: color-mix(in srgb, var(--primary, #6c4ce8) 12%, transparent); color: var(--primary, #6c4ce8); font-weight: 700; }
.group-price-toggle { flex: 0 0 auto; margin-left: auto; color: var(--ink-soft); font-size: 15px; line-height: 1; }
.group-price-toggle::after { content: '+'; }
.group-price-summary[aria-expanded="true"] .group-price-toggle::after { content: '−'; }
.group-price-panel { display: grid; gap: 5px; padding: 0 10px 9px; }
.group-price-unit { margin: 0; color: var(--ink-soft); font-size: 11px; }
.group-price-table-wrap { min-width: 0; overflow-x: auto; }
.group-price-table { width: 100%; min-width: 0; border-collapse: collapse; table-layout: fixed; }
.group-price-table th, .group-price-table td { padding: 4px 3px; border-bottom: 1px solid rgba(83, 99, 136, .12); text-align: right; vertical-align: baseline; }
.group-price-table th { color: var(--ink-soft); font-size: 10px; font-weight: 650; white-space: normal; }
.group-price-table td { font-size: 12px; }
.group-price-table th:last-child, .group-price-table td:last-child { border-right: 0; }
.numeric-cell { font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
