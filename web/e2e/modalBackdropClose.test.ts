/**
 * Dashboard modal backdrop-close, stacking and Persona section titles (#1377).
 *
 * These are DOM/CSS regressions, so none of the structural gates can see them:
 * `tsc`, the svelte-check ratchet and `go test` never render a component, and a
 * screenshot of the surrounding view does not prove a click on the backdrop
 * reaches the handler. This suite boots the real gateway in Chromium and
 * asserts the observable result — the modal disappears, the backdrop computes
 * to z-index 100, and both Persona sections carry a heading.
 *
 * The backdrop handler compares e.target with e.currentTarget, so a click that
 * starts inside the panel must NOT close the modal. That negative assertion is
 * here on purpose: a naive `onclick={close}` on the backdrop would satisfy the
 * positive case while breaking every select and toggle in the dialog.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20477
const SLOW = 60_000

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await page.goto(`${app.baseURL}/dashboard/endpoint`)
  await page.getByRole('button', { name: 'Create Key' }).waitFor({ timeout: SLOW })
}, 90_000)

afterAll(async () => {
  await app?.stop()
})

/** Clicks the bare backdrop in the top-left corner, clear of the centered panel. */
async function clickBackdrop() {
  await page.mouse.click(6, 6)
}

/** Makes the Persona entry reachable; the default preset hides `jailbreak`. */
async function revealPersonaEntry() {
  const status = await page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/api/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ navVisibility: { hidden: [], order: [] } }),
    })
    return res.status
  }, app.baseURL)
  expect(status).toBe(200)
}

describe('dashboard modals', () => {
  test('Create API Key closes on backdrop click, not on a panel click', async () => {
    await page.getByRole('button', { name: 'Create Key' }).click()
    const heading = page.getByRole('heading', { name: 'Create API Key' })
    await heading.waitFor({ timeout: SLOW })

    // A click inside the panel must leave the dialog open.
    await heading.click()
    expect(await heading.isVisible()).toBe(true)

    await clickBackdrop()
    await heading.waitFor({ state: 'detached', timeout: 5_000 })
  })

  test('Create API Key backdrop stacks above the topbar', async () => {
    await page.getByRole('button', { name: 'Create Key' }).click()
    const heading = page.getByRole('heading', { name: 'Create API Key' })
    await heading.waitFor({ timeout: SLOW })

    // The backdrop once used the escaped class `z-\[100\]`, which never became
    // a Tailwind utility and left z-index:auto below the z-50 topbar.
    const z = await page.evaluate(() => {
      const el = [...document.querySelectorAll('div.fixed.inset-0')].find((e) =>
        e.textContent?.includes('Create API Key'),
      )
      return el ? getComputedStyle(el).zIndex : null
    })
    expect(z).toBe('100')

    await clickBackdrop()
    await heading.waitFor({ state: 'detached', timeout: 5_000 })
  })
})

describe('Persona sections', () => {
  test('per-key editor and resolver check both carry a title', async () => {
    await revealPersonaEntry()
    await page.goto(`${app.baseURL}/dashboard/jailbreak`)
    await page.waitForLoadState('domcontentloaded')

    await page.getByRole('heading', { name: 'Per-key bindings' }).waitFor({ timeout: 10_000 })
    await page.getByRole('heading', { name: 'Resolver check' }).waitFor({ timeout: 10_000 })
  }, SLOW)
})
