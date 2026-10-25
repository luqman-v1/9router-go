import { describe, expect, it } from 'bun:test'
import { normalizeComboStepId, slugify } from './steps'

describe('slugify & normalizeComboStepId', () => {
  it('drops leading and trailing separators in linear time', () => {
    expect(normalizeComboStepId('openai/gpt-4o', { comboName: '--My Combo!!--' })).toBe(
      'my-combo-model-1-openai-gpt-4o',
    )
    expect(normalizeComboStepId(' ..Claude Sonnet.. ')).toBe('combo-model-1-claude-sonnet')
    expect(normalizeComboStepId('a--b')).toBe('combo-model-1-a-b')
  })

  it('falls back to "step" when input has no letters or digits', () => {
    expect(slugify('!!!')).toBe('step')
    expect(slugify('---')).toBe('step')
    expect(normalizeComboStepId('!!!')).toBe('combo-model-1-step')
  })

  it('handles long separator runs in linear time with zero ReDoS stalling', () => {
    const run = '-'.repeat(200_000)
    const started = performance.now()
    const id = normalizeComboStepId(`${run}a${run}b${run}`)
    const elapsedMs = performance.now() - started

    expect(id).toBe('combo-model-1-a-b')
    expect(elapsedMs).toBeLessThan(100)
  })
})
