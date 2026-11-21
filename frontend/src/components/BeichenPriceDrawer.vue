<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, shallowRef, useId, watch } from 'vue'
import { teacherAPI } from '../api/teacher'
import { apiErrorMessage } from '../api/client'
import type { BeichenModel, BeichenModelPriceUpdate, BeichenOverview } from '../api/types'
import { priceFields, scaledNumber, type SchoolCatalogRow } from '../utils/beichenModels'
import { multiplyDecimal, shiftDecimal } from '../utils/credits'
import { schoolDay } from '../utils/format'
import AppDialog from './AppDialog.vue'

const props = defineProps<{ open: boolean; providerId: string; row?: SchoolCatalogRow }>()
const emit = defineEmits<{ 'update:open': [open: boolean]; saved: [] }>()
type RateKey = typeof priceFields[number]['key']
type Channel = { credential_id: string; label: string }
type PriceOverview = BeichenOverview & { credentials?: Array<Channel & { status?: string }> | null }
type SaveOutcome =
  | { status: 'saved'; count: number }
  | { status: 'different'; missing: Channel[]; extra: Channel[] }
  | { status: 'unknown'; selected: Channel[] }

const fieldId = useId()
const mode = ref<'follow' | 'manual'>('follow')
const scope = ref<'all' | 'credentials'>('all')
const selectedIds = ref<string[]>([])
const rates = reactive<Record<RateKey, string>>({ input: '', output: '', cache_read: '', cache_write: '' })
const overview = shallowRef<PriceOverview>()
const overviewLoading = ref(false)
const overviewError = ref('')
const saving = ref(false)
const outcome = shallowRef<SaveOutcome>()
const attempted = ref(false)
const error = ref('')
const decimalPattern = /^\d+(?:\.\d{1,6})?$/
let openVersion = 0

const modelName = computed(() => props.row?.model.trim().toLowerCase())
const catalogEntry = computed(() => overview.value?.model_catalog?.find(entry => entry.model.trim().toLowerCase() === modelName.value))
const modelPrices = computed(() => {
  const prices = new Map<string, BeichenModel>()
  for (const record of overview.value?.models ?? []) {
    if (record.model.trim().toLowerCase() === modelName.value) prices.set(record.credential_id, record)
  }
  return prices
})
const modelKeys = computed(() => {
  const entry = catalogEntry.value
  const ids = new Set(entry?.credential_ids)
  const labels = new Map(props.row?.credentials.map(key => [key.credential_id, key.label]))
  for (const route of entry?.preferred_routes ?? []) {
    if (route.provider_id !== props.providerId || !route.credential_id) continue
    if (route.upstream_model && route.upstream_model.trim().toLowerCase() !== modelName.value) continue
    ids.add(route.credential_id)
    if (route.credential_label) labels.set(route.credential_id, route.credential_label)
  }
  for (const id of modelPrices.value.keys()) ids.add(id)
  const credentials = new Map(overview.value?.credentials?.map(key => [key.credential_id, key]))
  return Array.from(ids, id => ({
    credential_id: id,
    label: credentials.get(id)?.label || labels.get(id) || `Key 编号 ${id}`,
    inactive: credentials.get(id)?.status === 'inactive',
  }))
})
const affectedKeys = computed(() => {
  if (scope.value === 'all') return modelKeys.value
  const selected = new Set(selectedIds.value)
  return modelKeys.value.filter(key => !key.inactive && selected.has(key.credential_id))
})
const incompleteScope = computed(() => modelKeys.value.length < (catalogEntry.value?.key_count ?? 0))
const editingDisabled = computed(() => !overview.value || overviewLoading.value || saving.value || Boolean(outcome.value))
const canSave = computed(() => props.open && props.row && !editingDisabled.value && affectedKeys.value.length > 0 &&
  (scope.value === 'credentials' || !incompleteScope.value))

const validation = computed(() => Object.fromEntries(priceFields.flatMap(({ key, label }) => {
  const value = rates[key].trim()
  if (!value && (key === 'input' || key === 'output')) return [[key, `请填写${label}价格`]]
  if (value && !decimalPattern.test(value)) return [[key, '请填非负数字，最多保留 6 位小数（例如 0.5）']]
  return []
})) as Partial<Record<RateKey, string>>)
const manualPrices = computed(() => priceFields.map(field => {
  const value = rates[field.key].trim() || (field.key.startsWith('cache_') ? '0' : '')
  return { ...field, value: decimalPattern.test(value) ? shiftDecimal(value, 0) : null }
}))
const followPrices = computed(() => affectedKeys.value.map(key => {
  const record = modelPrices.value.get(key.credential_id)
  const upstream = record?.upstream_rates
  // 已应用渠道倍率的上游报价以百万分之一元返回，再乘 100 换成积分。
  return { ...key, derived: record?.price_source === 'derived_from_usage',
    prices: priceFields.map(field => ({ ...field, value: scaledNumber(upstream?.[field.key], 4) })) }
}))
const hasDerived = computed(() => followPrices.value.some(quote => quote.derived) ||
  (props.row?.quotes.some(quote => quote.status === 'derived') ?? false))
const followLabel = computed(() => hasDerived.value ? '自动计价' : '跟随官网')
const importableQuote = computed(() => followPrices.value.find(({ prices }) =>
  prices.every(field => field.value == null ? field.key.startsWith('cache_') : decimalPattern.test(field.value))))
const confirmationPrices = computed(() => mode.value === 'manual'
  ? [{ credential_id: 'manual', label: '', prices: manualPrices.value }]
  : followPrices.value)

watch([() => props.open, () => props.providerId, () => props.row?.model], () => {
  openVersion++
  overview.value = undefined
  overviewLoading.value = false
  overviewError.value = ''
  saving.value = false
  outcome.value = undefined
  if (!props.open) return
  scope.value = 'all'
  selectedIds.value = []
  attempted.value = false
  error.value = ''
  mode.value = props.row?.mode === 'follow' && props.row.priceReady &&
    props.row.quotes.every(quote => quote.status !== 'conditional') ? 'follow' : 'manual'
  const quotes = props.row?.quotes.filter(quote => quote.actualRates)
  const current = quotes?.find(quote => quote.mode === 'manual') ?? quotes?.[0]
  for (const { key } of priceFields) rates[key] = current?.actualRates?.[key] ?? ''
  if (props.row) void loadOverview()
}, { immediate: true })
onBeforeUnmount(() => { openVersion++ })

async function loadOverview() {
  const version = openVersion
  const firstLoad = !overview.value
  overviewLoading.value = true
  overviewError.value = ''
  try {
    const data = await teacherAPI.beichenOverview(props.providerId, schoolDay())
    if (version !== openVersion) return
    overview.value = data
    const selectableIds = new Set(modelKeys.value.filter(key => !key.inactive).map(key => key.credential_id))
    selectedIds.value = selectedIds.value.filter(id => selectableIds.has(id))
    if (firstLoad) {
      const state = catalogEntry.value?.price_state
      if (state && state !== 'confirmed' && state !== 'derived') mode.value = 'manual'
      const current = Array.from(modelPrices.value.values()).filter(record => record.school_rates)
      const source = (current.find(record => record.mode === 'manual') ?? current[0])?.school_rates
      if (source) {
        for (const { key } of priceFields) rates[key] = scaledNumber(source[key], 6) ?? ''
      }
    }
  } catch {
    if (version === openVersion) overviewError.value = '读取此模型的 Key 失败，请重试。'
  } finally {
    if (version === openVersion) overviewLoading.value = false
  }
}

function importOfficial() {
  if (!importableQuote.value || editingDisabled.value) return
  for (const { key, value } of importableQuote.value.prices) rates[key] = value ?? ''
  attempted.value = false
  error.value = ''
}

function close(open: boolean) {
  if (!saving.value) emit('update:open', open)
}

async function save() {
  if (!canSave.value || !props.row) return
  attempted.value = true
  error.value = ''
  if (mode.value === 'manual' && Object.keys(validation.value).length) return
  const version = openVersion
  const selected = affectedKeys.value
  const selection = scope.value === 'all'
    ? { scope: 'all' as const }
    : { scope: 'credentials' as const, credential_ids: selected.map(key => key.credential_id) }
  const body: BeichenModelPriceUpdate = mode.value === 'follow'
    ? { model: props.row.model, mode: 'follow', ...selection }
    : { model: props.row.model, mode: 'manual', ...selection, rates: {
      input: rates.input.trim(), output: rates.output.trim(),
      cache_read: rates.cache_read.trim() || '0', cache_write: rates.cache_write.trim() || '0',
    } }
  saving.value = true
  try {
    const result = await teacherAPI.setBeichenModelPrice(props.providerId, body)
    if (version !== openVersion) return
    if (!result.saved) throw new Error('未保存')
    if (Array.isArray(result.applied_credential_ids)) {
      const actual = new Set(result.applied_credential_ids)
      const expected = new Set(selected.map(key => key.credential_id))
      const missing = selected.filter(key => !actual.has(key.credential_id))
      const labels = new Map([...overview.value?.credentials ?? [], ...modelKeys.value, ...result.credentials ?? []]
        .map(key => [key.credential_id, key.label]))
      const extra = Array.from(actual).filter(id => !expected.has(id))
        .map(id => ({ credential_id: id, label: labels.get(id) || `Key 编号 ${id}` }))
      outcome.value = missing.length || extra.length
        ? { status: 'different', missing, extra }
        : { status: 'saved', count: actual.size }
    } else {
      outcome.value = { status: 'unknown', selected }
    }
    emit('saved')
  } catch (cause) {
    if (version === openVersion) error.value = apiErrorMessage(cause, '价格保存失败，请保留当前填写内容并重试')
  } finally {
    if (version === openVersion) saving.value = false
  }
}
</script>

<template>
  <AppDialog :open="open" :title="`${row?.model ?? '模型'} · 价格设置`"
    description="选择价格和生效范围，保存前核对受影响的 Key。" @update:open="close">
    <form class="price-drawer beichen-stack" :aria-busy="saving" novalidate @submit.prevent="save">
      <fieldset class="price-mode" :disabled="editingDisabled">
        <legend>计价方式</legend>
        <label><input v-model="mode" type="radio" value="follow" :name="fieldId + '-mode'">{{ followLabel }}</label>
        <label><input v-model="mode" type="radio" value="manual" :name="fieldId + '-mode'">手动定价</label>
      </fieldset>
      <p v-if="hasDerived" class="beichen-note">反推价，待核对。自动计价优先采用同分组的正式官网价。反推价的缓存单价待核实。</p>

      <template v-if="mode === 'manual'">
        <div class="beichen-toolbar">
          <strong>基础价 · 积分 / 百万 Token</strong>
          <button type="button" class="button ghost" :disabled="editingDisabled || !importableQuote"
            :title="importableQuote ? `带入 ${importableQuote.label} 的渠道基础价` : '暂无可带入的官网基础价，请手动填写'"
            @click="importOfficial">{{ importableQuote?.derived ? '从反推价带入' : '从官网价带入' }}</button>
        </div>
        <div class="price-inputs">
          <div v-for="field in manualPrices" :key="field.key" class="price-input">
            <label :for="fieldId + '-' + field.key">{{ field.label }}<span v-if="field.key.startsWith('cache_')" class="optional">（选填）</span></label>
            <input :id="fieldId + '-' + field.key" v-model="rates[field.key]" :name="field.key" type="text" inputmode="decimal"
              autocomplete="off" :placeholder="field.key.startsWith('cache_') ? '留空按 0' : '例如 100'"
              :disabled="editingDisabled" :required="!field.key.startsWith('cache_')" maxlength="128"
              :aria-invalid="attempted && Boolean(validation[field.key])"
              :aria-describedby="fieldId + '-' + field.key + '-hint'">
            <p :id="fieldId + '-' + field.key + '-hint'" class="price-hint">
              <span v-if="attempted && validation[field.key]" class="error">{{ validation[field.key] }}</span>
              <span v-else-if="field.value != null">× 1.10 后预估扣 {{ multiplyDecimal(field.value, '1.10') }} 积分</span>
              <span v-else>非负数字，最多 6 位小数</span>
            </p>
          </div>
        </div>
      </template>
      <div v-else class="follow-hint">
        <p class="beichen-note">{{ hasDerived ? '基础价随所选报价和渠道倍率更新。使用了无报价的缓存项时，调用进入待核账。' : '基础价随官网报价和渠道倍率更新。缺少可用报价时，调用可能进入待核账。' }}</p>
      </div>

      <fieldset class="price-mode" :disabled="editingDisabled || !modelKeys.length">
        <legend>生效范围</legend>
        <label><input v-model="scope" type="radio" value="all" :name="fieldId + '-scope'">全部 Key</label>
        <label><input v-model="scope" type="radio" value="credentials" :name="fieldId + '-scope'">仅选中的 Key</label>
      </fieldset>
      <fieldset v-if="scope === 'credentials'" class="price-mode" :disabled="editingDisabled">
        <legend>选择 Key（可多选）</legend>
        <label v-for="key in modelKeys" :key="key.credential_id">
          <input v-model="selectedIds" type="checkbox" :value="key.credential_id" :disabled="key.inactive">
          {{ key.label }}<template v-if="key.inactive">（停用）</template>
        </label>
      </fieldset>

      <p v-if="overviewLoading" class="beichen-note" role="status">正在读取此模型的 Key…</p>
      <div v-else-if="!outcome && (overviewError || (overview && (!modelKeys.length || incompleteScope)))" class="scope-notice">
        <p class="beichen-note" role="alert">{{ overviewError || (incompleteScope
          ? '目录中的 Key 清单不完整，暂时无法保存全部 Key。'
          : '目录中暂无此模型的 Key。') }}</p>
        <button type="button" class="button ghost" :disabled="saving" @click="loadOverview">重新读取</button>
      </div>

      <section v-if="overview && !overviewLoading" class="price-confirmation" :aria-labelledby="fieldId + '-confirmation'">
        <h3 :id="fieldId + '-confirmation'">保存确认</h3>
        <dl class="confirmation-summary">
          <div><dt>影响 Key 数</dt><dd>{{ scope === 'all' && incompleteScope ? '待确认' : affectedKeys.length }}</dd></div>
          <div><dt>影响行数</dt><dd>{{ scope === 'all' && incompleteScope ? '待确认' : affectedKeys.length }}</dd></div>
          <div><dt>计价方式</dt><dd>{{ mode === 'follow' ? followLabel : '手动价' }}</dd></div>
        </dl>
        <h4>本次将影响的全部 Key</h4>
        <ul v-if="affectedKeys.length" class="affected-keys" aria-label="本次将影响的全部 Key">
          <li v-for="key in affectedKeys" :key="key.credential_id">{{ key.label }}<template v-if="key.inactive">（停用）</template></li>
        </ul>
        <p v-else-if="scope === 'credentials'" class="beichen-note">请至少选择一个 Key。</p>
        <p class="beichen-note">每个 Key 对应此模型的一行价格。</p>
        <table v-if="confirmationPrices.length" class="confirmation-rates">
          <caption>基础单价 / 百万 Token（1 元 = 100 积分）</caption>
          <thead><tr><th scope="col">用量项</th><th scope="col">元</th><th scope="col">积分</th></tr></thead>
          <tbody v-for="quote in confirmationPrices" :key="quote.credential_id">
            <tr v-if="quote.label" class="confirmation-channel"><th colspan="3" scope="rowgroup">{{ quote.label }}<template v-if="'derived' in quote && quote.derived"> · 反推价，待核对</template></th></tr>
            <tr v-for="field in quote.prices" :key="field.key">
              <th scope="row">{{ field.label }}</th>
              <td>{{ field.value == null ? (mode === 'follow' ? '待核' : '—') : shiftDecimal(field.value, -2) }}</td>
              <td>{{ field.value == null ? (mode === 'follow' ? '待核' : '—') : field.value }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="mode === 'manual' && scope === 'all'" class="beichen-note">新渠道同步后按此价生效。</p>
      </section>
      <p class="beichen-note">只影响保存后发起的调用，历史扣费保持原记账金额。</p>

      <p v-if="outcome?.status === 'saved'" class="price-saved" role="status">已保存 {{ outcome.count }} 个 Key 的价格。</p>
      <div v-else-if="outcome?.status === 'different'" class="price-warning" role="alert">
        <strong>服务端实际写入范围与选择不同</strong>
        <ul>
          <li v-if="outcome.missing.length">所选但未写入：{{ outcome.missing.map(key => key.label).join('、') }}</li>
          <li v-if="outcome.extra.length">未选但已写入：{{ outcome.extra.map(key => key.label).join('、') }}</li>
        </ul>
      </div>
      <div v-else-if="outcome?.status === 'unknown'" class="price-warning" role="alert">
        <strong>服务端未返回实际写入范围，以下 Key 的保存结果待确认</strong>
        <p>{{ outcome.selected.map(key => key.label).join('、') }}</p>
      </div>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="price-footer">
        <button v-if="outcome" type="button" class="button primary" @click="close(false)">完成</button>
        <template v-else>
          <button type="button" class="button ghost" :disabled="saving" @click="close(false)">取消</button>
          <button type="submit" class="button primary" :disabled="!canSave">{{ saving ? '保存中…' : mode === 'follow' ? '保存并' + followLabel : '保存手动价' }}</button>
        </template>
      </div>
    </form>
  </AppDialog>
</template>

<style scoped>
.price-drawer { min-width: 0; font-size: 13px; }
.price-mode { display: flex; gap: 20px; flex-wrap: wrap; border: 0; padding: 0; margin: 0; }
.price-mode legend { margin-bottom: 8px; }
.price-mode label { display: inline-flex; align-items: center; gap: 7px; overflow-wrap: anywhere; }
.price-mode input { flex: none; width: 16px; height: 16px; margin: 0; accent-color: var(--primary); box-shadow: none; }
.price-inputs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.price-input { min-width: 0; }
.price-input input { font-variant-numeric: tabular-nums; }
.price-input input[aria-invalid="true"] { border-color: #b53b55; }
.optional, .price-hint { color: var(--ink-soft); font-weight: 400; }
.price-hint { margin: 7px 0 0; font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.scope-notice { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.price-confirmation { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--line); border-radius: 12px; }
.price-confirmation h3, .price-confirmation h4 { font-size: 13px; margin: 0; }
.confirmation-summary { display: flex; flex-wrap: wrap; gap: 8px 20px; margin: 0 0 4px; }
.confirmation-summary div { display: flex; gap: 6px; }
.confirmation-summary dt { color: var(--ink-soft); }
.confirmation-summary dd { margin: 0; font-weight: 600; }
.affected-keys { display: flex; flex-wrap: wrap; gap: 4px 22px; margin: 0; padding-left: 17px; overflow-wrap: anywhere; }
.confirmation-rates { width: 100%; table-layout: fixed; border-collapse: collapse; font-variant-numeric: tabular-nums; }
.confirmation-rates caption { text-align: left; padding: 4px 0; font-weight: 600; }
.confirmation-rates th, .confirmation-rates td {
  padding: 4px 6px; text-align: left; border-bottom: 1px solid var(--line); overflow-wrap: anywhere;
}
.confirmation-rates th { font-weight: 500; }
.confirmation-channel th { background: var(--primary-soft); font-weight: 600; }
.price-warning, .price-saved { margin: 0; padding: 10px 12px; border-radius: 8px; overflow-wrap: anywhere; }
.price-warning { border-left: 3px solid #bf860b; background: #fff6db; color: #684700; }
.price-warning ul { margin: 6px 0 0; padding-left: 18px; }
.price-warning p { margin: 6px 0 0; }
.price-saved { background: #e8f7ef; color: #15563b; }
.price-footer { display: flex; justify-content: flex-end; gap: 8px; padding-top: 10px; }
button:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@media (max-width: 560px) {
  .price-inputs { grid-template-columns: minmax(0, 1fr); }
  .price-footer .button { flex: 1; }
}
</style>
