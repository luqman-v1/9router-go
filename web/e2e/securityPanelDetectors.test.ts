/**
 * The Security panel's detector chips (#1540).
 *
 * "Secrets" is a selectable detector like pii and injection. A screenshot shows
 * the chip rendered; this drives the live UI contract instead: the chip is
 * offered, it toggles, and a policy saved with it carries the detector into
 * the stored config — which is what makes the inbound scanner mask anything.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'

import { startDashboard, type Dashboard } from './harness'

const SLOW = 60_000

let app: Dashboard
let page: Dashboard['page']

/** Opens a fresh editor, closing whichever one the previous test left open. */
async function openEditor() {
  const cancel = page.getByRole('button', { name: 'Cancel' })
  if (await cancel.isVisible()) await cancel.click()
  await page.getByRole('button', { name: 'New policy' }).click()
  await page.getByText('New guardrail policy').waitFor()
}

beforeAll(async () => {
  app = await startDashboard(20497)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app?.stop()
})

describe('security panel detectors', () => {
  beforeAll(async () => {
    // The login lands on the overview route; the security view is a separate
    // SPA route. On a cold throwaway instance the boot gate outlasts bun's
    // 5s default, hence the explicit waits.
    await page.goto(`${app.baseURL}/dashboard/security`)
    await page.getByRole('button', { name: 'New policy' }).waitFor({ timeout: 30_000 })
  }, SLOW)

  test('the Secrets detector is offered next to pii and injection', async () => {
    await openEditor()
    const secrets = page.getByRole('button', { name: 'Secrets', exact: true })
    await secrets.waitFor()
    // A new policy defaults to pii only, so Secrets starts unpressed.
    expect(await secrets.getAttribute('aria-pressed')).toBe('false')
    for (const label of ['PII', 'Prompt injection', 'Custom pattern']) {
      await page.getByRole('button', { name: label, exact: true }).waitFor()
    }
  })

  test('the Secrets detector toggles', async () => {
    await openEditor()
    const secrets = page.getByRole('button', { name: 'Secrets', exact: true })
    await secrets.click()
    expect(await secrets.getAttribute('aria-pressed')).toBe('true')
    await secrets.click()
    expect(await secrets.getAttribute('aria-pressed')).toBe('false')
  })

  test('a policy saved with Secrets keeps the detector', async () => {
    await openEditor()
    await page.locator('#policy-name').fill('E2E Secrets Policy')
    await page.getByRole('button', { name: 'Secrets', exact: true }).click()
    await page.getByRole('button', { name: 'Create policy' }).click()

    const row = page.getByRole('row').filter({ hasText: 'E2E Secrets Policy' })
    await row.waitFor()
    // The list renders the detector label from the stored config, so a row
    // carrying "Secrets" proves the selection reached the API and came back.
    expect(await row.textContent()).toContain('Secrets')
  })
})