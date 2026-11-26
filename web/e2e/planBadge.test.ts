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
/** The Codex tier the stubbed usage payload reports; a test may change it. */
let codexPlan = 'plus'

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
  const codexId = await seed(page, app.baseURL, 'codex', 'e2e-codex')
  const claudeId = await seed(page, app.baseURL, 'claude', 'e2e-claude')

  // The usage payload the card reads. `toResponse` spreads `extra` into the
  // top level, and the tracker reads the subscription block off the raw
  // response, so the block sits beside `quotas` exactly as the server sends it.
  // One handler reads `codexPlan` so a test can change the Codex tier without
  // re-routing, which would also take the Claude card's own payload with it.
  await page.route('**/api/usage/*', (route) => {
    const isClaude = route.request().url().includes(claudeId)
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        plan: isClaude ? 'Claude Code' : 'pro',
        quotas: isClaude
          ? { 'session (5h)': { used: 10, total: 100, remaining: 90, unlimited: false } }
          : { session: { used: 10, total: 100, remaining: 90, resetAt: null, unlimited: false } },
        subscription: isClaude
          ? { plan: 'Max 20x' }
          : { plan: codexPlan, expiresAt: '2026-12-09T15:04:05Z' },
      }),
    })
  })
  void codexId
  await page.goto(`${app.baseURL}/dashboard/quota`)
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/**
 * Expands every collapsed provider section. Only the first group starts open,
 * and which group that is depends on the sort, so a test that assumed the Codex
 * card was on screen would pass or fail on row order rather than on the badge.
 *
 * Scoped to `section >` on purpose: the sidebar's own disclosure buttons also
 * carry aria-expanded, and clicking the first of those navigates away from the
 * tracker instead of opening a group.
 */
async function expandGroups(): Promise<void> {
  // The sections are drawn from the connection list, which arrives after the
  // first paint: without this wait the count is 0 and the loop is a no-op.
  await page.locator('section > button[aria-expanded]').first().waitFor({ timeout: SLOW })
  const collapsed = page.locator('section > button[aria-expanded="false"]')
  for (let i = 0, n = await collapsed.count(); i < n; i++) await collapsed.first().click()
}

/** The plan badge inside the card whose account name is `name`. */
function badgeFor(name: string) {
  return page
    .locator('div.rounded-\\[14px\\]')
    .filter({ hasText: name })
    .locator('[data-plan-badge]')
}

describe('plan badge', () => {
  test('draws the tier and the day it lapses', async () => {
    await expandGroups()
    const badge = badgeFor('e2e-codex')
    await badge.waitFor({ timeout: SLOW })
    const text = await badge.innerText()
    expect(text).toContain('Plus')
    expect(text).toContain('Renews by')
    await shot('plan-badge-codex')
  }, SLOW)

  // The compact ids the endpoint answers with must not reach the screen; the
  // unit test covers the folding, this proves the card calls it.
  test('never draws a raw compact id', async () => {
    codexPlan = 'chatgptplusplan'
    await page.reload()
    // A reload re-seeds the tracker, so the group collapse state starts over.
    await expandGroups()
    const badge = badgeFor('e2e-codex')
    await badge.waitFor({ timeout: SLOW })
    const text = await badge.innerText()
    expect(text).toContain('Plus')
    expect(text).not.toContain('chatgptplusplan')
  }, SLOW)

  // A Claude seat has no end date: the profile reports the tier and never when
  // it lapses, so the badge must stay tier-only rather than draw year 1.
  test('draws the Claude tier with no end date', async () => {
    await expandGroups()
    const badge = badgeFor('e2e-claude')
    await badge.waitFor({ timeout: SLOW })
    const text = await badge.innerText()
    expect(text).toContain('Max 20x')
    expect(text).not.toContain('Renews by')
  }, SLOW)
})
