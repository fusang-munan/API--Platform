<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef, useId, watch } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import { APIError, apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { BeichenReceiptPage, BeichenWebsiteSync } from '../../api/types'
import { schoolDay } from '../../utils/format'

const props = defineProps<{ providerId: string; websiteSync?: BeichenWebsiteSync | null }>()
const emit = defineEmits<{ refreshOverview: [] }>()
const panelId = useId()
const expanded = ref(false)
const day = ref(schoolDay())
const page = shallowRef<BeichenReceiptPage>()
const loading = ref(false)
const error = ref('')
let activeRequest: AbortController | undefined

const timestamp = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})

function beijingTime(value: string | null) {
  const date = value ? new Date(value) : null
  return date && Number.isFinite(date.getTime()) ? timestamp.format(date) : '未知'
}

function reported(value: string | null) {
  return typeof value === 'string' && value.trim() !== '' ? value : null
}

function refresh() {
  emit('refreshOverview')
  void load()
}

async function load(cursor: string | null = null) {
  if (cursor && loading.value) return
  activeRequest?.abort()
  error.value = ''
  if (!cursor) page.value = undefined
  if (!expanded.value || !day.value) {
    loading.value = false
    return
  }

  const controller = new AbortController()
  activeRequest = controller
  loading.value = true
  try {
    const result = await teacherAPI.beichenReceipts(props.providerId, day.value, cursor, controller.signal)
    if (controller.signal.aborted) return
    if (!Array.isArray(result?.items)) throw new Error('缺少逐笔记录列表')
    page.value = {
      items: cursor && page.value ? [...page.value.items, ...result.items] : result.items,
      next_cursor: result.next_cursor,
    }
  } catch (e) {
    if (controller.signal.aborted) return
    if (e instanceof APIError && (e.status === 401 || e.status === 403)) page.value = undefined
    error.value = apiErrorMessage(e, '读取逐笔对账失败，请重试。')
  } finally {
    if (!controller.signal.aborted) {
      loading.value = false
      activeRequest = undefined
    }
  }
}

watch([expanded, day, () => props.providerId], () => { void load() }, { flush: 'sync' })
onBeforeUnmount(() => activeRequest?.abort())
</script>

<template>
  <section class="receipt-section" aria-label="近期逐笔对账">
    <button type="button" class="button secondary receipt-toggle" :aria-expanded="expanded" :aria-controls="panelId"
      @click="expanded = !expanded">
      <ChevronRight :size="16" aria-hidden="true" />近期逐笔对账
    </button>
    <div :id="panelId">
      <div v-if="expanded" class="receipt-body">
        <div class="sync-status">
          <h3>官网同步近况（北京时间）</h3>
          <dl v-if="websiteSync" class="sync-facts">
            <div><dt>当前有有效网页报价</dt><dd>{{ websiteSync.priced_channels }} / {{ websiteSync.channels }} 个渠道</dd></div>
            <div><dt>成功读取过流水页</dt><dd>{{ websiteSync.receipt_checked_channels }} / {{ websiteSync.channels }} 个渠道</dd></div>
            <div><dt>最近一次扫描已到末页</dt><dd>{{ websiteSync.receipt_caught_up_channels }} / {{ websiteSync.channels }} 个渠道</dd></div>
            <div><dt>最近记录报价时间</dt><dd>{{ beijingTime(websiteSync.price_last_seen) }}</dd></div>
            <div><dt>最近一页流水读取成功时间</dt><dd>{{ beijingTime(websiteSync.receipt_last_checked) }}</dd></div>
          </dl>
          <p v-else class="muted">官网同步状态未知。</p>
        </div>

        <div class="receipt-tools">
          <label>对账日期（北京时间）<input v-model="day" type="date" required></label>
          <button type="button" class="button secondary" :disabled="!day" @click="refresh">刷新对账</button>
        </div>
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <p v-if="loading" class="muted" role="status">正在读取逐笔流水…</p>
        <p v-else-if="!day" class="muted">请选择对账日期。</p>
        <p v-else-if="page && !page.items.length && !page.next_cursor && !error" class="muted">学校尚未归档所选日期的北辰流水</p>
        <div v-if="page?.items.length" class="receipt-scroll">
          <table class="usage-table receipt-table" aria-label="北辰逐笔流水">
            <thead><tr>
              <th scope="col">发生时间（北京时间）</th><th scope="col">模型与学生</th><th scope="col">学校记账</th>
              <th scope="col">北辰实扣（余额单位）</th><th scope="col">差额（积分）</th><th scope="col">匹配状态</th>
            </tr></thead>
            <tbody>
              <tr v-for="(row, index) in page.items" :key="index">
                <td><time :datetime="row.occurred_at" :title="row.occurred_at">{{ beijingTime(row.occurred_at) }}</time></td>
                <th scope="row">{{ row.model }}<small>学生 {{ row.student_name?.trim() || '待核' }}</small></th>
                <td>
                  <span>已扣积分 {{ reported(row.charged_credits) ?? '待核' }}</span>
                  <small>基础积分 {{ reported(row.base_credits) ?? '待核' }}</small>
                  <small>学校倍率 {{ reported(row.school_multiplier) ?? '待核' }}</small>
                </td>
                <td>{{ reported(row.actual_cost) ?? '待核' }}</td>
                <td>{{ reported(row.difference_credits) ?? '待核' }}</td>
                <td>{{ row.matched && reported(row.actual_cost) !== null ? '已匹配' : '待核' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <button v-if="page?.next_cursor" type="button" class="button ghost" :disabled="loading"
          @click="load(page.next_cursor)">加载更多</button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.receipt-section { margin: .75rem 0 1rem; }
.receipt-toggle { display: inline-flex; align-items: center; gap: .4rem; }
.receipt-toggle[aria-expanded="true"] svg { transform: rotate(90deg); }
.receipt-body { display: grid; gap: .75rem; min-width: 0; margin-top: .75rem; }
.receipt-body p, .sync-status h3 { margin: 0; }
.sync-status h3 { font-size: .95rem; margin-bottom: .5rem; }
.sync-facts { grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); margin: 0; }
.sync-facts dd { font-variant-numeric: tabular-nums; }
.receipt-tools { display: flex; align-items: end; flex-wrap: wrap; gap: .75rem; }
.receipt-tools label { margin: 0; }
.receipt-scroll { overflow-x: auto; }
.receipt-table { font-variant-numeric: tabular-nums; }
.receipt-table th[scope="row"] { overflow-wrap: anywhere; white-space: normal; }
.receipt-table small { display: block; color: var(--ink-soft); font-weight: 400; }
.receipt-body > button { justify-self: start; }
</style>
