import { test, expect } from '@playwright/test'
import { register, login, type Creds } from './helpers'

// A distinct account from the auth spec, created lazily on first use. Whichever
// spec runs first registers the admin; the other gets an auto-approved member —
// either way this account can own plugins.
const owner: Creds = {
  email: 'owner@e2e.test',
  username: 'e2eowner',
  password: 'supersecret123',
}

test.describe.serial('plugin lifecycle', () => {
  const pluginName = 'e2e-demo-plugin'

  test('create a plugin and see it on its detail page and in the list', async ({ page }) => {
    await register(page, owner)

    await page.goto('/plugins/new')
    const inputs = page.locator('form input')
    await inputs.nth(0).fill(pluginName) // name (slug)
    await inputs.nth(1).fill('A plugin created by the e2e suite') // description
    await page.getByRole('button', { name: 'Create plugin' }).click()

    // Redirects to the detail page for the new plugin.
    await expect(page).toHaveURL(`/plugins/${pluginName}`)
    await expect(page.locator('code.pd-bar__path')).toHaveText(pluginName)

    // It also shows up in the marketplace list.
    await page.goto('/')
    await expect(page.getByRole('link', { name: pluginName })).toBeVisible()
  })

  test('soft-delete the plugin removes it from the active list', async ({ page }) => {
    // Each Playwright test gets a fresh context (no carried-over JWT), so the
    // already-registered owner signs back in before acting.
    await login(page, owner)
    await page.goto(`/plugins/${pluginName}`)
    // exact + case-sensitive: the page trigger ("delete plugin") and the confirm
    // dialog's button ("Delete plugin") differ only by case.
    await page.getByRole('button', { name: 'delete plugin', exact: true }).click()
    await page.getByRole('button', { name: 'Delete plugin', exact: true }).click()

    await expect(page).toHaveURL('/')
    // With no active plugins left the view defaults to the "connect" tab, so
    // switch back to the plugins tab to inspect the list.
    await page.getByRole('tab', { name: /plugins/ }).click()
    // No longer an active row…
    await expect(page.getByRole('link', { name: pluginName })).toHaveCount(0)
    // …but recoverable from the "deleted plugins" disclosure.
    await expect(page.getByText('deleted plugins')).toBeVisible()
  })
})
