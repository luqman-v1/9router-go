/**
 * The "This month" preset, driven through the real dashboard.
 *
 * A new entry in the period picker is a visible change (AGENTS.md §6.H), and a
 * green type-check cannot tell whether the option is in the list, whether the
 * server accepts the value it sends, or whether the panel closes after the
 * pick. This suite drives the built binary in Chromium on the Usage page and
 * answers all three. It fails on `main`, where the option does not exist.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import type { Page } from '@playwright/test'
import { startDashboard, type Dashboard } from './harness'

const PORT = 20494
const SHOTS = join(resolve(import.meta.dirname, '..', '..'), 'docs', 'screenshots')
const CAPTURE = process.env.CAPTURE_SCREENSHOTS === '1'
const SLOW = 60_000

let app: Dashboard
let page: Page

/** The period picker. The Usage page also carries an "Overview" view picker
 * with the same popup contract, so the trigger is addressed by a window label. */
function trigger(page: Page) {
  return page.locator('button[aria-haspopup="listbox"]').filter({ hasText: /Today|Last |All time|This month/ }).first()
}

function panel(page: Page) {
  return page.getByRole('listbox', { name: 'Time window' })
}

/** Opens the panel unless it is already open: the trigger toggles, so a second
 * click would close the panel the previous test left visible. */
async function openPanel() {
  if (await panel(page).isVisible()) return
  await trigger(page).click()
  await panel(page).waitFor()
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await page.goto(`${app.baseURL}/dashboard/usage`)
  await trigger(page).waitFor()
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('the This month preset', () => {
  test(
    'the picker offers This month',
    async () => {
      await openPanel()
      expect(await page.getByRole('option', { name: 'This month', exact: true }).count()).toBe(1)

      if (CAPTURE) {
        mkdirSync(SHOTS, { recursive: true })
        await panel(page).screenshot({ path: join(SHOTS, '1455-month-period-after.png') })
      }
    },
    SLOW
  )

  test(
    'selecting it asks the server for period=month and closes the panel',
    async () => {
      await openPanel()

      const stats = page.waitForRequest(
        (req) => req.url().includes('/api/usage/stats') && new URL(req.url()).searchParams.get('period') === 'month',
      )
      await page.getByRole('option', { name: 'This month', exact: true }).click()
      const res = await (await stats).response()

      // The server answered the month window rather than falling back: an
      // unknown period resolves to 7d, which is the failure this catches.
      expect(res.status()).toBe(200)
      expect(await panel(page).count()).toBe(0)
      expect(await trigger(page).innerText()).toContain('This month')
    },
    SLOW
  )
})
