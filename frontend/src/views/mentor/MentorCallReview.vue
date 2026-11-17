<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { mentorAPI } from '../../api/mentor'
import type { CallTurn } from '../../api/types'
import { APIError } from '../../api/client'
import AppDialog from '../../components/AppDialog.vue'
import EmptyState from '../../components/EmptyState.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import MentorUsageFilters, { type MentorResourceFilter } from '../../components/MentorUsageFilters.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'

const rows = ref<CallTurn[]>([])
const selected = ref<CallTurn | null>(null)
const nextCursor = ref<string | null>(null)
const error = ref('')
const route = useRoute()
const filter = reactive<MentorResourceFilter & { q: string; start: string; end: string }>({
  q: '',
  project_id: '',
  user_id: '',
  api_key_id: '',
  model_id: '',
  status: '',
  start: '',
  end: '',
})
const statusOptions = [
  { value: 'succeeded', label: '成功' },
  { value: 'upstream_failed', label: '上游失败' },
  { value: 'outcome_unknown', label: '结果未知' },
]

function query(cursor = '') {
  const entries: Array<[string, string]> = Object.entries(filter)
    .filter(([, value]) => value)
    .map(([key, value]) => [key, (key === 'start' || key === 'end') ? new Date(value).toISOString() : value])
  const params = new URLSearchParams(entries)
  if (cursor) params.set('cursor', cursor)
  return params.toString()
}

function formatTokens(value: number) {
  return value > 0 ? value.toLocaleString() : '—'
}

async function load(append = false) {
  try {
    const page = await mentorAPI.callTurns(query(append ? nextCursor.value ?? '' : ''))
    rows.value = append ? rows.value.concat(page.items) : page.items
    nextCursor.value = page.next_cursor ?? null
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载调用审查失败'
  }
}

async function detail(id: string) {
  try {
    selected.value = await mentorAPI.callTurn(id)
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载任务详情失败'
  }
}

useVisibilityPolling(() => load())

onMounted(() => {
  if (typeof route.query.project_id === 'string') filter.project_id = route.query.project_id
  if (typeof route.query.api_key_id === 'string') filter.api_key_id = route.query.api_key_id
  if (typeof route.query.user_id === 'string') filter.user_id = route.query.user_id
})
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">MENTOR</p>
        <h1>调用审查</h1>
      </div>
      <button class="button secondary" @click="load()">刷新</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel">
      <MentorUsageFilters :filter="filter" :status-options="statusOptions" @error="error = $event" />
      <div class="filter-grid">
        <label>开始时间<input v-model="filter.start" type="datetime-local"></label>
        <label>结束时间<input v-model="filter.end" type="datetime-local"></label>
      </div>
      <button class="button primary" @click="load()">查询</button>
    </section>
    <section class="panel">
      <table v-if="rows.length" class="usage-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>用户 / Key</th>
            <th>项目 / 模型</th>
            <th>用户 prompt</th>
            <th>模型调用</th>
            <th>Token</th>
            <th>结果</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.id">
            <td>{{ new Date(row.started_at).toLocaleString() }}</td>
            <td>{{ row.user_name }}<br><span class="muted">{{ row.api_key_name }}</span></td>
            <td>{{ row.project_name }}<br><span class="muted">{{ row.model_name }}</span></td>
            <td class="prompt-cell">{{ row.root_prompt_preview || '—' }}<br><span class="muted">{{ row.root_prompt_bytes }} bytes</span></td>
            <td><strong>{{ row.call_count }}</strong> 次<br><span class="muted">{{ row.credits }} credits</span></td>
            <td>{{ formatTokens(row.total_tokens) }}<br><span class="muted">in {{ formatTokens(row.prompt_tokens) }} / out {{ formatTokens(row.completion_tokens) }}</span></td>
            <td><StatusBadge :status="row.outcome" /></td>
            <td><button class="button ghost" @click="detail(row.id)">详情</button></td>
          </tr>
        </tbody>
      </table>
      <EmptyState v-else title="暂无调用任务" />
      <button v-if="nextCursor" class="button secondary wide" @click="load(true)">加载更多</button>
    </section>
    <AppDialog :open="Boolean(selected)" title="任务详情" wide @update:open="selected = $event ? selected : null">
      <template v-if="selected">
        <div class="usage-summary">
          <div class="usage-stat">模型调用<strong>{{ selected.call_count }} 次</strong></div>
          <div class="usage-stat">credits<strong>{{ selected.credits }}</strong></div>
          <div class="usage-stat">total tokens<strong>{{ formatTokens(selected.total_tokens) }}</strong></div>
        </div>
        <p>{{ selected.user_name }} · {{ selected.project_name }} · {{ selected.model_name }}</p>
        <h3>用户 prompt</h3>
        <pre class="sensitive-text">{{ selected.root_prompt || '—' }}</pre>
        <h3>每次模型调用</h3>
        <table class="usage-table">
          <thead>
            <tr>
              <th>#</th>
              <th>时间</th>
              <th>模型</th>
              <th>路径</th>
              <th>Token</th>
              <th>credits</th>
              <th>结果</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="call in selected.calls || []" :key="call.id">
              <td>{{ call.call_index || '—' }}</td>
              <td>{{ new Date(call.created_at).toLocaleString() }}</td>
              <td>{{ call.model_name || '—' }}</td>
              <td>{{ call.path }}</td>
              <td>{{ formatTokens(call.total_tokens ?? 0) }}</td>
              <td>{{ call.credits }}</td>
              <td><StatusBadge :status="call.outcome" /></td>
            </tr>
          </tbody>
        </table>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.prompt-cell {
  max-width: 280px;
  white-space: normal;
}
</style>
