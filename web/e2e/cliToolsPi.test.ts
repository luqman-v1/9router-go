/**
 * Issue #165: the CLI Tools page had no card for the pi / Oh My Pi coding
 * agents, so a user who connects them through `models.json` / `models.yml` had
 * to reconstruct the gateway snippet from a comment on the issue.
 *
 * Two halves, both of which can regress silently:
 *
 * - `CliToolsView.svelte` renders a card per catalog entry and looks the badge
 *   up in the `GET /api/cli-tools/all-statuses` map by id. A card whose id has
 *   no detector still renders, but the badge always reads "Guide" — an added
 *   card and a missing detector together look identical to a working card.
 *   The suite therefore asserts the id is present in the statuses payload, not
 *   just that a card is on screen.
 * - The snippet text is generated from the live origin and the active API key.
 *   A card whose instructions do not interpolate those renders a placeholder
 *   the user cannot paste, and nothing in the build notices.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

/** The two agents issue #165 asked for. */
const AGENTS = [
  { card: 'Pi', id: 'pi', configPath: '.pi/agent/models.json' },
  { card: 'Oh My Pi', id: 'omp', configPath: '.omp/agent/models.yml' },
] as const

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20419)
  page = app.page
}, 20_000)

afterAll(async () => {
  await app.stop()
})

/** The tool card with the given heading. */
function card(name: string) {
  return page.locator('[role="button"]').filter({ has: page.getByRole('heading', { name, exact: true }) })
}

/** Opens a tool card and returns its dialog text. */
async function openTool(name: string) {
  await page.goto(`${app.baseURL}/dashboard/cli-tools`)
  await card(name).click()
  const dialog = page.locator('.fixed.inset-0')
  await dialog.waitFor({ timeout: 10_000 })
  return dialog
}

describe('CLI Tools page lists the pi / Oh My Pi agents (#165)', () => {
  for (const { card: cardName, id, configPath } of AGENTS) {
    test(`${cardName} has a card backed by a real install detector`, async () => {
      await page.goto(`${app.baseURL}/dashboard/cli-tools`)

      const statuses = await page.evaluate(async () => {
        const res = await fetch('/api/cli-tools/all-statuses', { credentials: 'include' })
        return (await res.json()) as Record<string, { installed?: boolean } | null>
      })

      // A null entry would render the card with a "Guide" badge and no
      // install state, which is exactly the regression this suite guards.
      expect(statuses[id]).not.toBeNull()
      expect(typeof statuses[id].installed).toBe('boolean')

      await card(cardName).waitFor()
    })

    test(`${cardName} shows a pasteable snippet pointing at the live gateway`, async () => {
      const dialog = await openTool(cardName)
      const text = (await dialog.innerText()).replace(/\s+/g, ' ')

      expect(text).toContain(configPath)
      // The origin must come from the running gateway, not the SSR fallback.
      expect(text).toContain(`${app.baseURL}/v1`)
      expect(text).toMatch(/openai-completions/)
      // Both lanes are served; the fallback for a model missing from /model.
      expect(text).toContain('openai-responses')
      expect(text).not.toContain('localhost:20130')
      expect(text).not.toContain('<your-api-key>')
    })
  }

  test('both cards ship artwork that actually loads', async () => {
    await page.goto(`${app.baseURL}/dashboard/cli-tools`)

    for (const { card: cardName, id } of AGENTS) {
      const img = card(cardName).locator('img').first()
      await img.waitFor()
      // A missing /providers/<id>.png falls back to the initials badge via
      // onerror, which still looks like a working card. naturalWidth is what
      // distinguishes the two.
      const rendered = await img.evaluate((el) => ({
        src: (el as HTMLImageElement).getAttribute('src'),
        naturalWidth: (el as HTMLImageElement).naturalWidth,
      }))
      expect(rendered.src).toBe(`/providers/${id}.png`)
      expect(rendered.naturalWidth).toBeGreaterThan(0)
    }
  })
 })