<script setup lang="ts">
import type { PriceDetailRow, PriceDetailView } from '../utils/beichenModels'

defineProps<{ detail: PriceDetailView; title?: string }>()

function gapText(row: PriceDetailRow) {
  return (row.key === 'cache_write_1h' ? '未列单价' : '缺价') + '：' + row.label
}
</script>

<template>
  <section class="price-detail-card" data-testid="price-detail">
    <header class="price-detail-head">
      <strong v-if="title">{{ title }}</strong>
      <span class="status-pill price-source" :data-source="detail.source">{{ detail.sourceLabel }}</span>
      <small class="price-observed">observed_at {{ detail.observedLabel }}</small>
    </header>
    <dl class="price-table price-detail-list">
      <div class="price-row price-detail-item">
        <dt class="price-label">计费模式</dt>
        <dd class="price-value">{{ detail.billingModeLabel }}</dd>
      </div>
      <div v-for="row in detail.rows" :key="row.key" class="price-row price-detail-item"
        :data-key="row.key" :data-missing="row.missing">
        <dt class="price-label">{{ row.label }}</dt>
        <dd class="price-value">
          <span class="price-amount">
            <template v-if="row.notApplicable">
              <span class="price-figure price-empty">—</span>
              <small class="price-gap price-not-applicable">不适用：{{ row.label }}</small>
            </template>
            <template v-else-if="row.missing">
              <span class="price-figure price-yuan">—</span>
              <span class="price-figure price-credits">—</span>
              <span class="price-unit">/ 1M Token</span>
              <small class="price-gap">{{ gapText(row) }}</small>
            </template>
            <template v-else>
              <span class="price-figure price-yuan">{{ row.yuan }}<small class="price-unit-mark"> 元</small></span>
              <span class="price-figure price-credits">{{ row.credits }}<small class="price-unit-mark"> 积分</small></span>
              <span class="price-unit">/ 1M Token</span>
              <small v-if="row.shared" class="price-shared">与缓存写入(5m)同价</small>
            </template>
          </span>
        </dd>
      </div>
    </dl>
  </section>
</template>

<style scoped>
.price-detail-card { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--line); border-radius: 12px; min-width: 0; }
.price-detail-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12px; }
.price-detail-head strong { font-size: 13px; }
.price-observed { color: var(--ink-soft); font-weight: 400; }
.price-detail-list { margin: 0; }
.price-detail-item { padding: 5px 0; }
</style>
