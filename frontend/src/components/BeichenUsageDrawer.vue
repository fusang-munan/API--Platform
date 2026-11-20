<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import type { BeichenModelUsage, BeichenUsageRange, BeichenUsageTotals } from '../api/types'
import { formatCredits, formatPercent, formatTokens } from '../utils/credits'
import AppDialog from './AppDialog.vue'

const props = defineProps<{
  open: boolean; range: BeichenUsageRange; usage?: BeichenModelUsage; loading?: boolean; error?: string; initialModel?: string
}>()
const emit = defineEmits<{ 'update:open': [open: boolean]; 'update:range': [range: BeichenUsageRange]; retry: [] }>()
type Tab = 'models' | 'students' | 'keys'
const tabs: Array<{ id: Tab; label: string }> = [{ id: 'models', label: '按模型' }, { id: 'students', label: '按学生' }, { id: 'keys', label: '按 Key' }]
const ranges: Array<{ id: BeichenUsageRange; label: string }> = [{ id: 'day', label: '当天' }, { id: 'month', label: '本月' }, { id: 'all', label: '总计' }]
const tab = ref<Tab>('models')
const model = ref('')
const panelId = useId()
watch(() => [props.open, props.initialModel] as const, () => {
  if (!props.open) return
  model.value = props.initialModel ?? ''
  tab.value = model.value ? 'students' : 'models'
}, { immediate: true })
const matches = (name: string) => name.toLowerCase() === model.value.toLowerCase()
const selectedModel = computed(() => model.value ? props.usage?.models.find(row => matches(row.model)) : undefined)
const students = computed(() => (props.usage?.students ?? []).flatMap(student => {
  if (!model.value) return [student]
  const detail = student.models.find(row => matches(row.model))
  return detail ? [{ ...student, credits: detail.credits, tokens: detail.tokens, calls: detail.calls }] : []
}))
const totals = computed<BeichenUsageTotals>(() => {
  const empty: BeichenUsageTotals = { credits: '0', tokens: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, calls: 0, pending_calls: 0 }
  if (model.value) return { ...(selectedModel.value ?? empty), students: new Set(students.value.map(row => row.user_id)).size }
  return { ...(props.usage?.totals ?? empty), students: props.usage?.totals.students ?? new Set(students.value.map(row => row.user_id)).size }
})
const rows = computed(() => {
  if (tab.value === 'students') return students.value.map(student => ({
    id: student.user_id + ':' + student.project_id, name: student.user_name, detail: student.project_name,
    credits: student.credits, tokens: student.tokens, calls: student.calls, pending: undefined,
  }))
  if (tab.value === 'keys') return (props.usage?.keys ?? []).map(key => ({
    id: key.api_key_id, name: key.api_key_name, detail: [key.user_name, key.project_name].filter(Boolean).join(' · '),
    credits: key.credits, tokens: key.tokens, calls: key.calls, pending: undefined,
  }))
  return (props.usage?.models ?? []).filter(row => !model.value || matches(row.model)).map(row => ({
    id: row.model, name: row.model, detail: '', credits: row.credits, tokens: row.tokens, calls: row.calls, pending: row.pending_calls,
  }))
})
function share(credits: string) {
  const denominator = Number(totals.value.credits)
  const ratio = denominator > 0 ? Number(credits) / denominator : 0
  return Number.isFinite(ratio) ? Math.max(0, Math.min(1, ratio)) : 0
}
function selectTab(next: Tab) {
  tab.value = next
  // 当前接口的 Key 用量没有模型维度，切到 Key 时必须恢复全部模型，防止误归属。
  if (next === 'keys') model.value = ''
}
function tabKey(event: KeyboardEvent, index: number) {
  const offset = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0
  if (!offset && event.key !== 'Home' && event.key !== 'End') return
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + offset + tabs.length) % tabs.length
  selectTab(tabs[next].id)
  document.getElementById(panelId + '-' + tabs[next].id)?.focus()
}
function inspectModel(name: string) {
  model.value = name
  tab.value = 'students'
}
</script>

<template>
  <AppDialog :open="open" title="北辰 · 用量总览" description="按模型、学生和学校 Key 核对实际积分与 Token。" wide @update:open="emit('update:open', $event)">
    <section class="usage-drawer beichen-stack" :aria-busy="loading">
      <div class="beichen-toolbar">
        <div class="range-switch" role="group" aria-label="用量时间范围">
          <button v-for="item in ranges" :key="item.id" type="button" :aria-pressed="range === item.id" @click="emit('update:range', item.id)">{{ item.label }}</button>
        </div>
        <label class="model-filter">模型
          <select v-model="model" :disabled="tab === 'keys' || loading" aria-label="筛选用量模型">
            <option value="">全部模型</option>
            <option v-if="model && !usage?.models.some(row => row.model === model)" :value="model">{{ model }}</option>
            <option v-for="item in usage?.models ?? []" :key="item.model" :value="item.model">{{ item.model }}</option>
          </select>
        </label>
      </div>
      <p class="beichen-note">北京时间 · {{ range === 'all' ? '全部已记录业务数据' : range === 'day' ? '当天' : '当前自然月' }}。积分和 Token 分别来自实际记账与实际用量，不能按固定比例互换。</p>
      <div v-if="error" class="usage-error" role="alert"><p>{{ error }}</p><button type="button" class="button secondary" :disabled="loading" @click="emit('retry')">重试读取用量</button></div>
      <div v-else-if="loading" class="usage-skeleton" role="status">
        <span class="sr-only">正在读取用量…</span><i v-for="index in 4" :key="index" aria-hidden="true" />
      </div>
      <template v-else-if="usage">
        <div class="beichen-summary" aria-label="用量汇总">
          <div class="beichen-stat"><span>已扣积分</span><strong>{{ formatCredits(totals.credits, 6) }}</strong><small>积分</small></div>
          <div class="beichen-stat"><span>总 Token</span><strong>{{ formatTokens(totals.tokens) }}</strong><small>Token</small></div>
          <div class="beichen-stat"><span>调用数</span><strong>{{ formatTokens(totals.calls) }}</strong><small>次调用</small></div>
          <div class="beichen-stat"><span>使用学生</span><strong>{{ formatTokens(totals.students) }}</strong><small>人 · 按账号去重</small></div>
        </div>
        <div class="token-breakdown">
          <p class="beichen-note">总 Token = 未缓存输入 + 输出 + 缓存读取 + 缓存写入</p>
          <dl><div><dt>未缓存输入</dt><dd>{{ formatTokens(totals.input_tokens) }}</dd></div><div><dt>输出</dt><dd>{{ formatTokens(totals.output_tokens) }}</dd></div><div><dt>缓存读取</dt><dd>{{ formatTokens(totals.cache_read_tokens) }}</dd></div><div><dt>缓存写入</dt><dd>{{ formatTokens(totals.cache_write_tokens) }}</dd></div></dl>
        </div>
        <p v-if="totals.pending_calls" class="pending-note" role="status">待核账 {{ formatTokens(totals.pending_calls) }} 笔，单独计数，尚未计入已扣积分。</p>
        <div class="usage-tabs" role="tablist" aria-label="用量分组">
          <button v-for="(item, index) in tabs" :id="panelId + '-' + item.id" :key="item.id" type="button" role="tab"
            :aria-selected="tab === item.id" :tabindex="tab === item.id ? 0 : -1" :aria-controls="panelId + '-table'"
            @click="selectTab(item.id)" @keydown="tabKey($event, index)">{{ item.label }}</button>
        </div>
        <p v-if="tab === 'keys'" class="beichen-note" role="status">每把 Key 显示当前时间范围的全部模型合计；查看单个模型的学生用量，请切换“按学生”。</p>
        <div :id="panelId + '-table'" role="tabpanel" :aria-labelledby="panelId + '-' + tab" class="usage-panel">
          <table v-if="rows.length" class="beichen-table usage-table">
            <caption class="sr-only">{{ tabs.find(item => item.id === tab)?.label }}的积分与 Token 用量</caption>
            <thead><tr><th scope="col">{{ tab === 'models' ? '模型' : tab === 'students' ? '学生 / 项目' : 'Key / 学生' }}</th><th scope="col" class="numeric-cell">已扣积分</th><th scope="col" class="numeric-cell">Token</th><th scope="col" class="numeric-cell">调用</th><th scope="col">积分占比</th></tr></thead>
            <tbody><tr v-for="row in rows" :key="row.id">
              <th scope="row"><button v-if="tab === 'models'" type="button" class="usage-link" :aria-label="'查看 ' + row.name + ' 的学生用量'" @click="inspectModel(row.name)">{{ row.name }}</button><strong v-else>{{ row.name }}</strong><small v-if="row.detail">{{ row.detail }}</small><small v-if="row.pending">待核账 {{ formatTokens(row.pending) }} 笔</small></th>
              <td class="numeric-cell" data-label="已扣积分">{{ formatCredits(row.credits, 6) }} <span>积分</span></td>
              <td class="numeric-cell" data-label="Token">{{ formatTokens(row.tokens) }}</td>
              <td class="numeric-cell" data-label="调用">{{ formatTokens(row.calls) }}</td>
              <td data-label="积分占比"><span>{{ formatPercent(share(row.credits)) }}</span><div class="usage-share" aria-hidden="true"><i :style="{ width: share(row.credits) * 100 + '%' }" /></div></td>
            </tr></tbody>
          </table>
          <div v-else class="beichen-empty"><strong>此范围暂无用量</strong><span>{{ model ? '该模型还没有已记录的使用数据，可切换全部模型查看。' : '学生开始调用后，会在这里按实际使用记录汇总。' }}</span></div>
        </div>
      </template>
      <div v-else class="beichen-empty"><strong>暂无可显示的用量</strong><button type="button" class="button ghost" @click="emit('retry')">读取用量</button></div>
    </section>
  </AppDialog>
</template>

<style scoped>
.usage-drawer, .usage-panel { min-width: 0; }
.model-filter { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 0 1 320px; }
.model-filter select { margin: 0; min-width: 0; flex: 1; }
.token-breakdown { border-bottom: 1px solid var(--line); padding-bottom: 12px; }
.token-breakdown dl { display: flex; gap: 12px 24px; flex-wrap: wrap; margin: 8px 0 0; }
.token-breakdown dl div { display: flex; gap: 8px; font-size: 12px; }
.token-breakdown dt { color: var(--ink-soft); }
.token-breakdown dd { margin: 0; font-variant-numeric: tabular-nums; }
.pending-note { padding: 10px 12px; border-radius: 10px; background: #fff4dd; color: #835615; margin: 0; font-size: 12px; }
.usage-tabs { display: flex; gap: 8px; border-bottom: 1px solid var(--line); }
.usage-tabs button { border: 0; border-bottom: 2px solid transparent; padding: 10px 12px; background: transparent; color: var(--ink-soft); font-size: 13px; font-weight: 700; }
.usage-tabs button[aria-selected="true"] { border-bottom-color: var(--primary); color: var(--primary-strong); }
.usage-table { table-layout: fixed; }
.usage-table th:first-child { width: 30%; }
.usage-table td, .usage-table th { overflow-wrap: anywhere; }
.usage-table .numeric-cell { white-space: normal; }
.usage-table small { display: block; margin-top: 5px; color: var(--ink-soft); font-size: 12px; font-weight: 400; }
.usage-link { padding: 0; text-align: left; border: 0; background: transparent; color: var(--primary-strong); font-weight: 700; overflow-wrap: anywhere; }
.usage-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.usage-share { margin-top: 7px; }
.usage-share i { background: var(--primary); }
.usage-skeleton { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.usage-skeleton i { display: block; height: 90px; background: #edf0f7; border-radius: 12px; }
.usage-error p { margin-top: 0; }
button:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@media (max-width: 900px) {
  .usage-table tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  .usage-table th:first-child { width: 100%; grid-column: 1 / -1; }
  .usage-table td { min-width: 0; }
  .usage-table td::before { content: attr(data-label); display: block; color: var(--ink-soft); font-size: 11px; margin-bottom: 4px; }
}
@media (max-width: 560px) {
  .model-filter { flex-basis: 100%; }
  .usage-tabs button { flex: 1; padding-inline: 8px; }
}
</style>
