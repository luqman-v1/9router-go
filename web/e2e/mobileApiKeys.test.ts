/**
 * API-key list layout on a phone-width viewport (#224, follow-up comment).
 *
 * The reported bug was the API-keys table on v1.9.11-exp7: the list is five
 * columns wide by construction, so at 390px the `overflow-x-auto` wrapper held
 * 500px of content inside a 292px box. The policy and actions columns sat
 * outside the viewport with no way to scroll to them, and the secret cell broke
 * one character per line.
 *
 * Below `md` the list renders as cards. These tests assert the thing the bug
 * was about — that nothing is pushed out of the viewport — rather than that a
 * card element exists, which is the assertion that would still pass against a
 * card list that overflowed.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { createKey, keyCard, startDashboard, type Dashboard } from './harness'


let app: Dashboard
let page: Page

// Booting the gateway and creating keys through the modal takes longer than
// bun's 5s default hook budget, so the hooks carry the same allowance the other
// dashboard suites use.
const SLOW = 60_000

beforeAll(async () => {
  app = await startDashboard(20414)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/** Viewport-independent metrics for the API-key list. */
async function layout(page: Page) {
  return page.evaluate(() => {
    const list = document.querySelector('ul.md\\:hidden')
    const table = document.querySelector('table')
    const wrapper = table?.parentElement
    const card = list?.firstElementChild
    return {
      docScrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      listDisplay: list ? getComputedStyle(list as Element).display : null,
      wrapperClientWidth: wrapper?.clientWidth ?? 0,
      wrapperScrollWidth: wrapper?.scrollWidth ?? 0,
      tableDisplay: wrapper ? getComputedStyle(wrapper).display : null,
      cards: list?.children.length ?? 0,
      cardWidth: card?.getBoundingClientRect().width ?? 0,
      // A secret broken one character per line is dozens of lines tall; on one
      // line the cell is well under 40px.
      secretHeight: card?.querySelector('code')?.getBoundingClientRect().height ?? 0,
    }
  })
}

describe('api keys at 390px', () => {
  beforeAll(async () => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${app.baseURL}/dashboard/apikeys`)
    await page.getByRole('heading', { name: 'API Keys' }).waitFor({ timeout: 15_000 })
    await createKey(page, 'Layout Probe A')
    await createKey(page, 'Layout Probe B')
  }, SLOW)

  test('no cell is pushed out of the viewport', async () => {
    // The document's own scrollWidth is NOT the assertion here: the broken
    // build clipped the table inside `overflow-x-auto`, so the document stayed
    // 390px wide while two columns sat unreachably to the right. That is what
    // makes the bug easy to miss — nothing scrolled, so nothing looked broken.
    // The cell rects are the thing that was actually wrong.
    const m = await layout(page)
    expect(m.docScrollWidth).toBeLessThanOrEqual(m.innerWidth)

    const offscreen = await page.evaluate(() => {
      // Either layout, so the same check stays meaningful against a build that
      // still renders the table at this width.
      const root =
        document.querySelector('ul.md\\:hidden') ?? document.querySelector('table')?.parentElement
      if (!root) return null
      const width = window.innerWidth
      return [...root.querySelectorAll('code, button, p, span, td')].flatMap((el) => {
        const r = el.getBoundingClientRect()
        return r.width > 0 && (r.left < 0 || r.right > width) ? [el.textContent?.trim() ?? el.tagName] : []
      })
    })
    expect(offscreen).toEqual([])
  })

  test('the cards list replaces the table below md', async () => {
    const m = await layout(page)

    expect(m.cards).toBe(2)
    expect(m.listDisplay).not.toBe('none')
    expect(m.tableDisplay).toBe('none')
  })

  test('a card fits the viewport instead of scrolling inside it', async () => {
    const m = await layout(page)

    expect(m.cardWidth).toBeGreaterThan(0)
    expect(m.cardWidth).toBeLessThanOrEqual(m.innerWidth)
    // No horizontal scroll box remains at this width, so the card carries every
    // column the table used to hide.
    expect(m.wrapperScrollWidth).toBe(m.wrapperClientWidth)
  })

  test('the secret stays on one line', async () => {
    const m = await layout(page)

    expect(m.secretHeight).toBeLessThan(40)
  })

  test('the row menu opens fully on screen from the card', async () => {
    await page.getByRole('button', { name: 'Actions for Layout Probe A' }).first().click()

    const panel = page.locator('[role="menu"]')
    await panel.waitFor()
    const box = await panel.boundingBox()
    if (!box) throw new Error('api-key menu has no bounding box')
    const viewport = page.viewportSize()

    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport!.width)
    expect(box.y + box.height).toBeLessThanOrEqual(viewport!.height)
  })

  test('a key can be paused from the card menu', async () => {
    const card = keyCard(page, 'Layout Probe B')
    await card.getByRole('button', { name: 'Actions for Layout Probe B' }).click()
    await page.locator('[role="menuitem"]').filter({ hasText: 'Pause key' }).click()

    // bun:test's expect has no toContainText; waitFor with the same predicate is
    // what the other dashboard suites use for a post-mutation read.
    await page.waitForFunction(
      () => document.body.innerText.includes('Paused'),
      undefined,
      { timeout: 10_000 },
    )
    expect((await card.innerText()).trim()).toContain('Paused')
  })
})

describe('api keys at 1440px', () => {
  beforeAll(async () => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(`${app.baseURL}/dashboard/apikeys`)
    await page.getByRole('heading', { name: 'API Keys' }).waitFor({ timeout: 15_000 })
  }, SLOW)

  test('the table is restored at md and above', async () => {
    const m = await layout(page)

    expect(m.tableDisplay).not.toBe('none')
    expect(m.listDisplay).toBe('none')
    expect(m.docScrollWidth).toBeLessThanOrEqual(m.innerWidth)
  })
})