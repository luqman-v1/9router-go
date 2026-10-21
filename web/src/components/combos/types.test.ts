import { describe, expect, it } from 'bun:test'
import { COMBO_STRATEGIES, resolveComboStrategy } from './types'

const OPTION_VALUES = COMBO_STRATEGIES.map((s) => s.value)

describe('resolveComboStrategy', () => {
  // The server normalizes combo strategies onto the three canonical values
  // (db.NormalizeComboStrategy), but a card can still meet a legacy value in
  // a hand-edited settings blob — everything has to land on a real option.
  it('maps every value the server can send onto an existing option', () => {
    const serverValues = [
      undefined,
      null,
      '',
      'fallback',
      'round-robin',
      'first-model',
      'sticky',
      'capacity',
      'fusion',
      'ensemble',
      'nonsense-from-a-hand-edited-backup'
    ]

    for (const value of serverValues) {
      expect(OPTION_VALUES).toContain(resolveComboStrategy(value))
    }
  })

  it('treats the global routing mode as the card fallback', () => {
    // `first-model` is the legacy name of the global Combo Routing Mode's
    // try-in-order behaviour, which is exactly what the card calls
    // `fallback`.
    expect(resolveComboStrategy('first-model')).toBe('fallback')
  })

  it('passes a canonical per-combo strategy through unchanged', () => {
    for (const value of ['round-robin', 'fallback', 'fusion']) {
      expect(resolveComboStrategy(value)).toBe(value)
    }
  })

  it('folds legacy strategies into the closest canonical one', () => {
    expect(resolveComboStrategy('sticky')).toBe('round-robin')
    expect(resolveComboStrategy('failover')).toBe('fallback')
    expect(resolveComboStrategy('capacity')).toBe('fallback')
    expect(resolveComboStrategy('ensemble')).toBe('fusion')
  })

  it('resolves a missing or unrecognised value to round-robin, the default', () => {
    expect(resolveComboStrategy(undefined)).toBe('round-robin')
    expect(resolveComboStrategy(null)).toBe('round-robin')
    expect(resolveComboStrategy('')).toBe('round-robin')
    expect(resolveComboStrategy('nonsense')).toBe('round-robin')
  })

  it('lists every option with a non-empty label', () => {
    for (const strategy of COMBO_STRATEGIES) {
      expect(strategy.value.length).toBeGreaterThan(0)
      expect(strategy.label.length).toBeGreaterThan(0)
    }
  })
})
