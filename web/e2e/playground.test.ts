/**
 * The Playground.
 *
 * A conversation with the gateway's own dispatch, from the dashboard session,
 * with no engine key. The page's contract lines are asserted from the rendered
 * DOM and the wire:
 *
 *   1. The reply streams in and renders what the gateway returned — the content,
 *      the resolved model and the finish reason. A stub that rendered "ok" for
 *      every upstream would pass a unit test but not this page.
 *   2. The turn goes out as a dashboard call, not a public-lane one: the
 *      browser sends no Authorization header, and the gateway still routes it.
 *   3. A follow-up turn carries the earlier conversation, so the history is the
 *      model's, not a fresh single-turn request.
 *   4. Nothing is persisted: after a turn, web storage holds no conversation,
 *      and a reload starts empty.
 *
 * A fake upstream answers a canned SSE stream, because the assertion is about
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

/** A non-streaming completion, for a body that did not ask to stream. */
function completionBody(): string {
  return JSON.stringify({
    object: 'chat.completion',
    model: MODEL,
    choices: [{ index: 0, message: { role: 'assistant', content: UPSTREAM_REPLY }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 9, completion_tokens: 2, total_tokens: 11 },
  })
}

/**
 * The same answer as an SSE stream, in three deltas: the page has to render
 * text that arrives in pieces, which is the whole point of the streaming path.
 */
function completionStream(options: { reasoning?: boolean } = {}): string {
  const chunk = (delta: Record<string, unknown>, finish: string | null = null) =>
    `data: ${JSON.stringify({
      object: 'chat.completion.chunk',
      model: MODEL,
      choices: [{ index: 0, delta, finish_reason: finish }],
    })}\n\n`
  return (
    chunk({ role: 'assistant' }) +
    (options.reasoning ? chunk({ reasoning_content: 'checking the question first. ' }) : '') +
    chunk({ content: 'upstream ' }) +
    chunk({ content: 'reply' }) +
    `data: ${JSON.stringify({
      object: 'chat.completion.chunk',
      model: MODEL,
      choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
      usage: { prompt_tokens: 9, completion_tokens: 2, total_tokens: 11 },
    })}\n\n` +
    'data: [DONE]\n\n'
  )
}

let app: Dashboard
let page: Page
let stopUpstream: (() => void) | undefined
/** Every body the fake upstream received, in order. */
let upstreamBodies: Record<string, unknown>[] = []

beforeAll(async () => {
  const server = Bun.serve({
    port: UPSTREAM_PORT,
    async fetch(req) {
      const url = new URL(req.url)
      if (url.pathname.endsWith('/chat/completions')) {
        const body = (await req.json()) as Record<string, unknown>
        upstreamBodies.push(body)
        if (body.stream === true) {
          // The first turn carries reasoning so the page's collapsible block is
          // exercised; a follow-up answers plainly, which keeps the history the
          // second request sends to exactly the assistant text asserted below.
          return new Response(completionStream({ reasoning: upstreamBodies.length === 1 }), {
            status: 200,
            headers: { 'Content-Type': 'text/event-stream' },
          })
        }
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
    'streams a turn without an engine key and keeps no conversation in storage',
    async () => {
      upstreamBodies = []

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
      expect(sent.stream).toBe(true)
      expect(Array.isArray(sent.messages)).toBe(true)

      // The streamed reply, the resolved model and the finish reason, rendered.
      await page.getByText(UPSTREAM_REPLY, { exact: true }).waitFor({ timeout: SLOW })
      const panel = await page.evaluate(() => document.body.innerText)
      expect(panel).toContain('deepseek-chat')
      expect(panel).toContain('stop')
      expect(panel).toContain('11 tok')
      // Reasoning the provider emitted is kept, in its own collapsible block.
      expect(panel).toContain('Reasoning')

      // The upstream saw a stream request, not a buffered one.
      expect(upstreamBodies.at(-1)?.stream).toBe(true)

      // A follow-up turn must carry the earlier conversation to the model.
      await page.getByLabel('Prompt to send').fill('And now in one sentence.')
      const secondPromise = page.waitForResponse(
        (r) => r.url().endsWith('/api/playground/chat') && r.request().method() === 'POST',
        { timeout: SLOW },
      )
      await page.getByRole('button', { name: 'Send' }).click()
      expect((await secondPromise).ok()).toBe(true)

      const history = upstreamBodies.at(-1)?.messages as { role: string; content: string }[]
      expect(history.length).toBe(3)
      expect(history[0]).toEqual({ role: 'user', content: 'Summarize the Go memory model.' })
      expect(history[1].role).toBe('assistant')
      expect(history[1].content).toBe(UPSTREAM_REPLY)
      expect(history[2]).toEqual({ role: 'user', content: 'And now in one sentence.' })

      if (CAPTURE) {
        mkdirSync(SHOTS, { recursive: true })
        // Captured after the follow-up: the log holds two exchanges, the
        // reasoning block is open, and the controls the page now offers are all
        // visible — the state a reviewer needs to see.
        await page.getByText('Reasoning', { exact: true }).click()
        await page.locator('main').screenshot({ path: join(SHOTS, '1466-playground-after.png') })
      }

      // Nothing about the conversation is persisted.
      const stored = await page.evaluate(() => ({
        local: Object.keys(localStorage),
        session: Object.keys(sessionStorage),
      }))
      const storageKeys = [...stored.local, ...stored.session].join(' ').toLowerCase()
      expect(storageKeys).not.toContain('playground')
      expect(storageKeys).not.toContain('conversation')
      expect(storageKeys).not.toContain('message')

      // A reload starts from an empty page: the conversation lived in memory.
      await page.reload({ waitUntil: 'domcontentloaded', timeout: SLOW })
      await page.getByLabel('Model to chat with').waitFor({ timeout: SLOW })
      const afterReload = await page.evaluate(() => document.body.innerText)
      expect(afterReload).not.toContain('Summarize the Go memory model.')
    },
    SLOW,
  )
})
