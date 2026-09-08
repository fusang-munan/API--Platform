<script setup lang="ts">
import { ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import type { Organization, ProjectUsage, UsageSlice } from '../../api/types'
import { APIError } from '../../api/client'
import CreditDonut from '../../components/CreditDonut.vue'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import { useVisibilityPolling } from '../../composables/useVisibilityPolling'

type MemberUsage = ProjectUsage['members'][number]

const rows = ref<ProjectUsage[]>([])
const organizations = ref<Organization[]>([])
const expanded = ref<ProjectUsage | null>(null)
const memberDetail = ref<MemberUsage | null>(null)
const month = ref(new Date().toISOString().slice(0, 7))
const organization = ref('')
const query = ref('')
const error = ref('')

async function load() {
  try {
    const params = new URLSearchParams({
      month: month.value,
      ...(organization.value ? { organization_id: organization.value } : {}),
      ...(query.value ? { q: query.value } : {}),
    })
    ;[rows.value, organizations.value] = await Promise.all([
      teacherAPI.projectSpend(params.toString()),
      teacherAPI.organizations(),
    ])
    error.value = ''
  } catch (caught) {
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
  return Math.max(0, Number(row.quota) - Number(row.charged)).toFixed(3)
}

function segments(row: ProjectUsage) {
  return [
    ...row.models.map(model => ({ name: model.name, value: Number(model.credits) })),
    { name: '未消耗额度', value: Number(remaining(row)), color: '#dfe3ec' },
  ]
}

function initials(name: string) {
  return name.trim().charAt(0).toUpperCase() || '?'
}

function addUsageSlice(map: Map<string, UsageSlice>, model: UsageSlice) {
  const existing = map.get(model.id) ?? { id: model.id, name: model.name, credits: '0.000', calls: 0 }
  const credits = Number(existing.credits) + Number(model.credits)
  existing.credits = credits.toFixed(3)
  existing.calls = (existing.calls ?? 0) + (model.calls ?? 0)
  map.set(model.id, existing)
}

function memberModelRows(member: MemberUsage) {
  const map = new Map<string, UsageSlice>()
  for (const key of member.keys) {
    for (const model of key.models) addUsageSlice(map, model)
  }
  for (const model of member.free_models) addUsageSlice(map, model)
  return [...map.values()].sort((a, b) =>
    Number(b.credits) - Number(a.credits) || (b.calls ?? 0) - (a.calls ?? 0) || a.name.localeCompare(b.name),
  )
}

function closeProjectDialog() {
  expanded.value = null
  memberDetail.value = null
}

function openMemberDetail(member: MemberUsage) {
  memberDetail.value = member
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

    <section class="panel filter-panel">
      <div class="filter-toolbar">
        <label>组织<select v-model="organization"><option value="">全部组织</option><option v-for="item in organizations" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <label>项目名称<input v-model="query" placeholder="输入项目名称" @keyup.enter="load"></label>
        <div class="filter-action">
          <button type="button" class="button primary" @click="load">查询</button>
        </div>
      </div>
    </section>

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
                <span v-if="Number(row.charged) > Number(row.quota)" class="error over-quota">超额 {{ (Number(row.charged) - Number(row.quota)).toFixed(3) }} credits</span>
              </div>
            </td>
            <td><button type="button" class="button ghost" @click="expanded = row">查看详情</button></td>
          </tr>
        </tbody>
      </table>
      <EmptyState v-else title="暂无项目花费数据" />
    </section>

    <AppDialog
      :open="Boolean(expanded)"
      :title="`${expanded?.project_name || ''} · 项目监控`"
      wide
      @update:open="open => !open && closeProjectDialog()"
    >
      <template v-if="expanded">
        <div class="usage-summary usage-dialog-summary dialog-stats">
          <div class="usage-stat">项目月额度<strong>{{ expanded.quota }}</strong></div>
          <div class="usage-stat">已发给学生<strong>{{ expanded.allocated }}</strong></div>
          <div class="usage-stat">可再分配<strong>{{ expanded.available }}</strong></div>
          <div class="usage-stat highlight">本月实际消耗<strong>{{ expanded.charged }}</strong></div>
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
                      <span>已用 <strong>{{ member.credits }}</strong></span>
                      <span>剩余 <strong>{{ member.available }}</strong></span>
                    </div>
                    <div class="quota-progress member-progress" aria-hidden="true">
                      <span :style="{ width: `${usagePercent(member.credits, member.quota)}%` }"></span>
                    </div>
                  </div>
                </div>
                <button type="button" class="button primary member-detail-btn" @click="openMemberDetail(member)">查看详情</button>
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
              <p class="muted">按模型汇总的本月项目级消耗</p>
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
                    <th scope="col" class="numeric-cell">credits</th>
                    <th scope="col" class="numeric-cell">调用次数</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="model in expanded.models" :key="model.id">
                    <th scope="row">{{ model.name }}</th>
                    <td class="numeric-cell">{{ model.credits }}</td>
                    <td class="numeric-cell"><span class="call-pill">{{ model.calls ?? 0 }}</span></td>
                  </tr>
                  <tr v-for="model in expanded.free_models" :key="`free-${model.id}`">
                    <th scope="row">
                      {{ model.name }}
                      <span class="free-tag">0 倍率</span>
                    </th>
                    <td class="numeric-cell">0.000</td>
                    <td class="numeric-cell"><span class="call-pill free">{{ model.calls ?? 0 }}</span></td>
                  </tr>
                  <tr v-if="Number(remaining(expanded)) > 0" class="remaining-row">
                    <th scope="row">未消耗额度</th>
                    <td class="numeric-cell">{{ remaining(expanded) }}</td>
                    <td class="numeric-cell">—</td>
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
      :title="`${memberDetail?.user_name || ''} · 成员用量`"
      wide
      @update:open="open => !open && (memberDetail = null)"
    >
      <template v-if="memberDetail">
        <div class="member-detail-hero">
          <span class="member-avatar member-avatar-large" aria-hidden="true">{{ initials(memberDetail.user_name) }}</span>
          <div class="member-detail-copy">
            <h3>{{ memberDetail.user_name }}</h3>
            <div class="member-hero-stats">
              <span>月额度 <strong>{{ memberDetail.quota }}</strong></span>
              <span>已用 <strong>{{ memberDetail.credits }}</strong></span>
              <span>剩余 <strong>{{ memberDetail.available }}</strong></span>
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
          <table v-if="memberModelRows(memberDetail).length" class="usage-table member-model-table">
            <thead>
              <tr>
                <th scope="col">模型</th>
                <th scope="col" class="numeric-cell">credits</th>
                <th scope="col" class="numeric-cell">调用次数</th>
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
                <span class="muted">已用 {{ key.used }} credits</span>
              </div>
              <table v-if="key.models.length" class="usage-table key-model-table">
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th scope="col" class="numeric-cell">credits</th>
                    <th scope="col" class="numeric-cell">调用次数</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="model in key.models" :key="model.id">
                    <th scope="row">{{ modelDisplayLine(model).title }}</th>
                    <td class="numeric-cell">{{ model.credits }}</td>
                    <td class="numeric-cell">{{ model.calls ?? 0 }}</td>
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
