import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type AuthMode, type User } from '../api'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(loadUser())
  const token = ref<string | null>(localStorage.getItem('token'))
  const mode = ref<AuthMode | null>(null)
  let modePromise: Promise<AuthMode> | null = null

  function loadUser(): User | null {
    const raw = localStorage.getItem('user')
    if (!raw) return null
    try { return JSON.parse(raw) } catch { return null }
  }

  function setSession(t: string, u: User) {
    token.value = t
    user.value = u
    localStorage.setItem('token', t)
    localStorage.setItem('user', JSON.stringify(u))
  }

  async function ensureMode(): Promise<AuthMode> {
    if (mode.value) return mode.value
    if (!modePromise) {
      modePromise = api.authConfig().then(c => {
        mode.value = c.mode
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

  return { user, token, mode, ensureMode, login, register, loginViaOIDC, logout, setSession }
})
