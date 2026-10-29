/**
 * Usage page: the dashboard payload is read once, and the ten-second
 * connection poll stays off the tabs that do not draw connections.
 *
 * Both are invisible below the browser. `tsc` reads types, `vite build`
 * bundles, the svelte-check ratchet counts diagnostics, and `go test` serves
 * JSON — every one stays green while the Usage page re-reads a ~120 KB payload
 * on each section switch and re-fetches 28 KB of connections every ten seconds
 * for a page that draws none of them.
 *
 * The assertions are request counts, because the cost is the request: the
 * rendered page is identical either way.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { startDashboard } from './harness'

const SLOW = 20_000

/** One provider, one model, so the Details panels have something to draw. */
const DASHBOARD_FIXTURE = {
  daily: [
    { date: '2026-10-27', requests: 4, tokens: 800, cost: 0.004 },
    { date: '2026-10-29', requests: 6, tokens: 1200, cost: 0.006 },
  ],
  nodes: [],
  models: [],
  lifetime: {
    totalRequests: 10,
    totalTokens: 2000,
    peakTokens: 1200,
    peakDate: '2026-10-29',
    currentStreak: 2,
    longestStreak: 2,
    topModel: null,
    cacheHitRate: null,
  },
}

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20437)
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
      body: JSON.stringify({ details: [], total: 0, limit: 20, offset: 0 }),
    }),
  )
  // Both Details panels start collapsed and the arrangement hides them by
  // default; the test needs them drawn, because the payload is only read while
  // one of its cards is on screen.
  await app.page.addInitScript(() => {
    localStorage.setItem('detailsExpandedByDefault', '1')
    localStorage.setItem('usagePanelLayout', JSON.stringify({ hidden: [], order: {} }))
  })
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/** Switches the Usage page to one of its four sections. */
async function openSection(label: string): Promise<void> {
  const page = app.page
  await page.getByRole('button', { name: /Overview|Details|Cache Analytics|Compression Analytics/ }).first().click()
  await page.getByRole('option', { name: new RegExp(label) }).click()
}

describe('usage page request economy', () => {
  test(
    'switching sections re-reads the dashboard once, not once per visit',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByText('Usage & Analytics').waitFor({ timeout: SLOW })

      const reads: string[] = []
      page.on('request', (req) => {
        if (req.url().includes('/api/usage/dashboard')) reads.push(req.url())
      })

      // Five switches, three of which land on Details. The first is a genuine
      // read; the rest are served from the module cache because the payload
      // cannot have changed and the TTL has not elapsed.
      for (const section of ['Details', 'Overview', 'Details', 'Cache Analytics', 'Details']) {
        await openSection(section)
        await page.waitForTimeout(900)
      }

      expect(reads.length).toBe(1)
    },
    SLOW,
  )

  test(
    'the connection poll stays off a tab that draws no connections',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByText('Usage & Analytics').waitFor({ timeout: SLOW })

      const polls: string[] = []
      page.on('request', (req) => {
        const url = req.url()
        if (url.includes('/api/connections') || url.includes('/api/provider-nodes')) polls.push(url)
      })

      // Two poll ticks. The Usage page renders nothing from these lists, so
      // neither should be re-fetched; the navigation into the page already
      // loaded them.
      await page.waitForTimeout(23_000)

      expect(polls).toEqual([])
    },
    40_000,
  )
})
