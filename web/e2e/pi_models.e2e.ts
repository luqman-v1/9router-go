import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { startDashboard, type Dashboard } from './harness'

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20539)
}, 20_000)

afterAll(async () => {
  await app.stop()
})

/** The CLI-tools card whose heading is exactly `name`. */
function card(name: string) {
  return app.page.getByRole('heading', { name, exact: true }).locator('xpath=ancestor::div[.//button[normalize-space(.)="Configure"]][1]')
}

describe('Pi models editor', () => {
  // 20s, not bun's 5s default: the /api/models fetch that fills the picker and
  // the config write both happen inside this one test.
  test('can add a model, toggle caps, and save', async () => {
    const page = app.page
    // The picker is filled from /api/models, which needs both a credential and
    // at least one provider connection — a fresh instance answers 401 and an
    // empty list, leaving only the "Add model…" placeholder.
    const seededKey = await page.evaluate(async () => {
      const res = await fetch('/api/keys', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'E2E Pi Models' }),
      })
      return (await res.json()).key as string
    })
    expect(seededKey).toBeTruthy()

    const conn = await page.request.post(`${app.baseURL}/api/connections`, {
      data: { provider: 'deepseek', name: 'e2e-pi-models', apiKey: 'sk-e2e' },
    })
    expect(conn.ok()).toBe(true)

    // The dashboard sends the stored key as a Bearer header; without it the
    // browser's /api/models fetch is 401 and the picker stays empty.
    await page.evaluate((k) => localStorage.setItem('9router_key', k), seededKey)

    await page.goto(`${app.baseURL}/dashboard/cli-tools`)

    const piCard = card('Pi')
    await piCard.waitFor()
    await piCard.getByRole('button', { name: 'Configure' }).click()

    const dialog = page.locator('.fixed.inset-0')
    await dialog.waitFor({ timeout: 10_000 })
    await dialog.getByText('Pi Models', { exact: true }).waitFor({ timeout: 10_000 })

    // The picker is the Pi Models section's own select, not the dialog's first
    // `select` (the base-URL/model inputs share the dialog).
    const picker = dialog.getByTestId('pi-model-select')
    // `option` elements are hidden by the browser; wait for attachment, not
    // visibility.
    await picker.locator('option').nth(1).waitFor({ state: 'attached', timeout: 10_000 })

    await picker.selectOption({ index: 1 })
    // The Add button is the picker's sibling inside the Pi Models section.
    await picker.locator('xpath=following-sibling::button').click()

    // The entry renders its own cap controls; "image input" only exists once a
    // model row is on screen.
    await dialog.getByText(/image input/i).waitFor({ timeout: 5_000 })

    // Toggle reasoning on the added row.
    await dialog.locator('label').filter({ hasText: 'reasoning' }).locator('input[type="checkbox"]').check()

    // Save
    await dialog.getByRole('button', { name: /Save & Apply/i }).click()

    // Verify success message
    await dialog.getByText(/successfully applied/i).waitFor({ timeout: 10_000 })
  }, 20_000)
})
