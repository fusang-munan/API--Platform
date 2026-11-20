<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenModelUsage, BeichenOverview, BeichenUsageRange } from '../../api/types'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { useToast } from '../../composables/useToast'
import { priceFields, schoolCatalog, type SchoolCatalogRow, type SchoolQuote } from '../../utils/beichenModels'
import { formatCredits, formatTokens, multiplyDecimal, shiftDecimal } from '../../utils/credits'
import { schoolDay, statusLabel } from '../../utils/format'
import AppDialog from '../../components/AppDialog.vue'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenUsageDrawer from '../../components/BeichenUsageDrawer.vue'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string>; providerMaintenance?: boolean }>()
const overview = ref<BeichenOverview>()
const usage = ref<BeichenModelUsage>()
const range = ref<BeichenUsageRange>('all')
const loading = ref(false)
const usageLoading = ref(false)
const usageError = ref('')
const updating = ref('')
const syncing = ref(false)
const error = ref('')
const toast = useToast()
const ranges: Array<{ id: BeichenUsageRange; label: string }> = [{ id: 'day', label: '当天' }, { id: 'month', label: '本月' }, { id: 'all', label: '总计' }]
const filters = [{ id: 'all', label: '全部' }, { id: 'active', label: '已启用' }, { id: 'pending', label: '待配置' }, { id: 'unpriced', label: '无报价' }, { id: 'manual', label: '手动价' }] as const
const filter = ref<typeof filters[number]['id']>('all')
const priceRow = ref<SchoolCatalogRow>()
const conditionsRow = ref<SchoolCatalogRow>()
const usageOpen = ref(false)
const initialModel = ref('')
const catalog = computed(() => overview.value ? schoolCatalog(overview.value, props.providerId) : [])
const searchedRows = computed(() => catalog.value.filter(row =>
  row.model.toLowerCase().includes((props.query ?? '').trim().toLowerCase()) &&
  (!props.commonIds || props.commonIds.has(row.model.toLowerCase()))))
const rows = computed(() => searchedRows.value.filter(row => filter.value === 'all' ||
  filter.value === 'active' && row.status === 'active' ||
  filter.value === 'pending' && row.status !== 'active' ||
  filter.value === 'unpriced' && !row.priceReady ||
  filter.value === 'manual' && row.mode !== 'follow'))
const usageByModel = computed(() => new Map((usage.value?.models ?? []).map(row => [row.model.toLowerCase(), row])))
const batchRows = ref<SchoolCatalogRow[]>()
const batchBusy = ref(false)
const batchDone = ref(false)
type BatchOutcome = { model: string; status: 'enabled' | 'skipped' | 'failed'; reason: string }
const batchResults = ref<BatchOutcome[]>([])
const busy = computed(() => loading.value || syncing.value || Boolean(updating.value) || batchBusy.value)
function skipReason(row: SchoolCatalogRow) {
  if (row.status === 'active') return '已启用'
  if (!row.priceReady) return '官网未报价，请先手动定价'
  if (!row.canEnable || !row.routeCount) return '线路未就绪'
  return ''
}
const batchPlan = computed(() => (batchRows.value ?? []).map(row => ({ row, reason: skipReason(row) })))
const batchEligible = computed(() => batchPlan.value.filter(item => !item.reason))
const batchSkipped = computed(() => batchPlan.value.filter(item => item.reason))
const batchCounts = computed(() => ({
  enabled: batchResults.value.filter(row => row.status === 'enabled').length,
  skipped: batchResults.value.filter(row => row.status === 'skipped').length,
  failed: batchResults.value.filter(row => row.status === 'failed').length,
}))
const keyHealth = computed(() => {
  const keys = new Map(catalog.value.flatMap(row => row.credentials.map(key => [key.credential_id, key.label] as const)))
  for (const dataset of overview.value?.datasets ?? []) if (!keys.has(dataset.credential_id)) keys.set(dataset.credential_id, '未命名 Key')
  return Array.from(keys, ([id, label]) => {
    const datasets = (overview.value?.datasets ?? []).filter(row => row.credential_id === id)
    const errors = [...new Set(datasets.flatMap(row => row.last_error ? [row.last_error] : []))]
    const healthy = !errors.length && datasets.some(row => row.last_success)
    return { id, label, healthy, state: errors.length ? '同步异常' : healthy ? '已同步' : '待同步', errors }
  })
})
const keyCount = computed(() => overview.value?.website_sync?.channels || keyHealth.value.length)
const lastSync = computed(() => overview.value?.website_sync?.price_last_seen ??
  (overview.value?.datasets ?? []).filter(row => row.kind === 'prices' && row.last_success).map(row => row.last_success!).sort().at(-1))
const loginNeeded = computed(() => overview.value?.web_login_required ?? overview.value?.datasets?.some(row =>
  /重新授权|重新登录|完成登录验证|(?:官网|网站).*(?:授权|会话)(?:已)?(?:失效|过期)/u.test(row.last_error ?? '')))
const priceErrors = computed(() => [...new Set((overview.value?.datasets ?? [])
  .filter(row => row.kind === 'prices' && row.last_error)
  .map(row => row.last_error as string))])
const workspaceUrl = computed(() => {
  const raw = overview.value?.maintenance_workspace?.launch_url
  if (typeof raw !== 'string' || !/^https:\/\/[^/@]+(?:\/|$)/i.test(raw)) return null
  // 只接受原始的主机和路径，拒绝参数、片段及浏览器会自动修正的分隔符。
  if (/[?#\\\s\u0000-\u001f\u007f]/u.test(raw)) return null
  try {
    const target = new URL(raw)
    // 同一主机即使端口不同，也可能收到学校的会话 Cookie。
    return target.hostname === window.location.hostname ? null : target.href
  } catch {
    return null
  }
})

let overviewRequest = 0
let usageRequest = 0
let initialized = false
let disposed = false
async function load() {
  const request = ++overviewRequest
  const providerId = props.providerId
  loading.value = true
  error.value = ''
  try {
    const result = await teacherAPI.beichenOverview(providerId, schoolDay())
    if (!disposed && request === overviewRequest) overview.value = result
  } catch (e) {
    if (!disposed && request === overviewRequest) {
      overview.value = undefined
      error.value = apiErrorMessage(e, '读取北辰模型失败，请点击刷新模型重试')
    }
  } finally {
    if (!disposed && request === overviewRequest) loading.value = false
  }
}

async function loadUsage() {
  const request = ++usageRequest
  usageLoading.value = true
  usageError.value = ''
  usage.value = undefined
  try {
    const result = await teacherAPI.modelUsage(range.value, props.providerId)
    if (!disposed && request === usageRequest) usage.value = result
  } catch (e) {
    if (!disposed && request === usageRequest) usageError.value = apiErrorMessage(e, '读取用量失败，请重试')
  } finally {
    if (!disposed && request === usageRequest) usageLoading.value = false
  }
}

async function refreshOverview() {
  if (!busy.value) await load()
}

async function syncPrices() {
  if (busy.value) return
  syncing.value = true
  error.value = ''
  try {
    await teacherAPI.syncBeichen(props.providerId)
    await load()
  } catch (e) {
    error.value = apiErrorMessage(e, '立即同步失败，请稍后重试')
  } finally {
    syncing.value = false
  }
}

async function changeStatus(row: SchoolCatalogRow, enable: boolean) {
  if (busy.value || enable && skipReason(row)) return
  if (!enable && !window.confirm(`禁用 ${row.model} 后，学生将无法发起新的调用，确定继续？`)) return
  updating.value = row.model
  error.value = ''
  try {
    if (enable) {
      await teacherAPI.enableBeichenModel(props.providerId, row.model, row.school)
    } else {
      await teacherAPI.updateModel({ model_id: row.school, status: 'inactive' })
    }
    await load()
    toast.success(enable ? '模型已启用' : '模型已禁用')
  } catch (e) {
    error.value = apiErrorMessage(e, '模型状态更新失败')
  } finally {
    updating.value = ''
  }
}

function previewBatch() {
  batchRows.value = [...rows.value]
  batchDone.value = false
  batchResults.value = []
}
async function enableBatch() {
  if (busy.value || batchDone.value || !batchEligible.value.length) return
  const providerId = props.providerId
  const models = batchEligible.value.map(item => item.row.model)
  batchResults.value = batchSkipped.value.map(item => ({ model: item.row.model, status: 'skipped', reason: item.reason }))
  batchBusy.value = true
  try {
    for (let offset = 0; offset < models.length; offset += 200) {
      if (disposed || props.providerId !== providerId) return
      const chunk = models.slice(offset, offset + 200)
      try {
        const result = await teacherAPI.enableBeichenModelsBatch(providerId, chunk)
        const returned = new Set([...result.enabled, ...result.skipped].map(row => row.model.toLowerCase()))
        batchResults.value.push(...result.enabled.map(row => ({ model: row.model, status: 'enabled' as const, reason: '已启用' })),
          ...result.skipped.map(row => ({ model: row.model, status: 'skipped' as const, reason: row.message || '线路或价格未就绪' })),
          ...chunk.filter(model => !returned.has(model.toLowerCase())).map(model => ({ model, status: 'failed' as const, reason: '服务器未返回结果，请刷新后重试' })))
      } catch (cause) {
        const reason = apiErrorMessage(cause, '启用失败，请稍后重试')
        batchResults.value.push(...chunk.map(model => ({ model, status: 'failed' as const, reason })))
      }
    }
    batchDone.value = true
    const counts = batchCounts.value
    const message = `已启用 ${counts.enabled} 个，跳过 ${counts.skipped} 个，失败 ${counts.failed} 个`
    if (counts.failed) toast.error(message)
    else toast.success(message)
    await load()
  } finally {
    batchBusy.value = false
  }
}
function openUsage(model = '') {
  initialModel.value = model
  usageOpen.value = true
}
async function priceSaved() {
  toast.success('价格已保存，后续调用采用新计价方式')
  await load()
}
function syncTime(value?: string | null) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return '尚未同步'
  return new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}
function multiplierLabel(row: SchoolCatalogRow) {
  if (row.mode === 'manual') return '手动定价'
  if (row.mode === 'mixed' || new Set(row.quotes.map(quote => quote.multiplier)).size > 1) return '多种'
  return row.quotes[0]?.multiplier ?? '待同步'
}
function conditional(quote: SchoolQuote) {
  return quote.status === 'conditional' || /上下文分档|推理强度/u.test((quote.standard ?? '') + (quote.actual ?? ''))
}
function tokenPrice(text: string | null) { return Boolean(text?.startsWith('每百万 Token 的积分')) }
type RateKey = typeof priceFields[number]['key']
function price(quote: SchoolQuote, key: RateKey, actual: boolean) {
  if (actual && quote.actualRates?.[key] != null) return quote.actualRates[key]
  if (actual && quote.mode === 'manual') return null
  const text = actual ? quote.actual : quote.standard
  const label = priceFields.find(field => field.key === key)!.label
  return text?.split('\n')[0].split(' · ').find(part => part.startsWith(label + ' '))?.slice(label.length + 1) ?? null
}
function studentPrice(value: string | null) {
  return value && /^\d+(?:\.\d+)?$/.test(value) ? multiplyDecimal(value, '1.10') : value
}
function originalPrice(value: string | null) {
  return value && /^\d+(?:\.\d+)?$/.test(value) ? shiftDecimal(value, -2) : '—'
}
function modelUsage(model: string) { return usageByModel.value.get(model.toLowerCase()) }

watch(range, loadUsage)
watch(() => props.providerId, () => {
  overview.value = undefined
  priceRow.value = undefined
  conditionsRow.value = undefined
  usageOpen.value = false
  batchRows.value = undefined
  initialized = true
  void Promise.all([load(), loadUsage()])
})
useVisibilityPolling(async () => {
  if (busy.value) return
  if (!initialized) {
    initialized = true
    await Promise.all([load(), loadUsage()])
  } else await load()
}, 60_000)
onBeforeUnmount(() => { disposed = true; overviewRequest++; usageRequest++ })
</script>

<template>
  <section v-if="providerMaintenance && overview?.maintenance_workspace" aria-label="校方北辰工作区">
    <a v-if="workspaceUrl" class="button secondary" :href="workspaceUrl" target="_blank"
      rel="noopener noreferrer" referrerpolicy="no-referrer">打开校方北辰工作区</a>
    <p v-else class="muted">校方尚未配置远程应用地址</p>
  </section>
  <BeichenRecentReceipts v-if="providerMaintenance" :provider-id="providerId" :website-sync="overview?.website_sync"
    @refresh-overview="refreshOverview" />
  <section class="beichen-stack beichen-models" aria-label="北辰模型目录" :aria-busy="loading">
    <div class="beichen-summary" aria-label="模型与用量概况">
      <div class="beichen-stat"><span>可用模型</span><strong>{{ overview ? formatTokens(catalog.length) : '—' }}</strong><small>已从上游发现</small></div>
      <div class="beichen-stat"><span>已启用</span><strong>{{ overview ? formatTokens(catalog.filter(row => row.status === 'active').length) : '—' }}</strong><small>学校模型</small></div>
      <div class="beichen-stat"><span>报价覆盖</span><strong>{{ overview ? catalog.filter(row => row.priceReady).length + ' / ' + catalog.length : '—' }}</strong><small>含教师手动价</small></div>
      <div class="beichen-stat"><span>{{ ranges.find(item => item.id === range)?.label }}已扣积分</span><strong>{{ usageLoading ? '…' : usage ? formatCredits(usage.totals.credits, 6) : '—' }}</strong><small>积分 · 学生实际消耗</small></div>
      <div class="beichen-stat sync-stat"><span>价格最后同步</span><strong>{{ syncTime(lastSync) }}</strong><small>北京时间</small></div>
      <div class="beichen-stat"><span>Key 健康</span><strong>{{ keyHealth.filter(key => key.healthy).length }} / {{ keyCount }}</strong><small>无同步错误且已成功读取</small></div>
    </div>
    <details v-if="keyHealth.length" class="key-health"><summary>查看 Key 同步状态</summary><ul><li v-for="key in keyHealth" :key="key.id"><strong>{{ key.label }}</strong><span class="status-pill" :class="key.healthy ? 'active' : key.errors.length ? 'danger' : 'pending'">{{ key.state }}</span><small v-for="message in key.errors" :key="message" class="error">{{ message }}</small></li></ul></details>
    <div class="beichen-toolbar">
      <div class="beichen-toolbar-group">
        <div class="range-switch" role="group" aria-label="模型用量时间范围"><button v-for="item in ranges" :key="item.id" type="button" :aria-pressed="range === item.id" @click="range = item.id">{{ item.label }}</button></div>
        <button type="button" class="button ghost" @click="openUsage()">用量总览</button>
      </div>
      <div class="beichen-toolbar-group">
        <button type="button" class="button ghost" :disabled="busy" @click="refreshOverview">刷新模型</button>
        <button type="button" class="button secondary" :disabled="busy" @click="syncPrices">{{ syncing ? '同步中…' : '立即同步价格' }}</button>
        <button type="button" class="button primary" :disabled="busy || !rows.length" @click="previewBatch">一键启用</button>
      </div>
    </div>
    <div class="model-filters" role="group" aria-label="模型状态筛选"><button v-for="item in filters" :key="item.id" type="button" :aria-pressed="filter === item.id" @click="filter = item.id">{{ item.label }}</button><small>当前筛选 {{ rows.length }} 个模型</small></div>
    <p class="beichen-note">价格以积分展示，1 元 = 100 积分；学生实际价包含学校 1.10 倍扣分。报价和倍率持续自动同步，手动价持续覆盖官网。</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="usageError" class="error usage-feedback" role="alert">{{ usageError }}<button type="button" class="button ghost" :disabled="usageLoading" @click="loadUsage">重试用量</button></p>
    <p v-if="providerMaintenance && loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="priceErrors.length" class="error" role="alert">价格同步失败：{{ priceErrors.join('；') }}</p>
    <div v-if="loading && !overview" class="model-skeleton" role="status"><span class="sr-only">正在读取模型…</span><i v-for="index in 3" :key="index" aria-hidden="true" /></div>
    <div v-if="rows.length" class="model-list" data-layout="responsive-cards">
      <table class="beichen-table model-table">
        <caption class="sr-only">北辰模型状态、积分价格与区间用量</caption>
        <colgroup><col class="model-col"><col class="state-col"><col class="multiplier-col"><col class="price-col"><col class="price-col"><col class="usage-col"><col class="action-col"></colgroup>
        <thead><tr><th scope="col">模型</th><th scope="col">状态与线路</th><th scope="col">学校倍率</th><th scope="col">官网标准价（积分）</th><th scope="col">学生实际价（积分）</th><th scope="col">区间用量</th><th scope="col">操作</th></tr></thead>
        <tbody>
          <tr v-for="row in rows" :key="row.model" :data-model="row.model">
            <th scope="row" class="model-name">{{ row.model }}<span v-if="row.mode !== 'follow'" class="status-pill manual">{{ row.mode === 'manual' ? '手动价' : '混合定价' }}</span></th>
            <td data-label="状态与线路">
              <span class="status-pill" :class="row.status === 'active' ? 'active' : 'pending'">{{ row.status === 'not_configured' ? '未配置' : statusLabel(row.status ?? undefined) || '待同步' }}</span>
              <small class="row-note">{{ row.readyCount }} / {{ row.routeCount }} 条线路就绪</small>
              <small v-if="row.status !== 'active' && !row.canEnable" class="row-note">{{ row.canEnable === false ? '暂不可启用' : '启用条件待同步' }}</small>
            </td>
            <td data-label="学校倍率" class="model-multiplier"><strong>{{ multiplierLabel(row) }}</strong></td>
            <td colspan="2" class="pricing-cell" data-label="积分价格">
              <div v-for="quote in row.quotes" :key="quote.key" class="quote-row" :data-price-status="quote.status">
                <div class="quote-caption"><span v-if="row.quotes.length > 1">{{ quote.multiplierLabel }}</span><span class="status-pill" :class="quote.mode === 'manual' ? 'manual' : quote.status">{{ quote.mode === 'manual' ? '手动价' : quote.statusLabel }}</span></div>
                <dl class="quote-prices">
                  <div class="standard-price"><dt>官网标准价</dt><dd>
                    <template v-if="conditional(quote)"><span class="status-pill conditional">按条件计价</span><button type="button" class="price-link" @click="conditionsRow = row">查看档位</button></template>
                    <dl v-else-if="tokenPrice(quote.standard)" class="price-grid"><div v-for="field in priceFields" :key="field.key" class="price-cell"><dt class="price-label">{{ field.label }}</dt><dd class="price-value">{{ price(quote, field.key, false) ?? '—' }}<small v-if="price(quote, field.key, false) != null"> 积分</small></dd><dd v-if="price(quote, field.key, false) != null" class="price-sub">北辰 {{ originalPrice(price(quote, field.key, false)) }} 元</dd></div></dl>
                    <p v-else-if="quote.standard" class="price-line">{{ quote.standard }}</p>
                    <span v-else class="price-empty">{{ quote.status === 'not_listed' ? '官网未报价' : '标准价待同步' }}</span>
                  </dd></div>
                  <div class="actual-price"><dt>{{ quote.actual && !tokenPrice(quote.actual) && !conditional(quote) && quote.mode === 'follow' ? '实际基础价（按官网单位）' : '学生实际价（含 1.10）' }}</dt><dd>
                    <span v-if="conditional(quote) && quote.mode !== 'manual'" class="price-empty">随条件计价，查看档位</span>
                    <dl v-else-if="quote.mode === 'manual' && quote.actualRates || tokenPrice(quote.actual) || !quote.actual && quote.actualRates" class="price-grid"><div v-for="field in priceFields" :key="field.key" class="price-cell"><dt class="price-label">{{ field.label }}</dt><dd class="price-value">{{ studentPrice(price(quote, field.key, true)) ?? '—' }}<small v-if="price(quote, field.key, true) != null"> 积分</small></dd><dd v-if="price(quote, field.key, true) != null" class="price-sub">基础 {{ price(quote, field.key, true) }} 积分<span v-if="quote.mode === 'follow'"> · 北辰 {{ originalPrice(price(quote, field.key, true)) }} 元</span></dd></div></dl>
                    <template v-else-if="quote.actual"><p class="price-line">{{ quote.actual }}</p><small class="price-sub">学生实扣 = 基础 × 1.10</small></template>
                    <span v-else class="price-empty">尚无可用单价</span>
                  </dd></div>
                </dl>
                <small v-for="note in quote.notes" :key="note" class="row-note">{{ note }}</small>
              </div>
              <span v-if="!row.quotes.length" class="price-empty">暂无可显示报价的北辰首选线路</span>
              <small v-if="row.quotes.some(quote => tokenPrice(quote.standard) || tokenPrice(quote.actual) || quote.mode === 'manual')" class="price-unit">普通价格单位：积分 / 百万 Token</small>
            </td>
            <td data-label="区间用量" class="model-usage"><button type="button" class="usage-link" :aria-label="'查看 ' + row.model + ' 用量'" @click="openUsage(row.model)">{{ usageLoading ? '…' : usage ? formatCredits(modelUsage(row.model)?.credits ?? '0', 6) + ' 积分' : '—' }}</button><small class="row-note">{{ usage ? formatTokens(modelUsage(row.model)?.tokens ?? 0) + ' Token' : usageError ? '用量读取失败' : '正在读取用量' }}</small><small v-if="modelUsage(row.model)?.pending_calls" class="row-note">待核账 {{ modelUsage(row.model)?.pending_calls }} 笔</small></td>
            <td data-label="操作"><div class="model-actions">
              <button type="button" class="button ghost" :disabled="busy" :aria-label="'定价 ' + row.model" @click="priceRow = row">{{ row.mode !== 'follow' ? '调整价格' : '手动定价' }}</button>
              <button v-if="row.status !== 'active' && row.canEnable" type="button" class="button secondary" :disabled="busy || !row.priceReady" :title="skipReason(row) || '启用此模型'" :aria-label="'启用 ' + row.model" @click="changeStatus(row, true)">{{ updating === row.model ? '启用中…' : '启用' }}</button>
              <button v-if="row.canDisable" type="button" class="button ghost" :disabled="busy" :aria-label="'禁用 ' + row.model" @click="changeStatus(row, false)">禁用</button>
              <small v-if="!row.priceReady" class="row-note">{{ row.quotes.some(quote => quote.status === 'conditional') ? '条件价待核实，可先手动定价' : '官网未报价，请先手动定价' }}</small>
            </div></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else-if="!loading && !error" class="beichen-empty"><strong>{{ catalog.length ? '当前筛选没有匹配模型' : '暂无模型记录' }}</strong><span>{{ catalog.length ? '调整搜索、常用模型或状态筛选后再试。' : '后台会自动发现上游模型，也可以立即刷新目录。' }}</span><button type="button" class="button ghost" @click="catalog.length ? filter = 'all' : refreshOverview()">{{ catalog.length ? '查看全部状态' : '刷新模型' }}</button></div>
  </section>

  <BeichenPriceDrawer :open="Boolean(priceRow)" :provider-id="providerId" :row="priceRow" @update:open="open => { if (!open) priceRow = undefined }" @saved="priceSaved" />
  <BeichenUsageDrawer v-model:open="usageOpen" v-model:range="range" :usage="usage" :loading="usageLoading" :error="usageError" :initial-model="initialModel" @retry="loadUsage" />
  <AppDialog :open="Boolean(batchRows)" title="一键启用当前筛选模型" description="确认当前筛选结果中的可启用模型，其他模型将跳过。" @update:open="open => { if (!open && !batchBusy) batchRows = undefined }">
    <section class="batch-preview beichen-stack" :aria-busy="batchBusy">
      <template v-if="!batchDone">
        <p class="batch-count" role="status">将启用 {{ batchEligible.length }} 个 · 跳过 {{ batchSkipped.length }} 个</p>
        <p class="beichen-note">仅处理打开预览时的搜索、常用模型和状态筛选结果。后台仍会逐项核实可用条件。</p>
        <div class="batch-list"><h3>将启用</h3><ul v-if="batchEligible.length"><li v-for="item in batchEligible" :key="item.row.model">{{ item.row.model }}</li></ul><p v-else class="beichen-note">当前没有可启用的模型。</p><h3 v-if="batchSkipped.length">跳过原因</h3><ul><li v-for="item in batchSkipped" :key="item.row.model"><strong>{{ item.row.model }}</strong><span>{{ item.reason }}</span></li></ul></div>
        <p v-if="batchBusy" role="status">正在逐批启用，已返回 {{ batchResults.filter(row => row.status !== 'skipped').length }} 个结果…</p>
      </template>
      <template v-else>
        <p class="batch-count" role="status">已启用 {{ batchCounts.enabled }} 个 · 跳过 {{ batchCounts.skipped }} 个 · 失败 {{ batchCounts.failed }} 个</p>
        <ul class="batch-list"><li v-for="item in batchResults" :key="item.model"><strong>{{ item.model }}</strong><span :class="item.status === 'failed' ? 'error' : 'beichen-note'">{{ item.reason }}</span></li></ul>
      </template>
      <div class="actions"><button type="button" class="button ghost" :disabled="batchBusy" @click="batchRows = undefined">{{ batchDone ? '完成' : '取消' }}</button><button v-if="!batchDone" type="button" class="button primary" :disabled="busy || !batchEligible.length" @click="enableBatch">{{ batchBusy ? '启用中…' : '确认启用 ' + batchEligible.length + ' 个' }}</button></div>
    </section>
  </AppDialog>
  <AppDialog :open="Boolean(conditionsRow)" :title="(conditionsRow?.model ?? '模型') + ' · 计价档位'" description="保留官网计价条件及原始精度，实际基础价已包含渠道倍率。" wide @update:open="open => { if (!open) conditionsRow = undefined }">
    <div class="conditions-panel beichen-stack"><p class="beichen-note">以下价格均为积分；“实际基础价”已乘渠道倍率，学生实扣再乘 1.10。单位未知的原值保留官网原样，不用于计算。</p><section v-for="quote in conditionsRow?.quotes ?? []" :key="quote.key" class="condition-quote"><h3>{{ quote.multiplierLabel }}</h3><dl class="condition-prices"><div><dt>官网标准价</dt><dd class="price-detail">{{ quote.standard ?? '尚未取得官网明细' }}</dd></div><div><dt>实际基础价（已乘渠道倍率）</dt><dd class="price-detail">{{ quote.actual ?? '尚无可用单价' }}</dd></div></dl><p v-for="note in quote.notes" :key="note" class="beichen-note">{{ note }}</p></section></div>
  </AppDialog>
</template>

<style scoped>
.beichen-models, .model-list { min-width: 0; max-width: 100%; }
.model-table { table-layout: fixed; }
.model-col { width: 17%; } .state-col { width: 12%; } .multiplier-col { width: 10%; } .price-col { width: 19%; } .usage-col { width: 12%; } .action-col { width: 11%; }
.model-table thead th { white-space: normal; }
.model-table th, .model-table td { min-width: 0; overflow-wrap: anywhere; }
.model-name { font-size: 13px; }
.model-name .status-pill { display: table; margin-top: 8px; }
.model-multiplier { font-size: 12px; font-variant-numeric: tabular-nums; }
.model-actions { display: flex; gap: 6px; flex-wrap: wrap; }
.model-actions .button { padding: 7px 10px; }
.model-actions .row-note { flex-basis: 100%; }
.row-note, .price-unit { display: block; color: var(--ink-soft); font-size: 11px; line-height: 1.5; margin-top: 6px; overflow-wrap: anywhere; }
.price-unit { margin-top: 12px; }
.price-line { white-space: pre-line; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; font-size: 12px; line-height: 1.6; margin: 0; }
.quote-row { display: grid; gap: 8px; }
.quote-row + .quote-row { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); }
.quote-caption { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 11px; font-variant-numeric: tabular-nums; }
.quote-prices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin: 0; }
.quote-prices > div { min-width: 0; }
.quote-prices > div > dt { font-size: 11px; font-weight: 700; color: var(--ink-soft); margin-bottom: 8px; }
.quote-prices dd { margin: 0; }
.price-grid { grid-template-columns: minmax(0, 1fr); gap: 8px; margin: 0; }
.price-cell { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr); align-items: baseline; gap: 2px 8px; }
.price-value { font-size: 13px; }
.price-cell > .price-value { text-align: right; }
.price-cell > .price-sub { grid-column: 1 / -1; text-align: right; }
.price-value small { font-size: 10px; font-weight: 500; }
.price-sub { font-size: 10px; overflow-wrap: anywhere; }
.price-link, .usage-link { padding: 0; border: 0; background: transparent; text-align: left; color: var(--primary-strong); font-weight: 700; font-size: 12px; overflow-wrap: anywhere; }
.price-link { display: block; margin-top: 8px; }
.price-link:hover, .usage-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.model-filters { display: flex; gap: 7px; align-items: center; flex-wrap: wrap; }
.model-filters button { border: 1px solid var(--line); background: #fff; color: var(--ink-soft); border-radius: 999px; font-size: 12px; padding: 6px 12px; }
.model-filters button[aria-pressed="true"] { background: var(--primary-soft); border-color: var(--primary); color: var(--primary-strong); }
.model-filters small { margin-left: auto; color: var(--ink-soft); font-size: 11px; }
.sync-stat strong { font-size: 16px; line-height: 1.5; }
.key-health { color: var(--ink-soft); font-size: 12px; }
.key-health summary { width: fit-content; cursor: pointer; }
.key-health ul { display: grid; gap: 10px; list-style: none; padding: 10px 0 0; margin: 0; }
.key-health li { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.key-health small { flex-basis: 100%; }
.usage-feedback { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.model-skeleton { display: grid; gap: 12px; }
.model-skeleton i { height: 105px; background: #edf0f7; border-radius: 12px; }
.batch-preview { min-width: 0; font-size: 13px; }
.batch-count { margin: 0; font-size: 17px; font-weight: 700; font-variant-numeric: tabular-nums; }
.batch-list { max-height: 40vh; overflow-y: auto; padding: 0; margin: 0; overflow-wrap: anywhere; }
.batch-list h3 { margin: 16px 0 8px; font-size: 13px; }
.batch-list h3:first-child { margin-top: 0; }
.batch-list ul { list-style: none; padding: 0; margin: 0; }
.batch-list li { display: grid; gap: 4px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.batch-list li span { font-size: 12px; color: var(--ink-soft); }
.condition-quote h3 { font-size: 14px; margin-top: 0; }
.condition-prices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; margin: 0; }
.condition-prices dt { font-weight: 700; font-size: 13px; margin-bottom: 8px; }
.price-detail { white-space: pre-line; overflow-wrap: anywhere; color: var(--ink-soft); font-variant-numeric: tabular-nums; font-size: 13px; margin: 0; line-height: 1.7; }
button:focus-visible, summary:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@media (max-width: 900px) {
  .model-table colgroup { display: none; }
  .model-table tbody { display: grid; gap: 16px; }
  .model-table tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 10px; padding: 16px 0; border: 1px solid var(--line); border-radius: 14px; }
  .model-table th, .model-table td { width: auto; padding: 0 14px; }
  .model-name, .pricing-cell, .model-table td:last-child { grid-column: 1 / -1; }
  .model-table td[data-label]::before { content: attr(data-label); display: block; margin-bottom: 6px; font-size: 11px; color: var(--ink-soft); }
  .price-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .price-cell { grid-template-columns: minmax(0, 1fr); }
  .price-cell > .price-value, .price-cell > .price-sub { text-align: left; }
  .model-actions .row-note { margin-top: 0; }
  .condition-prices { grid-template-columns: minmax(0, 1fr); gap: 16px; }
}
@media (max-width: 600px) {
  .quote-prices { grid-template-columns: minmax(0, 1fr); gap: 16px; }
  .model-filters small { margin-left: 0; flex-basis: 100%; }
  .beichen-toolbar-group { width: 100%; }
  .beichen-toolbar-group > .button { flex: 1; }
}
</style>
