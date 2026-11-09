/**
 * The auto-ping schedule editor: before this existed the schedule could only be
 * written by PUTting a raw settings blob, so the dashboard offered a "on reset"
 * switch and no way to configure the anchored schedule the scheduler actually
 * reads (internal/schedule/scheduler.go iterates cfg.Schedules).
 *
 * The assertions are on what the operator sees and what reaches the server, not
 * on "the modal opened" — an opened modal that saves the wrong shape is the
 * failure this suite exists for.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const SLOW = 20_000

let app: Dashboard
let page: Page

/**
 * Sessions are cookies, so every authenticated call goes through Playwright's
 * request context (`page.request`) rather than a bare fetch — a bare fetch has
 * no session and the dashboard answers 401.
 */
async function seedClaudeConnection(page: Page, baseURL: string): Promise<void> {
  const res = await page.request.post(`${baseURL}/api/connections`, {
    data: {
      provider: 'claude',
      authType: 'oauth',
      name: 'e2e-claude',
      providerSpecificData: { authMethod: 'oauth' },
    },
  })
  if (!res.ok()) {
    throw new Error(`seed connection: ${res.status()} ${await res.text()}`)
  }
}

/** Reads one key of the settings blob through the logged-in session. */
async function readSettings(page: Page, baseURL: string): Promise<Record<string, unknown>> {
  const res = await page.request.get(`${baseURL}/api/settings`)
  if (!res.ok()) {
    throw new Error(`read settings: ${res.status()} ${await res.text()}`)
  }
  return (await res.json()) as Record<string, unknown>
}

/** Slots the modal previews, as one string. */
async function previewText(page: Page): Promise<string> {
  return (await page.locator('p:has-text("→")').first().innerText()).trim()
}

beforeAll(async () => {
  app = await startDashboard(20431)
  page = app.page
  await seedClaudeConnection(page, app.baseURL)
  await page.goto(`${app.baseURL}/dashboard/quota`)
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('auto-ping schedule editor', () => {
  test(
    'the row offers a schedule control and opens the editor',
    async () => {
      // The quota tracker renders one card per connection, not a <tr>, so the
      // control is addressed by its accessible name. There is exactly one
      // connection in this fixture, so the name is unambiguous.
      await page.getByRole('button', { name: 'Configure auto-ping schedule' }).click()

      await page.getByRole('heading', { name: 'Auto-ping settings' }).waitFor()

      // Three modes, and the schedule one is not preselected for a connection
      // with neither half set — opening the editor must not silently enable a
      // schedule the operator never asked for.
      //
      // Each mode button carries its material icon as text ("block Off"), so
      // the names are matched as substrings rather than exactly.
      for (const label of [/Off$/, /On reset$/, /Schedule$/]) {
        await page.getByRole('button', { name: label }).waitFor()
      }
      await page.getByRole('button', { name: /Schedule$/ }).click()
    },
    SLOW
  )

  test(
    'the preview matches the values in the form',
    async () => {
      await page.getByLabel('Anchor time').fill('09:30')
      await page.getByLabel('Window (hours)').fill('5')
      await page.getByLabel('Pings per day').fill('3')

      expect(await previewText(page)).toBe('09:30 → 14:30 → 19:30')

      // A value the window cannot fit is refused rather than clamped on the way
      // out: the operator gets the bound, not a schedule quietly different from
      // the one they typed.
      await page.getByLabel('Pings per day').fill('9')
      await page.getByRole('button', { name: 'Save' }).waitFor()
      expect(await page.getByRole('button', { name: 'Save' }).isDisabled()).toBe(true)
    },
    SLOW
  )

  test(
    'saving writes both halves of the settings key',
    async () => {
      await page.getByLabel('Pings per day').fill('3')
      await page.getByRole('button', { name: 'Save' }).click()
      await page.getByRole('heading', { name: 'Auto-ping settings' }).waitFor({ state: 'hidden' })

      // The schedule half, read back from the server — the whole point of the
      // editor is that the value the scheduler reads is the one set here.
      const settings = await readSettings(page, app.baseURL)
      const claude = settings.claudeAutoPing as {
        connections?: Record<string, boolean>
        schedules?: Record<string, { enabled?: boolean; anchor?: string; slotsPerDay?: number }>
      }
      const entries = Object.entries(claude?.schedules ?? {})
      expect(entries.length).toBe(1)
      const [, schedule] = entries[0]
      expect(schedule.enabled).toBe(true)
      expect(schedule.anchor).toBe('09:30')
      expect(schedule.slotsPerDay).toBe(3)

      // The switch half must be false: "Schedule" means the anchored slots, and
      // leaving the reactive ping on would fire both.
      const switches = Object.values(claude?.connections ?? {})
      expect(switches).toEqual([false])
    },
    SLOW
  )

  test(
    'the row lights the schedule control once a schedule exists',
    async () => {
      const button = page.getByRole('button', { name: 'Configure auto-ping schedule' })
      // The modal closed before the reload, so the row has to reflect the
      // persisted half rather than local state.
      await page.reload()
      await button.waitFor()
      expect(await button.getAttribute('class')).toContain('text-primary')
    },
    SLOW
  )

  test(
    'choosing Off clears the schedule',
    async () => {
      await page.getByRole('button', { name: 'Configure auto-ping schedule' }).click()
      await page.getByRole('button', { name: /Off$/ }).click()
      await page.getByRole('button', { name: 'Save' }).click()
      await page.getByRole('heading', { name: 'Auto-ping settings' }).waitFor({ state: 'hidden' })

      // The server deletes the entry rather than storing `{enabled:false}`
      // (internal/schedule/autopingconfig.go): keeping the numbers would
      // resurrect a schedule the operator stopped. So "cleared" means absent,
      // and the assertion has to be on absence rather than on a flag.
      const settings = await readSettings(page, app.baseURL)
      const claude = settings.claudeAutoPing as {
        connections?: Record<string, boolean>
        schedules?: Record<string, unknown>
      }
      expect(Object.keys(claude?.schedules ?? {})).toEqual([])
      // ...and the switch half must be off too, or the reactive ping would
      // still fire after the operator chose Off.
      expect(Object.values(claude?.connections ?? {})).toEqual([false])
    },
    SLOW
  )
})
