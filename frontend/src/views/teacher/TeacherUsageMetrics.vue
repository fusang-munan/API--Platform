<script setup lang="ts">
import { usageCompletenessLabel, type TeacherUsageMetrics } from '../../api/teacherUsage'

defineProps<{ usage: TeacherUsageMetrics; credits: string }>()
</script>

<template>
  <span class="usage-metrics">
    <span><strong>{{ credits }}</strong> 积分</span>
    <span>{{ usage.calls ?? 0 }} 次 · 零额 {{ usage.zero_cost_calls }}</span>
    <span>{{ usage.tokens.total ?? '—' }} Token · {{ usageCompletenessLabel[usage.completeness] }}</span>
    <span v-if="usage.pending_review_calls" class="review">
      待核 {{ usage.pending_review_calls }} 笔 · {{ usage.pending_review_credits }} 积分
    </span>
  </span>
</template>

<style scoped>
.usage-metrics { display: flex; flex-wrap: wrap; gap: .2rem .9rem; font-size: .82rem; color: var(--ink-soft); }
.usage-metrics > span { min-width: 0; overflow-wrap: anywhere; }
strong { color: var(--ink); font-variant-numeric: tabular-nums; }
.review { color: #825316; }
</style>
