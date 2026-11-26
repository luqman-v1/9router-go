/**
 * "Import from /models" stages the fetch in a preview panel instead of writing
 * every model into the list.
 *
 * The contract has four observable parts, and every one of them lives in the
 * DOM, so no other gate in this repo can see a regression in any of them:
 *
 * 1. The button opens the panel and writes nothing until a chip is added.
 * 2. The header badge reports the catalogue size, and the match count once a
 *    search narrows it — the title is what answers "how many did my query hit".
 * 3. A chip's + adds that one model to the main list, and the chip turns into
 *    the added state (green, "Added", a delete button, no +).
 * 4. The chip's trash removes it again, and the chip goes back to default.
 *
 * Driven against a fake upstream listing three models so the counts stay exact:
 * a panel that imported everything on open would read 3 in /api/models/custom
 * before any chip was clicked, which is the behaviour this replaced.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { parseCustomModelsResponse } from '../src/lib/customModels'
import { startDashboard, type Dashboard } from './harness'

const PORT = 20451
const UPSTREAM_PORT = 20452

const NODE_SUFFIX = 'e2eimportpreview'
const NODE_ID = `openai-compatible-chat-${NODE_SUFFIX}`

/** The fake catalogue: two ids that match "dee", one that does not. */
const DEEPSEEK = 'deepseek-chat'
const DEEPSEEK_REASONER = 'deepseek-reasoner'
const OTHER = 'zzz-other-model'

let app: Dashboard
let page: Page
let stopUpstream: (() => void) | undefined

function modelsBody(): string {
  return JSON.stringify({
    object: 'list',
    data: [{ id: DEEPSEEK }, { id: DEEPSEEK_REASONER }, { id: OTHER }],
  })
}

/** The rows the gateway stored, as "<providerAlias>|<id>|llm" keys. */
async function customModelKeys(): Promise<string[]> {
  const res = await page.request.get(`${app.baseURL}/api/models/custom`)
  return parseCustomModelsResponse(await res.json())
    .filter((m) => typeof m.providerAlias === 'string')
    .map((m) => `${m.providerAlias}|${m.id}|llm`)
}

/** The panel's header badge: the count beside "Preview Fetch Models". */
async function headerCount(): Promise<string> {
  return (await page.locator('[role="dialog"] header span.rounded-full').first().textContent())?.trim() ?? ''
}

/** One chip's row, found by the model id it renders. */
function chip(modelId: string) {
  return page.locator('[role="dialog"] div.font-mono', { hasText: modelId }).first()
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
}, 90_000)

afterAll(async () => {
  await app.stop()
  stopUpstream?.()
})

describe('Import from /models opens a preview panel', () => {
  test('stages the fetch, filters, and syncs each chip with the main list', async () => {
    const node = await page.request.post(`${app.baseURL}/api/provider-nodes`, {
      data: {
        name: 'E2E Import Preview',
        prefix: 'e2epreview',
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
        name: 'e2e-import-preview',
        apiKey: 'e2e-dummy-key',
        authType: 'apiKey',
        isActive: 1,
      },
    })
    expect(conn.ok()).toBe(true)

    await page.goto(`${app.baseURL}/dashboard/providers/${NODE_ID}`)

    // 1. Opening the panel writes nothing: the import is staged, not applied.
    // The button's accessible name carries its Material Symbols ligature
    // ("download Import from /models"), so an exact name match never hits.
    await page.getByRole('button', { name: /Import from \/models/ }).click()
    await page.locator('[role="dialog"]').waitFor({ state: 'visible', timeout: 30_000 })
    // The panel opens before the fetch resolves, so wait for the staged count
    // rather than a duration: it reaching 3 is what says the fetch landed.
    await page.waitForFunction(
      () => document.querySelector('[role="dialog"] header span.rounded-full')?.textContent?.trim() === '3',
      undefined,
      { timeout: 30_000 }
    )
    expect(await headerCount()).toBe('3')
    expect(await customModelKeys()).toEqual([])

    // 2. The search narrows the badge to the match count, client-side.
    await page.locator('[role="dialog"] input[aria-label="Search models by id"]').fill('dee')
    await page.waitForFunction(
      () => document.querySelector('[role="dialog"] header span.rounded-full')?.textContent?.trim() === '2',
      undefined,
      { timeout: 10_000 }
    )
    expect(await headerCount()).toBe('2')

    // 3. The + adds that one model, and the chip flips to the added state.
    await chip(DEEPSEEK).getByRole('button', { name: `Add ${DEEPSEEK} to the list` }).click()
    await page.waitForFunction(
      (id) =>
        [...document.querySelectorAll('[role="dialog"] div.font-mono')].some(
          (el) => el.textContent?.includes(id) && el.textContent?.includes('Added')
        ),
      DEEPSEEK,
      { timeout: 10_000 }
    )
    expect(await customModelKeys()).toEqual([`${NODE_ID}|${DEEPSEEK}|llm`])

    // 4. The trash removes it again and the chip returns to default.
    await chip(DEEPSEEK).getByRole('button', { name: `Remove ${DEEPSEEK} from the list` }).click()
    await page.waitForFunction(
      (id) =>
        [...document.querySelectorAll('[role="dialog"] div.font-mono')].some(
          (el) => el.textContent?.includes(id) && !el.textContent?.includes('Added')
        ),
      DEEPSEEK,
      { timeout: 10_000 }
    )
    expect(await customModelKeys()).toEqual([])
  }, 120_000)

  // Selecting a chip must narrow the bulk actions to it. Before this the
  // selection only tinted the row: "Add all" wrote the whole filter and the
  // button labels never said which set they would act on, so a hand-picked
  // subset was not reachable from the UI at all.
  test('a chip selection narrows the bulk actions to the selected ids', async () => {
    await page.locator('[role="dialog"] input[aria-label="Search models by id"]').fill('')
    await page.waitForFunction(
      () => document.querySelector('[role="dialog"] header span.rounded-full')?.textContent?.trim() === '3',
      undefined,
      { timeout: 10_000 }
    )

    // Nothing selected: the action row acts on the whole filter. The previous
    // test removed nothing, so all three staged ids are addable.
    await expect(page.getByRole('button', { name: /Add all \(3\)/ }).isVisible()).resolves.toBe(true)

    // Select one chip by clicking its name.
    // The chip's select button carries no aria-label; its accessible name is
    // the model id it renders, so select by the pressed-state button inside it.
    await chip(OTHER).locator('button[aria-pressed]').click()
    await expect(page.getByRole('button', { name: /Add selected \(1\)/ }).isVisible()).resolves.toBe(true)
    await expect(page.getByRole('button', { name: /Test selected \(1\)/ }).isVisible()).resolves.toBe(true)

    // Acting on it writes exactly that id, not the filter.
    await page.getByRole('button', { name: /Add selected \(1\)/ }).click()
    await page.waitForFunction(
      (id) =>
        [...document.querySelectorAll('[role="dialog"] div.font-mono')].some(
          (el) => el.textContent?.includes(id) && el.textContent?.includes('Added')
        ),
      OTHER,
      { timeout: 10_000 }
    )
    expect(await customModelKeys()).toEqual([`${NODE_ID}|${OTHER}|llm`])
  }, 120_000)
})
