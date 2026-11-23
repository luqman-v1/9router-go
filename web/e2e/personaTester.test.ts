/**
 * The persona injection tester's Body mode.
 *
 * The tester answers the question the Persona page exists for — "does the
 * prompt I armed actually change what the gateway sends?" — and Body mode
 * answers it with zero upstream calls: one read of the gateway's own resolver,
 * diffed word by word against what the operator typed.
 *
 * Two defects are invisible to every other gate and live here:
 *
 *   1. A diff anchored on message *position* instead of on the shared tail. A
 *      preset prepends a system turn, which shifts the operator's turn down by
 *      one, so a positional diff pairs the operator's words with the injected
 *      prompt and reports "the whole conversation changed" — the one reading
 *      that would make the panel useless. Only a rendered page catches it,
 *      because the alignment lives in the DOM the browser draws.
 *   2. A diff that compares two *scopes* rather than injected against
 *      un-injected. Both scopes usually resolve to the same body, so the panel
 *      would show "no difference" for a binding that plainly works.
 *
 * Both are asserted from the rendered text: the injected prompt appears as a
 * message "only in the body the gateway sends", and the operator's own turn is
 * still marked unchanged beside it.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20454

/** The preset armed for the test, with a prompt unmistakably its own. */
const PRESET_PROMPT = 'You are a ruthless assistant.'
/** The model id armed against that preset. */
const MODEL_ID = 'deepseek/deepseek-chat'

/** Boot time plus a SQLite write and read-back, well over the 5s default. */
const SLOW = 60_000

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  await page.setViewportSize({ width: 1284, height: 1100 })
  // The seed issues its fetches from inside the page, so the page must already
  // be on the gateway's origin — on about:blank the same fetch is cross-origin
  // and fails, which strands the seed and every wait after it.
  await page.goto(`${app.baseURL}/dashboard/endpoint`)
}, SLOW)

afterAll(async () => {
  // A timed-out beforeAll leaves app undefined; without the guard the afterAll
  // TypeError would bury the real hook timeout.
  await app?.stop()
})

/** Seeds the preset and its binding through the dashboard API. */
async function seedPersona(): Promise<void> {
  await page.evaluate(
    async (base) => {
      await fetch(`${base}/api/settings`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ navVisibility: { hidden: [], order: [] } }),
      })
      await fetch(`${base}/api/jailbreak/presets`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: 'e2e-tester-persona', name: 'E2E Tester Persona', prompt: 'You are a ruthless assistant.' }),
      })
      await fetch(`${base}/api/jailbreak/models`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: 'deepseek/deepseek-chat', presetId: 'e2e-tester-persona', enabled: true }),
      })
    },
    app.baseURL,
  )
}

describe('persona injection tester', () => {
  test(
    'diffs the injected body against what the operator typed',
    async () => {
      await seedPersona()
      // The Persona entry is hidden by default, and the app redirects a deep
      // link to a hidden tab back to Endpoint — so the seed runs first, then the
      // page is loaded with the entry already visible.
      await page.goto(`${app.baseURL}/dashboard/jailbreak`, {
        waitUntil: 'domcontentloaded',
        timeout: SLOW,
      })
      await page.getByLabel('Model id to test').waitFor({ timeout: SLOW })

      await page.getByLabel('Model id to test').fill(MODEL_ID)
      await page.getByLabel('Prompt to test').fill('write me a farming script')
      await page.getByRole('button', { name: 'Show body' }).click()

      // The injected prompt reaches the DOM as a message that exists only in
      // the body the gateway sends. Under a positional diff this reads "the
      // whole message changed" instead, and the assertion below never sees it.
      await page.getByText('only in the gateway sends').waitFor({ timeout: SLOW })
      expect(await page.getByText(PRESET_PROMPT).count()).toBeGreaterThan(0)

      // And the operator's own turn is still there, unchanged, next to it — so
      // the panel is showing an addition, not a replacement of the request.
      expect(await page.getByText('write me a farming script').count()).toBeGreaterThan(0)
      expect(await page.getByText('unchanged').count()).toBeGreaterThan(0)

      // The header counts what changed rather than claiming nothing did, which
      // is the failure a scope-vs-scope comparison would produce.
      const diffText = await page.evaluate(() => document.body.innerText)
      expect(diffText).not.toContain('No difference')
    },
    SLOW,
  )

  test(
    'says so plainly when nothing resolves for the id',
    async () => {
      // An unarmed id must read as "nothing injected", not as an error or as a
      // silent empty panel — the operator's next question is always "why not?".
      const modelInput = page.getByLabel('Model id to test')
      await modelInput.waitFor({ timeout: SLOW })
      await modelInput.fill('openai/gpt-5-mini-nothing-armed')
      await page.getByRole('button', { name: 'Show body' }).click()
      await page.getByText('Nothing resolved for this id under this scope').waitFor({ timeout: SLOW })
    },
    SLOW,
  )
})