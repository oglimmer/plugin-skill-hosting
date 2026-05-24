import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/vue'
import userEvent from '@testing-library/user-event'
import { nextTick } from 'vue'

vi.mock('../api', () => ({
  api: { skillVersions: vi.fn() },
  errMsg: (e: unknown, fallback = 'something went wrong') =>
    e instanceof Error ? e.message : fallback,
}))

import { api } from '../api'
import SkillVersionHistory from './SkillVersionHistory.vue'

function v(overrides: Partial<{
  id: string
  version: number
  action: 'create' | 'update' | 'delete' | 'restore' | 'revert'
  description: string
  editedByName: string
  editedAt: string
}> = {}) {
  return {
    id: overrides.id ?? 'v1',
    skillId: 's1',
    version: overrides.version ?? 1,
    action: overrides.action ?? 'create',
    name: 'my-skill',
    description: overrides.description ?? 'initial',
    body: '',
    extraFrontmatter: '',
    editedByName: overrides.editedByName ?? 'alice',
    editedAt: overrides.editedAt ?? '2026-01-01T12:00:00Z',
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('SkillVersionHistory', () => {
  it('renders versions returned from the API', async () => {
    vi.mocked(api.skillVersions).mockResolvedValue([
      v({ version: 2, action: 'update', description: 'tweak' }),
      v({ version: 1, action: 'create', description: 'initial' }),
    ])
    render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'my-skill' },
    })
    expect(await screen.findByText('tweak')).toBeInTheDocument()
    expect(screen.getByText('initial')).toBeInTheDocument()
    expect(api.skillVersions).toHaveBeenCalledWith('demo', 'my-skill')
  })

  it('shows the empty-state when no versions returned', async () => {
    vi.mocked(api.skillVersions).mockResolvedValue([])
    render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'my-skill' },
    })
    expect(await screen.findByText('no history yet.')).toBeInTheDocument()
  })

  it('shows an error when the API rejects', async () => {
    vi.mocked(api.skillVersions).mockRejectedValue(new Error('boom'))
    render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'my-skill' },
    })
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })

  it('does not fetch when skillName is null (create mode)', async () => {
    render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: null },
    })
    await nextTick()
    expect(api.skillVersions).not.toHaveBeenCalled()
  })

  it('emits revert with the version number when the button is clicked', async () => {
    vi.mocked(api.skillVersions).mockResolvedValue([
      v({ version: 3, action: 'update' }),
    ])
    const user = userEvent.setup()
    const { emitted } = render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'my-skill' },
    })
    await user.click(await screen.findByRole('button', { name: /revert/i }))
    expect(emitted().revert).toEqual([[3]])
  })

  it('hides the Revert button for delete-action rows', async () => {
    vi.mocked(api.skillVersions).mockResolvedValue([
      v({ version: 2, action: 'delete' }),
    ])
    render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'my-skill' },
    })
    await screen.findByText('delete')
    expect(screen.queryByRole('button', { name: /revert/i })).toBeNull()
  })

  it('reloads when skillName prop changes', async () => {
    vi.mocked(api.skillVersions).mockResolvedValue([])
    const { rerender } = render(SkillVersionHistory, {
      props: { pluginName: 'demo', skillName: 'a' },
    })
    await nextTick()
    await rerender({ pluginName: 'demo', skillName: 'b' })
    await nextTick()
    expect(api.skillVersions).toHaveBeenCalledTimes(2)
    expect(api.skillVersions).toHaveBeenNthCalledWith(1, 'demo', 'a')
    expect(api.skillVersions).toHaveBeenNthCalledWith(2, 'demo', 'b')
  })
})
