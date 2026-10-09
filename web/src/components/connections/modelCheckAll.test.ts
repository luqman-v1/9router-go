import { describe, expect, test } from 'bun:test'
import {
  formatCheckAllSummary,
  getBlockedModelIds,
  parseModelTestVerdict,
} from './modelCheckAll'

describe('parseModelTestVerdict', () => {
  test('returns ok when res.ok is true', () => {
    const res = parseModelTestVerdict({ ok: true })
    expect(res.status).toBe('ok')
    expect(res.error).toBeNull()
  })

  test('returns blocked with resetAt when res.blocked is true', () => {
    const res = parseModelTestVerdict({
      ok: false,
      blocked: true,
      resetAt: '2026-10-09T02:00:00Z',
    })
    expect(res.status).toBe('blocked')
    expect(res.error).toBe('Blocked (cooldown until 2026-10-09T02:00:00Z)')
  })

  test('returns blocked and extracts resetAt from 502 all in cooldown message', () => {
    const res = parseModelTestVerdict({
      ok: false,
      status: 502,
      error: 'HTTP 502: upstream error: no available connections for provider: deepseek (all in cooldown, earliest reset 2026-10-09T03:00:00Z)',
    })
    expect(res.status).toBe('blocked')
    expect(res.error).toBe('Blocked (cooldown until 2026-10-09T03:00:00Z)')
  })

  test('returns blocked without resetAt when res.blocked is true but resetAt absent', () => {
    const res = parseModelTestVerdict({
      ok: false,
      blocked: true,
      error: 'all in cooldown',
    })
    expect(res.status).toBe('blocked')
    expect(res.error).toBe('all in cooldown')
  })

  test('returns error for standard failure', () => {
    const res = parseModelTestVerdict({
      ok: false,
      status: 404,
      error: 'HTTP 404 Model not found',
    })
    expect(res.status).toBe('error')
    expect(res.error).toBe('HTTP 404 Model not found')
  })
})

describe('formatCheckAllSummary', () => {
  test('returns null message when all models passed', () => {
    const summary = formatCheckAllSummary({
      m1: 'ok',
      m2: 'ok',
    })
    expect(summary.passed).toBe(2)
    expect(summary.failed).toBe(0)
    expect(summary.blocked).toBe(0)
    expect(summary.severity).toBe('ok')
    expect(summary.message).toBeNull()
  })

  test('formats failed models correctly with error severity', () => {
    const summary = formatCheckAllSummary({
      m1: 'ok',
      m2: 'error',
    })
    expect(summary.passed).toBe(1)
    expect(summary.failed).toBe(1)
    expect(summary.blocked).toBe(0)
    expect(summary.severity).toBe('error')
    expect(summary.message).toBe('1 passed · 1 failed — see per-row status.')
  })

  test('formats blocked models correctly with blocked severity and no failed mention', () => {
    const summary = formatCheckAllSummary({
      m1: 'ok',
      m2: 'blocked',
      m3: 'blocked',
    })
    expect(summary.passed).toBe(1)
    expect(summary.failed).toBe(0)
    expect(summary.blocked).toBe(2)
    expect(summary.severity).toBe('blocked')
    expect(summary.message).toBe('1 passed · 2 blocked — see per-row status.')
    expect(summary.message?.includes('failed')).toBe(false)
  })

  test('formats tri-state with both failed and blocked models', () => {
    const summary = formatCheckAllSummary({
      m1: 'ok',
      m2: 'error',
      m3: 'blocked',
    })
    expect(summary.passed).toBe(1)
    expect(summary.failed).toBe(1)
    expect(summary.blocked).toBe(1)
    expect(summary.severity).toBe('error')
    expect(summary.message).toBe('1 passed · 1 failed · 1 blocked — see per-row status.')
  })
})

describe('getBlockedModelIds', () => {
  test('extracts blocked model ids', () => {
    const ids = getBlockedModelIds({
      m1: 'ok',
      m2: 'blocked',
      m3: 'error',
      m4: 'blocked',
    })
    expect(ids).toEqual(['m2', 'm4'])
  })
})
