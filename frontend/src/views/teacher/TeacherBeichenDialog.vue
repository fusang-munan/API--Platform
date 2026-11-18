<script setup lang="ts">
import { computed, nextTick, reactive, ref, useId } from 'vue'
import { APIError, apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { BeichenComparison, BeichenImportKind, BeichenModel, BeichenOverview, BeichenPriceUpdate, BeichenRates, Provider } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import LoadingButton from '../../components/LoadingButton.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { fullTime } from '../../utils/format'

const { provider } = defineProps<{ provider: Provider }>()

const priceFields = [
  { key: 'input', label: '输入' },
  { key: 'output', label: '输出' },
  { key: 'cache_read', label: '缓存读取' },
  { key: 'cache_write', label: '缓存写入' },
] as const
const usageFields = [
  { local: 'local_requests', upstream: 'upstream_requests', label: '请求数' },
  { local: 'local_input_tokens', upstream: 'upstream_input_tokens', label: '输入' },
  { local: 'local_output_tokens', upstream: 'upstream_output_tokens', label: '输出' },
  { local: 'local_cache_read_tokens', upstream: 'upstream_cache_read_tokens', label: '缓存读取' },
  { local: 'local_cache_write_tokens', upstream: 'upstream_cache_write_tokens', label: '缓存写入' },
] as const satisfies ReadonlyArray<{ local: keyof BeichenComparison; upstream: keyof BeichenComparison; label: string }>
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
    hint: '填写最终实际单价，单位为微美元 / 百万 Token。',
  },
  prices_base: {
    label: '价格（已确认的基础价）',
    header: priceHeader,
    hint: '填写已确认的基础价，单位为微美元 / 百万 Token。最终单价由基础价乘以该凭据的北辰实际倍率计算。',
  },
  usage: {
    label: '用量',
    header: 'date,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros',
    hint: '填写逐日用量，账单金额以微美元计。',
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
const importMultiplier = computed(() => datasetRows.value.find(
  row => row.kind === 'billing' && row.credential_id === credentialId.value,
)?.effective_multiplier)
const csvInput = ref<HTMLInputElement>()
const csvHintId = useId()
const editing = ref<BeichenModel>()
const priceEditor = ref<HTMLFormElement>()
const priceTitleId = useId()
const priceMode = ref<BeichenPriceUpdate['mode']>('manual')
const priceDraft = reactive({ input: '', output: '', cache_read: '', cache_write: '' })
let priceTrigger: HTMLButtonElement | undefined

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
  if (!value || busy.value) return
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
    const body = { credential_id: credential, kind, csv }
    if (new TextEncoder().encode(JSON.stringify(body)).length > maxImportBytes) {
      throw new CSVImportError('导入请求超过 1 MiB，请选择较小的文件。')
    }
    await teacherAPI.importBeichen(provider.id, body)
    if (csvInput.value) csvInput.value.value = ''
  }, 'CSV 已导入。', 'CSV 导入失败，请核对文件后重试')
}

function scaledNumber(value: number, digits: number, group = false) {
  const scale = 10 ** digits
  const absolute = Math.abs(value)
  const whole = Math.floor(absolute / scale)
  const fraction = String(absolute % scale).padStart(digits, '0').replace(/0+$/, '')
  return `${value < 0 ? '-' : ''}${group ? integerFormat.format(whole) : whole}${fraction ? `.${fraction}` : ''}`
}

function amount(value: number | null | undefined, unit: 'USD' | 'credit', digits = 6) {
  if (value == null) return '—'
  const number = scaledNumber(value, digits, true)
  return unit === 'USD' ? `$${number}` : `${number} credit`
}

function count(value?: number | null) {
  return value == null ? '—' : integerFormat.format(value)
}

function multiplierLabel(value?: number | null) {
  return value == null ? '—' : `${value}×`
}

async function editPrice(row: BeichenModel, event: MouseEvent) {
  priceTrigger = event.currentTarget as HTMLButtonElement
  editing.value = row
  priceMode.value = 'manual'
  for (const { key } of priceFields) {
    const rate = row.school_rates?.[key]
    priceDraft[key] = rate == null ? '' : scaledNumber(rate, 6)
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
    description="查看北辰同步结果、核对每日用量、导入官方 CSV 和编辑校内模型价格。"
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

        <details class="beichen-section" open>
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
              <p class="muted">账期 {{ snapshot?.day }}。请求数和 Token 用量均按「校内 / 北辰」排列。</p>
              <div v-if="comparison.length" class="beichen-table" tabindex="0" role="region" aria-label="按凭据和模型对账">
                <table class="usage-table">
                  <thead>
                    <tr>
                      <th scope="col">模型 / 凭据</th>
                      <th v-for="field in usageFields" :key="field.local" scope="col" class="numeric-cell">{{ field.label }}</th>
                      <th scope="col" class="numeric-cell">上游费用</th>
                      <th scope="col" class="numeric-cell">校内试算</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="row in comparison" :key="JSON.stringify([row.credential_id, row.model])">
                      <th scope="row">{{ row.model }}<small class="muted">{{ credentialLabel(row.credential_id) }}</small></th>
                      <td v-for="field in usageFields" :key="field.local" class="numeric-cell">
                        <span class="sr-only">校内 </span>{{ count(row[field.local]) }}<br>
                        <span class="sr-only">北辰 </span>{{ count(row[field.upstream]) }}
                      </td>
                      <td class="numeric-cell">估算 {{ amount(row.estimated_upstream_micros, 'USD') }}<br>账单 {{ amount(row.billed_upstream_micros, 'USD') }}</td>
                      <td class="numeric-cell">{{ amount(row.trial_credit_milli, 'credit', 3) }}<br>未定价次数 {{ count(row.trial_unpriced_calls) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <p v-else class="muted">这一天暂无对账记录。</p>
            </template>
            <p v-else-if="overviewError" class="error">未能读取 {{ requestedDay }} 的对账数据。{{ overviewError }}</p>
          </div>
        </details>

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
              <label>CSV 文件<input ref="csvInput" type="file" accept=".csv,text/csv" required :aria-describedby="csvHintId"></label>
              <p :id="csvHintId" class="muted">
                {{ csvFormats[importKind].hint }}
                文件须为 UTF-8 编码，最多 2,000 条记录。表头为 <code>{{ csvFormats[importKind].header }}</code>
              </p>
              <p v-if="importKind === 'prices_base' && credentialId" class="muted">
                当前凭据实际倍率 {{ multiplierLabel(importMultiplier) }}<span v-if="importMultiplier == null">，同步倍率后才能计算最终单价。</span>
              </p>
            </fieldset>
            <div v-if="credentials.length" class="beichen-toolbar">
              <LoadingButton class="button secondary" type="submit" :pending="action === 'import'" :disabled="busy" pending-label="导入中…">导入 CSV</LoadingButton>
              <p v-if="feedback.import" :class="feedback.import.error ? 'error' : 'success'" :role="feedback.import.error ? 'alert' : 'status'">{{ feedback.import.text }}</p>
            </div>
          </form>
        </details>

        <details class="beichen-section">
          <summary>模型价格</summary>
          <div class="beichen-body">
            <p class="muted">单价按百万 Token 显示。每格依次为上游（美元）、校内（credit）。</p>
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
