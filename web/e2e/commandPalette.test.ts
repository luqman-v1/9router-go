/**
 * The search palette runs its query only when the operator asks for it.
 *
 * The index holds every model, provider, page and settings section — about
 * 1,700 rows — and a keystroke-driven filter over that re-renders the list
 * between two keypresses, which reads as lag on the small hardware this
 * gateway serves. Typing must stage the query; Enter or the Search button must
 * run it. That contract is invisible to `tsc`, `vite build`, the svelte-check
 * ratchet and `go test`, so the assertion has to be the rendered DOM.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const SLOW = 20_000
let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20477)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/** Opens the palette the way an operator does: the keyboard shortcut. */
async function openPalette() {
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByRole('heading', { name: 'Search' }).waitFor({ timeout: 10_000 })
}

function queryInput() {
  return page.locator('input[aria-label="Search query"]')
}

describe('Search palette', () => {
  test('typing stages the query without running it', async () => {
    await openPalette()

    await queryInput().fill('quota')
    // No result list: the pass has not happened.
    await page.waitForTimeout(400)
    expect(await page.locator('ul[role="listbox"] li').count()).toBe(0)

    // Enter runs it.
    await page.keyboard.press('Enter')
    await page.waitForTimeout(400)
    expect(await page.locator('ul[role="listbox"] li').count()).toBeGreaterThan(0)
  }, SLOW)

  test('the Search button runs the query too', async () => {
    // A fresh query, so the previous test's results are not what is on screen.
    await queryInput().fill('anthropic')
    await page.waitForTimeout(300)
    expect(await page.locator('ul[role="listbox"] li').count()).toBe(0)

    await page.locator('.fixed.inset-0.z-50 button:has-text("Search")').click()
    await page.waitForTimeout(400)
    expect(await page.locator('ul[role="listbox"] li').count()).toBeGreaterThan(0)
  }, SLOW)

  test('a model query finds models and a page query finds the page first', async () => {
    await queryInput().fill('claude sonnet')
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    const rows = page.locator('ul[role="listbox"] li')
    expect(await rows.count()).toBeGreaterThan(0)
    // The first row is a model, because no page or settings row matches.
    expect(await rows.first().innerText()).toContain('MODELS')

    await queryInput().fill('quota tracker')
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    // A page outranks a model, so the row an operator wants is the first one.
    expect(await rows.first().innerText()).toContain('Quota Tracker')
    expect(await rows.first().innerText()).toContain('PAGES')
  }, SLOW)

  test('a result navigates and closes the palette', async () => {
    await queryInput().fill('quota tracker')
    await page.keyboard.press('Enter')
    await page.waitForTimeout(400)
    await page.locator('ul[role="listbox"] li button').first().click()
    await page.waitForURL('**/dashboard/quota', { timeout: 10_000 })
    expect(await page.getByRole('heading', { name: 'Search' }).count()).toBe(0)
  }, SLOW)
})

describe('Pinned entries', () => {
  test('a pin survives a reload and an unpin removes it', async () => {
    await openPalette()
    await queryInput().fill('usage')
    await page.keyboard.press('Enter')
    await page.waitForTimeout(400)

    await page.locator('ul[role="listbox"] li button[title*="Pin"]').first().click()
    await page.waitForTimeout(300)
    expect(await page.evaluate(() => localStorage.getItem('9router-nav-pinned'))).toContain(
      'analytics'
    )

    await page.keyboard.press('Escape')
    await page.waitForTimeout(500)
    // The section is drawn above the groups, with the row it names.
    expect(await page.locator('nav p', { hasText: 'PINNED' }).count()).toBe(1)
    expect(await page.locator('nav a:has-text("Usage")').count()).toBeGreaterThan(0)

    await page.reload()
    await page.waitForTimeout(1_500)
    expect(await page.locator('nav p', { hasText: 'PINNED' }).count()).toBe(1)

    await openPalette()
    await queryInput().fill('usage')
    await page.keyboard.press('Enter')
    await page.waitForTimeout(400)
    await page.locator('ul[role="listbox"] li button[title*="Unpin"]').first().click()
    await page.waitForTimeout(300)
    await page.keyboard.press('Escape')
    await page.waitForTimeout(500)
    expect(await page.locator('nav p', { hasText: 'PINNED' }).count()).toBe(0)
  }, SLOW)
})

describe('Collapsible groups', () => {
  test('a labelled group folds, and the group holding the active tab stays open', async () => {
    // Groups start open: a build that never wrote a collapsed list draws every
    // group the way it always did.
    const advanced = page.locator('nav button:has-text("ADVANCED FEATURES")')
    await advanced.waitFor({ timeout: 10_000 })
    expect(await advanced.getAttribute('aria-expanded')).toBe('true')
    expect(await page.locator('nav a:has-text("Security")').count()).toBe(1)

    await advanced.click()
    await page.waitForTimeout(500)
    expect(await advanced.getAttribute('aria-expanded')).toBe('false')
    expect(await page.locator('nav a:has-text("Security")').count()).toBe(0)

    // The fold survives a reload, because the sidebar is the surface the
    // operator arranges rather than a dialog they dismiss.
    await page.reload()
    await page.waitForTimeout(1_500)
    expect(await advanced.getAttribute('aria-expanded')).toBe('false')

    // Navigating to a page inside the folded group reopens it: folding it would
    // hide the page the operator is looking at.
    await page.goto(`${app.baseURL}/dashboard/security`)
    await page.waitForTimeout(1_500)
    expect(await advanced.getAttribute('aria-expanded')).toBe('true')
    expect(await page.locator('nav a:has-text("Security")').count()).toBe(1)
  }, SLOW)
})

describe('Sidebar badges', () => {
  test('a count appears beside the entry it describes', async () => {
    // The gateway starts empty, so no badge is drawn yet — a badge is a fact
    // the operator would otherwise open a page to learn, and "0" is not one.
    await page.goto(`${app.baseURL}/dashboard/endpoint`)
    await page.waitForTimeout(1_500)
    expect(await page.locator('nav span.tabular-nums').count()).toBe(0)

    // One API key, so the Endpoint & Key row earns a badge with a sentence a
    // hover and a screen reader both read.
    await page.getByRole('button', { name: 'Create Key' }).click()
    await page.locator('#key-name-input').fill('badge-e2e')
    await page.getByRole('button', { name: 'Create', exact: true }).click()
    await page.waitForTimeout(2_000)

    const badge = page.locator('nav a:has-text("Endpoint & Key") span.tabular-nums').first()
    await badge.waitFor({ timeout: 10_000 })
    expect(await badge.innerText()).toBe('1')
    expect(await badge.getAttribute('title')).toBe('1 API key issued')

    // The "key created" dialog shows the secret once and stays up until Done.
    // It is a plain overlay with no Escape handler, so it is dismissed by its
    // own button — leaving it up would block the next test's clicks.
    await page.getByRole('button', { name: 'Done' }).click()
    await page.waitForTimeout(500)
  }, SLOW)
})

describe('Collapse to icons', () => {
  test('the rail shrinks the sidebar and the switch restores it', async () => {
    await page.locator('nav a:has-text("Settings")').first().click()
    await page.waitForTimeout(1_000)
    const disclosure = page.getByRole('switch', { name: 'Advanced settings' })
    if ((await disclosure.getAttribute('aria-checked')) !== 'true') await disclosure.click()
    await page.waitForTimeout(600)

    const rail = page.getByRole('switch', { name: 'Collapse to icons' })
    await rail.waitFor({ timeout: 10_000 })
    // Off by default: the layout hides every label at once, so it is opted into.
    expect(await rail.getAttribute('aria-checked')).toBe('false')

    const wide = (await page.locator('aside').boundingBox())?.width ?? 0
    await rail.click()
    await page.waitForTimeout(800)
    const narrow = (await page.locator('aside').boundingBox())?.width ?? 0
    expect(narrow).toBeLessThan(wide)
    // The links are still there — only their labels are gone.
    expect(await page.locator('nav a').count()).toBeGreaterThan(0)

    await rail.click()
    await page.waitForTimeout(800)
    expect((await page.locator('aside').boundingBox())?.width).toBe(wide)
  }, SLOW)
})
