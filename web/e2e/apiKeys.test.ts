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

describe('copying a key', () => {
  // Issue #310. The copy button used to copy whatever the cell displayed, and a
  // hidden cell displays the mask — so copying straight off a loaded dashboard
  // put a masked value on the clipboard and the pasted credential 401'd.
  //
  // The assertion is the clipboard text itself rather than a flag or a call:
  // the user's whole complaint is the bytes that land on the clipboard, and the
  // second check turns those bytes into the thing the user actually does with
  // them — authenticate.
  //
  // The mask is matched by its bullets rather than by a specific character. It
  // used to collapse the middle to a single ellipsis, which told the reader the
  // key's length for free; it now keeps the exact length with bullets, so a test
  // pinned to "…" would fail on the fix rather than on a regression.
  test('copies the usable secret while the row is still hidden', async () => {
    const key = await createKey(page, 'E2E Copy Hidden')

    // The key must not be revealed: revealing first is the workaround the
    // reporter found, and it would hide the bug from this test.
    const displayed = await keyRow(page, key).locator('code').innerText()
    expect(displayed).toContain('•')
    // The mask is the same length as the secret, so the only way to tell them
    // apart is that one carries bullets and the other does not.
    expect(displayed).not.toBe(key)

    await app.page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    await keyRow(page, key).getByRole('button', { name: `Copy key ${key}` }).click()
    await app.page.waitForTimeout(300)

    const copied = await app.page.evaluate(() => navigator.clipboard.readText())

    // A mask is unusable as a credential; the secret starts with `sk-`.
    expect(copied).not.toContain('•')
    expect(copied).not.toBe(displayed)
    expect(copied.startsWith('sk-')).toBe(true)

    // The end of the user story: the pasted value has to work.
    const res = await fetch(`${app.baseURL}/v1/models`, {
      headers: { Authorization: `Bearer ${copied}` },
    })
    expect(res.status).toBe(200)
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