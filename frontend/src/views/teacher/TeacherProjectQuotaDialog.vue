<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { APIError, patch } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { Project, ProjectUsage } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import CreditDonut, { type CreditSegment } from '../../components/CreditDonut.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useToast } from '../../composables/useToast'
import { formatCredits, formatTokens } from '../../utils/credits'
import { calculateCredits, quotaBalance, schoolMonth } from '../../utils/format'

const props = defineProps<{ project: Project; initialMonth: string }>()
const emit = defineEmits<{ close: []; saved: [] }>()

type Member = ProjectUsage['members'][number]
type Draft = {
  value: string
  topUp: boolean
  state?: 'saving' | 'saved' | 'failed'
  error?: string
}

const quotaMonth = ref(props.initialMonth)
const usage = ref<ProjectUsage | null>(null)
const drafts = ref<Record<string, Draft>>({})
const batchAmount = ref('')
const batchError = ref('')
const loadError = ref('')
const loading = ref(false)
const saving = ref(false)
const toast = useToast()
const fieldId = useId()
let request = 0

const editable = computed(() => props.project.status === 'active'
  && quotaMonth.value === schoolMonth() && usage.value?.month === quotaMonth.value)
const busy = computed(() => loading.value || saving.value)

const rows = computed(() => {
  const current = usage.value
  if (!current) return []
  return current.members.map(member => {
    const draft = drafts.value[member.user_id]
    const minimum = calculateCredits(member.credits, member.pending ?? '0', 'add')
    const value = draft && draft.state !== 'saved' ? draft.value : member.quota
    const target = draft ? parseQuota(value) : null
    const dirty = Boolean(draft && draft.state !== 'saved'
      && (target === null || greater(target, member.quota) || greater(member.quota, target)))
    return {
      member, draft, value, target, minimum, dirty,
      validation: dirty ? quotaError(target, member, minimum, current.available, draft?.topUp) : '',
      pending: pendingDisplay(member),
      balance: quotaBalance(member.quota, member.credits, member.available),
    }
  })
})
type QuotaRow = (typeof rows.value)[number]

const showBatch = computed(() => editable.value && rows.value.some(row => row.member.is_student))
const changedRows = computed(() => rows.value.filter(row => row.member.is_student && row.dirty))
const canSaveBatch = computed(() => changedRows.value.some(row => !row.validation))
const projectPending = computed(() => usage.value ? pendingDisplay(usage.value) : null)
const allocation = computed<CreditSegment[]>(() => {
  if (!usage.value) return []
  return [
    ...usage.value.members.filter(member => member.is_student && greater(member.quota, '0'))
      .map(member => ({ name: member.user_name, value: chartMilli(member.quota), displayValue: formatCredits(member.quota) })),
    { name: '可再分配', value: chartMilli(usage.value.available), color: '#dfe3ec', displayValue: formatCredits(usage.value.available) },
  ]
})

function greater(left: string, right: string) {
  return calculateCredits(left, right, 'subtract') !== '0.000'
}

function chartMilli(value: string) {
  const [whole, fraction = ''] = value.split('.')
  return Number(whole + fraction.padEnd(3, '0'))
}

function pendingDisplay(item: { pending?: string | null; pending_review?: boolean }) {
  const credits = formatCredits(item.pending)
  const tokens = 'tokens_missing' in item ? item.tokens_missing : null
  return {
    credits: item.pending_review && credits === '0' ? '—' : credits,
    tokens: typeof tokens === 'number' && Number.isSafeInteger(tokens) && tokens >= 0 ? formatTokens(tokens) : '—',
  }
}

function parseQuota(value: string) {
  const trimmed = value.trim()
  return /^\d+(?:\.\d{1,3})?$/.test(trimmed) ? trimmed.replace(/^0+(?=\d)/, '') : null
}

function quotaError(target: string | null, member: Member, minimum: string, available: string, topUp = false) {
  if (target === null) return '请输入非负积分，最多三位小数。'
  if (topUp && greater(member.quota, target)) return '当前额度已提高，请重新点击「补足已用」。'
  if (greater(minimum, target)) {
    return `新额度不能低于已用与待核预留之和，至少需 ${formatCredits(roundUpQuota(minimum))} 积分。`
  }
  if (greater(calculateCredits(target, member.quota, 'subtract'), available)) {
    return `增加的额度超过项目可再分配的 ${formatCredits(available)} 积分。`
  }
  return ''
}

// 月额度最多三位小数，向上取整才能覆盖精度更高的已用积分。
function roundUpQuota(value: string) {
  const [whole, fraction = ''] = value.split('.')
  const milli = BigInt(whole + fraction.slice(0, 3).padEnd(3, '0')) + (/[1-9]/.test(fraction.slice(3)) ? 1n : 0n)
  const digits = milli.toString().padStart(4, '0')
  return `${digits.slice(0, -3)}.${digits.slice(-3)}`
}

async function loadUsage(reset = false) {
  const currentRequest = ++request
  const projectID = props.project.id
  const selectedMonth = quotaMonth.value
  loadError.value = ''
  if (reset) {
    usage.value = null
    drafts.value = {}
    batchAmount.value = ''
    batchError.value = ''
  }
  if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(selectedMonth)) {
    loading.value = false
    loadError.value = '请选择有效月份。'
    return
  }
  loading.value = true
  try {
    const result = await teacherAPI.projectUsage(projectID, selectedMonth)
    if (currentRequest === request) usage.value = result
  } catch (error) {
    if (currentRequest === request) loadError.value = '读取学生额度失败。' + message(error)
  } finally {
    if (currentRequest === request) loading.value = false
  }
}

function editQuota(member: Member, value: string, topUp = false) {
  if (editable.value && !busy.value && member.is_student) {
    drafts.value[member.user_id] = { value, topUp }
  }
}

function fillBatch() {
  if (!showBatch.value || busy.value) return
  const target = parseQuota(batchAmount.value)
  batchError.value = target === null ? '请输入非负积分，最多三位小数。' : ''
  if (target === null) return
  for (const row of rows.value) editQuota(row.member, target)
}

function previewTopUp(row: QuotaRow) {
  const { member, minimum } = row
  editQuota(member, greater(minimum, member.quota) ? roundUpQuota(minimum) : member.quota, true)
}

function saveBatch() {
  const decreases: QuotaRow[] = []
  const remaining: QuotaRow[] = []
  for (const row of changedRows.value) {
    if (row.target !== null && greater(row.member.quota, row.target)) decreases.push(row)
    else remaining.push(row)
  }
  return saveQuotas([...decreases, ...remaining])
}

async function saveQuotas(queue: QuotaRow[]) {
  const current = usage.value
  if (!current || !editable.value || busy.value) return
  const revision = request
  const projectID = props.project.id
  let saved = 0
  let refresh = false
  saving.value = true
  try {
    for (const { member, draft, target, minimum, dirty } of queue) {
      if (request !== revision || projectID !== props.project.id || !editable.value || current.month !== schoolMonth()) break
      if (!member.is_student || !draft || !dirty || target === null
        || quotaError(target, member, minimum, current.available, draft.topUp)) continue
      draft.state = 'saving'
      draft.error = ''
      try {
        if (draft.topUp) {
          await patch<void>(
            `/api/v1/teacher/projects/${encodeURIComponent(projectID)}/members/${encodeURIComponent(member.user_id)}/monthly-credit-quota`,
            { monthly_credits: target, increase_only: true },
          )
        } else {
          await teacherAPI.updateMemberQuota(projectID, member.user_id, target)
        }
      } catch (error) {
        draft.state = 'failed'
        const outdated = draft.topUp && error instanceof APIError && error.code === 'INVALID_STATE_TRANSITION'
        draft.error = outdated ? '当前额度已提高，请重新点击「补足已用」。' : message(error)
        if (error instanceof APIError && (error.status === 409 || error.code === 'VALIDATION_ERROR')) refresh = true
        continue
      }
      if (request !== revision) return
      draft.state = 'saved'
      saved += 1
      // 下一行按已经保存的结果校验项目余额，整批结束后再读取服务器快照。
      const otherQuota = calculateCredits(current.allocated, member.quota, 'subtract')
      current.allocated = calculateCredits(otherQuota, target, 'add')
      current.available = calculateCredits(current.quota, current.allocated, 'subtract')
      member.quota = target
      member.available = calculateCredits(target, minimum, 'subtract')
    }
    if (request !== revision) return
    if (saved > 0) {
      toast.success(saved === 1 ? '学生月额度已保存' : `已保存 ${saved} 位学生的月额度`)
      emit('saved')
    }
    if (saved > 0 || refresh) await loadUsage()
  } finally {
    saving.value = false
  }
}

function message(error: unknown) {
  return error instanceof APIError ? error.message : '请求失败，请稍后重试。'
}

function closeDialog() {
  if (!saving.value) emit('close')
}

watch([() => props.project.id, quotaMonth], () => loadUsage(true), { immediate: true })
onBeforeUnmount(() => { request += 1 })
</script>

<template>
  <AppDialog
    :open="true"
    :title="`${project.name} · 学生额度`"
    description="查看项目积分分配和成员用量，在本月调整学生额度。"
    size="xwide"
    @update:open="!$event && closeDialog()"
  >
    <div class="quota-dialog">
      <div class="quota-toolbar">
        <label :for="`${fieldId}-month`">月份
          <input :id="`${fieldId}-month`" v-model="quotaMonth" type="month" :max="schoolMonth()" :disabled="saving">
        </label>
        <span class="muted">额度与用量以积分计</span>
      </div>
      <div class="quota-scroll" role="region" aria-label="项目额度和成员用量" tabindex="0">
        <p v-if="quotaMonth !== schoolMonth()" class="quota-notice" role="status">{{ quotaMonth < schoolMonth() ? '历史月份只读，不能调整额度。' : '仅可调整本月额度。' }}</p>
        <p v-else-if="project.status !== 'active'" class="quota-notice" role="status">项目已停用，额度仅供查看。</p>
        <div v-if="loadError" class="quota-load-error" role="alert">
          <p class="error">{{ loadError }}</p>
          <button type="button" class="button secondary" :disabled="busy" @click="loadUsage()">重新加载</button>
        </div>
        <LoadingRegion
          :initial-loading="loading && !usage"
          :refreshing="loading && Boolean(usage)"
          label="正在加载额度分配"
          variant="cards"
        >
          <div v-if="usage" class="quota-layout">
            <section class="quota-overview" :aria-labelledby="`${fieldId}-overview`">
              <h2 :id="`${fieldId}-overview`">项目额度总览</h2>
              <dl class="quota-summary" aria-label="项目额度汇总">
                <div><dt>项目月额度</dt><dd>{{ formatCredits(usage.quota) }}</dd></div>
                <div><dt>已分配</dt><dd>{{ formatCredits(usage.allocated) }}</dd></div>
                <div class="quota-available"><dt>可再分配</dt><dd>{{ formatCredits(usage.available) }}</dd></div>
                <div><dt>本月已消耗</dt><dd>{{ formatCredits(usage.charged) }}</dd></div>
                <div>
                  <dt>待核</dt>
                  <dd class="quota-pending" :class="{ 'needs-review': usage.pending_review }">
                    <span>积分 {{ projectPending?.credits }}</span>
                    <span>Token {{ projectPending?.tokens }}</span>
                    <small v-if="usage.pending_review">有调用待核</small>
                  </dd>
                </div>
              </dl>
              <div class="quota-allocation">
                <h3>成员分配占比</h3>
                <CreditDonut
                  v-if="allocation.length > 1"
                  :segments="allocation"
                  :total="chartMilli(usage.quota)"
                  label="项目额度中已分配与可再分配的积分"
                  unit="积分"
                />
                <p v-else class="muted quota-empty">尚未分配</p>
              </div>
            </section>
            <section class="quota-members" :aria-labelledby="`${fieldId}-members`">
              <div class="quota-members-heading">
                <h2 :id="`${fieldId}-members`">成员用量</h2>
                <span class="muted">{{ rows.length }} 位成员</span>
              </div>
              <form v-if="showBatch" class="quota-batch" @submit.prevent="fillBatch">
                <label :for="`${fieldId}-batch`">批量新额度</label>
                <input
                  :id="`${fieldId}-batch`"
                  v-model="batchAmount"
                  inputmode="decimal"
                  :disabled="busy"
                  :aria-invalid="Boolean(batchError)"
                  :aria-describedby="batchError ? `${fieldId}-batch-error` : undefined"
                  @input="batchError = ''"
                >
                <button type="submit" class="button secondary" :disabled="busy">批量填入</button>
                <p v-if="batchError" :id="`${fieldId}-batch-error`" class="quota-feedback error" role="alert">{{ batchError }}</p>
              </form>
              <table v-if="rows.length" class="quota-table" role="table" :aria-labelledby="`${fieldId}-members`">
                <thead role="rowgroup">
                  <tr role="row">
                    <th role="columnheader" scope="col">成员</th>
                    <th role="columnheader" scope="col">已分配</th>
                    <th role="columnheader" scope="col">已用</th>
                    <th role="columnheader" scope="col">待核</th>
                    <th role="columnheader" scope="col">剩余</th>
                    <th role="columnheader" scope="col">本月新额度</th>
                  </tr>
                </thead>
                <tbody role="rowgroup">
                  <tr v-for="row in rows" :key="row.member.user_id" class="quota-member" role="row" :data-member-id="row.member.user_id">
                    <th scope="row" role="rowheader" class="quota-name">
                      {{ row.member.user_name }}
                      <small v-if="row.member.is_student">学生</small>
                    </th>
                    <td role="cell"><span class="quota-cell-label" aria-hidden="true">已分配</span>{{ formatCredits(row.member.quota) }}</td>
                    <td role="cell"><span class="quota-cell-label" aria-hidden="true">已用</span>{{ formatCredits(row.member.credits) }}</td>
                    <td role="cell">
                      <span class="quota-cell-label" aria-hidden="true">待核</span>
                      <div class="quota-pending" :class="{ 'needs-review': row.member.pending_review }">
                        <span>积分 {{ row.pending.credits }}</span>
                        <span>Token {{ row.pending.tokens }}</span>
                        <small v-if="row.member.pending_review">有调用待核</small>
                      </div>
                    </td>
                    <td role="cell">
                      <span class="quota-cell-label" aria-hidden="true">剩余</span>{{ formatCredits(row.member.available) }}
                      <small v-if="row.balance.label === '已超用'" class="quota-overused">已超用 {{ formatCredits(row.balance.amount) }}</small>
                    </td>
                    <td role="cell" class="quota-entry">
                      <span class="quota-cell-label" aria-hidden="true">本月新额度</span>
                      <form v-if="editable && row.member.is_student" class="quota-row-form" @submit.prevent="saveQuotas([row])">
                        <div class="quota-row-actions">
                          <input
                            :value="row.value"
                            inputmode="decimal"
                            :aria-label="`${row.member.user_name}本月新额度`"
                            :aria-invalid="Boolean(row.validation)"
                            :aria-describedby="row.validation || row.draft?.error ? `${fieldId}-${row.member.user_id}-feedback` : undefined"
                            :disabled="busy"
                            @input="editQuota(row.member, ($event.target as HTMLInputElement).value)"
                          >
                          <button type="submit" class="button secondary quota-save-row" :disabled="busy || !row.dirty || Boolean(row.validation)">
                            {{ row.draft?.state === 'failed' ? '重试' : '保存' }}
                          </button>
                        </div>
                        <button type="button" class="button ghost quota-top-up" :disabled="busy" @click="previewTopUp(row)">补足已用</button>
                        <p v-if="row.draft?.topUp && row.draft.state !== 'saved' && !greater(row.member.quota, row.value)" class="quota-preview">
                          补足预览 <strong>{{ formatCredits(row.member.quota) }} → {{ formatCredits(row.value) }} 积分</strong>
                          <small v-if="!row.dirty">无需补足</small>
                        </p>
                        <div v-if="row.validation || row.draft?.error" :id="`${fieldId}-${row.member.user_id}-feedback`">
                          <p v-if="row.validation" class="quota-feedback error" role="alert">{{ row.validation }}</p>
                          <p v-if="row.draft?.error && row.draft.error !== row.validation" class="quota-feedback error" role="alert">保存失败。{{ row.draft.error }}</p>
                        </div>
                        <p v-else-if="row.draft" class="quota-feedback" role="status">
                          {{ row.draft.state === 'saved' ? '已保存' : row.draft.state === 'saving' ? '正在保存…' : row.dirty ? '待保存' : '' }}
                        </p>
                      </form>
                      <span v-else class="muted">{{ row.member.is_student === false ? '导师只读' : '只读' }}</span>
                    </td>
                  </tr>
                </tbody>
              </table>
              <p v-else class="muted quota-empty">该项目暂无成员用量。</p>
            </section>
          </div>
        </LoadingRegion>
      </div>
      <footer class="quota-footer">
        <span v-if="editable && changedRows.length" class="muted">{{ changedRows.length }} 位学生有未保存的额度</span>
        <div class="quota-footer-actions">
          <button type="button" class="button ghost" :disabled="saving" @click="closeDialog">关闭</button>
          <button v-if="showBatch" type="button" class="button primary quota-save-batch" :disabled="busy || !canSaveBatch" @click="saveBatch">批量保存</button>
        </div>
      </footer>
    </div>
  </AppDialog>
</template>

<style scoped>
:global(.dialog-content:has(> .quota-dialog)) {
  display: flex;
  flex-direction: column;
  max-height: min(780px, calc(100vh - 30px));
  max-height: min(780px, calc(100dvh - 30px));
  overflow: hidden;
}
:global(.dialog-content:has(> .quota-dialog) > .dialog-title) { flex: none; overflow-wrap: anywhere; }
.quota-dialog { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
.quota-toolbar, .quota-members-heading { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px 16px; }
.quota-toolbar { flex: none; margin-bottom: 12px; font-size: 13px; }
.quota-toolbar label { display: flex; align-items: center; gap: 8px; }
.quota-toolbar input { width: 156px; min-width: 0; margin: 0; padding: 8px; }
.quota-scroll {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  padding: 4px;
  margin-inline: -4px;
}
.quota-layout { display: grid; grid-template-columns: minmax(0, 220px) minmax(0, 1fr); gap: 20px; align-items: start; }
.quota-layout h2 { font-size: 15px; margin: 0; }
.quota-overview, .quota-members { min-width: 0; }
.quota-overview { border-right: 1px solid var(--line); padding-right: 16px; }
.quota-summary { display: grid; gap: 10px; margin: 12px 0 16px; font-size: 13px; }
.quota-summary > div { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr); gap: 6px; }
.quota-summary dt { color: var(--ink-soft); }
.quota-summary dd { margin: 0; text-align: right; font-weight: 650; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.quota-available dd { color: var(--primary-strong); }
.quota-allocation { border-top: 1px solid var(--line); padding-top: 12px; }
.quota-allocation h3 { margin: 0; font-size: 13px; }
.quota-allocation :deep(.credit-chart) { grid-template-columns: minmax(0, 1fr); justify-items: center; gap: 8px; }
.quota-allocation :deep(.credit-donut) { width: min(100%, 172px); min-width: 0; height: 144px; }
.quota-allocation :deep(.credit-legend) { width: 100%; }
.quota-allocation :deep(.credit-legend li) { grid-template-columns: 10px minmax(0, 1fr) minmax(0, auto); }
.quota-allocation :deep(.credit-legend strong) { overflow-wrap: anywhere; }
.quota-allocation :deep(table.sr-only) { display: block; }

.quota-members-heading { margin-bottom: 10px; }
.quota-members-heading .muted { font-size: 12px; }
.quota-batch { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.quota-batch label { font-size: 13px; }
.quota-batch input { width: 112px; max-width: 100%; min-width: 0; }
.quota-batch input, .quota-row-actions input { margin: 0; padding: 7px 8px; font-size: 13px; }
.quota-batch .quota-feedback { flex-basis: 100%; }
.quota-table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 13px; }
.quota-table th, .quota-table td { text-align: left; padding: 10px 6px; border-bottom: 1px solid var(--line); vertical-align: top; overflow-wrap: anywhere; }
.quota-table thead th { color: var(--ink-soft); background: var(--primary-soft); font-weight: 650; }
.quota-table thead th:first-child { width: 19%; }
.quota-table thead th:nth-child(4) { width: 17%; }
.quota-table thead th:last-child { width: 30%; }
.quota-table td { font-variant-numeric: tabular-nums; }
.quota-name small, .quota-overused { display: block; margin-top: 4px; font-weight: 500; }
.quota-name small { color: var(--ink-soft); }
.quota-overused { color: #a7354c; }
.quota-cell-label { display: none; }
.quota-pending { display: grid; gap: 3px; font-size: 12px; font-weight: 500; }
.quota-pending.needs-review { color: #795419; }
.quota-pending small { font-size: 11px; }
.quota-row-actions { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 6px; }
.quota-row-actions input { min-width: 0; }
.quota-row-actions button, .quota-top-up { padding: 7px 8px; }
.quota-top-up { margin-top: 4px; }
.quota-preview, .quota-feedback { margin: 4px 0 0; line-height: 1.5; font-size: 12px; overflow-wrap: anywhere; }
.quota-preview { color: var(--ink-soft); }
.quota-preview strong, .quota-preview small { display: block; }
.quota-preview strong { color: var(--ink); }
.quota-empty { padding: 16px 8px; margin: 0; text-align: center; font-size: 13px; }
.quota-notice { margin: 0 0 12px; padding: 8px 10px; background: #fff8e8; color: #795419; border-radius: 8px; font-size: 13px; }
.quota-load-error { margin-bottom: 12px; }
.quota-footer {
  position: sticky;
  bottom: 0;
  flex: none;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
  background: var(--surface-raised, #fff);
  font-size: 12px;
}
.quota-footer-actions { margin-left: auto; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }

@media (max-width: 880px) {
  .quota-layout { grid-template-columns: minmax(0, 1fr); }
  .quota-overview { border-right: 0; border-bottom: 1px solid var(--line); padding: 0 0 12px; }
  .quota-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 16px; }
  .quota-allocation :deep(.credit-chart) { grid-template-columns: minmax(0, 172px) minmax(0, 1fr); }
}
@media (max-width: 640px) {
  .quota-table, .quota-table tbody { display: block; }
  .quota-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .quota-table .quota-member {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px 12px;
    padding: 12px 0;
    border-bottom: 1px solid var(--line);
  }
  .quota-table .quota-member > * { padding: 0; border: 0; min-width: 0; }
  .quota-name, .quota-entry { grid-column: 1 / -1; }
  .quota-name small { display: inline; margin-left: 6px; }
  .quota-cell-label { display: block; color: var(--ink-soft); font-size: 12px; margin-bottom: 2px; }
  .quota-row-actions { grid-template-columns: minmax(0, 12em) auto; justify-content: start; }
  .quota-summary { grid-template-columns: minmax(0, 1fr); }
}
</style>
