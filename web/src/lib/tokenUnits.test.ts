import { describe, expect, it } from 'bun:test'
import { formatTokenAmount, parseTokenAmount } from './tokenUnits'

describe('parseTokenAmount', () => {
  it('reads a plain count', () => {
    expect(parseTokenAmount('1000000')).toBe(1_000_000)
    expect(parseTokenAmount('0')).toBe(0)
    expect(parseTokenAmount('  42 ')).toBe(42)
  })

  it('reads the K/M/B scales as decimal units', () => {
    expect(parseTokenAmount('1M')).toBe(1_000_000)
    expect(parseTokenAmount('2m')).toBe(2_000_000)
    expect(parseTokenAmount('500K')).toBe(500_000)
    expect(parseTokenAmount('1B')).toBe(1_000_000_000)
  })

  it('reads a fractional scale', () => {
    expect(parseTokenAmount('1.5M')).toBe(1_500_000)
  })

  it('tolerates spacing and thousands separators', () => {
    expect(parseTokenAmount('1 M')).toBe(1_000_000)
    expect(parseTokenAmount('1,000,000')).toBe(1_000_000)
  })

  it('refuses anything it cannot read exactly', () => {
    for (const input of ['abc', '1X', '1e9', '-5', '1.2.3', '1Mb', '']) {
      expect(parseTokenAmount(input)).toBeNull()
    }
  })
})

describe('formatTokenAmount', () => {
  it('writes a count back as the scale it was typed in', () => {
    expect(formatTokenAmount(1_000_000)).toBe('1M')
    expect(formatTokenAmount(2_500_000)).toBe('2.5M')
    expect(formatTokenAmount(500_000)).toBe('500K')
    expect(formatTokenAmount(1_000_000_000)).toBe('1B')
  })

  it('writes a small count out in full', () => {
    expect(formatTokenAmount(500)).toBe('500')
  })

  it('writes nothing for an unset budget', () => {
    expect(formatTokenAmount(0)).toBe('')
  })
})
