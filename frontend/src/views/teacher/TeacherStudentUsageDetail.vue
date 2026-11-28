<script setup lang="ts">
import { computed } from 'vue'
import type { TeacherStudentUsageDetail } from '../../api/teacherUsage'
import { formatPercent } from '../../utils/credits'
import { statusLabel } from '../../utils/format'
import TeacherUsageMetrics from './TeacherUsageMetrics.vue'

const props = defineProps<{ detail: TeacherStudentUsageDetail }>()
const keys = computed(() => props.detail.projects.flatMap(project =>
  project.keys.map(key => ({ project, key }))))
</script>

<template>
  <ul class="usage-detail" aria-label="按 Key 汇总">
    <li v-for="{ project, key } in keys" :key="`${project.project_id}/${key.id}`">
      <details class="key-detail">
        <summary>
          <span class="key-summary">
            <span class="key-identity">
              <strong>{{ key.name || key.id }}</strong>
              <small class="muted">{{ project.project_name || project.project_id }} · {{ key.status === 'unknown' ? '历史 Key' : statusLabel(key.status) }}</small>
            </span>
            <span><strong>{{ key.used }}</strong> 积分</span>
            <span>{{ key.calls }} 次</span>
            <span class="detail-label">详情</span>
          </span>
        </summary>
        <div class="key-metrics">
          <TeacherUsageMetrics :usage="key" :credits="key.used" />
        </div>
        <ul class="model-list" :aria-label="`${key.name || key.id} 的模型用量`">
          <li v-for="model in key.models" :key="model.id">
            <strong>{{ model.name || model.id }}</strong>
            <TeacherUsageMetrics :usage="model" :credits="model.credits" />
            <span class="parts muted">
              <span>输入 {{ model.tokens.input ?? '无样本' }}</span>
              <span>输出 {{ model.tokens.output ?? '无样本' }}</span>
              <span>缓存读 {{ model.tokens.cache_read ?? '无样本' }}</span>
              <span>缓存写 {{ model.tokens.cache_write ?? '无样本' }}</span>
              <span>缓存命中率 {{ model.cache_hit_rate == null ? '无样本' : formatPercent(model.cache_hit_rate) }}</span>
            </span>
          </li>
        </ul>
      </details>
    </li>
  </ul>
</template>

<style scoped>
.usage-detail, .model-list { display: grid; gap: .5rem; padding: 0; margin: 0; list-style: none; }
.key-detail { border: 1px solid var(--line); border-radius: .65rem; background: var(--surface); }
summary {
  display: flex; align-items: center; gap: .5rem; padding: .65rem .8rem; cursor: pointer;
  overflow-wrap: anywhere; font-variant-numeric: tabular-nums;
}
summary::before {
  content: ''; flex: none; width: .45rem; height: .45rem; border: solid currentColor;
  border-width: 0 1px 1px 0; transform: rotate(-45deg);
}
.key-detail[open] summary::before { transform: rotate(45deg); }
.key-summary {
  flex: 1; min-width: 0; display: grid; align-items: center; gap: .25rem .8rem;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, .65fr) auto;
}
.key-identity small { display: block; margin-top: .2rem; }
.detail-label { color: var(--primary-strong); }
.key-metrics, .model-list { margin: 0 .8rem .65rem; }
.model-list li { display: grid; gap: .25rem; padding: .55rem .7rem; border-radius: .45rem; background: var(--primary-soft); overflow-wrap: anywhere; }
.parts { display: flex; flex-wrap: wrap; gap: .15rem .7rem; }
@media (max-width: 560px) {
  .key-summary { grid-template-columns: repeat(2, minmax(0, 1fr)) auto; }
  .key-identity { grid-column: 1 / -1; }
}
</style>
