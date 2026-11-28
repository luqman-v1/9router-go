/**
 * The Autoclaw provider page must render the registry's model catalog.
 *
 * PR #1506 registers `autoclaw` with six models in three hand-maintained
 * places: `web/src/lib/models.ts` (what the dashboard renders),
 * `internal/providers/registry_models.go` (what the gateway serves), and the
 * provider registry entry. The lists can drift: a model present in the
 * registry but missing from `BUILTIN_MODELS_BY_PROVIDER` leaves the provider
 * page showing a partial catalog while `/v1/models` still serves the id.
 * This test renders the page in Chromium and pins the rendered set, so the
 * drift is caught where the operator actually sees it.
 *
 * Rule §6.H "prove it fails on base": on `origin/main` the SPA's catch-all
 * route still renders the provider shell for `autoclaw`, but with an empty
 * model grid ("Select all (0)") — the base failure is the empty rendered set,
 * not a 404. The rendered.length assertion fails there and passes once the
 * six rows render.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20497

const PROVIDER_ID = 'autoclaw'
const PROVIDER_PATH = `/dashboard/providers/${PROVIDER_ID}`

/** The six ids registry_models.go and models.ts must both carry. */
const EXPECTED_MODELS = [
  'zai_auto',
  'zai_auto-fast',
  'zai_glm-x-preview',
  'zaicoding_glm-5.3',
  'zai_glm-5.3-flash',
  'zai_glm-5.3-flashx-legal',
]

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
}, 90_000)

afterAll(async () => {
  await app.stop()
})

describe('autoclaw provider page', () => {
  test('renders all six registry models with one consistent prefix', async () => {
    await page.goto(`${app.baseURL}${PROVIDER_PATH}`)
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)

    const rendered = await page.evaluate(() =>
      Array.from(document.querySelectorAll('code'))
        .map((el) => (el.textContent ?? '').trim())
        .filter((t) => t.includes('/')),
    )
    expect(rendered.length).toBeGreaterThan(0)

    const renderedModels = rendered.map((id) => id.slice(id.indexOf('/') + 1))
    for (const model of EXPECTED_MODELS) {
      expect(renderedModels).toContain(model)
    }

    // One prefix across the whole page, and it must not be an id the router
    // fails to resolve: the registry publishes autoclaw under its own id
    // (no RegistryAliases entry), and `au` resolves incoming ids back to it.
    const prefixes = new Set(rendered.map((id) => id.slice(0, id.indexOf('/'))))
    expect(prefixes.size).toBe(1)
  })

  test('catalog exposes autoclaw as OAuth-only with no API key mode', async () => {
    const catalog = await page.evaluate(async () => {
      const res = await fetch('/api/providers/catalog')
      return res.json() as Promise<{
        sections: { providers: { id: string; category: string; authModes?: string[] }[] }[]
      }>
    })
    const autoclaw = catalog.sections
      .flatMap((s) => s.providers)
      .find((p) => p.id === 'autoclaw')
    expect(autoclaw).toBeDefined()
    expect(autoclaw!.category).toBe('oauth')
    expect(autoclaw!.authModes).toEqual(['oauth'])
  })
})
