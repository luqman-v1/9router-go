/**
 * OpenRouter custom models carry an operator request body (extraBody).
 *
 * Driven through the Add Custom Model dialog on the real provider page: a body
 * inside the allowlist is stored and merged into the outbound OpenRouter
 * request (operator wins over the client), while a body naming a routing key
 * (`provider`) is refused on save with the offending key shown in the dialog.
 * Unit tests cover the validator; only a rendered dialog proves the field is
 * reachable, submitted, and its refusal visible.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'
import { parseCustomModelsResponse } from '../src/lib/customModels'
import { startDashboard, type Dashboard } from './harness'
const PORT = 20461
const UPSTREAM_PORT = 20462
const OK_ID = 'vendor/e2e-extra-ok:free'
const BAD_ID = 'vendor/e2e-extra-bad:free'
let app: Dashboard
let page: Page
let stopUpstream: (() => void) | undefined
/** Bodies the fake OpenRouter received on /chat/completions. */
const upstreamBodies: Record<string, unknown>[] = []
const dialogs: string[] = []
async function storedRow(id: string): Promise<Record<string, unknown> | undefined> {
  const res = await page.request.get(`${app.baseURL}/api/models/custom`)
  const rows = parseCustomModelsResponse(await res.json()) as Record<string, unknown>[]
  return rows.find((m) => m.id === id && m.providerAlias === 'openrouter')
}
async function openAddDialog(id: string, body: string): Promise<void> {
  await page.getByRole('button', { name: /Add Model/ }).first().click()
  await page.getByPlaceholder(/paste a list to add several/).fill(id)
  await page.getByLabel('Custom request body (JSON, optional)').fill(body)
}
/** Optional element-scoped evidence: set E2E_SHOTS to a directory to keep the dialog image. */
async function shot(name: string): Promise<void> {
  const dir = process.env.E2E_SHOTS
  if (dir) await page.locator('#add-custom-model-extra-body').locator('xpath=..').screenshot({ path: `${dir}/${name}.png` })
}
beforeAll(async () => {
  const server = Bun.serve({
    port: UPSTREAM_PORT,
    async fetch(req) {
      if (new URL(req.url).pathname !== '/chat/completions') return new Response('nf', { status: 404 })
      upstreamBodies.push((await req.json()) as Record<string, unknown>)
      return Response.json({
        id: 'x',
        object: 'chat.completion',
        created: 1,
        model: 'm',
        choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      })
    },
  })
  stopUpstream = () => void server.stop(true)
  app = await startDashboard(PORT)
  page = app.page
  page.on('dialog', (d) => {
    dialogs.push(d.message())
    void d.accept()
  })
  const conn = await page.request.post(`${app.baseURL}/api/connections`, {
    data: {
      provider: 'openrouter',
      name: 'e2e-openrouter',
      authType: 'apikey',
      isActive: 1,
      data: { apiKey: 'sk-e2e', baseUrl: `http://127.0.0.1:${UPSTREAM_PORT}/chat/completions` },
    },
  })
  expect(conn.ok()).toBe(true)
  await page.goto(`${app.baseURL}/dashboard/providers/openrouter`)
}, 90_000)
afterAll(async () => {
  await app.stop()
  stopUpstream?.()
})
describe('OpenRouter custom model request body', () => {
  test('a valid body is saved and reaches the upstream request', async () => {
    await openAddDialog(OK_ID, '{"temperature":0.7,"reasoning":{"effort":"high"}}')
    await page.getByRole('button', { name: /^Add Model$/ }).last().click()
    await page.getByText('Add Custom Model').waitFor({ state: 'hidden' })
    expect(await storedRow(OK_ID)).toBeTruthy()
    expect((await storedRow(OK_ID))?.extraBody).toEqual({ temperature: 0.7, reasoning: { effort: 'high' } })
    const minted = await page.request.post(`${app.baseURL}/api/keys`, { data: { name: 'e2e-extra-body' } })
    const { key } = (await minted.json()) as { key: string }
    const res = await page.request.post(`${app.baseURL}/v1/chat/completions`, {
      headers: { Authorization: `Bearer ${key}` },
      data: { model: `openrouter/${OK_ID}`, messages: [{ role: 'user', content: 'hi' }], temperature: 0.1 },
    })
    expect(res.status()).toBe(200)
    expect(upstreamBodies.at(-1)).toMatchObject({ model: OK_ID, temperature: 0.7, reasoning: { effort: 'high' } })
  }, 60_000)
  test('a routing key is refused on save and named in the dialog', async () => {
    await page.reload()
    await openAddDialog(BAD_ID, '{"provider":{"sort":"price"}}')
    await page.getByRole('alert').filter({ hasText: 'provider' }).waitFor({ timeout: 5_000 })
    expect(await page.getByRole('button', { name: /^Add Model$/ }).last().isDisabled()).toBe(true)
    await shot('pr1-extrabody-refused')
    expect(await storedRow(BAD_ID)).toBeUndefined()
    expect(dialogs).toEqual([])
  }, 60_000)
})
