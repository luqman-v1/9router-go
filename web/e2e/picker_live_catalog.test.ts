/**
 * Issue #1556: the model picker listed the CLI-tool providers (commandcode,
 * claude, zed, cursor, cline/clinepass) from the static registry bundle, so a
 * model added upstream was unreachable in the combo editor until the next
 * snapshot. The picker now reads each connected provider's live catalogue, and
 * the gateway resolves every entry's capability block before it reaches the
 * browser.
 *
 * The observable contract is the pill: a live-only id renders under its
 * provider group, carrying the vision and reasoning marks the gateway resolved.
 * Proven failing-on-base: with the pre-change bundle the same flow issues no
 * `GET /api/providers/<id>/models` at all (the picker never asked), so the pill
 * cannot render; the id is absent from the static bundle either way.
 * The catalogue response is fulfilled at the network edge: the CommandCode
 * catalogue URL is a constant in the provider registry, so there is no upstream
 * to point at a fake. What is under test is the wiring the browser owns — the
 * picker issues GET /api/providers/<id>/models for a connected live provider
 * and renders what comes back, caps included.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20473

/** Absent from the static commandcode catalogue. */
const LIVE_ONLY_ID = 'vendor/live-only-model-1556'
const LIVE_ONLY_NAME = 'Live Only 1556'

let app: Dashboard
let page: Page
let activeConnId = ''
let activeConnId2 = ''
let inactiveConnId = ''

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page

  const conn = await page.request.post(`${app.baseURL}/api/connections`, {
    data: {
      provider: 'commandcode',
      name: 'e2e-live-catalog',
      apiKey: 'user_e2e_live_catalog',
      authType: 'apiKey',
      isActive: 1,
    },
  })
  expect(conn.ok()).toBe(true)
  activeConnId = (await conn.json()).id

  // A second active account for the same live-catalog provider, carrying a
  // disjoint model plus one shared with the first account (review blocker
  // #1580: every active connection must be read, duplicates collapsed).
  const conn2 = await page.request.post(`${app.baseURL}/api/connections`, {
    data: {
      provider: 'commandcode',
      name: 'e2e-live-catalog-2',
      apiKey: 'user_e2e_live_catalog_2',
      authType: 'apiKey',
      isActive: 1,
    },
  })
  expect(conn2.ok()).toBe(true)
  activeConnId2 = (await conn2.json()).id

  // An inactive account whose model must never reach the picker. The create
  // endpoint always inserts an active row, so deactivate it explicitly.
  const conn3 = await page.request.post(`${app.baseURL}/api/connections`, {
    data: {
      provider: 'commandcode',
      name: 'e2e-live-catalog-inactive',
      apiKey: 'user_e2e_live_catalog_inactive',
      authType: 'apiKey',
    },
  })
  expect(conn3.ok()).toBe(true)
  inactiveConnId = (await conn3.json()).id
  const deactivate = await page.request.put(
    `${app.baseURL}/api/connections/${inactiveConnId}`,
    { data: { isActive: false } }
  )
  expect(deactivate.ok()).toBe(true)
}, 90_000)

afterAll(async () => {
  await app.stop()
})

/** Opens the combo editor's model picker and waits for the pill list. */
async function openPicker(): Promise<void> {
  await page.goto(`${app.baseURL}/dashboard/combos`)
  await page.getByRole('button', { name: /Create Combo/i }).first().click()
  await page.getByRole('button', { name: /Add Models Manually/ }).click()
  await page.getByPlaceholder(/Search models/).waitFor({ state: 'visible', timeout: 30_000 })
}

describe('model picker reads the live catalogue (#1556)', () => {
  test(
    'renders a live-only CommandCode model with the caps the gateway resolved',
    async () => {
      const calls: string[] = []

      await page.route('**/api/providers/*/models', async (route) => {
        calls.push(route.request().url())
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            provider: 'commandcode',
            connectionId: 'e2e',
            models: [
              {
                id: LIVE_ONLY_ID,
                name: LIVE_ONLY_NAME,
                contextLength: 262144,
                maxOutput: 8192,
                isVL: true,
                isReasoning: true,
                thinking_levels: ['low', 'high'],
              },
            ],
          }),
        })
      })

      await openPicker()

      const pill = page.getByRole('button', { name: new RegExp(LIVE_ONLY_NAME) })
      await pill.waitFor({ state: 'visible', timeout: 30_000 })

      // The picker asked the gateway for the live catalogue...
      expect(calls.length).toBeGreaterThan(0)
      // ...and the pill it drew is the live-only id, not a static-bundle entry.
      expect(await pill.count()).toBe(1)
      // The resolved capability block reaches the pill's marks.
      expect(await pill.getByTitle('Vision — Supports image input').count()).toBe(1)
      expect(await pill.getByTitle('Reasoning / Thinking').count()).toBe(1)
    },
    120_000
  )

  test(
    'reads every active connection and dedupes shared models (#1580)',
    async () => {
      const SHARED_ID = 'vendor/shared-model-1580'
      const SHARED_NAME = 'Shared 1580'
      const DISJOINT_A = 'vendor/disjoint-a-1580'
      const DISJOINT_A_NAME = 'Disjoint A 1580'
      const DISJOINT_B = 'vendor/disjoint-b-1580'
      const DISJOINT_B_NAME = 'Disjoint B 1580'
      const INACTIVE_ONLY = 'vendor/inactive-only-1580'
      const INACTIVE_ONLY_NAME = 'Inactive Only 1580'
      const requested: string[] = []

      // Serve a different catalogue per connection id: two active accounts
      // carry disjoint models plus one shared id; the inactive account carries
      // a model that must never surface.
      await page.route('**/api/providers/*/models', async (route) => {
        const url = route.request().url()
        requested.push(url)
        const connId = url.split('/api/providers/')[1]?.split('/')[0] ?? ''
        const byConn: Record<string, Array<{ id: string; name: string }>> = {
          [activeConnId]: [
            { id: DISJOINT_A, name: DISJOINT_A_NAME },
            { id: SHARED_ID, name: SHARED_NAME },
          ],
          [activeConnId2]: [
            { id: DISJOINT_B, name: DISJOINT_B_NAME },
            { id: SHARED_ID, name: SHARED_NAME },
          ],
          [inactiveConnId]: [{ id: INACTIVE_ONLY, name: INACTIVE_ONLY_NAME }],
        }
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            provider: 'commandcode',
            connectionId: connId,
            models: byConn[connId] ?? [],
          }),
        })
      })

      await openPicker()

      // Both disjoint models render...
      const pillA = page.getByRole('button', { name: new RegExp(DISJOINT_A_NAME) })
      const pillB = page.getByRole('button', { name: new RegExp(DISJOINT_B_NAME) })
      await pillA.waitFor({ state: 'visible', timeout: 30_000 })
      await pillB.waitFor({ state: 'visible', timeout: 30_000 })
      expect(await pillA.count()).toBe(1)
      expect(await pillB.count()).toBe(1)

      // ...the shared model appears exactly once (no duplicate row)...
      const shared = page.getByRole('button', { name: new RegExp(SHARED_NAME) })
      expect(await shared.count()).toBe(1)

      // ...and the inactive connection's model is absent.
      const inactive = page.getByRole('button', { name: new RegExp(INACTIVE_ONLY_NAME) })
      expect(await inactive.count()).toBe(0)

      // Both active connections were queried; the inactive one was not.
      expect(requested.some((u) => u.includes(activeConnId))).toBe(true)
      expect(requested.some((u) => u.includes(activeConnId2))).toBe(true)
      expect(requested.some((u) => u.includes(inactiveConnId))).toBe(false)
    },
    120_000
  )
})