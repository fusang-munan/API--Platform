<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import type { StudentAccount } from '../../api/types'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import LoadingButton from '../../components/LoadingButton.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { fullTime } from '../../utils/format'

const students = ref<StudentAccount[]>([])
const query = ref('')
const error = ref('')
const toast = useToast()
const loadState = useLoadState()
const pending = ref<string | null>(null)

const mentorCount = computed(() =>
  students.value.filter(student => student.has_mentor_profile && student.mentor_profile_status === 'active').length,
)

function initials(name: string) {
  return name.trim().charAt(0).toUpperCase() || '?'
}

function mentorGrantedAt(student: StudentAccount) {
  return student.has_mentor_profile && student.mentor_granted_at ? fullTime(student.mentor_granted_at) : '—'
}

async function load() {
  await loadState.run(async () => {
    try {
      students.value = await teacherAPI.students(query.value.trim())
      error.value = ''
    } catch (e) {
      error.value = e instanceof APIError ? e.message : '加载学生列表失败'
    }
  })
}

async function grant(student: StudentAccount) {
  pending.value = student.id
  try {
    const updated = await teacherAPI.grantMentor(student.id)
    students.value = students.value.map(row => row.id === updated.id ? updated : row)
    toast.success(`已为 ${student.name} 授予导师身份`)
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '授予导师身份失败'
  } finally {
    pending.value = null
  }
}

async function revoke(student: StudentAccount) {
  pending.value = student.id
  try {
    const updated = await teacherAPI.revokeMentor(student.id)
    students.value = students.value.map(row => row.id === updated.id ? updated : row)
    toast.success(`已撤销 ${student.name} 的导师身份`)
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '撤销导师身份失败'
  } finally {
    pending.value = null
  }
}

onMounted(load)
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">TEACHER</p>
        <h1>学生管理</h1>
        <p class="muted page-lead">管理学生账号，授予或撤销导师身份</p>
      </div>
      <LoadingButton class="button secondary" :pending="loadState.refreshing.value" pending-label="刷新中…" @click="load">
        刷新
      </LoadingButton>
    </div>

    <p v-if="error" class="error banner">{{ error }}</p>

    <LoadingRegion
      :initial-loading="loadState.initialLoading.value"
      :refreshing="loadState.refreshing.value"
      variant="list"
      label="正在加载学生"
    >
      <section class="panel student-panel">
        <div class="student-toolbar">
          <div class="student-stats">
            <div class="usage-stat">
              学生总数
              <strong>{{ students.length }}</strong>
            </div>
            <div class="usage-stat">
              导师身份
              <strong>{{ mentorCount }}</strong>
            </div>
          </div>
          <div class="student-search">
            <input v-model="query" type="search" placeholder="搜索姓名或邮箱" @keyup.enter="load">
            <LoadingButton class="button primary" :pending="loadState.loading.value" pending-label="搜索中…" @click="load">
              搜索
            </LoadingButton>
          </div>
        </div>

        <div v-if="students.length" class="table-wrap">
          <table class="usage-table student-table">
            <caption class="table-caption"><strong>学生列表</strong></caption>
            <thead>
              <tr>
                <th scope="col">学生</th>
                <th scope="col">账号状态</th>
                <th scope="col">导师身份</th>
                <th scope="col">授予时间</th>
                <th scope="col" class="actions-col">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="student in students" :key="student.id">
                <th scope="row">
                  <div class="student-identity">
                    <span class="member-avatar" aria-hidden="true">{{ initials(student.name) }}</span>
                    <div>
                      <strong>{{ student.name }}</strong>
                      <span class="muted">{{ student.email }}</span>
                    </div>
                  </div>
                </th>
                <td><StatusBadge :status="student.status" /></td>
                <td>
                  <StatusBadge
                    v-if="student.has_mentor_profile && student.mentor_profile_status === 'active'"
                    status="active"
                  />
                  <span v-else class="mentor-pending">未授予</span>
                </td>
                <td class="time-cell">{{ mentorGrantedAt(student) }}</td>
                <td class="actions-cell">
                  <LoadingButton
                    v-if="!student.has_mentor_profile || student.mentor_profile_status !== 'active'"
                    class="button primary"
                    :pending="pending === student.id"
                    pending-label="处理中…"
                    @click="grant(student)"
                  >
                    设为导师
                  </LoadingButton>
                  <LoadingButton
                    v-else
                    class="button ghost"
                    :pending="pending === student.id"
                    pending-label="处理中…"
                    @click="revoke(student)"
                  >
                    撤销导师
                  </LoadingButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <EmptyState v-else title="暂无学生" description="调整搜索条件后重试，或等待学生注册" />
      </section>
    </LoadingRegion>
  </section>
</template>

<style scoped>
.page-lead {
  margin: 0;
}

.student-panel {
  display: grid;
  gap: 20px;
}

.student-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 18px;
  border-bottom: 1px solid var(--line);
}

.student-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.student-stats .usage-stat {
  min-width: 120px;
  margin: 0;
}

.student-search {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  flex: 1 1 320px;
  justify-content: flex-end;
}

.student-search input {
  flex: 1 1 240px;
  min-width: 200px;
  max-width: 360px;
}

.student-table th[scope='row'] {
  font-weight: 500;
}

.student-identity {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.student-identity strong {
  display: block;
  font-size: 14px;
  margin-bottom: 2px;
}

.student-identity .muted {
  display: block;
  font-size: 12px;
}

.mentor-pending {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 999px;
  background: rgba(83, 99, 136, 0.08);
  color: var(--ink-soft);
  font-size: 12px;
  font-weight: 650;
}

.time-cell {
  white-space: nowrap;
  color: var(--ink-soft);
  font-size: 13px;
}

.actions-col,
.actions-cell {
  text-align: right;
  white-space: nowrap;
}

@media (max-width: 800px) {
  .student-toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .student-search {
    justify-content: stretch;
  }

  .student-search input {
    max-width: none;
  }
}
</style>
