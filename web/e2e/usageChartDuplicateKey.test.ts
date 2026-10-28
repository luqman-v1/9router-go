/**
 * Usage chart: a repeated bucket label must not take the page down.
 *
 * The chart's data table was keyed by the bucket label. A daily bucket is
 * labelled "Jan 2", so a period long enough to cross a year boundary repeats
 * it — and Svelte throws `each_key_duplicate` on the second occurrence, which
 * kills the whole usage page rather than just the table.
 *
 * The chart renders one bucket per day for the "all" period, so the fixture
 * below spans a year boundary: the same label, two buckets.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { openUsageTrend, startDashboard } from './harness'

const SLOW = 20_000

/**
 * Two buckets whose labels are identical — "Jan 2" of two different years —
 * which is what the daily format produces for a period crossing a year
 * boundary.
 */
const DUPLICATE_LABEL_FIXTURE = [
  { label: 'Jan 2', tokens: 1200, cost: 0.42, byModel: { claude: 1200 } },
  { label: 'Jan 2', tokens: 3400, cost: 1.15, byModel: { claude: 3400 } },
]

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20424)
  await app.page.route('**/api/usage/chart*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(DUPLICATE_LABEL_FIXTURE),
    }),
  )
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('usage chart duplicate bucket labels', () => {
  test(
    'a repeated bucket label renders both rows instead of throwing',
    async () => {
      const page = app.page
      const errors: string[] = []
      page.on('pageerror', (err) => errors.push(err.message))

      await page.goto(`${app.baseURL}/dashboard/usage`)
      // The Usage trend panel starts collapsed, so the chart and its table are
      // not in the DOM until the chevron is pressed.
      await openUsageTrend(page)

      // The table is only built while its <details> is open — a long period is
      // thousands of buckets, and the table behind a shut disclosure is a cost
      // the page still paid. So the disclosure is opened first, which is also
      // what a reader does to reach the table.
      const details = page.locator('details').filter({ hasText: 'View table' })
      await details.locator('summary').click()

      // It is then addressed by its locator rather than by role: it is
      // attached but still the content of a disclosure, and the assertion
      // below is about the rows, not about visibility.
      const table = page.locator('table[aria-label="Usage trend table"]')
      await table.waitFor({ state: 'attached' })

      // Both buckets are present. A duplicate key throws before the table is
      // ever rendered, so this is the assertion that would fail.
      const rows = table.locator('tbody tr')
      expect(await rows.count()).toBe(DUPLICATE_LABEL_FIXTURE.length)

      const labels = await rows.locator('th[scope="row"]').allTextContents()
      expect(labels).toEqual(DUPLICATE_LABEL_FIXTURE.map((b) => b.label))

      expect(errors.filter((e) => e.includes('each_key_duplicate'))).toEqual([])
    },
    SLOW,
  )
})
