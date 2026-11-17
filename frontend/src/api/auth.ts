import { get, post } from './client'
import type { LoginChallenge, MeView, Role, Session, User } from './types'

export const authAPI = {
  sendCode: (email: string, purpose: string) =>
    post<{ sent: boolean; verification_code?: string }>('/api/v1/auth/verification-codes', { email, purpose }),
  register: (body: { name: string; email: string; password: string; verification_code: string }) =>
    post<User>('/api/v1/auth/register/student', body),
  login: (body: { email: string; password: string }) => post<LoginChallenge>('/api/v1/auth/login', body),
  createSession: (identity: Role) => post<Session>('/api/v1/auth/session', { identity }),
  refresh: () => post<Session>('/api/v1/auth/refresh'),
  logout: () => post<void>('/api/v1/auth/logout'),
  forgot: (email: string) => post<{ accepted: boolean }>('/api/v1/auth/password/forgot', { email }),
  reset: (body: { email: string; verification_code: string; new_password: string }) => post<void>('/api/v1/auth/password/reset', body),
  me: () => get<MeView>('/api/v1/me'),
}
