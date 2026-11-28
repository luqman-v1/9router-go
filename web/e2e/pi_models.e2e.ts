import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { startDashboard, type Dashboard } from './harness'

let app: Dashboard

beforeAll(async () => {
  app = await startDashboard(20421)
}, 20_000)

afterAll(async () => {
  await app.stop()
})

describe('Pi models editor smoke', () => {
  test('dashboard loads', async () => {
    await app.page.goto(`${app.baseURL}/dashboard/cli-tools`)
    const body = await app.page.content()
    expect(body.length).toBeGreaterThan(0)
  })
})
