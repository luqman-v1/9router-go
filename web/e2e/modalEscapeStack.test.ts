/**
 * Escape on a hand-rolled dialog (#1411 follow-up).
 *
 * `web/src/lib/ui/escapeStack.ts` keeps Escape on the topmost dialog, and the
 * dialogs built on `Modal.svelte` already register there. Four dialogs that
 * render their own overlay (Add/Edit Connection, Changelog, Update) used to add
 * a raw `window` keydown listener instead, so one Escape could close a dialog
 * underneath as well. This suite boots the real gateway in Chromium and proves
 * the rewritten handler still closes the dialog: the structural gates cannot,
 * because a component that registers nothing at all also compiles and lints.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20498
const SLOW = 60_000

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page

  await page.goto(`${app.baseURL}/dashboard/media-providers/image`)
  const stabilityCard = page.getByRole('link', { name: /Stability AI/i })
  await stabilityCard.waitFor({ timeout: SLOW })
  await stabilityCard.click()
  await page.getByRole('heading', { name: 'Connections' }).waitFor({ timeout: SLOW })
}, 90_000)

afterAll(async () => {
  await app?.stop()
})

describe('hand-rolled dialogs and the Escape stack', () => {
  test(
    'Escape closes the Add Connection dialog it registered on the stack',
    async () => {
      const heading = page.getByRole('heading', { name: 'Add Stability AI Connection' })

      await page.getByRole('button', { name: /Add Connection/i }).click()
      await heading.waitFor({ timeout: SLOW })

      await page.keyboard.press('Escape')
      await heading.waitFor({ state: 'hidden', timeout: SLOW })

      // The page underneath is untouched by the dismiss: the dialog closed
      // itself rather than the whole view unmounting.
      expect(await page.getByRole('heading', { name: 'Connections' }).isVisible()).toBe(true)
    },
    SLOW,
  )
})
