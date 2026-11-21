/**
 * Custom model rows can be edited, and deleting one leaves no trace.
 *
 * Edit reopens the Add Custom Model dialog prefilled for a stored OpenRouter
 * row and re-saves it under the same key. Delete used to hit an endpoint that
 * parks the id in `disabledModels`, so a deleted custom model reappeared under
 * "Disabled models"; the page now prunes it. Both are read back through the
 * served API and the rendered page, not the component state.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'
import { parseCustomModelsResponse } from '../src/lib/customModels'
import { startDashboard, type Dashboard } from './harness'
const PORT = 20471
const MODEL_ID = 'vendor/e2e-edit-delete:free'
let app: Dashboard
let page: Page
async function storedRow(): Promise<Record<string, unknown> | undefined> {
  const res = await page.request.get(`${app.baseURL}/api/models/custom`)
  const rows = parseCustomModelsResponse(await res.json()) as Record<string, unknown>[]
  return rows.find((m) => m.id === MODEL_ID && m.providerAlias === 'openrouter')
}
async function disabledIds(): Promise<string[]> {
  const res = await page.request.get(`${app.baseURL}/api/models/disabled`)
  const all = (await res.json()) as Record<string, string[]>
  return all.openrouter ?? []
}
/** Optional element-scoped evidence: set E2E_SHOTS to a directory to keep the page section. */
async function shot(name: string): Promise<void> {
  const dir = process.env.E2E_SHOTS
  if (dir) await page.getByText(MODEL_ID).first().locator('xpath=../..').screenshot({ path: `${dir}/${name}.png` })
}
beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  page.on('dialog', (d) => void d.accept())
  const seeded = await page.request.post(`${app.baseURL}/api/models/custom`, {
    data: {
      key: `openrouter|${MODEL_ID}|llm`,
      value: { providerAlias: 'openrouter', id: MODEL_ID, type: 'llm', name: 'Keep Me', extraBody: { temperature: 0.7 } },
    },
  })
  expect(seeded.ok()).toBe(true)
  await page.goto(`${app.baseURL}/dashboard/providers/openrouter`)
}, 90_000)
afterAll(async () => {
  await app.stop()
})
describe('custom model edit and delete', () => {
  test('editing the request body persists it and keeps other fields', async () => {
    await page.getByTitle('Edit model').first().click()
    const field = page.getByLabel('Custom request body (JSON, optional)')
    expect(await field.inputValue()).toContain('"temperature": 0.7')
    await field.fill('{"top_p":0.5}')
    await page.getByRole('button', { name: /^Save$/ }).click()
    await page.getByText('Edit Model').waitFor({ state: 'hidden' })
    const row = await storedRow()
    expect(row?.extraBody).toEqual({ top_p: 0.5 })
    expect(row?.name).toBe('Keep Me')
  }, 60_000)
  test('deleting a custom model does not park it under Disabled models', async () => {
    await shot('pr2-before-delete')
    // The row's X: titled "Disable this model" before the fix, "Delete this custom model" after.
    await page
      .getByText(MODEL_ID)
      .first()
      .locator("xpath=ancestor::div[.//button[@title='Disable this model' or @title='Delete this custom model']][1]")
      .locator("button[title='Disable this model'], button[title='Delete this custom model']")
      .click()
    await modelRowGone()
    expect(await storedRow()).toBeUndefined()
    expect(await disabledIds()).not.toContain(MODEL_ID)
    expect(await page.getByText(/Disabled models \(/).count()).toBe(0)
  }, 60_000)
})
async function modelRowGone(): Promise<void> {
  await page.getByText(MODEL_ID).first().waitFor({ state: 'detached', timeout: 15_000 })
}
