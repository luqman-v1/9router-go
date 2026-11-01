import { describe, expect, it } from 'bun:test'
import { NAV_ORDER } from './nav'
import {
  MAX_PINNED_NAV_ENTRIES,
  normalizePinnedNav,
  togglePinnedNav,
} from './navFavorites'

describe('pinned nav entries', () => {
  it('drops an id this build no longer draws', () => {
    expect(normalizePinnedNav(['quota', 'not-an-entry', 'analytics'], NAV_ORDER)).toEqual([
      'quota',
      'analytics',
    ])
  })

  it('keeps the order the ids arrived in and refuses a duplicate', () => {
    expect(normalizePinnedNav(['quota', 'quota', 'analytics'], NAV_ORDER)).toEqual([
      'quota',
      'analytics',
    ])
  })

  it('caps the list so the section cannot grow without bound', () => {
    const many = NAV_ORDER.slice(0, 20)
    const normalized = normalizePinnedNav(many, NAV_ORDER)
    expect(normalized.length).toBe(MAX_PINNED_NAV_ENTRIES)
    expect(normalized).toEqual(NAV_ORDER.slice(0, MAX_PINNED_NAV_ENTRIES))
  })

  it('reads a non-array as nothing pinned', () => {
    expect(normalizePinnedNav('quota', NAV_ORDER)).toEqual([])
    expect(normalizePinnedNav(null, NAV_ORDER)).toEqual([])
    expect(normalizePinnedNav(undefined, NAV_ORDER)).toEqual([])
  })

  it('pins an entry that is not pinned yet', () => {
    const next = togglePinnedNav([], 'quota', NAV_ORDER)
    expect(next.pinned).toBe(true)
    expect(next.ids).toEqual(['quota'])
  })

  it('unpins an entry that is already pinned', () => {
    const next = togglePinnedNav(['quota', 'analytics'], 'quota', NAV_ORDER)
    expect(next.pinned).toBe(false)
    expect(next.ids).toEqual(['analytics'])
  })

  it('refuses a new pin at the cap rather than dropping the oldest', () => {
    const full = NAV_ORDER.slice(0, MAX_PINNED_NAV_ENTRIES)
    const next = togglePinnedNav(full, 'skills', NAV_ORDER)
    expect(next.pinned).toBe(false)
    // Nothing was lost: the operator sees the star stay unlit and unpins first.
    expect(next.ids).toEqual(full)
  })

  it('normalizes whatever it was handed before toggling', () => {
    const next = togglePinnedNav(['quota', 'not-an-entry'], 'analytics', NAV_ORDER)
    expect(next.ids).toEqual(['quota', 'analytics'])
  })
})
