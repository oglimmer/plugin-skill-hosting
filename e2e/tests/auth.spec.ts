import { test, expect } from '@playwright/test'
import { register, login, logout, type Creds } from './helpers'

// Shared across this file's serial steps: registered once, reused for login.
const user: Creds = {
  email: 'admin@e2e.test',
  username: 'e2eadmin',
  password: 'supersecret123',
}

test.describe.serial('authentication', () => {
  test('unauthenticated visit to a protected route redirects to login', async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL(/\/login/)
    await expect(page.locator('#lv-email')).toBeVisible()
  })

  test('register creates the first (admin) account and lands on the plugin list', async ({ page }) => {
    await register(page, user)
    // The plugins tab is the default landing surface.
    await expect(page.getByRole('tab', { name: /plugins/ })).toBeVisible()
    // First account on a fresh stack is admin, so the admin dropdown (behind
    // the username) exposes the admin-only destinations, and the admin-only
    // /users page loads instead of 403ing.
    await page.getByRole('button', { name: user.username }).click()
    await expect(page.getByRole('menuitem', { name: 'Security audit' })).toBeVisible()
    await page.goto('/users')
    await expect(page).toHaveURL('/users')
  })

  test('logout then login round-trips the same session', async ({ page }) => {
    await login(page, user)
    await expect(page.getByRole('tab', { name: /plugins/ })).toBeVisible()
    await logout(page)
    // Session is gone: the protected route bounces back to login.
    await page.goto('/')
    await expect(page).toHaveURL(/\/login/)
  })

  test('wrong password is rejected and keeps the user on the login page', async ({ page }) => {
    await page.goto('/login')
    await page.locator('#lv-email').fill(user.email)
    await page.locator('#lv-pass').fill('totally-wrong-password')
    await page.getByRole('button', { name: 'sign in' }).click()
    await expect(page.locator('.lv-error')).toBeVisible()
    await expect(page).toHaveURL(/\/login/)
  })
})
