/**
 * The Usage › Details "View" dialog (issue #729).
 *
 * The dialog is `position: fixed` inside the content column, and the sidebar
 * wrapper is a sibling of that column. While the wrapper kept `z-50` on desktop
 * — where it is back in normal flow — it stayed a stacking context painted
 * above the whole column, so the dialog rendered underneath the sidebar and
 * lost its left edge. The screenshot in the report shows exactly that: the
 * dialog's left half hidden behind the navigation.
 *
 * Nothing below the browser can see this. `tsc` reads types, `vite build`
 * bundles, the svelte-check ratchet counts diagnostics, and `go test` serves
 * JSON — all of them stay green while the dialog is unclickable. So the
 * assertion is a hit test in a real layout: the topmost element at a point
 * inside the dialog has to belong to the dialog, not to the sidebar behind it.
 *
 * The Details table reads /api/usage/request-details, which is empty on a fresh
 * database, so both reads are intercepted with a fixed row. That keeps the test
 * about the layout rather than about how usage rows happen to be seeded.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { startDashboard } from './harness'

const SLOW = 20_000

const DETAIL_ROW = {
  id: 'req-e2e-1',
  timestamp: '2026-10-27T05:00:00Z',
  provider: 'deepseek',
  model: 'deepseek-chat',
  connectionId: 'conn-e2e',
  status: 'success',
  latency: { total: 1234, ttft: 321 },
  tokens: { prompt_tokens: 100, completion_tokens: 50, cached_tokens: 0 },
  account: 'conn-e2e',
  cost: 0.001,
  protocol: 'OpenAI-Chat',
}

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20431)
  await app.page.route('**/api/usage/request-details**', (route) => {
    // The by-id read the dialog makes once it opens; the list read is
    // everything else on the same prefix.
    if (/\/request-details\/[^?]+$/.test(route.request().url())) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ detail: { ...DETAIL_ROW, request: { messages: [] }, response: {} } }),
      })
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ details: [DETAIL_ROW], total: 1, limit: 50, offset: 0 }),
    })
  })
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('request detail payload dialog', () => {
  test(
    'paints above the sidebar, so its left edge stays clickable',
    async () => {
      const page = app.page
      await page.goto(`${app.baseURL}/dashboard/usage`)
      await page.getByRole('button', { name: /Overview/ }).click()
      await page.getByRole('option', { name: 'Details' }).click()
      await page.getByRole('button', { name: 'View', exact: true }).click()
      // The footer button, not the header's aria-label="Close" icon: the dialog
      // carries both, so an exact accessible-name match resolves to two nodes.
      await page.getByText('Close', { exact: true }).waitFor()

      // The dialog is centred in the 1440px window and is wider than the space
      // the sidebar leaves, so its left edge sits over the sidebar's column.
      // That overlap is the whole bug: whatever wins the hit test there is what
      // the user can click, and the dialog has to win.
      const result = await page.evaluate(() => {
        const dialog = [...document.querySelectorAll('div')].find(
          (el) =>
            getComputedStyle(el).position === 'fixed' &&
            el.className.includes('inset-0') &&
            el.textContent?.includes('Request ID'),
        )
        const sidebar = document.querySelector('aside')
        if (!dialog || !sidebar) return { found: false as const }

        const box = dialog.firstElementChild as HTMLElement
        const boxRect = box.getBoundingClientRect()
        const sidebarRect = sidebar.getBoundingClientRect()

        // A point inside the dialog that is also inside the sidebar's column,
        // and inside the dialog box itself — the corner the report shows hidden.
        const x = Math.max(boxRect.left + 4, sidebarRect.left + 4)
        const y = boxRect.top + boxRect.height / 2
        const hit = document.elementFromPoint(x, y)

        return {
          found: true as const,
          // If the layout ever stops overlapping, the point stops testing
          // anything — so the overlap is asserted, not assumed.
          overlapsSidebar: boxRect.left < sidebarRect.right,
          insideDialog: hit !== null && dialog.contains(hit),
          hitClass: hit ? String(hit.className).slice(0, 60) : null,
        }
      })

      expect(result.found).toBe(true)
      expect(result.found && result.overlapsSidebar).toBe(true)
      // The message names what won the hit test, so a regression reads as
      // "the sidebar covered it" rather than as a bare false.
      expect(result.found && result.insideDialog ? 'dialog' : result.found ? result.hitClass : 'missing').toBe(
        'dialog',
      )
    },
    SLOW,
  )
})
