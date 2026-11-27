<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { BeichenModelUsage, BeichenUsageRange } from '../api/types'
import {
  schoolUsageCredits, schoolUsageModelLabel, schoolUsageModelLookup, schoolUsageModelReference,
  schoolUsagePendingNote, schoolUsageSettlementNote, schoolUsageTokenNote, schoolUsageTokens,
  usageDispatchedCalls, usagePendingReviewCalls, type SchoolUsageMetrics,
} from '../utils/beichenModels'
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
  initialModelKey?: string
  initialModelLabel?: string
}>()
const emit = defineEmits<{ 'update:open': [open: boolean]; 'update:range': [range: BeichenUsageRange]; retry: [] }>()

type UsageRow = {
  id: string
  kind: 'model' | 'student' | 'key'
  name: string
  detail?: string
  metrics: SchoolUsageMetrics
  parentModel?: string
}

const ranges = [{ id: 'day', label: '当天' }, { id: 'month', label: '本月' }, { id: 'all', label: '总计' }] as const
const groups = [{ id: 'model', label: '按模型' }, { id: 'student', label: '按学生账号' }, { id: 'key', label: '按 Key' }] as const
const completenessLabels = { complete: '完整', partial: '部分缺失', missing: '缺失' }
const group = ref<UsageRow['kind']>('model')
const focusedModel = ref('')
const focusedModelLabel = ref('')
const expandedModel = ref('')

watch(() => [props.open, props.initialModel, props.initialModelKey, props.initialModelLabel] as const,
  ([open, model, key, label]) => {
  if (!open) return
  group.value = 'model'
  focusedModel.value = key || model || ''
  focusedModelLabel.value = label || model || focusedModel.value
  expandedModel.value = focusedModel.value.toLowerCase()
}, { immediate: true })

const period = computed(() => ranges.find(item => item.id === props.range)?.label ?? '本月')
const focusedLookup = computed(() => schoolUsageModelLookup(props.usage?.models, focusedModel.value, focusedModelLabel.value))
const identityAmbiguous = computed(() => Boolean(focusedModel.value && focusedLookup.value.ambiguous))
const totals = computed(() => focusedModel.value ? focusedLookup.value.model : props.usage?.totals)
const noCalls = computed(() => Boolean(props.usage && !identityAmbiguous.value &&
  (focusedModel.value && !totals.value ||
    usageDispatchedCalls(totals.value) === 0 && !usagePendingReviewCalls(totals.value))))
const noCallsLabel = computed(() => props.range === 'all' ? '所选时段无校内调用' : period.value + '无校内调用')
const keyBreakdownIncomplete = computed(() => Boolean(group.value === 'key' && focusedModel.value && !identityAmbiguous.value &&
  props.usage?.keys?.some(key => key.models == null)))
const expandedStudents = computed(() => expandedModel.value ? accounts(expandedModel.value) : [])
const columns = computed(() => ({ model: '模型', student: '学生账号', key: 'Key' })[group.value])
const rows = computed<UsageRow[]>(() => {
  if (group.value === 'student') return accounts(focusedModel.value)
  if (group.value === 'key') {
    return (props.usage?.keys ?? []).flatMap(key => {
      const metrics = focusedModel.value
        ? schoolUsageModelLookup(key.models, focusedModel.value, focusedModelLabel.value).model : key
      if (!metrics) return []
      return [{
        id: key.api_key_id, kind: 'key', name: key.api_key_name,
        detail: [key.user_name, key.project_name].filter(Boolean).join(' · '), metrics,
      }]
    })
  }
  const models = focusedModel.value ? (focusedLookup.value.model ? [focusedLookup.value.model] : []) : props.usage?.models ?? []
  return models
    .flatMap(model => {
      const id = schoolUsageModelReference(model)
      const row: UsageRow = { id, kind: 'model', name: schoolUsageModelLabel(model), metrics: model }
      return id === expandedModel.value ? [row, ...expandedStudents.value] : [row]
    })
})

function sumCounts(left?: number | null, right?: number | null) {
  return left == null || right == null ? undefined : left + right
}

function accounts(model: string): UsageRow[] {
  const students = new Map<string, { name: string; projects: Set<string>; metrics: SchoolUsageMetrics }>()
  for (const student of props.usage?.students ?? []) {
    const metrics: SchoolUsageMetrics | undefined = model
      ? schoolUsageModelLookup(student.models, model, focusedModelLabel.value).model
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
      calls: sumCounts(previous.calls, metrics.calls),
      dispatched_calls: sumCounts(previous.dispatched_calls, metrics.dispatched_calls),
      settled_calls: sumCounts(previous.settled_calls, metrics.settled_calls),
      zero_cost_calls: sumCounts(previous.zero_cost_calls, metrics.zero_cost_calls),
      pending_calls: sumCounts(previous.pending_calls, metrics.pending_calls),
      pending_review_calls: sumCounts(previous.pending_review_calls, metrics.pending_review_calls),
      pending_review_credits: previous.pending_review_credits == null || metrics.pending_review_credits == null
        ? undefined : calculateCredits(previous.pending_review_credits, metrics.pending_review_credits, 'add'),
      token_missing_calls: sumCounts(previous.token_missing_calls, metrics.token_missing_calls),
      tokens_missing: sumCounts(previous.tokens_missing, metrics.tokens_missing),
      completeness: completeness === nextCompleteness ? completeness : 'partial',
    }
  }
  return Array.from(students, ([id, account]) => ({
    id: 'student-' + id, kind: 'student', name: account.name, metrics: account.metrics,
    detail: Array.from(account.projects).filter(Boolean).join(' · '), parentModel: model || undefined,
  }))
}

function completenessLabel(metrics?: SchoolUsageMetrics) {
  return (metrics?.completeness && completenessLabels[metrics.completeness]) || '待确认'
}

</script>

<template>
  <AppDialog :open="open" title="北辰 · 用量明细" description="按模型、学生账号或 Key 查看校内网关用量。" wide
    @update:open="emit('update:open', $event)">
    <section class="model-usage-panel beichen-stack" :aria-busy="loading">
      <div class="beichen-toolbar">
        <div class="range-switch" role="group" aria-label="用量时间范围">
          <button v-for="item in ranges" :key="item.id" type="button" :aria-pressed="range === item.id"
            @click="emit('update:range', item.id)">{{ item.label }}</button>
        </div>
        <div v-if="focusedModel" class="beichen-toolbar">
          <span>模型 {{ focusedModelLabel }}</span>
          <button type="button" class="button ghost" @click="focusedModel = ''; focusedModelLabel = ''">查看全部模型</button>
        </div>
      </div>
      <p v-if="loading" role="status">正在读取用量…</p>
      <div v-else-if="error" role="alert">
        <p>{{ error }}</p>
        <button type="button" class="button secondary" @click="emit('retry')">重试读取用量</button>
      </div>
      <template v-else-if="usage">
        <dl v-if="totals && !noCalls" class="usage-totals" aria-label="用量汇总">
          <div>
            <dt>{{ period }}已扣积分</dt>
            <dd>{{ schoolUsageCredits(totals) }}<small v-if="schoolUsageSettlementNote(totals)">{{ schoolUsageSettlementNote(totals) }}</small></dd>
          </div>
          <div>
            <dt>Token 总数</dt>
            <dd>{{ schoolUsageTokens(totals) }}<small v-if="schoolUsageTokenNote(totals)">{{ schoolUsageTokenNote(totals) }}</small></dd>
          </div>
          <div><dt>完整性</dt><dd>{{ completenessLabel(totals) }}</dd></div>
          <div>
            <dt>待核笔数</dt>
            <dd>{{ formatTokens(usagePendingReviewCalls(totals)) }}<small v-if="schoolUsagePendingNote(totals)">{{ schoolUsagePendingNote(totals) }}</small></dd>
          </div>
        </dl>
        <div class="usage-groups" role="group" aria-label="用量分组">
          <button v-for="item in groups" :key="item.id" type="button" class="button ghost"
            :aria-pressed="group === item.id" @click="group = item.id">{{ item.label }}</button>
        </div>
        <p v-if="identityAmbiguous" class="beichen-empty" data-testid="usage-identity-ambiguous">
          同名模型缺少校内身份，用量待确认
        </p>
        <p v-else-if="noCalls" class="beichen-empty">{{ noCallsLabel }}</p>
        <p v-else-if="keyBreakdownIncomplete" class="beichen-note">部分 Key 的分型号明细未回报。</p>
        <table v-if="!noCalls && rows.length" class="beichen-table model-usage-table">
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
                <td class="numeric-cell" data-label="已扣积分">
                  {{ schoolUsageCredits(row.metrics) }}
                  <small v-if="schoolUsageSettlementNote(row.metrics)">{{ schoolUsageSettlementNote(row.metrics) }}</small>
                </td>
                <td class="numeric-cell" data-label="Token">
                  {{ schoolUsageTokens(row.metrics) }}
                  <small v-if="schoolUsageTokenNote(row.metrics)">{{ schoolUsageTokenNote(row.metrics) }}</small>
                </td>
                <td data-label="完整性" :data-completeness="row.metrics.completeness">{{ completenessLabel(row.metrics) }}</td>
                <td data-label="待核">
                  {{ usagePendingReviewCalls(row.metrics) == null ? '—' : formatTokens(usagePendingReviewCalls(row.metrics)) + ' 笔' }}
                  <small v-if="schoolUsagePendingNote(row.metrics)">{{ schoolUsagePendingNote(row.metrics) }}</small>
                </td>
              </tr>
              <tr v-if="row.kind === 'model' && expandedModel === row.id && !expandedStudents.length">
                <td colspan="5" class="beichen-note">暂无该模型的学生用量。</td>
              </tr>
            </template>
          </tbody>
        </table>
        <p v-else-if="!noCalls && !keyBreakdownIncomplete" class="beichen-empty">{{ focusedModel ? '暂无该模型的分组明细' : '此时段暂无用量' }}</p>
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
