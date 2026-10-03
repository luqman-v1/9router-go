import { describe, expect, test } from 'bun:test'
import { getStatusVariant, getLatencyBadge } from './helpers'

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
