/**
 * Usage-chart accessibility (control-plane gap row 5b).
 *
 * This row is the one deliberate exception to the "chi router + SQLite"
 * mechanism the rest of the acceptance matrix uses, and it cannot be moved
 * inside it: all four assertions are properties of the rendered DOM and of
 * keyboard behaviour in a real browser. A Go HTTP handler returns JSON, it does
 * not render Svelte, and it cannot dispatch a keystroke — no handler call can
 * prove that a `<details>`/`<table>` exists or that a tooltip opens on
 * ArrowRight. Row 5a covers the data source behind the table; this file covers
 * the table itself, and neither substitutes for the other.
 *
 * The chart reads /api/usage/chart, which is empty on a fresh database, so the
 * response is intercepted with a fixed three-bucket payload. That keeps the
 * test about the component rather than about how usage rows happen to be
 * seeded, and gives a known row count to compare the table against.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { openUsageTrend, startDashboard } from './harness'

const SLOW = 20_000

/** Three days of usage, one model family each, so the chart has a drawn area. */
const CHART_FIXTURE = [
  { label: 'Oct 19', tokens: 1200, cost: 0.42, byModel: { claude: 1200 } },
  { label: 'Oct 20', tokens: 3400, cost: 1.15, byModel: { claude: 3400 } },
  { label: 'Oct 21', tokens: 900, cost: 0.31, byModel: { claude: 900 } },
]

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20423)
  await app.page.route('**/api/usage/chart*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(CHART_FIXTURE),
    }),
  )
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('usage chart accessibility', () => {
  test(
    'the chart exposes a data table with one row per bucket',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      // The Usage trend panel starts collapsed, so the chart is not in the DOM
      // until its chevron is pressed.
      await openUsageTrend(page)

      // The drawn areas carry no text a screen reader can read, so the same
      // numbers must exist as a table. The table lives inside a collapsed
      // <details>, and a closed <details> hides its content from the
      // accessibility tree — Playwright's role engine therefore matches
      // nothing while it is closed, and the rows' text reads back empty. So
      // the disclosure is opened first, the way a keyboard user reaches it,
      // and the table is then queried by its accessible role and name.
      // The table is only built while the disclosure is open: a long period is
      // thousands of buckets, and a collapsed table is a row and three cells
      // per bucket that a 1-vCPU box still pays for. So the <details> is found
      // by its summary, which exists before the table does.
      const details = page.locator('details').filter({ hasText: 'View table' })
      await details.locator('summary').click()

      const table = page.getByRole('table', { name: 'Usage trend table' })
      await table.waitFor()

      // One body row per bucket — not per series and not one summary row.
      const rows = table.locator('tbody tr')
      expect(await rows.count()).toBe(CHART_FIXTURE.length)

      const labels = await rows.locator('th[scope="row"]').allInnerTexts()
      expect(labels).toEqual(CHART_FIXTURE.map((b) => b.label))
    },
    SLOW,
  )

  test(
    'ArrowRight moves the tooltip to the next bucket',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await openUsageTrend(page)

      // The pointer must not be over the chart. A bucket rect opens the same
      // tooltip on hover as on focus, and a panel that expands under a resting
      // pointer leaves a hover event behind that races the keyboard focus —
      // the tooltip then names a bucket the keyboard never moved to. Parking
      // the pointer in the corner leaves the tooltip to the keyboard alone.
      await page.mouse.move(0, 0)

      // The bucket overlay rects are the keyboard stops: each one announces its
      // bucket and token count.
      const firstPoint = page.getByRole('img', { name: 'Usage trend chart' }).locator('rect[tabindex="0"]').first()
      await firstPoint.focus()

      // Focusing a point opens the tooltip, so the anchor text is the first
      // bucket's label. The wait is a poll rather than a read because the
      // focus handler updates Svelte state and the DOM settles on the next
      // microtask.
      const tooltip = page.getByRole('status')
      await tooltip.waitFor()
      await page.waitForFunction(
        (label) => document.querySelector('[role="status"]')?.textContent?.includes(label) ?? false,
        CHART_FIXTURE[0].label,
      )
      expect(await tooltip.innerText()).toContain(CHART_FIXTURE[0].label)

      // A tooltip reachable only by hovering is unreadable to anyone driving
      // the page from the keyboard. ArrowRight must walk the same points. The
      // wait is a poll rather than a read because the key handler updates
      // Svelte state and the DOM settles on the next microtask.
      //
      // Two presses from a known start, and the assertion is on the FOCUSED
      // element, not just the tooltip. The keydown fires on whatever is
      // focused, so a handler that never moves focus reads its own index
      // again and ArrowRight becomes idempotent after one step — a single
      // press from one start cannot tell that apart from a working walk.
      await firstPoint.press('ArrowRight')
      await page.waitForFunction(
        (label) => document.querySelector('[role="status"]')?.textContent?.includes(label) ?? false,
        CHART_FIXTURE[1].label,
      )
      expect(await tooltip.innerText()).toContain(CHART_FIXTURE[1].label)

      await page.keyboard.press('ArrowRight')
      await page.waitForFunction(
        (label) => document.querySelector('[role="status"]')?.textContent?.includes(label) ?? false,
        CHART_FIXTURE[2].label,
      )
      expect(await tooltip.innerText()).toContain(CHART_FIXTURE[2].label)

      const focusedLabel = await page.evaluate(() => document.activeElement?.getAttribute('aria-label') ?? '')
      expect(focusedLabel).toContain(CHART_FIXTURE[2].label)

      // One tab stop for the chart, not one per bucket: an all-time window is
      // thousands of points, and a keyboard user cannot Tab past them.
      const tabbable = await page
        .getByRole('img', { name: 'Usage trend chart' })
        .locator('rect[tabindex="0"]')
        .count()
      expect(tabbable).toBe(1)
    },
    SLOW,
  )
})
