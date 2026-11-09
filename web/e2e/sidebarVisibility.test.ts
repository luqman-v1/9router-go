/**
 * Settings › Sidebar: the visibility switch hides the entry it names.
 *
 * The persona PR (#601) rewrote the row's `<Toggle>` to `disabled={true}`, so
 * every switch rendered greyed out and swallowed the click. A `disabled`
 * attribute is invisible to tsc, vite build, svelte-check and go test, so the
 * assertion has to be the rendered DOM.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const SLOW = 20_000
let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20473)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/** Opens Settings and unfolds the advanced section. */
async function openAdvancedSettings() {
  await page.getByRole('link', { name: 'Settings' }).click()
  const disclosure = page.getByRole('switch', { name: 'Advanced settings' })
  await disclosure.waitFor({ timeout: 10_000 })
  if ((await disclosure.getAttribute('aria-checked')) !== 'true') {
    await disclosure.click()
  }
  await page.getByRole('heading', { name: 'Sidebar' }).waitFor({ timeout: 10_000 })
}

/** The visibility switch of one sidebar row. */
function visibilitySwitch(entryLabel: string) {
  return page.getByRole('switch', { name: `${entryLabel} in the sidebar` })
}

/**
 * Publishes the pending change.
 *
 * The hide list is a server-side setting, so the switch only edits the panel's
 * local copy until the settings are saved — and the sidebar reads the saved
 * value.
 */
async function saveSettings() {
  await page.getByRole('button', { name: 'Save Changes' }).click()
  await page.waitForTimeout(1_500)
}

describe('Settings › Sidebar', () => {
  test('a visibility switch is enabled and hides the entry it names', async () => {
    await openAdvancedSettings()

    const quotaSwitch = visibilitySwitch('Quota Tracker')
    await quotaSwitch.waitFor({ timeout: 10_000 })

    expect(await quotaSwitch.isDisabled()).toBe(false)
    expect(await quotaSwitch.getAttribute('aria-checked')).toBe('true')

    // The entry must be on screen, or the test would pass on a switch that
    // hides something already absent.
    await page.getByRole('link', { name: 'Quota Tracker' }).waitFor({ timeout: 10_000 })

    await quotaSwitch.click()

    expect(await quotaSwitch.getAttribute('aria-checked')).toBe('false')
    await saveSettings()
    await page.getByRole('link', { name: 'Quota Tracker' }).waitFor({ state: 'detached', timeout: 10_000 })
  }, SLOW)

  test('the entry comes back when the switch is turned on again', async () => {
    const quotaSwitch = visibilitySwitch('Quota Tracker')
    await quotaSwitch.waitFor({ timeout: 10_000 })
    expect(await quotaSwitch.getAttribute('aria-checked')).toBe('false')

    await quotaSwitch.click()

    expect(await quotaSwitch.getAttribute('aria-checked')).toBe('true')
    await saveSettings()
    await page.getByRole('link', { name: 'Quota Tracker' }).waitFor({ timeout: 10_000 })
  }, SLOW)

  test('the entry the panel lives behind cannot be switched off', async () => {
    const settingsSwitch = visibilitySwitch('Settings')
    await settingsSwitch.waitFor({ timeout: 10_000 })

    expect(await settingsSwitch.isDisabled()).toBe(true)
    expect(await settingsSwitch.getAttribute('aria-checked')).toBe('true')
  }, SLOW)
})

describe('Settings › Display card layout', () => {
  test('the advanced-settings row does not swallow the rows below it', async () => {
    await openAdvancedSettings()

    // A missing `</div>` made the "Advanced settings" row the parent of the
    // provider-toggle row, the animation dropdown and the Currency block, so
    // the card rendered as one nested chain instead of four sibling rows.
    const nesting = await page.evaluate(() => {
      const label = (text: string) =>
        [...document.querySelectorAll('p')].find((node) => node.textContent?.trim() === text)
      const rows = ['Advanced settings', 'Provider card toggle', 'Animation level', 'Currency']
        .map((text) => label(text))
        .filter((node): node is Element => node !== undefined)
      if (rows.length !== 4) return null
      // Each label sits in its own row; the row's parent is the card.
      const rowOf = (node: Element) => node.closest('div.flex.items-center, div.space-y-3')
      const card = rowOf(rows[0])?.parentElement
      if (!card) return null
      return rows.map((node) => rowOf(node)?.parentElement === card)
    })

    expect(nesting).not.toBeNull()
    expect(nesting).toEqual([true, true, true, true])
  }, SLOW)
})
