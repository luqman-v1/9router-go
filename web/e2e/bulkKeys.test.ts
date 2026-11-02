/**
 * Bulk-create API keys.
 *
 * The single-key path is covered by apiKeys.test.ts and the phone layout by
 * mobileApiKeys.test.ts. This file covers the batch: that a malformed line is
 * refused before anything is sent, that a clean batch creates one key per line,
 * and that every secret the batch minted is still on screen afterwards — a
 * secret appears exactly once, so a dialog that closes before the operator has
 * read them loses the credential for good.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Dashboard['page']
// Creating keys through the modal takes longer than bun's 5s default hook
// budget, so the hooks carry the same allowance the other dashboard suites use.
const SLOW = 60_000

beforeAll(async () => {
  app = await startDashboard(20451)
  page = app.page
  await page.getByRole('button', { name: 'Create Key' }).waitFor()
}, SLOW)

afterAll(async () => {
  await app.stop()
})

/**
 * The bulk panel's submit button.
 *
 * Addressed by its exact name rather than a /^Create/ prefix: the empty-state
 * button is "Create Key" and the submit is "Create", so a prefix matches both
 * and strict mode refuses.
 */
function bulkSubmit() {
  return page.getByRole('button', { name: 'Create keys' })
}

/**
 * Opens the create dialog on its Bulk tab.
 *
 * "Create Key" exists twice once the list has rows — the empty state carries one
 * and the search row carries another — so the first is taken. Both open the same
 * dialog; only one is on screen at a time in practice, and `first()` keeps the
 * helper stable across the empty and populated states.
 */
async function openBulkTab() {
  await page.getByRole('button', { name: 'Create Key' }).first().click()
  await page.getByRole('button', { name: 'Bulk' }).click()
  await page.locator('#bulk-keys-input').waitFor()
}

describe('bulk create', () => {
  test('a malformed line is refused before anything is sent', async () => {
    await openBulkTab()
    // The third field is a machineId the operator meant to send. Truncating it
    // would create a key that looks right and authorises the wrong machine.
    await page.locator('#bulk-keys-input').fill('office | m-abc | m-xyz')
    await page.waitForTimeout(300)
    const refused = await page.locator('body').innerText()
    expect(refused).toContain('too many separators')
    // The submit stays disabled: a refused batch must not fire on Enter.
    expect(await bulkSubmit().isDisabled()).toBe(true)
    // Nothing was sent, so closing leaves the database exactly as it was — the
    // next test starts from an empty list rather than a half-built batch.
    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('a clean batch creates one key per line and shows every secret', async () => {
    await openBulkTab()
    await page.locator('#bulk-keys-input').fill('office | m-abc-123\nlaptop | m-def-456\nserver')
    await page.waitForTimeout(300)
    const preview = await page.locator('body').innerText()
    expect(preview).toContain('3 keys will be created')
    expect(await bulkSubmit().isDisabled()).toBe(false)

    await bulkSubmit().click()
    // The result dialog is the only place the secrets appear, so waiting for it
    // is also waiting for the credentials to still be readable.
    await page.getByRole('heading', { name: '3 API Keys Created' }).waitFor({ timeout: 30_000 })

    const body = await page.locator('body').innerText()
    expect(body).toContain('office · m-abc-123')
    expect(body).toContain('laptop · m-def-456')
    expect(body).toContain('server')
    expect(body).toContain('Download .txt')

    // Closing hands the list back, and the three rows are in it. The harness
    // viewport is 1440 wide, so the list renders as table rows rather than the
    // phone's <li> cards — addressed by text, which is true of either layout.
    await page.getByRole('button', { name: 'Done' }).click()
    for (const name of ['office', 'laptop', 'server']) {
      // `attached` rather than the default `visible`: the name sits in a
      // truncating span, so Playwright sees the element but not a visible box,
      // and the default wait never resolves.
      await page.getByText(name, { exact: true }).first().waitFor({ state: 'attached', timeout: 10_000 })
    }
  }, SLOW)

  test('a batch with no name still creates a labelled key', async () => {
    await openBulkTab()
    // A bare machineId: the server accepts an empty name, but a list of unnamed
    // rows is unreadable, so the planner fills "Key 1".
    await page.locator('#bulk-keys-input').fill('| m-only-1')
    await page.waitForTimeout(300)
    const single = await page.locator('body').innerText()
    expect(single).toContain('1 key will be created')
    await bulkSubmit().click()
    await page.getByRole('heading', { name: '1 API Key Created' }).waitFor({ timeout: 30_000 })
    const named = await page.locator('body').innerText()
    expect(named).toContain('Key 1 · m-only-1')
    await page.getByRole('button', { name: 'Done' }).click()
  }, SLOW)
})
