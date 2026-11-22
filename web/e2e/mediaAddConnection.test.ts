/**
 * Regression test: adding a credential from MediaProviderDetail must send
 * `apiKey` (not `key`) so the Go backend persists the secret on the connection.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'

import { startDashboard, type Dashboard } from './harness'

const SLOW = 30_000

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20487)
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('media provider add connection', () => {
  test(
    'sends apiKey and persists masked secret on the created connection',
    async () => {
      const page = app.page

      await page.goto(`${app.baseURL}/dashboard/media-providers/image`)
      const stabilityCard = page.getByRole('link', { name: /Stability AI/i })
      await stabilityCard.waitFor()
      await stabilityCard.click()

      await page.getByRole('heading', { name: 'Connections' }).waitFor()
      await page.getByRole('button', { name: /Add Connection/i }).click()
      await page.getByRole('heading', { name: 'Add Stability AI Connection' }).waitFor()

      await page.locator('input[placeholder="Enter API key"]').fill('sk-media-e2e-secret')
      await page.locator('input[placeholder="e.g. Primary Account"]').fill('Stability Primary')

      const reqPromise = page.waitForRequest(
        (req) => req.url().endsWith('/api/connections') && req.method() === 'POST',
      )
      const resPromise = page.waitForResponse(
        (res) => res.url().endsWith('/api/connections') && res.request().method() === 'POST',
      )

      await page.getByRole('button', { name: 'Add Connection', exact: true }).click()

      const [req, res] = await Promise.all([reqPromise, resPromise])
      expect(res.ok()).toBe(true)

      const sent = req.postDataJSON() as Record<string, unknown>
      expect(sent.provider).toBe('stability-ai')
      expect(sent.authType).toBe('apikey')
      expect(sent.apiKey).toBe('sk-media-e2e-secret')
      expect(sent.name).toBe('Stability Primary')
      expect('key' in sent).toBe(false)

      await page.getByText('Stability Primary').first().waitFor()

      const conns = await page.evaluate(async () => {
        const r = await fetch('/api/connections')
        return r.json() as Promise<Array<Record<string, unknown>>>
      })
      const created = (Array.isArray(conns) ? conns : []).find(
        (c) => c.provider === 'stability-ai' && c.name === 'Stability Primary',
      )
      if (!created) throw new Error('expected stability-ai connection to be created')
      expect(typeof created.apiKeyMasked).toBe('string')
      expect((created.apiKeyMasked as string).length).toBeGreaterThan(0)
    },
    SLOW,
  )
})
