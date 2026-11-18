<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ArrowLeftRight } from 'lucide-vue-next'
import { teacherAPI } from '../../api/teacher'
import type { Application, MentorCandidate, MentorSummary, Organization, Project, ProjectBudget, ProjectUsage } from '../../api/types'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import CreditDonut, { type CreditSegment } from '../../components/CreditDonut.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import EmptyState from '../../components/EmptyState.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'

const orgs = ref<Organization[]>([])
const projects = ref<Project[]>([])
const budgets = ref<ProjectBudget[]>([])
const apps = ref<Application[]>([])
const month = ref(new Date().toISOString().slice(0, 7))
const organization = ref('')
const query = ref('')
const drawer = ref(false)
const editing = ref<Project | null>(null)
const manageProject = ref<Project | null>(null)
const mentorPicker = ref<'add' | 'replace' | null>(null)
const replacingMentor = ref<MentorSummary | null>(null)
const candidates = ref<MentorCandidate[]>([])
const candidateQuery = ref('')
const candidateCursor = ref<string | null>(null)
const reviewing = ref<Application | null>(null)
const approve = ref(false)
const comment = ref('')
const error = ref('')
const toast = useToast()
const loadState = useLoadState()
const candidateLoadState = useLoadState()
const usageLoadState = useLoadState()
const detailProject = ref<Project | null>(null)
const projectUsage = ref<ProjectUsage | null>(null)
const form = reactive({ organization_id: '', name: '', description: '', status: 'active' as 'active' | 'inactive', monthly_credit_quota: '' })

type Tab = 'projects' | 'pending'
const tab = ref<Tab>('projects')

type MemberUsage = ProjectUsage['members'][number]

const quotaChanged = computed(() => editing.value != null && form.monthly_credit_quota.trim() !== editing.value.monthly_credit_quota)
const budgetsById = computed(() => new Map(budgets.value.map(row => [row.id, row])))
const filtered = computed(() => projects.value.filter(project => {
  if (organization.value && project.organization_id !== organization.value) return false
  return !query.value || project.name.toLowerCase().includes(query.value.toLowerCase())
}))
const pendingApps = computed(() => apps.value.filter(app => app.status === 'pending'))
const tabLabel = computed(() => (tab.value === 'pending' ? '待审核' : '全部项目'))
const tabHint = computed(() => (tab.value === 'pending' ? '切换到全部项目' : '切换到待审核'))
const pickerCandidates = computed(() => candidates.value.filter(candidate => !candidate.is_member))

function toggleTab() {
  tab.value = tab.value === 'projects' ? 'pending' : 'projects'
}

async function load() {
  await loadState.run(async () => {
    try {
      const params = new URLSearchParams({ month: month.value, ...(organization.value ? { organization_id: organization.value } : {}) })
      ;[orgs.value, projects.value, apps.value, budgets.value] = await Promise.all([
        teacherAPI.organizations(),
        teacherAPI.projects(),
        teacherAPI.mentorApplications(),
        teacherAPI.projectBudgets(params.toString()),
      ])
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function open(project?: Project) {
  editing.value = project ?? null
  Object.assign(form, {
    organization_id: project?.organization_id ?? '',
    name: project?.name ?? '',
    description: project?.description ?? '',
    status: project?.status ?? 'active',
    monthly_credit_quota: project?.monthly_credit_quota ?? '',
  })
  drawer.value = true
}

async function save() {
  try {
    if (editing.value) {
      if (!form.monthly_credit_quota.trim()) {
        error.value = '请填写月额度'
        return
      }
      await teacherAPI.updateProject(editing.value.id, { name: form.name, description: form.description, status: form.status })
      if (quotaChanged.value) {
        await teacherAPI.updateProjectBudget(editing.value.id, {
          monthly_credit_quota: form.monthly_credit_quota.trim(),
        })
      }
    } else {
      if (!form.monthly_credit_quota.trim()) {
        error.value = '请填写月额度'
        return
      }
      await teacherAPI.createProject({
        organization_id: form.organization_id,
        name: form.name,
        description: form.description,
        status: form.status,
        monthly_credit_quota: form.monthly_credit_quota.trim(),
      })
    }
    toast.success(editing.value ? (quotaChanged.value ? '项目与额度已更新' : '项目已更新') : '项目已创建')
    drawer.value = false
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function toggle(project: Project) {
  try {
    await teacherAPI.updateProject(project.id, { status: project.status === 'active' ? 'inactive' : 'active' })
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

function openMentorManage(project: Project) {
  manageProject.value = project
  closeMentorPicker()
}

function closeMentorManage() {
  manageProject.value = null
  closeMentorPicker()
}

function closeMentorPicker() {
  mentorPicker.value = null
  replacingMentor.value = null
  candidateQuery.value = ''
  candidateCursor.value = null
  candidates.value = []
}

function openAddPicker() {
  mentorPicker.value = 'add'
  replacingMentor.value = null
  candidateQuery.value = ''
  candidateCursor.value = null
  void searchCandidates(true)
}

function openReplacePicker(mentor: MentorSummary) {
  mentorPicker.value = 'replace'
  replacingMentor.value = mentor
  candidateQuery.value = ''
  candidateCursor.value = null
  void searchCandidates(true)
}

async function refreshManageProject() {
  const projectID = manageProject.value?.id
  await load()
  if (projectID) {
    manageProject.value = projects.value.find(project => project.id === projectID) ?? manageProject.value
  }
}

async function searchCandidates(reset = true) {
  if (!manageProject.value) return
  await candidateLoadState.run(async () => {
    try {
      const page = await teacherAPI.projectMentorCandidates(
        manageProject.value!.id,
        candidateQuery.value,
        reset ? '' : candidateCursor.value ?? '',
        20,
      )
      candidates.value = reset ? page.data : [...candidates.value, ...page.data]
      candidateCursor.value = page.meta.next_cursor ?? null
    } catch (e) {
      error.value = msg(e)
    }
  })
}

async function addMentor(candidate: MentorCandidate) {
  if (!manageProject.value || !confirm(`确认添加 ${candidate.name} 为导师？`)) return
  try {
    await teacherAPI.assignProjectMentor(manageProject.value.id, candidate.id)
    toast.success('导师已添加')
    closeMentorPicker()
    await refreshManageProject()
  } catch (e) {
    error.value = msg(e)
  }
}

async function replaceMentor(candidate: MentorCandidate) {
  if (!manageProject.value || !replacingMentor.value || !confirm(`确认将 ${replacingMentor.value.name} 更换为 ${candidate.name}？`)) return
  try {
    await teacherAPI.removeProjectMentor(manageProject.value.id, replacingMentor.value.id)
    await teacherAPI.assignProjectMentor(manageProject.value.id, candidate.id)
    toast.success('导师已更换')
    closeMentorPicker()
    await refreshManageProject()
  } catch (e) {
    error.value = msg(e)
  }
}

async function removeMentor(mentor: MentorSummary) {
  if (!manageProject.value || !confirm(`确认删除导师 ${mentor.name}？`)) return
  try {
    await teacherAPI.removeProjectMentor(manageProject.value.id, mentor.id)
    toast.success('导师已删除')
    await refreshManageProject()
  } catch (e) {
    error.value = msg(e)
  }
}

async function review() {
  if (!reviewing.value) return
  if (!approve.value && !comment.value.trim()) {
    error.value = '驳回必须填写原因'
    return
  }
  try {
    await teacherAPI.reviewMentor(reviewing.value.id, approve.value, comment.value || undefined)
    reviewing.value = null
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

function mentorLabel(project: Project) {
  if (!project.mentors?.length) return '暂无导师'
  return project.mentors.map(mentor => mentor.name).join('、')
}

function budgetStats(project: Project) {
  return budgetsById.value.get(project.id)
}

function orgName(id: string) {
  return orgs.value.find(org => org.id === id)?.name ?? ''
}

function statsLine(project: Project) {
  const stats = budgetStats(project)
  const quota = stats?.quota ?? project.monthly_credit_quota
  if (!stats) return `月额度 ${quota}`
  return `月额度 ${stats.quota} · 已发给学生 ${stats.allocated} · 可再分配 ${stats.available} · 已消耗 ${stats.charged}`
}

async function showDetail(project: Project) {
  detailProject.value = project
  projectUsage.value = null
  await usageLoadState.run(async () => {
    try {
      projectUsage.value = await teacherAPI.projectUsage(project.id, month.value)
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function memberQuotaValue(member: MemberUsage) {
  return Number(member.quota)
}

function allocationSegments(usage: ProjectUsage): CreditSegment[] {
  return [
    ...usage.members
      .map(member => ({ name: member.user_name, value: Number(member.quota) }))
      .filter(item => item.value > 0),
    { name: '可再分配', value: Number(usage.available), color: '#dfe3ec' },
  ]
}

function msg(e: unknown) {
  return e instanceof APIError ? e.message : '请求失败'
}

onMounted(load)
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">TEACHER</p>
        <div class="title-row">
          <h1>项目管理</h1>
          <button
            type="button"
            class="view-toggle"
            :class="{ reviewed: tab === 'projects' }"
            :aria-label="tabHint"
            :title="tabHint"
            @click="toggleTab"
          >
            <span class="view-toggle-label">{{ tabLabel }}</span>
            <ArrowLeftRight :size="14" class="view-toggle-icon" aria-hidden="true" />
          </button>
        </div>
      </div>
      <div class="row">
        <label v-if="tab === 'projects'">月份<input v-model="month" type="month" @change="load"></label>
        <button class="button secondary" @click="load">刷新</button>
        <button v-if="tab === 'projects'" class="button primary" @click="open()">添加项目</button>
      </div>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel">
      <template v-if="tab === 'projects'">
        <div class="filter-grid project-filters">
          <label>组织<select v-model="organization" @change="load"><option value="">全部组织</option><option v-for="org in orgs" :key="org.id" :value="org.id">{{ org.name }}</option></select></label>
          <label>项目名称<input v-model="query" placeholder="输入项目名称"></label>
        </div>
        <div v-if="loadState.initialLoading.value" class="empty">正在加载项目…</div>
        <template v-else>
          <div v-for="project in filtered" :key="project.id" class="list-row project-row">
            <div class="project-row-main">
              <strong>{{ project.name }}</strong>
              <p class="muted">{{ orgName(project.organization_id) }} · 导师：{{ mentorLabel(project) }}</p>
              <p class="muted">{{ statsLine(project) }}</p>
            </div>
            <div class="actions">
              <StatusBadge :status="project.status" />
              <button class="button ghost" @click="showDetail(project)">详情</button>
              <button class="button ghost" @click="openMentorManage(project)">导师管理</button>
              <button class="button ghost" @click="open(project)">编辑</button>
              <button class="button" :class="project.status === 'active' ? 'danger' : 'secondary'" @click="toggle(project)">
                {{ project.status === 'active' ? '停用' : '启用' }}
              </button>
            </div>
          </div>
          <div v-if="!filtered.length" class="empty">暂无项目</div>
        </template>
      </template>

      <template v-else>
        <div v-if="loadState.initialLoading.value" class="empty">正在加载待审核申请…</div>
        <template v-else>
          <article v-for="app in pendingApps" :key="app.id" class="review-row">
            <div>
              <strong>{{ app.mentor.name }}</strong>
              <p>{{ app.project.name }} · {{ orgName(app.project.organization_id) }}</p>
            </div>
            <div class="actions">
              <button class="button danger" @click="reviewing = app; approve = false">驳回</button>
              <button class="button primary" @click="reviewing = app; approve = true">通过</button>
            </div>
          </article>
          <EmptyState v-if="!pendingApps.length" title="暂无待审核申请" />
        </template>
      </template>
    </section>

    <AppDialog :open="drawer" :title="editing ? '编辑项目' : '创建项目'" @update:open="drawer = $event">
      <form @submit.prevent="save">
        <label>所属组织<select v-model="form.organization_id" :disabled="Boolean(editing)" required><option value="">请选择</option><option v-for="org in orgs" :key="org.id" :value="org.id">{{ org.name }}</option></select></label>
        <label>名称<input v-model="form.name" required /></label>
        <label>月额度（credits）<input v-model="form.monthly_credit_quota" inputmode="decimal" required /></label>
        <label>说明<textarea v-model="form.description" rows="3" /></label>
        <label>状态<select v-model="form.status"><option value="active">启用</option><option value="inactive">停用</option></select></label>
        <button class="button primary">保存</button>
      </form>
    </AppDialog>

    <AppDialog :open="Boolean(manageProject)" :title="`导师管理 · ${manageProject?.name || ''}`" wide @update:open="open => !open && closeMentorManage()">
      <div class="mentor-manage-toolbar">
        <button v-if="!mentorPicker" type="button" class="button primary" @click="openAddPicker">添加导师</button>
        <button v-else type="button" class="button ghost" @click="closeMentorPicker">取消</button>
      </div>

      <LoadingRegion
        v-if="mentorPicker"
        :initial-loading="candidateLoadState.initialLoading.value"
        :refreshing="candidateLoadState.refreshing.value"
        variant="list"
        label="正在加载导师"
      >
        <p v-if="mentorPicker === 'replace' && replacingMentor" class="muted picker-hint">更换 {{ replacingMentor.name }}</p>
        <div class="row">
          <input v-model="candidateQuery" placeholder="搜索姓名或邮箱" @keyup.enter="searchCandidates()">
          <button type="button" class="button secondary" @click="searchCandidates()">搜索</button>
        </div>
        <article v-for="candidate in pickerCandidates" :key="candidate.id" class="list-row">
          <div><strong>{{ candidate.name }}</strong><span class="muted"> · {{ candidate.email }}</span></div>
          <button
            type="button"
            class="button primary"
            @click="mentorPicker === 'replace' ? replaceMentor(candidate) : addMentor(candidate)"
          >
            选择
          </button>
        </article>
        <p v-if="!pickerCandidates.length && !candidateLoadState.initialLoading.value" class="muted">没有可选择的导师</p>
        <button v-if="candidateCursor" type="button" class="button ghost wide" @click="searchCandidates(false)">加载更多</button>
      </LoadingRegion>

      <article v-for="mentor in manageProject?.mentors ?? []" :key="mentor.id" class="list-row mentor-row">
        <div><strong>{{ mentor.name }}</strong><span class="muted"> · {{ mentor.email }}</span></div>
        <div class="actions">
          <button type="button" class="button ghost" @click="openReplacePicker(mentor)">修改</button>
          <button type="button" class="button danger" @click="removeMentor(mentor)">删除</button>
        </div>
      </article>
      <p v-if="!(manageProject?.mentors?.length)" class="empty">暂无导师</p>
    </AppDialog>

    <AppDialog :open="Boolean(reviewing)" :title="approve ? '通过导师申请' : '驳回导师申请'" @update:open="reviewing = $event ? reviewing : null">
      <label>审核意见<textarea v-model="comment" rows="3" /></label>
      <button class="button" :class="approve ? 'primary' : 'danger'" @click="review">确认</button>
    </AppDialog>

    <AppDialog
      :open="Boolean(detailProject)"
      :title="`${detailProject?.name || ''} · 用量详情`"
      wide
      @update:open="detailProject = $event ? detailProject : null"
    >
      <div class="row">
        <label>月份<input v-model="month" type="month" @change="detailProject && showDetail(detailProject)"></label>
      </div>
      <LoadingRegion
        :initial-loading="usageLoadState.initialLoading.value"
        :refreshing="usageLoadState.refreshing.value"
        variant="list"
        label="正在加载额度分配"
      >
        <template v-if="projectUsage">
          <div class="usage-summary usage-dialog-summary">
            <div class="usage-stat">项目月额度<strong>{{ projectUsage.quota }}</strong></div>
            <div class="usage-stat">已分配给学生<strong>{{ projectUsage.allocated }}</strong></div>
            <div class="usage-stat">可再分配<strong>{{ projectUsage.available }}</strong></div>
            <div class="usage-stat">本月已消耗<strong>{{ projectUsage.charged }}</strong><small v-if="projectUsage.pending_review">有调用待核账</small></div>
          </div>
          <section class="usage-chart-card">
            <h2>成员分配占比</h2>
            <CreditDonut
              :segments="allocationSegments(projectUsage)"
              :total="Number(projectUsage.quota)"
              label="项目额度：各学生月额度与剩余可分配额度"
            />
          </section>
          <section v-if="projectUsage.members.length" class="member-usage-section">
            <h2>成员用量</h2>
            <table class="usage-table member-quota-table">
              <caption class="sr-only">各成员月额度与消耗</caption>
              <colgroup>
                <col class="member-col">
                <col class="metric-col" span="3">
              </colgroup>
              <thead>
                <tr>
                  <th scope="col">成员</th>
                  <th scope="col" class="numeric-cell">月额度</th>
                  <th scope="col" class="numeric-cell">已用</th>
                  <th scope="col" class="numeric-cell">剩余</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="member in projectUsage.members" :key="member.user_id">
                  <th scope="row">{{ member.user_name }}</th>
                  <td class="numeric-cell">{{ member.quota }}</td>
                  <td class="numeric-cell">{{ member.credits }}<small v-if="member.pending_review"> · 有调用待核账</small></td>
                  <td class="numeric-cell">{{ member.available }}</td>
                </tr>
              </tbody>
            </table>
          </section>
          <div v-else class="empty">该项目本月暂无成员用量</div>
        </template>
      </LoadingRegion>
    </AppDialog>
  </section>
</template>

<style scoped>
.title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
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

.mentor-manage-toolbar {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 16px;
}

.picker-hint {
  margin: 0 0 12px;
}

.mentor-row + .mentor-row {
  border-top: 1px solid var(--border-subtle, rgba(0, 0, 0, 0.08));
}
</style>
