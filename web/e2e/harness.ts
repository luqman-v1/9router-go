/**
 * Harness for the dashboard E2E suite.
 *
 * The suite drives a real `9router-go` binary against a real SQLite file and
 * a real Chromium, because the regressions it exists to catch live in the DOM.
 *
 * Every other gate in this repo is structurally blind to them: `tsc` reads
 * types, `vite build` bundles, the svelte-check ratchet counts diagnostics, and
 * `go test ./...` exercises the HTTP API without ever rendering a component. A
 * destructive action losing its confirmation dialog passes all four. That is not
 * hypothetical — collapsing the API-key row controls into a menu (#224) dropped
 * the `confirm()` on delete and regenerate, and every one of those gates stayed
 * green. The `confirm` assertions below exist so that cannot happen twice.
 */

import { spawn, type ChildProcess } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { chromium, type Browser, type Page } from '@playwright/test'

/** Dashboard password for the throwaway instance. */
export const PASSWORD = 'e2e-password'

const REPO_ROOT = resolve(import.meta.dirname, '..', '..')
// The release build appends .exe on Windows and nothing elsewhere; CI runs
// this suite on ubuntu, so the name has to follow the platform or the suite
// only ever works on the machine that wrote it.
// Checked before the spawn, in startDashboard: bun reports a rejected beforeAll
// as a 5s hook timeout followed by an afterAll crash on the undefined handle,
// so one missing file used to read as six failures.
const BINARY = join(REPO_ROOT, process.platform === 'win32' ? '9router-go.exe' : '9router-go')

export interface Dashboard {
  page: Page
  baseURL: string
  /** Closes the browser and deletes the throwaway SQLite database. */
  stop: () => Promise<void>
}

/**
 * Boots the gateway on an isolated data dir and logs a Chromium session into
 * the dashboard.
 *
 * `DATA_DIR` rather than `HOME_DIR`: the binary resolves its home directory in
 * `config.ResolveDataDir` and ignores `HOME_DIR`, so a test that set it would
 * boot against the developer's real database.
 *
 * `port` is a parameter rather than a shared constant because `bun test` runs
 * each file in its own process: two suites booting on one port would race, and
 * the loser's `afterAll` would close a browser the winner was still driving.
 */
export async function startDashboard(port: number): Promise<Dashboard> {
  if (!existsSync(BINARY)) {
    throw new Error(`gateway binary not found at ${BINARY}; build it before running the e2e suite`)
  }

  const dataDir = mkdtempSync(join(tmpdir(), '9router-e2e-'))
  const server = spawn(BINARY, [], {
    cwd: REPO_ROOT,
    env: {
      ...process.env,
      DATA_DIR: dataDir,
      PORT: String(port),
      INITIAL_PASSWORD: PASSWORD,
      // Token savers rewrite the prompt upstream, which would make byte-level
      // assertions on proxied bodies fail for reasons unrelated to this suite.
      RTK_ENABLED: 'false',
      CAVEMAN_ENABLED: 'false',
      PONYTAIL_ENABLED: 'false',
    },
    stdio: 'ignore',
  })

  const baseURL = `http://127.0.0.1:${port}`
  await waitForServer(baseURL, server)

  const browser: Browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })

  await page.goto(`${baseURL}/login`)
  // getByLabel('Password') also matches the "Show password" toggle button, so
  // the field is addressed by its id instead.
  await page.locator('#login-password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Login', exact: true }).click()
  await page.waitForURL('**/dashboard/**')

  return {
    page,
    baseURL,
    stop: async () => {
      await browser.close()
      server.kill()
      // Windows keeps the SQLite file locked briefly after the process exits,
      // so the delete is best-effort: a leftover temp dir is not a test failure.
      try {
        rmSync(dataDir, { recursive: true, force: true })
      } catch {}
    },
  }
}

/** Resolves once the gateway answers, and fails fast if the process died. */
async function waitForServer(baseURL: string, server: ChildProcess): Promise<void> {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    if (server.exitCode !== null) {
      throw new Error(`gateway exited during boot with code ${server.exitCode}`)
    }
    try {
      const res = await fetch(`${baseURL}/api/version`)
      if (res.ok) return
    } catch {
      // Not listening yet.
    }
    await delay(200)
  }
  throw new Error('gateway did not start listening within 30s')
}

/** Sleeps between gateway boot polls. */
function delay(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, ms)
  return promise
}

/**
 * The table row for one key.
 *
 * Scoped with `filter({ hasText })` rather than `getByRole('cell', { name })`:
 * a key's name appears in four cells on the row — the selection checkbox, the
 * name cell, the token cell and the actions cell — so a cell lookup by name
 * matches all four and trips Playwright's strict mode.
 */
export function keyRow(page: Page, keyName: string) {
  return page.getByRole('row').filter({ hasText: keyName })
}

/**
 * Creates a key through the UI and waits for it to appear in whichever list
 * the current viewport renders.
 *
 * Below `md` that list is cards rather than table rows (issue #224), so waiting
 * on `keyRow` alone timed out on every phone-width test.
 */
export async function createKey(page: Page, name: string): Promise<string> {
  await page.getByRole('button', { name: 'Create Key' }).click()
  await page.getByLabel('Key Name').fill(name)
  await page.getByLabel('Key Name').press('Enter')
  await page.getByRole('button', { name: 'Done' }).click()
  await keyCard(page, name)
    .or(keyRow(page, name))
    .first()
    .waitFor()
  return name
}

/**
 * The card for one key, in the list rendered below `md` (issue #224).
 *
 * The counterpart of `keyRow` for the phone-width layout: the list swaps the
 * table for `<li>` cards there, so a test that only knew `keyRow` found
 * nothing at 390px and passed vacuously.
 */
export function keyCard(page: Page, keyName: string) {
  return page.getByRole('listitem').filter({ hasText: keyName })
}

/** Opens the overflow menu on one key row. */
export async function openRowMenu(page: Page, keyName: string): Promise<void> {
  await keyRow(page, keyName)
    .getByRole('button', { name: `Actions for ${keyName}` })
    .click()
}