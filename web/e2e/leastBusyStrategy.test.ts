/**
 * Least-busy strategy option, driven through the real dashboard.
 *
 * The strategy is only reachable if the operator can pick it, and a `<option>`
 * is exactly the kind of visible change a green type-check does not prove
 * (AGENTS.md §6.H). This drives the built binary in Chromium, asserts the
 * option is present and selectable, and captures the card when asked.
 *
 * The `before` state is the absence of the option on `main` — the same locator
 * proves it — so no second build is needed to say what changed.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import type { Page } from '@playwright/test'
import { startDashboard, type Dashboard } from './harness'

const PORT = 20485
const REPO_ROOT = resolve(import.meta.dirname, '..', '..')
const SHOTS = join(REPO_ROOT, 'docs', 'screenshots')
const CAPTURE = process.env.CAPTURE_SCREENSHOTS === '1'

let app: Dashboard
let page: Page

/** The card holding the strategy picker: the select's own settings section. */
function strategyCard(page: Page) {
  return page.locator('#fallback-strat').locator('xpath=ancestor::div[contains(@class,"rounded-")][1]')
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await page.goto(`${app.baseURL}/dashboard/profile`)
  await page.locator('#fallback-strat').waitFor()
}, 120_000)

afterAll(async () => {
  await app.stop()
})

describe('least-busy routing strategy', () => {
  test('the strategy picker offers least-busy', async () => {
    const option = page.locator('#fallback-strat option[value="least-busy"]')
    await expect(option.count()).resolves.toBe(1)
    // The label names what the strategy does, not its wire value: an operator
    // choosing between four strategies needs the sentence, not the slug.
    await expect(option.innerText()).resolves.toContain('least-loaded')
  })

  test('selecting it binds the value the server reads', async () => {
    // Selecting is a real user action on the page, not a rewritten DOM: the
    // closed control then displays the chosen strategy, which is what the
    // capture below shows.
    await page.locator('#fallback-strat').selectOption('least-busy')
    await expect(page.locator('#fallback-strat').inputValue()).resolves.toBe('least-busy')
    // innerText() of the control returns every option, so the assertion reads
    // the selected one.
    await expect(page.locator('#fallback-strat option:checked').innerText()).resolves.toContain('Least Busy')

    if (CAPTURE) {
      mkdirSync(SHOTS, { recursive: true })
      await strategyCard(page).screenshot({ path: join(SHOTS, '1454-least-busy-strategy-after.png') })
    }
  })

  test('the neighbouring strategies are unchanged', async () => {
    // Adding a case must not drop the existing ones: the picker is the only
    // place an operator sets the global strategy.
    for (const value of ['failover', 'round-robin', 'sticky-round-robin']) {
      await expect(page.locator(`#fallback-strat option[value="${value}"]`).count()).resolves.toBe(1)
    }
  })
})
