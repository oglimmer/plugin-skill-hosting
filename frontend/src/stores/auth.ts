import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '../api'
import type { AuthMode, User } from '../types'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(loadUser())
  const token = ref<string | null>(localStorage.getItem('token'))
  const mode = ref<AuthMode | null>(null)
  const marketplaceName = ref<string>('')
  const defaultLicense = ref<string>('MIT')
  const userApprovalRequired = ref<boolean>(false)
  let modePromise: Promise<AuthMode> | null = null

  function loadUser(): User | null {
    const raw = localStorage.getItem('user')
    if (!raw) return null
    try {
      const u = JSON.parse(raw) as Partial<User> | null
      if (!u || typeof u !== 'object' || !u.id) return null
      // Legacy sessions saved before the approval flow shipped have no
      // status field; treat them as approved so existing logins keep working.
      return { ...u, status: u.status ?? 'approved' } as User
    } catch { return null }
  }

  function setSession(t: string, u: User) {
    localStorage.setItem('token', t)
    localStorage.setItem('user', JSON.stringify(u))
    token.value = t
    user.value = u
  }

  async function ensureMode(): Promise<AuthMode> {
    if (mode.value) return mode.value
    if (!modePromise) {
      modePromise = api.authConfig().then(c => {
        mode.value = c.mode
        marketplaceName.value = c.marketplaceName
        defaultLicense.value = c.defaultLicense
        userApprovalRequired.value = c.userApprovalRequired
        return c.mode
      })
    }
    return modePromise
  }

  async function login(email: string, password: string) {
    const r = await api.login(email, password)
    setSession(r.token, r.user)
  }
  async function register(email: string, username: string, password: string) {
    const r = await api.register(email, username, password)
    setSession(r.token, r.user)
  }
  function loginViaOIDC() {
    window.location.href = '/api/auth/oidc/login'
  }
  function logout() {
    token.value = null
    user.value = null
    localStorage.removeItem('token')
    localStorage.removeItem('user')
  }

  // doLogout clears local state, then either kicks off RP-initiated logout
  // (open OIDC: tear down the upstream session too) or just hands control
  // back so the caller can router.push('/login'). Returns true when the
  // browser has been navigated away (caller should NOT push a route).
  function doLogout(): boolean {
    const wantsUpstream = mode.value === 'oidc' && userApprovalRequired.value
    logout()
    if (wantsUpstream) {
      window.location.href = '/api/auth/oidc/logout'
      return true
    }
    return false
  }

  async function refreshUser() {
    if (!token.value) return
    const u = await api.me()
    localStorage.setItem('user', JSON.stringify(u))
    user.value = u
  }

  async function regenerateToken() {
    const r = await api.regenerateToken()
    if (user.value) {
      const u = { ...user.value, apiToken: r.apiToken }
      localStorage.setItem('user', JSON.stringify(u))
      user.value = u
    }
    return r.apiToken
  }

  return {
    user, token, mode, marketplaceName, defaultLicense, userApprovalRequired,
    ensureMode, login, register, loginViaOIDC, logout, doLogout, setSession,
    refreshUser, regenerateToken,
  }
})
