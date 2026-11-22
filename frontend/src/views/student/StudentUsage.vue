<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { studentAPI } from '../../api/student'
import { APIError, get } from '../../api/client'
import type { KeyUsage, StudentProjectUsage, StudentUsage, UsageSlice } from '../../api/types'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { formatPercent, formatTokens, shiftDecimal } from '../../utils/credits'
import { calculateCredits, fullTime, quotaBalance, schoolMonth, statusLabel } from '../../utils/format'

type UsageRange = 'day' | 'month' | 'all'
type RecentRange = 'today' | 'month' | 'all'
type ModelUsageSlice = UsageSlice & {
  tokens?: number | null
  input_tokens?: number | null
  output_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  cache_hit_rate?: number | null
  tokens_missing?: number | null
  completeness?: 'complete' | 'partial' | 'missing' | null
}
type StudentKeyUsage = Omit<KeyUsage, 'models'> & { models: ModelUsageSlice[] }
type StudentProjectUsageView = Omit<StudentProjectUsage, 'keys'> & { keys: StudentKeyUsage[] }
type StudentUsageView = Omit<StudentUsage, 'projects'> & { projects: StudentProjectUsageView[] }
type StudentCallReceipt = {
  request_id: string
  model_id: string
  channel: string
  billing_state: string
  charged_credits: string | null
  receipt_applied: boolean | null
  created_at?: string | null
  input_tokens?: number | null
  output_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
}
type ModelRow = {
  id: string
  modelName: string
  modelId: string
  projectName: string
  keyName: string
  model: ModelUsageSlice
}
type StudentRecentCall = {
  id: string
  request_id: string
  model_id: string
  model_name: string
  project_name: string
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  credits: string
  status: string
  billing_state: string
  created_at: string
}
type StudentRecentPage = { items: StudentRecentCall[]; next_cursor: string | null }

const ranges: Array<{ id: UsageRange; label: string }> = [
  { id: 'day', label: '今天' },
  { id: 'month', label: '本月' },
  { id: 'all', label: '全部' },
]
const creditRateNote = '100 积分 = 1 元；金额统一用积分显示。'
const recentRanges: Array<{ id: RecentRange; label: string }> = [
  { id: 'today', label: '今天' },
  { id: 'month', label: '本月' },
  { id: 'all', label: '全部' },
]

const usage = ref<StudentUsageView | null>(null)
const month = ref(schoolMonth())
const range = ref<UsageRange>('month')
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const receiptQuery = ref('')
const receipt = ref<StudentCallReceipt | null>(null)
const receiptLoading = ref(false)
const receiptError = ref('')
const recentRange = ref<RecentRange>('today')
const recentItems = ref<StudentRecentCall[]>([])
const recentCursor = ref<string | null>(null)
const recentLoading = ref(false)
const recentLoaded = ref(false)
const recentError = ref('')
let recentRequest = 0

async function load() {
  loading.value = true
  try {
    usage.value = await studentAPI.usage(month.value) as StudentUsageView
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载个人用量失败'
  } finally {
    loading.value = false
    loaded.value = true
  }
}

const { refresh } = useVisibilityPolling(load)

async function loadRecent(reset = false) {
  const request = ++recentRequest
  recentLoading.value = true
  if (reset) {
    recentItems.value = []
    recentCursor.value = null
    recentLoaded.value = false
  }
  try {
    const params = new URLSearchParams({ range: recentRange.value, limit: '20' })
    if (!reset && recentCursor.value) params.set('cursor', recentCursor.value)
    const page = await get<StudentRecentPage>(`/api/v1/student/usage/recent?${params.toString()}`)
    if (request !== recentRequest) return
    const items = page.items ?? []
    recentItems.value = reset ? items : [...recentItems.value, ...items]
    recentCursor.value = page.next_cursor ?? null
    recentError.value = ''
  } catch (caught) {
    if (request === recentRequest) {
      recentError.value = caught instanceof APIError ? caught.message : '加载最近调用失败'
    }
  } finally {
    if (request === recentRequest) {
      recentLoading.value = false
      recentLoaded.value = true
    }
  }
}

function changeRecentRange(value: RecentRange) {
  if (recentRange.value === value) return
  recentRange.value = value
  void loadRecent(true)
}

const selectedRange = computed(() => ranges.find(item => item.id === range.value) ?? ranges[1])

function creditText(value?: string | number | null) {
  if (value == null || value === '') return '—'
  const text = String(value).trim()
  if (!/^[+-]?\d+(?:\.\d+)?$/.test(text)) return '—'
  if (/^[+-]?0+(?:\.0+)?$/.test(text)) return '0'
  return shiftDecimal(text, 0)
}

function sumProjectField(field: 'quota' | 'used' | 'pending' | 'available') {
  const projects = usage.value?.projects ?? []
  if (!projects.length) return null
  let total = '0.000'
  for (const project of projects) {
    const raw = project[field]
    if (typeof raw !== 'string' || !/^\d+(?:\.\d+)?$/.test(raw.trim())) return null
    total = calculateCredits(total, raw.trim(), 'add')
  }
  return total
}

const summary = computed(() => ({
  quota: creditText(sumProjectField('quota')),
  used: creditText(sumProjectField('used')),
  pending: creditText(sumProjectField('pending')),
  available: creditText(sumProjectField('available')),
}))

const modelRows = computed<ModelRow[]>(() => {
  const rows: ModelRow[] = []
  for (const project of usage.value?.projects ?? []) {
    for (const key of project.keys ?? []) {
      for (const model of key.models ?? []) {
        rows.push({
          id: `${project.project_id}:${key.id}:${model.id || model.name}`,
          modelName: model.name || model.id,
          modelId: model.id,
          projectName: project.project_name,
          keyName: key.name,
          model,
        })
      }
    }
  }
  return rows
})

const rangeNote = computed(() => range.value === 'month'
  ? ''
  : `学生用量接口当前只返回本月汇总；「${selectedRange.value.label}」范围暂未接入，积分、Token 与缓存命中率显示为「—」。`)

function modelCredits(row: ModelRow) {
  return range.value === 'month' ? creditText(row.model.credits) : '—'
}

function modelTokens(row: ModelRow) {
  if (range.value !== 'month') return '—'
  if (row.model.tokens != null) return formatTokens(row.model.tokens)
  if (row.model.input_tokens != null && row.model.output_tokens != null) {
    return formatTokens(row.model.input_tokens + row.model.output_tokens)
  }
  return '—'
}

function modelCacheHitRate(row: ModelRow) {
  return range.value === 'month' ? formatPercent(row.model.cache_hit_rate, 1) : '—'
}

function modelCalls(row: ModelRow) {
  return range.value === 'month' && row.model.calls != null ? `${formatTokens(row.model.calls)} 次` : '—'
}

function modelDetail(row: ModelRow) {
  return [
    row.modelId && row.modelId !== row.modelName ? row.modelId : '',
    row.projectName,
    row.keyName,
    modelCalls(row),
  ].filter(Boolean).join(' · ')
}

const billingStateLabels: Record<string, string> = {
  reserved: '已预留',
  pending: '待核',
  pending_review: '待核账',
  charged: '已扣',
  released: '已释放',
}

function receiptStatusLabel(value?: string | null) {
  if (!value) return '—'
  return billingStateLabels[value] ?? (statusLabel(value) || value)
}

function receiptStatusClass(value?: string | null) {
  if (value === 'charged') return 'active'
  if (value && ['reserved', 'pending', 'pending_review'].includes(value)) return 'pending'
  return 'muted'
}

function receiptAppliedText(value?: boolean | null) {
  return value == null ? '—' : value ? '已回执' : '未回执'
}

const recentStatusLabels: Record<string, string> = {
  succeeded: '成功',
  upstream_failed: '上游失败',
  quota_rejected: '额度不足',
  outcome_unknown: '结果未知',
  pending_review: '待核账',
  pending: '处理中',
}

function recentStatusLabel(value?: string | null) {
  if (!value) return '—'
  return recentStatusLabels[value] ?? statusLabel(value) ?? value
}

function recentStatusClass(value?: string | null) {
  if (value === 'succeeded') return 'active'
  if (value === 'upstream_failed') return 'danger'
  if (value && ['pending_review', 'pending', 'outcome_unknown'].includes(value)) return 'pending'
  return 'muted'
}

async function lookupReceipt() {
  const query = receiptQuery.value.trim()
  if (!query) {
    receipt.value = null
    receiptError.value = '请输入请求编号'
    return
  }
  receiptLoading.value = true
  receiptError.value = ''
  try {
    receipt.value = await get<StudentCallReceipt>(`/api/v1/student/usage/calls/${encodeURIComponent(query)}`)
  } catch (caught) {
    receipt.value = null
    receiptError.value = caught instanceof APIError
      ? (caught.status === 404 ? '未找到该请求编号对应的本人调用，请核对编号。' : caught.message)
      : '查询单笔明细失败，请稍后重试'
  } finally {
    receiptLoading.value = false
  }
}

function percent(used: string, quota: string) {
  const quotaValue = Number(quota)
  const usedValue = Number(used)
  return !Number.isFinite(quotaValue) || quotaValue === 0 || !Number.isFinite(usedValue)
    ? null
    : usedValue / quotaValue * 100
}

function percentLabel(used: string, quota: string) {
  const value = percent(used, quota)
  return value === null ? 'N/A' : `${value.toFixed(1)}%`
}

function projectSegments(project: StudentProjectUsageView) {
  return [
    ...project.keys.map(key => ({ name: key.name, value: Number(key.used), displayValue: key.used })),
    { name: '剩余额度', value: Number(project.available), displayValue: project.available, color: '#dfe3ec' },
  ]
}

onMounted(() => { void loadRecent(true) })
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
    <p class="muted" data-testid="credit-rate-note">{{ creditRateNote }}</p>

    <section class="panel student-usage-summary-panel">
      <div class="beichen-section-head">
        <div>
          <h2>本月概览</h2>
          <p class="muted">{{ usage?.month ?? '—' }} · {{ loading ? '正在加载' : '全部项目合计' }}</p>
        </div>
      </div>
      <div class="beichen-summary" aria-label="本月额度概览">
        <div class="beichen-stat" data-testid="usage-total-quota">
          <span>本月额度</span><strong>{{ summary.quota }}</strong><small>老师分配的月额度</small>
        </div>
        <div class="beichen-stat" data-testid="usage-total-used">
          <span>已用</span><strong>{{ summary.used }}</strong><small>学校已记账积分</small>
        </div>
        <div class="beichen-stat" data-testid="usage-total-pending">
          <span>待核</span><strong>{{ summary.pending }}</strong><small>上游未回报，尚未结算</small>
        </div>
        <div class="beichen-stat" data-testid="usage-total-available">
          <span>剩余</span><strong>{{ summary.available }}</strong><small>扣除已用与待核后可用</small>
        </div>
      </div>
    </section>

    <section class="panel student-model-panel">
      <div class="beichen-section-head">
        <div>
          <h2>按模型用量</h2>
          <p class="muted">积分按学校记账；Token 与缓存命中率来自上游回报。</p>
          <p class="muted">需要别的模型？<RouterLink :to="{ name: 'student-models' }" data-testid="usage-model-plaza-link">到模型广场把模型加入当前 Key</RouterLink>。</p>
        </div>
        <div class="range-switch" role="group" aria-label="用量范围">
          <button v-for="item in ranges" :key="item.id" type="button"
            :aria-pressed="range === item.id" :data-testid="`usage-range-${item.id}`"
            @click="range = item.id">{{ item.label }}</button>
        </div>
      </div>
      <p v-if="rangeNote" class="beichen-note" role="status">{{ rangeNote }}</p>
      <div v-if="modelRows.length" class="beichen-table-wrap">
        <table class="beichen-table student-model-table">
          <caption class="sr-only">{{ selectedRange.label }}按模型用量</caption>
          <thead>
            <tr>
              <th scope="col">模型</th>
              <th scope="col" class="numeric-cell">积分</th>
              <th scope="col" class="numeric-cell">Token</th>
              <th scope="col" class="numeric-cell">缓存命中率</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in modelRows" :key="row.id">
              <th scope="row">
                <strong>{{ row.modelName }}</strong>
                <small>{{ modelDetail(row) }}</small>
              </th>
              <td class="numeric-cell" data-label="积分">{{ modelCredits(row) }}</td>
              <td class="numeric-cell" data-label="Token">{{ modelTokens(row) }}</td>
              <td class="numeric-cell" data-label="缓存命中率">{{ modelCacheHitRate(row) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else-if="loaded" title="所选范围暂无模型用量" />
      <p v-else class="muted" role="status">正在加载模型用量…</p>
    </section>

    <section class="panel student-recent-panel">
      <div class="beichen-section-head">
        <div>
          <h2>最近调用</h2>
          <p class="muted">只显示本人经学校网关发起的调用；Token 与缓存数据来自上游回报。</p>
        </div>
        <div class="range-switch" role="group" aria-label="最近调用范围">
          <button v-for="item in recentRanges" :key="item.id" type="button"
            :aria-pressed="recentRange === item.id" :data-testid="`recent-range-${item.id}`"
            @click="changeRecentRange(item.id)">{{ item.label }}</button>
        </div>
      </div>
      <p v-if="recentError" class="error banner" role="alert">{{ recentError }}</p>
      <div v-if="recentItems.length" class="beichen-table-wrap">
        <table class="beichen-table student-recent-table" data-testid="recent-calls-table">
          <caption class="sr-only">最近调用</caption>
          <thead>
            <tr>
              <th scope="col">时间</th>
              <th scope="col">模型</th>
              <th scope="col" class="numeric-cell">输入</th>
              <th scope="col" class="numeric-cell">输出</th>
              <th scope="col" class="numeric-cell">缓存读</th>
              <th scope="col" class="numeric-cell">缓存写</th>
              <th scope="col" class="numeric-cell">扣分</th>
              <th scope="col">状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in recentItems" :key="item.id">
              <td data-label="时间">{{ fullTime(item.created_at) }}</td>
              <td data-label="模型">
                <strong>{{ item.model_name || item.model_id }}</strong>
                <small class="recent-request">{{ item.model_id }} · {{ item.request_id }}</small>
              </td>
              <td class="numeric-cell" data-label="输入">{{ formatTokens(item.input_tokens) }}</td>
              <td class="numeric-cell" data-label="输出">{{ formatTokens(item.output_tokens) }}</td>
              <td class="numeric-cell" data-label="缓存读">{{ formatTokens(item.cache_read_tokens) }}</td>
              <td class="numeric-cell" data-label="缓存写">{{ formatTokens(item.cache_write_tokens) }}</td>
              <td class="numeric-cell" data-label="扣分">{{ creditText(item.credits) }}</td>
              <td data-label="状态"><span class="status-pill" :class="recentStatusClass(item.status)">{{ recentStatusLabel(item.status) }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else-if="recentLoaded && !recentLoading" title="所选范围暂无调用记录" />
      <p v-else class="muted" role="status">正在加载最近调用…</p>
      <div class="student-recent-actions">
        <button v-if="recentCursor" class="button ghost" :disabled="recentLoading" @click="loadRecent()">
          {{ recentLoading ? '加载中…' : '加载更多' }}
        </button>
        <span v-else-if="recentLoaded && recentItems.length" class="muted">已显示全部</span>
        <button class="button ghost" :disabled="recentLoading" @click="loadRecent(true)">刷新</button>
      </div>
    </section>

    <section class="panel student-receipt-panel">
      <div class="beichen-section-head">
        <div>
          <h2>单笔明细</h2>
          <p class="muted">输入学校网关返回的请求编号，查询本人一笔调用。</p>
        </div>
        <form class="student-receipt-search" @submit.prevent="lookupReceipt">
          <input v-model="receiptQuery" aria-label="请求编号" placeholder="输入请求编号" :disabled="receiptLoading">
          <button class="button primary" :disabled="receiptLoading || !receiptQuery.trim()">{{ receiptLoading ? '查询中…' : '查询' }}</button>
        </form>
      </div>
      <p v-if="receiptError" class="error banner" role="alert">{{ receiptError }}</p>
      <p class="beichen-note">可按请求编号查询单笔明细；时间、输入/输出与缓存 Token 以学校网关和上游回报为准。</p>
      <div class="beichen-table-wrap">
        <table class="beichen-table student-call-table">
          <caption class="sr-only">单笔调用明细</caption>
          <thead>
            <tr>
              <th scope="col">时间</th>
              <th scope="col">模型</th>
              <th scope="col" class="numeric-cell">输入</th>
              <th scope="col" class="numeric-cell">输出</th>
              <th scope="col" class="numeric-cell">缓存读</th>
              <th scope="col" class="numeric-cell">缓存写</th>
              <th scope="col" class="numeric-cell">扣分</th>
              <th scope="col">状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="receipt">
              <td data-label="时间">{{ fullTime(receipt.created_at) }}</td>
              <td data-label="模型">{{ receipt.model_id }}</td>
              <td class="numeric-cell" data-label="输入">{{ formatTokens(receipt.input_tokens) }}</td>
              <td class="numeric-cell" data-label="输出">{{ formatTokens(receipt.output_tokens) }}</td>
              <td class="numeric-cell" data-label="缓存读">{{ formatTokens(receipt.cache_read_tokens) }}</td>
              <td class="numeric-cell" data-label="缓存写">{{ formatTokens(receipt.cache_write_tokens) }}</td>
              <td class="numeric-cell" data-label="扣分">{{ creditText(receipt.charged_credits) }}</td>
              <td data-label="状态"><span class="status-pill" :class="receiptStatusClass(receipt.billing_state)">{{ receiptStatusLabel(receipt.billing_state) }}</span></td>
            </tr>
            <tr v-else>
              <td colspan="8" class="beichen-note">{{ receiptLoading ? '正在查询…' : '输入请求编号后显示单笔明细。' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="receipt" class="student-receipt-meta">
        <span>请求编号 <code>{{ receipt.request_id }}</code></span>
        <span>渠道 {{ receipt.channel }}</span>
        <span>回执 {{ receiptAppliedText(receipt.receipt_applied) }}</span>
      </div>
    </section>

    <section v-if="usage?.projects.length" class="student-project-section">
      <div class="beichen-section-head">
        <div>
          <h2>项目与 Key 用量</h2>
          <p class="muted">保留原有分项目、分 Key 的环图与额度明细。</p>
        </div>
      </div>
      <div class="grid two">
        <article v-for="project in usage.projects" :key="project.project_id" class="panel">
          <div class="row">
            <div>
              <h2>{{ project.project_name }}</h2>
              <p class="muted">已分配 {{ project.quota }} 积分 · 学校已用 {{ project.used }} · 在途 {{ project.pending }}<template v-if="project.pending_review"> · 有调用待核账</template></p>
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
            <div class="usage-stat">{{ quotaBalance(project.quota, project.used, project.available).label }}<strong>{{ quotaBalance(project.quota, project.used, project.available).amount }}</strong></div>
          </div>
          <section v-if="project.keys.length" class="member-key-stack">
            <article v-for="key in project.keys" :key="key.id" class="member-key-row">
              <strong>{{ key.name }}</strong>
              <span class="muted">{{ statusLabel(key.status) }} · 学校已用 {{ key.used }} 积分 · 在途 {{ key.pending }}<template v-if="key.pending_review"> · 有调用待核账</template></span>
              <ul v-if="key.models.length" class="key-models">
                <li v-for="model in key.models" :key="model.id">{{ model.name }} · 学校已用 {{ model.credits }} 积分</li>
              </ul>
            </article>
          </section>
        </article>
      </div>
    </section>
    <EmptyState v-else-if="loaded" title="本月暂无可展示的额度" />
    <p v-else class="muted" role="status">正在加载项目用量…</p>
  </section>
</template>

<style scoped>
.student-usage-summary-panel,
.student-model-panel,
.student-recent-panel,
.student-receipt-panel,
.student-project-section {
  display: grid;
  gap: 16px;
  min-width: 0;
}

.student-usage-summary-panel .beichen-stat strong {
  overflow-wrap: anywhere;
}

.student-model-table th small {
  display: block;
  margin-top: 4px;
  color: var(--ink-soft);
  font-size: 11px;
  font-weight: 400;
  line-height: 1.5;
}

.student-model-table th[scope="row"] {
  min-width: 0;
  overflow-wrap: anywhere;
}

.student-model-table td[data-label],
.student-recent-table td[data-label],
.student-call-table td[data-label] {
  overflow-wrap: anywhere;
}

.student-recent-table th small,
.student-recent-table td small {
  display: block;
  margin-top: 4px;
  color: var(--ink-soft);
  font-size: 11px;
  font-weight: 400;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.student-recent-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.student-receipt-search {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

.student-receipt-search input {
  flex: 1 1 240px;
  min-width: 0;
}

.student-receipt-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 18px;
  color: var(--ink-soft);
  font-size: 12px;
}

.student-receipt-meta code {
  color: var(--ink);
  overflow-wrap: anywhere;
}

@media (max-width: 560px) {
  .student-receipt-search,
  .student-receipt-search input {
    width: 100%;
  }
}

@media (max-width: 900px) {
  .student-model-table tbody tr,
  .student-recent-table tbody tr,
  .student-call-table tbody tr {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px 10px;
    padding: 10px 0;
  }

  .student-model-table tbody th,
  .student-recent-table tbody th,
  .student-call-table td[colspan] {
    grid-column: 1 / -1;
  }

  .student-model-table th,
  .student-model-table td,
  .student-recent-table th,
  .student-recent-table td,
  .student-call-table th,
  .student-call-table td {
    padding: 4px 8px;
  }

  .student-model-table td[data-label]::before,
  .student-recent-table td[data-label]::before,
  .student-call-table td[data-label]::before {
    content: attr(data-label);
    display: block;
    color: var(--ink-soft);
    font-size: 11px;
  }

  .student-model-table .numeric-cell,
  .student-recent-table .numeric-cell,
  .student-call-table .numeric-cell {
    text-align: left;
  }
}
</style>
