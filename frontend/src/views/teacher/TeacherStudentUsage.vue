<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import { apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import {
  teacherUsageAPI, usageCompletenessLabel, type TeacherStudentUsageDetail, type TeacherStudentUsagePage, type TeacherUsageSort,
} from '../../api/teacherUsage'
import type { Project } from '../../api/types'
import EmptyState from '../../components/EmptyState.vue'
import { schoolMonth } from '../../utils/format'
import TeacherStudentUsageDetails from './TeacherStudentUsageDetail.vue'

const currentMonth = schoolMonth()
const filters = reactive({ month: currentMonth, project_id: '', search: '', sort: 'credits' as TeacherUsageSort })
let appliedFilters = { ...filters }
const pageSize = 20
const report = ref<TeacherStudentUsagePage>()
const loading = ref(true)
const error = ref('')
const projects = ref<Project[]>([])
const projectsLoading = ref(true)
const projectsError = ref('')
const expanded = ref<string>()
const detail = ref<TeacherStudentUsageDetail>()
const detailLoading = ref(false)
const detailError = ref('')
const lastPage = computed(() => Math.max(1, Math.ceil((report.value?.total ?? 0) / (report.value?.page_size ?? pageSize))))
const projectOptions = computed(() => {
  const names = new Map<string, number>()
  for (const project of projects.value) names.set(project.name, (names.get(project.name) ?? 0) + 1)
  return projects.value.map(project => ({
    id: project.id,
    label: project.name + ((names.get(project.name) ?? 0) > 1 ? ` · ${project.id.slice(-8)}` : '')
      + (project.status === 'inactive' ? '（已停用）' : ''),
  }))
})
const beijingTime = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', timeZone: 'Asia/Shanghai' })

let listRequest: AbortController | undefined
let detailRequest: AbortController | undefined
let requestedPage = 1

async function loadProjects() {
  projectsLoading.value = true
  projectsError.value = ''
  try {
    projects.value = await teacherAPI.projects()
  } catch (caught) {
    projectsError.value = apiErrorMessage(caught, '项目列表加载失败')
  } finally {
    projectsLoading.value = false
  }
}

function closeDetail() {
  detailRequest?.abort()
  expanded.value = undefined
  detail.value = undefined
  detailError.value = ''
  detailLoading.value = false
}

async function loadList(page: number) {
  listRequest?.abort()
  const controller = listRequest = new AbortController()
  closeDetail()
  requestedPage = page
  loading.value = true
  report.value = undefined
  error.value = ''
  try {
    const result = await teacherUsageAPI.students({ ...appliedFilters, page, page_size: pageSize }, controller.signal)
    if (controller.signal.aborted) return
    const availablePage = Math.max(1, Math.ceil(result.total / result.page_size))
    if (page > availablePage) {
      void loadList(availablePage)
      return
    }
    report.value = result
  } catch (caught) {
    if (!controller.signal.aborted) error.value = apiErrorMessage(caught, '学生用量加载失败')
  } finally {
    if (!controller.signal.aborted) loading.value = false
  }
}

function applyFilters() {
  appliedFilters = { ...filters, search: filters.search.trim() }
  void loadList(1)
}

async function loadDetail(userId: string) {
  detailRequest?.abort()
  const controller = detailRequest = new AbortController()
  expanded.value = userId
  detail.value = undefined
  detailLoading.value = true
  detailError.value = ''
  try {
    const result = await teacherUsageAPI.student(userId, appliedFilters, controller.signal)
    if (!controller.signal.aborted) detail.value = result
  } catch (caught) {
    if (!controller.signal.aborted) detailError.value = apiErrorMessage(caught, '学生用量明细加载失败')
  } finally {
    if (!controller.signal.aborted) detailLoading.value = false
  }
}

function toggleDetail(userId: string) {
  if (expanded.value === userId) closeDetail()
  else void loadDetail(userId)
}

onMounted(() => {
  void loadProjects()
  void loadList(1)
})
onBeforeUnmount(() => {
  listRequest?.abort()
  detailRequest?.abort()
})
</script>

<template>
  <section class="page student-usage-report" aria-label="学生用量报表">
    <p class="muted scope-note">
      按北京时间账月及调用时的学生归属统计。调用次数包含已结算扣费和零额调用，待核单列。Token 显示已知量及明细完整性。
    </p>
    <form class="panel usage-filters" @submit.prevent="applyFilters">
      <label>账月 <input v-model="filters.month" type="month" :max="currentMonth" required /></label>
      <label>
        项目
        <select v-model="filters.project_id" :disabled="projectsLoading">
          <option value="">全部项目</option>
          <option v-for="project in projectOptions" :key="project.id" :value="project.id">{{ project.label }}</option>
        </select>
      </label>
      <label>搜索学生 <input v-model="filters.search" type="search" maxlength="128" placeholder="姓名或邮箱" /></label>
      <label>
        排序
        <select v-model="filters.sort">
          <option value="credits">已扣积分最多</option>
          <option value="calls">调用次数最多</option>
          <option value="tokens">Token 最多</option>
        </select>
      </label>
      <button class="button primary" type="submit">查询</button>
      <div v-if="projectsError" class="filter-error error" role="alert">
        {{ projectsError }} <button class="button ghost" type="button" @click="loadProjects">重试项目列表</button>
      </div>
    </form>

    <section class="panel report-panel" aria-label="学生列表" :aria-busy="loading">
      <p v-if="loading" class="muted report-message" role="status">正在加载学生用量…</p>
      <div v-else-if="error" class="report-message" role="alert">
        <p class="error">{{ error }}</p>
        <button class="button ghost" type="button" @click="loadList(requestedPage)">重试加载</button>
      </div>
      <template v-else-if="report">
        <EmptyState v-if="!report.items.length" title="所选范围内没有学生用量" />
        <template v-else>
          <table class="usage-table" aria-label="按学生汇总">
            <thead>
              <tr>
                <th scope="col">学生</th>
                <th scope="col">项目 · Key 数</th>
                <th scope="col">已扣积分</th>
                <th scope="col">调用次数</th>
                <th scope="col">Token</th>
                <th scope="col">待核</th>
                <th scope="col">最近调用</th>
              </tr>
            </thead>
            <tbody v-for="student in report.items" :key="student.user_id">
              <tr class="student-row" :class="{ selected: expanded === student.user_id }" @click="toggleDetail(student.user_id)">
                <th scope="row">
                  <button class="student-button" type="button" :aria-expanded="expanded === student.user_id"
                    :aria-controls="expanded === student.user_id ? `details-${student.user_id}` : undefined"
                    @click.stop="toggleDetail(student.user_id)">
                    <ChevronRight :size="16" aria-hidden="true" />
                    <span>
                      <strong>{{ student.user_name || student.user_id }}</strong>
                      <small v-if="student.user_email" class="muted">{{ student.user_email }}</small>
                    </span>
                  </button>
                </th>
                <td><span class="mobile-label" aria-hidden="true">项目 · Key 数</span>{{ student.project_count }} · {{ student.key_count }}</td>
                <td><span class="mobile-label" aria-hidden="true">已扣积分</span><strong>{{ student.credits }}</strong></td>
                <td>
                  <span class="mobile-label" aria-hidden="true">调用次数</span>{{ student.calls }}
                  <small class="muted">扣费 {{ student.calls - student.zero_cost_calls }} · 零额 {{ student.zero_cost_calls }}</small>
                </td>
                <td>
                  <span class="mobile-label" aria-hidden="true">Token</span>{{ student.total_tokens ?? '—' }}
                  <small class="muted">{{ usageCompletenessLabel[student.completeness] }}</small>
                </td>
                <td>
                  <span class="mobile-label" aria-hidden="true">待核</span>{{ student.pending_review_calls }} 笔
                  <small class="muted">{{ student.pending_review_credits }} 积分</small>
                </td>
                <td>
                  <span class="mobile-label" aria-hidden="true">最近调用</span>
                  <time v-if="student.last_call_at" :datetime="student.last_call_at">{{ beijingTime.format(new Date(student.last_call_at)) }}</time>
                  <span v-else>—</span>
                </td>
              </tr>
              <tr v-if="expanded === student.user_id" :id="`details-${student.user_id}`" class="detail-row">
                <td colspan="7">
                  <p v-if="detailLoading" class="muted" role="status">正在加载项目、Key 和模型明细…</p>
                  <div v-else-if="detailError" role="alert">
                    <p class="error">{{ detailError }}</p>
                    <button class="button ghost" type="button" @click="loadDetail(student.user_id)">重试明细</button>
                  </div>
                  <TeacherStudentUsageDetails v-else-if="detail?.projects.length" :detail="detail" />
                  <EmptyState v-else title="所选范围内没有用量明细" />
                </td>
              </tr>
            </tbody>
          </table>
          <nav class="usage-pagination" aria-label="学生用量分页">
            <span class="muted">共 {{ report.total }} 位学生 · 第 {{ report.page }} / {{ lastPage }} 页</span>
            <div>
              <button class="button ghost" type="button" :disabled="report.page <= 1" @click="loadList(report.page - 1)">上一页</button>
              <button class="button ghost" type="button" :disabled="report.page >= lastPage" @click="loadList(report.page + 1)">下一页</button>
            </div>
          </nav>
        </template>
      </template>
    </section>

    <section v-if="report" class="panel upstream-summary" aria-labelledby="upstream-summary-title">
      <h2 id="upstream-summary-title">未确认归属 / 校方使用</h2>
      <p class="muted">全校北辰归档流水，按上游发生时间取所选账月。项目和学生筛选仅作用于学生列表。</p>
      <p>{{ report.unattributed_upstream.rows }} 笔已归档未确认流水</p>
      <p v-if="report.unattributed_upstream.debit_rows" class="muted">
        {{ report.unattributed_upstream.debit_rows }} 笔有可确认余额扣款，共 {{ report.unattributed_upstream.balance_debit }} 余额单位
      </p>
    </section>
  </section>
</template>

<style scoped>
.student-usage-report { display: grid; gap: 1rem; min-width: 0; }
.scope-note { margin: 0; }
.usage-filters {
  display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)) auto; align-items: end;
  gap: .75rem; padding: clamp(.8rem, 2vw, 1.25rem);
}
.usage-filters label { min-width: 0; }
.usage-filters .primary { min-height: 2.5rem; }
.filter-error { grid-column: 1 / -1; }
.report-panel { padding: 0; overflow: hidden; }
.report-message { padding: 1rem 1.25rem; }
.usage-table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: .86rem; }
.usage-table th, .usage-table td { padding: .75rem .55rem; text-align: left; vertical-align: top; overflow-wrap: anywhere; }
.usage-table th:first-child, .usage-table td:first-child { padding-inline-start: 1rem; }
.usage-table thead th { font-size: .77rem; background: var(--primary-soft); color: var(--ink-soft); }
.usage-table thead th:first-child { width: 25%; }
.usage-table thead th:nth-child(2), .usage-table thead th:nth-child(4) { width: 11%; }
.usage-table thead th:nth-child(3), .usage-table thead th:nth-child(5) { width: 15%; }
.usage-table thead th:nth-child(6) { width: 10%; }
.usage-table small { display: block; line-height: 1.35; margin-top: .15rem; }
.student-row { border-bottom: 1px solid var(--line); cursor: pointer; font-variant-numeric: tabular-nums; }
.student-row:hover, .student-row.selected { background: var(--primary-soft); }
.student-button {
  display: flex; align-items: flex-start; gap: .35rem; text-align: left; width: 100%;
  padding: 0; border: 0; background: transparent; color: var(--ink); font: inherit;
}
.student-button svg { flex: none; margin-top: .08rem; color: var(--primary-strong); }
.student-button[aria-expanded="true"] svg { transform: rotate(90deg); }
.student-button > span { min-width: 0; white-space: normal; overflow-wrap: anywhere; }
.student-button small { font-weight: 400; }
.detail-row td { background: var(--primary-soft); border-bottom: 1px solid var(--line); padding: .65rem 1rem .9rem; }
.usage-pagination { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: .65rem; padding: .75rem 1rem; }
.usage-pagination div { display: flex; gap: .45rem; }
.usage-pagination button:disabled { opacity: .48; cursor: default; }
.upstream-summary { padding: clamp(.8rem, 2vw, 1.25rem); overflow-wrap: anywhere; }
.upstream-summary h2, .upstream-summary p { margin: 0 0 .45rem; }
.upstream-summary p:last-child { margin-bottom: 0; }
.mobile-label { display: none; }
@media (max-width: 1000px) {
  .usage-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .usage-filters .primary { justify-self: start; }
}
@media (max-width: 760px) {
  .usage-table, .usage-table tbody, .usage-table .detail-row, .usage-table .detail-row td { display: block; }
  .usage-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .student-row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .65rem .85rem; padding: .75rem 1rem; }
  .usage-table .student-row > th { grid-column: 1 / -1; padding: 0; }
  .usage-table .student-row > td { padding: 0; }
  .usage-table .student-row > td:nth-child(3), .usage-table .student-row > td:nth-child(5) { font-size: .8rem; }
  .student-button { min-height: 2rem; }
  .mobile-label { display: block; color: var(--ink-soft); font-size: .75rem; font-weight: 700; }
}
</style>
