/**
 * "Import from /models" lost every model below a duplicate name.
 *
 * POST /api/models/custom refused an id that already addressed a model alias
 * with a typed 409 (MODEL_ALIAS_CONFLICT), and the import loop awaited every
 * save inside one try/catch: the first refused id threw past the ids listed
 * after it, so the run ended, the alert carried the raw 409 body, and nothing
 * below the collision was imported. A bare-name alias usually points at the same
 * model on another node, so a single already-aliased id in the list was enough
 * to fail the whole import.
 *
 * Driven as reported: an OpenAI-compatible node whose /models endpoint lists an
 * already-aliased id FIRST, then a fresh one, with the alias in place. The
 * observable contract is both ids landing in /api/models/custom and no refusal
 * dialog — "the ids after the collision are imported" is the regression.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { parseCustomModelsResponse } from '../src/lib/customModels'
import { startDashboard, type Dashboard } from './harness'

/** Already owned by a model alias when the import runs. */
const ALIASED_ID = 'claude-opus-4-6'
/** Listed after the collision: must be imported too. */
const FRESH_ID = 'zzz-fresh-model'

const PORT = 20441
const UPSTREAM_PORT = 20442

const NODE_SUFFIX = 'e2eimportconflict'
const NODE_ID = `openai-compatible-chat-${NODE_SUFFIX}`

let app: Dashboard
let page: Page
/** Stopper for the fake /models endpoint; undefined until it listens. */
let stopUpstream: (() => void) | undefined
/** Every dialog the page raised, so a refusal alert cannot pass unnoticed. */
const dialogs: string[] = []

/** The /models list, aliased id first — where the old loop stopped. */
function modelsBody(): string {
  return JSON.stringify({
    object: 'list',
    data: [{ id: ALIASED_ID }, { id: FRESH_ID }],
  })
}

/** The rows the gateway stored, as "<providerAlias>|<id>|llm" keys. */
async function customModelKeys(): Promise<string[]> {
  const res = await page.request.get(`${app.baseURL}/api/models/custom`)
  return parseCustomModelsResponse(await res.json())
    .filter((m) => typeof m.providerAlias === 'string')
    .map((m) => `${m.providerAlias}|${m.id}|llm`)
}

beforeAll(async () => {
  const server = Bun.serve({
    port: UPSTREAM_PORT,
    fetch(req) {
      if (new URL(req.url).pathname === '/v1/models') {
        return new Response(modelsBody(), { headers: { 'Content-Type': 'application/json' } })
      }
      return new Response('not found', { status: 404 })
    },
  })
  stopUpstream = () => {
    void server.stop(true)
  }

  app = await startDashboard(PORT)
  page = app.page
  page.on('dialog', (dialog) => {
    dialogs.push(dialog.message())
    void dialog.accept()
  })
}, 90_000)

afterAll(async () => {
  await app.stop()
  stopUpstream?.()
})

describe('Import from /models with a duplicate name', () => {
  test('imports every id, including the one a model alias already owns', async () => {
    const node = await page.request.post(`${app.baseURL}/api/provider-nodes`, {
      data: {
        name: 'E2E Import Conflict',
        prefix: 'e2econflict',
        type: 'openai-compatible',
        apiType: 'chat',
        baseUrl: `http://127.0.0.1:${UPSTREAM_PORT}/v1`,
        urlSuffix: NODE_SUFFIX,
      },
    })
    expect(node.ok()).toBe(true)

    const conn = await page.request.post(`${app.baseURL}/api/connections`, {
      data: {
        provider: NODE_ID,
        name: 'e2e-import-conflict',
        apiKey: 'e2e-dummy-key',
        authType: 'apiKey',
        isActive: 1,
      },
    })
    expect(conn.ok()).toBe(true)

    // The occupant: a bare-name alias for the id the upstream list opens with.
    await page.request.put(`${app.baseURL}/api/models/alias`, {
      data: { model: 'openai/gpt-4o', alias: ALIASED_ID },
    })

    await page.goto(`${app.baseURL}/dashboard/providers/${NODE_ID}`)
    // The import is one request per model, and the list opens with the colliding
    // id. Awaiting the save of the id that follows it — rather than a duration —
    // is what says the run reached the end of the list.
    const savedFresh = page.waitForResponse(
      (res) =>
        res.request().method() === 'POST' &&
        new URL(res.url()).pathname === '/api/models/custom' &&
        (res.request().postData() ?? '').includes(FRESH_ID),
      { timeout: 30_000 },
    )

    // The button's accessible name carries its Material Symbols ligature
    // ("download Import from /models"), so an exact name match never hits.
    await page.getByRole('button', { name: /Import from \/models/ }).click()
    expect((await savedFresh).ok()).toBe(true)

    const keys = await customModelKeys()
    expect(keys).toContain(`${NODE_ID}|${ALIASED_ID}|llm`)
    expect(keys).toContain(`${NODE_ID}|${FRESH_ID}|llm`)
    expect(dialogs).toEqual([])
  }, 90_000)
})
