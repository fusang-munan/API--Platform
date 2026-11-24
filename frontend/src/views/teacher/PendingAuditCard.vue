<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef } from 'vue'
import { APIError, apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { PendingAuditReport } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'

const props = defineProps<{ providerId: string }>()
const emit = defineEmits<{ settled: [] }>()
const preview = shallowRef<PendingAuditReport>()
const result = shallowRef<PendingAuditReport>()
const busy = ref(false)
const offset = ref(0)
const confirming = ref(false)
const confirmingTerminal = ref(false)
const terminalResult = ref('')
const error = ref('')
let active = true
onBeforeUnmount(() => { active = false })

async function run(apply: boolean, requestedOffset = 0, terminal = false) {
  if (busy.value || (apply && (!confirming.value || !preview.value?.auto_settle))) return
  if (terminal && (!confirmingTerminal.value || !preview.value?.terminal_enabled)) return
  const token = preview.value?.preview_token
  busy.value = true
  confirming.value = false
  confirmingTerminal.value = false
  terminalResult.value = ''
  preview.value = undefined
  error.value = ''
  result.value = undefined
  try {
    if (terminal) {
      const booked = await teacherAPI.resolveTerminalPending(props.providerId)
      if (!active) return
      terminalResult.value = `本次已结清缺终态 ${booked.resolved} 笔，出错 ${booked.errors} 笔。`
      emit('settled')
    }
    if (apply && token) {
      const booked = await teacherAPI.pendingAudit(props.providerId, { apply: true, limit: 200, offset: offset.value, preview_token: token })
      if (!active) return
      result.value = booked
      emit('settled')
    }
    offset.value = requestedOffset
    const next = await teacherAPI.pendingAudit(props.providerId, { apply: false, limit: 200, offset: requestedOffset })
    if (active) preview.value = next
  } catch (cause) {
    if (active) error.value = cause instanceof APIError && cause.status === 409 && cause.code === 'pending_audit_changed'
      ? '待核集合已变化，请重新回放' : apiErrorMessage(cause)
  } finally {
    if (active) busy.value = false
  }
}

const labels = { exact: '金额唯一', narrow: '误差较小', wide: '误差较大', unknown: '证据不足' }
</script>

<template>
  <section class="beichen-card beichen-stack" aria-label="存量待核回放">
    <h3>存量待核回放</h3>
    <div class="beichen-toolbar-group">
      <button type="button" class="button secondary" :disabled="busy" @click="run(false)">只读回放</button>
      <button v-if="preview?.next_offset != null" type="button" class="button secondary" :disabled="busy" @click="run(false, preview.next_offset)">查看下一批</button>
      <AppDialog v-if="preview" v-model:open="confirming" title="确认批量结算" description="请核对待结算笔数与扣款说明。">
        <template #trigger>
          <button type="button" class="button" :disabled="!preview.auto_settle || busy">批量结算可自动项</button>
        </template>
        <p>将对 {{ preview.auto_settle }} 笔调用真实扣款，清零已结算调用的待核预留。确认继续？</p>
        <button type="button" class="button secondary" @click="confirming = false">取消</button>
        <button type="button" class="button" @click="run(true)">确认扣款</button>
      </AppDialog>
      <span v-if="busy" role="status">正在处理…</span>
      <AppDialog v-if="preview?.terminal_enabled && preview.terminal_missing" v-model:open="confirmingTerminal" title="确认处理缺终态" description="仅处理本供应商已过宽限期的一批调用。">
        <template #trigger>
          <button type="button" class="button secondary" :disabled="busy">立即处理缺终态</button>
        </template>
        <p>按已知用量扣款。没有可计费用量或无法定价的调用按 0 积分核销。</p>
        <button type="button" class="button secondary" @click="confirmingTerminal = false">取消</button>
        <button type="button" class="button" @click="run(false, 0, true)">确认处理</button>
      </AppDialog>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="terminalResult" role="status">{{ terminalResult }}</p>
    <p v-if="result" role="status">已结算 {{ result.charged }} 笔，仍待核 {{ result.still_pending }} 笔，跳过 {{ result.skipped }} 笔，出错 {{ result.errors }} 笔。本次实扣 {{ result.credits_charged }} 积分，供应商剩余待核 {{ result.remaining_pending }} 笔。</p>
    <template v-if="preview">
      <p>供应商待核共 {{ preview.backlog_total }} 笔，本批 {{ preview.total }} 笔<span v-if="preview.total">（第 {{ offset + 1 }} 至 {{ offset + preview.total }} 笔）</span>，可自动结算 {{ preview.auto_settle }} 笔。</p>
      <p v-if="preview.auto_settle === 0 && preview.next_offset != null">本批没有可自动结算的调用，请继续查看下一批。</p>
      <p>其中 {{ preview.terminal_missing }} 笔缺终态，{{ preview.terminal_enabled ? '超过宽限期按已知用量结清' : '缺终态处理已关闭' }}。</p>
      <p>已知区间最大误差 {{ preview.max_error_yuan }} 元，可自动项预计上游金额 {{ preview.estimated_auto_yuan }} 元。</p>
      <p>金额唯一 {{ preview.exact }} · 误差较小 {{ preview.narrow }} · 误差较大 {{ preview.wide }} · 证据不足 {{ preview.unknown }}</p>
      <div v-if="preview.rows.length" class="audit-table">
        <table class="beichen-table">
          <caption>待核样本（最多展示 20 笔）</caption>
          <thead><tr><th>调用</th><th>学生 / 模型</th><th>分类</th><th>上游区间（元）</th><th>推荐（元）</th><th>预计扣分</th><th>原因与说明</th></tr></thead>
          <tbody><tr v-for="row in preview.rows.slice(0, 20)" :key="row.id">
            <td>{{ row.id }}</td><td>{{ row.student }} · {{ row.model }} · {{ row.protocol }}</td><td>{{ labels[row.class] }}</td>
            <td>{{ row.class === 'unknown' ? '待核' : `${row.min} 至 ${row.max}` }}</td><td>{{ row.class === 'unknown' ? '待核' : row.recommended }}</td>
            <td>{{ row.student_credits ?? '待核' }}</td><td>{{ row.reason }} · {{ row.note }}</td>
          </tr></tbody>
        </table>
      </div>
    </template>
  </section>
</template>

<style scoped>
h3, p { margin: 0; }
.audit-table { max-width: 100%; overflow-x: auto; }
.audit-table td { overflow-wrap: anywhere; }
caption { text-align: left; padding-block: 8px; }
</style>
