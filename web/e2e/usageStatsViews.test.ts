/**
 * The five-second usage poll must not carry the per-model breakdown maps.
 *
 * `/api/usage/stats` builds five breakdown maps. On a ledger with 411 models
 * the response is 415 KB, of which the four per-model maps are ~98% — and the
 * dashboard read it every five seconds for a KPI row that only needs totals.
 * Measured 4.9 MB a minute, 1.7 GB an hour, on a box the project targets at
 * 1 vCPU.
 *
 * The fix is a `?views=` parameter: the poll asks for `none` (totals plus
 * byProvider, which the totals are summed from) and a slower tick refreshes
 * the breakdowns. Both halves need pinning, because either one alone is a bug:
 * a poll that still carries the maps wastes the bandwidth, and a breakdown
 * refresh that never happens leaves the table frozen on the first read.
 *
 * Nothing below the browser sees this. The rendered page is identical, the
 * table is identical, `tsc` and `go test` are green — only the request bodies
 * differ.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { startDashboard } from './harness'

const SLOW = 20_000

/** One provider and one model per breakdown, so every map has content. */
const STATS_FIXTURE = {
  totalRequests: 7,
  totalPromptTokens: 700,
  totalCompletionTokens: 70,
  totalCachedTokens: 0,
  totalCost: 0.07,
  byProvider: { deepseek: { requests: 7, promptTokens: 700, completionTokens: 70, cachedTokens: 0, cost: 0.07 } },
  byModel: {
    'deepseek-chat': {
      requests: 7,
      promptTokens: 700,
      completionTokens: 70,
      cachedTokens: 0,
      cost: 0.07,
      rawModel: 'deepseek-chat',
      provider: 'deepseek',
    },
  },
  byAccount: {},
  byApiKey: {},
  byEndpoint: {},
  activeRequests: [],
  recentRequests: [],
  errorProvider: '',
  pending: {},
}

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20439)
  // The fixture answers both shapes; the assertion is about which query string
  // the page sends, not about what the server would build.
  await app.page.route('**/api/usage/stats**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(STATS_FIXTURE),
    }),
  )
  await app.page.route('**/api/usage/dashboard', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ daily: [], nodes: [], models: [], lifetime: null }),
    }),
  )
  await app.page.route('**/api/usage/request-details**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ details: [], total: 0, limit: 20, offset: 0 }),
    }),
  )
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('usage stats poll payload', () => {
  test(
    'the five-second poll asks for totals only',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByText('Usage & Analytics').waitFor({ timeout: SLOW })

      const urls: string[] = []
      page.on('request', (req) => {
        const url = req.url()
        if (url.includes('/api/usage/stats')) urls.push(url)
      })

      // Two poll ticks. Every read inside this window is a poll; the slower
      // breakdown tick is 30 s and does not land here.
      await page.waitForTimeout(13_000)

      expect(urls.length).toBeGreaterThanOrEqual(2)
      for (const url of urls) {
        expect(url).toContain('views=none')
      }
    },
    SLOW,
  )

  test(
    'the breakdown maps are still refreshed, on the slower tick',
    async () => {
      const page = app.page
      const urls: string[] = []
      page.on('request', (req) => {
        const url = req.url()
        if (url.includes('/api/usage/stats')) urls.push(url)
      })

      // Past the 30 s breakdown tick. A poll that never asked for the maps
      // would leave the table frozen on its first read, which is the failure
      // mode this half of the fix must not introduce.
      await page.waitForTimeout(33_000)

      const fullReads = urls.filter((u) => !u.includes('views='))
      expect(fullReads.length).toBeGreaterThanOrEqual(1)
    },
    45_000,
  )
})
