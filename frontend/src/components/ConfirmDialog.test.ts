import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/vue'
import userEvent from '@testing-library/user-event'
import ConfirmDialog from './ConfirmDialog.vue'
import { useConfirm } from '../composables/useConfirm'

// The composable exposes a singleton `active` ref, so each test needs to
// resolve any pending dialog before the next one renders.

describe('ConfirmDialog', () => {
  it('does not render when no dialog is active', () => {
    const { answer } = useConfirm()
    answer(false) // clear any leftover state from prior tests
    render(ConfirmDialog)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('renders the active confirm and resolves true on confirm click', async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    const { confirm } = useConfirm()
    const p = confirm({ message: 'delete it?', confirmLabel: 'Delete', danger: true })

    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('delete it?')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await expect(p).resolves.toBe(true)
  })

  it('resolves false when the cancel button is clicked', async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    const { confirm } = useConfirm()
    const p = confirm({ message: 'cancel me' })

    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await expect(p).resolves.toBe(false)
  })

  it('Escape key resolves false; Enter resolves true', async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    const { confirm } = useConfirm()

    const cancelled = confirm({ message: 'press esc' })
    await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    await expect(cancelled).resolves.toBe(false)

    const accepted = confirm({ message: 'press enter' })
    await screen.findByRole('dialog')
    await user.keyboard('{Enter}')
    await expect(accepted).resolves.toBe(true)
  })
})
