/**
 * The Playground.
 *
 * One chat turn from the dashboard, through the gateway's own dispatch, with
 * no engine key. The page's two contract lines are asserted from the rendered
 * DOM and the wire:
 *
 *   1. The reply renders what the gateway returned — the message, the resolved
 *      model and the finish reason. A stub that rendered "ok" for every
 *      upstream would pass a unit test but not this page.
 *   2. The turn goes out as a dashboard call, not a public-lane one: the
 *      browser sends no Authorization header, and the gateway still routes it
 *      — that is the whole point of the surface.
 *
 * A fake upstream answers a canned completion, because the assertion is about
 * the dashboard wiring, not about a provider.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20501
const UPSTREAM_PORT = 20502
const SLOW = 60_000
const SHOTS = join(resolve(import.meta.dirname, '..', '..'), 'docs', 'screenshots')
const CAPTURE = process.env.CAPTURE_SCREENSHOTS === '1'

/** The canned completion the fake upstream answers with. */
const UPSTREAM_REPLY = 'upstream reply'
const PROVIDER = 'deepseek'
const MODEL = 'deepseek-chat'
const WIRE_MODEL = `${PROVIDER}/${MODEL}`

function completionBody(): string {
  return JSON.stringify({
    object: 'chat.completion',
    model: MODEL,
    choices: [{ index: 0, message: { role: 'assistant', content: UPSTREAM_REPLY }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 9, completion_tokens: 2, total_tokens: 11 },
  })
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
        return new Response(completionBody(), {
          status: 200,
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

  // A connection whose baseUrl is the fake upstream the gateway will dial.
  const conn = await page.request.post(`${app.baseURL}/api/connections`, {
    data: {
      provider: PROVIDER,
      name: 'playground-e2e',
      apiKey: 'sk-upstream',
      authType: 'apiKey',
      isActive: 1,
      data: {
        apiKey: 'sk-upstream',
        baseUrl: `http://127.0.0.1:${UPSTREAM_PORT}/chat/completions`,
      },
    },
  })
  if (!conn.ok()) {
    throw new Error(`seed connection failed: ${conn.status()} ${await conn.text()}`)
  }
}, 120_000)

afterAll(async () => {
  await app?.stop()
  stopUpstream?.()
})

describe('playground', () => {
  test(
    'sends the turn without an engine key and renders the gateway reply',
    async () => {
      // Every entry visible, so the Playground link renders.
      await page.request.put(`${app.baseURL}/api/settings`, {
        data: { navVisibility: { hidden: [], order: [] } },
      })

      await page.goto(`${app.baseURL}/dashboard/playground`, {
        waitUntil: 'domcontentloaded',
        timeout: SLOW,
      })

      await page.getByLabel('Model to chat with').waitFor({ timeout: SLOW })
      await page.getByLabel('Model to chat with').fill(WIRE_MODEL)
      await page.getByLabel('Prompt to send').fill('Summarize the Go memory model.')

      const resPromise = page.waitForResponse(
        (res) => res.url().endsWith('/api/playground/chat') && res.request().method() === 'POST',
        { timeout: SLOW },
      )
      // The button carries the material icon glyph beside its label, so its
      // accessible name is "send Send"; match the label without `exact`.
      await page.getByRole('button', { name: 'Send' }).click()
      const res = await resPromise
      expect(res.ok()).toBe(true)

      // No Authorization header: the dashboard session is the credential, and
      // the playground's job is to work without an engine key.
      const headers = res.request().headers()
      expect(headers.authorization).toBeUndefined()

      const sent = res.request().postDataJSON() as Record<string, unknown>
      expect(sent.model).toBe(WIRE_MODEL)
      expect(Array.isArray(sent.messages)).toBe(true)

      // The reply, the resolved model and the finish reason, rendered.
      await page.getByText(UPSTREAM_REPLY, { exact: true }).waitFor({ timeout: SLOW })
      const panel = await page.evaluate(() => document.body.innerText)
      expect(panel).toContain('deepseek-chat')
      expect(panel).toContain('stop')

      if (CAPTURE) {
        mkdirSync(SHOTS, { recursive: true })
        // The page itself, reply card included: the element that changed.
        await page.locator('main').screenshot({ path: join(SHOTS, '1466-playground-after.png') })
        // The sidebar entry a new tab adds, captured from the same page load.
        await page.locator('aside').screenshot({ path: join(SHOTS, '1466-playground-nav-after.png') })
      }
    },
    SLOW,
  )
})
