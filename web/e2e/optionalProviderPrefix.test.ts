/**
 * Name-driven prefix and URL-suffix autofill for compatible provider creation.
 *
 * The Add form derives both values from the custom name, lowercases capitals,
 * and turns separators into dashes. Either field can be taken over by typing;
 * after that it remains operator-owned when the name changes. This is create
 * only: editing a live provider's prefix or suffix changes routing identities.
 * Prefix suggestions also consult the full reserved-id/alias set from the
 * server so they never suggest a name the create route refuses with 409.
 */


import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
let page: Page

beforeAll(async () => {
  app = await startDashboard(20418)
  page = app.page
}, 60_000)

afterAll(async () => {
  await app.stop()
})

/**
 * The input a field label sits above.
 *
 * Input.svelte emits the label and the input as siblings inside one wrapper
 * and gives the input no id, so `getByLabel()` finds nothing. Walking to the
 * label's own parent and taking that wrapper's single control is the narrowest
 * handle the markup supports.
 */
function field(page: Page, label: string) {
  return page
    .locator('label')
    .filter({ hasText: new RegExp(`^${label}`) })
    .locator('xpath=..')
    .locator('input, textarea, select')
    .first()
}

/**
 * Closes whichever compatible-node dialog is open.
 *
 * Test 1 aborts with the Add dialog up when it fails on base, and a leftover
 * modal's backdrop swallows every click after it — which would make test 2
 * fail on a cascade instead of on its own assertion. Each case therefore
 * leaves no dialog behind, pass or fail.
 */
async function closeOpenDialog(page: Page) {
  const addButton = page.getByRole('button', { name: 'Create', exact: true })
  if (await addButton.isVisible().catch(() => false)) {
    await page.keyboard.press('Escape')
    await addButton.waitFor({ state: 'hidden' })
  }
  const editTitle = page.getByText('Edit OpenAI Compatible')
  if (await editTitle.isVisible().catch(() => false)) {
    await page.keyboard.press('Escape')
    await editTitle.waitFor({ state: 'hidden' })
  }
}

describe('compatible provider name autofill', () => {
  test(
    'derives both fields from the name and follows subsequent name changes',
    async () => {
      await page.getByRole('link', { name: 'Providers' }).click()
      await page.getByRole('button', { name: 'Add Custom Provider' }).click()
      await page.getByRole('radio', { name: /OpenAI/ }).first().waitFor()

      await field(page, 'Name').fill('No Prefix Node')
      expect(await field(page, 'Prefix').inputValue()).toBe('no-prefix-node')
      expect(await field(page, 'Custom URL Suffix').inputValue()).toBe('no-prefix-node')

      await field(page, 'Name').fill('Acme Gateway 2')
      expect(await field(page, 'Prefix').inputValue()).toBe('acme-gateway-2')
      expect(await field(page, 'Custom URL Suffix').inputValue()).toBe('acme-gateway-2')
      await field(page, 'Base URL').fill('https://api.example.com/v1')

      try {
        await page.getByRole('button', { name: 'Create', exact: true }).click()
        await page.getByText('OpenAI Compatible Details').waitFor()
        await page.getByRole('button', { name: 'Edit' }).click()
        await page.getByText('Edit OpenAI Compatible').waitFor()
        expect(await field(page, 'Prefix').inputValue()).toBe('acme-gateway-2')
        expect(await field(page, 'Custom URL Suffix').inputValue()).toBe('acme-gateway-2')
      } finally {
        await closeOpenDialog(page)
      }
    },
    60_000,
  )

  test(
    'keeps both values operator-owned after they are edited',
    async () => {
      await page.getByRole('link', { name: 'Providers' }).click()
      await page.getByRole('button', { name: 'Add Custom Provider' }).click()
      await page.getByRole('radio', { name: /OpenAI/ }).first().waitFor()

      await field(page, 'Name').fill('Manual Namespace')
      await field(page, 'Prefix').fill('manual-prefix')
      await field(page, 'Custom URL Suffix').fill('mnsfx')
      await field(page, 'Name').fill('Changed Display Name')
      expect(await field(page, 'Prefix').inputValue()).toBe('manual-prefix')
      expect(await field(page, 'Custom URL Suffix').inputValue()).toBe('mnsfx')
      await field(page, 'Base URL').fill('https://api.example.com/v1')

      try {
        await page.getByRole('button', { name: 'Create', exact: true }).click()
        await page.getByText('OpenAI Compatible Details').waitFor()
        await page.getByRole('button', { name: 'Edit' }).click()
        await page.getByText('Edit OpenAI Compatible').waitFor()
        expect(await field(page, 'Prefix').inputValue()).toBe('manual-prefix')
        expect(await field(page, 'Custom URL Suffix').inputValue()).toBe('mnsfx')
      } finally {
        await closeOpenDialog(page)
      }
    },
    60_000,
  )

  test(
    'shows live availability for the suggested prefix and suffix',
    async () => {
      await page.getByRole('link', { name: 'Providers' }).click()
      await page.getByRole('button', { name: 'Add Custom Provider' }).click()
      await page.getByRole('radio', { name: /OpenAI/ }).first().waitFor()

      // A free name: both identifiers report available.
      await field(page, 'Name').fill('Availability Probe')
      const list = page.getByRole('list', { name: 'Name availability' })
      await list.waitFor()
      await page.getByText('available', { exact: true }).first().waitFor()
      const rows = await list.locator('li').allTextContents()
      expect(rows.length).toBe(2)
      expect(rows.join(' ')).toContain('availability-probe')
      expect(rows.join(' ')).not.toContain('taken')

      // A reserved name: the suggestion must NOT become the reserved prefix —
      // the field stays blank and the row reports the automatic server fill.
      await field(page, 'Name').fill('OpenAI')
      await page.getByText('A prefix will be assigned automatically when you save.').waitFor()
      expect(await field(page, 'Prefix').inputValue()).toBe('')

      // Typing a reserved prefix by hand is the taken path: the row names the
      // owning provider, exactly like a username picker reports a taken name.
      await field(page, 'Prefix').fill('openai')
      await page.getByText('Already used by provider "openai".').waitFor()

      // The suffix slug is not a prefix namespace: `openai` stays available
      // as a suffix even though it is reserved as a prefix.
      await field(page, 'Prefix').fill('probe-ns')
      await page.getByText('available', { exact: true }).first().waitFor()

      await page.keyboard.press('Escape')
      await page.getByRole('button', { name: 'Create', exact: true }).waitFor({ state: 'hidden' })
    },
    60_000,
  )
})
