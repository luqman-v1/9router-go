// The two sidebar layout preferences, mirrored for the components that read
// them outside the Settings panel: the sidebar draws the Pinned section and the
// icon rail, and the Settings switch writes both.
//
// A store rather than a poll: the switch and the sidebar are siblings in the
// same tab, so a `storage` event never fires for the write — that one only
// reaches another tab. Polling localStorage would work and is the kind of
// thing that quietly becomes permanent, so the write goes through `set` here
// and every reader re-renders on the same tick.
//
// Both are localStorage preferences, not server settings: they decide how this
// browser draws the sidebar, and an operator who sets them on one machine has
// not asked for them on another.
let favoritesEnabled = $state(true)
let iconRail = $state(false)

const FAVORITES_KEY = 'navFavoritesEnabled'
const ICON_RAIL_KEY = 'navIconRail'

function readBool(key: string, fallback: boolean): boolean {
  if (typeof localStorage === 'undefined') return fallback
  const stored = localStorage.getItem(key)
  // An absent key is the default, not "off": an untouched install gets the
  // documented default rather than whatever a cleared storage happens to read.
  return stored === null ? fallback : stored === '1'
}

function writeBool(key: string, on: boolean): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(key, on ? '1' : '0')
  } catch {
    // A private-mode browser that refuses the write still gets the preference
    // for this session; it just does not survive the reload.
  }
}

/** Reads both keys once, at boot. */
export function loadSidebarPrefs(): void {
  favoritesEnabled = readBool(FAVORITES_KEY, true)
  iconRail = readBool(ICON_RAIL_KEY, false)
}

export function isFavoritesEnabled(): boolean {
  return favoritesEnabled
}

export function setFavoritesEnabled(on: boolean): void {
  favoritesEnabled = on
  writeBool(FAVORITES_KEY, on)
}

export function isIconRailEnabled(): boolean {
  return iconRail
}

export function setIconRailEnabled(on: boolean): void {
  iconRail = on
  writeBool(ICON_RAIL_KEY, on)
}
