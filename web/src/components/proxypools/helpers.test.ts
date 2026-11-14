import { describe, expect, test } from 'bun:test'
import type { ProxyPool } from '../../api/client'
import {
  filterProxyPools,
  getLatencyBadge,
  getVisibleProxyPoolSelection,
  getStatusVariant,
  hasMeasuredLatency,
  isFailedStatus,
  poolStatusCounts,
  sortProxyPools,
  updateProxyPoolSelection
} from './helpers'

function pool(over: Partial<ProxyPool> = {}): ProxyPool {
  return { id: 'id', name: 'pool', type: 'http', ...over }
}

describe('getStatusVariant', () => {
  test('returns success for active and passed', () => {
    expect(getStatusVariant('active')).toBe('success')
    expect(getStatusVariant('passed')).toBe('success')
  })

  test('returns error for error and failed', () => {
    expect(getStatusVariant('error')).toBe('error')
    expect(getStatusVariant('failed')).toBe('error')
  })

  test('returns default for unknown or unset states', () => {
    expect(getStatusVariant('unknown')).toBe('default')
    expect(getStatusVariant('')).toBe('default')
    expect(getStatusVariant(null)).toBe('default')
    expect(getStatusVariant(undefined)).toBe('default')
  })
})

describe('getLatencyBadge', () => {
  test('returns null for missing, zero, or negative latency', () => {
    expect(getLatencyBadge(undefined)).toBeNull()
    expect(getLatencyBadge(null)).toBeNull()
    expect(getLatencyBadge(0)).toBeNull()
    expect(getLatencyBadge(-50)).toBeNull()
  })

  test('returns green lightning badge for latency < 300ms', () => {
    expect(getLatencyBadge(50)).toEqual({ variant: 'success', text: '⚡ 50ms' })
    expect(getLatencyBadge(299)).toEqual({ variant: 'success', text: '⚡ 299ms' })
  })

  test('returns amber hourglass badge for latency between 300ms and 800ms', () => {
    expect(getLatencyBadge(300)).toEqual({ variant: 'warning', text: '⏳ 300ms' })
    expect(getLatencyBadge(550)).toEqual({ variant: 'warning', text: '⏳ 550ms' })
    expect(getLatencyBadge(800)).toEqual({ variant: 'warning', text: '⏳ 800ms' })
  })

  test('returns red turtle badge for latency > 800ms', () => {
    expect(getLatencyBadge(801)).toEqual({ variant: 'error', text: '🐢 801ms' })
    expect(getLatencyBadge(1250)).toEqual({ variant: 'error', text: '🐢 1250ms' })
  })

  test('rounds fractional latency to nearest integer', () => {
    expect(getLatencyBadge(123.4)).toEqual({ variant: 'success', text: '⚡ 123ms' })
    expect(getLatencyBadge(456.7)).toEqual({ variant: 'warning', text: '⏳ 457ms' })
  })
})

describe('isFailedStatus', () => {
  test('matches both the gateway and the upstream failure vocabulary', () => {
    expect(isFailedStatus(pool({ testStatus: 'failed' }))).toBe(true)
    expect(isFailedStatus(pool({ testStatus: 'error' }))).toBe(true)
  })

  test('does not match healthy or untested pools', () => {
    expect(isFailedStatus(pool({ testStatus: 'passed' }))).toBe(false)
    expect(isFailedStatus(pool({ testStatus: 'active' }))).toBe(false)
    expect(isFailedStatus(pool({ testStatus: 'unknown' }))).toBe(false)
    expect(isFailedStatus(pool())).toBe(false)
  })
})

describe('hasMeasuredLatency', () => {
  test('requires a status that means the proxy answered', () => {
    expect(hasMeasuredLatency(pool({ testStatus: 'passed' }))).toBe(true)
    expect(hasMeasuredLatency(pool({ testStatus: 'active' }))).toBe(true)
  })

  test('a failed or untested pool has no measurement worth ranking', () => {
    expect(hasMeasuredLatency(pool({ testStatus: 'failed' }))).toBe(false)
    expect(hasMeasuredLatency(pool({ testStatus: 'error' }))).toBe(false)
    expect(hasMeasuredLatency(pool({ testStatus: 'unknown' }))).toBe(false)
  })
})

describe('filterProxyPools', () => {
  const pools = [
    pool({ id: '1', name: 'active-passed', isActive: true, testStatus: 'passed' }),
    pool({ id: '2', name: 'inactive-passed', isActive: false, testStatus: 'passed' }),
    pool({ id: '3', name: 'active-failed', isActive: true, testStatus: 'failed' }),
    pool({ id: '4', name: 'inactive-error', isActive: false, testStatus: 'error' }),
    pool({ id: '5', name: 'active-unknown', isActive: true, testStatus: 'unknown' })
  ]

  test('all returns every pool unchanged', () => {
    expect(filterProxyPools(pools, 'all')).toHaveLength(5)
  })

  test('active filters on isActive, not on test status', () => {
    expect(filterProxyPools(pools, 'active').map((p) => p.id)).toEqual(['1', '3', '5'])
  })

  test('passed filters on the last successful probe', () => {
    expect(filterProxyPools(pools, 'passed').map((p) => p.id)).toEqual(['1', '2'])
  })

  test('failed covers failed and error in one bucket', () => {
    expect(filterProxyPools(pools, 'failed').map((p) => p.id)).toEqual(['3', '4'])
  })
})

describe('sortProxyPools', () => {
  test('default leaves the existing order untouched', () => {
    const pools = [pool({ id: '1', name: 'b' }), pool({ id: '2', name: 'a' })]
    expect(sortProxyPools(pools, 'default').map((p) => p.id)).toEqual(['1', '2'])
  })

  test('fastest ranks measured pools by latency and pushes the rest to the end', () => {
    const pools = [
      pool({ id: '1', name: 'dead-low-latency', latency: 10, testStatus: 'failed' }),
      pool({ id: '2', name: 'error-low-latency', latency: 15, testStatus: 'error' }),
      pool({ id: '3', name: 'passed-high-latency', latency: 300, testStatus: 'passed' }),
      pool({ id: '4', name: 'active-med-latency', latency: 120, testStatus: 'active' }),
      pool({ id: '5', name: 'untested', testStatus: 'unknown' })
    ]
    expect(sortProxyPools(pools, 'fastest').map((p) => p.id)).toEqual(['4', '3', '1', '2', '5'])
  })

  test('fastest does not produce NaN ordering when every latency is unranked', () => {
    const pools = [
      pool({ id: '1', name: 'zeta', testStatus: 'failed' }),
      pool({ id: '2', name: 'alpha', testStatus: 'unknown' }),
      pool({ id: '3', name: 'mid', testStatus: 'error' })
    ]
    expect(sortProxyPools(pools, 'fastest').map((p) => p.name)).toEqual(['alpha', 'mid', 'zeta'])
  })

  test('fastest breaks equal latencies by name so the order is stable', () => {
    const pools = [
      pool({ id: '1', name: 'b', latency: 120, testStatus: 'passed' }),
      pool({ id: '2', name: 'a', latency: 120, testStatus: 'passed' })
    ]
    expect(sortProxyPools(pools, 'fastest').map((p) => p.name)).toEqual(['a', 'b'])
  })

  test('recently tested orders newest first and treats unparseable stamps as oldest', () => {
    const pools = [
      pool({ id: '1', name: 'old', lastTestedAt: '2026-01-01T00:00:00Z' }),
      pool({ id: '2', name: 'never', lastTestedAt: null }),
      pool({ id: '3', name: 'new', lastTestedAt: '2026-10-01T00:00:00Z' }),
      pool({ id: '4', name: 'garbage', lastTestedAt: 'not-a-date' })
    ]
    expect(sortProxyPools(pools, 'recently_tested').map((p) => p.id)).toEqual(['3', '1', '4', '2'])
  })

  test('name sorts alphabetically', () => {
    const pools = [pool({ id: '1', name: 'charlie' }), pool({ id: '2', name: 'alpha' })]
    expect(sortProxyPools(pools, 'name').map((p) => p.name)).toEqual(['alpha', 'charlie'])
  })

  test('never mutates the input array', () => {
    const pools = [pool({ id: '1', name: 'b' }), pool({ id: '2', name: 'a' })]
    const before = pools.map((p) => p.id)
    sortProxyPools(pools, 'name')
    expect(pools.map((p) => p.id)).toEqual(before)
  })
})

describe('poolStatusCounts', () => {
  test('counts each bucket independently of the active flag', () => {
    const pools = [
      pool({ id: '1', isActive: true, testStatus: 'passed' }),
      pool({ id: '2', isActive: false, testStatus: 'passed' }),
      pool({ id: '3', isActive: true, testStatus: 'failed' }),
      pool({ id: '4', isActive: false, testStatus: 'error' })
    ]
    expect(poolStatusCounts(pools)).toEqual({ active: 2, passed: 2, failed: 2 })
  })

  test('an empty list counts zero everywhere', () => {
    expect(poolStatusCounts([])).toEqual({ active: 0, passed: 0, failed: 0 })
  })
})

describe('updateProxyPoolSelection', () => {
  const pools = [
    pool({ id: '1' }),
    pool({ id: '2' }),
    pool({ id: '3' }),
    pool({ id: '4' })
  ]

  test('a plain click selects just the clicked row', () => {
    expect(
      updateProxyPoolSelection(pools, [], { id: '2', checked: true })
    ).toEqual(['2'])
  })

  test('a plain click deselects the clicked row without touching others', () => {
    expect(
      updateProxyPoolSelection(pools, ['2', '3'], { id: '2', checked: false })
    ).toEqual(['3'])
  })

  test('shift-click selects the whole range from the anchor', () => {
    expect(
      updateProxyPoolSelection(pools, ['1'], {
        id: '3',
        anchorId: '1',
        checked: true,
        shiftKey: true
      })
    ).toEqual(['1', '2', '3'])
  })

  test('shift-click ranges run in either direction', () => {
    expect(
      updateProxyPoolSelection(pools, [], {
        id: '1',
        anchorId: '3',
        checked: true,
        shiftKey: true
      })
    ).toEqual(['1', '2', '3'])
  })

  test('shift-click clears the range when the anchor row was checked off', () => {
    expect(
      updateProxyPoolSelection(pools, ['1', '2', '3'], {
        id: '3',
        anchorId: '1',
        checked: false,
        shiftKey: true
      })
    ).toEqual([])
  })

  test('rows hidden by the current filter never join the selection', () => {
    const visible = [pool({ id: '1' }), pool({ id: '3' })]
    expect(
      updateProxyPoolSelection(visible, ['4'], {
        id: '3',
        anchorId: '1',
        checked: true,
        shiftKey: true
      })
    ).toEqual(['1', '3'])
  })

  test('a shift-click with no anchor row acts on the clicked row alone', () => {
    expect(
      updateProxyPoolSelection(pools, ['1'], {
        id: '3',
        anchorId: 'gone',
        checked: true,
        shiftKey: true
      })
    ).toEqual(['1', '3'])
  })
})

describe('getVisibleProxyPoolSelection', () => {
  test('drops selected ids the filter has hidden', () => {
    expect(getVisibleProxyPoolSelection([pool({ id: '1' }), pool({ id: '3' })], ['1', '2', '3'])).toEqual(['1', '3'])
  })
})
