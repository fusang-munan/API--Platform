import { get, patch, post } from './client'
import type { ApiKey, Application, CallTurn, Organization, Project, ProjectUsage } from './types'
export const mentorAPI = {
  organizations: () => get<Organization[]>('/api/v1/mentor/organizations'),
  projects: (org: string) => get<Project[]>(`/api/v1/mentor/organizations/${org}/projects`),
  applications: () => get<Application[]>('/api/v1/mentor/project-applications'),
  apply: (project_id: string) => post<Application>('/api/v1/mentor/project-applications', { project_id }),
  reviews: () => get<ApiKey[]>('/api/v1/mentor/api-key-reviews'),
  review: (id: string) => get<ApiKey>(`/api/v1/mentor/api-key-reviews/${id}`),
  keys: () => get<ApiKey[]>('/api/v1/mentor/api-keys'),
  key: (id: string) => get<ApiKey>(`/api/v1/mentor/api-keys/${id}`),
  approve: (id: string, monthly_credits?: string, comment?: string) => post<void>(`/api/v1/mentor/api-key-reviews/${id}/approve`, { ...(monthly_credits ? { monthly_credits } : {}), ...(comment ? { comment } : {}) }),
  reject: (id: string, comment: string) => post<void>(`/api/v1/mentor/api-key-reviews/${id}/reject`, { comment }),
  activeKeys: (project: string) => get<ApiKey[]>(`/api/v1/mentor/projects/${project}/api-keys`),
  deactivate: (id: string) => post<void>(`/api/v1/mentor/api-keys/${id}/deactivate`, {}),
  activate: (id: string) => post<void>(`/api/v1/mentor/api-keys/${id}/activate`, {}),
  updateQuota: (id: string, monthly_credits: string) => patch<void>(`/api/v1/mentor/api-keys/${id}/monthly-credit-quota`, { monthly_credits }),
  usage: (id: string, month?: string) => get<ProjectUsage>(`/api/v1/mentor/projects/${id}/usage${month ? `?month=${month}` : ''}`),
  callTurns: (query = '') => get<{ items: CallTurn[]; next_cursor?: string }>(`/api/v1/mentor/call-turns${query ? `?${query}` : ''}`),
  callTurn: (id: string) => get<CallTurn>(`/api/v1/mentor/call-turns/${id}`),
}
