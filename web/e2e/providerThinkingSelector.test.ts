/**
 * Issue #1214: per-provider Thinking control for OpenAI-compatible custom nodes
 * and explicit `Off` option.
 *
 * Previously:
 *   1. `ProviderDetailView.svelte` only rendered the Thinking `<select>` inside
 *      the built-in provider `{:else}` branch, so compatible custom nodes (e.g.
 *      `openai-compatible-chat-amd`) had no Thinking selector in the dashboard
 *      at all.
 *   2. `providerThinkingLevels` filtered out `'none'` and returned `null` when
 *      models lacked `thinkingLevels` metadata, leaving no explicit `Off`
 *      option even when the backend accepted `providerThinking[id] = { mode: "off" }`.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20461
const SLOW = 60_000

let app: Dashboard
let page: Page

function field(page: Page, label: string) {
  return page
    .locator('label')
    .filter({ hasText: new RegExp(`^${label}`) })
    .locator('xpath=..')
    .locator('input, textarea, select')
    .first()
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('provider thinking selector (#1214)', () => {
  test(
    'exposes Thinking control with Off option on OpenAI-compatible custom nodes and persists mode',
    async () => {
      await page.getByRole('link', { name: 'Providers' }).click()
      await page.getByRole('button', { name: 'Add Custom Provider' }).click()
      await page.getByRole('radio', { name: /OpenAI/ }).first().waitFor()

      await field(page, 'Name').fill('AMD Radeon')
      await field(page, 'Prefix').fill('amd')
      await field(page, 'Base URL').fill('https://developer.amd.com.cn/radeon/api/v1')
      await page.getByRole('button', { name: 'Create', exact: true }).click()

      await page.getByText('OpenAI Compatible Details').waitFor()
      await page.waitForURL('**/dashboard/providers/openai-compatible-*')
      const url = new URL(page.url())
      const nodeId = url.pathname.split('/').pop() ?? ''
      expect(nodeId.startsWith('openai-compatible-')).toBe(true)

      const thinkingSelect = page.getByRole('combobox', { name: 'Provider thinking mode' })
      await thinkingSelect.waitFor({ timeout: 10_000 })
      expect(await thinkingSelect.inputValue()).toBe('auto')

      const options = await thinkingSelect
        .locator('option')
        .evaluateAll((els) => els.map((el) => (el as HTMLOptionElement).value))
      expect(options).toEqual(['auto', 'off', 'on'])

      await Promise.all([
        page.waitForResponse(
          (res) =>
            res.url().includes('/api/settings') &&
            res.request().method() === 'PUT' &&
            res.status() === 200,
        ),
        thinkingSelect.selectOption('off'),
      ])

      const afterSave = await (await page.request.get(`${app.baseURL}/api/settings`)).json()
      expect(afterSave.providerThinking?.[nodeId]?.mode).toBe('off')

      await page.reload()
      await thinkingSelect.waitFor()
      expect(await thinkingSelect.inputValue()).toBe('off')

      await Promise.all([
        page.waitForResponse(
          (res) =>
            res.url().includes('/api/settings') &&
            res.request().method() === 'PUT' &&
            res.status() === 200,
        ),
        thinkingSelect.selectOption('auto'),
      ])

      const afterReset = await (await page.request.get(`${app.baseURL}/api/settings`)).json()
      expect(afterReset.providerThinking?.[nodeId]).toBeUndefined()
    },
    SLOW,
  )

  test(
    'built-in reasoning provider exposes explicit Off option without appending (off) suffix to model ids',
    async () => {
      await page.goto(`${app.baseURL}/dashboard/providers/openai`)
      const modelsCardHeader = page.getByRole('heading', { name: 'Available Models' }).locator('xpath=..')
      const thinkingSelect = modelsCardHeader.locator('select')
      await thinkingSelect.waitFor({ timeout: 10_000 })

      const options = await thinkingSelect
        .locator('option')
        .evaluateAll((els) => els.map((el) => (el as HTMLOptionElement).value))
      expect(options).toContain('auto')
      expect(options).toContain('off')
      expect(await thinkingSelect.getAttribute('aria-label')).toBe('Provider thinking mode')

      await Promise.all([
        page.waitForResponse(
          (res) =>
            res.url().includes('/api/settings') &&
            res.request().method() === 'PUT' &&
            res.status() === 200,
        ),
        thinkingSelect.selectOption('off'),
      ])

      await page.reload()
      await thinkingSelect.waitFor()
      expect(await thinkingSelect.inputValue()).toBe('off')

      const modelIds = await page.evaluate(() =>
        Array.from(document.querySelectorAll('code'))
          .map((el) => (el.textContent ?? '').trim())
          .filter((t) => t.startsWith('openai/')),
      )
      expect(modelIds.length).toBeGreaterThan(0)
      for (const id of modelIds) {
        expect(id.includes('(off)')).toBe(false)
      }

      await Promise.all([
        page.waitForResponse(
          (res) =>
            res.url().includes('/api/settings') &&
            res.request().method() === 'PUT' &&
            res.status() === 200,
        ),
        thinkingSelect.selectOption('auto'),
      ])
    },
    SLOW,
  )
})
