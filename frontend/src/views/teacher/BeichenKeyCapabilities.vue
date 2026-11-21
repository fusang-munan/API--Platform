<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenKeyCapability, BeichenKeyUsageMetric } from '../../api/types'
import { formatTokens } from '../../utils/credits'

const props = defineProps<{ providerId: string; capabilities: BeichenKeyCapability[]; disabled?: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
const pending = ref('')
const failure = ref<{ credential: string; message: string }>()
let disposed = false
const timestamp = new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', dateStyle: 'short', timeStyle: 'medium' })
const rateText = (value: string | number | null | undefined) => value == null ? '待核' : `${value}×`

// 数据口径：本卡片来自 /v1/usage 的账号级参考，不能当作单把 Key 的独立事实。
// 分组数据接口（M）就绪后，用按 Key 的分组事实替换本卡片的数据源与标题标记。

function usageUnit(unit?: string | null) {
  if (unit == null || unit === 'unknown') return '币种待核'
  return unit === 'mixed' ? '多币种' : unit
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
      expect_claude: form.has('claude'),
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
  <section class="beichen-card beichen-stack" aria-label="Key 实际能力（账号级参考）"
    data-scope="account-reference" data-replace-with="key-group-data">
    <h3>Key 实际能力 <span class="scope-tag">账号级参考</span></h3>
    <p class="beichen-note key-scope" data-testid="key-capability-scope">
      数据口径：本卡片中的能力与用量参考来自北辰 <code>/v1/usage</code> 的账号级数据，仅作账号级参考，不能单独当作单把 Key 的事实。
      分组数据接入后，这里会替换为按 Key 的分组事实。
    </p>
    <!-- 分组数据接入后：用 M 的分组数据接口替换本卡片（data-scope="key-group"），并移除标题中的“账号级参考”。 -->
    <article v-for="key in capabilities" :key="key.credential_id" class="key-evidence">
      <div class="beichen-section-head">
        <div>
          <strong>{{ key.label }}</strong>
          <small class="muted" :title="key.credential_id"> · 渠道编号 {{ key.credential_id.slice(0, 8) }}</small>
        </div>
        <span v-if="key.has_claude != null">{{ key.models.length }} 个模型 · {{ key.has_claude ? '含 Claude' : '无 Claude' }}</span>
        <span v-else class="muted">模型待核</span>
      </div>
      <p class="beichen-note">
        <template v-if="key.website_group">
          <strong>{{ key.website_group.name || '官网分组' }}</strong> · 分组 {{ key.website_group.id }}
          · {{ rateText(key.website_group.rate_multiplier) }}
          <template v-if="key.models_state === 'stale'"> · 上次核实</template>
        </template>
        <template v-else>官网分组待核</template>
        <template v-if="key.website_key_id"> · 北辰 Key {{ key.website_key_id }}</template>
        <template v-if="key.daily_usage"> · {{ key.daily_usage.date }} · {{ formatTokens(key.daily_usage.requests) }} 笔</template>
      </p>
      <p class="rate-line" :class="{ 'rate-warning': key.daily_usage?.multiplier_diff }">
        账单实付比值 {{ rateText(key.daily_usage?.observed_multiplier) }} · 已存计费倍率 {{ rateText(key.stored_multiplier) }} · 目标分组 {{ rateText(key.target_multiplier) }}
        <small v-if="key.daily_usage?.multiplier_diff"> · 观测与已存偏差超过 5%</small>
      </p>
      <p v-if="key.group_check === 'old_group'" class="rate-warning" role="alert">该 Key 仍按旧分组计费。请在北辰「API 密钥」核对这把 Key 绑定的分组，再回到这里重新核对。</p>
      <p v-else-if="key.group_check === 'mismatch'" class="rate-warning" role="status">官网分组倍率或模型清单与目标有差异。</p>
      <p v-else-if="key.group_check === 'target_observed'" class="beichen-note" role="status">官网分组符合设定的目标。</p>
      <p class="beichen-note">
        模型清单 {{ fetched(key.models_fetched_at) }}<strong v-if="key.models_state === 'stale'">（已陈旧）</strong>
        · 用量 {{ fetched(key.usage_fetched_at) }}<strong v-if="key.usage_state === 'stale'">（已陈旧）</strong>
        <template v-if="key.models_error"> · {{ key.models_error }}</template>
        <template v-if="key.usage_error"> · {{ key.usage_error }}</template>
      </p>
      <details>
        <summary>可用模型与用量明细</summary>
        <ul v-if="key.models.length" class="key-models">
          <li v-for="name in key.models" :key="name">{{ name }}</li>
        </ul>
        <p v-else class="beichen-note">{{ key.models_fetched_at ? '官网分组没有列出模型。' : '等待北辰返回分组模型清单。' }}</p>
        <p v-if="key.daily_usage" class="beichen-note">
          {{ tokenParts(key.daily_usage) }}<br />
          单日原价 {{ key.daily_usage.cost ?? '—' }} · 实付 {{ key.daily_usage.actual_cost ?? '—' }}（{{ usageUnit(key.day_unit) }}）
        </p>
        <template v-if="key.model_stats.length">
          <p class="beichen-note">{{ key.model_stats_day ? key.model_stats_day + ' 模型用量' : '模型用量的日期待核实' }} · 抓取于 {{ fetched(key.model_stats_fetched_at) }}。观测倍率为实付金额除以原价。</p>
          <ul class="model-observations">
            <li v-for="model in key.model_stats" :key="model.model">
              <strong>{{ model.model }}</strong> · {{ formatTokens(model.requests) }} 笔 · 原价 {{ model.cost ?? '—' }}
              · 实付 {{ model.actual_cost ?? '—' }}（{{ usageUnit(key.day_unit) }}）
              · 观测 {{ rateText(model.observed_multiplier) }}<strong v-if="model.multiplier_diff" class="rate-warning">（与已存偏差超过 5%）</strong>
              <br />{{ tokenParts(model) }}
            </li>
          </ul>
        </template>
      </details>
      <form class="actions key-check" @submit.prevent="refresh($event, key)">
        <label>目标分组倍率
          <input name="target" inputmode="decimal" maxlength="24" placeholder="如 0.6" :value="key.target_multiplier ?? ''" />
        </label>
        <label><input name="claude" type="checkbox" :checked="key.expect_claude" /> 目标分组应含 Claude</label>
        <button type="submit" class="button secondary" :disabled="disabled || !!pending">{{ pending === key.credential_id ? '读取中…' : '核对这个 Key' }}</button>
        <a class="button ghost" href="https://beichenapi.com/" target="_blank" rel="noopener noreferrer">打开北辰核对分组</a>
      </form>
      <p v-if="failure?.credential === key.credential_id" class="error" role="alert">{{ failure.message }}</p>
    </article>
  </section>
</template>

<style scoped>
.key-evidence { display: grid; gap: 8px; padding-top: 12px; border-top: 1px solid var(--line); overflow-wrap: anywhere; }
.key-evidence p, h3 { margin: 0; }
.scope-tag { margin-left: 6px; padding: 2px 7px; border-radius: 999px; background: #fff4dd; color: #9a6a17; font-size: 0.72rem; font-weight: 700; vertical-align: middle; white-space: nowrap; }
.key-scope code { padding: 0 3px; border-radius: 4px; background: rgba(23, 32, 51, 0.06); }
.key-evidence summary { cursor: pointer; }
.rate-line { font-weight: 600; }
.rate-warning { color: #9a3815; }
.key-models { display: flex; flex-wrap: wrap; column-gap: 26px; }
.key-models, .model-observations { margin: 8px 0; padding-inline-start: 22px; }
.key-check { flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.key-check label { display: inline-flex; align-items: center; gap: 5px; }
.key-check input[name=target] { width: 6.5em; }
.key-check input[type=checkbox] { width: auto; }
</style>
