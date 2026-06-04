import { test, expect } from '@playwright/test'
import { register, createPlugin, type Creds } from './helpers'

const owner: Creds = {
  email: 'authoring@e2e.test',
  username: 'e2eauthoring',
  password: 'supersecret123',
}

// A real SKILL.md body with a recognisable heading and a unique sentinel so we
// can assert it survived the round-trip without matching on incidental text.
const SENTINEL = 'sentinel-marker-7f3a'
const SKILL_BODY = `## Purpose

A genuine skill body authored in the raw markdown editor. ${SENTINEL}

## Steps

1. Do the first thing.
2. Then do the second thing.
`

// The existing skills.spec.ts only ever creates a skill with the editor's
// *default* body. These journeys cover the authoring paths that aren't
// otherwise exercised: hand-writing the body in the raw markdown editor, and
// running it through the Claude-backed validator.
test('authors a skill body in the raw markdown editor and it round-trips', async ({ page }) => {
  await register(page, owner)
  await createPlugin(page, 'raw-editor-plugin', 'holds a hand-authored skill')

  const skillName = 'raw-body-skill'

  // --- create the skill with a hand-written raw body ---
  await page.getByRole('link', { name: '+ new skill' }).click()
  await expect(page).toHaveURL('/plugins/raw-editor-plugin/skills/new')
  await page.getByPlaceholder('my-skill-slug').fill(skillName)
  await page.getByPlaceholder(/One sentence/).fill('demonstrates the raw editor')

  // The body editor defaults to the Visual (WYSIWYG) mode; switch to Raw to
  // paste markdown verbatim, then type the body into the exposed <textarea>.
  await page.getByRole('tab', { name: 'Raw' }).click()
  const rawBody = page.locator('.md-raw')
  await expect(rawBody).toBeVisible()
  await rawBody.fill(SKILL_BODY)

  await page.getByRole('button', { name: 'create', exact: true }).click()

  // Back on the plugin detail page, the skill shows up in the skills table.
  await expect(page).toHaveURL('/plugins/raw-editor-plugin')
  await expect(page.getByRole('link', { name: skillName })).toBeVisible()

  // --- reopen the skill and confirm the raw body persisted verbatim ---
  await page.getByRole('link', { name: skillName }).click()
  await expect(page).toHaveURL(`/plugins/raw-editor-plugin/skills/${skillName}/edit`)

  await page.getByRole('tab', { name: 'Raw' }).click()
  // The stored SKILL.md body is what we typed — sentinel and all.
  await expect(page.locator('.md-raw')).toHaveValue(new RegExp(SENTINEL))
})

// The validator calls the Anthropic API, so it only works when the backend was
// started with ANTHROPIC_API_KEY. compose.yml forwards that var from the host
// env, so the same value gates both the stack and this test. Skip otherwise —
// we don't want a missing key (or its per-call cost) to break CI.
test('validate runs the skill through the claude review panel', async ({ page }) => {
  test.skip(
    !process.env.ANTHROPIC_API_KEY,
    'validate requires ANTHROPIC_API_KEY on the backend',
  )

  await register(page, { ...owner, email: 'reviewer@e2e.test', username: 'e2ereviewer' })
  await createPlugin(page, 'review-plugin', 'holds a skill to validate')

  await page.getByRole('link', { name: '+ new skill' }).click()
  await page.getByPlaceholder('my-skill-slug').fill('review-skill')
  await page.getByPlaceholder(/One sentence/).fill('a skill worth reviewing')
  await page.getByRole('tab', { name: 'Raw' }).click()
  await page.locator('.md-raw').fill(SKILL_BODY)

  await page.getByRole('button', { name: 'validate' }).click()

  // The review section appears immediately (loading state), then resolves to a
  // report. The exact findings are LLM-generated, so assert the structure, not
  // the content: the counts row renders whenever a report comes back.
  await expect(page.getByText('claude review')).toBeVisible()
  await expect(page.locator('.se-review__counts')).toBeVisible({ timeout: 90_000 })
})
