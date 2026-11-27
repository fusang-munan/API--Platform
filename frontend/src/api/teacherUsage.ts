import { request } from './client'
import type { KeyUsage, StudentProjectUsage, UsageSlice } from './types'

type UsageCompleteness = 'complete' | 'partial' | 'unknown'
export type TeacherUsageSort = 'credits' | 'calls' | 'tokens'
export type TeacherUsageScope = { month: string; project_id?: string }
export type TeacherUsageQuery = TeacherUsageScope & {
  search?: string
  sort: TeacherUsageSort
  page: number
  page_size: number
}

type StudentIdentity = { user_id: string; user_name: string; user_email: string | null }
type UsageCounts = {
  zero_cost_calls: number
  completeness: UsageCompleteness
  pending_review_calls: number
  pending_review_credits: string
}

export type TeacherStudentUsageRow = StudentIdentity & UsageCounts & {
  project_count: number
  key_count: number
  credits: string
  calls: number
  total_tokens: string | null
  last_call_at: string | null
}

export type TeacherStudentUsagePage = {
  month: string
  items: TeacherStudentUsageRow[]
  page: number
  page_size: number
  total: number
  unattributed_upstream: { rows: number; debit_rows: number; balance_debit: string | null }
}

export type TeacherUsageMetrics = UsageCounts & {
  calls?: number
  tokens: {
    input: string | null
    output: string | null
    cache_read: string | null
    cache_write: string | null
    total: string | null
  }
  token_coverage: {
    calls: number
    known_calls: number
    complete_calls: number
    unclassified_tokens: string | null
  }
  cache_hit_rate: number | null
  cache_hit_coverage: {
    sample_calls: number
    cache_read_tokens: string | null
    input_with_cache_read_tokens: string | null
  }
}

type TeacherUsageModel = Pick<UsageSlice, 'id' | 'name' | 'credits' | 'calls'> & TeacherUsageMetrics
type TeacherUsageKey = Omit<KeyUsage, 'models' | 'status'> & TeacherUsageMetrics & {
  status: KeyUsage['status'] | 'unknown'
  calls: number
  in_flight_credits: string
  pending_calls: number
  models: TeacherUsageModel[]
}
type TeacherUsageProject = Omit<StudentProjectUsage, 'keys'> & TeacherUsageMetrics & {
  calls: number
  in_flight_credits: string
  keys: TeacherUsageKey[]
}

export type TeacherStudentUsageDetail = StudentIdentity & {
  month: string
  totals: TeacherUsageMetrics & { credits: string; calls: number }
  projects: TeacherUsageProject[]
  models: TeacherUsageModel[]
}

export const usageCompletenessLabel = { complete: '完整', partial: '部分', unknown: '未知' } as const

const path = '/api/v1/teacher/usage/students'
function scopeParams(scope: TeacherUsageScope) {
  const params = new URLSearchParams({ month: scope.month })
  if (scope.project_id) params.set('project_id', scope.project_id)
  return params
}

export const teacherUsageAPI = {
  students(filters: TeacherUsageQuery, signal: AbortSignal) {
    const params = scopeParams(filters)
    params.set('page', String(filters.page))
    params.set('page_size', String(filters.page_size))
    params.set('sort', filters.sort)
    if (filters.search) params.set('search', filters.search)
    return request<TeacherStudentUsagePage>(`${path}?${params}`, { signal })
  },
  student(userId: string, scope: TeacherUsageScope, signal: AbortSignal) {
    return request<TeacherStudentUsageDetail>(`${path}/${encodeURIComponent(userId)}?${scopeParams(scope)}`, { signal })
  },
}
