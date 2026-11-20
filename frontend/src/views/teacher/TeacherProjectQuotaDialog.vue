<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { APIError } from '../../api/client'
import type { Project, ProjectUsage } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import CreditDonut, { type CreditSegment } from '../../components/CreditDonut.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { formatCredits } from '../../utils/credits'
import { beichenBillingExplanation, calculateCredits, quotaBalance, schoolMonth } from '../../utils/format'

const props = defineProps<{ project: Project; initialMonth: string }>()
const emit = defineEmits<{ close: []; saved: [] }>()
type Member = ProjectUsage['members'][number]

const quotaMonth = ref(props.initialMonth)
const usage = ref<ProjectUsage | null>(null)
const loadError = ref('')
const editor = ref<Member | null>(null)
const amount = ref('')
const saveError = ref('')
const saving = ref(false)
const loadState = useLoadState()
const toast = useToast()
const fieldId = useId()
let request = 0

const editable = computed(() => props.project.status === 'active'
  && quotaMonth.value === schoolMonth()
  && usage.value?.month === quotaMonth.value
  && !loadState.loading.value)
const minimum = computed(() => editor.value
  ? calculateCredits(editor.value.credits, editor.value.pending ?? '0', 'add') : '0')
const minimumQuota = computed(() => roundUpQuota(minimum.value))
const validAmount = computed(() => /^\d+(\.\d{1,3})?$/.test(amount.value.trim()))
const validation = computed(() => {
  if (!validAmount.value) return '请输入非负积分，最多三位小数。'
  if (greater(minimum.value, amount.value.trim())) {
    return `新额度不能低于已用与待核合计，至少需 ${formatCredits(minimumQuota.value)} 积分。`
  }
  return ''
})
const exceedsAvailable = computed(() => validAmount.value && editor.value && usage.value
  && greater(amount.value.trim(), calculateCredits(editor.value.quota, usage.value.available, 'add')))
const remaining = computed(() => validAmount.value
  ? calculateCredits(amount.value.trim(), minimum.value, 'subtract') : null)
const allocation = computed<CreditSegment[]>(() => {
  if (!usage.value) return []
  return [
    ...usage.value.members.filter(member => member.is_student && Number(member.quota) > 0)
      .map(member => ({ name: member.user_name, value: Number(member.quota), displayValue: `${formatCredits(member.quota)} 积分` })),
    { name: '可再分配', value: Number(usage.value.available), color: '#dfe3ec', displayValue: `${formatCredits(usage.value.available)} 积分` },
  ]
})
const hasAllocation = computed(() => usage.value?.members.some(member => member.is_student && Number(member.quota) > 0))

function greater(left: string, right: string) {
  return calculateCredits(left, right, 'subtract') !== '0.000'
}

// 已用积分可能超过三位小数，向上取整才能覆盖全部已用与待核金额。
function roundUpQuota(value: string) {
  const [whole, fraction = ''] = value.split('.')
  const milli = BigInt(whole + fraction.slice(0, 3).padEnd(3, '0')) + (/[1-9]/.test(fraction.slice(3)) ? 1n : 0n)
  const digits = milli.toString().padStart(4, '0')
  return `${digits.slice(0, -3)}.${digits.slice(-3)}`
}

function usedPercent(member: Member) {
  const quota = Number(member.quota)
  const used = Number(member.credits)
  return quota > 0 ? Math.min(100, Math.max(0, used / quota * 100)) : used > 0 ? 100 : 0
}

async function loadUsage() {
  const currentRequest = ++request
  const projectID = props.project.id
  const selectedMonth = quotaMonth.value
  editor.value = null
  usage.value = null
  loadError.value = ''
  if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(selectedMonth)) {
    loadError.value = '请选择有效月份。'
    return
  }
  await loadState.run(async () => {
    try {
      const result = await teacherAPI.projectUsage(projectID, selectedMonth)
      if (currentRequest === request) usage.value = result
    } catch (error) {
      if (currentRequest === request) loadError.value = '读取学生额度失败。' + message(error)
    }
  })
}

function openEditor(member: Member) {
  if (!editable.value || !member.is_student || saving.value) return
  editor.value = member
  amount.value = member.quota
  saveError.value = ''
}

function addQuota(extra: string) {
  amount.value = calculateCredits(validAmount.value ? amount.value.trim() : editor.value?.quota ?? '0', extra, 'add')
  saveError.value = ''
}

async function saveQuota() {
  const member = editor.value
  if (!member?.is_student || !editable.value || quotaMonth.value !== schoolMonth() || saving.value || validation.value) return
  saving.value = true
  saveError.value = ''
  try {
    await teacherAPI.updateMemberQuota(props.project.id, member.user_id, amount.value.trim())
    toast.success('学生月额度已保存')
    editor.value = null
    emit('saved')
    await loadUsage()
  } catch (error) {
    saveError.value = message(error)
  } finally {
    saving.value = false
  }
}

function message(error: unknown) {
  return error instanceof APIError ? error.message : '请求失败，请稍后重试。'
}

watch([() => props.project.id, quotaMonth], loadUsage, { immediate: true })
onBeforeUnmount(() => { request += 1 })
</script>

<template>
  <AppDialog
    :open="true"
    :title="`${project.name} · 学生额度`"
    description="查看项目积分分配和成员用量，在本月调整学生额度。"
    size="xwide"
    @update:open="!$event && !saving && emit('close')"
  >
    <div class="quota-dialog">
      <div class="quota-toolbar">
        <label :for="`${fieldId}-month`">月份
          <input :id="`${fieldId}-month`" v-model="quotaMonth" type="month" :max="schoolMonth()" :disabled="saving">
        </label>
        <p class="muted">额度与用量均以积分显示<br>仅可调整本月的学生额度</p>
      </div>
      <p v-if="quotaMonth !== schoolMonth()" class="quota-notice" role="status">{{ quotaMonth < schoolMonth() ? '历史月份只读，不能调整额度。' : '仅可调整本月额度。' }}</p>
      <p v-else-if="project.status !== 'active'" class="quota-notice" role="status">项目已停用，额度仅供查看。</p>
      <div v-if="loadError" class="quota-load-error" role="alert">
        <p class="error">{{ loadError }}</p>
        <button type="button" class="button secondary" @click="loadUsage">重新加载</button>
      </div>
      <LoadingRegion
        :initial-loading="loadState.loading.value && !usage"
        :refreshing="loadState.refreshing.value"
        label="正在加载额度分配"
        variant="cards"
      >
        <template v-if="usage">
          <div class="beichen-summary quota-summary" aria-label="项目额度汇总">
            <div class="beichen-stat"><span>项目月额度</span><strong>{{ formatCredits(usage.quota) }}<small>积分</small></strong></div>
            <div class="beichen-stat"><span>已分配给学生</span><strong>{{ formatCredits(usage.allocated) }}<small>积分</small></strong></div>
            <div class="beichen-stat"><span>可再分配</span><strong>{{ formatCredits(usage.available) }}<small>积分</small></strong></div>
            <div class="beichen-stat"><span>本月已消耗</span><strong>{{ formatCredits(usage.charged) }}<small>积分</small></strong><small v-if="usage.pending_review">有调用待核账</small></div>
          </div>
          <div class="quota-body">
            <section class="quota-allocation" aria-label="成员分配占比">
              <h2>成员分配占比</h2>
              <CreditDonut v-if="hasAllocation" :segments="allocation" :total="Number(usage.quota)" label="项目额度：学生分配与可再分配积分" unit="积分" />
              <div v-else class="quota-empty">
                <strong>尚未分配</strong>
                <p>{{ editable ? '选择右侧学生，点击「调整额度」开始分配。' : '该月没有学生额度分配。' }}</p>
              </div>
            </section>
            <section class="quota-members" aria-label="成员用量">
              <div class="quota-members-heading"><h2>成员用量</h2><span class="muted">{{ usage.members.length }} 位成员</span></div>
              <ul v-if="usage.members.length" class="quota-member-list" tabindex="0" aria-label="成员额度列表">
                <li v-for="member in usage.members" :key="member.user_id" class="quota-member" :data-member-id="member.user_id">
                  <div class="quota-member-heading">
                    <div class="quota-member-identity"><strong>{{ member.user_name }}</strong><span class="status-badge" :class="{ active: member.is_student }">{{ member.is_student ? '学生' : '导师' }}</span></div>
                    <button v-if="member.is_student && editable" type="button" class="button secondary" :aria-label="`${member.user_name} · 调整额度`" :disabled="saving" @click="openEditor(member)">调整额度</button>
                    <span v-else class="quota-readonly">{{ member.is_student ? '只读' : '导师只读' }}</span>
                  </div>
                  <dl class="quota-member-metrics">
                    <div><dt>已分配</dt><dd>{{ formatCredits(member.quota) }}</dd></div>
                    <div><dt>已用</dt><dd>{{ formatCredits(member.credits) }}</dd></div>
                    <div :class="{ 'quota-overused': greater(member.credits, member.quota) }"><dt>{{ quotaBalance(member.quota, member.credits, member.available).label }}</dt><dd>{{ formatCredits(quotaBalance(member.quota, member.credits, member.available).amount) }}</dd></div>
                  </dl>
                  <progress :value="usedPercent(member)" max="100" :aria-label="`${member.user_name}的额度使用进度`" :class="{ overused: greater(member.credits, member.quota) }" />
                  <p v-if="member.pending_review || Number(member.pending) > 0" class="quota-pending">{{ Number(member.pending) > 0 ? `待核预留 ${formatCredits(member.pending)} 积分` : '有调用待核账' }}</p>
                </li>
              </ul>
              <div v-else class="quota-empty"><strong>暂无成员</strong><p>该项目本月暂无成员用量。</p></div>
            </section>
          </div>
          <details class="quota-billing-note"><summary>积分扣费说明</summary><p>{{ beichenBillingExplanation }}</p></details>
        </template>
      </LoadingRegion>
    </div>
  </AppDialog>
  <AppDialog
    :open="Boolean(editor)"
    :title="`${editor?.user_name ?? ''} · 调整额度`"
    description="设置该学生的项目月额度，保存后从本月生效。"
    @update:open="!$event && !saving && (editor = null)"
  >
    <form v-if="editor" class="quota-editor" @submit.prevent="saveQuota">
      <p class="muted">{{ quotaMonth }} · {{ project.name }} · 单位：积分</p>
      <dl class="quota-editor-current">
        <div><dt>当前额度</dt><dd>{{ formatCredits(editor.quota) }}</dd></div>
        <div><dt>已用</dt><dd>{{ formatCredits(editor.credits) }}</dd></div>
        <div><dt>待核预留</dt><dd>{{ formatCredits(editor.pending ?? '0') }}</dd></div>
      </dl>
      <label :for="`${fieldId}-amount`">新月额度（本月起，积分）
        <input :id="`${fieldId}-amount`" v-model="amount" inputmode="decimal" required :disabled="saving" :aria-invalid="Boolean(validation)" :aria-describedby="`${fieldId}-validation`" @input="saveError = ''">
      </label>
      <div class="quota-shortcuts" aria-label="快捷调整额度">
        <button v-for="extra in ['100', '500', '1000']" :key="extra" type="button" class="button ghost" :disabled="saving" @click="addQuota(extra)">+{{ extra }}</button>
        <button type="button" class="button ghost" :disabled="saving" @click="amount = minimumQuota; saveError = ''">补足已用</button>
      </div>
      <p class="quota-preview">调整后可用额度<strong>{{ validation ? '—' : formatCredits(remaining) }} 积分</strong><small>已扣除已用及待核预留；「补足已用」也会覆盖待核预留。</small></p>
      <p :id="`${fieldId}-validation`" :class="validation ? 'error' : 'muted'" :role="validation ? 'alert' : undefined">{{ validation || '请输入非负积分，最多三位小数。' }}</p>
      <p v-if="exceedsAvailable" class="quota-notice" role="status">增加的额度超过项目可再分配的 {{ formatCredits(usage?.available) }} 积分，保存时将由服务器核验。</p>
      <p v-if="saveError" role="alert" class="error">{{ saveError }}</p>
      <div class="quota-editor-actions">
        <button type="button" class="button ghost" :disabled="saving" @click="editor = null">取消</button>
        <button class="button primary" :disabled="saving || !editable || Boolean(validation)">{{ saving ? '正在保存…' : '保存' }}</button>
      </div>
    </form>
  </AppDialog>
</template>

<style scoped>
/* 只提升本弹窗的成员编辑层，遮罩需盖住仍打开的额度总览。 */
:global(.dialog-overlay:has(+ .dialog-content > .quota-editor)) { z-index: 42; }
:global(.dialog-content:has(> .quota-editor)) { z-index: 43; background: #fff; }
.quota-dialog, .quota-editor { min-width: 0; }
.quota-toolbar, .quota-members-heading, .quota-member-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.quota-toolbar { margin-bottom: 20px; }
.quota-toolbar label { width: 180px; max-width: 100%; }
.quota-toolbar p { margin: 0; text-align: right; }
.quota-summary { grid-template-columns: repeat(4, minmax(0, 1fr)); margin-bottom: 24px; }
.quota-summary strong { overflow-wrap: anywhere; }
.quota-summary strong small { display: inline; margin-left: 6px; font-size: 11px; font-weight: 500; }
.quota-body { display: grid; grid-template-columns: minmax(0, 0.85fr) minmax(0, 1.65fr); gap: 24px; align-items: start; }
.quota-allocation, .quota-members { min-width: 0; }
.quota-body h2 { margin: 0 0 16px; font-size: 15px; }
.quota-allocation { padding-right: 24px; border-right: 1px solid var(--line); }
.quota-allocation :deep(.credit-chart) { grid-template-columns: minmax(0, 1fr); justify-items: center; gap: 16px; }
.quota-allocation :deep(.credit-legend) { width: 100%; max-height: 24vh; overflow-y: auto; padding-right: 4px; }
.quota-allocation :deep(.credit-legend li) { grid-template-columns: 10px minmax(0, 1fr) minmax(0, auto); }
.quota-allocation :deep(.credit-legend strong) { overflow-wrap: anywhere; }
/* 隐藏表格仍须保留给读屏器，块布局可避免长姓名撑开表格的固有宽度。 */
.quota-allocation :deep(table.sr-only) { display: block; }
.quota-members-heading { margin-bottom: 12px; }
.quota-members-heading h2 { margin: 0; }
.quota-member-list { list-style: none; padding: 0 8px 0 0; margin: 0; max-height: 42vh; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; scrollbar-color: var(--line) transparent; }
.quota-member { padding: 16px 0; border-top: 1px solid var(--line); }
.quota-member:first-child { padding-top: 0; border-top: 0; }
.quota-member-heading { gap: 12px; margin-bottom: 12px; }
.quota-member-identity { min-width: 0; display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.quota-member-identity > strong { font-size: 14px; overflow-wrap: anywhere; }
.quota-member-heading > button { flex-shrink: 0; min-height: 40px; }
.quota-readonly { flex-shrink: 0; color: var(--ink-soft); font-size: 12px; }
.quota-member-metrics, .quota-editor-current { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin: 0 0 12px; }
.quota-member-metrics > div, .quota-editor-current > div { min-width: 0; }
dt { color: var(--ink-soft); font-size: 12px; line-height: 1.5; }
dd { margin: 4px 0 0; font-size: 14px; font-weight: 650; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.quota-overused dt, .quota-overused dd { color: #b53b55; }
.quota-member progress { display: block; width: 100%; height: 6px; border: 0; border-radius: 3px; appearance: none; background: var(--primary-soft); }
.quota-member progress::-webkit-progress-bar { border-radius: 3px; background: var(--primary-soft); }
.quota-member progress::-webkit-progress-value { border-radius: 3px; background: var(--primary); }
.quota-member progress::-moz-progress-bar { border-radius: 3px; background: var(--primary); }
.quota-member progress.overused::-webkit-progress-value { background: #b53b55; }
.quota-member progress.overused::-moz-progress-bar { background: #b53b55; }
.quota-pending { margin: 8px 0 0; color: #8b631c; font-size: 12px; }
.quota-empty { padding: 36px 12px; text-align: center; color: var(--ink-soft); font-size: 13px; line-height: 1.6; }
.quota-empty strong { color: var(--ink); font-size: 15px; }
.quota-empty p { margin: 8px 0 0; }
.quota-billing-note { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--line); color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.quota-billing-note summary { cursor: pointer; width: fit-content; }
.quota-billing-note p { margin: 12px 0 0; }
.quota-notice { border: 1px solid #ead9b4; border-radius: 12px; padding: 12px; color: #7c581b; background: #fff8e8; font-size: 13px; line-height: 1.6; }
.quota-load-error { margin-bottom: 16px; }
.quota-editor { display: grid; gap: 16px; }
.quota-editor > p { margin: 0; }
.quota-editor-current { margin: 0; padding-bottom: 16px; border-bottom: 1px solid var(--line); }
.quota-shortcuts { display: flex; flex-wrap: wrap; gap: 8px; }
.quota-shortcuts button { min-height: 40px; }
.quota-preview { color: var(--ink-soft); font-size: 13px; }
.quota-preview strong { display: block; margin: 8px 0; color: var(--ink); font-size: 20px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.quota-preview small { display: block; line-height: 1.6; }
.quota-editor-actions { display: flex; justify-content: flex-end; gap: 12px; }
.quota-dialog :focus-visible, .quota-editor :focus-visible { outline: 2px solid var(--primary); outline-offset: 3px; }
@media (max-width: 800px) {
  .quota-body { grid-template-columns: minmax(0, 1fr); }
  .quota-allocation { padding: 0 0 20px; border-right: 0; border-bottom: 1px solid var(--line); }
  .quota-allocation :deep(.credit-chart) { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: center; }
  .quota-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 480px) {
  .quota-toolbar { align-items: flex-start; gap: 12px; }
  .quota-toolbar label { width: 150px; flex-shrink: 0; }
  .quota-toolbar p { font-size: 11px; }
  .quota-allocation :deep(.credit-chart) { grid-template-columns: minmax(0, 1fr); }
  .quota-member-list { padding-right: 4px; }
  .quota-member-heading { align-items: flex-start; }
  .quota-member-identity { gap: 6px; }
  .quota-member-heading > button { padding: 8px 12px; }
  .quota-editor-actions > button { flex: 1; min-height: 44px; }
}
</style>
