import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../api', () => ({
  api: {
    login: vi.fn(),
    register: vi.fn(),
    me: vi.fn(),
    regenerateToken: vi.fn(),
    authConfig: vi.fn(),
  },
}))

import { api } from '../api'
import { useAuthStore } from './auth'

const fakeUser = { id: 'u1', email: 'a@b.c', username: 'alice', apiToken: 'tok' }

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

  it('ensureMode caches the auth config response', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({
      mode: 'password',
      marketplaceName: 'mp',
      defaultLicense: 'MIT',
    })
    const s = useAuthStore()
    const m1 = await s.ensureMode()
    const m2 = await s.ensureMode()
    expect(m1).toBe('password')
    expect(m2).toBe('password')
    expect(api.authConfig).toHaveBeenCalledTimes(1)
    expect(s.marketplaceName).toBe('mp')
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

  it('refreshUser is a no-op without a token', async () => {
    const s = useAuthStore()
    await s.refreshUser()
    expect(api.me).not.toHaveBeenCalled()
  })
})
