<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { teacherAPI } from '../../api/teacher'
import type { Organization, Project, ProjectBudget } from '../../api/types'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import EmptyState from '../../components/EmptyState.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'

const orgs = ref<Organization[]>([])
const projects = ref<Project[]>([])
const budgets = ref<ProjectBudget[]>([])
const month = ref(new Date().toISOString().slice(0, 7))
const drawer = ref(false)
const editing = ref<Organization | null>(null)
const expandedOrg = ref<string | null>(null)
const inviteOpen = ref(false)
const inviteEmail = ref('')
const error = ref('')
const toast = useToast()
const loadState = useLoadState()
const form = reactive({ name: '', description: '', status: 'active' as 'active' | 'inactive' })

const projectsByOrg = computed(() => {
  const map = new Map<string, Project[]>()
  for (const project of projects.value) {
    const list = map.get(project.organization_id) ?? []
    list.push(project)
    map.set(project.organization_id, list)
  }
  for (const list of map.values()) {
    list.sort((a, b) => a.name.localeCompare(b.name))
  }
  return map
})

const budgetsById = computed(() => new Map(budgets.value.map(row => [row.id, row])))

function orgProjects(orgId: string) {
  return projectsByOrg.value.get(orgId) ?? []
}

function orgSummary(orgId: string) {
  const list = orgProjects(orgId)
  return {
    total: list.length,
    active: list.filter(project => project.status === 'active').length,
    withMentor: list.filter(project => project.has_mentor).length,
  }
}

function toggleProjects(orgId: string) {
  expandedOrg.value = expandedOrg.value === orgId ? null : orgId
}

async function load() {
  await loadState.run(async () => {
    try {
      const params = new URLSearchParams({ month: month.value })
      ;[orgs.value, projects.value, budgets.value] = await Promise.all([
        teacherAPI.organizations(),
        teacherAPI.projects(),
        teacherAPI.projectBudgets(params.toString()),
      ])
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function open(row?: Organization) {
  editing.value = row ?? null
  Object.assign(form, { name: row?.name ?? '', description: row?.description ?? '', status: row?.status ?? 'active' })
  drawer.value = true
}

async function save() {
  try {
    if (editing.value) await teacherAPI.updateOrganization(editing.value.id, form)
    else await teacherAPI.createOrganization(form)
    toast.success(editing.value ? '组织已更新' : '组织已创建')
    await load()
    if (!editing.value) drawer.value = false
  } catch (e) {
    error.value = msg(e)
  }
}

async function toggle(row: Organization) {
  if (row.status === 'active' && !confirm('停用组织将阻止新的学生申请，是否继续？')) return
  try {
    await teacherAPI.updateOrganization(row.id, { status: row.status === 'active' ? 'inactive' : 'active' })
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function invite() {
  try {
    await teacherAPI.invite(inviteEmail.value)
    toast.success('老师邀请已发送')
    inviteOpen.value = false
    inviteEmail.value = ''
  } catch (e) {
    error.value = msg(e)
  }
}

function budgetLine(project: Project) {
  const stats = budgetsById.value.get(project.id)
  if (!stats) return `月额度 ${project.monthly_credit_quota}`
  return `月额度 ${stats.quota} · 已分配 ${stats.allocated} · 可再分配 ${stats.available} · 已消耗 ${stats.charged}`
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
        <h1>组织管理</h1>
        <p class="muted page-lead">维护组织及其下属项目、导师配置</p>
      </div>
      <div class="actions">
        <button type="button" class="button secondary" @click="load">刷新</button>
        <button type="button" class="button secondary" @click="inviteOpen = true">邀请老师</button>
        <button type="button" class="button primary" @click="open()">添加组织</button>
      </div>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="list" label="正在加载组织">
      <section class="panel org-panel">
        <article v-for="row in orgs" :key="row.id" class="org-card" :class="{ expanded: expandedOrg === row.id }">
          <div class="org-card-header">
            <div class="org-main">
              <div class="org-title-row">
                <strong>{{ row.name }}</strong>
                <StatusBadge :status="row.status" />
              </div>
              <p class="muted org-description">{{ row.description || '暂无说明' }}</p>
              <div class="org-meta">
                <span>{{ orgSummary(row.id).total }} 个项目</span>
                <span>{{ orgSummary(row.id).active }} 个启用</span>
                <span>{{ orgSummary(row.id).withMentor }} 个已配导师</span>
              </div>
            </div>
            <div class="actions">
              <button type="button" class="button ghost org-expand" @click="toggleProjects(row.id)">
                <span>{{ expandedOrg === row.id ? '收起项目' : '查看项目与导师' }}</span>
                <ChevronDown :size="16" class="org-expand-icon" :class="{ open: expandedOrg === row.id }" aria-hidden="true" />
              </button>
              <button type="button" class="button ghost" @click="open(row)">编辑</button>
              <button type="button" class="button" :class="row.status === 'active' ? 'danger' : 'secondary'" @click="toggle(row)">
                {{ row.status === 'active' ? '停用' : '启用' }}
              </button>
            </div>
          </div>

          <div v-if="expandedOrg === row.id" class="org-project-panel">
            <div class="org-project-summary">
              <div class="usage-stat">
                项目总数
                <strong>{{ orgSummary(row.id).total }}</strong>
              </div>
              <div class="usage-stat">
                启用中
                <strong>{{ orgSummary(row.id).active }}</strong>
              </div>
              <div class="usage-stat">
                已配导师
                <strong>{{ orgSummary(row.id).withMentor }}</strong>
              </div>
            </div>

            <div v-if="orgProjects(row.id).length" class="org-project-grid">
              <article v-for="project in orgProjects(row.id)" :key="project.id" class="org-project-card">
                <div class="org-project-card-head">
                  <div>
                    <strong>{{ project.name }}</strong>
                    <p v-if="project.description" class="muted">{{ project.description }}</p>
                    <p v-else class="muted">暂无项目说明</p>
                  </div>
                  <StatusBadge :status="project.status" />
                </div>
                <p class="muted org-project-budget">{{ budgetLine(project) }}</p>
                <div class="org-mentor-block">
                  <span class="org-mentor-label">负责导师</span>
                  <div v-if="project.mentors?.length" class="org-mentor-list">
                    <div v-for="mentor in project.mentors" :key="mentor.id" class="org-mentor-chip">
                      <strong>{{ mentor.name }}</strong>
                      <span class="muted">{{ mentor.email }}</span>
                    </div>
                  </div>
                  <p v-else class="mentor-empty">暂无导师，学生申请可能卡在导师审核</p>
                </div>
              </article>
            </div>
            <EmptyState v-else title="该组织暂无项目" description="可在项目管理中创建并关联到此组织" />
          </div>
        </article>
      </section>
    </LoadingRegion>
    <AppDialog :open="drawer" :title="editing ? '编辑组织' : '创建组织'" @update:open="drawer = $event">
      <form @submit.prevent="save">
        <label>名称<input v-model="form.name" maxlength="128" required /></label>
        <label>说明<textarea v-model="form.description" rows="4" /></label>
        <label>状态<select v-model="form.status"><option value="active">启用</option><option value="inactive">停用</option></select></label>
        <button class="button primary">保存</button>
      </form>
    </AppDialog>
    <AppDialog :open="inviteOpen" title="邀请老师" @update:open="inviteOpen = $event">
      <label>老师邮箱<input v-model="inviteEmail" type="email" required /></label>
      <button class="button primary" @click="invite">发送邀请</button>
    </AppDialog>
  </section>
</template>

<style scoped>
.page-lead {
  margin: 0;
}

.org-panel {
  display: grid;
  gap: 16px;
}

.org-card {
  border: 1px solid var(--line);
  border-radius: 16px;
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.96), rgba(247, 248, 253, 0.88));
  box-shadow: 0 8px 22px rgba(29, 38, 73, 0.04);
  overflow: hidden;
}

.org-card.expanded {
  border-color: rgba(108, 76, 232, 0.18);
  box-shadow: 0 10px 28px rgba(108, 76, 232, 0.08);
}

.org-card-header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 20px;
}

.org-main {
  flex: 1;
  min-width: 0;
}

.org-title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}

.org-title-row strong {
  font-size: 18px;
}

.org-description {
  margin: 0 0 10px;
}

.org-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 14px;
  color: var(--ink-soft);
  font-size: 12px;
}

.org-meta span {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.org-meta span::before {
  content: '';
  width: 5px;
  height: 5px;
  border-radius: 999px;
  background: rgba(108, 76, 232, 0.45);
}

.org-expand {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.org-expand-icon {
  transition: transform var(--motion-state) var(--ease-out);
}

.org-expand-icon.open {
  transform: rotate(180deg);
}

.org-project-panel {
  padding: 0 20px 20px;
  border-top: 1px solid rgba(83, 99, 136, 0.1);
  background: rgba(249, 250, 253, 0.72);
}

.org-project-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  padding: 16px 0 6px;
}

.org-project-summary .usage-stat {
  min-width: 110px;
  margin: 0;
}

.org-project-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 14px;
  padding-top: 10px;
}

.org-project-card {
  padding: 16px;
  border: 1px solid rgba(83, 99, 136, 0.12);
  border-radius: 14px;
  background: rgba(255, 255, 255, 0.88);
}

.org-project-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.org-project-card-head strong {
  display: block;
  font-size: 15px;
  margin-bottom: 4px;
}

.org-project-card-head p {
  margin: 0;
  font-size: 12px;
}

.org-project-budget {
  margin: 0 0 14px;
  font-size: 12px;
  line-height: 1.5;
}

.org-mentor-block {
  display: grid;
  gap: 8px;
}

.org-mentor-label {
  color: var(--ink-soft);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.04em;
}

.org-mentor-list {
  display: grid;
  gap: 8px;
}

.org-mentor-chip {
  display: grid;
  gap: 2px;
  padding: 10px 12px;
  border-radius: 10px;
  background: rgba(108, 76, 232, 0.06);
  border: 1px solid rgba(108, 76, 232, 0.1);
}

.org-mentor-chip strong {
  font-size: 13px;
}

.org-mentor-chip .muted {
  font-size: 12px;
}

.mentor-empty {
  margin: 0;
  padding: 10px 12px;
  border-radius: 10px;
  background: rgba(154, 106, 23, 0.08);
  color: #9a6a17;
  font-size: 12px;
  line-height: 1.5;
}

@media (max-width: 800px) {
  .org-card-header {
    flex-direction: column;
  }

  .org-card-header .actions {
    width: 100%;
    justify-content: flex-start;
    flex-wrap: wrap;
  }
}
</style>
