/**
 * The DeepSeek Web tile read "No connections" beside a live connection.
 *
 * The API Key Providers section renders the browser-login providers too, but
 * scored every card against a fixed ['apikey','api_key'] list. A browser-login
 * credential is stored as `authType: 'cookie'` — the add modal derives that from
 * the catalog's `webCookie` category — so the card counted zero while the
 * detail page, which filters by provider alone, listed the connection.
 *
 * Driven through the dashboard's own create route and read off the rendered
 * tile: a unit test on the section list would still pass if a caller passed the
 * fixed list again, which is the regression. The tile asserting "Connected" is
 * the observable contract.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20421)
  page = app.page
}, 60_000)

afterAll(async () => {
  await app.stop()
})

/** The grid tile for one provider, keyed by the name the card prints. */
function providerTile(page: Page, name: string) {
  return page.locator('[role="button"]').filter({ hasText: name }).first()
}

describe('browser-login provider tile (#352)', () => {
  test('a cookie connection is counted on the DeepSeek Web card', async () => {
    const created = await page.request.post(`${app.baseURL}/api/connections`, {
      data: {
        provider: 'deepseek-web',
        name: 'ds-web-live',
        apiKey: 'user-token-for-ui-test',
        authType: 'cookie',
        isActive: 1,
      },
    })
    expect(created.ok()).toBe(true)

    // The section renders at the bottom of the grid and the tile is behind the
    // "show all" fold, so the view has to be reloaded after the write for the
    // list to pick the connection up.
    await page.goto(`${app.baseURL}/dashboard/providers`)
    const tile = providerTile(page, 'DeepSeek Web')
    await tile.waitFor({ timeout: 15_000 })

    const text = (await tile.innerText()).replace(/\s+/g, ' ')
    expect(text).not.toContain('No connections')
    expect(text).toContain('1 Connected')
  }, 60_000)
})
