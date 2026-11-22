/**
 * Deep-linking directly to a media provider detail route (`/dashboard/media-providers/<kind>/<providerId>`)
 * must mount MediaProviderDetail after the provider catalog finishes loading, on both cold navigation
 * and browser reload.
 */

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'

import { startDashboard, type Dashboard } from './harness'

const PORT = 20489
const SLOW = 30_000
const DETAIL_PATH = '/dashboard/media-providers/image/stability-ai'

let app: Dashboard | undefined

beforeAll(async () => {
  app = await startDashboard(PORT)
}, SLOW)

afterAll(async () => {
  await app?.stop()
})

async function expectMediaProviderDetailVisible(dashboard: Dashboard) {
  const { page } = dashboard
  expect(new URL(page.url()).pathname).toBe(DETAIL_PATH)

  const connectionsHeading = page.getByRole('heading', { name: 'Connections', exact: true })
  await connectionsHeading.waitFor({ state: 'visible', timeout: 8_000 })
  expect(await connectionsHeading.isVisible()).toBe(true)

  const emptyState = page.getByText('No connections yet for Stability AI.')
  await emptyState.waitFor({ state: 'visible', timeout: 8_000 })
  expect(await emptyState.isVisible()).toBe(true)

  const addButton = page.getByRole('button', { name: /Add Connection/i })
  await addButton.waitFor({ state: 'visible', timeout: 8_000 })
  expect(await addButton.isVisible()).toBe(true)
}

describe('media provider deep-link', () => {
  test('cold deep-link renders MediaProviderDetail directly', async () => {
    const dashboard = app
    if (!dashboard) throw new Error('dashboard not started')

    await dashboard.page.goto(`${dashboard.baseURL}${DETAIL_PATH}`)
    await expectMediaProviderDetailVisible(dashboard)
  }, SLOW)

  test('reloading a media provider detail URL keeps MediaProviderDetail mounted', async () => {
    const dashboard = app
    if (!dashboard) throw new Error('dashboard not started')

    await dashboard.page.reload()
    await expectMediaProviderDetailVisible(dashboard)
  }, SLOW)
})
