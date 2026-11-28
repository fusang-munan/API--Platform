<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId } from 'vue'
import { ChevronDown, History, Search } from 'lucide-vue-next'
import { APIError, apiErrorMessage } from '../../api/client'
import { studentAPI } from '../../api/student'
import type {
  StudentCallReceipt, StudentKeyUsage, StudentModelUsageFields, StudentProjectUsageRow, StudentUsageFields,
  StudentRecentCall, StudentRecentRange, StudentUsageSnapshot,
} from '../../api/student'
import AppDialog from '../../components/AppDialog.vue'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { quotaBalance, schoolMonth, statusLabel } from '../../utils/format'
import {
  formatUsageCacheCoverage,
  formatUsageCacheRate,
  formatUsageCount,
  formatUsageCredits,
  formatUsagePendingReview,
  formatUsageTokenCount,
  hasStudentUsageModels,
  isUsageCount,
  studentInFlightCredits,
  studentKeyAvailable,
  sumStudentProjectCredits,
} from '../../utils/studentUsage'

const pageId = useId()
const month = ref(schoolMonth())
const monthTitle = computed(() => {
  const [year, monthNumber] = month.value.split('-')
  return `${year} 年 ${Number(monthNumber)} 月个人用量`
})
const report = ref<StudentUsageSnapshot | null>(null)
const loading = ref(true)
const loadFailed = ref(false)
const expandedSections = ref(new Set<string>())
let latestRequest = 0
let loadingMonth: string | null = null

async function load() {
  const requestedMonth = month.value
  if (loadingMonth === requestedMonth) return
  const request = ++latestRequest
  loadingMonth = requestedMonth
  loading.value = true
  loadFailed.value = false
  try {
    const data = await studentAPI.usage(requestedMonth)
    if (!Array.isArray(data?.projects)) throw new Error('Invalid student usage report')
    if (request === latestRequest) report.value = data
  } catch {
    if (request === latestRequest) {
      report.value = null
      loadFailed.value = true
    }
  } finally {
    if (request === latestRequest) {
      loadingMonth = null
      loading.value = false
    }
  }
}

function changeMonth(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.validity.valid || !/^\d{4}-(0[1-9]|1[0-2])$/.test(input.value) || input.value > schoolMonth()) {
    input.value = month.value
    return
  }
  if (input.value === month.value) return
  month.value = input.value
  report.value = null
  expandedSections.value.clear()
  void load()
}

useVisibilityPolling(load)

const totals = computed(() => {
  const total = report.value?.totals
  return typeof total?.credits === 'string' && isUsageCount(total.calls) ? total : null
})
const models = computed(() => hasStudentUsageModels(report.value?.models) ? report.value.models : null)

const creditStats = computed(() => {
  if (!report.value) return []
  const { projects } = report.value
  return [
    { id: 'quota', label: '月额度', amount: sumStudentProjectCredits(projects, 'quota') },
    { id: 'used', label: '已扣积分', amount: totals.value?.credits },
    { id: 'pending', label: '在途积分', amount: sumStudentProjectCredits(projects, 'in_flight_credits') },
    { id: 'available', label: '各项目个人余额合计', amount: sumStudentProjectCredits(projects, 'available') },
  ]
})

const projectSummaries = computed(() => (report.value?.projects ?? []).map(project => {
  const keys = Array.isArray(project.keys) ? project.keys : []
  return {
    project,
    balance: quotaBalance(project.quota, project.used, project.available),
    pendingReview: formatUsagePendingReview(project),
    keyRows: keys.map(key => ({
      key,
      models: hasStudentUsageModels(key.models) ? key.models : null,
      pendingReview: formatUsagePendingReview(key),
    })),
  }
}))
const keySummaries = computed(() => projectSummaries.value.flatMap(({ project, keyRows }) =>
  keyRows.map(row => ({ ...row, project }))))

function tokenSample(value: unknown) {
  const count = formatUsageTokenCount(value)
  return count === '—' ? '无样本' : count
}

function tokenCoverage(metrics: StudentUsageFields) {
  if (tokenSample(metrics.tokens?.total) === '无样本') return ''
  if (metrics.completeness === 'complete') return '完整'
  const coverage = metrics.token_coverage
  return isUsageCount(coverage?.complete_calls) && isUsageCount(coverage?.calls)
    ? `部分回报（${formatUsageCount(coverage.complete_calls)}/${formatUsageCount(coverage.calls)} 笔完整）`
    : '部分回报'
}

function cacheRate(metrics: StudentUsageFields) {
  const rate = formatUsageCacheRate(metrics.cache_hit_rate)
  return rate === '—' || metrics.cache_hit_coverage?.sample_calls === 0 ? '无样本' : rate
}

function cacheCoverage(metrics: StudentUsageFields) {
  const total = metrics.token_coverage?.calls
  const samples = metrics.cache_hit_coverage?.sample_calls
  const coverage = formatUsageCacheCoverage(metrics)
  if (cacheRate(metrics) === '无样本') return coverage
  const label = isUsageCount(total) && total > 0 && total === samples ? '完整' : '部分回报'
  return coverage ? `${label} · ${coverage}` : label
}

function modelNotes(model: StudentModelUsageFields) {
  const notes: string[] = []
  const unclassified = tokenSample(model.token_coverage?.unclassified_tokens)
  if (unclassified !== '无样本' && unclassified !== '0') notes.push(`未分类 ${unclassified} Token`)
  if (model.calls && formatUsageCredits(model.credits) === '0') notes.push('零额已结算')
  const review = formatUsagePendingReview(model)
  if (review) notes.push(review)
  return notes.join(' · ')
}

const modelColumns: Array<{
  label: string
  value: (model: StudentModelUsageFields) => string
  detail?: (model: StudentModelUsageFields) => string
  numeric?: boolean
}> = [
  { label: '已扣积分', value: model => formatUsageCredits(model.credits), numeric: true },
  { label: '调用次数', value: model => formatUsageCount(model.calls ?? 0), numeric: true },
  { label: 'Token', value: model => tokenSample(model.tokens?.total), detail: tokenCoverage, numeric: true },
  {
    label: '缓存命中率',
    value: cacheRate,
    detail: cacheCoverage,
    numeric: true,
  },
  { label: '说明', value: modelNotes },
]

function keySectionId(projectId: string, keyId: string) {
  return `${pageId}-key-${projectId}-${keyId}`
}

function chartSectionId(projectId: string) {
  return `${pageId}-quota-${projectId}`
}

function toggleSection(id: string) {
  if (expandedSections.value.has(id)) expandedSections.value.delete(id)
  else expandedSections.value.add(id)
}

function projectSegments(project: StudentProjectUsageRow, keyRows: { key: StudentKeyUsage }[]) {
  const inFlight = studentInFlightCredits(project)
  return [
    ...keyRows.map(({ key }) => ({ name: key.name, value: Number(key.used), displayValue: formatUsageCredits(key.used) })),
    {
      name: '在途积分', value: Number(inFlight),
      displayValue: formatUsageCredits(inFlight), color: '#e5a93f',
    },
    {
      name: '项目内个人余额', value: Number(project.available),
      displayValue: formatUsageCredits(project.available), color: '#dfe3ec',
    },
  ]
}

const receiptOpen = ref(false)
const receiptQuery = ref('')
const receipt = ref<StudentCallReceipt | null>(null)
const receiptLoading = ref(false)
const receiptError = ref('')
const receiptNotFound = '未找到可查询的本人调用。请核对请求编号，并确认这笔调用经学校网关发往北辰。'
let latestLookup = 0

function setReceiptOpen(open: boolean) {
  receiptOpen.value = open
  if (open) return
  ++latestLookup
  receiptQuery.value = ''
  receipt.value = null
  receiptLoading.value = false
  receiptError.value = ''
}

async function lookupReceipt() {
  const query = receiptQuery.value.trim()
  if (!receiptOpen.value || !query || receiptLoading.value) return
  receipt.value = null
  receiptError.value = ''
  // 浏览器会把这两种路径段解析为目录，无法作为请求编号发送。
  if (query === '.' || query === '..') {
    receiptError.value = receiptNotFound
    return
  }
  const lookup = ++latestLookup
  receiptLoading.value = true
  try {
    const data = await studentAPI.callReceipt(query)
    if (data?.request_id !== query || !Number.isFinite(Date.parse(data.created_at))) {
      throw new Error('Invalid student call receipt')
    }
    if (lookup === latestLookup) receipt.value = data
  } catch (error) {
    if (lookup !== latestLookup) return
    receiptError.value = error instanceof APIError && error.status === 404
      ? receiptNotFound
      : apiErrorMessage(error, '查询失败，请稍后重试')
  } finally {
    if (lookup === latestLookup) receiptLoading.value = false
  }
}

const billingStateLabels: Record<string, string> = {
  reserved: '处理中',
  pending: '处理中',
  pending_review: '待核账',
  charged: '已结算',
  released: '已释放',
  failed: '结算失败',
}
const callTime = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', dateStyle: 'medium', timeStyle: 'medium',
})
const callTokenFields = [
  { field: 'input_tokens', label: '输入' },
  { field: 'output_tokens', label: '输出' },
  { field: 'cache_read_tokens', label: '缓存读' },
  { field: 'cache_write_tokens', label: '缓存写' },
] as const
const receiptTokenFields = [
  ...callTokenFields,
  { field: 'total_tokens', label: '总 Token' },
] as const
const usageStateLabels: Record<string, string> = {
  reported: '已回报', partial: '部分回报', missing: '无回报', interrupted: '响应中断',
}
const usageReasonLabels: Record<string, string> = {
  usage_not_reported: '上游未提供用量',
  terminal_usage_missing: '上游结束时未返回最终用量',
  response_interrupted: '响应中断',
  upstream_http_error: '上游返回错误',
  usage_observation_limit: '响应超出用量记录范围',
  token_breakdown_unconfirmed: '分项用量未确认',
  incomplete_token_breakdown: '分项用量不完整',
  cache_write_subtotal_only: '仅回报了部分缓存写入量',
  cache_write_not_in_usage_metadata: '上游未提供缓存写入量',
  inconsistent_token_breakdown: '分项用量不一致',
  inconsistent_token_total: '总量与分项不一致',
}

function receiptAppliedLabel(applied: boolean | null) {
  if (typeof applied !== 'boolean') return '暂无匹配回执'
  return applied ? '已用回执补记' : '已匹配回执，未用于补记'
}

const recentRanges: { value: StudentRecentRange; label: string }[] = [
  { value: 'today', label: '今天' },
  { value: 'month', label: '本月' },
  { value: 'all', label: '全部' },
]
const recentStatusLabels: Record<string, string> = {
  succeeded: '成功', failed: '失败', pending_review: '待核账',
}
const recentOpen = ref(false)
const recentRange = ref<StudentRecentRange>('today')
const recentCalls = ref<StudentRecentCall[]>([])
const recentCursor = ref<string | null>(null)
const recentLoading = ref(false)
const recentError = ref('')
const expandedCallID = ref('')
let recentRequest: AbortController | undefined

async function loadRecent() {
  if (!recentOpen.value || recentRequest) return
  const request = new AbortController()
  recentRequest = request
  recentLoading.value = true
  recentError.value = ''
  try {
    const page = await studentAPI.recentCalls(recentRange.value, recentCursor.value, request.signal)
    if (request.signal.aborted) return
    if (!Array.isArray(page?.items) || !page.items.every(call =>
      typeof call?.id === 'string' && typeof call.created_at === 'string' && Number.isFinite(Date.parse(call.created_at)),
    )) throw new Error('Invalid student recent calls')
    recentCalls.value = [...recentCalls.value, ...page.items]
    recentCursor.value = typeof page.next_cursor === 'string' && page.next_cursor ? page.next_cursor : null
  } catch (error) {
    if (!request.signal.aborted) recentError.value = apiErrorMessage(error, '最近调用读取失败，请重试')
  } finally {
    if (!request.signal.aborted) {
      recentRequest = undefined
      recentLoading.value = false
    }
  }
}

function setRecentRange(range: StudentRecentRange) {
  recentRequest?.abort()
  recentRequest = undefined
  recentRange.value = range
  recentCalls.value = []
  recentCursor.value = null
  recentLoading.value = false
  recentError.value = ''
  expandedCallID.value = ''
  void loadRecent()
}

function setRecentOpen(open: boolean) {
  recentOpen.value = open
  setRecentRange('today')
}

onBeforeUnmount(() => recentRequest?.abort())
</script>

<template>
  <section class="page student-usage">
    <header class="page-heading usage-heading">
      <div>
        <h1>{{ monthTitle }}</h1>
        <p class="muted" data-testid="credit-rate-note">100 积分 = 1 元；金额统一用积分显示。</p>
      </div>
      <div class="usage-toolbar">
        <label>月份（北京时间）
          <input :value="month" type="month" :max="schoolMonth()" required @change="changeMonth">
        </label>
        <AppDialog :open="recentOpen" title="最近调用" description="按北京时间筛选，点开一条调用查看详情。"
          @update:open="setRecentOpen">
          <template #trigger>
            <button class="button secondary usage-disclosure" type="button" data-testid="open-recent">
              <History :size="15" aria-hidden="true" />最近调用
            </button>
          </template>
          <div class="usage-recent" :aria-busy="recentLoading">
            <div class="range-switch" role="group" aria-label="最近调用范围（北京时间）">
              <button v-for="range in recentRanges" :key="range.value" type="button"
                :aria-pressed="recentRange === range.value" :data-testid="`recent-range-${range.value}`"
                @click="range.value !== recentRange && setRecentRange(range.value)">{{ range.label }}</button>
            </div>
            <ul v-if="recentCalls.length" class="usage-recent-list" aria-label="最近调用记录">
              <li v-for="call in recentCalls" :key="call.id">
                <button class="usage-recent-row" type="button" :aria-expanded="expandedCallID === call.id"
                  :aria-controls="`${pageId}-recent-${call.id}`"
                  @click="expandedCallID = expandedCallID === call.id ? '' : call.id">
                  <span>
                    <strong>{{ call.model_name || call.model_id || '—' }}</strong>
                    <time class="muted" :datetime="call.created_at">{{ callTime.format(new Date(call.created_at)) }}</time>
                  </span>
                  <span class="usage-recent-amount">
                    <strong>{{ formatUsageCredits(call.credits) }} 积分</strong>
                    <small class="muted">{{ billingStateLabels[call.billing_state] ?? statusLabel(call.billing_state) }}</small>
                  </span>
                  <ChevronDown :size="16" aria-hidden="true" />
                </button>
                <div v-show="expandedCallID === call.id" :id="`${pageId}-recent-${call.id}`">
                  <div v-if="expandedCallID === call.id" class="usage-receipt-result usage-recent-detail">
                    <dl class="usage-receipt-meta">
                      <div><dt>项目</dt><dd>{{ call.project_name || '—' }}</dd></div>
                      <div><dt>调用结果</dt><dd>{{ recentStatusLabels[call.status] ?? statusLabel(call.status) }}</dd></div>
                      <div><dt>模型标识</dt><dd>{{ call.model_id || '—' }}</dd></div>
                      <div class="usage-receipt-status"><dt>请求编号</dt><dd><code>{{ call.request_id || '—' }}</code></dd></div>
                    </dl>
                    <dl class="usage-receipt-tokens" aria-label="Token 明细">
                      <div v-for="{ field, label } in callTokenFields" :key="field">
                        <dt>{{ label }}</dt><dd>{{ tokenSample(call[field]) }}</dd>
                      </div>
                    </dl>
                  </div>
                </div>
              </li>
            </ul>
            <p v-if="recentError" class="error banner" role="alert">{{ recentError }}</p>
            <p v-else-if="recentLoading" class="muted" role="status">正在读取最近调用…</p>
            <p v-else-if="!recentCalls.length" class="muted usage-recent-empty" role="status">所选范围暂无调用记录</p>
            <button v-if="recentError || recentCursor" class="button ghost usage-recent-more" type="button"
              :disabled="recentLoading" @click="loadRecent">{{ recentError ? '重试' : '加载更多' }}</button>
          </div>
        </AppDialog>
        <AppDialog
          :open="receiptOpen"
          title="查单笔调用"
          description="输入学校网关返回的请求编号，查询发往北辰的本人调用。"
          @update:open="setReceiptOpen">
          <template #trigger>
            <button class="button secondary usage-disclosure" type="button" data-testid="open-receipt">
              <Search :size="15" aria-hidden="true" />查单笔调用
            </button>
          </template>
          <form class="usage-receipt-form" @submit.prevent="lookupReceipt">
            <label>请求编号
              <input v-model="receiptQuery" placeholder="学校网关返回的 X-Request-ID" :readonly="receiptLoading" required>
            </label>
            <button class="button primary" type="submit" :disabled="receiptLoading || !receiptQuery.trim()">
              {{ receiptLoading ? '查询中…' : '查询' }}
            </button>
          </form>
          <p v-if="receiptError" class="error banner usage-receipt-feedback" role="alert">{{ receiptError }}</p>
          <p v-else-if="receiptLoading" class="muted usage-receipt-feedback" role="status">正在查询调用…</p>
          <div v-else-if="receipt" class="usage-receipt-result" data-testid="receipt-result">
            <p class="sr-only" role="status">已找到调用</p>
            <dl class="usage-receipt-meta">
              <div><dt>模型</dt><dd>{{ receipt.model_name || receipt.model_id }}</dd></div>
              <div><dt>请求编号</dt><dd><code>{{ receipt.request_id }}</code></dd></div>
              <div>
                <dt>时间（北京时间）</dt>
                <dd><time :datetime="receipt.created_at">{{ callTime.format(new Date(receipt.created_at)) }}</time></dd>
              </div>
              <div><dt>已扣积分</dt><dd>{{ formatUsageCredits(receipt.charged_credits) }}</dd></div>
              <div><dt>结算状态</dt><dd>{{ billingStateLabels[receipt.billing_state] ?? statusLabel(receipt.billing_state) }}</dd></div>
              <div><dt>渠道</dt><dd>{{ receipt.channel }}</dd></div>
              <div class="usage-receipt-status"><dt>回执状态</dt><dd>{{ receiptAppliedLabel(receipt.receipt_applied) }}</dd></div>
            </dl>
            <dl class="usage-receipt-tokens" aria-label="Token 明细">
              <div v-for="{ field, label } in receiptTokenFields" :key="field">
                <dt>{{ label }}</dt><dd>{{ tokenSample(receipt[field]) }}</dd>
              </div>
            </dl>
            <p v-if="receipt.usage_state || receipt.usage_reason" class="muted usage-evidence">
              Token 回报
              <template v-if="receipt.usage_state">{{ usageStateLabels[receipt.usage_state] ?? receipt.usage_state }}</template>
              <template v-if="receipt.usage_reason"> · {{ usageReasonLabels[receipt.usage_reason] ?? receipt.usage_reason }}</template>
            </p>
          </div>
        </AppDialog>
      </div>
    </header>

    <LoadingRegion :initial-loading="loading && !report" :refreshing="loading && Boolean(report)"
      label="正在读取个人用量" variant="list" :rows="3">
      <div v-if="loadFailed" class="panel usage-load-error">
        <p class="error" role="alert">读取失败</p>
        <button class="button secondary" type="button" @click="load">重试</button>
      </div>
      <div v-else-if="report" class="usage-report">
        <section class="panel usage-panel" :aria-labelledby="`${pageId}-overview`">
          <h2 :id="`${pageId}-overview`">月度概览</h2>
          <div class="beichen-summary usage-stats">
            <div v-for="stat in creditStats" :key="stat.id" class="beichen-stat" :data-testid="`usage-total-${stat.id}`">
              <span>{{ stat.label }}</span><strong>{{ formatUsageCredits(stat.amount) }}</strong>
            </div>
          </div>
        </section>

        <section class="panel usage-panel" :aria-labelledby="`${pageId}-keys`">
          <header class="beichen-section-head">
            <h2 :id="`${pageId}-keys`">按 Key 查看</h2>
            <RouterLink class="usage-model-link" :to="{ name: 'student-models' }">到模型广场添加模型</RouterLink>
          </header>
          <p class="muted usage-table-note">调用次数含零额结算。待核账记录单列。</p>
          <EmptyState v-if="!keySummaries.length" title="所选月暂无 Key 记录" />
          <ul v-else class="usage-key-list" aria-label="Key 用量">
            <li v-for="{ project, key, models: keyModels, pendingReview: keyReview } in keySummaries"
              :key="key.id">
              <div class="usage-key-row" data-testid="usage-key-row">
                <div class="usage-key-name">
                  <strong>{{ key.name }}</strong>
                  <span class="status-badge" :class="key.status">{{ statusLabel(key.status) }}</span>
                </div>
                <dl class="usage-key-totals">
                  <div><dt>项目</dt><dd>{{ project.project_name }}</dd></div>
                  <div><dt>已扣积分</dt><dd>{{ formatUsageCredits(key.used) }}</dd></div>
                  <div><dt>调用次数</dt><dd>{{ isUsageCount(key.calls) ? formatUsageCount(key.calls) : '未回报' }}</dd></div>
                </dl>
                <button class="button ghost usage-disclosure" type="button"
                  :aria-expanded="expandedSections.has(keySectionId(project.project_id, key.id))"
                  :aria-controls="keySectionId(project.project_id, key.id)"
                  :aria-label="`查看 ${project.project_name} · ${key.name} 的用量明细`"
                  @click="toggleSection(keySectionId(project.project_id, key.id))">
                  详情<ChevronDown :size="14" aria-hidden="true" />
                </button>
              </div>
              <div v-show="expandedSections.has(keySectionId(project.project_id, key.id))"
                :id="keySectionId(project.project_id, key.id)" class="usage-key-models">
                <template v-if="expandedSections.has(keySectionId(project.project_id, key.id))">
                  <dl class="usage-key-details">
                    <div><dt>在途积分</dt><dd>{{ formatUsageCredits(studentInFlightCredits(key)) }}</dd></div>
                    <div><dt>本项目个人可用积分</dt><dd>{{ formatUsageCredits(studentKeyAvailable(key)) }}</dd></div>
                    <div>
                      <dt>Token</dt><dd>{{ tokenSample(key.tokens?.total) }}</dd>
                      <small v-if="tokenCoverage(key)">{{ tokenCoverage(key) }}</small>
                    </div>
                    <div>
                      <dt>缓存命中率</dt><dd>{{ cacheRate(key) }}</dd>
                      <small v-if="cacheCoverage(key)">{{ cacheCoverage(key) }}</small>
                    </div>
                  </dl>
                  <p v-if="keyReview" class="usage-review">{{ keyReview }}</p>
                  <div v-if="keyModels?.length" class="beichen-table-wrap">
                    <table class="beichen-table usage-model-table">
                      <caption class="sr-only">{{ key.name }} 的模型用量</caption>
                      <thead>
                        <tr>
                          <th scope="col">模型</th>
                          <th v-for="column in modelColumns" :key="column.label" scope="col"
                            :class="{ 'numeric-cell': column.numeric }">{{ column.label }}</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-for="model in keyModels" :key="model.id">
                          <th scope="row">
                            <strong>{{ model.name || model.id }}</strong>
                            <small v-if="model.name && model.name !== model.id">{{ model.id }}</small>
                          </th>
                          <td v-for="column in modelColumns" :key="column.label" :data-label="column.label"
                            :class="{ 'numeric-cell': column.numeric }">
                            {{ column.value(model) }}
                            <small v-if="column.detail">{{ column.detail(model) }}</small>
                          </td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                  <p v-else class="muted">{{ keyModels ? '暂无模型记录' : '模型明细暂不可用' }}</p>
                </template>
              </div>
            </li>
          </ul>
        </section>

        <details v-if="projectSummaries.length" class="panel usage-panel usage-secondary">
          <summary>项目额度</summary>
          <article v-for="{ project, balance, keyRows, pendingReview } in projectSummaries" :key="project.project_id"
            class="usage-project">
            <header class="usage-project-heading">
              <div>
                <h3>{{ project.project_name }}</h3>
                <div class="usage-summary">
                  <span>月额度 <strong>{{ formatUsageCredits(project.quota) }}</strong></span>
                  <span>学校已用 <strong>{{ formatUsageCredits(project.used) }}</strong></span>
                  <span>在途 <strong>{{ formatUsageCredits(studentInFlightCredits(project)) }}</strong></span>
                  <span class="usage-project-balance">{{ balance.label }}<strong>{{ formatUsageCredits(balance.amount) }}</strong></span>
                  <span v-if="isUsageCount(project.calls)">已结算 {{ formatUsageCount(project.calls) }} 次</span>
                </div>
                <p v-if="pendingReview" class="usage-review">{{ pendingReview }}</p>
              </div>
              <button class="button ghost usage-disclosure" type="button"
                :aria-expanded="expandedSections.has(chartSectionId(project.project_id))"
                :aria-controls="chartSectionId(project.project_id)"
                @click="toggleSection(chartSectionId(project.project_id))">
                额度图<ChevronDown :size="14" aria-hidden="true" />
              </button>
            </header>
            <div v-show="expandedSections.has(chartSectionId(project.project_id))"
              :id="chartSectionId(project.project_id)" class="usage-chart">
              <CreditDonut v-if="expandedSections.has(chartSectionId(project.project_id))"
                :segments="projectSegments(project, keyRows)" :total="Number(project.quota)" unit="积分"
                :label="`${project.project_name} 各 Key 已扣积分与额度`" />
            </div>

            <p v-if="!keyRows.length" class="muted usage-no-keys">暂无 Key 记录</p>
          </article>
        </details>

        <details class="panel usage-panel usage-secondary">
          <summary>全部 Key 按模型合计</summary>
          <div v-if="!totals || !models" class="usage-load-error">
            <p class="error" role="alert">模型汇总读取失败。返回数据缺少汇总字段。</p>
            <button class="button secondary" type="button" @click="load">重试</button>
          </div>
          <template v-if="models?.length">
            <p class="muted usage-table-note">调用次数只统计已结算调用，含零额结算。待核笔数单独显示。</p>
            <div class="beichen-table-wrap">
              <table class="beichen-table usage-model-table" data-testid="student-model-summary">
                <caption class="sr-only">所选月按模型合计</caption>
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th v-for="column in modelColumns" :key="column.label" scope="col"
                      :class="{ 'numeric-cell': column.numeric }">{{ column.label }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="model in models" :key="model.id">
                    <th scope="row">
                      <strong>{{ model.name || model.id }}</strong>
                      <small v-if="model.name && model.name !== model.id">{{ model.id }}</small>
                    </th>
                    <td v-for="column in modelColumns" :key="column.label" :data-label="column.label"
                      :class="{ 'numeric-cell': column.numeric }">
                      {{ column.value(model) }}
                      <small v-if="column.detail">{{ column.detail(model) }}</small>
                    </td>
                  </tr>
                </tbody>
                <tfoot v-if="totals">
                  <tr>
                    <th scope="row">合计</th>
                    <td v-for="column in modelColumns" :key="column.label" :data-label="column.label"
                      :class="{ 'numeric-cell': column.numeric }">
                      {{ column.value(totals) }}
                      <small v-if="column.detail">{{ column.detail(totals) }}</small>
                    </td>
                  </tr>
                </tfoot>
              </table>
            </div>
          </template>
          <EmptyState v-else-if="models && totals" title="所选月暂无模型用量" />
        </details>
      </div>
    </LoadingRegion>
  </section>
</template>

<style scoped>
.usage-heading {
  flex-wrap: wrap;
  gap: 14px 24px;
  margin-bottom: 20px;
}

.usage-heading p,
.usage-panel h2,
.usage-table-note,
.usage-load-error p {
  margin: 0;
}

.usage-toolbar,
.usage-load-error {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 10px;
}

.usage-toolbar label {
  min-width: 138px;
}

.usage-toolbar input {
  margin-top: 5px;
}

.usage-load-error {
  align-items: center;
}

.usage-report,
.usage-panel {
  display: grid;
  gap: 14px;
  min-width: 0;
}

.usage-panel {
  padding: clamp(14px, 2.1vw, 24px);
}

.usage-secondary > summary {
  cursor: pointer;
  font-weight: 650;
}

.usage-secondary[open] > summary {
  margin-bottom: 12px;
}

.usage-secondary > .usage-project + .usage-project {
  margin-top: 12px;
}

.usage-stats {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.usage-stats strong,
.usage-model-table th,
.usage-model-table td,
.usage-project,
.usage-receipt-result {
  overflow-wrap: anywhere;
}

.usage-model-link {
  color: var(--primary);
  font-size: 13px;
}

.usage-model-table {
  table-layout: fixed;
}

.usage-model-table thead th,
.usage-model-table .numeric-cell {
  white-space: normal;
}

.usage-model-table thead th:first-child {
  width: 22%;
}

.usage-model-table thead th:last-child {
  width: 25%;
}

.usage-model-table tfoot {
  background: rgba(108, 76, 232, .045);
  font-weight: 650;
}

.usage-model-table tfoot th,
.usage-model-table tfoot td {
  border-bottom: 0;
}

.usage-model-table small,
.usage-key-models small {
  display: block;
  font-size: 11px;
  font-weight: 400;
  color: var(--ink-soft);
  line-height: 1.45;
}

.usage-model-table small {
  margin-top: 3px;
}

.usage-model-table small:empty {
  display: none;
}

.usage-project {
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: 14px;
}

.usage-project-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px 16px;
  flex-wrap: wrap;
  padding: 14px;
  background: var(--surface);
  border-radius: 14px 14px 0 0;
}

.usage-project-heading h3 {
  margin: 0 0 6px;
  font-size: 15px;
}

.usage-project-heading > div {
  min-width: 0;
}

.usage-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin: 0;
  color: var(--ink-soft);
  font-size: 12px;
}

.usage-summary strong {
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}

.usage-project-balance strong {
  margin-inline-start: .25em;
}

.usage-review,
.usage-key-models .usage-review {
  margin: 4px 0 0;
  color: #835615;
  font-size: 12px;
}

.usage-key-list {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.usage-key-list > li {
  min-width: 0;
  border-top: 1px solid var(--line);
}

.usage-key-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 2.6fr) auto;
  grid-template-areas: 'name totals action';
  align-items: center;
  gap: 10px 14px;
  padding: 12px 0;
}

.usage-key-totals { grid-area: totals; }
.usage-key-row > button { grid-area: action; }

.usage-key-name {
  grid-area: name;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  min-width: 0;
  font-size: 13px;
}

.usage-key-name strong {
  min-width: 0;
  overflow-wrap: anywhere;
}

.usage-key-totals,
.usage-key-details,
.usage-receipt-meta,
.usage-receipt-tokens {
  display: grid;
  gap: 8px;
  margin: 0;
  min-width: 0;
}

.usage-key-totals {
  grid-template-columns: minmax(0, 1.4fr) repeat(2, minmax(0, 1fr));
  overflow-wrap: anywhere;
}

.usage-key-details {
  grid-template-columns: repeat(auto-fit, minmax(min(125px, 100%), 1fr));
  overflow-wrap: anywhere;
  margin-bottom: 8px;
}

.usage-key-row dt,
.usage-key-details dt,
.usage-receipt-result dt {
  font-size: 11px;
  color: var(--ink-soft);
}

.usage-key-row dd,
.usage-key-details dd,
.usage-receipt-result dd {
  margin: 3px 0 0;
  font-size: 13px;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
}

.usage-key-models {
  padding: 10px 12px;
  margin-bottom: 12px;
  border-radius: 10px;
  background: var(--surface);
}

.usage-key-models > p {
  margin: 4px 0;
}

.usage-disclosure {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  min-height: 36px;
  white-space: nowrap;
}

.usage-disclosure:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: 2px;
}

.usage-disclosure[aria-expanded="true"] svg {
  transform: rotate(180deg);
}

.usage-chart {
  margin: 0 14px 12px;
}

.usage-chart :deep(.credit-legend-name) {
  white-space: normal;
  overflow-wrap: anywhere;
}

.usage-recent {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.usage-recent p {
  margin: 0;
}

.usage-recent-empty {
  font-size: 13px;
  line-height: 1.5;
}

.usage-recent-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.usage-recent-list li {
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: 12px;
}

.usage-recent-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, .75fr) auto;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 12px;
  border: 0;
  border-radius: inherit;
  background: transparent;
  color: var(--ink);
  cursor: pointer;
  text-align: start;
  overflow-wrap: anywhere;
}

.usage-recent-row:hover {
  background: var(--surface);
}

.usage-recent-row:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: -2px;
}

.usage-recent-row time,
.usage-recent-row small {
  display: block;
  margin-top: 4px;
  font-size: 12px;
}

.usage-recent-amount {
  text-align: end;
  font-variant-numeric: tabular-nums;
}

.usage-recent-row[aria-expanded="true"] svg {
  transform: rotate(180deg);
}

.usage-recent-detail {
  margin: 0 12px 12px;
  padding-top: 12px;
}

.usage-recent-more {
  justify-self: end;
}

.usage-recent-more,
.usage-recent .range-switch button,
.usage-receipt-form .button {
  min-height: 44px;
}

:global(.dialog-content:has(> .usage-recent, > .usage-receipt-form)) {
  max-height: calc(100dvh - 28px);
}

.usage-receipt-form {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: 8px;
}

.usage-receipt-feedback {
  margin: 12px 0 0;
}

.usage-receipt-result {
  display: grid;
  gap: 12px;
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px solid var(--line);
}

.usage-receipt-meta {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.usage-receipt-status {
  grid-column: 1 / -1;
}

.usage-receipt-tokens {
  grid-template-columns: repeat(auto-fit, minmax(64px, 1fr));
  padding: 10px;
  border-radius: 10px;
  background: var(--surface);
}

.usage-evidence {
  margin: 0;
  font-size: 12px;
}

@media (max-width: 900px) {
  .usage-model-table thead {
    display: table-header-group;
    position: absolute;
    width: 1px;
    height: 1px;
    clip-path: inset(50%);
    overflow: hidden;
  }

  .usage-model-table tfoot {
    display: block;
  }

  .usage-model-table tbody tr,
  .usage-model-table tfoot tr {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    padding: 8px 0;
  }

  .usage-model-table th[scope="row"],
  .usage-model-table td:last-child {
    grid-column: 1 / -1;
  }

  .usage-model-table td::before {
    content: attr(data-label);
    display: block;
    color: var(--ink-soft);
    font-size: 11px;
    font-weight: 500;
  }
}

@media (max-width: 560px) {
  .usage-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .usage-key-row {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas: 'name action' 'totals totals';
    gap: 8px 6px;
  }

  .usage-heading .usage-disclosure,
  .usage-key-row .usage-disclosure,
  .usage-receipt-form input {
    min-height: 44px;
  }

  .usage-receipt-form {
    grid-template-columns: minmax(0, 1fr);
  }

  .usage-receipt-form .button {
    justify-self: end;
  }
}

@media (prefers-reduced-motion: reduce) {
  /* 保留弹窗的静态居中定位，减少动态效果时仍只过渡透明度。 */
  :global(.dialog-content:has(> .usage-recent, > .usage-receipt-form)) {
    transform: translate(-50%, -50%) !important;
  }
}

@media (max-width: 900px) and (prefers-reduced-motion: reduce) {
  /* 侧栏不滑出屏幕时，隐藏它的点击区域和焦点入口。 */
  :global(.app-shell:has(.student-usage) > .sidebar:not(.open)) {
    visibility: hidden;
  }
}
</style>
