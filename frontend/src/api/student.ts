import { get, post, request } from './client'
import type { ApiKey, Model, ModelCatalogCandidate, ModelCatalogPage, Organization, StudentProject, StudentUsageReport, SubmitApiKey } from './types'

export type StudentAvailabilityState = 'available' | 'degraded' | 'maintenance' | 'probe_failed'
  | 'channel_unavailable' | 'unverified' | 'disabled' | 'not_published'

type StudentAvailabilityInfo = { availability_state?: StudentAvailabilityState }
export type StudentModel = Model & StudentAvailabilityInfo
export type StudentModelCandidate = ModelCatalogCandidate & StudentAvailabilityInfo
export type StudentModelCatalogPage = Omit<ModelCatalogPage, 'items'> & { items: StudentModelCandidate[] }
type StudentModelPlaza = { common_models: StudentModel[]; library_models: StudentModel[] }
type StudentKey = Omit<ApiKey, 'models'> & { models: StudentModel[] }

export const studentAPI = {
  organizations: () => get<Organization[]>('/api/v1/student/organizations'),
  projects: (org: string) => get<StudentProject[]>(`/api/v1/student/organizations/${org}/projects`),
  models: () => get<StudentModelPlaza>('/api/v1/student/models'),
  commonModels: () => get<StudentModel[]>('/api/v1/student/common-models'),
  modelCatalog: (query = '', page = 1, pageSize = 20, signal?: AbortSignal) =>
    request<StudentModelCatalogPage>(`/api/v1/student/model-catalog?${new URLSearchParams({ q: query, page: String(page), page_size: String(pageSize) })}`, { signal }),
  addModel: (modelID: string) => post<StudentModel>('/api/v1/student/models', { model_id: modelID }),
  removeModel: (modelID: string) => post<{ removed: boolean }>('/api/v1/student/models/remove', { model_id: modelID }),
  usage: (month?: string) => get<StudentUsageReport>(`/api/v1/student/usage${month ? `?month=${month}` : ''}`),
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
