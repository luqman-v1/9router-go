/**
 * Usage › Details renders its panels instead of spinning forever.
 *
 * #510 moved the dashboard read from `onMount` into an `$effect` so a section
 * whose cards are all hidden stops fetching. That rewrite left the success path
 * without a `loading = false`: the `.finally()` guard only cleared the flag when
 * `!settled`, and both `.then()` and `.catch()` set `settled = true` first. The
 * payload arrived, `data` was assigned, and the component still painted
 * "Loading dashboard..." over the panels — the section looked like a read that
 * never finished, and neither Activity Heatmap nor Node Health could be opened.
 *
 * Nothing below the browser catches this. `tsc` reads types, `vite build`
 * bundles, the svelte-check ratchet counts diagnostics, and `go test` serves
 * JSON — all green while the panels never mount. The assertion is therefore the
 * rendered DOM in a real browser: the lifetime cards and both panel headers
 * must exist, and no spinner may remain.
 *
 * Both reads the Details section makes are intercepted with fixed payloads, so
 * the test is about the component rather than about how usage rows are seeded.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { startDashboard } from './harness'

const SLOW = 20_000

/** Enough days for the heatmap grid to draw more than one week column. */
const DASHBOARD_FIXTURE = {
  daily: [
    { date: '2026-10-13', requests: 12, tokens: 2400, cost: 0.01 },
    { date: '2026-10-20', requests: 40, tokens: 8000, cost: 0.04 },
    { date: '2026-10-27', requests: 7, tokens: 900, cost: 0.004 },
  ],
  nodes: [
    {
      provider: 'deepseek',
      name: 'DeepSeek',
      requests: 120,
      errors: 1,
      successRate: 99.2,
      avgLatencyMs: 1200,
      avgTtftMs: 300,
      avgSpeed: 55.6,
      hasPerfData: true,
      score: 96,
      promptTokens: 12000,
      completionTokens: 3400,
      cost: 0.12,
      lastUsed: '2026-10-27T05:00:00Z',
    },
  ],
  models: [
    {
      provider: 'deepseek',
      model: 'deepseek-chat',
      requests: 60,
      errors: 0,
      successRate: 100,
      avgLatencyMs: 1100,
      avgTtftMs: 280,
      avgSpeed: 58.1,
      hasPerfData: true,
      score: 97,
      promptTokens: 6000,
      completionTokens: 1700,
      cost: 0.06,
      lastUsed: '2026-10-27T05:00:00Z',
    },
  ],
  lifetime: {
    totalRequests: 59,
    totalTokens: 11300,
    peakTokens: 8000,
    peakDate: '2026-10-20',
    currentStreak: 2,
    longestStreak: 5,
    topModel: { model: 'deepseek-chat', provider: 'deepseek', tokens: 3400 },
    cacheHitRate: 41.5,
  },
}

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20433)
  await app.page.route('**/api/usage/dashboard', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(DASHBOARD_FIXTURE),
    }),
  )
  await app.page.route('**/api/usage/request-details**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ details: [], total: 0, limit: 50, offset: 0 }),
    }),
  )
  // The heatmap, Node Health and lifetime cards are the three Details panels an
  // unarranged dashboard hides, and the panels start collapsed; both defaults
  // are overridden so the section draws the state the report describes.
  await app.page.addInitScript(() => {
    localStorage.setItem('detailsExpandedByDefault', '1')
    localStorage.setItem('usagePanelLayout', JSON.stringify({ hidden: [], order: {} }))
  })
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('usage details panels', () => {
  test(
    'the section paints its panels instead of a permanent loading card',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByRole('button', { name: /Overview/ }).click()
      await page.getByRole('option', { name: 'Details' }).click()

      // The panel headers only exist once `data` is assigned AND `loading` is
      // false. Under the regression the spinner wins that branch forever, so
      // this wait is the assertion that fails.
      await page.getByText('Activity Heatmap').waitFor({ timeout: SLOW })

      expect(await page.getByText('Loading dashboard...').count()).toBe(0)
      expect(await page.getByText('Node Health').count()).toBe(1)

      // The lifetime cards are drawn from the same payload, so a stuck `data`
      // assignment would show up here too.
      await page.getByText('LIFETIME REQUESTS').waitFor({ timeout: SLOW })
      expect(await page.getByText('59', { exact: true }).count()).toBeGreaterThan(0)

      // The heatmap draws one cell per day of its window; the fixture's three
      // days must produce titled cells rather than an empty grid.
      const cells = page.locator('div[title*="requests"], div[title="No activity"]')
      expect(await cells.count()).toBeGreaterThan(0)

      // Node Health is ranked, so the fixture's scored node is listed.
      await page.getByRole('cell', { name: 'DeepSeek' }).first().waitFor({ timeout: SLOW })
    },
    SLOW,
  )

  test(
    'the heatmap chevron collapses and re-opens the grid',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByRole('button', { name: /Overview/ }).click()
      await page.getByRole('option', { name: 'Details' }).click()
      await page.getByText('Activity Heatmap').waitFor({ timeout: SLOW })

      const cells = page.locator('div[title="No activity"], div[title*=" requests,"]')
      const heatmapToggle = page.getByRole('button', { name: 'Toggle Activity Heatmap' })

      // The setting in beforeAll opens both panels, so the first press closes.
      await cells.first().waitFor({ timeout: SLOW })
      await heatmapToggle.click()
      expect(await heatmapToggle.getAttribute('aria-expanded')).toBe('false')
      expect(await cells.count()).toBe(0)

      // And the second press draws it again — the "does not open" half of the
      // report, which a stuck `loading` flag made permanent.
      await heatmapToggle.click()
      expect(await heatmapToggle.getAttribute('aria-expanded')).toBe('true')
      await cells.first().waitFor({ timeout: SLOW })
    },
    SLOW,
  )
})
