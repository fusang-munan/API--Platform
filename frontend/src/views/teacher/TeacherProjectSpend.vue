<script setup lang="ts">
import { computed, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import type { Organization, ProjectUsage, UsageSlice } from '../../api/types'
import { APIError } from '../../api/client'
import { beichenBillingExplanation, calculateCredits, quotaBalance, schoolMonth } from '../../utils/format'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'
import { memberModelRows, studentSpend } from '../../utils/teacherSpend'
import BeichenReceiptSpend from './BeichenReceiptSpend.vue'

const rows = ref<ProjectUsage[]>([])
const organizations = ref<Organization[]>([])
const students = computed(() => studentSpend(rows.value))
const projectId = ref('')
const selection = ref<{ projectId: string; userId: string } | null>(null)
const expanded = computed(() => rows.value.find(project => project.project_id === projectId.value))
const memberProject = computed(() => rows.value.find(project => project.project_id === selection.value?.projectId))
const memberDetail = computed(() => memberProject.value?.members.find(member => member.user_id === selection.value?.userId))
const month = ref(schoolMonth())
const organization = ref('')
const query = ref('')
const error = ref('')

let latestRequest = 0
let displayedQuery = ''
async function load() {
  const request = ++latestRequest
  try {
    const params = new URLSearchParams({
      month: month.value,
      ...(organization.value ? { organization_id: organization.value } : {}),
      ...(query.value ? { q: query.value } : {}),
    })
    const queryString = params.toString()
    if (queryString !== displayedQuery) {
      rows.value = []
      closeProjectDialog()
    }
    const [projects, orgs] = await Promise.all([
      teacherAPI.projectSpend(queryString),
      teacherAPI.organizations(),
    ])
    if (request !== latestRequest) return
    displayedQuery = queryString
    rows.value = projects
    organizations.value = orgs
    error.value = ''
  } catch (caught) {
    if (request !== latestRequest) return
    error.value = caught instanceof APIError ? caught.message : '加载项目花费失败'
  }
}

useVisibilityPolling(load)

function rateLabel(row: ProjectUsage) {
  if (Number(row.quota) === 0) return 'N/A'
  return `${(Number(row.charged) / Number(row.quota) * 100).toFixed(1)}%`
}

function usagePercent(used: string, quota: string) {
  const total = Number(quota)
  if (total <= 0) return 0
  return Math.min(100, (Number(used) / total) * 100)
}

function remaining(row: ProjectUsage) {
  return calculateCredits(row.quota, row.charged, 'subtract')
}

function segments(row: ProjectUsage) {
  const unspent = remaining(row)
  return [
    ...row.models.map(model => ({ name: model.name, value: Number(model.credits), displayValue: model.credits })),
    { name: '未消耗额度', value: Number(unspent), displayValue: unspent, color: '#dfe3ec' },
  ]
}

function initials(name: string) {
  return name.trim().charAt(0).toUpperCase() || '?'
}

function closeProjectDialog() {
  projectId.value = ''
  selection.value = null
}

function openMemberDetail(projectId: string, userId: string) {
  selection.value = { projectId, userId }
}

function modelDisplayLine(model: UsageSlice) {
  const title = model.name?.trim() || model.id
  const subtitle = title !== model.id ? model.id : ''
  return { title, subtitle }
}
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">TEACHER</p>
        <h1>项目监控</h1>
        <p class="muted page-lead">查看项目级消耗与各成员模型调用明细</p>
      </div>
      <div class="heading-actions">
        <label>月份<input v-model="month" type="month" @change="load"></label>
        <button type="button" class="button secondary" @click="load">刷新</button>
      </div>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <p class="muted">{{ beichenBillingExplanation }}</p>

    <section class="panel filter-panel">
      <div class="filter-toolbar">
        <label>组织<select v-model="organization"><option value="">全部组织</option><option v-for="item in organizations" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label>项目名称<input v-model="query" placeholder="输入项目名称" @keyup.enter="load"></label>
        <div class="filter-action">
          <button type="button" class="button primary" @click="load">查询</button>
        </div>
      </div>
    </section>

    <details v-if="students.length" class="panel student-summary">
      <summary>学生月度开销（{{ students.length }} 人）</summary>
      <p class="muted">汇总当前查询范围内各项目的校内积分。北辰实扣仅合计已匹配的独立回执，待核笔数另列。</p>
      <div class="model-table-wrap">
        <table class="usage-table">
          <thead><tr><th scope="col">学生</th><th scope="col">校内积分合计</th><th scope="col">北辰回执小计</th><th scope="col">分项目明细</th></tr></thead>
          <tbody>
            <tr v-for="student in students" :key="student.id">
              <th scope="row">{{ student.name }}<small class="muted model-id">{{ student.id }}</small></th>
              <td>{{ student.credits }}</td>
              <td><BeichenReceiptSpend :spend="student" /></td>
              <td><button v-for="entry in student.entries" :key="entry.project.project_id" type="button" class="button ghost"
                @click="openMemberDetail(entry.project.project_id, student.id)">{{ entry.project.project_name }} · {{ entry.member.credits }} 积分</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </details>

    <section class="panel">
      <table v-if="rows.length" class="usage-table project-spend-table">
        <caption class="table-caption"><strong>项目级花费</strong></caption>
        <thead>
          <tr>
            <th scope="col">项目</th>
            <th scope="col">实际开销</th>
            <th scope="col">已用 / 总额度</th>
            <th scope="col">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.project_id">
            <th scope="row">
              <span class="project-spend-name">
                <strong>{{ row.project_name }}</strong>
                <small class="muted">剩余 {{ remaining(row) }} credits</small>
                <small v-if="row.pending_review" class="muted">有调用待核账</small>
              </span>
            </th>
            <td class="project-spend-chart">
              <CreditDonut mini :segments="segments(row)" :total="Number(row.quota)" :label="`${row.project_name} 实际开销概览`" />
            </td>
            <td>
              <div class="project-spend-usage">
                <strong>{{ row.charged }} / {{ row.quota }}</strong>
                <div class="quota-progress" aria-hidden="true">
                  <span :style="{ width: `${usagePercent(row.charged, row.quota)}%` }"></span>
                </div>
                <span class="muted">{{ rateLabel(row) }} 已用</span>
                <span v-if="calculateCredits(row.charged, row.quota, 'subtract') !== '0.000'" class="error over-quota">超额 {{ calculateCredits(row.charged, row.quota, 'subtract') }} credits</span>
              </div>
            </td>
            <td><button type="button" class="button ghost" @click="projectId = row.project_id">查看详情</button></td>
          </tr>
        </tbody>
      </table>
      <EmptyState v-else title="暂无项目花费数据" />
    </section>

    <AppDialog
      :open="Boolean(expanded)"
      :title="`${expanded?.project_name || ''} · 项目监控`"
      description="查看本月成员开销、模型用量与北辰回执。"
      wide
      @update:open="open => !open && closeProjectDialog()"
    >
      <template v-if="expanded">
        <div class="usage-summary usage-dialog-summary dialog-stats">
          <div class="usage-stat">项目月额度<strong>{{ expanded.quota }}</strong></div>
          <div class="usage-stat">已发给学生<strong>{{ expanded.allocated }}</strong></div>
          <div class="usage-stat">可再分配<strong>{{ expanded.available }}</strong></div>
          <div class="usage-stat highlight">本月实际消耗<strong>{{ expanded.charged }}</strong><small v-if="expanded.pending_review">有调用待核账</small></div>
        </div>

        <section class="dialog-section">
          <div class="section-heading">
            <div>
              <h2>成员用量</h2>
              <p class="muted">点击成员查看各模型调用明细</p>
            </div>
            <span class="section-badge">{{ expanded.members.length }} 人</span>
          </div>
          <div v-if="expanded.members.length" class="spend-member-grid">
            <article v-for="member in expanded.members" :key="member.user_id" class="spend-member-card">
              <div class="member-card-top">
                <div class="spend-member-identity">
                  <span class="member-avatar" aria-hidden="true">{{ initials(member.user_name) }}</span>
                  <div class="member-card-copy">
                    <h3>{{ member.user_name }}</h3>
                    <div class="member-metrics">
                      <span>月额度 <strong>{{ member.quota }}</strong></span>
                      <span>已用 <strong>{{ member.credits }}</strong><template v-if="member.pending_review"> · 有调用待核账</template></span>
                      <span>{{ quotaBalance(member.quota, member.credits, member.available).label }} <strong>{{ quotaBalance(member.quota, member.credits, member.available).amount }}</strong></span>
                      <BeichenReceiptSpend :spend="member" />
                    </div>
                    <div class="quota-progress member-progress" aria-hidden="true">
                      <span :style="{ width: `${usagePercent(member.credits, member.quota)}%` }"></span>
                    </div>
                  </div>
                </div>
                <button type="button" class="button primary member-detail-btn" @click="openMemberDetail(expanded.project_id, member.user_id)">查看详情</button>
              </div>
              <div v-if="memberModelRows(member).length" class="model-chip-list">
                <span v-for="model in memberModelRows(member).slice(0, 4)" :key="model.id" class="model-chip">
                  {{ model.name }} · {{ model.calls ?? 0 }} 次
                </span>
                <span v-if="memberModelRows(member).length > 4" class="model-chip muted-chip">+{{ memberModelRows(member).length - 4 }}</span>
              </div>
            </article>
          </div>
          <EmptyState v-else title="暂无成员用量" description="该项目本月还没有学生活跃用量" />
        </section>

        <section class="dialog-section project-total-section">
          <div class="section-heading">
            <div>
              <h2>项目总用量</h2>
              <p class="muted">校内积分按月汇总。北辰实扣仅计已匹配回执，待核笔数另列。</p>
            </div>
          </div>
          <div class="project-total-layout">
            <div class="usage-chart-card project-total-chart">
              <CreditDonut
                :segments="segments(expanded)"
                :total="Number(expanded.quota)"
                :label="`${expanded.project_name} 按模型拆分的实际开销`"
              />
            </div>
            <div v-if="expanded.models.length || expanded.free_models.length" class="model-table-wrap">
              <table class="usage-table project-model-table">
                <caption class="table-caption"><strong>项目模型用量明细</strong></caption>
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th scope="col" class="numeric-cell">学校已用积分</th>
                    <th scope="col" class="numeric-cell">调用次数</th>
                    <th scope="col" class="numeric-cell">北辰回执</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="model in expanded.models" :key="model.id">
                    <th scope="row">{{ model.name }}</th>
                    <td class="numeric-cell">{{ model.credits }}</td>
                    <td class="numeric-cell"><span class="call-pill">{{ model.calls ?? 0 }}</span></td>
                    <td class="numeric-cell"><BeichenReceiptSpend :spend="model" /></td>
                  </tr>
                  <tr v-for="model in expanded.free_models" :key="`free-${model.id}`">
                    <th scope="row">
                      {{ model.name }}
                      <span class="free-tag">0 倍率</span>
                    </th>
                    <td class="numeric-cell">0.000</td>
                    <td class="numeric-cell"><span class="call-pill free">{{ model.calls ?? 0 }}</span></td>
                    <td class="numeric-cell"><BeichenReceiptSpend :spend="model" /></td>
                  </tr>
                  <tr v-if="Number(remaining(expanded)) > 0" class="remaining-row">
                    <th scope="row">未消耗额度</th>
                    <td class="numeric-cell">{{ remaining(expanded) }}</td>
                    <td class="numeric-cell" colspan="2">—</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <EmptyState v-else class="project-empty-side" title="暂无项目模型用量" />
          </div>
        </section>
      </template>
    </AppDialog>

    <AppDialog
      :open="Boolean(memberDetail)"
      :title="`${memberDetail?.user_name || ''} · ${memberProject?.project_name || ''} · 成员用量`"
      description="查看该学生在本项目的积分、北辰回执和各 Key 的模型明细。"
      wide
      @update:open="open => !open && (selection = null)"
    >
      <template v-if="memberDetail">
        <div class="member-detail-hero">
          <span class="member-avatar member-avatar-large" aria-hidden="true">{{ initials(memberDetail.user_name) }}</span>
          <div class="member-detail-copy">
            <h3>{{ memberDetail.user_name }}</h3>
            <div class="member-hero-stats">
              <span>月额度 <strong>{{ memberDetail.quota }}</strong></span>
              <span>已用 <strong>{{ memberDetail.credits }}</strong><template v-if="memberDetail.pending_review"> · 有调用待核账</template></span>
              <span>{{ quotaBalance(memberDetail.quota, memberDetail.credits, memberDetail.available).label }} <strong>{{ quotaBalance(memberDetail.quota, memberDetail.credits, memberDetail.available).amount }}</strong></span>
              <BeichenReceiptSpend :spend="memberDetail" />
              <span>{{ memberModelRows(memberDetail).length }} 个模型</span>
            </div>
            <div class="quota-progress member-progress" aria-hidden="true">
              <span :style="{ width: `${usagePercent(memberDetail.credits, memberDetail.quota)}%` }"></span>
            </div>
          </div>
        </div>

        <section class="dialog-section member-detail-section">
          <div class="section-heading compact">
            <h2>模型调用明细</h2>
          </div>
          <p class="muted">北辰实扣仅计已匹配回执，按北辰余额单位显示。</p>
          <table v-if="memberModelRows(memberDetail).length" class="usage-table member-model-table">
            <thead>
              <tr>
                <th scope="col">模型</th>
                <th scope="col" class="numeric-cell">学校已用积分</th>
                <th scope="col" class="numeric-cell">调用次数</th>
                <th scope="col" class="numeric-cell">北辰回执</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="model in memberModelRows(memberDetail)" :key="model.id">
                <th scope="row">
                  <span class="model-name">{{ modelDisplayLine(model).title }}</span>
                  <span v-if="modelDisplayLine(model).subtitle" class="muted model-id">{{ modelDisplayLine(model).subtitle }}</span>
                </th>
                <td class="numeric-cell">{{ model.credits }}</td>
                <td class="numeric-cell">{{ model.calls ?? 0 }}</td>
                <td class="numeric-cell"><BeichenReceiptSpend :spend="model" /></td>
              </tr>
            </tbody>
          </table>
          <EmptyState v-else title="暂无模型调用" description="该成员本月还没有产生模型调用记录" />
        </section>

        <section v-if="memberDetail.keys.length" class="dialog-section member-detail-section">
          <div class="section-heading compact">
            <h2>按 Key 拆分</h2>
          </div>
          <div class="member-key-blocks">
            <article v-for="key in memberDetail.keys" :key="key.id" class="member-key-block">
              <div class="member-key-head">
                <strong>{{ key.name }}</strong>
                <span class="muted">已用 {{ key.used }} credits<template v-if="key.pending_review"> · 有调用待核账</template></span>
              </div>
              <table v-if="key.models.length" class="usage-table key-model-table">
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th scope="col" class="numeric-cell">学校已用积分</th>
                    <th scope="col" class="numeric-cell">调用次数</th>
                    <th scope="col" class="numeric-cell">北辰回执</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="model in key.models" :key="model.id">
                    <th scope="row">{{ modelDisplayLine(model).title }}</th>
                    <td class="numeric-cell">{{ model.credits }}</td>
                    <td class="numeric-cell">{{ model.calls ?? 0 }}</td>
                    <td class="numeric-cell"><BeichenReceiptSpend :spend="model" /></td>
                  </tr>
                </tbody>
              </table>
              <p v-else class="muted key-empty">暂无模型调用</p>
            </article>
          </div>
        </section>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.student-summary summary { cursor: pointer; font-weight: 700; }

.page-lead {
  margin: 0;
}

.heading-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 10px;
}

.filter-panel {
  padding-bottom: 22px;
}

.filter-toolbar {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) minmax(180px, 1fr) auto;
  gap: 12px;
  align-items: end;
}

.filter-action {
  display: flex;
  align-items: flex-end;
}

.filter-action .button {
  min-width: 88px;
}

.over-quota {
  display: block;
  margin-top: 4px;
  font-size: 12px;
}

.dialog-stats .usage-stat.highlight {
  border-color: rgba(108, 76, 232, 0.18);
  background: rgba(108, 76, 232, 0.06);
}

.dialog-stats .usage-stat.highlight strong {
  color: #5d42c7;
}

.dialog-section {
  margin-top: 24px;
  padding: 18px;
  border: 1px solid rgba(83, 99, 136, 0.12);
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.72);
}

.project-total-section {
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.96), rgba(247, 248, 253, 0.88));
}

.section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.section-heading h2 {
  margin: 0 0 4px;
  font-size: 16px;
}

.section-heading p {
  margin: 0;
  font-size: 12px;
}

.section-badge {
  flex-shrink: 0;
  padding: 6px 10px;
  border-radius: 999px;
  background: rgba(108, 76, 232, 0.08);
  color: #6347ce;
  font-size: 12px;
  font-weight: 700;
}

.spend-member-grid {
  display: grid;
  gap: 14px;
}

.spend-member-card {
  display: grid;
  gap: 0;
  padding: 16px;
  border: 1px solid rgba(83, 99, 136, 0.12);
  border-radius: 14px;
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.96), rgba(247, 248, 253, 0.88));
  box-shadow: 0 8px 22px rgba(29, 38, 73, 0.04);
}

.member-card-top {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.spend-member-identity {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  flex: 1;
  min-width: 0;
}

.spend-member-card .member-avatar {
  flex: 0 0 42px;
}

.member-card-copy {
  flex: 1;
  min-width: 0;
}

.member-card-copy h3 {
  margin: 2px 0 8px;
  font-size: 16px;
}

.member-metrics {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 14px;
  color: var(--ink-soft);
  font-size: 12px;
}

.member-metrics strong {
  color: var(--ink);
}

.member-progress {
  margin-top: 10px;
}

.member-detail-btn {
  flex-shrink: 0;
}

.model-chip-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px solid rgba(83, 99, 136, 0.08);
}

.model-chip-list.compact {
  margin-top: 10px;
  padding-top: 0;
  border-top: 0;
}

.model-chip {
  display: inline-flex;
  align-items: center;
  padding: 6px 10px;
  border-radius: 999px;
  background: rgba(108, 76, 232, 0.08);
  color: #5d42c7;
  font-size: 11px;
  font-weight: 650;
  line-height: 1.2;
}

.model-chip.muted-chip {
  background: rgba(83, 99, 136, 0.08);
  color: var(--ink-soft);
}

.project-total-layout {
  display: grid;
  grid-template-columns: 1fr;
  gap: 18px;
}

.project-total-chart {
  margin: 0;
  overflow: hidden;
}

.project-total-chart :deep(.credit-chart) {
  grid-template-columns: minmax(150px, 190px) minmax(0, 1fr);
  align-items: center;
  width: 100%;
}

.project-total-chart :deep(.credit-donut) {
  width: 190px;
  height: 190px;
  min-width: 150px;
}

.project-total-chart :deep(.credit-legend) {
  min-width: 0;
}

.model-table-wrap {
  min-width: 0;
  overflow-x: auto;
  padding: 14px;
  border: 1px solid rgba(83, 99, 136, 0.1);
  border-radius: 14px;
  background: rgba(255, 255, 255, 0.82);
}

.project-model-table {
  margin: 0;
}

.call-pill {
  display: inline-flex;
  min-width: 34px;
  justify-content: center;
  padding: 4px 8px;
  border-radius: 999px;
  background: rgba(108, 76, 232, 0.1);
  color: #5d42c7;
  font-size: 12px;
  font-weight: 700;
}

.call-pill.free {
  background: rgba(154, 106, 23, 0.1);
  color: #9a6a17;
}

.free-tag {
  display: inline-flex;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  background: rgba(154, 106, 23, 0.1);
  color: #9a6a17;
  font-size: 10px;
  font-weight: 700;
  vertical-align: middle;
}

.remaining-row th,
.remaining-row td {
  color: var(--ink-soft);
}

.member-detail-hero {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  margin-bottom: 18px;
  padding: 16px;
  border: 1px solid rgba(83, 99, 136, 0.12);
  border-radius: 14px;
  background: rgba(249, 250, 253, 0.88);
}

.member-avatar-large {
  flex: 0 0 48px;
  width: 48px;
  height: 48px;
  font-size: 18px;
}

.member-detail-copy {
  flex: 1;
  min-width: 0;
}

.member-detail-copy h3 {
  margin: 0 0 8px;
  font-size: 18px;
}

.member-hero-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  margin-bottom: 10px;
  color: var(--ink-soft);
  font-size: 13px;
}

.member-hero-stats strong {
  color: var(--ink);
}

.member-detail-section {
  margin-top: 16px;
}

.section-heading.compact {
  margin-bottom: 12px;
}

.section-heading.compact h2 {
  margin: 0;
}

.member-model-table th[scope='row'] {
  font-weight: 600;
}

.model-name {
  display: block;
}

.model-id {
  display: block;
  margin-top: 2px;
  font-size: 12px;
}

.member-key-blocks {
  display: grid;
  gap: 12px;
}

.member-key-block {
  padding: 14px;
  border: 1px solid rgba(83, 99, 136, 0.1);
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.82);
}

.member-key-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
}

.key-model-table {
  margin: 0;
}

.key-empty {
  margin: 0;
  font-size: 12px;
}

.project-empty-side {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 220px;
  padding: 14px;
  border: 1px dashed rgba(83, 99, 136, 0.18);
  border-radius: 14px;
}

@media (max-width: 900px) {
  .filter-toolbar {
    grid-template-columns: 1fr;
  }

  .filter-action .button {
    width: 100%;
  }

  .member-card-top {
    flex-direction: column;
  }

  .member-detail-btn {
    width: 100%;
  }

  .project-total-chart :deep(.credit-chart) {
    grid-template-columns: 1fr;
    justify-items: center;
  }

  .project-total-layout {
    grid-template-columns: 1fr;
  }
}
</style>
