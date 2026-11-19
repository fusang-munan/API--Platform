<script setup lang="ts">
import { computed, nextTick, reactive, ref, useId, watch } from 'vue'
import { APIError, apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { BeichenComparison, BeichenImport, BeichenImportKind, BeichenModel, BeichenOverview, BeichenPriceUpdate, BeichenRates, Provider } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import BeichenCatalogAccess from './BeichenCatalogAccess.vue'
import { priceFields, scaledNumber } from '../../utils/beichenModels'
import LoadingButton from '../../components/LoadingButton.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { fullTime } from '../../utils/format'

const { provider } = defineProps<{ provider: Provider }>()

const usageFields = [
  { local: 'local_requests', upstream: 'upstream_requests', label: '请求数' },
  { local: 'local_input_tokens', upstream: 'upstream_input_tokens', label: '输入' },
  { local: 'local_output_tokens', upstream: 'upstream_output_tokens', label: '输出' },
  { local: 'local_cache_read_tokens', upstream: 'upstream_cache_read_tokens', label: '缓存读取' },
  { local: 'local_cache_write_tokens', upstream: 'upstream_cache_write_tokens', label: '缓存写入' },
] as const satisfies ReadonlyArray<{ local: keyof BeichenComparison; upstream: keyof BeichenComparison; label: string }>
const usageSourceLabels: Record<NonNullable<BeichenComparison['upstream_usage_source']>, string> = {
  usage: '自动同步',
  csv: '手工汇总',
  export: '导入明细（待核对）',
}
const datasetLabels: Record<string, string> = {
  catalog: '模型目录',
  billing: '计费倍率',
  prices: '上游价格',
  usage: '用量',
}
const priceHeader = 'model,input,output,cache_read,cache_write'
const csvFormats: Record<BeichenImportKind, { label: string; header: string; hint: string }> = {
  prices: {
    label: '价格（最终实际单价）',
    header: priceHeader,
    hint: '填写每百万 Token 的最终单价，以百万分之一北辰余额单位计。',
  },
  prices_base: {
    label: '价格（已确认的基础价）',
    header: priceHeader,
    hint: '填写每百万 Token 的基础价，以百万分之一北辰余额单位计。系统会乘一次该渠道的实际倍率。',
  },
  usage: {
    label: '用量（日汇总）',
    header: 'date,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros',
    hint: '每行填写一个模型的日汇总，金额以百万分之一北辰余额单位计。',
  },
  usage_export: {
    label: '北辰用量明细',
    header: 'Time,API Key Name,Model,Reasoning Effort,Inbound Endpoint,IP Address,Type,Billing Mode,Input Tokens,Output Tokens,Cache Read Tokens,Cache Creation Tokens,Rate Multiplier,Billed Cost,Original Cost,First Token (ms),Duration (ms)',
    hint: '先在北辰「用量」页选一把 Key 和一个已经结束的完整北京时间账日，清除模型、请求类别等额外筛选后导出。回到 Nebula 选择同名凭据。',
  },
}
const maxImportBytes = 1024 * 1024
const maxRate = 1_000_000_000_000
const integerFormat = new Intl.NumberFormat('zh-CN')
const billingDayFormat = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
})
const ratePattern = '[0-9]+([.][0-9]{1,6})?'
const rateRegex = new RegExp(`^${ratePattern}$`)

type Action = 'sync' | 'import' | 'price'
type Feedback = { text: string; error?: boolean }
class CSVImportError extends Error {}

const open = ref(false)
const day = ref('')
const requestedDay = ref('')
const snapshot = ref<{ day: string; overview: BeichenOverview }>()
const overviewError = ref('')
const loadState = useLoadState()
const action = ref<Action>()
const feedback = reactive<Partial<Record<Action, Feedback>>>({})
const busy = computed(() => loadState.loading.value || Boolean(action.value))
const datasetRows = computed(() => snapshot.value?.overview.datasets ?? [])
const modelRows = computed(() => snapshot.value?.overview.models ?? [])
const comparison = computed(() => snapshot.value?.day === requestedDay.value ? snapshot.value?.overview.comparison ?? [] : null)

const credentials = computed(() => (provider.credentials ?? []).filter(item => item.credential_configured))
const credentialId = ref('')
const importKind = ref<BeichenImportKind>('prices')
const usageExportConfirmed = ref(false)
const importMultiplier = computed(() => datasetRows.value.find(
  row => row.kind === 'billing' && !row.last_error && row.credential_id === credentialId.value,
)?.effective_multiplier)
const csvInput = ref<HTMLInputElement>()
const csvHintId = useId()
const usageExportImpactId = useId()
const editing = ref<BeichenModel>()
const priceEditor = ref<HTMLFormElement>()
const priceTitleId = useId()
const priceMode = ref<BeichenPriceUpdate['mode']>('manual')
const priceDraft = reactive({ input: '', output: '', cache_read: '', cache_write: '' })
let priceTrigger: HTMLButtonElement | undefined

watch([credentialId, importKind], () => { usageExportConfirmed.value = false })

function credentialLabel(id: string) {
  return provider.credentials?.find(item => item.id === id)?.label || id || '—'
}

function operationError(error: unknown, fallback: string) {
  if (error instanceof CSVImportError) return error.message
  if (error instanceof APIError && error.code === 'VALIDATION_ERROR' && /[\u3400-\u9fff]/.test(error.serverMessage)) {
    return error.serverMessage
  }
  return apiErrorMessage(error, fallback)
}

function setOpen(value: boolean) {
  open.value = value
  if (!value) {
    usageExportConfirmed.value = false
    return
  }
  if (busy.value) return
  day.value = requestedDay.value || billingDayFormat.format(new Date())
  void loadOverview(day.value)
}

async function loadOverview(date: string) {
  requestedDay.value = date
  overviewError.value = ''
  await loadState.run(async () => {
    try {
      const overview = await teacherAPI.beichenOverview(provider.id, date)
      snapshot.value = { day: date, overview }
    } catch (error) {
      snapshot.value = undefined
      overviewError.value = operationError(error, '请检查网络后重试')
    }
  })
}

function compareDay() {
  if (day.value && !busy.value) void loadOverview(day.value)
}

async function runAction(kind: Action, work: () => Promise<unknown>, success: string, fallback: string) {
  if (busy.value) return false
  action.value = kind
  delete feedback[kind]
  try {
    await work()
    feedback[kind] = { text: success }
  } catch (error) {
    feedback[kind] = { text: operationError(error, fallback), error: true }
  } finally {
    // A failed sync can still persist a dataset error on the server.
    if (kind === 'sync' || !feedback[kind]?.error) await loadOverview(requestedDay.value)
    action.value = undefined
  }
  return !feedback[kind]?.error
}

function sync() {
  void runAction('sync', () => teacherAPI.syncBeichen(provider.id), '同步已结束，请查看各项结果。', '同步失败，请稍后重试')
}

async function importCSV() {
  const file = csvInput.value?.files?.[0]
  const credential = credentialId.value
  const kind = importKind.value
  if (busy.value || !file || !credentials.value.some(item => item.id === credential)) return
  if (kind === 'usage_export' && !usageExportConfirmed.value) return

  await runAction('import', async () => {
    if (file.size > maxImportBytes) throw new CSVImportError('CSV 文件超过 1 MiB，请选择较小的文件。')
    let csv: string
    try {
      csv = new TextDecoder('utf-8', { fatal: true }).decode(await file.arrayBuffer())
    } catch {
      throw new CSVImportError('无法读取 CSV，请检查文件是否为 UTF-8 编码。')
    }
    const newline = csv.indexOf('\n')
    const header = (newline < 0 ? csv : csv.slice(0, newline)).replace(/\r$/, '')
    const columns = header.split(',').map(column => column.replace(/^"([^"]+)"$/, '$1')).join(',')
    if (columns !== csvFormats[kind].header) throw new CSVImportError('CSV 表头与所选类型不符，请核对列名和顺序。')
    if (newline < 0 || !csv.slice(newline + 1).trim()) throw new CSVImportError('CSV 只有表头，请选择含数据的文件。')
    const selection = { credential_id: credential, csv }
    const body: BeichenImport = kind === 'usage_export'
      ? { ...selection, kind, full_day_confirmed: true }
      : { ...selection, kind }
    if (new TextEncoder().encode(JSON.stringify(body)).length > maxImportBytes) {
      throw new CSVImportError('导入请求超过 1 MiB，请选择较小的文件。')
    }
    try {
      await teacherAPI.importBeichen(provider.id, body)
    } catch (error) {
      if (kind !== 'usage_export') throw error
      // Fixed messages keep API errors from echoing key names or IP addresses.
      throw new CSVImportError(error instanceof APIError && error.code === 'VALIDATION_ERROR'
        ? '北辰用量明细未通过校验。请核对凭据名称、已结束的北京时间账日及文件格式。'
        : '北辰用量明细导入失败，请稍后重试。')
    }
    usageExportConfirmed.value = false
    if (csvInput.value) csvInput.value.value = ''
  }, kind === 'usage_export' ? '明细已处理，请查看按日对账。' : 'CSV 已导入。', 'CSV 导入失败，请核对文件后重试')
}

function amount(value: number | string | null | undefined, unit: 'USD' | 'credit', digits = 6) {
  const number = scaledNumber(value, digits, true)
  if (number == null) return '—'
  return unit === 'USD' ? `${number} 单位` : `${number} credit`
}

function trialCredits(row: BeichenComparison) {
  if (row.trial_credits === undefined) return amount(row.trial_credit_milli, 'credit', 3)
  return exactCredits(row.trial_credits)
}

function exactCredits(value?: string | null) {
  return value == null ? '—' : `${value} credit`
}

function count(value?: number | null) {
  return value == null ? '—' : integerFormat.format(value)
}

function multiplierLabel(value?: number | string | null) {
  return value == null || value === '' ? '—' : `${value}×`
}

async function editPrice(row: BeichenModel, event: MouseEvent) {
  priceTrigger = event.currentTarget as HTMLButtonElement
  editing.value = row
  priceMode.value = 'manual'
  for (const { key } of priceFields) {
    const rate = row.school_rates?.[key]
    priceDraft[key] = scaledNumber(rate, 6) ?? ''
  }
  delete feedback.price
  await nextTick()
  priceEditor.value?.focus()
}

async function closePriceEditor() {
  editing.value = undefined
  await nextTick()
  priceTrigger?.focus()
  priceTrigger = undefined
}

function parseRate(value: string) {
  if (!rateRegex.test(value)) return NaN
  const [whole, fraction = ''] = value.split('.')
  return Number(whole + fraction.padEnd(6, '0'))
}

async function savePrice() {
  const row = editing.value
  if (!row || busy.value) return
  const identity = { credential_id: row.credential_id, model: row.model }
  let body: BeichenPriceUpdate = { ...identity, mode: 'follow' }
  if (priceMode.value === 'manual') {
    const rates: BeichenRates = {
      input: parseRate(priceDraft.input),
      output: parseRate(priceDraft.output),
      cache_read: priceDraft.cache_read === '' ? undefined : parseRate(priceDraft.cache_read),
      cache_write: priceDraft.cache_write === '' ? undefined : parseRate(priceDraft.cache_write),
    }
    if (!Object.values(rates).every(rate => rate === undefined || Number.isSafeInteger(rate) && rate <= maxRate)) {
      feedback.price = { text: '请填写有效单价，最多六位小数，每项上限为 1,000,000 credit。', error: true }
      return
    }
    body = { ...identity, mode: 'manual', rates }
  }

  if (await runAction('price', () => teacherAPI.setBeichenPrice(provider.id, body),
    body.mode === 'follow' ? '已恢复跟随。' : '校内手填价格已保存。', '价格保存失败，请稍后重试')) {
    await closePriceEditor()
  }
}
</script>

<template>
  <AppDialog
    :open="open"
    :title="`${provider.name} · 同步与对账`"
    description="查看各渠道扫描结果、模型单价及开销，可按日期对账和维护报价。"
    wide
    @update:open="setOpen"
  >
    <template #trigger>
      <button type="button" class="button secondary">同步与对账</button>
    </template>

    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" label="正在读取北辰概况" :rows="2">
      <div class="beichen-sections">
        <div v-if="overviewError" class="error banner">
          <p role="alert">北辰概况读取失败。{{ overviewError }}</p>
          <button type="button" class="button ghost" :disabled="busy" @click="loadOverview(requestedDay)">重新读取</button>
        </div>

        <details class="beichen-section">
          <summary>同步状态</summary>
          <div class="beichen-body">
            <div class="beichen-toolbar">
              <LoadingButton class="button primary" :pending="action === 'sync'" :disabled="busy" pending-label="同步中…" @click="sync">手动同步</LoadingButton>
              <p v-if="feedback.sync" :class="feedback.sync.error ? 'error' : 'success'" :role="feedback.sync.error ? 'alert' : 'status'">{{ feedback.sync.text }}</p>
            </div>
            <div v-if="datasetRows.length" class="beichen-table" tabindex="0" role="region" aria-label="同步结果">
              <table class="usage-table">
                <thead><tr><th scope="col">凭据</th><th scope="col">数据</th><th scope="col">上次成功</th><th scope="col">最近错误</th></tr></thead>
                <tbody>
                  <tr v-for="row in datasetRows" :key="JSON.stringify([row.credential_id, row.kind])">
                    <th scope="row">{{ credentialLabel(row.credential_id) }}</th>
                    <td>
                      {{ datasetLabels[row.kind] ?? row.kind }}
                      <strong v-if="row.kind === 'billing'" class="beichen-multiplier">{{ multiplierLabel(row.effective_multiplier) }}</strong>
                      <small v-if="row.kind === 'billing' && row.last_error && row.effective_multiplier != null">上次记录，本次倍率未知</small>
                    </td>
                    <td>{{ fullTime(row.last_success) }}</td>
                    <td :class="{ error: row.last_error }">{{ row.last_error || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else-if="snapshot" class="muted">暂无同步记录。</p>
          </div>
        </details>

        <details class="beichen-section">
          <summary>按日对账</summary>
          <div class="beichen-body">
            <form class="beichen-toolbar" @submit.prevent="compareDay">
              <label>对账日期（北京时间）<input v-model="day" type="date" min="2000-01-01" max="9999-12-31" required :disabled="busy"></label>
              <LoadingButton class="button secondary" type="submit" :disabled="busy || !day" :pending="loadState.loading.value && !action" pending-label="查询中…">查询</LoadingButton>
            </form>
            <template v-if="comparison">
              <p class="muted">账期 {{ snapshot?.day }}。请求数和 Token 用量均按「校内 / 北辰」排列。正式实扣来自逐笔记录；— 表示暂无金额或数据。</p>
              <div v-if="comparison.length" class="beichen-table" tabindex="0" role="region" aria-label="按凭据和模型对账">
                <table class="usage-table">
                  <thead>
                    <tr>
                      <th scope="col">模型 / 凭据</th>
                      <th v-for="field in usageFields" :key="field.local" scope="col" class="numeric-cell">{{ field.label }}</th>
                      <th scope="col" class="numeric-cell">上游费用</th>
                      <th scope="col" class="numeric-cell">校内试算</th>
                      <th scope="col" class="numeric-cell">校内正式实扣</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="row in comparison" :key="JSON.stringify([row.credential_id, row.model])">
                      <th scope="row">{{ row.model }}<small class="muted">{{ credentialLabel(row.credential_id) }}</small></th>
                      <td v-for="field in usageFields" :key="field.local" class="numeric-cell">
                        <span class="sr-only">校内 </span>{{ count(row[field.local]) }}<br>
                        <span class="sr-only">北辰 </span>{{ count(row[field.upstream]) }}
                      </td>
                      <td class="numeric-cell">
                        估算 {{ amount(row.estimated_upstream_micros, 'USD') }}<br>
                        账单 {{ amount(row.billed_upstream_micros, 'USD') }}
                        <small v-if="row.upstream_usage_source" class="muted">{{ usageSourceLabels[row.upstream_usage_source] }}</small>
                      </td>
                      <td class="numeric-cell">{{ trialCredits(row) }}<br>未定价次数 {{ count(row.trial_unpriced_calls) }}</td>
                      <td class="numeric-cell">{{ exactCredits(row.charged_credits) }}<br>待核账 {{ count(row.pending_review_calls) }} 条</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <p v-else class="muted">这一天暂无对账记录。</p>
            </template>
            <p v-else-if="overviewError" class="error">未能读取 {{ requestedDay }} 的对账数据。{{ overviewError }}</p>
          </div>
        </details>

        <h3>高级维护</h3>
        <BeichenCatalogAccess :provider-id="provider.id" :credentials="credentials" />
        <details class="beichen-section">
          <summary>导入官方 CSV</summary>
          <form class="beichen-body" @submit.prevent="importCSV">
            <p v-if="!credentials.length" class="muted">请先在供应商设置中配置凭据。</p>
            <fieldset v-else class="beichen-body" :disabled="busy">
              <div class="grid two">
                <label>导入到凭据
                  <select v-model="credentialId" required>
                    <option value="" disabled>请选择凭据</option>
                    <option v-for="item in credentials" :key="item.id" :value="item.id">
                      {{ item.label }}{{ item.status === 'inactive' ? '（已停用）' : '' }}
                    </option>
                  </select>
                </label>
                <label>CSV 类型
                  <select v-model="importKind">
                    <option v-for="(format, kind) in csvFormats" :key="kind" :value="kind">{{ format.label }}</option>
                  </select>
                </label>
              </div>
              <label>CSV 文件<input ref="csvInput" type="file" accept=".csv,text/csv" required :aria-describedby="csvHintId" @change="usageExportConfirmed = false"></label>
              <p :id="csvHintId" class="muted">
                {{ csvFormats[importKind].hint }}
                文件须为 UTF-8 编码，最多 2,000 条记录。表头为 <code>{{ csvFormats[importKind].header }}</code>
              </p>
              <p v-if="importKind === 'prices_base' && credentialId" class="muted">
                当前凭据实际倍率 {{ multiplierLabel(importMultiplier) }}<span v-if="importMultiplier == null">，同步倍率后才能计算最终单价。</span>
              </p>
              <template v-if="importKind === 'usage_export'">
                <p :id="usageExportImpactId" class="muted">
                  系统核对凭据名称和单日范围，按模型汇总为待核对补账。若该凭据已有自动同步确认的日账，整日以自动日账为准。
                  已有手工日汇总也优先，后续同步到的自动日账可替代补账。
                  若使用了默认「近 24 小时」范围，或记录不完整，请重新导出后再导入。
                </p>
                <p class="muted">文件含 IP 地址等使用明细，请勿将原文件或内容发到聊天、工单或日志。</p>
                <label class="beichen-confirm">
                  <input v-model="usageExportConfirmed" type="checkbox" required :aria-describedby="usageExportImpactId">
                  <span>我已确认文件包含同名凭据在一个已经结束的完整北京时间账日内的全部记录，作为待核对补账导入。</span>
                </label>
              </template>
            </fieldset>
            <div v-if="credentials.length" class="beichen-toolbar">
              <LoadingButton class="button secondary" type="submit" :pending="action === 'import'" :disabled="busy || (importKind === 'usage_export' && !usageExportConfirmed)" pending-label="导入中…">导入 CSV</LoadingButton>
              <p v-if="feedback.import" :class="feedback.import.error ? 'error' : 'success'" :role="feedback.import.error ? 'alert' : 'status'">{{ feedback.import.text }}</p>
            </div>
          </form>
        </details>

        <details class="beichen-section">
          <summary>模型价格</summary>
          <div class="beichen-body">
            <p class="muted">每格依次显示每百万 Token 的北辰余额单位和校内试算积分。1 北辰余额单位折合 100 积分。</p>
            <form v-if="editing" ref="priceEditor" class="beichen-body beichen-editor" tabindex="-1" :aria-labelledby="priceTitleId" @submit.prevent="savePrice">
              <strong :id="priceTitleId">{{ editing.model }} · {{ credentialLabel(editing.credential_id) }}</strong>
              <label>校内定价方式
                <select v-model="priceMode" :disabled="busy">
                  <option value="manual">手填价格</option>
                  <option value="follow">恢复跟随</option>
                </select>
              </label>
              <fieldset v-if="priceMode === 'manual'" class="grid two" :disabled="busy">
                <legend class="sr-only">校内单价，credit / 百万 Token</legend>
                <label v-for="field in priceFields" :key="field.key">
                  {{ field.label }}
                  <input v-model.trim="priceDraft[field.key]" type="text" inputmode="decimal" :pattern="ratePattern" maxlength="24" placeholder="例如 0.25" :required="field.key === 'input' || field.key === 'output'">
                </label>
              </fieldset>
              <p v-if="priceMode === 'manual'" class="muted">最多六位小数。输入和输出必填，缓存价可留空，免费项请填写 0。</p>
              <div class="beichen-toolbar">
                <LoadingButton class="button primary" type="submit" :pending="action === 'price'" :disabled="busy" pending-label="保存中…">{{ priceMode === 'follow' ? '恢复跟随' : '保存价格' }}</LoadingButton>
                <button type="button" class="button ghost" :disabled="action === 'price'" @click="closePriceEditor">取消</button>
              </div>
            </form>
            <p v-if="feedback.price" :class="feedback.price.error ? 'error' : 'success'" :role="feedback.price.error ? 'alert' : 'status'">{{ feedback.price.text }}</p>
            <div v-if="modelRows.length" class="beichen-table" tabindex="0" role="region" aria-label="上游与校内模型单价">
              <table class="usage-table">
                <thead>
                  <tr>
                    <th scope="col">模型 / 凭据</th>
                    <th scope="col">校内定价方式</th>
                    <th v-for="field in priceFields" :key="field.key" scope="col" class="numeric-cell">{{ field.label }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in modelRows" :key="JSON.stringify([row.credential_id, row.model])">
                    <th scope="row">{{ row.model }}<small class="muted">{{ credentialLabel(row.credential_id) }}</small></th>
                    <td>
                      {{ row.mode === 'manual' ? '手填' : '跟随' }}
                      <button type="button" class="button ghost beichen-edit" :disabled="busy" :aria-label="`编辑价格 ${row.model} · ${credentialLabel(row.credential_id)}`" @click="editPrice(row, $event)">编辑价格</button>
                    </td>
                    <td v-for="field in priceFields" :key="field.key" class="numeric-cell">
                      <span class="sr-only">上游 </span>{{ amount(row.upstream_rates?.[field.key], 'USD') }}<br>
                      <span class="sr-only">校内 </span>{{ amount(row.school_rates?.[field.key], 'credit') }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else-if="snapshot" class="muted">暂无模型价格。</p>
          </div>
        </details>
      </div>
    </LoadingRegion>
  </AppDialog>
</template>

<style scoped>
.beichen-sections, .beichen-body { display: grid; gap: 0.75rem; min-width: 0; }
.beichen-sections p { margin: 0; overflow-wrap: anywhere; }
.beichen-sections .banner { margin: 0; }
.beichen-sections .banner .button { margin-top: 0.5rem; }
.beichen-section {
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: 12px;
}
.beichen-section summary {
  padding: 0.8rem 1rem;
  color: var(--ink);
  font-weight: 700;
  cursor: pointer;
}
.beichen-section > .beichen-body { padding: 0 1rem 1rem; }
.beichen-toolbar { display: flex; align-items: end; flex-wrap: wrap; gap: 0.65rem; }
.beichen-toolbar p { align-self: center; }
.beichen-confirm { display: flex; align-items: flex-start; gap: 0.6rem; line-height: 1.5; }
.beichen-confirm input { flex: none; width: 1rem; height: 1rem; margin: 0.1rem 0 0; padding: 0; }
.beichen-editor { padding: 0.85rem; border: 1px solid var(--line); border-radius: 12px; }
.beichen-table { max-width: 100%; overflow-x: auto; }
.beichen-table .usage-table { display: table; font-size: 13px; }
.beichen-table th[scope="row"] { min-width: 8rem; white-space: normal; overflow-wrap: anywhere; }
.beichen-table small, .beichen-edit { display: block; }
.beichen-multiplier { margin-inline-start: 0.35em; white-space: nowrap; font-variant-numeric: tabular-nums; }
.beichen-edit { margin-top: 0.35rem; white-space: nowrap; }
.beichen-table .numeric-cell { line-height: 1.65; }
.beichen-table:focus-visible, .beichen-section summary:focus-visible, .beichen-editor:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: 2px;
}
</style>
