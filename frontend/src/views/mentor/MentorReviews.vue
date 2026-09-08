<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeftRight } from 'lucide-vue-next'
import { mentorAPI } from '../../api/mentor'
import type { ApiKey, User } from '../../api/types'
import { APIError } from '../../api/client'
import AppDialog from '../../components/AppDialog.vue'
import EmptyState from '../../components/EmptyState.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'

type Tab = 'pending' | 'reviewed'

const tab = ref<Tab>('pending')
const rows = ref<ApiKey[]>([])
const reviewTarget = ref<ApiKey | null>(null)
const quotaTarget = ref<ApiKey | null>(null)
const comment = ref('')
const monthlyCredits = ref('')
const quota = ref('')
const project = ref('')
const user = ref('')
const error = ref('')
const toast = useToast()
const loadState = useLoadState()

const pendingRows = computed(() => rows.value.filter(key => key.status === 'pending_mentor'))
const reviewedRows = computed(() => rows.value.filter(key => key.status !== 'pending_mentor'))
const visibleRows = computed(() => (tab.value === 'pending' ? pendingRows.value : reviewedRows.value))
const projects = computed(() => [...new Map(rows.value.map(key => [key.project.id, key.project])).values()])
const users = computed(() => {
  const source = project.value
    ? rows.value.filter(key => key.project.id === project.value)
    : rows.value
  const map = new Map<string, User>()
  for (const key of source) {
    if (key.applicant?.id) map.set(key.applicant.id, key.applicant)
  }
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name))
})
const filtered = computed(() => visibleRows.value.filter(key =>
  (!project.value || key.project.id === project.value)
  && (!user.value || key.applicant?.id === user.value),
))
const modelSummary = computed(() =>
  reviewTarget.value?.models.map(model => model.model_id).join('、') ?? '',
)
const tabLabel = computed(() => (tab.value === 'pending' ? '待审核' : '已审核'))
const tabHint = computed(() => (tab.value === 'pending' ? '切换到已审核' : '切换到待审核'))

watch(project, () => {
  user.value = ''
})

function toggleTab() {
  tab.value = tab.value === 'pending' ? 'reviewed' : 'pending'
}

async function load() {
  await loadState.run(async () => {
    try {
      rows.value = (await mentorAPI.keys()).sort((a, b) => b.updated_at.localeCompare(a.updated_at))
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function closeReviewDialog() {
  reviewTarget.value = null
  comment.value = ''
}

async function openReview(row: ApiKey) {
  try {
    reviewTarget.value = await mentorAPI.review(row.id)
    monthlyCredits.value = reviewTarget.value.mentor_monthly_credits ?? reviewTarget.value.requested_monthly_credits ?? ''
    comment.value = ''
    error.value = ''
  } catch (e) {
    error.value = msg(e)
  }
}

async function approve() {
  if (!reviewTarget.value) return
  try {
    await mentorAPI.approve(
      reviewTarget.value.id,
      monthlyCredits.value.trim() || undefined,
      comment.value.trim() || undefined,
    )
    toast.success('已通过审核')
    closeReviewDialog()
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function reject() {
  if (!reviewTarget.value || !comment.value.trim()) {
    error.value = '驳回必须填写原因'
    return
  }
  try {
    await mentorAPI.reject(reviewTarget.value.id, comment.value.trim())
    toast.success('已驳回申请')
    closeReviewDialog()
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function deactivate(row: ApiKey) {
  try {
    await mentorAPI.deactivate(row.id)
    toast.success('Key 已停用')
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function activate(row: ApiKey) {
  try {
    await mentorAPI.activate(row.id)
    toast.success('Key 已启用')
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

function canAdjustQuota(key: ApiKey) {
  return ['approved', 'active', 'inactive'].includes(key.status) && Boolean(key.member_monthly_credits)
}

function usageLabel(key: ApiKey) {
  if (!key.member_monthly_credits) return '—'
  const used = key.member_used_credits ?? '0.000'
  return `${used} / ${key.member_monthly_credits}`
}

function openQuota(row: ApiKey) {
  quotaTarget.value = row
  quota.value = row.member_monthly_credits ?? ''
  error.value = ''
}

function closeQuotaDialog() {
  quotaTarget.value = null
  quota.value = ''
}

async function adjustQuota() {
  if (!quotaTarget.value) return
  try {
    await mentorAPI.updateQuota(quotaTarget.value.id, quota.value.trim())
    toast.success('学生月额度已调整')
    closeQuotaDialog()
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}


function modelLabel(key: ApiKey) {
  return key.models.map(model => model.model_id).join('、') || '—'
}

function msg(e: unknown) {
  return e instanceof APIError ? e.message : '请求失败'
}

function refresh() {
  void load()
}

onMounted(() => {
  void load()
  window.addEventListener('nebula:refresh', refresh)
})
onBeforeUnmount(() => window.removeEventListener('nebula:refresh', refresh))
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">MENTOR</p>
        <div class="title-row">
          <h1>密钥管理</h1>
          <button
            type="button"
            class="view-toggle"
            :class="tab"
            :aria-label="tabHint"
            :title="tabHint"
            @click="toggleTab"
          >
            <span class="view-toggle-label">{{ tabLabel }}</span>
            <ArrowLeftRight :size="14" class="view-toggle-icon" aria-hidden="true" />
          </button>
        </div>
      </div>
      <button class="button secondary" @click="load">刷新</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>

    <section class="panel">
      <div class="filter-grid">
        <label>项目
          <select v-model="project">
            <option value="">全部</option>
            <option v-for="item in projects" :key="item.id" :value="item.id">{{ item.name }}</option>
          </select>
        </label>
        <label>用户
          <select v-model="user">
            <option value="">全部</option>
            <option v-for="item in users" :key="item.id" :value="item.id">{{ item.name }}</option>
          </select>
        </label>
      </div>
    </section>

    <LoadingRegion
      :initial-loading="loadState.initialLoading.value"
      :refreshing="loadState.refreshing.value"
      label="正在加载 Key 列表"
    >
      <section class="panel">
        <table v-if="filtered.length" class="usage-table">
          <thead>
            <tr v-if="tab === 'pending'">
              <th>Key 名称</th>
              <th>学生</th>
              <th>项目</th>
              <th>申请额度</th>
              <th>模型</th>
              <th></th>
            </tr>
            <tr v-else>
              <th>Key 名称</th>
              <th>学生</th>
              <th>项目</th>
              <th>状态</th>
              <th>前缀</th>
              <th>已用 / 分配</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="key in filtered" :key="key.id">
              <td><strong>{{ key.name }}</strong></td>
              <td>
                {{ key.applicant?.name ?? '—' }}
                <br><span class="muted">{{ key.applicant?.email }}</span>
              </td>
              <td>{{ key.project.name }}</td>
              <template v-if="tab === 'pending'">
                <td>{{ key.requested_monthly_credits }} credits</td>
                <td class="model-cell">{{ modelLabel(key) }}</td>
                <td><button type="button" class="button ghost" @click="openReview(key)">审核</button></td>
              </template>
              <template v-else>
                <td><StatusBadge :status="key.status" /></td>
                <td>{{ key.key_prefix ? `${key.key_prefix}…` : '—' }}</td>
                <td>
                  {{ usageLabel(key) }}
                  <button
                    v-if="canAdjustQuota(key)"
                    type="button"
                    class="button ghost quota-adjust"
                    @click="openQuota(key)"
                  >
                    调整
                  </button>
                </td>
                <td class="actions-cell">
                  <button
                    v-if="key.status === 'active'"
                    type="button"
                    class="button danger"
                    @click="deactivate(key)"
                  >
                    停用
                  </button>
                  <button
                    v-else-if="key.status === 'inactive'"
                    type="button"
                    class="button primary"
                    @click="activate(key)"
                  >
                    启用
                  </button>
                  <span v-else class="muted">—</span>
                </td>
              </template>
            </tr>
          </tbody>
        </table>
        <EmptyState
          v-else
          :title="tab === 'pending' ? '暂无待审核申请' : '暂无已审核 Key'"
        />
      </section>
    </LoadingRegion>

    <AppDialog
      :open="Boolean(reviewTarget)"
      title="审核申请"
      @update:open="(open) => { if (!open) closeReviewDialog() }"
    >
      <template v-if="reviewTarget">
        <p><strong>{{ reviewTarget.name }}</strong></p>
        <p class="muted">{{ reviewTarget.applicant?.name }} · {{ reviewTarget.project.name }}</p>
        <p>申请额度 {{ reviewTarget.requested_monthly_credits }} credits</p>
        <p class="muted">{{ modelSummary }}</p>
        <label>学生月额度（credits）<input v-model="monthlyCredits" inputmode="decimal"></label>
        <label>审核意见（驳回时必填）<textarea v-model="comment" rows="3"></textarea></label>
        <div class="actions">
          <button type="button" class="button danger" @click="reject">驳回</button>
          <button type="button" class="button primary" @click="approve">通过</button>
        </div>
      </template>
    </AppDialog>

    <AppDialog
      :open="Boolean(quotaTarget)"
      :title="`调整 ${quotaTarget?.applicant?.name || ''} 月额度`"
      @update:open="(open) => { if (!open) closeQuotaDialog() }"
    >
      <template v-if="quotaTarget">
        <p><strong>{{ quotaTarget.name }}</strong></p>
        <p class="muted">{{ quotaTarget.project.name }} · 当前已用 {{ quotaTarget.member_used_credits ?? '0.000' }}</p>
        <label>新月额度（credits）<input v-model="quota" inputmode="decimal"></label>
        <button type="button" class="button primary" @click="adjustQuota">确认调额</button>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.title-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.title-row h1 {
  margin: 0;
}

.view-toggle {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  margin-top: 2px;
  padding: 5px 10px 5px 12px;
  border: 1px dashed rgba(154, 106, 23, 0.42);
  border-radius: 10px;
  background: rgba(255, 244, 221, 0.72);
  color: #9a6a17;
  font-size: 12px;
  font-weight: 760;
  line-height: 1;
  cursor: pointer;
  transition:
    transform var(--motion-press) var(--ease-out),
    box-shadow var(--motion-press) var(--ease-out),
    border-color var(--motion-press) var(--ease-out),
    background var(--motion-press) var(--ease-out);
}

.view-toggle.reviewed {
  border-color: rgba(101, 66, 220, 0.34);
  background: rgba(101, 66, 220, 0.07);
  color: #6542dc;
}

.view-toggle:hover {
  transform: translateY(-1px);
  border-style: solid;
  box-shadow: 0 4px 14px rgba(101, 66, 220, 0.12);
}

.view-toggle:active {
  transform: translateY(0);
}

.view-toggle-icon {
  opacity: 0.5;
  transition:
    transform var(--motion-state) var(--ease-out),
    opacity var(--motion-state) var(--ease-out);
}

.view-toggle:hover .view-toggle-icon,
.view-toggle:focus-visible .view-toggle-icon {
  opacity: 1;
  transform: rotate(180deg);
}

.view-toggle:focus-visible {
  outline: 2px solid rgba(101, 66, 220, 0.35);
  outline-offset: 2px;
}

.model-cell {
  max-width: 220px;
  white-space: normal;
}

.quota-adjust {
  margin-left: 8px;
}

.actions-cell {
  white-space: nowrap;
}
</style>
