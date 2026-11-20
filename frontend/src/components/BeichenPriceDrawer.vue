<script setup lang="ts">
import { computed, reactive, ref, useId, watch } from 'vue'
import { teacherAPI } from '../api/teacher'
import { apiErrorMessage } from '../api/client'
import type { BeichenModelPriceUpdate } from '../api/types'
import { priceFields, type SchoolCatalogRow } from '../utils/beichenModels'
import { creditsToYuan, formatCredits, multiplyDecimal } from '../utils/credits'
import AppDialog from './AppDialog.vue'

const props = defineProps<{ open: boolean; providerId: string; row?: SchoolCatalogRow }>()
const emit = defineEmits<{ 'update:open': [open: boolean]; saved: [] }>()
type RateKey = typeof priceFields[number]['key']
const fieldId = useId()
const mode = ref<'follow' | 'manual'>('follow')
const rates = reactive<Record<RateKey, string>>({ input: '', output: '', cache_read: '', cache_write: '' })
const saving = ref(false)
const attempted = ref(false)
const error = ref('')
const decimalPattern = /^\d+(?:\.\d{1,6})?$/
const officialQuote = computed(() => props.row?.quotes.find(quote => quote.mode === 'follow' &&
  quote.actualRates?.input != null && quote.actualRates.output != null))
const canImport = computed(() => Boolean(officialQuote.value && priceFields.every(({ key }) => {
  const value = officialQuote.value?.actualRates?.[key]
  return value == null || decimalPattern.test(value)
})))
const validation = computed(() => Object.fromEntries(priceFields.flatMap(({ key, label }) => {
  const value = rates[key].trim()
  if (!value && (key === 'input' || key === 'output')) return [[key, `请填写${label}价格`]]
  if (value && !decimalPattern.test(value)) return [[key, '请填非负数字，最多保留 6 位小数（例如 0.5）']]
  return []
})) as Partial<Record<RateKey, string>>)
const previews = computed(() => priceFields.map(field => {
  const value = rates[field.key].trim() || (field.key.startsWith('cache_') ? '0' : '')
  return { ...field, value: decimalPattern.test(value) ? value : null,
    charged: decimalPattern.test(value) ? multiplyDecimal(value, '1.10') : null }
}))

watch(() => [props.open, props.row?.model] as const, () => {
  if (!props.open) return
  attempted.value = false
  error.value = ''
  mode.value = props.row?.mode === 'follow' && props.row.priceReady ? 'follow' : 'manual'
  const current = props.row?.quotes.find(quote => quote.mode === 'manual') ?? props.row?.quotes[0]
  for (const { key } of priceFields) rates[key] = current?.actualRates?.[key] ?? ''
}, { immediate: true })

function importOfficial() {
  if (!canImport.value || saving.value) return
  for (const { key } of priceFields) rates[key] = officialQuote.value?.actualRates?.[key] ?? ''
  mode.value = 'manual'
  attempted.value = false
  error.value = ''
}

function close(open: boolean) {
  if (!saving.value) emit('update:open', open)
}

async function save() {
  if (saving.value || !props.row) return
  attempted.value = true
  error.value = ''
  if (mode.value === 'manual' && Object.keys(validation.value).length) return
  // 新接口直接接收积分字符串；缩放到存储单位由服务端统一处理。
  const body: BeichenModelPriceUpdate = mode.value === 'follow'
    ? { model: props.row.model, mode: 'follow' }
    : { model: props.row.model, mode: 'manual', rates: {
      input: rates.input.trim(), output: rates.output.trim(),
      cache_read: rates.cache_read.trim() || '0', cache_write: rates.cache_write.trim() || '0',
    } }
  saving.value = true
  try {
    const result = await teacherAPI.setBeichenModelPrice(props.providerId, body)
    if (!result.saved) throw new Error('未保存')
    emit('saved')
    emit('update:open', false)
  } catch (cause) {
    error.value = apiErrorMessage(cause, '价格保存失败，请保留当前填写内容并重试')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <AppDialog :open="open" :title="`${row?.model ?? '模型'} · 价格设置`"
    description="设置所有关联 Key 的基础积分单价，或恢复跟随官网。" @update:open="close">
    <form class="price-drawer beichen-stack" :aria-busy="saving" novalidate @submit.prevent="save">
      <fieldset class="price-mode" :disabled="saving">
        <legend>计价方式</legend>
        <label><input v-model="mode" type="radio" value="follow" :name="fieldId + '-mode'">跟随官网</label>
        <label><input v-model="mode" type="radio" value="manual" :name="fieldId + '-mode'">手动定价</label>
      </fieldset>
      <p class="beichen-note">1 元对应 100 积分。手动价是基础单价，学生实际扣分再乘 1.10；保存后持续生效，直到恢复跟随官网。</p>

      <template v-if="mode === 'manual'">
        <div class="beichen-toolbar">
          <strong>基础价 · 积分 / 百万 Token</strong>
          <button type="button" class="button ghost" :disabled="saving || !canImport"
            :title="canImport ? '使用当前官网报价经渠道倍率计算后的基础价' : '暂无可带入的官网基础价，请手动填写'"
            @click="importOfficial">从官网价带入</button>
        </div>
        <div class="price-inputs">
          <div v-for="field in previews" :key="field.key" class="price-input">
            <label :for="fieldId + '-' + field.key">{{ field.label }}<span v-if="field.key.startsWith('cache_')" class="optional">（选填）</span></label>
            <input :id="fieldId + '-' + field.key" v-model="rates[field.key]" :name="field.key" type="text" inputmode="decimal"
              autocomplete="off" :placeholder="field.key.startsWith('cache_') ? '留空按 0' : '例如 100'"
              :disabled="saving" :required="!field.key.startsWith('cache_')" maxlength="128"
              :aria-invalid="attempted && Boolean(validation[field.key])"
              :aria-describedby="fieldId + '-' + field.key + '-hint'">
            <p :id="fieldId + '-' + field.key + '-hint'" class="price-hint">
              <span v-if="attempted && validation[field.key]" class="error">{{ validation[field.key] }}</span>
              <template v-else-if="field.value != null">约 {{ creditsToYuan(field.value, 8) }} 元 / 百万 Token<br>× 1.10 后实扣 {{ formatCredits(field.charged, 8) }} 积分</template>
              <span v-else>非负数字，最多 6 位小数</span>
            </p>
          </div>
        </div>
      </template>
      <div v-else class="follow-hint">
        <strong>保存后恢复自动更新</strong>
        <p class="beichen-note">后续调用按官网报价和渠道倍率计算基础积分，再乘 1.10 扣分。</p>
        <p v-if="!officialQuote" class="beichen-note">若官网仍未提供可用报价，调用可能进入待核账。已有手动价将在保存后停止生效。</p>
      </div>

      <section class="applied-keys" aria-label="定价适用 Key">
        <h3>应用到以下 Key</h3>
        <ul v-if="row?.credentials.length"><li v-for="credential in row.credentials" :key="credential.credential_id">{{ credential.label }}</li></ul>
        <p v-else class="beichen-note">应用到该供应商下所有提供此模型的 Key，具体范围由服务器核实。</p>
      </section>
      <p class="beichen-note">只影响保存后发起的调用，历史扣费保持原记账金额。</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="price-footer">
        <button type="button" class="button ghost" :disabled="saving" @click="close(false)">取消</button>
        <button type="submit" class="button primary" :disabled="saving || !row">{{ saving ? '保存中…' : mode === 'follow' ? '保存并跟随官网' : '保存手动价' }}</button>
      </div>
    </form>
  </AppDialog>
</template>

<style scoped>
.price-drawer { min-width: 0; font-size: 13px; }
.price-mode { display: flex; gap: 20px; flex-wrap: wrap; border: 0; padding: 0; margin: 0; }
.price-mode legend { margin-bottom: 8px; }
.price-mode label { display: inline-flex; align-items: center; gap: 7px; }
.price-mode input { width: 16px; height: 16px; margin: 0; accent-color: var(--primary); box-shadow: none; }
.price-inputs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.price-input { min-width: 0; }
.price-input input { font-variant-numeric: tabular-nums; }
.price-input input[aria-invalid="true"] { border-color: #b53b55; }
.optional, .price-hint { color: var(--ink-soft); font-weight: 400; }
.price-hint { margin: 7px 0 0; font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.applied-keys, .follow-hint { padding-top: 14px; border-top: 1px solid var(--line); }
.applied-keys h3 { font-size: 13px; margin: 0 0 8px; }
.applied-keys ul { display: flex; flex-wrap: wrap; gap: 8px 20px; margin: 0; padding-left: 17px; overflow-wrap: anywhere; }
.price-footer { display: flex; justify-content: flex-end; gap: 8px; padding-top: 10px; }
button:focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@media (max-width: 560px) {
  .price-inputs { grid-template-columns: minmax(0, 1fr); }
  .price-footer .button { flex: 1; }
}
</style>
