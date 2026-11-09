/**
 * One-entry cache for the Usage › Details payload.
 *
 * The Details section is remounted every time the operator switches between
 * Usage, Cache Analytics and Compression Analytics: `AnalyticsView` renders one
 * section at a time, so leaving Details unmounts it and coming back mounts a
 * fresh instance. That used to re-read the whole dashboard — five SQL reads and
 * a ~120 KB payload on a busy ledger — for numbers that cannot have changed in
 * between.
 *
 * Living outside the component is the whole point: instance state dies with the
 * instance, and the reason the read repeats is that the instance dies.
 *
 * The TTL bounds it. A dashboard left open on another section must not serve
 * stale totals forever, and the read is cheap on every window but the year-long
 * one, so an expired entry costs a round trip rather than correctness.
 */

import type { UsageDashboardResponse } from '../api/client'

interface Entry {
  at: number
  payload: UsageDashboardResponse
}

/** How long a cached payload may be drawn before it is read again. */
export const USAGE_DASHBOARD_CACHE_TTL_MS = 60_000

let entry: Entry | null = null

/** The cached payload while it is still fresh, otherwise null. */
export function cachedUsageDashboard(now: number = Date.now()): UsageDashboardResponse | null {
  if (!entry) return null
  if (now - entry.at > USAGE_DASHBOARD_CACHE_TTL_MS) return null
  return entry.payload
}

/** Records a payload just read from the server. */
export function rememberUsageDashboard(
  payload: UsageDashboardResponse,
  now: number = Date.now(),
): void {
  entry = { at: now, payload }
}

/**
 * Drops the entry so the next mount reads again.
 *
 * Called when a Details panel is turned back on: that is the one moment the
 * operator is asking for a fresh look at a section they had hidden, and the
 * old code re-read there too.
 */
export function forgetUsageDashboard(): void {
  entry = null
}
