<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { mentorAPI } from '../../api/mentor'
import type { Application, Organization, Project, ProjectUsage } from '../../api/types'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import CreditDonut, { type CreditSegment } from '../../components/CreditDonut.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'

type MemberUsage = ProjectUsage['members'][number]

const orgs = ref<Organization[]>([])
const groups = ref<Array<{ org: Organization; projects: Project[] }>>([])
const applications = ref<Application[]>([])
const activeProject = ref<Project | null>(null)
const projectUsage = ref<ProjectUsage | null>(null)
const quotaKey = ref<{ id: string; name: string; quota: string } | null>(null)
const quota = ref('')
const month = ref(new Date().toISOString().slice(0, 7))
const error = ref('')
const toast = useToast()
const loadState = useLoadState()
const usageLoadState = useLoadState()

async function load() {
  await loadState.run(async () => {
    try {
      orgs.value = await mentorAPI.organizations()
      groups.value = await Promise.all(orgs.value.map(async org => ({
        org,
        projects: await mentorAPI.projects(org.id),
      })))
      applications.value = await mentorAPI.applications()
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

async function apply(project: Project) {
  try {
    await mentorAPI.apply(project.id)
    toast.success('项目申请已提交')
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function showUsage(project: Project) {
  activeProject.value = project
  await usageLoadState.run(async () => {
    try {
      projectUsage.value = await mentorAPI.usage(project.id, month.value)
      error.value = ''
    } catch (e) {
      error.value = msg(e)
    }
  })
}

async function adjustQuota() {
  if (!quotaKey.value) return
  try {
    await mentorAPI.updateQuota(quotaKey.value.id, quota.value)
    toast.success('学生月额度已调整')
    quotaKey.value = null
    await showUsage(activeProject.value!)
  } catch (e) {
    error.value = msg(e)
  }
}

function firstAdjustableKey(member: MemberUsage) {
  return member.keys.find(key => key.status === 'active' || key.status === 'approved')
}

function allocationSegments(usage: ProjectUsage): CreditSegment[] {
  return [
    ...usage.members.map(member => ({ name: member.user_name, value: Number(member.quota) })).filter(item => item.value > 0),
    { name: '可再分配', value: Number(usage.available), color: '#dfe3ec' },
  ]
}

function spendSegments(usage: ProjectUsage): CreditSegment[] {
  return [
    ...usage.members.map(member => ({ name: member.user_name, value: Number(member.credits) })).filter(item => item.value > 0),
    { name: '未消耗额度', value: Math.max(0, Number(usage.quota) - Number(usage.charged)), color: '#dfe3ec' },
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
        <p class="eyebrow">MENTOR</p>
        <h1>项目管理</h1>
      </div>
      <button class="button secondary" @click="load">刷新</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <LoadingRegion
      :initial-loading="loadState.initialLoading.value"
      :refreshing="loadState.refreshing.value"
      variant="split"
      label="正在加载项目和申请"
    >
      <section v-for="group in groups" :key="group.org.id" class="panel" style="margin-bottom:20px">
        <h2>{{ group.org.name }}</h2>
        <div class="grid two">
          <article v-for="project in group.projects" :key="project.id" class="panel">
            <div class="row">
              <strong>{{ project.name }}</strong>
              <StatusBadge :status="project.status" />
            </div>
            <p>月额度 {{ project.monthly_credit_quota }}</p>
            <button v-if="project.is_responsible" class="button ghost" @click="showUsage(project)">查看额度与用量</button>
            <button
              v-else
              class="button primary"
              :disabled="project.status !== 'active' || project.application_status === 'pending'"
              @click="apply(project)"
            >
              {{ project.application_status === 'pending' ? '审核中' : '申请负责' }}
            </button>
          </article>
        </div>
      </section>
      <section class="panel">
        <h2>申请历史</h2>
        <article v-for="item in applications" :key="item.id" class="list-row">
          <strong>{{ item.project.name }}</strong>
          <StatusBadge :status="item.status" />
        </article>
      </section>
    </LoadingRegion>
  </section>

  <AppDialog
    :open="Boolean(activeProject)"
    :title="`${activeProject?.name || ''} · 额度与用量`"
    wide
    @update:open="activeProject = $event ? activeProject : null"
  >
    <div class="row">
      <label>月份<input v-model="month" type="month" @change="activeProject && showUsage(activeProject)"></label>
    </div>
    <LoadingRegion
      :initial-loading="usageLoadState.initialLoading.value"
      :refreshing="usageLoadState.refreshing.value"
      variant="list"
      label="正在加载项目用量"
    >
      <template v-if="projectUsage">
        <div class="usage-summary usage-dialog-summary">
          <div class="usage-stat">项目月额度<strong>{{ projectUsage.quota }}</strong></div>
          <div class="usage-stat">已发给学生<strong>{{ projectUsage.allocated }}</strong></div>
          <div class="usage-stat">可再分配<strong>{{ projectUsage.available }}</strong></div>
          <div class="usage-stat">本月实际消耗<strong>{{ projectUsage.charged }}</strong></div>
        </div>
        <div class="grid two">
          <section class="usage-chart-card">
            <h2>学生额度</h2>
            <CreditDonut
              :segments="allocationSegments(projectUsage)"
              :total="Number(projectUsage.quota)"
              :label="`${activeProject?.name || ''} 学生额度`"
            />
          </section>
          <section class="usage-chart-card">
            <h2>实际开销</h2>
            <CreditDonut
              :segments="spendSegments(projectUsage)"
              :total="Number(projectUsage.quota)"
              :label="`${activeProject?.name || ''} 实际开销`"
            />
          </section>
        </div>
        <section class="member-usage-section">
          <div class="member-usage-heading">
            <h2>成员用量</h2>
          </div>
          <div class="member-usage-cards">
            <article
              v-for="member in projectUsage.members"
              :key="member.user_id"
              class="member-usage-card member-usage-card--compact"
            >
              <header class="member-card-identity">
                <span class="member-avatar" aria-hidden="true">{{ member.user_name.slice(0, 1).toUpperCase() }}</span>
                <div>
                  <h3>{{ member.user_name }}</h3>
                  <p>月额度 {{ member.quota }} · 已用 {{ member.credits }} · 剩余 {{ member.available }}</p>
                </div>
                <button
                  v-if="firstAdjustableKey(member)"
                  class="button ghost"
                  @click="quotaKey = { id: firstAdjustableKey(member)!.id, name: member.user_name, quota: member.quota }; quota = member.quota"
                >
                  调整月额度
                </button>
              </header>
              <div v-if="member.keys.length" class="member-key-stack">
                <article v-for="key in member.keys" :key="key.id" class="member-key-row">
                  <div class="member-key-label">
                    <strong>{{ key.name }}</strong>
                    <StatusBadge :status="key.status" />
                    <small>已用 {{ key.used }} credits</small>
                  </div>
                </article>
              </div>
            </article>
          </div>
        </section>
      </template>
    </LoadingRegion>
  </AppDialog>

  <AppDialog
    :open="Boolean(quotaKey)"
    :title="`调整 ${quotaKey?.name || ''} 月额度`"
    @update:open="quotaKey = $event ? quotaKey : null"
  >
    <label>新月额度（credits）<input v-model="quota" inputmode="decimal"></label>
    <button class="button primary" @click="adjustQuota">确认调额</button>
  </AppDialog>
</template>
