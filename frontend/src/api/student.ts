import { get, post, request } from './client'
import type {
  ApiKey, KeyUsage, Model, ModelCatalogCandidate, ModelCatalogPage, Organization, StudentProject,
  StudentProjectUsage, StudentUsage, StudentUsageMetrics, SubmitApiKey,
} from './types'

export type StudentAvailabilityState = 'available' | 'degraded' | 'maintenance' | 'probe_failed'
  | 'channel_unavailable' | 'unverified' | 'disabled' | 'not_published'

type StudentAvailabilityInfo = { availability_state?: StudentAvailabilityState; addable?: boolean }
export type StudentModel = Model & StudentAvailabilityInfo
export type StudentModelCandidate = ModelCatalogCandidate & StudentAvailabilityInfo
export type StudentModelCatalogPage = Omit<ModelCatalogPage, 'items'> & { items: StudentModelCandidate[] }
type StudentModelPlaza = { common_models: StudentModel[]; library_models: StudentModel[] }
type StudentKey = Omit<ApiKey, 'models'> & { models: StudentModel[] }

// A frontend rollout can receive either version of the student usage response.
export type StudentUsageFields = Partial<StudentUsageMetrics> & {
  pending_review?: boolean
}
export type StudentModelUsageFields = StudentUsageFields & { credits: string; calls?: number }
export type StudentModelUsage = StudentModelUsageFields & { id: string; name: string }
type StudentUsageBalance = StudentUsageFields & { calls?: number; in_flight_credits?: string }
export type StudentKeyUsage = Omit<KeyUsage, 'models'> & StudentUsageBalance & {
  available_effective?: string
  project_member_available?: string
  pending_calls?: number
  models: StudentModelUsage[]
}
export type StudentProjectUsageRow = Omit<StudentProjectUsage, 'keys'> & StudentUsageBalance & {
  keys: StudentKeyUsage[]
}
export type StudentUsageSnapshot = Omit<StudentUsage, 'projects'> & {
  projects: StudentProjectUsageRow[]
  models?: StudentModelUsage[] | null
  totals?: (StudentModelUsageFields & { calls: number }) | null
}

export type StudentRecentRange = 'today' | 'month' | 'all'
export type StudentRecentCall = {
  id: string
  request_id: string
  model_id: string
  model_name: string
  project_name: string
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  credits: string
  status: string
  billing_state: string
  created_at: string
}
type StudentRecentPage = { items: StudentRecentCall[]; next_cursor: string | null }

export type StudentCallReceipt = {
  request_id: string
  model_id: string
  model_name: string
  created_at: string
  channel: string
  billing_state: string
  charged_credits: string | null
  receipt_applied: boolean | null
  usage_state?: string
  usage_reason: string | null
  input_tokens: string | null
  output_tokens: string | null
  cache_read_tokens: string | null
  cache_write_tokens: string | null
  total_tokens: string | null
}

export const studentAPI = {
  organizations: () => get<Organization[]>('/api/v1/student/organizations'),
  projects: (org: string) => get<StudentProject[]>(`/api/v1/student/organizations/${org}/projects`),
  models: () => get<StudentModelPlaza>('/api/v1/student/models'),
  commonModels: () => get<StudentModel[]>('/api/v1/student/common-models'),
  modelCatalog: (query = '', page = 1, pageSize = 20, signal?: AbortSignal) =>
    request<StudentModelCatalogPage>(`/api/v1/student/model-catalog?${new URLSearchParams({ q: query, page: String(page), page_size: String(pageSize) })}`, { signal }),
  addModel: (modelID: string) => post<StudentModel>('/api/v1/student/models', { model_id: modelID }),
  removeModel: (modelID: string) => post<{ removed: boolean }>('/api/v1/student/models/remove', { model_id: modelID }),
  usage: (month?: string) => get<StudentUsageSnapshot>(`/api/v1/student/usage${month ? `?month=${month}` : ''}`),
  recentCalls: (range: StudentRecentRange, cursor: string | null, signal: AbortSignal) => {
    const query = new URLSearchParams({ range, limit: '20' })
    if (cursor) query.set('cursor', cursor)
    return request<StudentRecentPage>(`/api/v1/student/usage/recent?${query}`, { signal })
  },
  callReceipt: (requestID: string) => get<StudentCallReceipt>(
    `/api/v1/student/usage/calls/${encodeURIComponent(requestID)}`,
  ),
  keys: () => get<StudentKey[]>('/api/v1/student/api-keys'),
  key: (id: string) => get<StudentKey>(`/api/v1/student/api-keys/${id}`),
  addKeyModels: (id: string, modelIDs: string[]) =>
    post<{ model_ids: string[]; added_model_ids: string[]; warnings?: { model_id: string; code: string; message: string }[] }>(
      `/api/v1/student/api-keys/${id}/models`, { model_ids: modelIDs },
    ),
  removeKeyModels: (id: string, modelIDs: string[]) =>
    post<{ model_ids: string[]; removed_model_ids: string[] }>(`/api/v1/student/api-keys/${id}/models/remove`, { model_ids: modelIDs }),
  submit: (body: SubmitApiKey) => post<ApiKey>('/api/v1/student/api-keys', body),
  claim: (id: string) => post<{ api_key: string; key_prefix: string; claimed_at: string }>(`/api/v1/student/api-keys/${id}/claim`),
  regenerate: (id: string) => post<{ api_key: string; key_prefix: string; claimed_at: string }>(`/api/v1/student/api-keys/${id}/regenerate`),
  withdraw: (id: string) => post<{ withdrawn: boolean }>(`/api/v1/student/api-keys/${id}/withdraw`),
}
