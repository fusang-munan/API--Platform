import { get, post } from './client'
import type { ApiKey, Model, ModelCatalogPage, Organization, Project, StudentModelPlaza, StudentProject, SubmitApiKey, StudentUsage } from './types'

export const studentAPI = {
  organizations: () => get<Organization[]>('/api/v1/student/organizations'),
  projects: (org: string) => get<StudentProject[]>(`/api/v1/student/organizations/${org}/projects`),
  models: () => get<StudentModelPlaza>('/api/v1/student/models'),
  commonModels: () => get<Model[]>('/api/v1/student/common-models'),
  modelCatalog: (query = '', page = 1, pageSize = 20) =>
    get<ModelCatalogPage>(`/api/v1/student/model-catalog?${new URLSearchParams({ q: query, page: String(page), page_size: String(pageSize) })}`),
  addModel: (modelID: string) => post<Model>('/api/v1/student/models', { model_id: modelID }),
  removeModel: (modelID: string) => post<{ removed: boolean }>('/api/v1/student/models/remove', { model_id: modelID }),
  usage: (month?: string) => get<StudentUsage>(`/api/v1/student/usage${month ? `?month=${month}` : ''}`),
  keys: () => get<ApiKey[]>('/api/v1/student/api-keys'),
  key: (id: string) => get<ApiKey>(`/api/v1/student/api-keys/${id}`),
  addKeyModels: (id: string, modelIDs: string[]) =>
    post<{ model_ids: string[]; added_model_ids: string[] }>(`/api/v1/student/api-keys/${id}/models`, { model_ids: modelIDs }),
  submit: (body: SubmitApiKey) => post<ApiKey>('/api/v1/student/api-keys', body),
  claim: (id: string) => post<{ api_key: string; key_prefix: string; claimed_at: string }>(`/api/v1/student/api-keys/${id}/claim`),
  regenerate: (id: string) => post<{ api_key: string; key_prefix: string; claimed_at: string }>(`/api/v1/student/api-keys/${id}/regenerate`),
  withdraw: (id: string) => post<{ withdrawn: boolean }>(`/api/v1/student/api-keys/${id}/withdraw`),
}
