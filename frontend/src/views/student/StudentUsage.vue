<script setup lang="ts">
import { ref } from 'vue'
import { studentAPI } from '../../api/student'
import type { StudentProjectUsage } from '../../api/types'
import { APIError } from '../../api/client'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'

const usage = ref<Awaited<ReturnType<typeof studentAPI.usage>> | null>(null)
const month = ref(new Date().toISOString().slice(0, 7))
const error = ref('')

async function load() {
  try {
    usage.value = await studentAPI.usage(month.value)
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载个人用量失败'
  }
}

const { refresh } = useVisibilityPolling(load)

function percent(used: string, quota: string) {
  return Number(quota) === 0 ? null : Number(used) / Number(quota) * 100
}

function percentLabel(used: string, quota: string) {
  const value = percent(used, quota)
  return value === null ? 'N/A' : `${value.toFixed(1)}%`
}

function remaining(project: StudentProjectUsage) {
  return project.available
}

function projectSegments(project: StudentProjectUsage) {
  const keyUsage = project.keys.reduce((sum, key) => sum + Number(key.used), 0)
  return [
    ...project.keys.map(key => ({ name: key.name, value: Number(key.used) })).filter(item => item.value > 0),
    { name: '剩余额度', value: Number(remaining(project)), color: '#dfe3ec' },
  ].filter(item => item.value > 0 || item.name === '剩余额度')
}
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">STUDENT</p>
        <h1>个人用量</h1>
      </div>
      <label>月份<input v-model="month" type="month" @change="refresh"></label>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <div v-if="usage?.projects.length" class="grid two">
      <article v-for="project in usage.projects" :key="project.project_id" class="panel">
        <div class="row">
          <div>
            <h2>{{ project.project_name }}</h2>
            <p class="muted">月额度 {{ project.quota }} · 已用 {{ project.used }} · 在途 {{ project.pending }}</p>
          </div>
          <strong>{{ percentLabel(project.used, project.quota) }}</strong>
        </div>
        <div class="usage-chart-card">
          <CreditDonut
            :segments="projectSegments(project)"
            :total="Number(project.quota)"
            :label="`${project.project_name} 各 Key 用量与剩余额度`"
          />
        </div>
        <div class="usage-summary">
          <div class="usage-stat">已用<strong>{{ project.used }}</strong></div>
          <div class="usage-stat">在途<strong>{{ project.pending }}</strong></div>
          <div class="usage-stat">剩余<strong>{{ project.available }}</strong></div>
        </div>
        <section v-if="project.keys.length" class="member-key-stack">
          <article v-for="key in project.keys" :key="key.id" class="member-key-row">
            <strong>{{ key.name }}</strong>
            <span class="muted">{{ key.status }} · 已用 {{ key.used }} · 在途 {{ key.pending }}</span>
          </article>
        </section>
      </article>
    </div>
    <EmptyState v-else title="本月暂无可展示的额度" />
  </section>
</template>
