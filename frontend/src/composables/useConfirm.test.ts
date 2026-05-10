import { describe, it, expect } from 'vitest'
import { useConfirm } from './useConfirm'

describe('useConfirm', () => {
  it('exposes confirm/answer that resolves a promise', async () => {
    const { active, confirm, answer } = useConfirm()
    const p = confirm({ message: 'really?' })
    expect(active.value?.message).toBe('really?')
    answer(true)
    await expect(p).resolves.toBe(true)
    expect(active.value).toBeNull()
  })

  it('fills defaults for optional fields', () => {
    const { active, confirm, answer } = useConfirm()
    confirm({ message: 'm' })
    expect(active.value).toMatchObject({
      title: 'Are you sure?',
      message: 'm',
      confirmLabel: 'Confirm',
      cancelLabel: 'Cancel',
      danger: false,
    })
    answer(false)
  })

  it('answer is a no-op when nothing is pending', () => {
    const { answer } = useConfirm()
    expect(() => answer(true)).not.toThrow()
  })

  it('shares state across hook calls (singleton store)', async () => {
    const a = useConfirm()
    const b = useConfirm()
    const p = a.confirm({ message: 'shared' })
    expect(b.active.value?.message).toBe('shared')
    b.answer(false)
    await expect(p).resolves.toBe(false)
  })
})
