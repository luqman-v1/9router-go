/**
 * The Codex plan badge in the quota tracker's card header.
 *
 * `wham/usage` reports rate-limit windows and never when the seat lapses, so
 * the tier and its end date come from a second read and are drawn as a badge
 * beside the account name. The read is a third-party call a fixture database
 * cannot answer, so `/api/usage/<id>` is intercepted: that keeps the test about
 * what the card renders rather than about how a live ChatGPT account happens to
 * be seated — the layer the unit tests on `planLabel` cannot reach, since they
 * assert a helper and never render the card.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20501
const SLOW = 30_000

let app: Dashboard
let page: Page

/**
 * Optional element-scoped evidence: set E2E_SHOTS to a directory to keep the
 * card image, which is what the PR attaches.
 */
async function shot(name: string): Promise<void> {
  const dir = process.env.E2E_SHOTS
  if (!dir) return
  await page
    .locator('[data-plan-badge]')
    .first()
    .locator('xpath=ancestor::div[contains(@class,"rounded-[14px]")][1]')
    .screenshot({ path: `${dir}/${name}.png` })
}

/** Seeds one connection through the logged-in session and returns its id. */
async function seed(page: Page, baseURL: string, provider: string, name: string): Promise<string> {
  const res = await page.request.post(`${baseURL}/api/connections`, {
    data: { provider, authType: 'oauth', name, providerSpecificData: { authMethod: 'oauth' } },
  })
  if (!res.ok()) throw new Error(`seed ${provider}: ${res.status()} ${await res.text()}`)
  const body = (await res.json()) as { id?: string; connection?: { id?: string } }
  return body.id ?? body.connection?.id ?? ''
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await seed(page, app.baseURL, 'codex', 'e2e-codex')

  // The usage payload the card reads. `toResponse` spreads `extra` into the
  // top level, and the tracker reads the subscription block off the raw
  // response, so the block sits beside `quotas` exactly as the server sends it.
  await page.route('**/api/usage/*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        plan: 'pro',
        quotas: { session: { used: 10, total: 100, remaining: 90, resetAt: null, unlimited: false } },
        subscription: { plan: 'plus', expiresAt: '2026-12-09T15:04:05Z' },
      }),
    }),
  )
  await page.goto(`${app.baseURL}/dashboard/quota`)
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('plan badge', () => {
  test('draws the tier and the day it lapses', async () => {
    const badge = page.locator('[data-plan-badge]').first()
    await badge.waitFor({ timeout: SLOW })
    const text = await badge.innerText()
    expect(text).toContain('Plus')
    expect(text).toContain('Renews by')
    await shot('plan-badge-codex')
  })

  // The compact ids the endpoint answers with must not reach the screen; the
  // unit test covers the folding, this proves the card calls it.
  test('never draws a raw compact id', async () => {
    await page.unroute('**/api/usage/*')
    await page.route('**/api/usage/*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          plan: 'pro',
          quotas: { session: { used: 10, total: 100, remaining: 90, resetAt: null, unlimited: false } },
          subscription: { plan: 'chatgptplusplan', expiresAt: '2026-12-09T15:04:05Z' },
        }),
      }),
    )
    await page.reload()
    const badge = page.locator('[data-plan-badge]').first()
    await badge.waitFor({ timeout: SLOW })
    const text = await badge.innerText()
    expect(text).toContain('Plus')
    expect(text).not.toContain('chatgptplusplan')
  })
})
