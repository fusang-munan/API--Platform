<script setup lang="ts">
import type { TeacherStudentUsageDetail } from '../../api/teacherUsage'
import { formatPercent } from '../../utils/credits'
import { statusLabel } from '../../utils/format'
import TeacherUsageMetrics from './TeacherUsageMetrics.vue'

defineProps<{ detail: TeacherStudentUsageDetail }>()
</script>

<template>
  <div class="usage-detail">
    <details v-for="project in detail.projects" :key="project.project_id" class="project-detail">
      <summary>
        <strong>{{ project.project_name || project.project_id }}</strong>
        <span class="muted">{{ project.keys.length }} 把 Key</span>
        <TeacherUsageMetrics :usage="project" :credits="project.used" />
      </summary>
      <details v-for="key in project.keys" :key="key.id" class="key-detail">
        <summary>
          <strong>{{ key.name || key.id }}</strong>
          <span class="muted">{{ key.status === 'unknown' ? '历史 Key' : statusLabel(key.status) }} · {{ key.models.length }} 个模型</span>
          <TeacherUsageMetrics :usage="key" :credits="key.used" />
        </summary>
        <ul class="model-list">
          <li v-for="model in key.models" :key="model.id">
            <strong>{{ model.name || model.id }}</strong>
            <TeacherUsageMetrics :usage="model" :credits="model.credits" />
            <span class="parts muted">
              <span>输入 {{ model.tokens.input ?? '—' }}</span>
              <span>输出 {{ model.tokens.output ?? '—' }}</span>
              <span>缓存读 {{ model.tokens.cache_read ?? '—' }}</span>
              <span>缓存写 {{ model.tokens.cache_write ?? '—' }}</span>
              <span v-if="model.cache_hit_rate != null">缓存命中率 {{ formatPercent(model.cache_hit_rate) }}</span>
            </span>
          </li>
        </ul>
      </details>
    </details>
  </div>
</template>

<style scoped>
.usage-detail { display: grid; gap: .5rem; }
.project-detail { border: 1px solid var(--line); border-radius: .65rem; background: var(--surface); }
summary { padding: .65rem .8rem; cursor: pointer; overflow-wrap: anywhere; }
summary > .muted { margin-inline-start: .5rem; }
summary > .usage-metrics { margin-top: .25rem; }
.key-detail { margin: 0 .75rem; border-top: 1px solid var(--line); }
.key-detail summary { padding-inline: .25rem; }
.model-list { display: grid; gap: .4rem; padding: 0; margin: 0 0 .7rem; list-style: none; }
.model-list li { display: grid; gap: .25rem; padding: .55rem .7rem; border-radius: .45rem; background: var(--primary-soft); overflow-wrap: anywhere; }
.parts { display: flex; flex-wrap: wrap; gap: .15rem .75rem; }
</style>
