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
    const reasonerOption = picker.locator('option').filter({ hasText: 'deepseek-reasoner' })
    await reasonerOption.waitFor({ state: 'attached', timeout: 10_000 })
    const reasonerId = (await reasonerOption.getAttribute('value'))!
    expect(reasonerId).toContain('deepseek-reasoner')

    await picker.selectOption(reasonerId)
    // The Add button is the picker's sibling inside the Pi Models section.
    await picker.locator('xpath=following-sibling::button').click()

    // The added row is the one carrying this model id; every cap assertion
    // below is scoped to it so a second row cannot satisfy them.
    const row = dialog
      .locator('div.rounded-xl')
      .filter({ has: page.locator('span', { hasText: reasonerId }) })
      .filter({ hasNot: page.locator('select[data-testid="pi-model-select"]') })
    await row.waitFor({ timeout: 5_000 })

    // Seeded from the catalog, not from a click: `*reasoner*` carries
    // Reasoning + ThinkingFormat in internal/providers/capabilities.go, so the
    // row must arrive with `reasoning` already checked.
    const reasoningBox = row.locator('label').filter({ hasText: 'reasoning' }).locator('input[type="checkbox"]')
    expect(await reasoningBox.isChecked()).toBe(true)

    // `image input` only exists once a model row is on screen.
    await row.getByText(/image input/i).waitFor({ timeout: 5_000 })

    // Thinking levels render per row, from the model's thinking_levels.
    await row.getByText('Thinking levels', { exact: true }).waitFor({ timeout: 5_000 })
    expect(await row.locator('input[type="checkbox"]').count()).toBeGreaterThan(2)

    // Save
    await dialog.getByRole('button', { name: /Save & Apply/i }).click()

    // Verify success message
    await dialog.getByText(/successfully applied/i).waitFor({ timeout: 10_000 })
  }, 20_000)

  // Review blocker on PR #1578: `handleApply` built its payload as
  // `{ baseUrl }` (+ `models` for pi), so every non-Claude tool re-applied
  // with no `model` and claude.go's else-branch deleted the stored `model`
  // key. The observable is the request body, so it is asserted here.
  test('apply sends the model for a non-Claude tool', async () => {
    const page = app.page
    const seededKey = await page.evaluate(async () => {
      const res = await fetch('/api/keys', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'E2E Model Payload' }),
      })
      return (await res.json()).key as string
    })
    await page.evaluate((k) => localStorage.setItem('9router_key', k), seededKey)

    await page.goto(`${app.baseURL}/dashboard/cli-tools`)

    const codexCard = card('OpenAI Codex CLI')
    await codexCard.waitFor()
    await codexCard.getByRole('button', { name: 'Configure' }).click()

    const dialog = page.locator('.fixed.inset-0')
    await dialog.waitFor({ timeout: 10_000 })

    const modelInput = dialog.locator('input[list="cli-tool-models-datalist"]').first()
    await modelInput.fill('oc/big-pickle')

    const sent = page.waitForRequest(
      (r) => r.url().endsWith('/api/cli-tools/codex-settings') && r.method() === 'POST',
      10_000
    )
    await dialog.getByRole('button', { name: /Save & Apply/i }).click()
    const body = (await sent).postDataJSON() as Record<string, unknown>

    expect(body.model).toBe('oc/big-pickle')
    expect(body.baseUrl).toBeTruthy()
  }, 20_000)
})
