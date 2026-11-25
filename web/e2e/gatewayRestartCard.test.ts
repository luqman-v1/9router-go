/**
 * Gateway Process card, driven through the real dashboard.
 *
 * AGENTS.md §6.H: a change to what the dashboard shows is not verified by a
 * unit test or a green type-check. The card exists so an operator can restart
 * the gateway without a shell, and its whole safety story is the double gate in
 * front of the button — so this suite drives the real binary in Chromium and
 * asserts both dialogs, dismissing both. It never accepts the pair: that would
 * restart the process the suite is talking to.
 *
 * The capture discharges §6.H.3 and is opt-in through `CAPTURE_SCREENSHOTS=1`.
 * The `before` state is the absence of the card on `main`, which the same
 * locator proves, so no second build is needed to say what changed.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import type { Page } from '@playwright/test'
import { startDashboard, type Dashboard } from './harness'

const PORT = 20484
const REPO_ROOT = resolve(import.meta.dirname, '..', '..')
const SHOTS = join(REPO_ROOT, 'docs', 'screenshots')
const CAPTURE = process.env.CAPTURE_SCREENSHOTS === '1'

let app: Dashboard
let page: Page

/** The card the operator sees. Card forwards only `id`, so the capture is
 * scoped by that rather than by whichever ancestor the DOM happens to return. */
function gatewayCard(page: Page) {
  return page.locator('#gateway-process')
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await page.goto(`${app.baseURL}/dashboard/profile`)
  await page.getByRole('heading', { name: 'Gateway Process' }).waitFor()
}, 120_000)

afterAll(async () => {
  await app.stop()
})

describe('gateway restart card', () => {
  test('the card is reachable and offers the restart control', async () => {
    await expect(page.getByRole('heading', { name: 'Gateway Process' }).isVisible()).resolves.toBe(true)
    await expect(page.getByRole('button', { name: 'Restart Gateway' }).isVisible()).resolves.toBe(true)
    // The card states the cost of the action before it is taken; a button
    // without that sentence is the one thing an operator cannot recover from.
    await expect(page.getByText(/In-flight requests are dropped/).isVisible()).resolves.toBe(true)

    if (CAPTURE) {
      mkdirSync(SHOTS, { recursive: true })
      await gatewayCard(page).screenshot({ path: join(SHOTS, '1451-gateway-restart-after.png') })
    }
  })

  test('dismissing the confirm dialog sends nothing', async () => {
    let dialogs = 0
    const onDialog = async (d: { type: () => string; dismiss: () => Promise<void> }) => {
      dialogs++
      await d.dismiss()
    }
    page.on('dialog', onDialog)
    try {
      await page.getByRole('button', { name: 'Restart Gateway' }).click()
      // One dialog, then the click resolves without a request: the prompt only
      // opens after the confirm is accepted, so dismissing it ends the flow.
      await page.waitForTimeout(300)
      expect(dialogs).toBe(1)
      await expect(page.getByRole('button', { name: 'Restart Gateway' }).isEnabled()).resolves.toBe(true)
    } finally {
      page.off('dialog', onDialog)
    }
  })

  test('accepting the confirm but mistyping the word sends nothing', async () => {
    const seen: string[] = []
    const onDialog = async (d: { type: () => string; message: () => string; accept: (v?: string) => Promise<void> }) => {
      seen.push(d.type())
      // Accept the first (confirm), answer the second (prompt) with the wrong
      // word: the handler returns before it disables the button or calls the
      // API, so a typo can never restart the gateway.
      await d.accept(d.type() === 'prompt' ? 'restart' : undefined)
    }
    page.on('dialog', onDialog)
    try {
      await page.getByRole('button', { name: 'Restart Gateway' }).click()
      await page.waitForTimeout(500)
      expect(seen).toEqual(['confirm', 'prompt'])
      await expect(page.getByRole('button', { name: 'Restart Gateway' }).isEnabled()).resolves.toBe(true)
    } finally {
      page.off('dialog', onDialog)
    }
  })
})
