/**
 * Combo Member Attempt Cap (#1518): the dashboard control for the setting the
 * combo fallback loops read.
 *
 * The e2e gate exists because the value has to survive the round trip an
 * operator actually performs: type it, save, reload, and the field still shows
 * it. A unit test on the resolver proves the arithmetic and nothing about
 * whether the control is wired to the API at all.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20471
const SLOW = 60_000

let app: Dashboard
let page: Page

function capInput(page: Page) {
  return page.locator('#combo-member-cap')
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('combo member attempt cap', () => {
  test(
    'saves the value and shows it again after a reload',
    async () => {
      await page.getByRole('link', { name: /Settings/ }).click()
      await capInput(page).waitFor()

      await capInput(page).fill('4')
      await page.getByRole('button', { name: /Save/ }).first().click()

      // The value comes from the API, not from the field it was typed into:
      // a reload drops every piece of client state.
      await page.reload()
      await capInput(page).waitFor()
      expect(await capInput(page).inputValue()).toBe('4')
    },
    SLOW,
  )

  test(
    'disabling with 0 is stored as off rather than dropped',
    async () => {
      await page.getByRole('link', { name: /Settings/ }).click()
      await capInput(page).waitFor()

      await capInput(page).fill('0')
      await page.getByRole('button', { name: /Save/ }).first().click()
      await page.reload()
      await capInput(page).waitFor()

      expect(await capInput(page).inputValue()).toBe('0')
    },
    SLOW,
  )
})
