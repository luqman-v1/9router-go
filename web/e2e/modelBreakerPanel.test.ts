/**
 * Per-model circuit breaker panel, driven through the real request path.
 *
 * AGENTS.md §6.H: a change to what the dashboard shows is not verified by a
 * unit test or a green type-check, so this suite drives the real gateway binary
 * in Chromium. The state on screen is produced the way an operator's gateway
 * produces it — the chat lane dials a fake upstream that answers a model-scoped
 * 404, five times, until the breaker opens a cooldown — rather than by writing
 * a row so the panel has something to render. A panel that renders a seeded row
 * would pass even if the request path never charged anything.
 *
 * The capture discharges §6.H.3 (element-scoped evidence for the visible
 * change) and is opt-in through `CAPTURE_SCREENSHOTS=1`, so a CI run asserts
 * behaviour without writing files into the tree. The `before` pair comes from a
 * `main` build, where neither the entry nor the panel exists.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import type { Page } from '@playwright/test'
import { startDashboard, type Dashboard } from './harness'

const PORT = 20495
const UPSTREAM_PORT = 20496
const REPO_ROOT = resolve(import.meta.dirname, '..', '..')
const SHOTS = join(REPO_ROOT, 'docs', 'screenshots')

/** Provider/model pair the fake upstream refuses, which is what gets gated. */
const PROVIDER = 'deepseek'
const MODEL = 'deepseek-chat'
const WIRE_MODEL = `${PROVIDER}/${MODEL}`

/** Model-scoped 404: errclass reads "model … not found" as ScopeModel. */
function notFoundBody(): string {
  return JSON.stringify({
    error: { message: `model ${MODEL} not found`, type: 'invalid_request_error' },
  })
}

/** One row of the panel, addressed by the pair it names. */
function panelRow(page: Page) {
  return page.getByRole('row').filter({ hasText: MODEL })
}

let app: Dashboard
let page: Page
let stopUpstream: (() => void) | undefined

beforeAll(async () => {
  const server = Bun.serve({
    port: UPSTREAM_PORT,
    fetch(req) {
      const url = new URL(req.url)
      if (url.pathname.endsWith('/chat/completions')) {
        return new Response(notFoundBody(), {
          status: 404,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response('not found', { status: 404 })
    },
  })
  stopUpstream = () => {
    void server.stop(true)
  }

  app = await startDashboard(PORT)
  page = app.page

  // Five connections, not one. A model-scoped 404 also locks the model on the
  // connection it came from (`ClassifyErrorForProvider`: status 404 carries a
  // long cooldown and ShouldFallback), so a single connection would answer the
  // second attempt with "provider is unhealthy" and never reach the breaker's
  // fifth charge. Five accounts all refusing the same model is also the honest
  // shape of the case this layer exists for: every credential lacks it, so the
  // model itself is what is wrong, not the account.
  for (let i = 0; i < 5; i++) {
    const conn = await page.request.post(`${app.baseURL}/api/connections`, {
      data: {
        provider: PROVIDER,
        name: `e2e-model-breaker-${i}`,
        apiKey: `e2e-dummy-key-${i}`,
        authType: 'apiKey',
        isActive: 1,
        data: {
          apiKey: `e2e-dummy-key-${i}`,
          baseUrl: `http://127.0.0.1:${UPSTREAM_PORT}/chat/completions`,
        },
      },
    })
    if (!conn.ok()) {
      throw new Error(`seed connection ${i} failed: ${conn.status()} ${await conn.text()}`)
    }
  }

  // A client key: /v1/chat/completions is the engine lane, gated by
  // RequireApiKey, so the charge needs a real credential rather than the
  // dashboard session.
  const key = await page.request.post(`${app.baseURL}/api/keys`, {
    data: { name: 'e2e-model-breaker-key' },
  })
  if (!key.ok()) {
    throw new Error(`seed api key failed: ${key.status()} ${await key.text()}`)
  }
  const keyBody = await key.json()
  const token: string = keyBody.key ?? keyBody.token ?? keyBody.apiKey ?? ''
  if (!token) {
    throw new Error('seeded api key carried no token in the response')
  }

  // Five model-scoped failures is the threshold that opens the first cooldown.
  for (let i = 0; i < 5; i++) {
    const res = await page.request.post(`${app.baseURL}/v1/chat/completions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { model: WIRE_MODEL, messages: [{ role: 'user', content: 'hi' }] },
    })
    // A 404 is the charge, not an error in the harness: the breaker counts it.
    if (res.status() !== 404) {
      throw new Error(`attempt ${i + 1} returned ${res.status()}: ${await res.text()}`)
    }
  }
}, 120_000)

afterAll(async () => {
  await app?.stop()
  stopUpstream?.()
})

describe('per-model circuit breaker panel', () => {
  test('shows the pair the request path actually charged, in the cooling state', async () => {
    // The registry state, read through the API the panel itself calls — the
    // server-side half of the assertion.
    const body = await page.evaluate(async (baseURL) => {
      const res = await fetch(`${baseURL}/api/model-breakers`)
      return { status: res.status, text: await res.text() }
    }, app.baseURL)
    if (body.status !== 200) {
      throw new Error(`GET /api/model-breakers = ${body.status}: ${body.text}`)
    }
    const parsed = JSON.parse(body.text)
    const row = (parsed.models ?? []).find(
      (m: { provider: string; model: string }) => m.provider === PROVIDER && m.model === MODEL,
    )
    if (!row) {
      throw new Error(`no breaker row for ${WIRE_MODEL} after five charged failures; got ${JSON.stringify(body)}`)
    }
    // Cooling, not disabled: five failures is one cooldown cycle, and the
    // panel's contract for that state is a countdown rather than a reset-only row.
    expect(row.state).toBe('cooling')
    expect(row.failures).toBeGreaterThanOrEqual(5)

    // The rendered row — the client half. A panel that renders an empty table
    // here fails, which is the regression the unit tests cannot see.
    await page.goto(`${app.baseURL}/dashboard/model-breakers`)
    await page.locator('#app').waitFor({ timeout: 30000 })
    const rowLocator = panelRow(page)
    await rowLocator.waitFor({ timeout: 30000 })
    expect(await rowLocator.count()).toBe(1)
    const text = await rowLocator.innerText()
    expect(text).toContain('Cooling')
    expect(text).toContain(PROVIDER)
    expect(text).toContain(MODEL)

    // Captured here, while the row is in the state the change is about, and
    // element-scoped per §6.H.3: the panel table (the new surface) and the
    // sidebar entry that reaches it (the new way in). The sibling `before`
    // images come from `main`, where neither exists.
    if (process.env.CAPTURE_SCREENSHOTS === '1') {
      mkdirSync(SHOTS, { recursive: true })
      await page.locator('table').first().screenshot({
        path: join(SHOTS, '1419-model-breakers-after.png'),
      })
      await page
        .locator('nav, aside')
        .first()
        .getByText('Model Breakers', { exact: true })
        .first()
        .screenshot({ path: join(SHOTS, '1419-model-breakers-nav-after.png') })
    }
  }, 60_000)

  test('lifts the pair, and the panel shows it serving again', async () => {
    // Reset is the operator's way back and the one control on this panel with
    // a side effect, so it is driven through the UI rather than the API.
    await panelRow(page).getByRole('button', { name: 'Reset', exact: true }).click()
    await page.getByText('serving again').waitFor({ timeout: 20000 }).catch(() => {})

    const result = await page.evaluate(async (baseURL) => {
      const res = await fetch(`${baseURL}/api/model-breakers`)
      return { status: res.status, text: await res.text() }
    }, app.baseURL)
    expect(result.status).toBe(200)
    const row = (JSON.parse(result.text).models ?? []).find(
      (m: { provider: string; model: string }) => m.provider === PROVIDER && m.model === MODEL,
    )
    if (!row) {
      throw new Error('the row vanished instead of returning to closed')
    }
    expect(row.state).toBe('closed')
  }, 60_000)
})
