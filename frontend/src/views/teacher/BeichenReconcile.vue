<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, useId, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenReconcile, BeichenReconcileModel, BeichenReconcileSide, BeichenUsageRange } from '../../api/types'
import CompareBars from '../../components/CompareBars.vue'
import CreditDonut from '../../components/CreditDonut.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { creditsToYuan, formatCredits, formatPercent, formatTokens, formatYuan, shiftDecimal } from '../../utils/credits'

type TokenSide = BeichenReconcileSide
type Margin = Pick<BeichenReconcile['totals']['money'], 'margin_available' | 'margin_yuan'>
type Channel = Pick<BeichenReconcileModel, 'credential_id' | 'label'>

const props = defineProps<{ providerId: string }>()
const titleId = useId()
const range = ref<BeichenUsageRange>('all')
const ranges: Array<{ value: BeichenUsageRange; label: string }> = [
  { value: 'day', label: '当天' }, { value: 'month', label: '本月' }, { value: 'all', label: '总计' },
]
const metric = ref<'tokens' | 'money'>('tokens')
const report = shallowRef<BeichenReconcile>()
const strict = ref(false)
const data = computed(() => strict.value ? report.value?.comparable_only : report.value)
const unmatched = computed(() => {
  if (!report.value?.unmatched_upstream) return []
  const { unmatched_upstream, coverage } = report.value
  if (!strict.value) return unmatched_upstream
  const dates = new Set(coverage.eligible_dates)
  // occurred_at is supplied in Beijing time with an explicit +08:00 offset.
  return unmatched_upstream.filter(row => dates.has(row.occurred_at.slice(0, 10)))
})
const page = ref(1)
const pageSize = 50
const pageRows = computed(() => unmatched.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch([strict, report], () => { page.value = 1 })
const currencyMismatch = computed(() => {
  const side = data.value?.totals.upstream
  return !!side && (!!side.mixed_units || (side.unit != null && side.unit !== 'CNY'))
})
const moneyUnit = computed(() => currencyMismatch.value ? '元（校内积分折算）' : '元（北辰余额单位）')
const chartMetric = computed(() => currencyMismatch.value ? 'tokens' : metric.value)
const loading = ref(true)
const error = ref('')
const models = computed(() => data.value?.models ?? [])
const keys = computed(() => data.value?.keys ?? [])
const empty = computed(() => !models.value.length && !keys.value.length && !unmatched.value.length)
const palette = ['#0071e3', '#168473', '#b77916', '#b44d76', '#7657b7', '#427886', '#646f80']
const matchLabels: Record<BeichenReconcileModel['match_status'], string> = {
  matched: '两边均有记录', local_only: '仅本地有记录', upstream_only: '仅北辰有记录',
}
const completenessLabels: Record<TokenSide['completeness'], string> = {
  complete: 'Token 回报完整', partial: 'Token 部分回报', missing: 'Token 待核',
}
const coverageLabels: Record<NonNullable<BeichenReconcile['coverage']['coverage_kind']>, string> = {
  full: '同步完整', partial: '部分同步', none: '暂无已同步账日',
}
const exclusionLabels: Record<string, string> = {
  open_day: '账日尚未结束', incomplete_sync: '渠道未同步完整', stale: '账单已陈旧',
  missing_cost: '缺少实扣金额', incompatible_unit: '金额单位不可比',
}
const period = computed(() => {
  if (!report.value) return ''
  const end = beijingDate(report.value.end)
  return report.value.start ? `${beijingDate(report.value.start)} 起 · 截至 ${end}（北京时间）` : `全部已记录数据 · 截至 ${end}（北京时间）`
})
const localSegments = computed(() => models.value.map((row, index) => ({
  name: modelLabel(row), value: row.local.tokens, color: palette[index % palette.length], displayValue: formatPercent(row.local_share),
})))
const upstreamSegments = computed(() => models.value.map((row, index) => ({
  name: modelLabel(row), value: row.upstream.tokens, color: palette[index % palette.length], displayValue: formatPercent(row.upstream_share),
})))
const bars = computed(() => ({
  categories: models.value.map(modelLabel),
  left: models.value.map(row => chartMetric.value === 'tokens' ? reportedTokens(row.local) : chartNumber(row.student_yuan)),
  right: models.value.map(row => chartMetric.value === 'tokens' ? reportedTokens(row.upstream) : chartNumber(row.actual_yuan)),
}))
const yuan = (value: string | number | null | undefined) => formatYuan(value, 6)
let requestVersion = 0

function channelLabel(row: Channel) {
  return row.label || `渠道 ${row.credential_id.slice(0, 8)}`
}

function modelLabel(row: BeichenReconcileModel) {
  return `${row.model} · ${channelLabel(row)}`
}

function reportedTokens(side: TokenSide) {
  return side.completeness === 'complete' || side.tokens > 0 ? side.tokens : null
}

function actualAmount(side: TokenSide, legacy: string | null, withUnit = true) {
  if (side.mixed_units) {
    return side.amounts_by_unit?.map(row => (row.actual_cost ?? '—') + ' ' + (row.unit ?? '历史单位')).join(' / ') || '多种单位'
  }
  if (side.unit == null) return yuan(legacy)
  if (side.actual_cost == null) return '—'
  return withUnit ? side.actual_cost + ' ' + side.unit : side.actual_cost
}

function sourceLabel(side: TokenSide) {
  const labels: Record<string, string> = { key_usage: 'Key 公开用量', website_receipts: '网页流水', usage: '北辰日账', export: '明细导入', csv: '用量导入' }
  const sources = side.source?.split(',').map(source => labels[source] ?? source).join('、')
  return [sources, side.stale ? '已陈旧' : null].filter(Boolean).join(' · ')
}

function schoolAbsorbed(value: string | null | undefined) {
  if (value == null) return '未统计'
  const amount = shiftDecimal(value, -2)
  return amount === '0' ? '未触顶' : amount
}

function beijingDate(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime())
    ? new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(date)
    : '—'
}

function chartNumber(value: string | null | undefined) {
  if (value == null || value.trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

function marginClass(line: Margin) {
  const number = line.margin_available ? chartNumber(line.margin_yuan) : null
  return number != null && number > 0 ? 'positive' : number != null && number < 0 ? 'negative' : ''
}

function margin(line: Margin) {
  if (!line.margin_available) return '不可比'
  return `${(chartNumber(line.margin_yuan) ?? 0) > 0 ? '+' : ''}${yuan(line.margin_yuan)}`
}

async function load() {
  const current = ++requestVersion
  loading.value = true
  error.value = ''
  report.value = undefined
  try {
    const result = await teacherAPI.beichenReconciliation(props.providerId, range.value)
    // 切换范围或供应商后，过期响应不能覆盖当前对账结果。
    if (current === requestVersion) report.value = result
  } catch (e) {
    if (current === requestVersion) error.value = apiErrorMessage(e, '北辰对账读取失败，请重试。')
  } finally {
    if (current === requestVersion) loading.value = false
  }
}

watch([() => props.providerId, range], () => { void load() }, { immediate: true })
onBeforeUnmount(() => { requestVersion++ })
</script>

<template>
  <section class="beichen-stack reconcile" :aria-labelledby="titleId">
    <header class="beichen-toolbar reconcile-header">
      <div>
        <h2 :id="titleId">北辰对账</h2>
        <p class="beichen-note">100 积分折合 1 {{ moneyUnit }}。学校承担只统计超过学生额度上限的部分。</p>
      </div>
      <div class="beichen-toolbar-group">
        <div class="range-switch" role="group" aria-label="对账时间范围">
          <button v-for="item in ranges" :key="item.value" type="button" :aria-pressed="range === item.value" @click="range = item.value">{{ item.label }}</button>
        </div>
        <button type="button" class="button secondary" :disabled="loading" @click="load">{{ loading ? '读取中…' : '刷新对账' }}</button>
      </div>
    </header>

    <div v-if="error" class="reconcile-error" role="alert">
      <p>{{ error }}</p><button type="button" class="button secondary" @click="load">重试</button>
    </div>
    <LoadingRegion :initial-loading="loading" variant="cards" label="正在读取北辰对账" :rows="2">
      <div v-if="report" class="beichen-stack">
        <div class="range-switch" role="group" aria-label="账日口径">
          <button type="button" :aria-pressed="!strict" @click="strict = false">全部已观察</button>
          <button type="button" :aria-pressed="strict" @click="strict = true">仅完整可比账日</button>
        </div>
        <p class="beichen-note">{{ period }}</p>
        <p v-if="strict" class="beichen-note">仅汇总已经结束、所有应同步渠道均有新鲜账单且实扣金额可按元比较的北京时间账日。</p>
        <p v-if="strict && !data" class="beichen-empty" role="status">当前范围无完整可比账日，差额不可比。</p>
        <details v-if="report.coverage.excluded_days?.length" class="beichen-note">
          <summary>未纳入严格汇总的账日（{{ report.coverage.excluded_days.length }} 天）</summary>
          <ul><li v-for="day in report.coverage.excluded_days" :key="day.date">{{ day.date }} · {{ day.reasons.map(reason => exclusionLabels[reason] ?? reason).join('、') }}</li></ul>
        </details>
        <template v-if="data">
        <div class="beichen-summary reconcile-kpis" aria-label="对账汇总">
          <div class="beichen-stat" data-kpi="local-tokens">
            <span>本地 Token 总量</span>
            <strong>{{ formatTokens(data.totals.local.requests === 0 ? 0 : reportedTokens(data.totals.local)) }}</strong>
            <small>学校网关已知用量，含待归类 Token</small>
          </div>
          <div class="beichen-stat" data-kpi="upstream-tokens">
            <span>北辰 Token 总量</span>
            <strong>{{ formatTokens(data.totals.upstream.requests === 0 && data.totals.upstream.source == null ? 0 : reportedTokens(data.totals.upstream)) }}</strong>
            <small>北辰回报 {{ formatTokens(data.totals.upstream.requests) }} 笔调用</small>
          </div>
          <div class="beichen-stat" data-kpi="student-yuan">
            <span>学生已扣</span><strong>{{ yuan(data.totals.money.student_yuan) }}</strong>
            <small v-if="data.totals.local.credits != null">{{ formatCredits(data.totals.local.credits, 6) }} 积分 ÷ 100 = {{ creditsToYuan(data.totals.local.credits, 6) }} {{ moneyUnit }}</small>
            <small v-else>已扣积分 ÷ 100</small>
          </div>
          <div class="beichen-stat" data-kpi="actual-yuan" :data-source="data.totals.upstream.source">
            <span>北辰实扣</span><strong>{{ actualAmount(data.totals.upstream, data.totals.money.actual_yuan, false) }}</strong>
            <small>{{ data.totals.upstream.unit ?? (data.totals.upstream.mixed_units ? '见渠道明细' : moneyUnit) }}</small>
            <small v-if="data.totals.upstream.source?.includes('key_usage') || data.totals.upstream.source?.includes('website_receipts')">{{ sourceLabel(data.totals.upstream) }}</small>
          </div>
          <div class="beichen-stat" data-kpi="margin-yuan"><span>差额</span><strong :class="marginClass(data.totals.money)">{{ margin(data.totals.money) }}</strong><small>学生已扣折合金额 − 北辰实扣</small></div>
        </div>

        <section class="beichen-card coverage-card" aria-label="日账覆盖">
          <div class="beichen-section-head">
            <h3>账单同步</h3>
            <span v-if="data.coverage.coverage_kind" class="status-pill" :class="data.coverage.coverage_kind === 'full' ? 'confirmed' : 'pending'">
              {{ coverageLabels[data.coverage.coverage_kind] }}
            </span>
          </div>
          <dl class="coverage-days">
            <div>
              <dt>完整账日</dt>
              <dd>{{ formatTokens(data.coverage.comparable_days) }} 天 <small>当天应同步的渠道均已同步</small></dd>
            </div>
            <div>
              <dt>渠道账日</dt>
              <dd>{{ formatTokens(data.coverage.days_present) }} / {{ formatTokens(data.coverage.days_expected) }} <small>已同步 / 应同步，每渠道每天计一次</small></dd>
            </div>
          </dl>
          <p v-if="strict" class="beichen-note">纳入账日 {{ data.coverage.eligible_dates.join('、') }}</p>
          <p v-if="data.coverage.key_usage_days" class="beichen-note">
            Key 公开用量覆盖 {{ data.coverage.key_usage_days }} 个渠道账日，模型明细已核对 {{ data.coverage.key_usage_model_days ?? 0 }} / {{ data.coverage.key_usage_days }}（{{ formatPercent(String((data.coverage.key_usage_model_days ?? 0) / data.coverage.key_usage_days)) }}）。
            <template v-if="data.coverage.stale_key_usage_days">{{ data.coverage.stale_key_usage_days }} 个渠道账日保留上次数据（已陈旧）。</template>
          </p>
          <p v-if="data.coverage.missing_days.length" class="coverage-warning">待同步日期 {{ data.coverage.missing_days.join('、') }}。当前区间差额不可比。</p>
          <p v-if="currencyMismatch" class="coverage-warning">校内金额按元折算，北辰实扣保留原单位，当前区间差额不可比。</p>
          <p v-else-if="data.coverage.coverage_kind === 'full' && !data.coverage.comparable" class="coverage-warning">缺少可核对的北辰实扣金额，差额不可比。</p>
          <p v-if="data.coverage.multiplier_mixed" class="coverage-warning">当前范围包含不同学校扣费倍率，不能统一按 1.10 倍推算差额。</p>
          <p class="beichen-note">
            基础成本 {{ yuan(data.totals.money.base_yuan) }} {{ moneyUnit }}<template v-if="data.totals.money.margin_available && data.totals.money.margin_rate != null">，差额率 {{ formatPercent(data.totals.money.margin_rate) }}</template>。
          </p>
        </section>

        <section class="beichen-card coverage-card" aria-label="调用核对">
          <h3>调用核对</h3>
          <div class="coverage-facts">
            <strong>可逐笔关联 {{ formatTokens(data.coverage.verified_matched_calls) }} 笔，其中本地 Token 完整 {{ formatTokens(data.coverage.verified_matched_complete_token_calls) }} 笔</strong>
            <span :class="{ 'coverage-warning': data.totals.local.charged_tokens_missing_calls > 0 }">已扣款但 Token 不完整 {{ formatTokens(data.totals.local.charged_tokens_missing_calls) }} 笔</span>
            <span :class="{ 'coverage-warning': data.coverage.pending_calls > 0 }">待核账请求 {{ formatTokens(data.coverage.pending_calls) }} 笔</span>
            <span :class="{ 'coverage-warning': data.coverage.unpriced_calls > 0 }">无报价调用 {{ formatTokens(data.coverage.unpriced_calls) }} 笔</span>
          </div>
          <p v-if="data.totals.local.tokens_missing > 0" class="beichen-note">已计入本地总量、待归类 {{ formatTokens(data.totals.local.tokens_missing) }} Token。</p>
        </section>

        <section class="beichen-card reconcile-detail" aria-label="北辰有、本地无">
          <h3>北辰有、本地无</h3>
          <p class="beichen-note">以下北辰归档流水尚未关联到已验证的本地扣款。超时或待核的请求仍可能有本地调用记录。</p>
          <div v-if="unmatched.length" class="reconcile-table-wrap">
            <table class="beichen-table reconcile-table">
              <caption>已归档未关联 {{ formatTokens(unmatched.length) }} 笔</caption>
              <thead><tr><th>北京时间</th><th>模型</th><th>渠道</th><th>普通输入</th><th>输出</th><th>缓存读</th><th>缓存写</th><th>北辰请求号</th><th>回执号</th></tr></thead>
              <tbody>
                <tr v-for="row in pageRows" :key="row.upstream_user_id + ':' + row.row_id">
                  <th scope="row">{{ row.occurred_at.slice(0, 19).replace('T', ' ') }}</th>
                  <td data-label="模型">{{ row.model }}</td><td data-label="渠道">{{ channelLabel(row) }}</td>
                  <td data-label="普通输入">{{ formatTokens(row.input_tokens) }}</td><td data-label="输出">{{ formatTokens(row.output_tokens) }}</td>
                  <td data-label="缓存读">{{ formatTokens(row.cache_read_tokens) }}</td><td data-label="缓存写">{{ formatTokens(row.cache_write_tokens) }}</td>
                  <td data-label="北辰请求号">{{ row.request_id || '—' }}</td><td data-label="回执号">{{ row.receipt_id || '—' }}</td>
                </tr>
              </tbody>
            </table>
            <nav v-if="unmatched.length > pageSize" aria-label="未关联流水翻页">
              <button type="button" :disabled="page === 1" @click="page--">上一页</button>
              第 {{ page }} / {{ Math.ceil(unmatched.length / pageSize) }} 页
              <button type="button" :disabled="page * pageSize >= unmatched.length" @click="page++">下一页</button>
            </nav>
          </div>
          <p v-else class="beichen-note">所选账日的已归档流水中暂无未关联记录。仅有日汇总的调用无法逐笔核对。</p>
        </section>

        <div v-if="empty" class="beichen-empty" role="status">
          <strong>当前范围暂无对账记录</strong>
          <span v-if="data.coverage.coverage_kind !== 'full'">先到「查看模型」同步北辰数据，完成后返回并点击「刷新对账」。</span>
          <span v-else-if="range !== 'all'">账单已同步，可切换到「总计」查看其他账日。</span>
          <span v-else>账单已同步，当前没有记录可对账。</span>
        </div>
        <template v-if="models.length">
          <div class="compare-grid reconcile-donuts">
            <section class="compare-card">
              <h3>本地 Token 模型占比</h3>
              <CreditDonut v-if="data.totals.local.tokens > 0" :segments="localSegments" :total="data.totals.local.tokens" label="本地 Token 模型占比" unit="Token" />
              <p v-else class="beichen-empty">当前范围没有本地 Token 记录。</p>
            </section>
            <section class="compare-card">
              <h3>北辰 Token 模型占比</h3>
              <CreditDonut v-if="data.totals.upstream.tokens > 0" :segments="upstreamSegments" :total="data.totals.upstream.tokens" label="北辰 Token 模型占比" unit="Token" />
              <p v-else class="beichen-empty">当前范围没有北辰 Token 记录。</p>
            </section>
          </div>

          <section class="beichen-card compare-section">
            <div class="beichen-section-head">
              <h3>按模型对比</h3>
              <div class="range-switch" role="group" aria-label="对账图表指标">
                <button type="button" :aria-pressed="chartMetric === 'tokens'" @click="metric = 'tokens'">Token 对比</button>
                <button type="button" :aria-pressed="chartMetric === 'money'" :disabled="currencyMismatch" @click="metric = 'money'">金额对比</button>
              </div>
            </div>
            <CompareBars :categories="bars.categories" :left="bars.left" :right="bars.right" :left-label="chartMetric === 'tokens' ? '本地 Token' : '学生已扣'" :right-label="chartMetric === 'tokens' ? '北辰 Token' : '北辰实扣'" :value-formatter="chartMetric === 'tokens' ? formatTokens : yuan" :unit="chartMetric === 'tokens' ? 'Token' : moneyUnit" />
          </section>

          <section class="beichen-card reconcile-detail">
            <h3>模型对账明细</h3>
            <p class="beichen-note">每行对应一条渠道的上游模型。两侧占比按各自已回报的 Token 总量计算。<template v-if="currencyMismatch">北辰金额显示原单位。</template><template v-else>金额单位为{{ moneyUnit }}。</template></p>
            <div class="reconcile-table-wrap">
              <table class="beichen-table reconcile-table model-details">
                <caption class="sr-only">模型对账明细，金额单位为{{ moneyUnit }}</caption>
                <thead><tr>
                  <th scope="col">模型</th>
                  <th scope="col" class="numeric-cell">本地 Token</th>
                  <th scope="col" class="numeric-cell">北辰 Token</th>
                  <th scope="col" class="numeric-cell">本地占比</th>
                  <th scope="col" class="numeric-cell">北辰占比</th>
                  <th scope="col" class="numeric-cell">学生已扣</th>
                  <th scope="col" class="numeric-cell">北辰实扣</th>
                  <th scope="col" class="numeric-cell">学校承担</th>
                  <th scope="col" class="numeric-cell">差额</th>
                  <th scope="col">记录情况</th>
                </tr></thead>
                <tbody>
                  <tr v-for="row in models" :key="`${row.credential_id}:${row.model}`" :class="{ 'unmatched-row': row.match_status !== 'matched' }">
                    <th scope="row">{{ row.model }}<small class="beichen-note model-channel">{{ channelLabel(row) }}</small></th>
                    <td class="numeric-cell" data-label="本地 Token">{{ formatTokens(reportedTokens(row.local)) }}</td>
                    <td class="numeric-cell" data-label="北辰 Token">{{ formatTokens(reportedTokens(row.upstream)) }}</td>
                    <td class="numeric-cell" data-label="本地占比">{{ formatPercent(reportedTokens(row.local) == null ? null : row.local_share) }}</td>
                    <td class="numeric-cell" data-label="北辰占比">{{ formatPercent(reportedTokens(row.upstream) == null ? null : row.upstream_share) }}</td>
                    <td class="numeric-cell" data-label="学生已扣">{{ yuan(row.student_yuan) }}</td>
                    <td class="numeric-cell" data-label="北辰实扣">{{ actualAmount(row.upstream, row.actual_yuan) }}</td>
                    <td class="numeric-cell" data-label="学校承担">{{ schoolAbsorbed(row.school_absorbed_credits) }}</td>
                    <td class="numeric-cell" :class="marginClass(row)" data-label="差额">{{ margin(row) }}</td>
                    <td data-label="记录情况">
                      <div class="reconcile-row-status">
                        <span class="status-pill" :class="row.match_status === 'matched' ? 'confirmed' : row.match_status">{{ matchLabels[row.match_status] }}</span>
                        <small v-if="row.completeness" class="beichen-note">{{ completenessLabels[row.completeness] }}</small>
                        <small v-if="row.upstream.unit" class="beichen-note">{{ sourceLabel(row.upstream) }}</small>
                        <small v-if="row.local.charged_tokens_missing_calls" class="beichen-note">已扣款但 Token 不完整 {{ formatTokens(row.local.charged_tokens_missing_calls) }} 笔</small>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </template>

        <section v-if="!empty" class="beichen-card reconcile-detail">
          <h3>按渠道 Key 对账</h3>
          <p class="beichen-note">此处的 Key 是学校配置的北辰渠道。<template v-if="currencyMismatch">北辰金额显示原单位。</template><template v-else>金额单位为{{ moneyUnit }}。</template></p>
          <div v-if="keys.length" class="reconcile-table-wrap">
            <table class="beichen-table reconcile-table key-details">
              <caption class="sr-only">按渠道 Key 对账，金额单位为{{ moneyUnit }}</caption>
              <thead><tr>
                <th scope="col">Key 名称</th>
                <th scope="col" class="numeric-cell">本地 Token</th>
                <th scope="col" class="numeric-cell">北辰 Token</th>
                <th scope="col" class="numeric-cell">学生已扣</th>
                <th scope="col" class="numeric-cell">北辰实扣</th>
                <th scope="col" class="numeric-cell">学校承担</th>
                <th scope="col" class="numeric-cell">差额</th>
                <th scope="col">Token 回报</th>
              </tr></thead>
              <tbody>
                <tr v-for="row in keys" :key="row.credential_id">
                  <th scope="row">{{ channelLabel(row) }}</th>
                  <td class="numeric-cell" data-label="本地 Token">{{ formatTokens(reportedTokens(row.local)) }}</td>
                  <td class="numeric-cell" data-label="北辰 Token">{{ formatTokens(reportedTokens(row.upstream)) }}</td>
                  <td class="numeric-cell" data-label="学生已扣">{{ yuan(row.student_yuan) }}</td>
                  <td class="numeric-cell" data-label="北辰实扣">{{ actualAmount(row.upstream, row.actual_yuan) }}</td>
                  <td class="numeric-cell" data-label="学校承担">{{ schoolAbsorbed(row.school_absorbed_credits) }}</td>
                  <td class="numeric-cell" :class="marginClass(row)" data-label="差额">{{ margin(row) }}</td>
                  <td data-label="Token 回报">
                    <div class="reconcile-row-status">
                      <span>{{ row.completeness ? completenessLabels[row.completeness] : '—' }}</span>
                      <small v-if="row.upstream.unit" class="beichen-note">{{ sourceLabel(row.upstream) }}</small>
                      <small v-if="row.local.charged_tokens_missing_calls" class="beichen-note">已扣款但 Token 不完整 {{ formatTokens(row.local.charged_tokens_missing_calls) }} 笔</small>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="beichen-empty">当前范围暂无按渠道 Key 汇总的记录。</p>
        </section>
        </template>
      </div>
    </LoadingRegion>
  </section>
</template>

<style scoped>
.reconcile { padding-top: 20px; }
.reconcile-header h2 { margin: 0 0 6px; font-size: 20px; }
.reconcile-kpis { grid-template-columns: repeat(5, minmax(0, 1fr)); }
.reconcile-kpis strong, .reconcile-kpis small { overflow-wrap: anywhere; }
.reconcile .positive { color: #16765e; }
.reconcile .negative { color: #b4233d; }
.reconcile-error { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border-radius: 12px; background: #fff0f1; color: #b4233d; }
.reconcile-error p { margin: 0; }
.coverage-card { display: grid; gap: 10px; font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.coverage-card h3, .coverage-card p, .coverage-days { margin: 0; }
.coverage-days { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.coverage-days div { padding: 10px 12px; border-radius: 10px; background: #f6f8fc; }
.coverage-days dt, .coverage-days small { color: var(--ink-soft); }
.coverage-days dd { margin: 2px 0 0; font-size: 18px; font-weight: 700; font-variant-numeric: tabular-nums; }
.coverage-days small { display: block; font-size: 11px; font-weight: 400; }
.coverage-facts { display: flex; flex-wrap: wrap; gap: 8px 20px; }
.coverage-warning { color: #8a5b10; }
.compare-section { display: grid; gap: 16px; }
.reconcile-donuts > *, .compare-section, .reconcile-table-wrap { min-width: 0; }
.reconcile-donuts :deep(.credit-chart) { grid-template-columns: 140px minmax(0, 1fr); }
.reconcile-donuts :deep(.credit-donut) { width: 140px; min-width: 0; height: 150px; }
.reconcile-donuts :deep(.credit-legend) { min-width: 0; max-height: 260px; overflow-y: auto; padding-inline: 0; }
.reconcile-donuts :deep(.credit-legend-name) { min-width: 0; overflow-wrap: anywhere; }
.reconcile-detail { display: grid; gap: 10px; }
.reconcile-detail h3 { margin: 0; font-size: 15px; }
.reconcile-table-wrap { border: 1px solid var(--line); border-radius: 14px; }
.reconcile-table { table-layout: fixed; }
.reconcile-table th, .reconcile-table td { padding: 12px 8px; overflow-wrap: anywhere; }
.reconcile-table thead th { white-space: normal; }
.reconcile-table .numeric-cell { text-align: right; font-variant-numeric: tabular-nums; white-space: normal; }
.model-details th:first-child { width: 17%; }
.model-details th:last-child { width: 15%; }
.model-channel { display: block; margin-top: 4px; }
.reconcile-row-status { display: grid; gap: 4px; justify-items: start; }
.reconcile-table .status-pill { max-width: 100%; white-space: normal; }
.reconcile-table .unmatched-row, .reconcile-table .unmatched-row:hover { background: #fff8f0; }
/* 隐藏表格按块裁切，避免表格的最小内容宽度撑开窄屏。 */
.reconcile :deep(table.sr-only), .reconcile-table caption.sr-only { display: block; }
.reconcile button:focus-visible { outline: 2px solid var(--primary, #0071e3); outline-offset: 3px; }
@media (min-width: 901px) and (max-width: 1180px) {
  .reconcile-donuts :deep(.credit-chart) { grid-template-columns: 1fr; }
  .reconcile-donuts :deep(.credit-donut) { margin-inline: auto; }
}
@media (max-width: 900px) {
  .reconcile-kpis, .reconcile-donuts { grid-template-columns: 1fr; }
  .reconcile-table thead { display: none; }
  .reconcile-table, .reconcile-table tbody, .reconcile-table tr, .reconcile-table th, .reconcile-table td { display: block; width: 100%; }
  .reconcile-table tr { padding: 12px 0; }
  .reconcile-table th[scope="row"] { width: 100%; padding: 4px 12px 10px; }
  .reconcile-table td { display: flex; align-items: baseline; justify-content: space-between; gap: 16px; padding: 5px 12px; border: 0; text-align: right; }
  .reconcile-table td::before { content: attr(data-label); flex: 0 0 45%; color: var(--ink-soft); text-align: left; }
  .reconcile-row-status { justify-items: end; }
}
@media (max-width: 480px) {
  .reconcile-header > *, .reconcile-header .beichen-toolbar-group { width: 100%; }
  .reconcile-donuts :deep(.credit-chart) { grid-template-columns: 1fr; }
  .reconcile-donuts :deep(.credit-donut) { margin-inline: auto; }
}
</style>
