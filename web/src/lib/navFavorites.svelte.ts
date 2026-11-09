// The pinned sidebar entries, mirrored for the components outside the sidebar
// itself — the command palette offers "pin this" next to a nav result, and the
// sidebar draws the section. The list is a per-operator preference, so it lives
// in localStorage rather than in the server-side navVisibility setting the hide
// list and the arrangement ride.
//
// The rules that keep the list honest live in lib/navFavorites.ts, which is
// pure and unit-tested; this module is the reactive half.
import {
  MAX_PINNED_NAV_ENTRIES,
  normalizePinnedNav,
  togglePinnedNav,
} from './navFavorites'

let pinned = $state<string[]>([])

const PINNED_KEY = '9router-nav-pinned'

function read(): unknown {
  if (typeof localStorage === 'undefined') return []
  try {
    return JSON.parse(localStorage.getItem(PINNED_KEY) ?? '[]')
  } catch {
    return []
  }
}

function write(ids: string[]): void {
  pinned = ids
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(PINNED_KEY, JSON.stringify(ids))
  } catch {
    // A private-mode browser that refuses the write still gets the pin for
    // this session; it just does not survive the reload.
  }
}

/** Reads the stored list once, at boot, against this build's ids. */
export function loadPinnedNavEntries(known: readonly string[]): void {
  write(normalizePinnedNav(read(), known))
}

/** Replaces the list, dropping ids this build no longer draws. */
export function setPinnedNavEntries(ids: readonly string[], known: readonly string[]): void {
  write(normalizePinnedNav(ids, known))
}

export function isNavEntryPinned(id: string): boolean {
  return pinned.includes(id)
}

/** The pinned ids in the order they were pinned. */
export function pinnedNavEntries(): string[] {
  return pinned
}

/** Adds or removes one pin; returns whether the entry is pinned afterwards. */
export function togglePinnedNavEntry(id: string, known: readonly string[]): boolean {
  const next = togglePinnedNav(pinned, id, known)
  write(next.ids)
  return next.pinned
}

export { MAX_PINNED_NAV_ENTRIES }
