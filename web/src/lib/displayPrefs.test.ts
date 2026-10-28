import { afterEach, beforeEach, describe, expect, it } from 'bun:test'
import { isDetailsExpandedByDefault, setDetailsExpandedByDefault } from './displayPrefs'

// Bun has no DOM storage; displayPrefs guards on `typeof localStorage`, so a
// stub is installed per test and the previous descriptor is restored after
// (same shape as compactNumber.test.ts).
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

describe('usage detail collapse preference', () => {
  it('starts collapsed on a browser that never touched the switch', () => {
    expect(isDetailsExpandedByDefault()).toBe(false)
  })

  it('round-trips the switch', () => {
    setDetailsExpandedByDefault(true)
    expect(isDetailsExpandedByDefault()).toBe(true)
    setDetailsExpandedByDefault(false)
    expect(isDetailsExpandedByDefault()).toBe(false)
  })

  it('treats a hand-edited value as collapsed', () => {
    localStorage.setItem('detailsExpandedByDefault', 'yes')
    expect(isDetailsExpandedByDefault()).toBe(false)
  })
})

// The Overview's two optional panels are hidden through the Usage arrangement
// (lib/usagePanel.svelte.ts) rather than a separate localStorage switch, so the
// coverage for that lives in usagePanelPlan.test.ts and nav.test.ts. Upstream's
// showComboFlow / showUsageTrend keys would be a second, competing control.
