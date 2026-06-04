import { expect, type Page } from '@playwright/test'

export interface Creds {
  email: string
  username: string
  password: string
}

// Register a brand-new account through the real /register form and land on the
// authenticated plugin list. In password-auth mode the first account created on
// a fresh stack is the admin; later accounts are still auto-approved.
export async function register(page: Page, creds: Creds) {
  await page.goto('/register')
  await page.locator('input[type=email]').fill(creds.email)
  await page.locator('input[autocomplete="username"]').fill(creds.username)
  await page.locator('input[type=password]').fill(creds.password)
  await page.getByRole('button', { name: 'Sign up' }).click()
  await expect(page).toHaveURL('/')
}

// Sign in through the real /login form.
export async function login(page: Page, creds: Pick<Creds, 'email' | 'password'>) {
  await page.goto('/login')
  await page.locator('#lv-email').fill(creds.email)
  await page.locator('#lv-pass').fill(creds.password)
  await page.getByRole('button', { name: 'sign in' }).click()
  await expect(page).toHaveURL('/')
}

export async function logout(page: Page) {
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL('/login')
}
