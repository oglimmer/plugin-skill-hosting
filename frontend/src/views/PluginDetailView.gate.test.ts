import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { http, HttpResponse } from 'msw'
import { server } from '../test/server'
import { makeAgent, makeAuthConfig, makeJwt, makePlugin, makeSkill, makeUser } from '../test/factories'
import { useAuthStore } from '../stores/auth'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import PluginDetailView from './PluginDetailView.vue'

async function mountView(isAdmin: boolean) {
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div>home</div>' } },
      { path: '/plugins/:name', component: PluginDetailView },
      { path: '/plugins/:name/:rest(.*)', component: { template: '<div>other</div>' } },
    ],
  })
  await router.push('/plugins/demo')
  await router.isReady()
  const auth = useAuthStore(pinia)
  auth.user = makeUser({ id: 'u1', isAdmin })
  render(
    { components: { PluginDetailView, ConfirmDialog }, template: '<PluginDetailView /><ConfirmDialog />' },
    { global: { plugins: [pinia, router] } },
  )
}

describe('PluginDetailView audit gate', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('token', makeJwt(3600))
    server.use(
      http.get('/api/auth/config', () => HttpResponse.json(makeAuthConfig())),
      http.get('/api/plugins/demo/deleted-skills', () => HttpResponse.json([])),
    )
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('marks a checking skill and a held agent, and offers no publish button to a non-admin', async () => {
    server.use(
      http.get('/api/plugins/demo', () =>
        HttpResponse.json(makePlugin('demo', {
          skills: [makeSkill('fresh', { auditPending: true })],
          agents: [makeAgent('stuck', { auditPending: true, auditPendingError: 'model unavailable' })],
        }))),
    )
    await mountView(false)

    expect(await screen.findByText('⏳ checking · not live yet')).toBeInTheDocument()
    await fireEvent.click(screen.getByRole('tab', { name: /agents/ }))
    expect(screen.getByText('⏸ held · check failed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'publish unchecked' })).toBeNull()
  })

  it('lets an admin publish a held skill after a confirmation', async () => {
    let approved = false
    server.use(
      http.get('/api/plugins/demo', () =>
        HttpResponse.json(makePlugin('demo', {
          skills: [makeSkill('stuck', approved
            ? {}
            : { auditPending: true, auditPendingError: 'model unavailable' })],
        }))),
      http.post('/api/plugins/demo/skills/stuck/audit-approve', () => {
        approved = true
        return HttpResponse.json(makeSkill('stuck'))
      }),
    )
    await mountView(true)

    await fireEvent.click(await screen.findByRole('button', { name: 'publish unchecked' }))
    // Nothing is published until the admin confirms the warning.
    expect(approved).toBe(false)
    await fireEvent.click(await screen.findByRole('button', { name: 'Publish unchecked' }))

    await waitFor(() => expect(approved).toBe(true))
    await waitFor(() => expect(screen.queryByText('⏸ held · check failed')).toBeNull())
  })

  it('refreshes on its own until a checking item is published', async () => {
    let calls = 0
    server.use(
      http.get('/api/plugins/demo', () => {
        calls++
        // The check passes between the first and the second load.
        return HttpResponse.json(makePlugin('demo', {
          skills: [makeSkill('fresh', calls === 1 ? { auditPending: true } : {})],
        }))
      }),
    )
    vi.useFakeTimers({ shouldAdvanceTime: true })
    await mountView(false)
    expect(await screen.findByText('⏳ checking · not live yet')).toBeInTheDocument()

    await vi.advanceTimersByTimeAsync(4100)
    await waitFor(() => expect(screen.queryByText('⏳ checking · not live yet')).toBeNull())
    const settled = calls

    // Once nothing is checking, the polling stops.
    await vi.advanceTimersByTimeAsync(12000)
    expect(calls).toBe(settled)
  })
})
