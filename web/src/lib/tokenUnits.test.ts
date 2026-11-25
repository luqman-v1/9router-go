import { describe, expect, it } from 'bun:test'
import { formatTokenAmount, parseTokenAmount, fmtCompactTokens } from './tokenUnits'

describe('parseTokenAmount', () => {
  it('reads a plain count', () => {
    expect(parseTokenAmount('1000000')).toBe(1_000_000)
    expect(parseTokenAmount('0')).toBe(0)
    expect(parseTokenAmount('  42 ')).toBe(42)
  })

  it('reads the K/M/B/T scales as decimal units', () => {
    expect(parseTokenAmount('1M')).toBe(1_000_000)
    expect(parseTokenAmount('2m')).toBe(2_000_000)
    expect(parseTokenAmount('500K')).toBe(500_000)
    expect(parseTokenAmount('1B')).toBe(1_000_000_000)
    expect(parseTokenAmount('1T')).toBe(1_000_000_000_000)
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
    expect(formatTokenAmount(1_000_000_000_000)).toBe('1T')
  })

  it('writes a small count out in full', () => {
    expect(formatTokenAmount(500)).toBe('500')
  })

  it('writes nothing for an unset budget', () => {
    expect(formatTokenAmount(0)).toBe('')
  })
})

describe('fmtCompactTokens', () => {
  it('formats zero, nullish, and small counts cleanly', () => {
    expect(fmtCompactTokens(0)).toBe('0')
    expect(fmtCompactTokens(null as unknown as number)).toBe('0')
    expect(fmtCompactTokens(undefined as unknown as number)).toBe('0')
    expect(fmtCompactTokens(842)).toBe('842')
  })

  it('formats thousands and millions with one decimal place', () => {
    expect(fmtCompactTokens(1000)).toBe('1.0K')
    expect(fmtCompactTokens(15_400)).toBe('15.4K')
    expect(fmtCompactTokens(550_000_000)).toBe('550.0M')
  })

  it('formats billions and trillions instead of 4+ digit M strings that clip on YAxis', () => {
    expect(fmtCompactTokens(1_000_000_000)).toBe('1.0B')
    expect(fmtCompactTokens(1_100_000_000)).toBe('1.1B')
    expect(fmtCompactTokens(1_650_000_000)).toBe('1.6B')
    expect(fmtCompactTokens(2_200_000_000)).toBe('2.2B')
    expect(fmtCompactTokens(1_250_000_000_000)).toBe('1.3T')
  })

  it('promotes boundary values that round to 1000.0 into the next unit', () => {
    expect(fmtCompactTokens(999_950)).toBe('1.0M')
    expect(fmtCompactTokens(999_950_000)).toBe('1.0B')
    expect(fmtCompactTokens(999_950_000_000)).toBe('1.0T')
  })
})
