<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { ChevronDown, Search } from 'lucide-vue-next'
import { APIError, apiErrorMessage, get } from '../../api/client'
import { studentAPI } from '../../api/student'
import AppDialog from '../../components/AppDialog.vue'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { formatPercent, formatTokens } from '../../utils/credits'
import { quotaBalance, schoolMonth, statusLabel } from '../../utils/format'
import {
  formatUsageCacheCoverage,
  formatUsageCredits,
  formatUsageModelNotes,
  formatUsagePendingReview,
  formatUsageTokenCount,
  sumStudentProjectCredits,
  type StudentCallReceipt,
  type StudentUsageModelMetrics,
  type StudentUsageProject,
  type StudentUsageReport,
} from '../../utils/studentUsage'

const pageId = useId()
const month = ref(schoolMonth())
const monthTitle = computed(() => {
  const [year, monthNumber] = month.value.split('-')
  return `${year} 年 ${Number(monthNumber)} 月个人用量`
})
const report = ref<StudentUsageReport | null>(null)
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
    // 学生用量客户端也供只读取项目和 Key 的页面使用。
    const data = await studentAPI.usage(requestedMonth) as StudentUsageReport
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

const modelSummary = computed(() => {
  const data = report.value
  // 项目月桶与模型账本是独立的，缺少模型汇总时仍可查看额度。
  if (!data?.totals || !Array.isArray(data.models)) return null
  return { models: data.models, totals: data.totals }
})

const creditStats = computed(() => {
  if (!report.value) return []
  const { projects } = report.value
  return [
    { id: 'quota', label: '月额度', amount: sumStudentProjectCredits(projects, 'quota') },
    { id: 'used', label: '已扣积分', amount: modelSummary.value?.totals.credits },
    { id: 'pending', label: '在途积分', amount: sumStudentProjectCredits(projects, 'in_flight_credits') },
    { id: 'available', label: '可用积分', amount: sumStudentProjectCredits(projects, 'available') },
  ]
})

const projectSummaries = computed(() => (report.value?.projects ?? []).map(project => ({
  project,
  balance: quotaBalance(project.quota, project.used, project.available),
})))

const modelColumns: Array<{
  label: string
  value: (model: StudentUsageModelMetrics) => string
  detail?: (model: StudentUsageModelMetrics) => string
  numeric?: boolean
}> = [
  { label: '已扣积分', value: model => formatUsageCredits(model.credits), numeric: true },
  { label: '调用次数', value: model => formatTokens(model.calls ?? 0), numeric: true },
  { label: 'Token', value: model => formatUsageTokenCount(model.tokens.total), numeric: true },
  {
    label: '缓存命中率',
    value: model => formatPercent(model.cache_hit_rate),
    detail: formatUsageCacheCoverage,
    numeric: true,
  },
  { label: '说明', value: formatUsageModelNotes },
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

function projectSegments(project: StudentUsageProject) {
  return [
    ...project.keys.map(key => ({ name: key.name, value: Number(key.used), displayValue: formatUsageCredits(key.used) })),
    {
      name: '在途积分', value: Number(project.in_flight_credits),
      displayValue: formatUsageCredits(project.in_flight_credits), color: '#e5a93f',
    },
    {
      name: '可用积分', value: Number(project.available),
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
    const data = await get<StudentCallReceipt>(`/api/v1/student/usage/calls/${encodeURIComponent(query)}`)
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
const receiptTime = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', dateStyle: 'medium', timeStyle: 'medium',
})
const receiptTokenFields = [
  { field: 'input_tokens', label: '输入' },
  { field: 'output_tokens', label: '输出' },
  { field: 'cache_read_tokens', label: '缓存读' },
  { field: 'cache_write_tokens', label: '缓存写' },
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
  if (applied === null) return '暂无匹配回执'
  return applied ? '已用回执补记' : '已匹配回执，未用于补记'
}
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
                <dd><time :datetime="receipt.created_at">{{ receiptTime.format(new Date(receipt.created_at)) }}</time></dd>
              </div>
              <div><dt>已扣积分</dt><dd>{{ formatUsageCredits(receipt.charged_credits) }}</dd></div>
              <div><dt>结算状态</dt><dd>{{ billingStateLabels[receipt.billing_state] ?? statusLabel(receipt.billing_state) }}</dd></div>
              <div><dt>渠道</dt><dd>{{ receipt.channel }}</dd></div>
              <div class="usage-receipt-status"><dt>回执状态</dt><dd>{{ receiptAppliedLabel(receipt.receipt_applied) }}</dd></div>
            </dl>
            <dl class="usage-receipt-tokens" aria-label="Token 明细">
              <div v-for="{ field, label } in receiptTokenFields" :key="field">
                <dt>{{ label }}</dt><dd>{{ formatUsageTokenCount(receipt[field]) }}</dd>
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

        <section class="panel usage-panel" :aria-labelledby="`${pageId}-models`">
          <header class="beichen-section-head">
            <h2 :id="`${pageId}-models`">按模型合计</h2>
            <RouterLink class="usage-model-link" :to="{ name: 'student-models' }">到模型广场添加模型</RouterLink>
          </header>
          <div v-if="!modelSummary" class="usage-load-error">
            <p class="error" role="alert">模型汇总读取失败。返回数据缺少汇总字段。</p>
            <button class="button secondary" type="button" @click="load">重试</button>
          </div>
          <template v-else-if="modelSummary.models.length">
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
                  <tr v-for="model in modelSummary.models" :key="model.id">
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
                <tfoot>
                  <tr>
                    <th scope="row">合计</th>
                    <td v-for="column in modelColumns" :key="column.label" :data-label="column.label"
                      :class="{ 'numeric-cell': column.numeric }">
                      {{ column.value(modelSummary.totals) }}
                      <small v-if="column.detail">{{ column.detail(modelSummary.totals) }}</small>
                    </td>
                  </tr>
                </tfoot>
              </table>
            </div>
          </template>
          <EmptyState v-else title="所选月暂无模型用量" />
        </section>

        <section class="panel usage-panel" :aria-labelledby="`${pageId}-keys`">
          <h2 :id="`${pageId}-keys`">按 Key 明细</h2>
          <EmptyState v-if="!projectSummaries.length" title="所选月暂无项目和 Key 记录" />
          <article v-for="{ project, balance } in projectSummaries" :key="project.project_id" class="usage-project">
            <header class="usage-project-heading">
              <div>
                <h3>{{ project.project_name }}</h3>
                <div class="usage-summary">
                  <span>月额度 <strong>{{ formatUsageCredits(project.quota) }}</strong></span>
                  <span>学校已用 <strong>{{ formatUsageCredits(project.used) }}</strong></span>
                  <span>在途 <strong>{{ formatUsageCredits(project.in_flight_credits) }}</strong></span>
                  <span class="usage-project-balance">{{ balance.label }}<strong>{{ formatUsageCredits(balance.amount) }}</strong></span>
                  <span>已结算 {{ formatTokens(project.calls) }} 次</span>
                </div>
                <p v-if="project.pending_review_calls > 0" class="usage-review">{{ formatUsagePendingReview(project) }}</p>
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
                :segments="projectSegments(project)" :total="Number(project.quota)" unit="积分"
                :label="`${project.project_name} 各 Key 已扣积分与额度`" />
            </div>

            <p v-if="!project.keys.length" class="muted usage-no-keys">暂无 Key 记录</p>
            <template v-for="key in project.keys" :key="keySectionId(project.project_id, key.id)">
              <div class="usage-key-row" data-testid="usage-key-row">
                <div class="usage-key-identity">
                  <div class="usage-key-name">
                    <strong>{{ key.name }}</strong>
                    <span class="status-badge" :class="key.status">{{ statusLabel(key.status) }}</span>
                  </div>
                  <small v-if="key.pending_review_calls > 0" class="usage-review">{{ formatUsagePendingReview(key) }}</small>
                </div>
                <dl class="usage-key-credits">
                  <div><dt>已扣积分</dt><dd>{{ formatUsageCredits(key.used) }}</dd></div>
                  <div><dt>在途积分</dt><dd>{{ formatUsageCredits(key.in_flight_credits) }}</dd></div>
                  <div><dt>可用积分</dt><dd>{{ formatUsageCredits(key.available) }}</dd></div>
                </dl>
                <dl class="usage-key-metrics">
                  <div><dt>调用次数</dt><dd>{{ formatTokens(key.calls) }}</dd></div>
                  <div>
                    <dt>Token</dt><dd>{{ formatUsageTokenCount(key.tokens.total) }}</dd>
                    <small v-if="key.token_coverage.calls && key.completeness !== 'complete'">
                      {{ key.completeness === 'partial' ? '部分回报' : '无回报' }}
                    </small>
                  </div>
                  <div><dt>模型数</dt><dd>{{ formatTokens(key.models.length) }}</dd></div>
                </dl>
                <div class="usage-key-action">
                  <button v-if="key.models.length" class="button ghost usage-disclosure" type="button"
                    :aria-expanded="expandedSections.has(keySectionId(project.project_id, key.id))"
                    :aria-controls="keySectionId(project.project_id, key.id)"
                    :aria-label="`${expandedSections.has(keySectionId(project.project_id, key.id)) ? '收起' : '展开'} ${key.name} 的模型明细`"
                    @click="toggleSection(keySectionId(project.project_id, key.id))">
                    {{ expandedSections.has(keySectionId(project.project_id, key.id)) ? '收起明细' : '展开明细' }}
                    <ChevronDown :size="14" aria-hidden="true" />
                  </button>
                  <span v-else class="muted">暂无模型记录</span>
                </div>
              </div>
              <div v-if="key.models.length" v-show="expandedSections.has(keySectionId(project.project_id, key.id))"
                :id="keySectionId(project.project_id, key.id)" class="beichen-table-wrap usage-key-models">
                <table v-if="expandedSections.has(keySectionId(project.project_id, key.id))" class="beichen-table usage-model-table">
                  <caption class="sr-only">{{ key.name }} 的模型用量</caption>
                  <thead>
                    <tr>
                      <th scope="col">模型</th>
                      <th v-for="column in modelColumns" :key="column.label" scope="col"
                        :class="{ 'numeric-cell': column.numeric }">{{ column.label }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="model in key.models" :key="model.id">
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
            </template>
          </article>
        </section>
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
.usage-key-row small {
  display: block;
  font-size: 11px;
  font-weight: 400;
  color: var(--ink-soft);
  line-height: 1.45;
}

.usage-model-table small {
  margin-top: 3px;
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
.usage-key-row .usage-review {
  margin: 4px 0 0;
  color: #835615;
  font-size: 12px;
}

.usage-no-keys {
  margin: 0;
  padding: 10px 14px 14px;
}

.usage-key-row {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(0, 2.45fr) minmax(0, 1.65fr) auto;
  grid-template-areas: 'identity credits metrics action';
  align-items: start;
  gap: 10px 14px;
  padding: 12px 14px;
  border-top: 1px solid var(--line);
}

.usage-key-identity { grid-area: identity; }
.usage-key-credits { grid-area: credits; }
.usage-key-metrics { grid-area: metrics; }
.usage-key-action { grid-area: action; }

.usage-key-name {
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

.usage-key-credits,
.usage-key-metrics,
.usage-receipt-meta,
.usage-receipt-tokens {
  display: grid;
  gap: 8px;
  margin: 0;
  min-width: 0;
}

.usage-key-credits,
.usage-key-metrics {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.usage-key-row dt,
.usage-receipt-result dt {
  font-size: 11px;
  color: var(--ink-soft);
}

.usage-key-row dd,
.usage-receipt-result dd {
  margin: 3px 0 0;
  font-size: 13px;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
}

.usage-key-action > span {
  font-size: 12px;
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

.usage-key-models,
.usage-chart {
  margin: 0 14px 12px;
}

.usage-chart :deep(.credit-legend-name) {
  white-space: normal;
  overflow-wrap: anywhere;
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

@media (max-width: 1100px) {
  .usage-key-row {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas: 'identity identity' 'credits credits' 'metrics action';
    align-items: center;
  }
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

  .usage-key-credits {
    padding: 8px 10px;
    border-radius: 8px;
    background: var(--surface);
  }

  .usage-key-row {
    grid-template-areas: 'identity action' 'credits credits' 'metrics metrics';
    gap: 8px 6px;
  }

  .usage-key-row dt,
  .usage-key-row dd {
    font-size: 11px;
  }
}

@media (prefers-reduced-motion: reduce) {
  /* 保留弹窗的静态居中定位，减少动态效果时仍只过渡透明度。 */
  :global(.dialog-content:has(> .usage-receipt-form)) {
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
