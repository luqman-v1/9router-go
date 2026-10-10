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

let seededKey = ''

beforeAll(async () => {
  app = await startDashboard(20419)
  page = app.page
  // A fresh instance has no key, so the card would render its placeholder and
  // every paste-shaped assertion below would pass against the placeholder
  // rather than against a real credential. Mint one first.
  seededKey = await page.evaluate(async () => {
    const res = await fetch('/api/keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'E2E Pi Snippet' }),
    })
    return (await res.json()).key as string
  })
  await page.goto(`${app.baseURL}/dashboard/cli-tools`)
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
      // Never a placeholder: the instance has a key, so a pasteable card must
      // carry it. This is the guard that failed while a literal key was
      // compiled into the bundle as a fallback for the empty case.
      expect(text).not.toContain('<your-9router-api-key>')
      expect(text).toContain(seededKey)
      expect(text).not.toContain('localhost:20130')
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
      //
      // `complete` has to be awaited first: waitFor() only guarantees the
      // element is attached, and naturalWidth stays 0 for every image still in
      // flight. Asserting on an undecoded image makes this test pass or fail
      // on network timing rather than on the asset.
      await img.evaluate(
        (el) =>
          new Promise<void>((resolve, reject) => {
            if ((el as HTMLImageElement).complete) return resolve()
            el.addEventListener('load', () => resolve(), { once: true })
            el.addEventListener('error', () => reject(new Error('image failed to load')), { once: true })
          })
      )
      const rendered = await img.evaluate((el) => ({
        src: (el as HTMLImageElement).getAttribute('src'),
        naturalWidth: (el as HTMLImageElement).naturalWidth,
      }))
      expect(rendered.src).toBe(`/providers/${id}.png`)
      expect(rendered.naturalWidth).toBeGreaterThan(0)
    }
  })

  test('the empty-key case renders a placeholder and says so', async () => {
    // Delete the seeded key through the API, then confirm the card stops
    // pretending a credential exists. Without this, a future fallback literal
    // would sail past the pasteable-snippet test above, which only runs with a
    // key present.
    const removed = await page.evaluate(async () => {
      const list = await (await fetch('/api/keys', { credentials: 'include' })).json()
      const del = await fetch(`/api/keys/${encodeURIComponent(list[0].id)}`, {
        method: 'DELETE',
        credentials: 'include',
      })
      return del.status
    })
    expect(removed).toBe(200)

    const dialog = await openTool('Pi')
    const text = (await dialog.innerText()).replace(/\s+/g, ' ')
    expect(text).toContain('<your-9router-api-key>')
    // An empty apiKey is not a placeholder: pi would send the provider without
    // auth and fail at runtime with a far less obvious error.
    expect(text).not.toContain('"apiKey":""')
    expect(text).toContain('No API key exists yet')
    expect(text).toContain('none created')
  })

  test('no compiled bundle ships a sk- key literal', async () => {
    // The regression that motivated this: CliToolsView fell back to a real
    // machine-minted key when the instance had none, which baked a working
    // credential into web/dist and handed it to every dashboard visitor. A
    // unit test cannot see that, so assert on the served bytes.
    const leaked = await page.evaluate(async (base) => {
      const html = await (await fetch(base)).text()
      const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+\.js)"/g)].map((m) => m[1])
      const hits: string[] = []
      for (const asset of assets) {
        const body = await (await fetch(asset)).text()
        for (const m of body.matchAll(/sk-[a-zA-Z0-9]{16,}/g)) hits.push(`${asset}: ${m[0]}`)
      }
      return hits
    }, app.baseURL)

    expect(leaked).toEqual([])
  })
 })