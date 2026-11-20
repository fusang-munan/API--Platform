<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, useId, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenReconcile as ReconcileData, BeichenReconcileModel, BeichenUsageRange } from '../../api/types'
import CompareBars from '../../components/CompareBars.vue'
import CreditDonut from '../../components/CreditDonut.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { creditsToYuan, formatCredits, formatPercent, formatTokens, formatYuan } from '../../utils/credits'

const props = defineProps<{ providerId: string }>()
const titleId = useId()
const range = ref<BeichenUsageRange>('all')
const ranges: Array<{ value: BeichenUsageRange; label: string }> = [
  { value: 'day', label: '当天' }, { value: 'month', label: '本月' }, { value: 'all', label: '总计' },
]
const metric = ref<'tokens' | 'money'>('tokens')
const data = shallowRef<ReconcileData>()
const loading = ref(true)
const error = ref('')
const models = computed(() => data.value?.models ?? [])
const keys = computed(() => data.value?.keys ?? [])
const missingDays = computed(() => data.value?.coverage.missing_days ?? [])
const empty = computed(() => !models.value.length && !keys.value.length)
const palette = ['#0071e3', '#168473', '#b77916', '#b44d76', '#7657b7', '#427886', '#646f80']
const matchLabels: Record<BeichenReconcileModel['match_status'], string> = {
  matched: '已匹配', local_only: '仅本地有记录', upstream_only: '仅北辰有记录',
}
const period = computed(() => {
  if (!data.value) return ''
  const end = beijingDate(data.value.end)
  return data.value.start ? `${beijingDate(data.value.start)} 起 · 截至 ${end}（北京时间）` : `全部已记录数据 · 截至 ${end}（北京时间）`
})
const localSegments = computed(() => models.value.map((row, index) => ({
  name: row.model, value: row.local.tokens, color: palette[index % palette.length], displayValue: formatPercent(row.local_share),
})))
const upstreamSegments = computed(() => models.value.map((row, index) => ({
  name: row.model, value: row.upstream.tokens, color: palette[index % palette.length], displayValue: formatPercent(row.upstream_share),
})))
const bars = computed(() => ({
  categories: models.value.map(row => row.model),
  left: models.value.map(row => metric.value === 'tokens' ? row.local.tokens : chartNumber(row.student_yuan)),
  right: models.value.map(row => metric.value === 'tokens' ? row.upstream.tokens : chartNumber(row.actual_yuan)),
}))
const yuan = (value: string | number | null | undefined) => formatYuan(value, 6)
let requestVersion = 0

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

function marginClass(value: string | null) {
  const number = chartNumber(value)
  return number != null && number > 0 ? 'positive' : number != null && number < 0 ? 'negative' : ''
}

function margin(value: string | null) {
  return `${(chartNumber(value) ?? 0) > 0 ? '+' : ''}${yuan(value)}`
}

async function load() {
  const current = ++requestVersion
  loading.value = true
  error.value = ''
  data.value = undefined
  try {
    const result = await teacherAPI.beichenReconciliation(props.providerId, range.value)
    // 切换范围或供应商后，过期响应不能覆盖当前对账结果。
    if (current === requestVersion) data.value = result
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
        <p class="beichen-note">核对学校用量与北辰账单。金额按 100 积分 = 1 元展示。</p>
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
      <div v-if="data" class="beichen-stack">
        <p class="beichen-note">{{ period }}</p>
        <div class="beichen-summary reconcile-kpis" aria-label="对账汇总">
          <div class="beichen-stat" data-kpi="local-tokens"><span>本地 Token</span><strong>{{ formatTokens(data.totals.local.tokens) }}</strong><small>学校网关记录</small></div>
          <div class="beichen-stat" data-kpi="upstream-tokens"><span>北辰 Token</span><strong>{{ formatTokens(data.totals.upstream.tokens) }}</strong><small>北辰已同步账单</small></div>
          <div class="beichen-stat" data-kpi="student-yuan">
            <span>学生已扣（元）</span><strong>{{ yuan(data.totals.money.student_yuan) }}</strong>
            <small v-if="data.totals.local.credits != null">{{ formatCredits(data.totals.local.credits, 6) }} 积分 ÷ 100 = {{ creditsToYuan(data.totals.local.credits, 6) }} 元</small>
            <small v-else>已扣积分 ÷ 100</small>
          </div>
          <div class="beichen-stat" data-kpi="actual-yuan"><span>北辰实扣（元）</span><strong>{{ yuan(data.totals.money.actual_yuan) }}</strong><small>北辰实际扣款</small></div>
          <div class="beichen-stat" data-kpi="margin-yuan"><span>差额（元）</span><strong :class="marginClass(data.totals.money.margin_yuan)">{{ margin(data.totals.money.margin_yuan) }}</strong><small>学生已扣折合金额 − 北辰实扣</small></div>
        </div>

        <section class="coverage-strip" aria-label="数据完整性">
          <div class="coverage-facts">
            <strong>账单覆盖：已有 {{ formatTokens(data.coverage.days_present) }} / 应有 {{ formatTokens(data.coverage.days_expected) }} 天</strong>
            <span :class="{ 'coverage-warning': data.coverage.pending_calls > 0 }">待核账 {{ formatTokens(data.coverage.pending_calls) }} 笔</span>
            <span :class="{ 'coverage-warning': data.coverage.unpriced_calls > 0 }">无报价调用 {{ formatTokens(data.coverage.unpriced_calls) }} 笔</span>
          </div>
          <p v-if="missingDays.length" class="coverage-warning">缺失日期：{{ missingDays.slice(0, 8).join('、') }}<template v-if="missingDays.length > 8"> 等 {{ missingDays.length }} 天</template>。数据尚未齐全，差额仅供参考。</p>
          <p v-else-if="data.coverage.days_present < data.coverage.days_expected" class="coverage-warning">仍有 {{ data.coverage.days_expected - data.coverage.days_present }} 天待同步，差额仅供参考。</p>
          <p v-if="data.coverage.multiplier_mixed" class="coverage-warning">当前范围包含不同学校扣费倍率，不能统一按 1.10 倍推算差额。</p>
          <p class="beichen-note">基础成本 {{ yuan(data.totals.money.base_yuan) }} 元；差额率 {{ formatPercent(data.totals.money.margin_rate) }}。差额会受计价、同步进度和未匹配记录影响。</p>
        </section>

        <div v-if="empty" class="beichen-empty" role="status"><strong>当前范围暂无对账记录</strong><span>可切换到“总计”，或待北辰账单同步后刷新。</span></div>
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
                <button type="button" :aria-pressed="metric === 'tokens'" @click="metric = 'tokens'">Token 对比</button>
                <button type="button" :aria-pressed="metric === 'money'" @click="metric = 'money'">金额对比</button>
              </div>
            </div>
            <CompareBars :categories="bars.categories" :left="bars.left" :right="bars.right" :left-label="metric === 'tokens' ? '本地 Token' : '学生已扣'" :right-label="metric === 'tokens' ? '北辰 Token' : '北辰实扣'" :value-formatter="metric === 'tokens' ? formatTokens : yuan" :unit="metric === 'tokens' ? 'Token' : '元'" />
          </section>

          <section class="beichen-stack">
            <h3 class="table-title">模型对账明细</h3>
            <div class="reconcile-table-wrap">
              <table class="beichen-table reconcile-table model-details">
                <caption class="sr-only">模型对账明细，金额单位为元</caption>
                <thead><tr><th scope="col">模型</th><th scope="col" class="numeric-cell">本地 Token</th><th scope="col" class="numeric-cell">北辰 Token</th><th scope="col" class="numeric-cell">本地占比</th><th scope="col" class="numeric-cell">北辰占比</th><th scope="col" class="numeric-cell">学生已扣（元）</th><th scope="col" class="numeric-cell">北辰实扣（元）</th><th scope="col" class="numeric-cell">差额（元）</th><th scope="col">匹配状态</th></tr></thead>
                <tbody>
                  <tr v-for="row in models" :key="row.model" :class="{ 'unmatched-row': row.match_status !== 'matched' }">
                    <th scope="row">{{ row.model }}</th>
                    <td class="numeric-cell" data-label="本地 Token">{{ formatTokens(row.local.tokens) }}</td>
                    <td class="numeric-cell" data-label="北辰 Token">{{ formatTokens(row.upstream.tokens) }}</td>
                    <td class="numeric-cell" data-label="本地占比">{{ formatPercent(row.local_share) }}</td>
                    <td class="numeric-cell" data-label="北辰占比">{{ formatPercent(row.upstream_share) }}</td>
                    <td class="numeric-cell" data-label="学生已扣（元）">{{ yuan(row.student_yuan) }}</td>
                    <td class="numeric-cell" data-label="北辰实扣（元）">{{ yuan(row.actual_yuan) }}</td>
                    <td class="numeric-cell" :class="marginClass(row.margin_yuan)" data-label="差额（元）">{{ margin(row.margin_yuan) }}</td>
                    <td data-label="匹配状态"><span class="status-pill" :class="row.match_status === 'matched' ? 'confirmed' : row.match_status">{{ matchLabels[row.match_status] }}</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </template>

        <section v-if="!empty" class="beichen-stack">
          <h3 class="table-title">按渠道 Key 对账</h3>
          <p class="beichen-note">此处的 Key 是学校配置的北辰渠道。</p>
          <div v-if="keys.length" class="reconcile-table-wrap">
            <table class="beichen-table reconcile-table key-details">
              <caption class="sr-only">按渠道 Key 对账，金额单位为元</caption>
              <thead><tr><th scope="col">Key 名称</th><th scope="col" class="numeric-cell">本地 Token</th><th scope="col" class="numeric-cell">北辰 Token</th><th scope="col" class="numeric-cell">学生已扣（元）</th><th scope="col" class="numeric-cell">北辰实扣（元）</th><th scope="col" class="numeric-cell">差额（元）</th></tr></thead>
              <tbody>
                <tr v-for="row in keys" :key="row.credential_id">
                  <th scope="row">{{ row.label || '未命名 Key' }}</th>
                  <td class="numeric-cell" data-label="本地 Token">{{ formatTokens(row.local.tokens) }}</td>
                  <td class="numeric-cell" data-label="北辰 Token">{{ formatTokens(row.upstream.tokens) }}</td>
                  <td class="numeric-cell" data-label="学生已扣（元）">{{ yuan(row.student_yuan) }}</td>
                  <td class="numeric-cell" data-label="北辰实扣（元）">{{ yuan(row.actual_yuan) }}</td>
                  <td class="numeric-cell" :class="marginClass(row.margin_yuan)" data-label="差额（元）">{{ margin(row.margin_yuan) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="beichen-empty">当前范围暂无按渠道 Key 汇总的记录。</p>
        </section>
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
.coverage-strip { display: grid; gap: 8px; padding: 14px 0; border-block: 1px solid var(--line); font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.coverage-strip p { margin: 0; }
.coverage-facts { display: flex; flex-wrap: wrap; gap: 8px 20px; }
.coverage-warning { color: #8a5b10; }
.compare-section { display: grid; gap: 16px; }
.reconcile-donuts > *, .compare-section, .reconcile-table-wrap { min-width: 0; }
.reconcile-donuts :deep(.credit-chart) { grid-template-columns: 140px minmax(0, 1fr); }
.reconcile-donuts :deep(.credit-donut) { width: 140px; min-width: 0; height: 150px; }
.reconcile-donuts :deep(.credit-legend) { min-width: 0; max-height: 260px; overflow-y: auto; padding-inline: 0; }
.reconcile-donuts :deep(.credit-legend-name) { min-width: 0; overflow-wrap: anywhere; }
.table-title { margin: 4px 0 0; font-size: 15px; }
.reconcile-table-wrap { border: 1px solid var(--line); border-radius: 14px; }
.reconcile-table { table-layout: fixed; }
.reconcile-table th, .reconcile-table td { padding: 12px 8px; overflow-wrap: anywhere; }
.reconcile-table thead th { white-space: normal; }
.reconcile-table .numeric-cell { white-space: normal; }
.model-details th:first-child { width: 17%; }
.model-details th:last-child { width: 12%; }
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
}
@media (max-width: 480px) {
  .reconcile-header > *, .reconcile-header .beichen-toolbar-group { width: 100%; }
  .reconcile-donuts :deep(.credit-chart) { grid-template-columns: 1fr; }
  .reconcile-donuts :deep(.credit-donut) { margin-inline: auto; }
}
</style>
