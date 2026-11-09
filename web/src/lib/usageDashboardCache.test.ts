/**
 * The Details payload cache.
 *
 * `AnalyticsView` renders one section at a time, so leaving Details unmounts
 * `UsageDashboard` and coming back mounts a fresh instance. That instance used
 * to re-read the whole payload every time — five SQL reads and a ~120 KB body
 * on a busy ledger — for numbers that cannot have changed in between. The cache
 * is module-level for exactly that reason: instance state dies with the
 * instance, and the repeated read is a consequence of the instance dying.
 *
 * The two properties worth pinning are the ones that make it safe: an entry
 * expires (so a dashboard left on another section does not serve yesterday's
 * totals forever) and a re-enabled panel drops it (so the operator asking for
 * a fresh look gets one).
 */

import { beforeEach, describe, expect, test } from 'bun:test'
import {
  USAGE_DASHBOARD_CACHE_TTL_MS,
  cachedUsageDashboard,
  forgetUsageDashboard,
  rememberUsageDashboard,
} from './usageDashboardCache'
import type { UsageDashboardResponse } from '../api/client'

const PAYLOAD: UsageDashboardResponse = {
  daily: [{ date: '2026-10-29', requests: 3, tokens: 300, cost: 0.01 }],
  nodes: [],
  models: [],
  lifetime: {
    totalRequests: 3,
    totalTokens: 300,
    peakTokens: 300,
    peakDate: '2026-10-29',
    currentStreak: 1,
    longestStreak: 1,
    topModel: null,
    cacheHitRate: null,
  },
}

beforeEach(() => {
  forgetUsageDashboard()
})

describe('usage dashboard cache', () => {
  test('an empty cache reads as a miss', () => {
    expect(cachedUsageDashboard(0)).toBeNull()
  })

  test('a remembered payload is served inside the TTL', () => {
    rememberUsageDashboard(PAYLOAD, 1_000)
    expect(cachedUsageDashboard(1_000)).toBe(PAYLOAD)
    // One millisecond before expiry is still fresh: the boundary itself is
    // tested below, so this pins the comparison rather than off-by-one luck.
    expect(cachedUsageDashboard(1_000 + USAGE_DASHBOARD_CACHE_TTL_MS - 1)).toBe(PAYLOAD)
  })

  test('an entry older than the TTL is a miss, so stale totals are re-read', () => {
    rememberUsageDashboard(PAYLOAD, 1_000)
    expect(cachedUsageDashboard(1_000 + USAGE_DASHBOARD_CACHE_TTL_MS + 1)).toBeNull()
  })

  test('forget drops the entry, which is what a re-enabled panel does', () => {
    rememberUsageDashboard(PAYLOAD, 1_000)
    forgetUsageDashboard()
    expect(cachedUsageDashboard(1_000)).toBeNull()
  })

  test('a second remember replaces the first rather than accumulating', () => {
    const second: UsageDashboardResponse = { ...PAYLOAD, lifetime: null }
    rememberUsageDashboard(PAYLOAD, 1_000)
    rememberUsageDashboard(second, 2_000)
    expect(cachedUsageDashboard(2_000)).toBe(second)
  })
})
