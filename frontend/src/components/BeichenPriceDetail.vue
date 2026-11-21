<script setup lang="ts">
import type { PriceDetailView } from '../utils/beichenModels'

defineProps<{ detail: PriceDetailView; title?: string }>()
</script>

<template>
  <section class="price-detail-card" data-testid="price-detail">
    <header class="price-detail-head">
      <strong v-if="title">{{ title }}</strong>
      <span class="status-pill price-source" :data-source="detail.source">{{ detail.sourceLabel }}</span>
      <small class="price-observed">observed_at {{ detail.observedLabel }}</small>
    </header>
    <dl class="price-detail-list">
      <div class="price-detail-item">
        <dt>计费模式</dt>
        <dd>{{ detail.billingModeLabel }}</dd>
      </div>
      <div v-for="row in detail.rows" :key="row.key" class="price-detail-item" :data-missing="row.missing">
        <dt>{{ row.label }}</dt>
        <dd v-if="row.notApplicable" class="price-empty">不适用</dd>
        <dd v-else-if="row.missing" class="price-missing">{{ row.key === 'cache_write_1h' ? '未列单价' : '缺价' }}</dd>
        <dd v-else>
          <strong>{{ row.yuan }} 元</strong>
          <small>{{ row.credits }} 积分 / 1M Token</small>
          <small v-if="row.shared" class="price-shared">与缓存写入(5m)同价</small>
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
.price-detail-list { display: grid; gap: 4px; margin: 0; }
.price-detail-item { display: grid; grid-template-columns: minmax(7em, auto) minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 4px 0; border-bottom: 1px solid var(--line); }
.price-detail-item:last-child { border-bottom: 0; }
.price-detail-item dt { color: var(--ink-soft); font-size: 12px; }
.price-detail-item dd { margin: 0; font-size: 13px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.price-detail-item dd strong { margin-right: 8px; }
.price-detail-item dd small { color: var(--ink-soft); font-size: 11px; }
.price-missing, .price-empty { color: var(--ink-soft); }
.price-shared { display: block; }
</style>
