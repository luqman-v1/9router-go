/**
 * Persona (jailbreak) prompt injection, per client API key.
 *
 * PR #1301 taught the resolver to prefer a key-scoped binding over the same
 * model's global one and shipped the per-key editor that writes it. Every other
 * gate is structurally blind to the two defects this file exists for:
 *
 *   1. `JailbreakKeyScope.svelte` shipped imported by nobody. The backend, the
 *      routes and the integration tests were all correct, so every gate stayed
 *      green while the editor was dead code and the two routes it drives had no
 *      caller in the dashboard. Found by a grep, not by a test — because no test
 *      rendered the page.
 *   2. The dashboard client sent `POST /api/jailbreak/keys/model` while the
 *      gateway registers `PUT`, so every key-scoped arm, toggle and disable
 *      answered `405 Method Not Allowed`. The route-verb case in Go exercises
 *      `http.MethodPut`, which is why that suite stayed green with the client
 *      broken.
 *
 * Both live in the DOM and in the browser's request log, so this suite drives a
 * real gateway and a real Chromium: it arms a key-scoped binding through the UI,
 * watches the request the browser actually issued, and then resolves the same id
 * under both scopes to see the key's binding outrank the global one.
 *
* The contracts pinned here:
 *   - the per-key editor is mounted and visible on the Persona page;
 *   - arming a key binding issues `PUT /api/jailbreak/keys/model` and the row
 *     appears in the key's own binding table;
 *   - after arming, the same model id resolves as "injected" under the key and
 *     "not injected" globally, with the preset the operator chose.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { createKey, startDashboard, type Dashboard } from './harness'

const PORT = 20453

/**
 * The page boots behind an auth gate and several boot fetches before any view
 * mounts, and arming a binding writes to SQLite and re-reads it. Bun's 5s
 * default per test cuts through that for reasons unrelated to the feature, so
 * each test carries its own budget — the same shape e2e/usageDetailsPanels.test.ts
 * uses.
 */
const SLOW = 60_000

/** The model id armed per key. Unbound globally, so only the key sees it. */
const MODEL_ID = 'e2e/scoped-persona-model'
/** The preset the operator picks for the key. Created through the UI. */
const PRESET_NAME = 'E2E Scoped Persona'

let app: Dashboard
let page: Page

/**
 * Requests the dashboard issued to the jailbreak API, so a wrong verb is caught
 * from what the browser actually sent rather than from what the client code
 * looks like.
 */
interface SentRequest {
  method: string
  url: string
  postData: string | null
}

let sent: SentRequest[] = []

/** The last request issued to one path fragment, verb included. */
function lastRequestTo(fragment: string): SentRequest | undefined {
  for (let i = sent.length - 1; i >= 0; i--) {
    if (sent[i].url.includes(fragment)) return sent[i]
  }
  return undefined
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
  // Passive recording, not interception: the dashboard holds a usage stream and
  // other long-lived API connections open, and a route() handler that re-issues
  // every one of them tears the page down. Listening records the verb the
  // browser actually used without touching the response.
  page.on('request', (request) => {
    sent.push({
      method: request.method(),
      url: request.url(),
      postData: request.postData(),
    })
  })

  await page.goto(`${app.baseURL}/dashboard/endpoint`)
  await createKey(page, 'e2e-persona-key')
}, 90_000)

afterAll(async () => {
  await app.stop()
})

/**
 * Makes the Persona entry reachable on the sidebar.
 *
 * The `jailbreak` tab is one of the two entries the default sidebar preset
 * hides (`DEFAULT_HIDDEN_NAV_IDS`), and `App.svelte` redirects a deep link to a
 * hidden entry back to Endpoint & Key — so a fresh gateway answers
 * `/dashboard/jailbreak` with the home page. The setting is written the way the
 * Settings > Sidebar switch writes it, which is the only surface that turns the
 * entry on.
 */
async function revealPersonaEntry(): Promise<void> {
  // Issued from the page, not from the test process: the dashboard auth is a
  // session cookie, so a bare fetch from Node answers 401.
  const status = await page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/api/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ navVisibility: { hidden: [], order: [] } }),
    })
    return res.status
  }, app.baseURL)
  expect(status).toBe(200)
}

describe('persona page, per-key scope', () => {
  test(
    'mounts the per-key editor with the client key in its picker',
    async () => {
      await revealPersonaEntry()
      await page.goto(`${app.baseURL}/dashboard/jailbreak`)
      // No networkidle: the usage stream and other boot fetches hold the network
      // busy by design, so the wait never settles. The controls below are their
      // own readiness signal.
      await page.waitForLoadState('domcontentloaded')

      // The editor's own label, so a page that renders the playground but not
      // the key editor fails here rather than passing on a sibling control.
      const keyPicker = page.getByLabel('API key', { exact: true })
      await keyPicker.waitFor({ timeout: SLOW })

      // The created key is offered by name, which proves the view carries the
      // key list the editor needs rather than rendering an empty picker.
      expect(await keyPicker.locator('option', { hasText: 'e2e-persona-key' }).count()).toBe(1)
    },
    SLOW,
  )

  test(
    'arms a key binding with PUT and lists the row',
    async () => {
      // A preset to pick, created the way an operator does. The name is chosen
      // so the assertion below names the preset the row shows.
      await page.getByRole('button', { name: 'New preset' }).click()
      await page.locator('input[placeholder="Expert Coder"]').fill(PRESET_NAME)
      await page.locator('input[placeholder="expert-coder"]').fill('e2e-scoped-persona')
      // The preset modal's own textarea, addressed by label: the tester on the same
      // page carries a second prompt field, and a positional locator would fill
      // whichever comes first in the DOM.
      await page.getByLabel('Preset prompt').fill('E2E scoped persona prompt.')
      await page.getByRole('button', { name: 'Save', exact: true }).click()
      // The modal closes on save, which is the signal the write landed.
      await page.getByRole('button', { name: 'Save', exact: true }).waitFor({ state: 'detached' })

      // The per-key editor: model id, preset, then Arm.
      await page.getByLabel('Model id for this key').fill(MODEL_ID)
      await page.getByLabel('Preset for this key').selectOption({ label: PRESET_NAME })
      await page.getByRole('button', { name: 'Arm for key' }).click()

      // The row lands in the key's own table, and the status line names the
      // model.
      await page.getByText(`Armed "${MODEL_ID}"`).waitFor({ timeout: SLOW })
      expect(await page.getByText(MODEL_ID, { exact: true }).count()).toBeGreaterThan(0)

      // The verb is the regression: POST answered 405 for every write this
      // feature needed, and only the client could have caught it.
      const write = lastRequestTo('/api/jailbreak/keys/model')
      expect(write).toBeDefined()
      expect(write?.method).toBe('PUT')
      // And it carried the scope the operator chose, not an empty payload.
      expect(write?.postData).toContain(MODEL_ID)
    },
    SLOW,
  )

  test(
    'resolves the key binding outranking the global one',
    async () => {
      // The playground is the surface that proves the binding resolves: the same
      // id answered "no injection" globally and "injected" under the key, which
      // is the whole claim of the feature.
      await page.getByLabel('Model id to resolve').fill(MODEL_ID)
      await page.getByLabel('API key to resolve as').selectOption({ label: 'e2e-persona-key' })
      await page.getByRole('button', { name: 'Resolve' }).click()

      await page.getByText('Only this key injects — global does not').waitFor({ timeout: SLOW })

      // The verdict sentence is the summary; the columns are the evidence. The
      // global column must read as not injected, otherwise a swapped pair would
      // still produce the same verdict sentence.
      const globalColumn = page
        .getByText('empty scope')
        .locator('xpath=ancestor::div[contains(@class,"rounded-[10px]")][1]')
      await globalColumn.waitFor({ timeout: SLOW })
      expect(await globalColumn.getByText('NOT INJECTED').count()).toBeGreaterThan(0)
    },
    SLOW,
  )
  test(
    'offers the model catalog as suggestions on every model-id input',
    async () => {
      // The datalist is page-level: one list, referenced by id from the
      // single-bind modal, the per-key editor and the resolver check. The
      // regression this pins is a catalog that loads but never reaches the
      // inputs — the picker would be free text again, which no other gate
      // renders.
      await page.goto(`${app.baseURL}/dashboard/jailbreak`)
      await page.waitForLoadState('domcontentloaded')

      const datalist = page.locator('datalist#jailbreak-model-catalog')
      // A datalist is hidden by design, so 'attached' rather than the default
      // 'visible': the element exists in the DOM and feeds the inputs without
      // ever painting.
      await datalist.waitFor({ state: 'attached', timeout: SLOW })
      // The static catalog ships with the binary, so a fresh gateway always
      // has at least one id; this is the readiness signal for the fetch.
      await datalist.locator('option').first().waitFor({ state: 'attached', timeout: SLOW })
      // Exact value, not hasText: hasText is a substring match, and the
      // catalog also carries deepseek/deepseek-v4-flash-vision-exp.
      expect(await datalist.locator('option[value="deepseek/deepseek-v4-flash"]').count()).toBe(1)

      // Each input keeps its own label, so the three locators cannot
      // collapse onto one field the way a positional one would.
      const perKeyInput = page.getByLabel('Model id for this key')
      await perKeyInput.waitFor({ timeout: SLOW })
      expect(await perKeyInput.getAttribute('list')).toBe('jailbreak-model-catalog')

      const resolverInput = page.getByLabel('Model id to resolve')
      await resolverInput.waitFor({ timeout: SLOW })
      expect(await resolverInput.getAttribute('list')).toBe('jailbreak-model-catalog')

      // The modal input only exists while the modal is open. The Add button is
      // addressed by its position after the "Model bindings" heading rather than
      // by accessible name: the icon ligature pollutes the name, and a bare
      // "Add" is not unique on this page.
      await page
        .getByRole('heading', { name: /^Model bindings/ })
        .locator('xpath=following::button[1]')
        .click()
      const modalInput = page.getByLabel('Model / provider / combo id')
      await modalInput.waitFor({ timeout: SLOW })
      expect(await modalInput.getAttribute('list')).toBe('jailbreak-model-catalog')
    },
    SLOW,
  )
})