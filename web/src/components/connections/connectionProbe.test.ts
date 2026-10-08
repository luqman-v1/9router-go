import { describe, expect, test } from 'bun:test'
import {
  badgeFor,
  canProbeRow,
  probeOutcomeFrom,
  probeOutcomeFromError,
  reconcileProbeStatuses,
  testingStatus,
  UNSUPPORTED_MESSAGE,
  type ProbeStatus
} from './connectionProbe'

describe('probeOutcomeFrom', () => {
  test('a valid probe is a success with no error', () => {
    expect(probeOutcomeFrom({ valid: true })).toEqual({ state: 'success', error: null })
  })

  test('an invalid probe keeps the provider message', () => {
    expect(probeOutcomeFrom({ valid: false, error: '401 unauthorized' })).toEqual({
      state: 'failed',
      error: '401 unauthorized'
    })
  })

  test('an invalid probe without a message falls back to a default', () => {
    expect(probeOutcomeFrom({ valid: false })).toEqual({
      state: 'failed',
      error: 'Test failed'
    })
  })

  // A 200 with an empty body used to fall through the res?.valid check and
  // land here; a null response must not read as a pass.
  test('a null response is a failure, not a silent success', () => {
    expect(probeOutcomeFrom(null)).toEqual({ state: 'failed', error: 'Test failed' })
    expect(probeOutcomeFrom(undefined)).toEqual({ state: 'failed', error: 'Test failed' })
  })
})

describe('probeOutcomeFromError', () => {
  test('keeps an Error message verbatim', () => {
    expect(probeOutcomeFromError(new Error('fetch failed'))).toEqual({
      state: 'failed',
      error: 'fetch failed'
    })
  })

  test('a non-Error rejection still produces a readable state', () => {
    expect(probeOutcomeFromError('boom')).toEqual({ state: 'failed', error: 'Test failed' })
    expect(probeOutcomeFromError(undefined)).toEqual({ state: 'failed', error: 'Test failed' })
  })
})

describe('canProbeRow', () => {
  test('allows a row with no probe in flight', () => {
    expect(canProbeRow(false, undefined)).toBe(true)
    expect(canProbeRow(false, { state: 'failed', error: 'x' })).toBe(true)
  })

  // PR #196's guard. The button's disabled attribute is presentation only, so
  // the sweep check must hold on its own or a queued click would fire a second
  // probe at the same account mid-sweep.
  test('refuses while a one-by-one sweep is running', () => {
    expect(canProbeRow(true, undefined)).toBe(false)
    expect(canProbeRow(true, { state: 'queued', error: null })).toBe(false)
  })

  test('refuses a second probe of the same row already in flight', () => {
    expect(canProbeRow(false, testingStatus())).toBe(false)
  })

  // A queued row belongs to a sweep that is still running, so isSweeping is
  // true in practice — but if that flag were ever wrong, the stale-queued entry
  // must not let a duplicate probe through either.
  test('a queued entry does not block, because the sweep flag owns that case', () => {
    expect(canProbeRow(false, { state: 'queued', error: null })).toBe(true)
  })
})

describe('badgeFor', () => {
  test('in-flight and queued states win over the persisted verdict', () => {
    expect(badgeFor(testingStatus(), 'error', '')).toBe('testing')
    expect(badgeFor({ state: 'queued', error: null }, 'error', '')).toBe('queued')
  })

  test('a failed probe wins over a healthy persisted status', () => {
    expect(badgeFor({ state: 'failed', error: 'boom' }, 'active', '')).toBe('error')
  })

  // A pass does NOT paint green over a persisted error. During the window
  // between a probe resolving and onRefresh() landing, conn.testStatus still
  // holds the previous run's verdict, and painting the row healthy then would
  // advertise an account the router has not been told is fixed. The sweep had
  // the same precedence before this helper existed, so it is preserved.
  test('a passed probe still defers to a persisted error', () => {
    expect(badgeFor({ state: 'success', error: null }, 'error', 'old failure')).toBe('error')
    expect(badgeFor({ state: 'success', error: null }, 'active', '')).toBe('active')
  })

  test('falls through to the persisted verdict with no probe entry', () => {
    expect(badgeFor(undefined, 'error', '401')).toBe('error')
    expect(badgeFor(undefined, 'failed', '401')).toBe('error')
    expect(badgeFor(undefined, 'active', '')).toBe('active')
    expect(badgeFor(undefined, undefined, '')).toBe('active')
  })

  // Providers with no probe registered answer with this string. Painting it red
  // would report a credential that works perfectly as broken.
  test('an unsupported probe never renders as an error', () => {
    expect(badgeFor({ state: 'failed', error: UNSUPPORTED_MESSAGE }, 'active', '')).toBe('active')
    expect(badgeFor(undefined, 'error', UNSUPPORTED_MESSAGE)).toBe('active')
  })
})

describe('reconcileProbeStatuses', () => {
  const conns = [
    { id: 'a', testStatus: 'active' },
    { id: 'b', testStatus: 'error' },
    { id: 'c', testStatus: 'active' }
  ]

  test('keeps in-flight entries — the server has not ruled on them yet', () => {
    const probes: Record<string, ProbeStatus> = {
      a: testingStatus(),
      c: { state: 'queued', error: null }
    }
    expect(reconcileProbeStatuses(probes, conns)).toEqual(probes)
  })

  test('keeps entries that agree with the server', () => {
    const probes: Record<string, ProbeStatus> = {
      a: { state: 'success', error: null },
      b: { state: 'failed', error: '401 unauthorized' }
    }
    expect(reconcileProbeStatuses(probes, conns)).toEqual(probes)
  })

  // The failure this guards: the Edit modal can mark a row repaired in the
  // database without probing it, and the stale entry would keep it red forever.
  test('drops a failed entry once the server reports the row healthy', () => {
    const probes: Record<string, ProbeStatus> = { c: { state: 'failed', error: 'boom' } }
    expect(reconcileProbeStatuses(probes, conns)).toEqual({})
  })

  test('drops a passed entry once the server reports the row broken', () => {
    const probes: Record<string, ProbeStatus> = { b: { state: 'success', error: null } }
    expect(reconcileProbeStatuses(probes, conns)).toEqual({})
  })

  test('drops entries for rows the server no longer lists', () => {
    const probes: Record<string, ProbeStatus> = {
      gone: { state: 'failed', error: 'boom' },
      a: testingStatus()
    }
    expect(reconcileProbeStatuses(probes, conns)).toEqual({ a: testingStatus() })
  })

  test('an empty map stays empty', () => {
    expect(reconcileProbeStatuses({}, conns)).toEqual({})
  })
})