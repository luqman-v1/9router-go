import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { startDashboard, type Dashboard } from './harness'

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20423)
}, 20_000)

afterAll(async () => {
  await app.stop()
})

describe('Pi models editor', () => {
  test('can add a model, toggle caps, and save', async () => {
    const page = app.page
    await page.goto(`${app.baseURL}/dashboard/cli-tools`)

    const piCard = page.locator('[role="button"]').filter({ has: page.getByRole('heading', { name: 'Pi', exact: true }) })
    await piCard.waitFor()
    await piCard.click()

    const dialog = page.locator('.fixed.inset-0')
    await dialog.waitFor({ timeout: 10_000 })

    // Wait for models to load
    const select = dialog.locator('select').first()
    await select.waitFor({ timeout: 10_000 })

    // Add first available model
    await select.selectOption({ index: 1 })
    await dialog.getByRole('button', { name: /Add/i }).click()

    // Wait for the model entry to render
    await dialog.getByText(/image input/i).waitFor({ timeout: 5_000 })

    // Toggle reasoning checkbox
    const reasoningCheckbox = dialog.locator('input[type="checkbox"]').nth(1)
    await reasoningCheckbox.check()

    // Save
    await dialog.getByRole('button', { name: /Save & Apply/i }).click()

    // Verify success message
    await dialog.getByText(/successfully applied/i).waitFor({ timeout: 10_000 })
  })
})
