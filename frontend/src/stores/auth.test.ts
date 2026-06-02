import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../api', async (importOriginal) => ({
  // Keep the real isJwtExpired so store init exercises the actual expiry logic;
  // only the network-touching `api` object is stubbed out.
  ...(await importOriginal<typeof import('../api')>()),
  api: {
    login: vi.fn(),
    register: vi.fn(),
    me: vi.fn(),
    setTheme: vi.fn(),
    regenerateToken: vi.fn(),
    revokeSessions: vi.fn(),
    authConfig: vi.fn(),
  },
}))

// makeJwt builds a structurally valid HS256-style JWT with the given exp
// (seconds since epoch); only the payload is decoded client-side, so the
// header/signature can be placeholders.
function makeJwt(expSeconds: number): string {
  const b64 = (o: unknown) =>
    btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64({ alg: 'HS256', typ: 'JWT' })}.${b64({ sub: 'u1', exp: expSeconds })}.sig`
}

import { api, ApiError } from '../api'
import { useAuthStore } from './auth'

const fakeUser = {
  id: 'u1',
  email: 'a@b.c',
  username: 'alice',
  apiToken: 'tok',
  status: 'approved' as const,
  isAdmin: false,
}

describe('auth store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('rehydrates user/token from localStorage on init', () => {
    localStorage.setItem('token', 't0')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    expect(s.token).toBe('t0')
    expect(s.user).toEqual(fakeUser)
  })

  it('keeps a still-valid JWT session on init', () => {
    const tok = makeJwt(Math.floor(Date.now() / 1000) + 3600)
    localStorage.setItem('token', tok)
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    expect(s.token).toBe(tok)
    expect(s.user).toEqual(fakeUser)
  })

  it('drops an expired JWT session (and cached user) on init', () => {
    localStorage.setItem('token', makeJwt(Math.floor(Date.now() / 1000) - 3600))
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    expect(s.token).toBeNull()
    expect(s.user).toBeNull()
    expect(localStorage.getItem('token')).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('returns null user when localStorage holds garbage JSON', () => {
    localStorage.setItem('user', '{not json')
    const s = useAuthStore()
    expect(s.user).toBeNull()
  })

  it('login persists token and user', async () => {
    vi.mocked(api.login).mockResolvedValue({ token: 'tok123', user: fakeUser })
    const s = useAuthStore()
    await s.login('a@b.c', 'pw')
    expect(s.token).toBe('tok123')
    expect(s.user).toEqual(fakeUser)
    expect(localStorage.getItem('token')).toBe('tok123')
    expect(JSON.parse(localStorage.getItem('user')!)).toEqual(fakeUser)
  })

  it('logout clears state and storage', () => {
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    s.logout()
    expect(s.token).toBeNull()
    expect(s.user).toBeNull()
    expect(localStorage.getItem('token')).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('doLogout stays local for password mode', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'password',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
      userApprovalRequired: false,
      enterpriseMode: false,
    })
    const s = useAuthStore()
    await s.ensureMode()
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    expect(s.doLogout()).toBe(false)
    expect(s.token).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('doLogout stays local for corporate OIDC (domain-restricted)', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'oidc',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
      userApprovalRequired: false,
      enterpriseMode: false,
    })
    const s = useAuthStore()
    await s.ensureMode()
    expect(s.doLogout()).toBe(false)
  })

  it('doLogout kicks off RP-initiated logout for open OIDC', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'oidc',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
      userApprovalRequired: true,
      enterpriseMode: false,
    })
    // jsdom's window.location is read-only by default; assign via Object.defineProperty.
    const setHref = vi.fn()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        get href() { return '' },
        set href(v: string) { setHref(v) },
      },
    })

    const s = useAuthStore()
    await s.ensureMode()
    localStorage.setItem('user', JSON.stringify(fakeUser))
    expect(s.doLogout()).toBe(true)
    expect(setHref).toHaveBeenCalledWith('/api/auth/oidc/logout')
    // Local state still cleared, so the in-flight redirect lands on a clean SPA.
    expect(s.token).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('ensureMode caches the auth config response', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'password',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
      userApprovalRequired: false,
      enterpriseMode: false,
    })
    const s = useAuthStore()
    const m1 = await s.ensureMode()
    const m2 = await s.ensureMode()
    expect(m1).toBe('password')
    expect(m2).toBe('password')
    expect(api.authConfig).toHaveBeenCalledTimes(1)
    expect(s.marketplaceName).toBe('mp')
  })

  it('ensureMode does not cache a failed config request (allows retry)', async () => {
    vi.mocked(api.authConfig)
      .mockRejectedValueOnce(new Error('network blip'))
      .mockResolvedValueOnce({
        mode: 'password',
        marketplaceName: 'mp',
        defaultLicense: 'MIT',
        userApprovalRequired: false,
        enterpriseMode: false,
      })
    const s = useAuthStore()
    await expect(s.ensureMode()).rejects.toThrow('network blip')
    // The rejected promise must not be cached: a second call retries and wins.
    const mode = await s.ensureMode()
    expect(mode).toBe('password')
    expect(api.authConfig).toHaveBeenCalledTimes(2)
  })

  it('ensureFreshUser refreshes /api/me once and shares the in-flight promise', async () => {
    vi.mocked(api.me).mockResolvedValue({ ...fakeUser, isAdmin: true })
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    await Promise.all([s.ensureFreshUser(), s.ensureFreshUser()])
    expect(api.me).toHaveBeenCalledTimes(1)
    expect(s.user?.isAdmin).toBe(true) // fresh server claims overwrote the cache
  })

  it('ensureFreshUser clears the session when /api/me returns 401', async () => {
    vi.mocked(api.me).mockRejectedValue(new ApiError(401, 'unauthorized'))
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    await s.ensureFreshUser()
    expect(s.token).toBeNull()
    expect(s.user).toBeNull()
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('ensureFreshUser rethrows a transient (non-401) failure but keeps the session and allows retry', async () => {
    vi.mocked(api.me)
      .mockRejectedValueOnce(new ApiError(503, 'unavailable'))
      .mockResolvedValueOnce({ ...fakeUser, isAdmin: true })
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    // Re-throws so the route guard can fail closed, but doesn't clear the
    // session (a 5xx isn't an auth failure) and doesn't poison the cache.
    await expect(s.ensureFreshUser()).rejects.toThrow('unavailable')
    expect(s.token).toBe('t')
    await s.ensureFreshUser() // retry succeeds against a fresh request
    expect(api.me).toHaveBeenCalledTimes(2)
    expect(s.user?.isAdmin).toBe(true)
  })

  it('setSession invalidates a prior session refresh so the next ensureFreshUser refetches', async () => {
    vi.mocked(api.me).mockResolvedValue({ ...fakeUser, isAdmin: true })
    localStorage.setItem('token', 't')
    const s = useAuthStore()
    await s.ensureFreshUser()
    expect(api.me).toHaveBeenCalledTimes(1)
    // A new login replaces the session; the cached refresh must not be reused.
    s.setSession('t2', { ...fakeUser, id: 'u2' })
    await s.ensureFreshUser()
    expect(api.me).toHaveBeenCalledTimes(2)
  })

  it('ensureFreshUser is a no-op without a token', async () => {
    const s = useAuthStore()
    await s.ensureFreshUser()
    expect(api.me).not.toHaveBeenCalled()
  })

  it('regenerateToken updates user.apiToken and storage', async () => {
    vi.mocked(api.regenerateToken).mockResolvedValue({ apiToken: 'NEW' })
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    const tok = await s.regenerateToken()
    expect(tok).toBe('NEW')
    expect(s.user?.apiToken).toBe('NEW')
    expect(JSON.parse(localStorage.getItem('user')!).apiToken).toBe('NEW')
  })

  it('signOutEverywhere revokes server-side then clears local state (password mode)', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'password',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
      userApprovalRequired: false,
      enterpriseMode: false,
    })
    vi.mocked(api.revokeSessions).mockResolvedValue()
    localStorage.setItem('token', 't')
    localStorage.setItem('user', JSON.stringify(fakeUser))
    const s = useAuthStore()
    await s.ensureMode()
    const redirecting = await s.signOutEverywhere()
    expect(api.revokeSessions).toHaveBeenCalledOnce()
    expect(redirecting).toBe(false) // password mode stays local, no OIDC redirect
    expect(s.token).toBeNull()
    expect(s.user).toBeNull()
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('refreshUser is a no-op without a token', async () => {
    const s = useAuthStore()
    await s.refreshUser()
    expect(api.me).not.toHaveBeenCalled()
  })

  describe('theme', () => {
    beforeEach(() => {
      localStorage.clear()
      delete document.documentElement.dataset.theme
    })

    it('setTheme applies to <html> + localStorage + ref and persists to server when authed', async () => {
      vi.mocked(api.setTheme).mockResolvedValue({ theme: 'dark' })
      localStorage.setItem('token', 't')
      localStorage.setItem('user', JSON.stringify(fakeUser))
      const s = useAuthStore()
      await s.setTheme('dark')
      expect(s.theme).toBe('dark')
      expect(document.documentElement.dataset.theme).toBe('dark')
      expect(localStorage.getItem('theme')).toBe('dark')
      expect(api.setTheme).toHaveBeenCalledWith('dark')
      // Mirrored onto the cached user so a reload stays consistent.
      expect(JSON.parse(localStorage.getItem('user')!).theme).toBe('dark')
    })

    it('setTheme applies locally but skips the server when logged out', async () => {
      const s = useAuthStore()
      await s.setTheme('sepia')
      expect(s.theme).toBe('sepia')
      expect(localStorage.getItem('theme')).toBe('sepia')
      expect(api.setTheme).not.toHaveBeenCalled()
    })

    it('setTheme rolls back local state if the server rejects', async () => {
      vi.mocked(api.setTheme).mockRejectedValue(new Error('boom'))
      // Seed a known starting theme, then build the store so it reads it.
      localStorage.setItem('theme', 'midnight')
      localStorage.setItem('token', 't')
      const s = useAuthStore()
      expect(s.theme).toBe('midnight')
      await expect(s.setTheme('contrast')).rejects.toThrow('boom')
      // Reverted to the pre-switch theme everywhere.
      expect(s.theme).toBe('midnight')
      expect(document.documentElement.dataset.theme).toBe('midnight')
      expect(localStorage.getItem('theme')).toBe('midnight')
    })

    it('setTheme ignores unknown values (normalizes to default) without a server call', async () => {
      const s = useAuthStore()
      await s.setTheme('not-a-theme')
      // Default theme is "light"; an unknown id collapses to it, and since that
      // equals the initial value, nothing is persisted to the server.
      expect(s.theme).toBe('light')
      expect(api.setTheme).not.toHaveBeenCalled()
    })

    it('login adopts the server theme from the returned user', async () => {
      vi.mocked(api.login).mockResolvedValue({
        token: 'tok',
        user: { ...fakeUser, theme: 'midnight' },
      })
      const s = useAuthStore()
      await s.login('a@b.c', 'pw')
      expect(s.theme).toBe('midnight')
      expect(document.documentElement.dataset.theme).toBe('midnight')
      expect(localStorage.getItem('theme')).toBe('midnight')
    })

    it('refreshUser adopts the server theme', async () => {
      localStorage.setItem('token', 't')
      vi.mocked(api.me).mockResolvedValue({ ...fakeUser, theme: 'contrast' })
      const s = useAuthStore()
      await s.refreshUser()
      expect(s.theme).toBe('contrast')
      expect(document.documentElement.dataset.theme).toBe('contrast')
    })

    it('login leaves the local theme untouched when the user has no valid theme', async () => {
      localStorage.setItem('theme', 'sepia')
      vi.mocked(api.login).mockResolvedValue({
        token: 'tok',
        user: { ...fakeUser, theme: undefined },
      })
      const s = useAuthStore()
      await s.login('a@b.c', 'pw')
      expect(s.theme).toBe('sepia')
    })
  })
})
