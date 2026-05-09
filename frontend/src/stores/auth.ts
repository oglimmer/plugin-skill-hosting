import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type User } from '../api'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(loadUser())
  const token = ref<string | null>(localStorage.getItem('token'))

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

  async function login(email: string, password: string) {
    const r = await api.login(email, password)
    setSession(r.token, r.user)
  }
  async function register(email: string, username: string, password: string) {
    const r = await api.register(email, username, password)
    setSession(r.token, r.user)
  }
  function logout() {
    token.value = null
    user.value = null
    localStorage.removeItem('token')
    localStorage.removeItem('user')
  }

  return { user, token, login, register, logout }
})
