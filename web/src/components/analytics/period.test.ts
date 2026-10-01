import { describe, expect, it } from 'bun:test'
import { PERIODS, normalizeCustomPeriod, periodLabel } from './types'

describe('period label', () => {
  it('names every preset the selector offers', () => {
    for (const preset of PERIODS) {
      expect(periodLabel(preset.value)).toBe(preset.label)
    }
  })

  it('offers the unbounded window the issue asked for', () => {
    expect(PERIODS.some((p) => p.value === 'all')).toBe(true)
  })

  it('labels a custom window rather than showing the raw value', () => {
    expect(periodLabel('14d')).toBe('Last 14 days')
    expect(periodLabel('12h')).toBe('Last 12 hours')
  })

  it('never renders blank for a value it does not recognise', () => {
    expect(periodLabel('garbage')).toBe('garbage')
  })
})

describe('custom period input', () => {
  it('accepts days and hours', () => {
    expect(normalizeCustomPeriod('14d')).toBe('14d')
    expect(normalizeCustomPeriod('12h')).toBe('12h')
  })

  it('normalizes spacing and case', () => {
    expect(normalizeCustomPeriod('  30D ')).toBe('30d')
  })

  it('rejects input the server could not resolve to a window', () => {
    // Sending these would silently read as the 7-day fallback server-side,
    // showing the user a number that is not the window they asked for.
    for (const bad of ['', '   ', 'd', '14', '14x', '-3d', '0d', 'abc']) {
      expect(normalizeCustomPeriod(bad)).toBeNull()
    }
  })
})