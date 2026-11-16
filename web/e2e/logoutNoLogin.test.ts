/**
 * Issue #1218: with Require login off, Logout used to clear the cookie and
 * send the browser to /login, where the auth-status check saw requireLogin=false
 * and bounced straight back to the dashboard. The click looked ignored.
 *
 * This suite asserts both sides of the contract at the DOM level: the disabled
 * path explains itself in place and never calls the logout endpoint, while the
 * enabled path still logs out and lands on the login view.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import type { Dashboard } from './harness'

import { startDashboard } from './harness'

const SLOW = 30_000
let app: Dashboard
let requireLogin = false
let logoutCalls = 0

beforeAll(async () => {
  app = await startDashboard(20483)
  await app.page.route('**/api/settings/require-login', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        requireLogin,
        authenticated: true,
        tunnelDashboardAccess: false,
      }),
    }),
  )
  await app.page.route('**/api/auth/logout', (route) => {
    logoutCalls++
    route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
  })
}, 60_000)

afterAll(async () => {
  await app.stop()
})

async function clickLogout() {
  await app.page.getByRole('button', { name: 'Account and display options' }).click()
  await app.page.getByRole('menuitem', { name: 'Logout' }).click()
}

describe('logout when login is disabled', () => {
  test('explains why logout is unavailable instead of bouncing back to the dashboard', async () => {
    const page = app.page
    await page.goto(`${app.baseURL}/dashboard/endpoint`)
    await page.getByRole('heading', { name: 'Endpoint' }).first().waitFor({ timeout: SLOW })

    await clickLogout()

    const dialog = page.getByRole('dialog', { name: 'Login is not required' })
    await dialog.waitFor({ timeout: SLOW })
    await expect(dialog.getByText('Logout is unavailable')).toBeTruthy()
    await expect(dialog.getByText('Profile')).toBeTruthy()
    expect(page.url()).toContain('/dashboard/endpoint')
    expect(logoutCalls).toBe(0)

    // Escape is the documented keyboard exit for every modal in this dashboard.
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'hidden', timeout: SLOW })
  }, SLOW)

  test('still logs out when Require login is on', async () => {
    requireLogin = true
    const page = app.page
    await page.goto(`${app.baseURL}/dashboard/endpoint`)
    await page.getByRole('heading', { name: 'Endpoint' }).first().waitFor({ timeout: SLOW })

    await clickLogout()

    await page.waitForURL('**/login', { timeout: SLOW })
    expect(logoutCalls).toBe(1)
  }, SLOW)
})
