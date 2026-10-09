/**
 * Every menu panel in this dashboard is `position: fixed` and placed against its
 * trigger's measured rect. The top bar's `<header>` carries `backdrop-blur-xl`,
 * and `backdrop-filter` establishes a containing block — so that panel resolved
 * its `left`/`top` against the header's padding box instead of the window. At a
 * 1440px viewport it rendered at `left=1491, top=55`: fully off-screen, and no
 * click ever reached it, so the menu answered with nothing (issue #235).
 *
 * `placePanel` cannot see that, because it only ever sees a rect. The check
 * belongs where the bug landed: on the DOM the panel actually ends up in. This
 * suite walks each panel from its trigger to `<body>` and fails if any ancestor
 * in between establishes a containing block, since that is what silently moves
 * a correctly computed panel off-screen.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20471)
  page = app.page
}, 20_000)

afterAll(async () => {
  await app.stop()
})

/**
 * Opens one menu and returns everything that decides whether it is usable.
 *
 * The two facts are kept apart because the reported bug produced a panel that
 * existed, measured non-zero, and answered nothing: it was painted outside the
 * window, so `elementFromPoint` hit the page behind it rather than an entry.
 */
async function inspectPanel() {
  const panel = page.locator('[role="menu"], [role="listbox"]')
  await panel.waitFor({ timeout: 10_000 })

  return panel.evaluate((el) => {
    const rect = el.getBoundingClientRect()
    const probeX = rect.left + rect.width / 2
    const probeY = rect.top + Math.min(24, rect.height / 2)
    const hit = document.elementFromPoint(probeX, probeY)

    // Every one of these makes the ancestor a containing block for
    // `position: fixed` descendants, which is what silently moves a correctly
    // computed panel off-screen. `none` is the default for all of them, so any
    // other value in the chain between the panel and `<body>` is the bug.
    const containingBlockProperties = [
      'transform',
      'filter',
      'backdrop-filter',
      'perspective',
      'contain',
    ]
    const offenders: string[] = []
    let node: HTMLElement | null = el.parentElement
    while (node && node !== document.body) {
      const style = getComputedStyle(node)
      for (const prop of containingBlockProperties) {
        const value = style.getPropertyValue(prop)
        if (value && value !== 'none') offenders.push(`${node.tagName.toLowerCase()}[${prop}]`)
      }
      node = node.parentElement
    }

    return {
      width: rect.width,
      height: rect.height,
      left: rect.left,
      top: rect.top,
      right: rect.right,
      bottom: rect.bottom,
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      parentIsBody: el.parentElement === document.body,
      offenders,
      hittable: hit ? el.contains(hit) : false,
    }
  })
}

/** Closes whatever the probe opened, so panels never stack across tests. */
async function closePanel() {
  await page.keyboard.press('Escape')
}

describe('menu panels are placed against the viewport', () => {
  // The reported surface. The header is `backdrop-blur-xl`, so before the fix
  // this was the only menu in the dashboard whose panel escaped the window —
  // and it is the one on every page.
  test('the top bar menu opens where the user can see and click it', async () => {
    await page.getByRole('button', { name: 'Account and display options' }).click()

    const panel = await inspectPanel()

    expect(panel.width).toBeGreaterThan(0)
    expect(panel.height).toBeGreaterThan(0)
    expect(panel.left).toBeGreaterThanOrEqual(0)
    expect(panel.top).toBeGreaterThanOrEqual(0)
    expect(panel.right).toBeLessThanOrEqual(panel.viewportWidth)
    expect(panel.bottom).toBeLessThanOrEqual(panel.viewportHeight)
    expect(panel.parentIsBody).toBe(true)
    expect(panel.offenders).toEqual([])
    expect(panel.hittable).toBe(true)

    await closePanel()
  }, 20_000)

  // The other two fixed panels in the dashboard share the placement helper and
  // the same failure mode; both sit inside the Usage page's own header rows.
  test('the Usage section and window pickers open inside the viewport', async () => {
    await page.goto(`${app.baseURL}/dashboard/usage/overview`)
    // Both pickers carry `aria-haspopup="listbox"`; their labels change with
    // the selection ("Overview", "Today"), so the attribute is the stable way
    // to address them rather than text that only matches one window.
    const pickers = page.locator('button[aria-haspopup="listbox"]')
    await pickers.first().waitFor({ timeout: 15_000 })

    for (let i = 0; i < (await pickers.count()); i++) {
      await pickers.nth(i).click()
      const panel = await inspectPanel()

      expect(panel.left).toBeGreaterThanOrEqual(0)
      expect(panel.top).toBeGreaterThanOrEqual(0)
      expect(panel.right).toBeLessThanOrEqual(panel.viewportWidth)
      expect(panel.offenders).toEqual([])
      expect(panel.hittable).toBe(true)

      await closePanel()
    }
  }, 20_000)

  // A row menu inside the `overflow-x-auto` keys table: the reason the panels
  // became `fixed` in the first place (#224). Portalling must not regress that.
  test('an API key row menu stays inside the viewport', async () => {
    await page.goto(`${app.baseURL}/dashboard/keys`)
    await page.getByRole('button', { name: 'Create Key' }).click()
    await page.getByLabel('Key Name').fill('e2e-panel')
    await page.getByLabel('Key Name').press('Enter')
    await page.getByRole('button', { name: 'Done' }).click()
    await page.getByRole('row').filter({ hasText: 'e2e-panel' }).first().waitFor()

    await page.getByRole('button', { name: 'Actions for e2e-panel' }).first().click()
    const panel = await inspectPanel()

    expect(panel.left).toBeGreaterThanOrEqual(0)
    expect(panel.right).toBeLessThanOrEqual(panel.viewportWidth)
    expect(panel.offenders).toEqual([])
    expect(panel.hittable).toBe(true)

    await closePanel()
  }, 20_000)
})