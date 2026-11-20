<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { BeichenModelUsage, BeichenUsageRange, BeichenUsageTotals } from '../api/types'
import { formatTokens, shiftDecimal } from '../utils/credits'
import { calculateCredits } from '../utils/format'
import AppDialog from './AppDialog.vue'

const props = defineProps<{
  open: boolean
  range: BeichenUsageRange
  usage?: BeichenModelUsage
  loading?: boolean
  error?: string
  initialModel?: string
}>()
const emit = defineEmits<{ 'update:open': [open: boolean]; 'update:range': [range: BeichenUsageRange]; retry: [] }>()

type UsageMetrics = {
  credits?: string | null
  tokens?: number | null
} & Partial<Pick<BeichenUsageTotals, 'pending_calls' | 'tokens_missing' | 'completeness'>>
type UsageRow = {
  id: string
  kind: 'model' | 'student' | 'key'
  name: string
  detail?: string
  metrics: UsageMetrics
  parentModel?: string
}

const ranges = [{ id: 'day', label: '当天' }, { id: 'month', label: '本月' }, { id: 'all', label: '总计' }] as const
const groups = [{ id: 'model', label: '按模型' }, { id: 'student', label: '按学生账号' }, { id: 'key', label: '按 Key' }] as const
const completenessLabels = { complete: '完整', partial: '部分缺失', missing: '缺失' }
const group = ref<UsageRow['kind']>('model')
const focusedModel = ref('')
const expandedModel = ref('')

watch(() => [props.open, props.initialModel] as const, ([open, model]) => {
  if (!open) return
  group.value = 'model'
  focusedModel.value = model ?? ''
  expandedModel.value = focusedModel.value.toLowerCase()
}, { immediate: true })

const period = computed(() => ranges.find(item => item.id === props.range)?.label ?? '本月')
const totals = computed(() => focusedModel.value
  ? props.usage?.models?.find(row => row.model.toLowerCase() === focusedModel.value.toLowerCase())
  : props.usage?.totals)
const expandedStudents = computed(() => expandedModel.value ? accounts(expandedModel.value) : [])
const columns = computed(() => ({ model: '模型', student: '学生账号', key: 'Key' })[group.value])
const rows = computed<UsageRow[]>(() => {
  if (group.value === 'student') return accounts(focusedModel.value)
  if (group.value === 'key') return (props.usage?.keys ?? []).map(key => ({
    id: key.api_key_id, kind: 'key', name: key.api_key_name,
    detail: [key.user_name, key.project_name].filter(Boolean).join(' · '), metrics: key,
  }))
  return (props.usage?.models ?? [])
    .filter(model => !focusedModel.value || model.model.toLowerCase() === focusedModel.value.toLowerCase())
    .flatMap(model => {
      const id = model.model.toLowerCase()
      const row: UsageRow = { id, kind: 'model', name: model.model, metrics: model }
      return id === expandedModel.value ? [row, ...expandedStudents.value] : [row]
    })
})

function sumCounts(left?: number | null, right?: number | null) {
  return left == null || right == null ? undefined : left + right
}

function accounts(model: string): UsageRow[] {
  const modelId = model.toLowerCase()
  const students = new Map<string, { name: string; projects: Set<string>; metrics: UsageMetrics }>()
  for (const student of props.usage?.students ?? []) {
    const metrics: UsageMetrics | undefined = modelId
      ? student.models?.find(detail => detail.model.toLowerCase() === modelId)
      : student
    if (!metrics) continue
    const account = students.get(student.user_id)
    if (!account) {
      students.set(student.user_id, {
        name: student.user_name || '未命名学生', projects: new Set([student.project_name]), metrics,
      })
      continue
    }
    account.projects.add(student.project_name)
    const previous = account.metrics
    const left = shiftDecimal(previous.credits, 0)
    const right = shiftDecimal(metrics.credits, 0)
    const completeness = previous.completeness ?? null
    const nextCompleteness = metrics.completeness ?? null
    account.metrics = {
      credits: left === '—' || right === '—' ? null : calculateCredits(left, right, 'add'),
      tokens: sumCounts(previous.tokens, metrics.tokens),
      pending_calls: sumCounts(previous.pending_calls, metrics.pending_calls),
      tokens_missing: sumCounts(previous.tokens_missing, metrics.tokens_missing),
      completeness: completeness === nextCompleteness ? completeness : 'partial',
    }
  }
  return Array.from(students, ([id, account]) => ({
    id: 'student-' + id, kind: 'student', name: account.name, metrics: account.metrics,
    detail: Array.from(account.projects).filter(Boolean).join(' · '), parentModel: modelId || undefined,
  }))
}

function selectGroup(next: UsageRow['kind']) {
  group.value = next
  if (next === 'key') {
    focusedModel.value = ''
    expandedModel.value = ''
  }
}

function completenessLabel(metrics?: UsageMetrics) {
  return (metrics?.completeness && completenessLabels[metrics.completeness]) || '待确认'
}

function tokenTotal(metrics?: UsageMetrics) {
  if (metrics?.completeness === 'missing' && metrics.tokens === 0) return '—'
  return formatTokens(metrics?.tokens)
}
</script>

<template>
  <AppDialog :open="open" title="北辰 · 用量明细" description="按模型或学生账号查看已扣积分、Token 和待核用量。" wide
    @update:open="emit('update:open', $event)">
    <section class="model-usage-panel beichen-stack" :aria-busy="loading">
      <div class="beichen-toolbar">
        <div class="range-switch" role="group" aria-label="用量时间范围">
          <button v-for="item in ranges" :key="item.id" type="button" :aria-pressed="range === item.id"
            @click="emit('update:range', item.id)">{{ item.label }}</button>
        </div>
        <button v-if="focusedModel" type="button" class="button ghost" @click="focusedModel = ''">查看全部模型</button>
      </div>
      <p v-if="loading" role="status">正在读取用量…</p>
      <div v-else-if="error" role="alert">
        <p>{{ error }}</p>
        <button type="button" class="button secondary" @click="emit('retry')">重试读取用量</button>
      </div>
      <template v-else-if="usage">
        <dl v-if="totals" class="usage-totals" aria-label="用量汇总">
          <div><dt>{{ period }}已扣积分</dt><dd>{{ shiftDecimal(totals.credits, 0) }}</dd></div>
          <div><dt>Token 总数</dt><dd>{{ tokenTotal(totals) }}</dd></div>
          <div><dt>完整性</dt><dd>{{ completenessLabel(totals) }}</dd></div>
          <div><dt>待核笔数</dt><dd>{{ formatTokens(totals.pending_calls) }}</dd></div>
        </dl>
        <p v-if="totals?.tokens_missing" class="beichen-note">待核 Token {{ formatTokens(totals.tokens_missing) }}</p>
        <div class="usage-groups" role="group" aria-label="用量分组">
          <button v-for="item in groups" :key="item.id" type="button" class="button ghost"
            :aria-pressed="group === item.id" @click="selectGroup(item.id)">{{ item.label }}</button>
        </div>
        <table v-if="rows.length" class="beichen-table model-usage-table">
          <caption class="sr-only">{{ period }}{{ columns }}用量</caption>
          <thead><tr>
            <th scope="col">{{ columns }}</th><th scope="col" class="numeric-cell">已扣积分</th>
            <th scope="col" class="numeric-cell">Token</th><th scope="col">完整性</th><th scope="col">待核</th>
          </tr></thead>
          <tbody>
            <template v-for="row in rows" :key="row.id">
              <tr :class="{ 'expanded-student': row.kind === 'student' && group === 'model' }">
                <th scope="row">
                  <button v-if="row.kind === 'model'" type="button" class="expand-model"
                    :aria-label="'查看 ' + row.name + ' 的学生用量'" :aria-expanded="expandedModel === row.id"
                    @click="expandedModel = expandedModel === row.id ? '' : row.id">{{ row.name }} <span aria-hidden="true">{{ expandedModel === row.id ? '▾' : '▸' }}</span></button>
                  <template v-else>
                    <span v-if="row.parentModel && group === 'model'" class="sr-only">学生账号 </span>{{ row.name }}
                    <small v-if="row.detail">{{ row.detail }}</small>
                  </template>
                </th>
                <td class="numeric-cell" data-label="已扣积分">{{ shiftDecimal(row.metrics.credits, 0) }}</td>
                <td class="numeric-cell" data-label="Token">{{ tokenTotal(row.metrics) }}</td>
                <td data-label="完整性" :data-completeness="row.metrics.completeness">{{ completenessLabel(row.metrics) }}</td>
                <td data-label="待核">
                  {{ row.metrics.pending_calls == null ? '—' : formatTokens(row.metrics.pending_calls) + ' 笔' }}
                  <small v-if="row.metrics.tokens_missing">待核 Token {{ formatTokens(row.metrics.tokens_missing) }}</small>
                </td>
              </tr>
              <tr v-if="row.kind === 'model' && expandedModel === row.id && !expandedStudents.length">
                <td colspan="5" class="beichen-note">暂无该模型的学生用量。</td>
              </tr>
            </template>
          </tbody>
        </table>
        <p v-else class="beichen-empty">{{ focusedModel ? focusedModel + ' 暂无此时段的用量' : '此时段暂无用量' }}</p>
      </template>
      <div v-else class="beichen-empty">
        <strong>暂无可显示的用量</strong>
        <button type="button" class="button ghost" @click="emit('retry')">读取用量</button>
      </div>
    </section>
  </AppDialog>
</template>

<style scoped>
.model-usage-panel { overflow-wrap: anywhere; }
.usage-totals { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin: 0; }
.usage-totals > div { border: 1px solid var(--line); border-radius: 12px; padding: 10px 12px; min-width: 0; }
.usage-totals dt, .model-usage-table small { color: var(--ink-soft); font-size: 11px; font-weight: 400; }
.usage-totals dd { margin: 4px 0 0; font-weight: 700; font-variant-numeric: tabular-nums; }
.usage-groups { display: flex; flex-wrap: wrap; gap: 6px; }
.usage-groups button[aria-pressed="true"] { background: var(--primary-soft); color: var(--primary-strong); }
.model-usage-table { table-layout: fixed; }
.model-usage-table thead th:first-child { width: 32%; }
.model-usage-table thead th, .model-usage-table .numeric-cell { white-space: normal; }
.model-usage-table td, .model-usage-table th { overflow-wrap: anywhere; min-width: 0; }
.model-usage-table small { display: block; margin-top: 4px; }
.model-usage-table .expanded-student { background: var(--primary-soft); }
.model-usage-table .expanded-student th { padding-inline-start: 22px; }
.expand-model { padding: 0; border: 0; background: transparent; color: var(--primary-strong); text-align: left; font-weight: 700; overflow-wrap: anywhere; }
.expand-model:hover { text-decoration: underline; text-underline-offset: 3px; }
[data-completeness="partial"], [data-completeness="missing"] { color: #835615; }
@media (max-width: 900px) {
  .model-usage-table tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 10px; }
  .model-usage-table th, .model-usage-table td { padding: 4px 8px; }
  .model-usage-table tbody th, .model-usage-table td[colspan] { grid-column: 1 / -1; }
  .model-usage-table [data-label]::before { content: attr(data-label); display: block; font-size: 11px; color: var(--ink-soft); }
}
@media (max-width: 560px) {
  .usage-totals { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
