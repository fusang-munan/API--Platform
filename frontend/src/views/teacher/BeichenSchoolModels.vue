<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenCatalogModel, BeichenModelUsage, BeichenOverview, BeichenUsageRange } from '../../api/types'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { useToast } from '../../composables/useToast'
import { priceFields, schoolCatalog, type SchoolCatalogRow, type SchoolQuote } from '../../utils/beichenModels'
import { formatPercent, formatTokens, multiplyDecimal, shiftDecimal } from '../../utils/credits'
import { schoolDay, statusLabel } from '../../utils/format'
import AppDialog from '../../components/AppDialog.vue'
import BeichenPriceDrawer from '../../components/BeichenPriceDrawer.vue'
import BeichenModelUsageDrawer from '../../components/BeichenModelUsageDrawer.vue'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string>; providerMaintenance?: boolean }>()
type OverviewCredential = {
  credential_id: string
  label: string
  catalog_last_success?: string | null
  prices_last_success?: string | null
  billing_last_success?: string | null
  usage_last_success?: string | null
  last_error?: string | null
}
type ModelOverview = BeichenOverview & { credentials?: OverviewCredential[] | null }
type UsageState = { data?: BeichenModelUsage; loading?: boolean; error?: string }
type PriceState = NonNullable<BeichenCatalogModel['price_state']>
type BatchOutcome = { model: string; status: 'enabled' | 'skipped' | 'failed'; reason: string }

const overview = ref<ModelOverview>()
const usageStates = ref<Partial<Record<BeichenUsageRange, UsageState>>>({})
const range = ref<BeichenUsageRange>('month')
const loading = ref(false)
const updating = ref('')
const syncing = ref(false)
const error = ref('')
const toast = useToast()
const reasonId = useId()
const priceColumns = [{ id: 'official', label: '官网价' }, { id: 'student', label: '学生实价' }] as const
const priceStateLabels: Record<PriceState, string> = {
  confirmed: '报价已确认', conditional: '按条件计价', manual: '手动价', missing: '缺少报价', mixed: '混合报价',
}
const tokenPriceUnit = '每百万 Token 的积分'
const convertibleUnits = new Set([tokenPriceUnit, '积分/次', '积分/秒'])
const filters = [{ id: 'all', label: '全部' }, { id: 'active', label: '已启用' }, { id: 'pending', label: '待配置' }, { id: 'unpriced', label: '无报价' }, { id: 'manual', label: '手动价' }] as const
const filter = ref<typeof filters[number]['id']>('all')
const priceRow = ref<SchoolCatalogRow>()
const conditionsRow = ref<SchoolCatalogRow>()
const usageOpen = ref(false)
const initialModel = ref('')
const catalog = computed(() => {
  const data = overview.value
  if (!data) return []
  const entries = new Map((data.model_catalog ?? []).map(row => [row.model.toLowerCase(), row]))
  const observedKeys = new Map<string, Set<string>>()
  for (const record of data.models ?? []) {
    const model = record.model.toLowerCase()
    const ids = observedKeys.get(model) ?? new Set<string>()
    ids.add(record.credential_id)
    observedKeys.set(model, ids)
  }
  return schoolCatalog(data, props.providerId).map(row => {
    const model = row.model.toLowerCase()
    const entry = entries.get(model)
    const routes = (entry?.preferred_routes ?? []).filter(route => route.provider_id === props.providerId)
    const protocols = new Set(routes.map(route => route.adapter).filter(Boolean))
    const keys = new Set([
      ...(entry?.credential_ids ?? []), ...(observedKeys.get(model) ?? []), ...row.credentials.map(key => key.credential_id),
    ].filter(Boolean))
    const keyCoverage = validCount(entry?.key_count) ?? keys.size
    const routeCount = validCount(entry?.route_count) ?? row.routeCount
    const readyCount = validCount(entry?.ready_count) ?? row.readyCount
    const activation = entry?.activation
    const active = row.status === 'active' || activation === 'active'
    const canDisable = row.canDisable || (activation === 'active' && Boolean(entry?.school_model))
    const priceState = entry?.price_state ?? oldPriceState(row)
    const priceCovered = entry?.price_state == null ? row.priceReady
      : priceState === 'confirmed' || priceState === 'manual' || (priceState === 'mixed' && row.priceReady)
    const blockers = [...new Set((entry?.blockers ?? []).map(blocker => blocker.message?.trim()).filter(Boolean))]
    const pricing = (row.quotes.length ? row.quotes : [null]).map(quote => ({
      key: quote?.key ?? 'missing',
      status: quote?.status ?? 'pending',
      statusLabel: quote?.statusLabel ?? '单价待同步',
      multiplierLabel: quote?.multiplierLabel,
      notes: quote?.notes ?? [],
      official: displayPrice(quote, false),
      student: displayPrice(quote, true),
    }))
    return {
      ...row, active, activation, canDisable, blockers, keyCoverage, routeCount, readyCount, priceState, priceCovered, pricing,
      protocolCount: protocols.size || (routeCount === 0 ? 0 : null),
    }
  })
})
type CatalogRow = (typeof catalog.value)[number]
const catalogByModel = computed(() => new Map(catalog.value.map(row => [row.model.toLowerCase(), row])))
const searchedRows = computed(() => catalog.value.filter(row =>
  row.model.toLowerCase().includes((props.query ?? '').trim().toLowerCase()) &&
  (!props.commonIds || props.commonIds.has(row.model.toLowerCase()))))
const rows = computed(() => searchedRows.value.filter(row => filter.value === 'all' ||
  filter.value === 'active' && row.active ||
  filter.value === 'pending' && !row.active ||
  filter.value === 'unpriced' && !row.priceCovered ||
  filter.value === 'manual' && (row.mode !== 'follow' || row.priceState === 'manual')))
const monthlyUsage = computed<UsageState>(() => usageStates.value.month ?? {})
const drawerUsage = computed<UsageState>(() => usageStates.value[range.value] ?? {})
const usageByModel = computed(() => new Map((monthlyUsage.value.data?.models ?? []).map(row => [row.model.toLowerCase(), row])))
const monthlyStats = computed(() => {
  const totals = monthlyUsage.value.data?.totals
  const hitRate = totals?.cache_hit_rate
  return [
    { label: '已扣积分', value: shiftDecimal(totals?.credits, 0) },
    { label: 'Token 总数', value: totals?.completeness === 'missing' && totals.tokens === 0 ? '—' : formatTokens(totals?.tokens) },
    { label: '缓存命中率', value: hitRate != null && hitRate >= 0 && hitRate <= 1 ? formatPercent(hitRate) : '—' },
    { label: '待核笔数', value: formatTokens(totals?.pending_calls) },
  ]
})

const batchSelection = ref<string[]>()
const batchBusy = ref(false)
const batchProgress = ref<{ completed: number; total: number }>()
const batchResults = ref<BatchOutcome[]>([])
const busy = computed(() => loading.value || syncing.value || Boolean(updating.value) || batchBusy.value)
const batchPlan = computed(() => planBatch(batchSelection.value ?? []))
const batchEligible = computed(() => batchPlan.value.filter(item => !item.reason))
const batchSkipped = computed(() => batchPlan.value.filter(item => item.reason))
const batchCounts = computed(() => ({
  enabled: batchResults.value.filter(row => row.status === 'enabled').length,
  skipped: batchResults.value.filter(row => row.status === 'skipped').length,
  failed: batchResults.value.filter(row => row.status === 'failed').length,
}))
const keyHealth = computed(() => {
  const keys = new Map(catalog.value.flatMap(row => row.credentials.map(key => [key.credential_id, key.label] as const)))
  for (const entry of overview.value?.model_catalog ?? []) {
    for (const id of entry.credential_ids ?? []) if (!keys.has(id)) keys.set(id, '未命名 Key')
  }
  for (const record of overview.value?.models ?? []) if (!keys.has(record.credential_id)) keys.set(record.credential_id, '未命名 Key')
  for (const dataset of overview.value?.datasets ?? []) if (!keys.has(dataset.credential_id)) keys.set(dataset.credential_id, '未命名 Key')
  const credentials = new Map((overview.value?.credentials ?? []).map(key => [key.credential_id, key]))
  for (const key of credentials.values()) if (key.label || !keys.has(key.credential_id)) keys.set(key.credential_id, key.label || '未命名 Key')
  return Array.from(keys, ([id, label]) => {
    const credential = credentials.get(id)
    const datasets = (overview.value?.datasets ?? []).filter(row => row.credential_id === id)
    const errors = [...new Set([credential?.last_error, ...datasets.map(row => row.last_error)]
      .filter((message): message is string => Boolean(message)))]
    const healthy = !errors.length && Boolean(credential?.catalog_last_success || credential?.prices_last_success ||
      credential?.billing_last_success || credential?.usage_last_success || datasets.some(row => row.last_success))
    return { id, label, healthy, state: errors.length ? '同步异常' : healthy ? '已同步' : '待同步', errors }
  })
})
const keyCount = computed(() => {
  const count = overview.value?.credentials?.length ??
    Math.max(validCount(overview.value?.website_sync?.channels) ?? 0, keyHealth.value.length)
  return catalog.value.reduce((total, row) => Math.max(total, row.keyCoverage), count)
})
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

async function loadUsage(period: BeichenUsageRange) {
  const providerId = props.providerId
  usageStates.value[period] = { loading: true }
  const request = usageStates.value[period]
  const isCurrent = () => !disposed && props.providerId === providerId && usageStates.value[period] === request
  try {
    const data = await teacherAPI.modelUsage(period, providerId)
    if (isCurrent()) usageStates.value[period] = { data }
  } catch (e) {
    if (isCurrent()) usageStates.value[period] = { error: apiErrorMessage(e, '读取用量失败，请重试') }
  }
}

function validCount(value?: number | null) {
  return value != null && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

function oldPriceState(row: SchoolCatalogRow): PriceState {
  const states = new Set<PriceState>(row.quotes.map(quote => {
    if (quote.mode === 'manual') return 'manual'
    if (hasPriceConditions(quote)) return 'conditional'
    return quote.status === 'confirmed' || quote.priceReady ? 'confirmed' : 'missing'
  }))
  return states.size > 1 ? 'mixed' : states.values().next().value ?? 'missing'
}

function publicationLabel(row: CatalogRow) {
  if (row.active) return '已启用'
  if (row.activation === 'ready') return '可启用'
  if (row.status === 'not_configured') return '未配置'
  return statusLabel(row.status ?? undefined) || '未发布'
}

function canEnableReason(row: CatalogRow) {
  if (row.activation === 'ready') return ''
  if (row.blockers.length) return row.blockers.join('；')
  if (row.activation == null && row.canEnable == null) return '启用条件待同步，请先刷新模型'
  if (!row.priceCovered) return row.priceState === 'conditional' ? '条件价待核实，可先手动定价' : '官网未报价，请先手动定价'
  if (row.activation === 'blocked' || !row.canEnable || !row.routeCount) return '线路未就绪，请刷新模型或检查渠道配置'
  return ''
}

function planBatch(models: string[]) {
  return models.map(model => {
    const row = catalogByModel.value.get(model.toLowerCase())
    const active = row?.active ?? false
    const canEnable = row?.activation === 'ready' || (row?.activation == null && row?.canEnable === true)
    const reason = active ? '已启用' : canEnable ? '' : row ? canEnableReason(row) : '当前目录中没有此模型'
    return { model, active, reason }
  })
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

async function changeStatus(row: CatalogRow, enable: boolean) {
  if (busy.value || enable && (row.active || canEnableReason(row))) return
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
  if (busy.value) return
  batchSelection.value = Array.from(new Map(rows.value.map(row => [row.model.toLowerCase(), row.model])).values())
  batchProgress.value = undefined
  batchResults.value = []
}
async function enableBatch(retry = false) {
  const selection = batchSelection.value
  if (!selection || busy.value || (retry ? !batchCounts.value.failed : !batchEligible.value.length || batchProgress.value)) return
  const providerId = props.providerId
  const isCurrent = () => !disposed && props.providerId === providerId && batchSelection.value === selection
  batchBusy.value = true
  try {
    const failures = batchResults.value.filter(item => item.status === 'failed').map(item => item.model)
    if (retry && !planBatch(failures).some(item => !item.reason)) await load()
    if (!isCurrent() || !overview.value) return
    const plan = retry ? planBatch(failures) : batchPlan.value
    const models = plan.filter(item => !item.reason).map(item => item.model)
    const results = new Map(batchResults.value.map(item => [item.model, item]))
    for (const item of plan) {
      if (!item.reason) results.delete(item.model)
      else if (!retry || item.active) results.set(item.model, { model: item.model, status: 'skipped', reason: item.reason })
      else results.set(item.model, { model: item.model, status: 'failed', reason: item.reason })
    }
    batchResults.value = Array.from(results.values())
    batchProgress.value = { completed: 0, total: models.length }
    for (let offset = 0; offset < models.length; offset += 200) {
      const chunk = models.slice(offset, offset + 200)
      let outcomes: BatchOutcome[]
      try {
        const result = await teacherAPI.enableBeichenModelsBatch(providerId, chunk)
        const enabled = new Set((result.enabled ?? []).map(item => item.model.toLowerCase()))
        const skipped = new Map((result.skipped ?? []).map(item => [item.model.toLowerCase(), item]))
        outcomes = chunk.map(model => {
          const key = model.toLowerCase()
          if (enabled.has(key)) return { model, status: 'enabled', reason: '已启用' }
          const item = skipped.get(key)
          if (item?.reason === 'already_active') return { model, status: 'skipped', reason: item.message || '已启用' }
          return { model, status: 'failed', reason: item ? item.message || '线路或价格未就绪' : '服务器未返回结果，请刷新后重试' }
        })
      } catch (cause) {
        const reason = apiErrorMessage(cause, '启用失败，请稍后重试')
        outcomes = chunk.map(model => ({ model, status: 'failed', reason }))
      }
      if (!isCurrent()) return
      for (const outcome of outcomes) results.set(outcome.model, outcome)
      batchResults.value = Array.from(results.values())
      batchProgress.value.completed += chunk.length
    }
    if (models.length) {
      const counts = batchCounts.value
      const message = `已启用 ${counts.enabled} 个，跳过 ${counts.skipped} 个，失败 ${counts.failed} 个`
      if (counts.failed) toast.error(message)
      else toast.success(message)
      await load()
    }
  } finally {
    if (isCurrent()) batchBusy.value = false
  }
}

function openUsage(model = '') {
  initialModel.value = model
  range.value = 'month'
  usageOpen.value = true
}
function changeUsageRange(period: BeichenUsageRange) {
  if (range.value === period) return
  range.value = period
  if (!usageStates.value[period]) void loadUsage(period)
}
async function priceSaved() {
  toast.success('价格已保存，后续调用采用新计价方式')
  await load()
}
function syncTime(value?: string | null) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return '尚未同步'
  return new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}
function hasPriceConditions(quote: SchoolQuote) {
  return quote.status === 'conditional' || /上下文分档|推理强度/u.test((quote.standard ?? '') + (quote.actual ?? ''))
}

function withYuan(text: string) {
  return text.split('\n').map(line => {
    const [unit, ...amounts] = line.split(' · ')
    if (!convertibleUnits.has(unit)) return line
    return [unit, ...amounts.map(amount => amount.replace(/ (\d+(?:\.\d+)?)$/, (_match, credits: string) =>
      ` ${credits} 积分（${shiftDecimal(credits, -2)} 元）`))].join(' · ')
  }).join('\n')
}

function displayPrice(quote: SchoolQuote | null, student: boolean) {
  const manual = student && quote?.mode === 'manual'
  if (quote && hasPriceConditions(quote) && !manual) return { conditional: true, cells: [], details: '' }
  // schoolCatalog 已把官网元价换成积分，跟随价也已按该线路的倍率计算。
  const text = (manual ? '' : student ? quote?.actual : quote?.standard) ?? ''
  const [first, ...other] = text.split('\n')
  const tokenLine = first.startsWith(tokenPriceUnit) ? first : ''
  const useGrid = manual || tokenLine || (!text && (!quote?.standard || quote.standard.startsWith(tokenPriceUnit)))
  const amounts = tokenLine.split(' · ').slice(1)
  const legacy = student && quote?.status === 'confirmed' && !quote.standard && !quote.actual
  const cells = useGrid ? priceFields.map(({ key, label }) => {
    const raw = manual || legacy ? quote?.actualRates?.[key]
      : amounts.find(amount => amount.startsWith(label + ' '))?.slice(label.length + 1)
    const base = shiftDecimal(raw, 0)
    // 学生实价 = 基础价 × 1.10，与网关记账保持一致；缺值保持「—」。
    const credits = student && base !== '—' ? multiplyDecimal(base, '1.10') : base
    return { key, label, credits, yuan: shiftDecimal(credits, -2) }
  }) : []
  return { conditional: false, cells, details: withYuan(tokenLine ? other.join('\n') : text) }
}

function modelUsage(model: string) { return usageByModel.value.get(model.toLowerCase()) }
function modelCredits(model: string) {
  const credits = shiftDecimal(modelUsage(model)?.credits, 0)
  return credits === '—' ? credits : credits + ' 积分'
}
function modelTokens(model: string) {
  const usage = modelUsage(model)
  return usage && !(usage.completeness === 'missing' && usage.tokens === 0) ? formatTokens(usage.tokens) : '—'
}

watch(() => props.providerId, () => {
  overview.value = undefined
  usageStates.value = {}
  priceRow.value = undefined
  conditionsRow.value = undefined
  usageOpen.value = false
  batchSelection.value = undefined
  batchProgress.value = undefined
  batchResults.value = []
  batchBusy.value = false
  initialized = true
  void Promise.all([load(), loadUsage('month')])
})
useVisibilityPolling(async () => {
  if (busy.value) return
  if (!initialized) {
    initialized = true
    await Promise.all([load(), loadUsage('month')])
  } else await load()
}, 60_000)
onBeforeUnmount(() => { disposed = true; overviewRequest++ })
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
    <section class="beichen-card monthly-usage" aria-label="本月用量汇总" :aria-busy="monthlyUsage.loading">
      <div class="beichen-section-head">
        <h2>本月用量</h2>
        <button type="button" class="button ghost" @click="openUsage()">查看全部明细</button>
      </div>
      <dl class="monthly-stats">
        <div v-for="stat in monthlyStats" :key="stat.label">
          <dt>{{ stat.label }}</dt><dd>{{ monthlyUsage.loading ? '…' : stat.value }}</dd>
        </div>
      </dl>
      <div v-if="monthlyUsage.error" class="error usage-feedback" role="alert">
        <span>{{ monthlyUsage.error }}</span>
        <button type="button" class="button ghost" @click="loadUsage('month')">重试用量</button>
      </div>
    </section>
    <div class="beichen-summary model-summary" aria-label="模型概况">
      <div class="beichen-stat"><span>可用模型</span><strong>{{ overview ? formatTokens(catalog.length) : '—' }}</strong><small>已从上游发现</small></div>
      <div class="beichen-stat"><span>已启用</span><strong>{{ overview ? formatTokens(catalog.filter(row => row.active).length) : '—' }}</strong><small>学校模型</small></div>
      <div class="beichen-stat"><span>报价覆盖</span><strong>{{ overview ? catalog.filter(row => row.priceCovered).length + ' / ' + catalog.length : '—' }}</strong><small>含教师手动价</small></div>
      <div class="beichen-stat sync-stat"><span>价格最后同步</span><strong>{{ syncTime(lastSync) }}</strong><small>北京时间</small></div>
      <div class="beichen-stat"><span>Key 健康</span><strong>{{ keyHealth.filter(key => key.healthy).length }} / {{ keyCount }}</strong><small>无同步错误且已成功读取</small></div>
    </div>
    <details v-if="keyHealth.length" class="key-health"><summary>查看 Key 同步状态</summary><ul><li v-for="key in keyHealth" :key="key.id"><strong>{{ key.label }}</strong><span class="status-pill" :class="key.healthy ? 'active' : key.errors.length ? 'danger' : 'pending'">{{ key.state }}</span><small v-for="message in key.errors" :key="message" class="error">{{ message }}</small></li></ul></details>
    <div class="beichen-toolbar model-toolbar">
      <div class="beichen-toolbar-group">
        <button type="button" class="button ghost" :disabled="busy" @click="refreshOverview">刷新模型</button>
        <button type="button" class="button secondary" :disabled="busy" @click="syncPrices">{{ syncing ? '同步中…' : '立即同步价格' }}</button>
        <button type="button" class="button primary" :disabled="busy || !rows.length" @click="previewBatch">一键启用</button>
      </div>
    </div>
    <div class="model-filters" role="group" aria-label="模型状态筛选"><button v-for="item in filters" :key="item.id" type="button" :aria-pressed="filter === item.id" @click="filter = item.id">{{ item.label }}</button><small>当前筛选 {{ rows.length }} 个模型</small></div>
    <p class="beichen-note">Token 单价按每百万 Token 计，1 元 = 100 积分。跟随官网时，学生实价按该线路的倍率计算。</p>
    <p v-if="error && !batchSelection" class="error" role="alert">{{ error }}</p>
    <p v-if="providerMaintenance && loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="priceErrors.length" class="error" role="alert">价格同步失败：{{ priceErrors.join('；') }}</p>
    <div v-if="loading && !overview" class="model-skeleton" role="status"><span class="sr-only">正在读取模型…</span><i v-for="index in 3" :key="index" aria-hidden="true" /></div>
    <div v-if="rows.length" class="model-list" data-layout="responsive-cards">
      <table class="beichen-table model-table">
        <caption class="sr-only">北辰模型覆盖、价格与本月用量</caption>
        <colgroup><col class="model-col"><col class="state-col"><col class="price-col"><col class="price-col"><col class="usage-col"><col class="action-col"></colgroup>
        <thead>
          <tr><th scope="col">模型</th><th scope="col">状态与覆盖</th><th scope="col">官网价</th><th scope="col">学生实价</th><th scope="col">本月用量</th><th scope="col">操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="row.model" :data-model="row.model">
            <th scope="row" class="model-name">{{ row.model }}</th>
            <td data-label="状态与覆盖" class="model-state">
              <span>{{ row.keyCoverage }}/{{ keyCount }} 把 Key 覆盖</span> ·
              <span :title="row.routeCount + ' 条候选线路'">{{ row.protocolCount == null ? '协议数待同步' : row.protocolCount + ' 种协议' }}</span> ·
              <span>{{ row.readyCount }} 条已发布</span> ·
              <span>{{ priceStateLabels[row.priceState] ?? '报价待同步' }}</span> ·
              <strong :class="{ 'published-state': row.active }">{{ publicationLabel(row) }}</strong>
            </td>
            <td v-for="column in priceColumns" :key="column.id" :data-label="column.label"
              :class="column.id === 'official' ? 'standard-price' : 'actual-price'">
              <div v-for="(quote, quoteIndex) in row.pricing" :key="quote.key" class="quote-row" :data-price-status="quote.status">
                <div class="quote-caption">
                  <span v-if="row.pricing.length > 1">报价 {{ quoteIndex + 1 }}</span>
                  <span v-if="column.id === 'official'" class="status-pill" :class="quote.status">{{ quote.statusLabel }}</span>
                  <span v-else>{{ quote.multiplierLabel ?? '倍率待同步' }}</span>
                </div>
                <template v-if="quote[column.id].conditional">
                  <button v-if="column.id === 'official'" type="button" class="price-link" @click="conditionsRow = row">查看档位</button>
                  <span v-else class="price-empty">随计价档位变化</span>
                </template>
                <dl v-if="quote[column.id].cells.length" class="price-grid">
                  <div v-for="cell in quote[column.id].cells" :key="cell.key" class="price-cell">
                    <dt class="price-label">{{ cell.label }}</dt>
                    <dd class="price-value">
                      <span class="price-credits">{{ cell.credits }}<small v-if="cell.credits !== '—'"> 积分</small></span>
                      <span v-if="cell.yuan !== '—'" class="price-sub">{{ cell.yuan }} 元</span>
                    </dd>
                  </div>
                </dl>
                <p v-if="quote[column.id].details" class="price-line">{{ quote[column.id].details }}</p>
                <span v-else-if="!quote[column.id].cells.length && !quote[column.id].conditional" class="price-empty">—</span>
                <template v-if="column.id === 'official'">
                  <small v-for="note in quote.notes" :key="note" class="row-note">{{ note }}</small>
                </template>
              </div>
            </td>
            <td data-label="本月用量" class="model-usage">
              <button type="button" class="usage-link" :aria-label="'查看 ' + row.model + ' 用量'" @click="openUsage(row.model)">{{ monthlyUsage.loading ? '…' : modelCredits(row.model) }}</button>
              <small class="row-note">Token {{ modelTokens(row.model) }}</small>
              <small v-if="modelUsage(row.model)?.pending_calls" class="row-note">待核 {{ formatTokens(modelUsage(row.model)?.pending_calls) }} 笔</small>
            </td>
            <td data-label="操作">
              <div class="model-actions">
                <button type="button" class="button ghost" :disabled="busy" :aria-label="'定价 ' + row.model" @click="priceRow = row">{{ row.mode !== 'follow' ? '调整价格' : '手动定价' }}</button>
                <button v-if="!row.active" type="button" class="button secondary" :disabled="busy || Boolean(canEnableReason(row))"
                  :aria-describedby="canEnableReason(row) ? reasonId + '-' + index : undefined" :aria-label="'启用 ' + row.model"
                  @click="changeStatus(row, true)">{{ updating === row.model ? '启用中…' : '启用' }}</button>
                <button v-else-if="row.canDisable" type="button" class="button ghost" :disabled="busy"
                  :aria-label="'禁用 ' + row.model" @click="changeStatus(row, false)">禁用</button>
                <small v-if="!row.active && canEnableReason(row)" :id="reasonId + '-' + index" class="row-note enable-reason">{{ canEnableReason(row) }}</small>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else-if="!loading && !error" class="beichen-empty"><strong>{{ catalog.length ? '当前筛选没有匹配模型' : '暂无模型记录' }}</strong><span>{{ catalog.length ? '调整搜索、常用模型或状态筛选后再试。' : '后台会自动发现上游模型，也可以立即刷新目录。' }}</span><button type="button" class="button ghost" @click="catalog.length ? filter = 'all' : refreshOverview()">{{ catalog.length ? '查看全部状态' : '刷新模型' }}</button></div>
  </section>

  <BeichenPriceDrawer :open="Boolean(priceRow)" :provider-id="providerId" :row="priceRow" @update:open="open => { if (!open) priceRow = undefined }" @saved="priceSaved" />
  <BeichenModelUsageDrawer v-model:open="usageOpen" :range="range" :usage="drawerUsage.data" :loading="drawerUsage.loading"
    :error="drawerUsage.error" :initial-model="initialModel" @update:range="changeUsageRange" @retry="loadUsage(range)" />
  <AppDialog :open="Boolean(batchSelection)" title="一键启用当前筛选模型" description="确认当前筛选结果中的可启用模型，其他模型将跳过。"
    @update:open="open => { if (!open && !batchBusy) batchSelection = undefined }">
    <section class="batch-preview beichen-stack" :aria-busy="batchBusy">
      <template v-if="!batchProgress">
        <p class="batch-count" role="status">将启用 {{ batchEligible.length }} 个 · 跳过 {{ batchSkipped.length }} 个</p>
        <div class="batch-list">
          <h3>将启用</h3>
          <ul v-if="batchEligible.length"><li v-for="item in batchEligible" :key="item.model">{{ item.model }}</li></ul>
          <p v-else class="beichen-note">当前没有可启用的模型。</p>
          <h3 v-if="batchSkipped.length">跳过原因</h3>
          <ul v-if="batchSkipped.length">
            <li v-for="item in batchSkipped" :key="item.model"><strong>{{ item.model }}</strong><span>{{ item.reason }}</span></li>
          </ul>
        </div>
      </template>
      <template v-else>
        <div v-if="batchBusy && batchProgress.total" class="batch-progress">
          <p role="status">已处理 {{ batchProgress.completed }}/{{ batchProgress.total }} 个</p>
          <progress aria-label="批量启用进度" :value="batchProgress.completed" :max="batchProgress.total" />
        </div>
        <p class="batch-count" role="status">已启用 {{ batchCounts.enabled }} 个 · 跳过 {{ batchCounts.skipped }} 个 · 失败 {{ batchCounts.failed }} 个</p>
        <ul v-if="batchResults.length" class="batch-list">
          <li v-for="item in batchResults" :key="item.model" :data-batch-status="item.status">
            <strong>{{ item.model }}</strong>
            <span>{{ item.status === 'failed' ? '失败 · ' : item.status === 'skipped' ? '跳过 · ' : '' }}{{ item.reason }}</span>
          </li>
        </ul>
      </template>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions batch-actions">
        <button type="button" class="button ghost" :disabled="batchBusy" @click="batchSelection = undefined">{{ batchProgress ? '完成' : '取消' }}</button>
        <button v-if="!batchProgress" type="button" class="button primary" :disabled="busy || !batchEligible.length"
          @click="enableBatch()">确认启用 {{ batchEligible.length }} 个</button>
        <button v-else-if="batchCounts.failed" type="button" class="button primary" :disabled="busy"
          @click="enableBatch(true)">重试失败项</button>
      </div>
    </section>
  </AppDialog>
  <AppDialog :open="Boolean(conditionsRow)" :title="(conditionsRow?.model ?? '模型') + ' · 计价档位'" description="查看官网计价条件及按线路倍率换算的学生实价。"
    wide @update:open="open => { if (!open) conditionsRow = undefined }">
    <div class="beichen-stack">
      <section v-for="quote in conditionsRow?.quotes ?? []" :key="quote.key" class="condition-quote">
        <h3>{{ quote.multiplierLabel }}</h3>
        <dl class="condition-prices">
          <div><dt>官网价</dt><dd class="price-detail">{{ quote.standard ? withYuan(quote.standard) : '尚未取得官网明细' }}</dd></div>
          <div><dt>学生实价</dt><dd class="price-detail">{{ quote.mode === 'manual' ? '请查看列表中的手动价' : quote.actual ? withYuan(quote.actual) : '尚无可用单价' }}</dd></div>
        </dl>
        <p v-for="note in quote.notes" :key="note" class="beichen-note">{{ note }}</p>
      </section>
    </div>
  </AppDialog>
</template>

<style scoped>
.beichen-models, .model-list { min-width: 0; max-width: 100%; }
.beichen-models { grid-template-columns: minmax(0, 1fr); overflow-wrap: anywhere; }
.monthly-usage { display: grid; gap: 12px; }
.monthly-usage h2 { font-size: 17px; }
.monthly-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 130px), 1fr)); gap: 10px; margin: 0; }
.monthly-stats > div { min-width: 0; padding: 10px 12px; border-radius: 12px; background: var(--primary-soft); }
.monthly-stats dt { font-size: 12px; color: var(--ink-soft); }
.monthly-stats dd { margin: 4px 0 0; font-size: 20px; font-weight: 700; font-variant-numeric: tabular-nums; }
.model-summary { grid-template-columns: repeat(auto-fit, minmax(min(100%, 130px), 1fr)); }
.model-toolbar { justify-content: flex-end; }
.model-list { container-type: inline-size; }
.model-table { table-layout: fixed; }
.model-col { width: 16%; } .state-col { width: 20%; } .price-col { width: 20%; } .usage-col { width: 11%; } .action-col { width: 13%; }
.model-table thead th { white-space: normal; }
.model-table th, .model-table td { min-width: 0; overflow-wrap: anywhere; }
.model-name { font-size: 13px; }
.model-state { font-size: 12px; line-height: 1.75; }
.published-state { color: #16765e; }
.beichen-models .button { min-width: 0; max-width: 100%; white-space: normal; }
.model-actions { display: flex; gap: 6px; flex-wrap: wrap; }
.model-actions .button { padding: 7px 10px; }
.model-actions .row-note { flex-basis: 100%; }
.row-note { display: block; color: var(--ink-soft); font-size: 11px; line-height: 1.5; margin-top: 6px; overflow-wrap: anywhere; }
.enable-reason { color: #835615; }
.price-line { white-space: pre-line; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; font-size: 12px; line-height: 1.6; margin: 0; }
.quote-row { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; }
.quote-row + .quote-row { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); }
.quote-caption { display: flex; align-items: center; gap: 6px; min-height: 22px; flex-wrap: wrap; font-size: 11px; font-variant-numeric: tabular-nums; }
.quote-caption .status-pill { white-space: normal; }
.price-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; margin: 0; }
.price-cell { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr); align-items: baseline; gap: 2px 8px; min-height: 32px; }
.price-cell > .price-value { margin: 0; font-size: 13px; text-align: right; font-variant-numeric: tabular-nums; }
.price-value small { font-size: 10px; font-weight: 500; }
.price-sub { font-size: 10px; font-weight: 400; overflow-wrap: anywhere; }
.price-link, .usage-link { padding: 0; border: 0; background: transparent; text-align: left; color: var(--primary-strong); font-weight: 700; font-size: 12px; overflow-wrap: anywhere; }
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
.batch-progress p { margin: 0 0 5px; }
.batch-progress progress { width: 100%; accent-color: var(--primary); }
.batch-actions { flex-wrap: wrap; }
.batch-list { max-height: 40vh; overflow-y: auto; padding: 0; margin: 0; overflow-wrap: anywhere; }
.batch-list h3 { margin: 16px 0 8px; font-size: 13px; }
.batch-list h3:first-child { margin-top: 0; }
.batch-list ul { list-style: none; padding: 0; margin: 0; }
.batch-list li { display: grid; gap: 4px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.batch-list li span { font-size: 12px; color: var(--ink-soft); }
.batch-list [data-batch-status="failed"] span { color: var(--danger, #b53b55); }
.condition-quote h3 { font-size: 14px; margin-top: 0; }
.condition-prices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; margin: 0; }
.condition-prices dt { font-weight: 700; font-size: 13px; margin-bottom: 8px; }
.price-detail { white-space: pre-line; overflow-wrap: anywhere; color: var(--ink-soft); font-variant-numeric: tabular-nums; font-size: 13px; margin: 0; line-height: 1.7; }
button:focus-visible, summary:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@container (max-width: 54rem) {
  .model-table { display: block; }
  .model-table colgroup, .model-table thead { display: none; }
  .model-table tbody { display: grid; gap: 16px; }
  .model-table tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 10px; padding: 16px 0; border: 1px solid var(--line); border-radius: 14px; }
  .model-table th, .model-table td { display: block; width: auto; padding: 0 14px; border: 0; }
  .model-name, .model-state { grid-column: 1 / -1; }
  .model-table td[data-label]::before { content: attr(data-label); display: block; margin-bottom: 6px; font-size: 11px; color: var(--ink-soft); }
  .price-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .price-cell { grid-template-columns: minmax(0, 1fr); }
  .model-actions .row-note { margin-top: 0; }
}
@container (max-width: 29rem) {
  .model-table tbody tr { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 900px) {
  .condition-prices { grid-template-columns: minmax(0, 1fr); gap: 16px; }
}
@media (max-width: 600px) {
  .model-filters small { margin-left: 0; flex-basis: 100%; }
  .beichen-toolbar-group { width: 100%; }
  .beichen-toolbar-group > .button { flex: 1; }
}
</style>
