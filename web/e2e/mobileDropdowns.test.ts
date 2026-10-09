/**
 * Dropdown placement on a phone-width viewport (#224).
 *
 * The reported bug was a Cache Analytics dropdown running off the right edge.
 * The cause was structural: every panel was `position: absolute` inside its
 * trigger's wrapper, so the `overflow-x-auto` table above it clipped the panel
 * and pushed it past the viewport. Panels are now `position: fixed` and clamped
 * by `lib/ui/menuPosition.ts`.
 *
 * These tests assert the panel's bounding box against the viewport, because
 * "the menu opened" is exactly the assertion that passed while the bug was
 * present.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Page
beforeAll(async () => {
  app = await startDashboard(20412)
  page = app.page
})

afterAll(async () => {
  await app.stop()
})

/**
 * Opens a picker and returns the panel's own box relative to the viewport.
 *
 * The panel is measured rather than one of its entries: an entry can sit well
 * inside a horizontally clipped panel and still report a perfectly on-screen
 * box, which is precisely what a half-off-screen menu looks like. Measuring
 * the panel is the assertion the reported bug needed and did not have.
 *
 * The trigger is matched on visible text rather than accessible name, because
 * these buttons render their label as Material Symbols glyphs and carry no
 * aria-label — their computed name is "menu expand_more".
 */
async function panelBox(
  page: Page,
  triggerText: string,
  panelRole: 'listbox' | 'menu',
  triggerLabel?: string,
) {
  // The actions trigger shows no text, so it can only be addressed by its
  // aria-label; the pickers are addressed by their visible text.
  const trigger = triggerLabel
    ? page.getByRole('button', { name: triggerLabel })
    : page.getByRole('button').filter({ hasText: triggerText })
  await trigger.first().click()

  const panel = page.locator(`[role="${panelRole}"]`)
  await panel.waitFor()
  const box = await panel.boundingBox()
  if (!box) throw new Error(`panel for "${triggerText}" has no bounding box`)
  await page.keyboard.press('Escape')
  return box
}
describe('dropdowns at 390px', () => {
  beforeAll(async () => {
    await page.setViewportSize({ width: 390, height: 780 })
    // Pinned to the cache section by URL: at 390px the sidebar sits behind the
    // hamburger drawer, and this suite is about the panels, not the drawer.
    await page.goto(`${app.baseURL}/dashboard/usage/cache`)
    await page.getByRole('button').filter({ hasText: 'Prompt Cache' }).first().waitFor({ timeout: 15_000 })
  })

  test('the section picker stays inside the viewport', async () => {
    const box = await panelBox(page, 'Cache Analytics', 'listbox')
    const viewport = page.viewportSize()

    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
  })

  test('the cache view picker stays inside the viewport', async () => {
    const box = await panelBox(page, 'Prompt Cache', 'listbox')
    const viewport = page.viewportSize()

    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
  })

  test('the cache actions menu stays inside the viewport', async () => {
    const box = await panelBox(page, '', 'menu', 'Cache analytics actions')
    const viewport = page.viewportSize()

    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
  })
})

  // The bug only shows when the trigger sits near the right edge, which is
  // where the header puts its controls once the layout wraps on a phone. The
  // pickers above sit at x≈28, where even an unclamped 224px panel happens to
  // fit — so a suite that only opens those would pass against the broken code.
  // This one pins the trigger to the edge first.
  test('a picker opened from the right edge stays on screen', async () => {
    await page
      .getByRole('button')
      .filter({ hasText: 'Prompt Cache' })
      .first()
      .evaluate((el) => {
        el.style.position = 'fixed'
        el.style.right = '0px'
        el.style.zIndex = '9999'
      })

    const box = await panelBox(page, 'Prompt Cache', 'listbox')
    const viewport = page.viewportSize()

    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
  })

  // A 390px viewport is too forgiving: the panels are 224px wide, so an
  // unclamped one still happens to fit. At 200px it cannot, and the right edge
  // runs off — measured against the build with the clamp removed, this panel
  // reported right=232 on a 200px viewport. That is the regression, so this is
  // the assertion that catches it.
  test('a panel cannot overflow a viewport narrower than itself', async () => {
    await page.setViewportSize({ width: 200, height: 600 })
    await page.getByRole('button').filter({ hasText: 'Prompt Cache' }).first().waitFor()

    const box = await panelBox(page, 'Prompt Cache', 'listbox')
    const viewport = page.viewportSize()

    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
  })