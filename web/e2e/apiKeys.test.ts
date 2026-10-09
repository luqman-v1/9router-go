/**
 * The API-key row menu (#224).
 *
 * The destructive-verb guards are the point of this file. Collapsing seven
 * per-row controls into one menu dropped the `confirm()` on delete and
 * regenerate, and no gate in the repo noticed: `tsc` reads types, `vite build`
 * bundles, the svelte-check ratchet counts diagnostics, and `go test` exercises
 * the HTTP API without rendering a component. Each assertion below therefore
 * checks an *observable* consequence — the row survives a dismissal, and the
 * dialog is the one an operator expects — rather than that some function calls
 * `confirm`.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'

import { createKey, keyRow, openRowMenu, startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Dashboard['page']

beforeAll(async () => {
  app = await startDashboard(20411)
  page = app.page
})
afterAll(async () => {
  await app.stop()
})

describe('api key row menu', () => {
  beforeAll(async () => {
    await page.getByRole('button', { name: 'Create Key' }).waitFor()
  })

  test('dismissing the delete dialog leaves the key intact', async () => {
    const key = await createKey(page, 'E2E Delete Guard')

    page.once('dialog', (d) => d.dismiss())
    await openRowMenu(page, key)
    await page.getByRole('menuitem', { name: 'Delete key' }).click()
    await page.waitForTimeout(500)

    // The row must still be there: a dismissed confirmation is a no-op.
    await keyRow(page, key).waitFor()
  })

  test('accepting the delete dialog removes the key', async () => {
    const key = await createKey(page, 'E2E Delete Confirm')

    page.once('dialog', (d) => d.accept())
    await openRowMenu(page, key)
    await page.getByRole('menuitem', { name: 'Delete key' }).click()

    await keyRow(page, key).waitFor({ state: 'detached' })
  })

  test('dismissing the regenerate dialog keeps the current secret', async () => {
    const key = await createKey(page, 'E2E Rotate Guard')

    const secretBefore = await keyRow(page, key).locator('code').innerText()

    page.once('dialog', (d) => d.dismiss())
    await openRowMenu(page, key)
    await page.getByRole('menuitem', { name: 'Regenerate secret' }).click()
    await page.waitForTimeout(500)

    const secretAfter = await keyRow(page, key).locator('code').innerText()

    expect(secretAfter).toBe(secretBefore)
  })

  test('the delete confirmation names the key it is about to destroy', async () => {
    const key = await createKey(page, 'E2E Named Prompt')

    const message = Promise.withResolvers<string>()
    page.once('dialog', (d) => {
      void message.resolve(d.message())
      void d.dismiss()
    })

    await openRowMenu(page, key)
    await page.getByRole('menuitem', { name: 'Delete key' }).click()

    // A prompt reading "this key" would be true of every key and useless.
    expect(await message.promise).toContain(key)
  })
})

describe('api key batch bar', () => {
  test('pauses every selected key at once', async () => {
    const a = await createKey(page, 'E2E Batch A')
    const b = await createKey(page, 'E2E Batch B')

    await page.getByRole('checkbox', { name: 'Select all keys' }).check()
    await page.getByRole('button', { name: 'Pause' }).click()

    for (const key of [a, b]) {
      await keyRow(page, key).getByText('Paused').waitFor()
    }
  })
})