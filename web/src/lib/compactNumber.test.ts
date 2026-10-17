import { afterEach, beforeEach, describe, expect, it } from 'bun:test'
import { fmtTokens, isCompactUnitsEnabled, setCompactUnits } from './compactNumber'

// Bun has no DOM storage; compactNumber guards on `typeof localStorage`, so a
// stub is installed per test and the previous descriptor is restored after
// (other suites such as api/client.test.ts keep their own stub on globalThis).
let entries: Record<string, string> = {}
const storageStub = {
  getItem: (key: string) => entries[key] ?? null,
  setItem: (key: string, value: string) => {
    entries[key] = value
  },
  removeItem: (key: string) => {
    delete entries[key]
  },
  clear: () => {
    entries = {}
  },
}
const previousDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')

beforeEach(() => {
  entries = {}
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    writable: true,
    value: storageStub,
  })
})

afterEach(() => {
  if (previousDescriptor) {
    Object.defineProperty(globalThis, 'localStorage', previousDescriptor)
  } else {
    Reflect.deleteProperty(globalThis, 'localStorage')
  }
})

describe('compactUnits setting', () => {
  it('defaults to enabled when no stored value exists', () => {
    expect(isCompactUnitsEnabled()).toBe(true)
  })

  it('persists off via setCompactUnits and back on again', () => {
    setCompactUnits(false)
    expect(isCompactUnitsEnabled()).toBe(false)
    setCompactUnits(true)
    expect(isCompactUnitsEnabled()).toBe(true)
  })
})

describe('fmtTokens', () => {
  it('abbreviates thousands, millions and billions when enabled', () => {
    setCompactUnits(true)
    expect(fmtTokens(1500)).toBe('1.5K')
    expect(fmtTokens(2_400_000)).toBe('2.4M')
    expect(fmtTokens(3_100_000_000)).toBe('3.1B')
  })

  it('leaves sub-thousand values unabbreviated', () => {
    setCompactUnits(true)
    expect(fmtTokens(999)).toBe('999')
  })

  it('shows full thousand-separated numbers when disabled', () => {
    setCompactUnits(false)
    expect(fmtTokens(2_400_000)).toBe((2_400_000).toLocaleString())
  })

  it('force abbreviates even when the switch is off', () => {
    setCompactUnits(false)
    expect(fmtTokens(2_400_000, 'en', true)).toBe('2.4M')
  })

  it('uses 亿/万 for zh locale when enabled', () => {
    setCompactUnits(true)
    expect(fmtTokens(150_000_000, 'zh')).toBe('1.5亿')
    expect(fmtTokens(25_000, 'zh')).toBe('2.5万')
  })
})
