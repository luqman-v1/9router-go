import { describe, expect, it } from 'bun:test'
import { getRemainingPercentage } from './types'

// "Unknown" has to stay distinguishable from "exhausted". A row that reports no
// remaining, no percentage and no finite used/total pair used to fall through to
// calculatePercentage, which answered 0 — the quota table then painted it red,
// the exact picture a drained account gets.
describe('getRemainingPercentage', () => {
  it('inverts a reported used share (percentageIsUsed)', () => {
    expect(getRemainingPercentage({ percentage: 30, percentageIsUsed: true })).toBe(70)
  })

  it('uses the reported percentage as-is when it is already the remaining share', () => {
    expect(getRemainingPercentage({ percentage: 45 })).toBe(45)
  })

  it('clamps an inverted percentage at zero', () => {
    expect(getRemainingPercentage({ percentage: 120, percentageIsUsed: true })).toBe(0)
  })

  it('returns remaining when the row reports it', () => {
    expect(getRemainingPercentage({ remaining: 42.4 })).toBe(42)
  })

  it('clamps a negative remaining to zero rather than going below it', () => {
    expect(getRemainingPercentage({ remaining: -3 })).toBe(0)
  })

  it('returns remainingPercentage when there is no remaining', () => {
    expect(getRemainingPercentage({ remainingPercentage: 61.6 })).toBe(62)
  })

  it('derives the percentage from a finite used/total pair', () => {
    expect(getRemainingPercentage({ used: 25, total: 100 })).toBe(75)
  })

  it('returns null when the row carries no measurement at all', () => {
    expect(getRemainingPercentage({})).toBeNull()
  })

  it('returns null when used is present but total is not', () => {
    expect(getRemainingPercentage({ used: 10 })).toBeNull()
  })
})
