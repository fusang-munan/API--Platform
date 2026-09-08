import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { authAPI } from '../api/auth'
import { token } from '../api/client'
import type { LoginChallenge, Role, User } from '../api/types'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const identity = ref<Role | null>(null)
  const identities = ref<Role[]>([])
  const ready = ref(false)
  const loading = ref(false)
  const isAuthenticated = computed(() => Boolean(user.value && token.get()))
  let bootstrapFlight: Promise<void> | null = null

  async function bootstrap() {
    if (ready.value) return
    if (!bootstrapFlight) {
      bootstrapFlight = (async () => {
        try {
          const session = await authAPI.refresh()
          token.set(session.access_token)
          user.value = session.user
          identity.value = session.identity
          const me = await authAPI.me()
          user.value = me.user
          identity.value = me.identity
          identities.value = me.identities
        } catch {
          token.set(null)
          user.value = null
          identity.value = null
          identities.value = []
        } finally {
          ready.value = true
        }
      })().finally(() => { bootstrapFlight = null })
    }
    await bootstrapFlight
  }

  async function login(email: string, password: string): Promise<LoginChallenge | null> {
    loading.value = true
    try {
      const challenge = await authAPI.login({ email, password })
      identities.value = challenge.identities
      if (challenge.identities.length === 1) {
        await completeSession(challenge.identities[0])
        return null
      }
      return challenge
    } finally {
      loading.value = false
    }
  }

  async function completeSession(selected: Role) {
    loading.value = true
    try {
      const session = await authAPI.createSession(selected)
      token.set(session.access_token)
      user.value = session.user
      identity.value = session.identity
      const me = await authAPI.me()
      user.value = me.user
      identity.value = me.identity
      identities.value = me.identities
    } finally {
      loading.value = false
    }
  }

  async function logout() {
    try { await authAPI.logout() } finally {
      token.set(null)
      user.value = null
      identity.value = null
      identities.value = []
    }
  }

  async function loadMe() {
    const me = await authAPI.me()
    user.value = me.user
    identity.value = me.identity
    identities.value = me.identities
  }

  function hasRole(role?: Role | Role[]) {
    const active = identity.value ?? user.value?.role
    if (!role || !active) return true
    const required = Array.isArray(role) ? role : [role]
    if (!required.includes(active)) return false
    if (identities.value.length > 0 && !identities.value.includes(active)) return false
    return true
  }

  function canAccess(role: Role) {
    if (!hasRole(role)) return false
    return identities.value.length === 0 || identities.value.includes(role)
  }

  return {
    user, identity, identities, ready, loading, isAuthenticated,
    bootstrap, login, completeSession, logout, loadMe, hasRole, canAccess,
  }
})
