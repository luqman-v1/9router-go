import type { ProxyPool } from '../../api/client'

export type PoolStatusFilter = 'all' | 'active' | 'passed' | 'failed'
export type PoolSortOption = 'default' | 'fastest' | 'recently_tested' | 'name'

export function getStatusVariant(status: string | null | undefined): 'success' | 'error' | 'default' {
  if (status === 'active' || status === 'passed') return 'success'
  if (status === 'error' || status === 'failed') return 'error'
  return 'default'
}

export function getLatencyBadge(
  latency?: number | null
): { variant: 'success' | 'warning' | 'error'; text: string } | null {
  if (!latency || latency <= 0) return null
  const ms = Math.round(latency)
  if (ms < 300) {
    return { variant: 'success', text: `⚡ ${ms}ms` }
  }
  if (ms <= 800) {
    return { variant: 'warning', text: `⏳ ${ms}ms` }
  }
  return { variant: 'error', text: `🐢 ${ms}ms` }
}

/**
 * A pool counts as failed when its last probe reported a hard failure. The
 * gateway writes `failed`, upstream `decolua/9router` writes `error`, and rows
 * imported from an upstream database can still carry either.
 */
export function isFailedStatus(pool: ProxyPool): boolean {
  return pool.testStatus === 'failed' || pool.testStatus === 'error'
}

/**
 * Latency is only meaningful for a pool that answered. A failed pool stores 0
 * and an untested one has no measurement, so both must stay unranked.
 * `active` is accepted for the upstream vocabulary, `passed` for ours.
 */
export function hasMeasuredLatency(pool: ProxyPool): boolean {
  return pool.testStatus === 'passed' || pool.testStatus === 'active'
}

export function filterProxyPools(pools: ProxyPool[], filter: PoolStatusFilter): ProxyPool[] {
  if (filter === 'active') return pools.filter((p) => p.isActive === true)
  if (filter === 'passed') return pools.filter((p) => p.testStatus === 'passed')
  if (filter === 'failed') return pools.filter(isFailedStatus)
  return pools
}

// Bulk actions must never include rows hidden by the current filter — the
// selection set can hold ids that the active status filter has taken off the
// table, and acting on those would surprise the operator who only sees half
// the list. Port of upstream src/shared/utils/proxyPoolSelection.js
// getVisibleProxyPoolSelection (12256f72).
export function getVisibleProxyPoolSelection(pools: ProxyPool[], selectedIds: string[]): string[] {
  const selected = new Set(selectedIds)
  return pools.filter((pool) => selected.has(pool.id)).map((pool) => pool.id)
}

export interface ProxyPoolSelectionChange {
  id: string
  anchorId?: string
  checked: boolean
  shiftKey?: boolean
}

// updateProxyPoolSelection applies one checkbox change; with shiftKey and an
// anchorId it selects (or clears) the whole visible range between anchor and
// clicked row, mirroring the file-manager convention. The result is ordered by
// the pools array so re-selecting a range never reshuffles existing ids.
// Port of upstream src/shared/utils/proxyPoolSelection.js
// updateProxyPoolSelection (12256f72).
export function updateProxyPoolSelection(
  pools: ProxyPool[],
  selectedIds: string[],
  change: ProxyPoolSelectionChange
): string[] {
  const selected = new Set(getVisibleProxyPoolSelection(pools, selectedIds))
  const ids = pools.map((pool) => pool.id)
  const index = ids.indexOf(change.id)
  if (index === -1) return [...selected]
  const anchor = change.shiftKey && change.anchorId ? ids.indexOf(change.anchorId) : -1
  const range =
    anchor === -1
      ? [change.id]
      : ids.slice(Math.min(anchor, index), Math.max(anchor, index) + 1)
  for (const target of range) {
    if (change.checked) selected.add(target)
    else selected.delete(target)
  }
  return ids.filter((target) => selected.has(target))
}

// Every comparator falls back to the same tie-break so equal keys keep a
// stable, predictable row order instead of whatever sort happened to produce.
const tieBreakByName = (a: ProxyPool, b: ProxyPool) => (a.name || '').localeCompare(b.name || '')

const rankByLatency = (pool: ProxyPool) =>
  hasMeasuredLatency(pool) && typeof pool.latency === 'number' && pool.latency > 0
    ? pool.latency
    : Infinity

const COMPARATORS: Record<
  Exclude<PoolSortOption, 'default'>,
  (a: ProxyPool, b: ProxyPool) => number
> = {
  fastest: (a, b) => {
    const diff = rankByLatency(a) - rankByLatency(b)
    // Infinity - Infinity is NaN, so only descend into subtraction when the
    // keys actually differ; equal keys fall through to the tie-break.
    return Number.isNaN(diff) || diff === 0 ? tieBreakByName(a, b) : diff
  },
  recently_tested: (a, b) => {
    const at = a.lastTestedAt ? Date.parse(a.lastTestedAt) || 0 : 0
    const bt = b.lastTestedAt ? Date.parse(b.lastTestedAt) || 0 : 0
    return at === bt ? tieBreakByName(a, b) : bt - at
  },
  name: tieBreakByName,
}

export function sortProxyPools(pools: ProxyPool[], option: PoolSortOption): ProxyPool[] {
  if (option === 'default') return pools
  return [...pools].sort(COMPARATORS[option])
}

export function poolStatusCounts(pools: ProxyPool[]): {
  active: number
  passed: number
  failed: number
} {
  return {
    active: pools.filter((p) => p.isActive === true).length,
    passed: pools.filter((p) => p.testStatus === 'passed').length,
    failed: pools.filter(isFailedStatus).length,
  }
}
