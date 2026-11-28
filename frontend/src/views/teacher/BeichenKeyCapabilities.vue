<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenKeyCapability, BeichenKeyUsageMetric } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import { keyGroupIssue } from '../../utils/beichenModels'
import { formatPriceDetail, formatTokens } from '../../utils/credits'

const props = defineProps<{ providerId: string; capabilities: BeichenKeyCapability[]; disabled?: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
const reviewKey = ref('')
const pending = ref('')
const failure = ref<{ credential: string; message: string }>()
let disposed = false
const timestamp = new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', dateStyle: 'short', timeStyle: 'medium' })
const rateText = (value: string | number | null | undefined) => value == null ? '待核' : `${value}×`

function groupIssue(key: BeichenKeyCapability) {
  return keyGroupIssue({ group_status: key.group_status, group_id: key.website_group?.id })
    || (key.website_group ? '' : '分组待同步')
}

function dayAmount(value?: string | null) {
  const amount = formatPriceDetail(value)
  return amount === '—' ? amount : amount + ' 元'
}

function originalCurrency(unit?: string | null) {
  return unit?.trim() || 'unknown'
}

function tokenParts(usage: BeichenKeyUsageMetric) {
  return [
    ['输入', usage.input_tokens], ['输出', usage.output_tokens],
    ['缓存读取', usage.cache_read_tokens], ['缓存写入', usage.cache_write_tokens],
  ].map(([label, value]) => label + ' ' + formatTokens(value)).join(' · ')
}

function fetched(at: string | null) {
  return at ? timestamp.format(new Date(at)) : '待首次读取'
}

async function refresh(event: Event, key: BeichenKeyCapability) {
  if (pending.value || props.disabled || !(event.currentTarget instanceof HTMLFormElement)) return
  const form = new FormData(event.currentTarget)
  pending.value = key.credential_id
  failure.value = undefined
  try {
    await teacherAPI.refreshBeichenKeyUsage(props.providerId, {
      credential_id: key.credential_id,
      target_multiplier: String(form.get('target') ?? '').trim(),
      expect_claude: false,
    })
  } catch (e) {
    if (!disposed) failure.value = { credential: key.credential_id, message: apiErrorMessage(e, '读取数据失败，请稍后重试') }
  } finally {
    if (!disposed) {
      pending.value = ''
      emit('refresh')
    }
  }
}

onBeforeUnmount(() => { disposed = true })
</script>

<template>
  <section class="beichen-card beichen-stack" aria-label="Key 实际能力">
    <h3>Key 实际能力</h3>
    <article v-for="key in capabilities" :key="key.credential_id" class="key-evidence">
      <header class="beichen-section-head key-evidence-head">
        <div class="key-identity">
          <h4>{{ key.label }}</h4>
          <small :title="key.credential_id">渠道编号 {{ key.credential_id.slice(0, 8) }}</small>
        </div>
        <span v-if="key.models_fetched_at && !groupIssue(key)" class="key-model-count">Key 清单 {{ formatTokens(key.models.length) }} 个</span>
        <span v-else class="beichen-note">Key 清单待核</span>
      </header>
      <p class="beichen-note">
        <template v-if="key.website_group">
          <strong>{{ key.website_group.name || '官网分组' }}</strong> · 分组 {{ key.website_group.id }}
          · {{ rateText(key.website_group.rate_multiplier) }}
          <template v-if="groupIssue(key)"> · </template>
        </template>
        <span v-if="groupIssue(key)" role="status">{{ groupIssue(key) }}</span>
        <template v-if="key.website_key_id"> · 北辰 Key {{ key.website_key_id }}</template>
      </p>
      <p class="rate-line" :class="{ 'rate-warning': key.daily_usage?.multiplier_diff }">
        账单实付比值 {{ rateText(key.daily_usage?.observed_multiplier) }} · 已存计费倍率 {{ rateText(key.stored_multiplier) }}
        <small v-if="key.daily_usage?.multiplier_diff"> · 观测与已存偏差超过 5%</small>
      </p>
      <p class="beichen-note">
        模型清单 {{ fetched(key.models_fetched_at) }}<strong v-if="key.models_state === 'stale'">（已陈旧）</strong>
        · 用量 {{ fetched(key.usage_fetched_at) }}<strong v-if="key.usage_state === 'stale'">（已陈旧）</strong>
        <template v-if="key.models_error && key.models_error !== groupIssue(key)"> · {{ key.models_error }}</template>
        <template v-if="key.usage_error && key.usage_error !== key.models_error && key.usage_error !== groupIssue(key)"> · {{ key.usage_error }}</template>
      </p>
      <details class="key-evidence-details">
        <summary>Key 清单与账日用量</summary>
        <ul v-if="key.models.length && !groupIssue(key)" class="key-models" aria-label="Key 清单">
          <li v-for="name in key.models" :key="name">{{ name }}</li>
        </ul>
        <p v-else-if="!groupIssue(key)" class="beichen-note">{{ key.models_fetched_at ? '官网分组没有列出模型。' : '等待北辰返回分组模型清单。' }}</p>
        <section v-if="key.daily_usage || key.model_stats.length || key.model_stats_fetched_at" class="key-day-usage" aria-label="账日用量">
          <div v-if="key.daily_usage" class="key-day-total">
            <strong>{{ key.daily_usage.date }} · {{ formatTokens(key.daily_usage.requests) }} 笔</strong>
            <p>原价 {{ dayAmount(key.daily_usage.cost) }} · 实付 {{ dayAmount(key.daily_usage.actual_cost) }}</p>
            <p class="beichen-note">{{ tokenParts(key.daily_usage) }}</p>
          </div>
          <small class="beichen-note">原币 {{ originalCurrency(key.day_unit) }}</small>
          <div v-if="key.model_stats.length || key.model_stats_fetched_at" class="key-usage-models">
            <h5>账日有记录 {{ formatTokens(key.model_stats.length) }} 个</h5>
            <p class="beichen-note">
              {{ key.model_stats_day ?? '模型用量的日期待核实' }} · 抓取于 {{ fetched(key.model_stats_fetched_at) }}
              <template v-if="key.model_stats.length"> · 观测倍率为实付金额除以原价</template>
            </p>
            <div v-if="key.model_stats.length" class="model-observations">
              <table>
                <caption class="sr-only">账日模型用量</caption>
                <thead>
                  <tr><th scope="col">模型</th><th scope="col">笔数</th><th scope="col">原价</th><th scope="col">实付</th><th scope="col">观测倍率</th></tr>
                </thead>
                <tbody>
                  <tr v-for="model in key.model_stats" :key="model.model">
                    <th scope="row">{{ model.model }}<small class="beichen-note">{{ tokenParts(model) }}</small></th>
                    <td>{{ formatTokens(model.requests) }}</td>
                    <td>{{ dayAmount(model.cost) }}</td>
                    <td>{{ dayAmount(model.actual_cost) }}</td>
                    <td>{{ rateText(model.observed_multiplier) }}<small v-if="model.multiplier_diff" class="rate-warning">与已存偏差超过 5%</small></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </section>
      </details>
      <div class="actions key-check">
        <AppDialog :open="reviewKey === key.credential_id" :title="'核对这个 Key · ' + key.label"
          description="读取这把 Key 的北辰分组、模型和近期用量。可填写要核对的倍率。"
          @update:open="open => reviewKey = open ? key.credential_id : ''">
          <template #trigger>
            <button type="button" class="button secondary" :disabled="disabled || !!pending">{{ pending === key.credential_id ? '读取中…' : '核对这个 Key' }}</button>
          </template>
          <form class="key-review" @submit.prevent="refresh($event, key)">
            <label>目标分组倍率
              <input name="target" inputmode="decimal" maxlength="24" placeholder="如 0.6" :value="key.target_multiplier ?? ''" :disabled="disabled || !!pending" />
            </label>
            <template v-if="!key.expect_claude && !groupIssue(key)">
              <p v-if="key.group_check === 'old_group'" class="rate-warning" role="status">当前倍率仍与已存计费倍率一致。请在北辰核对 Key 绑定的分组。</p>
              <p v-else-if="key.group_check === 'mismatch'" class="rate-warning" role="status">官网分组倍率与目标有差异。</p>
              <p v-else-if="key.group_check === 'target_observed'" class="beichen-note" role="status">官网分组倍率符合设定的目标。</p>
            </template>
            <p v-if="failure?.credential === key.credential_id" class="error" role="alert">{{ failure.message }}</p>
            <button type="submit" class="button primary" :disabled="disabled || !!pending">{{ pending === key.credential_id ? '读取中…' : '开始核对' }}</button>
          </form>
        </AppDialog>
        <a class="button ghost" href="https://beichenapi.com/" target="_blank" rel="noopener noreferrer">打开北辰核对分组</a>
      </div>
      <p v-if="reviewKey !== key.credential_id && failure?.credential === key.credential_id" class="error" role="alert">{{ failure.message }}</p>
    </article>
  </section>
</template>

<style scoped>
.key-evidence { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; width: 100%; max-width: 100%; min-width: 0; padding: 14px; border: 1px solid var(--line); border-radius: 12px; overflow-wrap: anywhere; }
.key-evidence p, h3, .key-evidence h4, .key-evidence h5 { margin: 0; }
.key-evidence-head { align-items: center; }
.key-identity { min-width: 0; display: grid; gap: 3px; }
.key-identity h4 { font-size: 15px; }
.key-identity small { color: var(--ink-soft); font-size: 11px; }
.key-model-count { background: var(--primary-soft); color: var(--primary-strong); border-radius: 8px; padding: 4px 8px; font-size: 12px; font-weight: 650; }
.rate-line { font-weight: 600; }
.rate-warning { color: #9a3815; }
.key-evidence-details { border-top: 1px solid var(--line); padding-top: 10px; min-width: 0; }
.key-evidence-details summary { cursor: pointer; font-size: 13px; font-weight: 700; }
.key-evidence-details[open] summary { margin-bottom: 10px; }
.key-evidence-details:not([open]) > :not(summary) { display: none; }
.key-models { display: flex; flex-wrap: wrap; gap: 5px 26px; margin: 0; padding: 0 0 0 18px; }
.key-day-usage { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; width: 100%; max-width: 100%; margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); min-width: 0; }
.key-day-total, .key-usage-models { display: grid; grid-template-columns: minmax(0, 1fr); gap: 5px; width: 100%; max-width: 100%; min-width: 0; }
.key-day-total, .key-usage-models h5 { font-size: 13px; }
.key-day-total p { font-variant-numeric: tabular-nums; }
.model-observations { width: 100%; max-width: 100%; min-width: 0; overflow-x: auto; }
.model-observations table { width: 100%; min-width: 30rem; border-collapse: collapse; font-size: 12px; }
.model-observations th, .model-observations td { padding: 8px; border-bottom: 1px solid var(--line); vertical-align: top; text-align: right; }
.model-observations :is(th, td):first-child { text-align: left; padding-inline-start: 0; }
.model-observations thead th { color: var(--ink-soft); font-size: 11px; }
.model-observations td { white-space: nowrap; font-variant-numeric: tabular-nums; }
.model-observations small { display: block; margin-top: 3px; font-weight: 400; white-space: normal; }
.key-check { justify-content: flex-start; margin-top: 2px; gap: 8px 12px; }
.key-review { display: grid; gap: 12px; justify-items: start; }
.key-review label { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.key-review input[name=target] { width: 6.5em; }
.key-review p { margin: 0; }
</style>
