import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, isJwtExpired, errStatus } from '../api'
import { applyTheme, getStoredTheme, isValidTheme, normalizeTheme, setStoredTheme } from '../theme'
import type { AuthMode, User } from '../types'

export const useAuthStore = defineStore('auth', () => {
  // loadToken runs first: on an expired session it clears the cached user from
  // storage, so the subsequent loadUser() reads null and the two stay in sync.
  const token = ref<string | null>(loadToken())
  const user = ref<User | null>(loadUser())
  const mode = ref<AuthMode | null>(null)
  const marketplaceName = ref<string>('')
  const defaultLicense = ref<string>('MIT')
  const userApprovalRequired = ref<boolean>(false)
  const enterpriseMode = ref<boolean>(false)
  // The active UI theme. Seeded from localStorage (already applied to <html> in
  // main.ts) and reconciled with the server preference once a user loads.
  const theme = ref<string>(getStoredTheme())
  let modePromise: Promise<AuthMode> | null = null
  let freshUserPromise: Promise<void> | null = null

  // loadToken reads the stored JWT but discards it (along with the cached user)
  // if it has already expired, so a tab reopened weeks later starts clean and
  // the route guard sends the user to /login rather than into a half-authed
  // state where every API call 401s.
  function loadToken(): string | null {
    const t = localStorage.getItem('token')
    if (t && isJwtExpired(t)) {
      localStorage.removeItem('token')
      localStorage.removeItem('user')
      return null
    }
    return t
  }

  function loadUser(): User | null {
    const raw = localStorage.getItem('user')
    if (!raw) return null
    try {
      const u = JSON.parse(raw) as Partial<User> | null
      if (!u || typeof u !== 'object' || !u.id) return null
      // Legacy sessions saved before the approval/admin flows shipped have no
      // status or isAdmin field; treat them as approved non-admins so existing
      // logins keep working — /api/me refresh will fill in the real values.
      return { ...u, status: u.status ?? 'approved', isAdmin: u.isAdmin ?? false } as User
    } catch { return null }
  }

  function setSession(t: string, u: User) {
    localStorage.setItem('token', t)
    localStorage.setItem('user', JSON.stringify(u))
    token.value = t
    user.value = u
    // New session: invalidate any cached refresh from a prior login so the next
    // ensureFreshUser() actually re-fetches for this user.
    freshUserPromise = null
    syncThemeFromUser(u)
  }

  // syncThemeFromUser adopts the server-side theme preference whenever a user
  // payload carries a valid one — the server is the source of truth across
  // devices. It updates the ref, the <html> attribute, and the localStorage
  // cache (so the next pre-boot read matches). A missing/unknown value is left
  // alone, preserving whatever the user already had locally.
  function syncThemeFromUser(u: User | null) {
    if (u && isValidTheme(u.theme)) {
      theme.value = u.theme
      applyTheme(u.theme)
      setStoredTheme(u.theme)
    }
  }

  // setTheme switches the active theme optimistically: it applies and persists
  // locally first (so the change is instant and survives reload), then — when
  // signed in — saves it to the server. A server rejection/failure rolls the
  // local state back so the UI never diverges from what's stored.
  async function setTheme(id: string) {
    const next = normalizeTheme(id)
    const prev = theme.value
    if (next === prev) return
    applyThemeLocally(next)
    if (!token.value) return
    try {
      await api.setTheme(next)
    } catch (e) {
      applyThemeLocally(prev)
      throw e
    }
  }

  // applyThemeLocally updates the ref + <html> + localStorage, and mirrors the
  // value onto the cached user object so a reload (which restores `user` from
  // storage before /api/me returns) stays consistent.
  function applyThemeLocally(next: string) {
    theme.value = next
    applyTheme(next)
    setStoredTheme(next)
    if (user.value) {
      user.value = { ...user.value, theme: next }
      localStorage.setItem('user', JSON.stringify(user.value))
    }
  }

  async function ensureMode(): Promise<AuthMode> {
    if (mode.value) return mode.value
    if (!modePromise) {
      modePromise = api.authConfig().then(c => {
        mode.value = c.mode
        marketplaceName.value = c.marketplaceName
        defaultLicense.value = c.defaultLicense
        userApprovalRequired.value = c.userApprovalRequired
        enterpriseMode.value = c.enterpriseMode
        return c.mode
      }).catch((e) => {
        // Don't cache a transient failure: clear the promise so a later caller
        // (e.g. the user retrying login after a network blip) starts a fresh
        // request instead of re-rejecting against the dead one forever.
        modePromise = null
        throw e
      })
    }
    return modePromise
  }

  // ensureFreshUser refreshes /api/me at most once per app load, caching the
  // in-flight promise so concurrent callers (the App-startup kick-off and the
  // route guard) share a single request. Route guards await this before
  // trusting cached status/isAdmin, so a demoted, rejected, or otherwise
  // changed user can't keep navigating on stale localStorage claims after a
  // reload — localStorage is treated as a display cache, /api/me as truth.
  function ensureFreshUser(): Promise<void> {
    if (!token.value) return Promise.resolve()
    if (!freshUserPromise) {
      freshUserPromise = refreshUser().catch((e: unknown) => {
        if (errStatus(e) === 401) {
          // Token expired or revoked (e.g. "sign out everywhere" elsewhere):
          // clear it so the guard sees a logged-out state and routes to login.
          logout()
        } else {
          // Offline / 5xx: don't poison the cache — allow a later retry — and
          // re-throw so the route guard can fail closed (it can't confirm the
          // user's current status/isAdmin, so it must not admit them on stale
          // claims) rather than silently proceed.
          freshUserPromise = null
          throw e
        }
      })
    }
    return freshUserPromise
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
    freshUserPromise = null
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
    syncThemeFromUser(u)
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

  // signOutEverywhere bumps the server-side token_version, invalidating every
  // JWT issued to this user (including the one in this tab). It then tears down
  // local state via doLogout, returning true when an upstream OIDC redirect is
  // already in flight (caller should NOT push a route in that case).
  async function signOutEverywhere(): Promise<boolean> {
    await api.revokeSessions()
    return doLogout()
  }

  return {
    user, token, mode, marketplaceName, defaultLicense, userApprovalRequired, enterpriseMode, theme,
    ensureMode, ensureFreshUser, login, register, loginViaOIDC, logout, doLogout, setSession,
    refreshUser, regenerateToken, signOutEverywhere, setTheme,
  }
})
