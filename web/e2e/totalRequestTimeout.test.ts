/**
 * Total Request Timeout: the dashboard control for the end-to-end request
 * bound added in PR #1527.
 *
 * The bound's arithmetic is unit-tested against a hung upstream. What this e2e
 * adds is the half a unit test cannot see: the operator types a value, saves,
 * reloads, and the API hands the same number back. A control wired to nothing
 * passes every other gate in this repo.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Page } from '@playwright/test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20481
const SLOW = 60_000

let app: Dashboard
let page: Page

function timeoutInput(page: Page) {
  return page.locator('#total-request-timeout')
}

beforeAll(async () => {
  app = await startDashboard(PORT)
  page = app.page
}, SLOW)

afterAll(async () => {
  await app.stop()
})

describe('total request timeout', () => {
  test(
    'saves the value and shows it again after a reload',
    async () => {
      await page.getByRole('link', { name: /Settings/ }).click()
      await timeoutInput(page).waitFor()

      await timeoutInput(page).fill('600000')
      await page.getByRole('button', { name: /Save/ }).first().click()

      await page.reload()
      await timeoutInput(page).waitFor()
      expect(await timeoutInput(page).inputValue()).toBe('600000')
    },
    SLOW,
  )

  test(
    'turning it off with 0 is stored as off rather than dropped',
    async () => {
      await page.getByRole('link', { name: /Settings/ }).click()
      await timeoutInput(page).waitFor()

      await timeoutInput(page).fill('0')
      await page.getByRole('button', { name: /Save/ }).first().click()
      await page.reload()
      await timeoutInput(page).waitFor()

      expect(await timeoutInput(page).inputValue()).toBe('0')
    },
    SLOW,
  )
})
