/**
 * The four surfaces #224 collapsed that the API-key suite does not already
 * cover: the top bar, the quota tracker header, the proxy-pool add dialog, and
 * the provider connections page.
 *
 * Each test pins something that could break when rows are unified — a verb that
 * disappeared from the header, a tab that no longer opens the right half of a
 * dialog, a binding left dangling by the collapse. "The menu opened" is
 * deliberately not an assertion here: that is the one that passed while a
 * destructive guard was missing.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

/**
 * Opens the proxy-pool actions menu and picks one entry.
 *
 * Test All, Add Proxy Pool and Batch Import were three header buttons before
 * issue #261; they now sit behind one trigger, so a test that still clicked
 * them by name would find nothing and fail on the wait rather than on the
 * behaviour it is about.
 */
async function pickProxyAction(page: Page, entry: string) {
  await page.getByRole('button', { name: 'Proxy pool actions' }).click()
  await page.getByRole('menu').waitFor()
  await page.getByRole('menuitem', { name: entry }).click()
}

/**
 * Each assertion waits on a real page transition and the binary's first fetch,
 * so bun's 5s default is not enough on a cold CI runner.
 */
const SLOW = 20_000

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20413)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('top bar', () => {
  test(
    'every former standalone control is behind the one menu',
    async () => {
      await page.getByRole('button', { name: 'Account and display options' }).click()

      const menu = page.getByRole('menu')
      await menu.waitFor()

      // These were six buttons in the header strip before #224. Donate, theme,
      // changelog and logout are the ones that must not have been dropped in
      // the move.
      for (const label of ['Donate', /Switch to (light|dark) mode/, 'Change Log', 'Logout']) {
        await menu.getByRole('menuitem', { name: label }).waitFor()
      }

      // Language is the one that went the other way (#240): the dashboard ships
      // English only, and the item's handler was an empty function, so the menu
      // offered a control that could not do anything.
      expect(await menu.getByRole('menuitem', { name: 'Language' }).count()).toBe(0)

      // Leaving the menu open would stack a second panel over the next test's
      // click target, so the inspection closes what it opened.
      await page.keyboard.press('Escape')
    },
    SLOW,
  )

  test(
    'the header strip holds exactly one control',
    async () => {
      // The point of the change: the six-button strip is gone, not merely
      // hidden behind a trigger that still renders them underneath.
      const header = page.locator('header')
      expect(await header.getByRole('button', { name: 'Donate' }).count()).toBe(0)
      expect(
        await header.getByRole('button', { name: 'Account and display options' }).count(),
      ).toBe(1)
    },
    SLOW,
  )

  test(
    'picking a menu entry runs the verb',
    async () => {
      await page.getByRole('button', { name: 'Account and display options' }).click()
      await page.getByRole('menuitem', { name: 'Change Log' }).click()
      // The changelog dialog is the observable consequence of picking that
      // entry. Its heading reads "Changelog" — one word, no space — which is
      // why this cannot match the menu item's "Change Log" label.
      await page.getByRole('heading', { name: 'Changelog', exact: true }).waitFor()
      await page.keyboard.press('Escape')
    },
    SLOW,
  )
})

describe('quota tracker header', () => {
  beforeAll(async () => {
    await page.goto(`${app.baseURL}/dashboard/quota`)
    await page.getByRole('button', { name: 'Quota tracker actions' }).waitFor()
  }, SLOW)

  test(
    'the filters stay visible while the verbs move into the menu',
    async () => {
      // Email masking, expiring-first and both bulk toggles used to sit in this
      // row. They must be reachable, but not as four more header buttons.
      expect(await page.getByRole('button', { name: 'Mask emails' }).count()).toBe(0)
      expect(
        await page.getByRole('combobox', { name: /filter accounts by status/i }).count(),
      ).toBe(1)
    },
    SLOW,
  )

  test(
    'the bulk and refresh verbs live in the menu',
    async () => {
      await page.getByRole('button', { name: 'Quota tracker actions' }).click()
      const menu = page.getByRole('menu')
      await menu.waitFor()

      // Email masking and both bulk toggles plus refresh. Expiring-first and
      // auto-refresh are menuitemcheckboxes and are asserted separately.
      for (const label of [
        /Mask emails|Show full emails/,
        'Disable depleted accounts',
        'Enable accounts with quota',
        'Refresh all',
      ]) {
        await menu.getByRole('menuitem', { name: label }).waitFor()
      }

      for (const label of ['Expiring first', 'Auto-refresh']) {
        await menu.getByRole('menuitemcheckbox', { name: label }).waitFor()
      }

      await page.keyboard.press('Escape')
    },
    SLOW,
  )

  test(
    'the provider filter panel stays on screen at a narrow width',
    async () => {
      await page.setViewportSize({ width: 320, height: 700 })
      await page.getByRole('button').filter({ hasText: 'All providers' }).first().click()

      // The panel carries no id, so it is found by its positioning: the fix
      // made it `fixed` precisely so no ancestor can clip it.
      const box = await page.evaluate(() => {
        const panel = [...document.querySelectorAll('div')].find(
          (d) => getComputedStyle(d).position === 'fixed' && d.clientWidth > 100,
        )
        if (!panel) return null
        const b = panel.getBoundingClientRect()
        return { x: b.x, right: b.x + b.width }
      })

      const viewport = page.viewportSize()
      expect(box).not.toBeNull()
      expect(box!.x).toBeGreaterThanOrEqual(0)
      expect(box!.right).toBeLessThanOrEqual(viewport!.width)

      await page.keyboard.press('Escape')
      await page.setViewportSize({ width: 1440, height: 900 })
    },
    SLOW,
  )
})

describe('proxy pool add dialog', () => {
  beforeAll(async () => {
    // Reached by clicking the sidebar rather than by goto: the SPA router keeps
    // the previously mounted view when the path is entered directly, so a URL
    // navigation would silently assert against the wrong page.
    await page.getByRole('link', { name: 'Proxy Pools' }).click()
    await page.getByRole('button', { name: 'Proxy pool actions' }).waitFor()
  }, SLOW)

  test(
    'the batch button opens the same dialog on the bulk tab',
    async () => {
      // Before #224 these were two modals behind two buttons. Opening Batch
      // Import has to land on the Bulk Add tab of the one dialog, not a second
      // window with its own title.
      await pickProxyAction(page, 'Batch Import')

      const bulkTab = page.getByRole('tab', { name: 'Bulk Add' })
      await bulkTab.waitFor()
      expect(await bulkTab.getAttribute('aria-selected')).toBe('true')
      await page.getByRole('button', { name: 'Cancel' }).click()
    },
    SLOW,
  )

  test(
    'the add button opens the same dialog on the single tab',
    async () => {
      await pickProxyAction(page, 'Add Proxy Pool')

      const singleTab = page.getByRole('tab', { name: 'Single' })
      expect(await singleTab.getAttribute('aria-selected')).toBe('true')
      await page.getByPlaceholder('Office Proxy').waitFor()
      await page.getByRole('button', { name: 'Cancel' }).click()
    },
    SLOW,
  )

  test(
    'switching tabs swaps the form without reopening the dialog',
    async () => {
      await pickProxyAction(page, 'Add Proxy Pool')
      await page.getByRole('tab', { name: 'Bulk Add' }).click()
      // The bulk half is a pasted list, not a form: the proxy URL field must be
      // gone, and the Import button present.
      await page.getByRole('button', { name: 'Import', exact: true }).waitFor()
      expect(await page.getByPlaceholder('http://127.0.0.1:7897').count()).toBe(0)

      await page.getByRole('tab', { name: 'Single' }).click()
      await page.getByPlaceholder('http://127.0.0.1:7897').waitFor()
      await page.getByRole('button', { name: 'Cancel' }).click()
    },
    SLOW,
  )
})

describe('provider connections page', () => {
  test(
    'renders without a script error after the action collapse',
    async () => {
      const scriptErrors: string[] = []
      const onConsole = (msg: { type(): string; text(): string }) => {
        // Only script-level failures. A missing provider icon also logs as a
        // console error, and that 404 is a separate pre-existing defect
        // (recorded in CHANGELOG) — folding it in would make this suite fail
        // for something this change did not cause.
        if (msg.type() !== 'error') return
        if (msg.text().includes('Failed to load resource')) return
        scriptErrors.push(msg.text())
      }
      page.on('console', onConsole)

      await page.getByRole('link', { name: 'Providers' }).click()
      await page.getByRole('button', { name: 'Add Custom Provider' }).waitFor()
      await page.waitForTimeout(1500)
      page.off('console', onConsole)

      // A collapsed action row can leave a verb referencing a removed binding,
      // which only throws when the entry is clicked — so the suite asserts the
      // page is clean and the menu's absence, rather than faking the provider
      // state this database does not have.
      expect(scriptErrors).toEqual([])
      expect(
        await page.getByRole('button', { name: 'Actions for this connection' }).count(),
      ).toBe(0)
    },
    SLOW,
  )
})