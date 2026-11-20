/**
 * The provider page's Copy button handed out an id the gateway does not serve.
 *
 * PR #1259 (commit `929acc66`) swapped the copy target from `storageAlias` to
 * `displayAlias()`, and `displayAlias()` was `selectedNode?.prefix || providerId`
 * — a shape that only covers a compatible node. Every registered provider fell
 * through to its internal id, so the button produced
 * "opencode/nemotron-3-ultra-free" where `/v1/models` publishes
 * "oc/nemotron-3-ultra-free". Pasting that id back into a client did not
 * resolve at the router.
 *
 * Every gate in this repo was blind to it: `tsc` reads types, `vite build`
 * bundles, the svelte-check ratchet counts diagnostics, `go test ./...` calls
 * the HTTP API without rendering a component, and the unit test the PR shipped
 * covered the node branch only. This suite renders the page in Chromium and
 * reads the clipboard, so the contract is checked where it actually lives.
 *
 * The contract pinned here: what Copy puts on the clipboard is the same id the
 * gateway serves for that model. The published prefix is the registry alias
 * (`oc` for opencode), not the provider's internal id.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20451

/** A provider whose published prefix differs from its internal id. */
const PROVIDER_ID = 'opencode'
const PROVIDER_PATH = `/dashboard/providers/${PROVIDER_ID}`
/** The prefix `/v1/models` publishes opencode model ids under. */
const PUBLISHED_PREFIX = 'oc'

let app: Dashboard
let page: Page

/** Reads the clipboard the page writes to. */
async function clipboardText(): Promise<string> {
  return page.evaluate(() => navigator.clipboard.readText())
}

/** Polls the clipboard until it holds the expected text or times out. */
async function waitForClipboard(expected: string, timeoutMs = 5_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if ((await clipboardText()) === expected) return
    await page.waitForTimeout(100)
  }
  expect(await clipboardText()).toBe(expected)
}

/**
 * The model ids the provider page rendered, as the "<prefix>/<model>" strings
 * the grid shows.
 */
async function renderedModelIds(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll('code'))
      .map((el) => (el.textContent ?? '').trim())
      .filter((t) => t.includes('/')),
  )
}

/**
 * Grants the clipboard permissions Chromium needs for readText, and focuses
 * the document so a copy triggered by a button press is allowed.
 */
async function allowClipboard(): Promise<void> {
  const context = page.context()
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await allowClipboard()
}, 90_000)

afterAll(async () => {
  await app.stop()
})

describe('provider page copy button', () => {
  test('copies the published prefix, not the provider id', async () => {
    await page.goto(`${app.baseURL}${PROVIDER_PATH}`)
    await page.waitForLoadState('networkidle')

    const rendered = await renderedModelIds()
    expect(rendered.length).toBeGreaterThan(0)

    // Every id on the page is published under the registry alias.
    for (const id of rendered) {
      expect(id.startsWith(`${PUBLISHED_PREFIX}/`)).toBe(true)
      expect(id.startsWith(`${PROVIDER_ID}/`)).toBe(false)
    }

    // Copy the first model and read what actually landed on the clipboard.
    const target = rendered[0]
    const modelId = target.slice(target.indexOf('/') + 1)

    const copyButton = page
      .locator('button')
      .filter({ has: page.locator('span.material-symbols-outlined', { hasText: 'content_copy' }) })
      .first()
    await copyButton.click()
    await waitForClipboard(target)

    // The copied id has to be one the gateway can resolve: same prefix the
    // page showed, and the model id without the prefix.
    const copied = await clipboardText()
    expect(copied.startsWith(`${PUBLISHED_PREFIX}/`)).toBe(true)
    expect(copied).toBe(`${PUBLISHED_PREFIX}/${modelId}`)
  })
})
