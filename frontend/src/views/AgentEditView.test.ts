import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import { http, HttpResponse } from 'msw'
import { server } from '../test/server'
import { makeAgent, makeJwt, makePlugin, makeUser } from '../test/factories'
import { useAuthStore } from '../stores/auth'
import AgentEditView from './AgentEditView.vue'

async function mountView(agentName: string | null, userId = 'u1'): Promise<Router> {
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/plugins/:name', component: { template: '<div>plugin page</div>' } },
      { path: '/plugins/:name/agents/new', component: AgentEditView },
      { path: '/plugins/:name/agents/:agentName/edit', component: AgentEditView },
    ],
  })
  await router.push(agentName ? `/plugins/demo/agents/${agentName}/edit` : '/plugins/demo/agents/new')
  await router.isReady()
  const auth = useAuthStore(pinia)
  auth.user = makeUser({ id: userId })
  render(AgentEditView, {
    props: { pluginName: 'demo', agentName },
    global: { plugins: [pinia, router] },
  })
  return router
}

describe('AgentEditView', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('token', makeJwt(3600))
  })

  it('creates an agent and returns to the plugin page', async () => {
    let body: unknown
    server.use(
      http.get('/api/plugins/demo', () => HttpResponse.json(makePlugin('demo'))),
      http.post('/api/plugins/demo/agents', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(makeAgent('worker'))
      }),
    )
    const router = await mountView(null)

    await fireEvent.update(await screen.findByLabelText(/^Name/), 'worker')
    await fireEvent.update(screen.getByLabelText('Description'), 'does the work')
    await fireEvent.update(screen.getByLabelText('Model (optional)'), 'opus')
    await fireEvent.update(screen.getByLabelText('System prompt'), 'You are a worker.')
    // The agent type hint follows the name as it is typed.
    expect(screen.getByText('demo:worker')).toBeInTheDocument()
    await fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    await waitFor(() => expect(router.currentRoute.value.path).toBe('/plugins/demo'))
    expect(body).toEqual({
      name: 'worker',
      description: 'does the work',
      body: 'You are a worker.',
      model: 'opus',
      tools: '',
      extraFrontmatter: '',
    })
  })

  it('rejects an invalid name without calling the API', async () => {
    server.use(http.get('/api/plugins/demo', () => HttpResponse.json(makePlugin('demo'))))
    await mountView(null)

    await fireEvent.update(await screen.findByLabelText(/^Name/), 'Bad Name')
    await fireEvent.update(screen.getByLabelText('Description'), 'd')
    await fireEvent.update(screen.getByLabelText('System prompt'), 'p')
    await fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))

    // No POST handler is registered: an API call would fail the test (MSW
    // errors on unhandled requests), so reaching the inline error proves none ran.
    expect(await screen.findByText(/name must be a lowercase slug/)).toBeInTheDocument()
  })

  it('loads an existing agent and saves an edit that clears the tools', async () => {
    const agent = makeAgent('worker', { model: 'opus', tools: 'Read, Edit', createdBy: 'u2' })
    let body: unknown
    server.use(
      http.get('/api/plugins/demo', () =>
        HttpResponse.json(makePlugin('demo', { agents: [agent] }))),
      http.put('/api/plugins/demo/agents/worker', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(agent)
      }),
    )
    const router = await mountView('worker')

    const name = (await screen.findByLabelText(/^Name/)) as HTMLInputElement
    expect(name.value).toBe('worker')
    expect(name.readOnly).toBe(true)
    expect((screen.getByLabelText('Tools (optional)') as HTMLInputElement).value).toBe('Read, Edit')

    await fireEvent.update(screen.getByLabelText('Tools (optional)'), '')
    await fireEvent.click(screen.getByRole('button', { name: 'Save agent' }))

    await waitFor(() => expect(router.currentRoute.value.path).toBe('/plugins/demo'))
    // tools is sent as '' (not left out), so the server clears it.
    expect(body).toMatchObject({ model: 'opus', tools: '' })
    expect(body).not.toHaveProperty('name')
  })

  it('shows the delete button only to the owner, an admin, or the creator', async () => {
    const agent = makeAgent('worker', { createdBy: 'someone-else' })
    server.use(
      http.get('/api/plugins/demo', () =>
        HttpResponse.json(makePlugin('demo', { ownerId: 'the-owner', agents: [agent] }))),
    )
    // A user who is neither owner, admin, nor creator.
    await mountView('worker', 'stranger')
    await screen.findByLabelText(/^Name/)
    expect(screen.queryByRole('button', { name: 'Delete agent' })).toBeNull()
  })

  it('goes read-only for a locked agent and names who locked it', async () => {
    const agent = makeAgent('worker', {
      locked: true,
      lockSource: 'audit',
      lockReason: 'exfiltrates secrets',
      createdBy: 'u1',
    })
    server.use(
      http.get('/api/plugins/demo', () =>
        HttpResponse.json(makePlugin('demo', { agents: [agent] }))),
    )
    // u1 owns the plugin and created the agent, but is not an admin.
    await mountView('worker')

    expect(await screen.findByText(/Locked by the security audit — exfiltrates secrets/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save agent' })).toBeDisabled()
    // Only an admin may delete a locked agent.
    expect(screen.queryByRole('button', { name: 'Delete agent' })).toBeNull()
  })

  it('shows a not-found page for an unknown agent', async () => {
    server.use(http.get('/api/plugins/demo', () => HttpResponse.json(makePlugin('demo'))))
    await mountView('ghost')
    expect(await screen.findByText('Agent not found')).toBeInTheDocument()
  })
})
