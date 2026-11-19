<script setup lang="ts">
import type { BeichenReceiptSpend } from '../../api/types'
defineProps<{ spend: BeichenReceiptSpend }>()
const sources: Record<string, string> = {
  gateway_usage_price: '网关用量与报价', receipt_usage_price: '回执用量与报价',
  receipt_actual_cost: '回执实付补记', legacy: '历史记账', mixed: '多种来源',
}
</script>

<template>
  <span class="receipt-spend">
    <span>北辰实扣 {{ spend.beichen_actual_cost?.trim() ? spend.beichen_actual_cost + ' 余额单位' : '未知' }}</span>
    <small class="muted">待核 {{ spend.beichen_unmatched_calls == null ? '未知' : spend.beichen_unmatched_calls + ' 笔' }}</small>
    <details v-if="spend.beichen_base_credits != null || spend.beichen_charged_credits != null">
      <summary>学校记账明细</summary>
      <small>基础积分 {{ spend.beichen_base_credits ?? '未知' }} · 入账倍率 {{ spend.beichen_school_multiplier == null ? '未知或多种' : spend.beichen_school_multiplier + '×' }}</small>
      <small>学校已记 {{ spend.beichen_charged_credits ?? '未知' }} 积分 · {{ sources[spend.beichen_base_source ?? ''] ?? '来源未知' }}</small>
    </details>
  </span>
</template>

<style scoped>
.receipt-spend { display: inline-grid; gap: 2px; font-variant-numeric: tabular-nums; }
.receipt-spend small { display: block; }
.receipt-spend summary { cursor: pointer; }
</style>
