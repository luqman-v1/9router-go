import { describe, expect, it } from 'bun:test'
import { TAB_ROUTES } from './router'
import {
  ALWAYS_VISIBLE_NAV_IDS,
  DEFAULT_HIDDEN_NAV_IDS,
  NAV_CONTAINERS,
  NAV_PRESETS,
  DEFAULT_HIDDEN_NAV_ENTRIES,
  NAV_GROUPS,
  NAV_ORDER,
  activeNavPreset,
  arrangedNavGroups,
  dropNavEntry,
  isNavHidden,
  isNavLinkActive,
  moveNavEntry,
  navPresetHidden,
  normalizeHiddenNav,
  normalizeNavOrder,
  orderedNavGroups,
  setNavVisible,
  type NavEntry,
} from './nav'

const links: Extract<NavEntry, { kind: 'link' }>[] = NAV_GROUPS.flatMap((group) =>
  group.entries.flatMap((entry) => (entry.kind === 'accordion' ? entry.children : [entry]))
)

const groupIds = (groups = NAV_GROUPS): string[] =>
  groups.flatMap((group) =>
    group.entries.flatMap((entry) =>
      entry.kind === 'accordion' ? [entry.id, ...entry.children.map((child) => child.id)] : [entry.id]
    )
  )

const renderedIds = (hidden: unknown, order: unknown = []): string[] =>
  groupIds(orderedNavGroups(hidden, order))

const MEDIA_CHILD_IDS = NAV_GROUPS.flatMap((group) =>
  group.entries.flatMap((entry) => (entry.kind === 'accordion' ? entry.children.map((c) => c.id) : []))
)

describe('nav model', () => {
  it('addresses a route the router knows for every entry', () => {
    // The consumer check: an id with no route is an entry the sidebar would
    // render but the app could not open.
    for (const link of links) {
      expect(TAB_ROUTES[link.id as keyof typeof TAB_ROUTES]).toBeTruthy()
    }
  })

  it('uses every id exactly once', () => {
    expect(new Set(NAV_ORDER).size).toBe(NAV_ORDER.length)
    expect(NAV_ORDER.length).toBeGreaterThan(0)
  })

  it('hides only the defaults, and offers every row to the arrangement', () => {
    expect(DEFAULT_HIDDEN_NAV_ENTRIES.map((entry) => entry.id)).toEqual(['plans', 'jailbreak'])
    for (const entry of DEFAULT_HIDDEN_NAV_ENTRIES) {
      expect(NAV_ORDER).toContain(entry.id)
      expect(ALWAYS_VISIBLE_NAV_IDS).not.toContain(entry.id)
    }
    // The arrangement reaches every row, the unhideable one included.
    const arranged = NAV_CONTAINERS.flatMap((container) => container.ids)
    expect(arranged.slice().sort()).toEqual(NAV_ORDER.slice().sort())
  })

  it('keeps the panel behind an entry it can never hide', () => {
    expect(ALWAYS_VISIBLE_NAV_IDS).toContain('settings')
    expect(renderedIds({ hidden: ['settings'] })).toContain('settings')
    expect(normalizeHiddenNav({ hidden: ['settings'] })).toEqual([])
    expect(setNavVisible([], 'settings', false)).toEqual([])
  })
})

describe('default visibility', () => {
  it('hides Plans and Jailbreak until a list is saved', () => {
    expect([...DEFAULT_HIDDEN_NAV_IDS]).toEqual(['plans', 'jailbreak'])
    for (const saved of [undefined, null, {}, 'plans', { hidden: 'plans' }]) {
      const ids = renderedIds(saved)
      expect(ids, JSON.stringify(saved)).not.toContain('plans')
      // Security was never a default and must stay visible out of the box.
      expect(ids, JSON.stringify(saved)).toContain('security')
      expect(ids, JSON.stringify(saved)).toContain('settings')
    }
  })

  it('treats an explicit empty list as "show everything"', () => {
    expect(normalizeHiddenNav({ hidden: [] })).toEqual([])
    expect(renderedIds({ hidden: [] })).toEqual(NAV_ORDER)
  })

  it('blocks the default-hidden tab until the operator restores it', () => {
    expect(isNavHidden('plans', undefined)).toBe(true)
    // Security is no longer a default: only a saved list hides it.
    expect(isNavHidden('security', undefined)).toBe(false)
    expect(isNavHidden('security', { hidden: [] })).toBe(false)
    expect(isNavHidden('plans', { hidden: ['plans'] })).toBe(true)
  })
})

describe('visibility filtering', () => {
  it('removes only the hidden link', () => {
    const ids = renderedIds({ hidden: ['quota'] })
    expect(ids).not.toContain('quota')
    expect(ids).toContain('endpoint')
    expect(ids).toContain('media-video')
  })

  it('removes an accordion with its children', () => {
    const ids = renderedIds({ hidden: ['media-providers'] })
    expect(ids).not.toContain('media-providers')
    for (const child of MEDIA_CHILD_IDS) {
      expect(ids).not.toContain(child)
    }
  })

  it('drops an accordion whose children are all hidden', () => {
    const ids = renderedIds({ hidden: MEDIA_CHILD_IDS })
    expect(ids).not.toContain('media-providers')
    expect(ids).toContain('proxy-pools')
  })

  it('keeps an accordion that still has one visible child', () => {
    const groups = orderedNavGroups(
      { hidden: MEDIA_CHILD_IDS.filter((id) => id !== 'media-video') },
      []
    )
    const accordion = groups
      .flatMap((group) => group.entries)
      .find((entry) => entry.kind === 'accordion')
    expect(accordion?.kind).toBe('accordion')
    expect(accordion?.kind === 'accordion' && accordion.children.map((c) => c.id)).toEqual(['media-video'])
  })

  it('ignores ids it cannot address and lower-cases the rest', () => {
    expect(normalizeHiddenNav({ hidden: ['QUOTA', 'not-an-entry', 7, 'quota'] })).toEqual(['quota'])
    expect(normalizeHiddenNav({ hidden: 'quota' })).toEqual([...DEFAULT_HIDDEN_NAV_IDS])
    expect(renderedIds({ hidden: ['not-an-entry'] })).toEqual(NAV_ORDER)
  })
})

describe('stored hide list', () => {
  it('stores ids in render order', () => {
    const first = setNavVisible({ hidden: [] }, 'security', false)
    expect(first).toEqual(['security'])
    expect(setNavVisible(first, 'quota', false)).toEqual(['quota', 'security'])
    expect(setNavVisible(['quota', 'security'], 'quota', true)).toEqual(['security'])
  })

  it('starts from the defaults when nothing was saved', () => {
    expect(setNavVisible(undefined, 'plans', true)).toEqual(['jailbreak'])
    expect(setNavVisible(undefined, 'quota', false)).toEqual(['quota', 'jailbreak', 'plans'])
  })

  it('survives a junk payload without losing a valid entry', () => {
    expect(setNavVisible({ hidden: ['quota', 'bogus'] }, 'media-tts', false)).toEqual([
      'quota',
      'media-tts',
    ])
  })
})

describe('route guard', () => {
  it('follows the aliases the sidebar keeps lit', () => {
    expect(isNavHidden('keys', { hidden: ['endpoint'] })).toBe(true)
    expect(isNavHidden('terminal', { hidden: ['console-log'] })).toBe(true)
    expect(isNavHidden('usage-cache', { hidden: ['analytics'] })).toBe(true)
    expect(isNavHidden('usage-compression', { hidden: ['analytics'] })).toBe(true)
  })

  it('treats a hidden accordion as hiding its children', () => {
    expect(isNavHidden('media-video', { hidden: ['media-providers'] })).toBe(true)
    expect(isNavHidden('media-video', { hidden: ['media-tts'] })).toBe(false)
  })

  it('never blocks the login page or an entry nobody hid', () => {
    expect(isNavHidden('login', { hidden: ['login'] })).toBe(false)
    expect(isNavHidden('quota', { hidden: [] })).toBe(false)
    expect(isNavHidden('endpoint', undefined)).toBe(false)
  })
})

describe('active highlighting', () => {
  it('keeps the aliased entries lit', () => {
    expect(isNavLinkActive('endpoint', 'keys')).toBe(true)
    expect(isNavLinkActive('console-log', 'terminal')).toBe(true)
    expect(isNavLinkActive('analytics', 'usage-cache')).toBe(true)
    expect(isNavLinkActive('analytics', 'usage-compression')).toBe(true)
    expect(isNavLinkActive('quota', 'analytics')).toBe(false)
  })
})

describe('sidebar arrangement', () => {
  it('keeps only ids that address a row, in stored order', () => {
    expect(normalizeNavOrder({ order: ['plans', 'plans', 'not-an-entry', 'QUOTA', 7] })).toEqual([
      'plans',
      'quota',
    ])
    // A bare array is what the helpers hand back in, so it reads too.
    expect(normalizeNavOrder(['quota'])).toEqual(['quota'])
    expect(normalizeNavOrder(undefined)).toEqual([])
  })

  it('gives every container one complete list, whatever was stored', () => {
    for (const container of NAV_CONTAINERS) {
      expect(new Set(container.ids).size).toBe(container.ids.length)
    }
    // Every row is drawn exactly once, in some order, however the stored
    // arrangement was written.
    const drawn = groupIds(arrangedNavGroups({ order: ['plans', 'endpoint'] }))
    expect(drawn.length).toBe(NAV_ORDER.length)
    for (const id of NAV_ORDER) expect(drawn).toContain(id)
  })

  it('draws the rows it was given, then the rest in declaration order', () => {
    const ids = groupIds(arrangedNavGroups({ order: ['analytics', 'endpoint'] }))
    expect(ids.slice(0, 2)).toEqual(['analytics', 'endpoint'])
    expect(ids).toContain('cli-tools')
  })

  it('moves a row one place inside its own container', () => {
    expect(moveNavEntry([], 'connections', -1).slice(0, 2)).toEqual(['connections', 'endpoint'])
    // Past an end: unchanged, which is what a disabled arrow relies on.
    expect(moveNavEntry([], 'endpoint', -1).slice(0, 2)).toEqual(['endpoint', 'connections'])
  })

  it('keeps an accordion whole and its children in their own container', () => {
    const moved = moveNavEntry([], 'proxy-pools', -1)
    const system = arrangedNavGroups(moved).find((group) => group.id === 'system')
    expect(system?.entries[0].id).toBe('proxy-pools')
    const accordion = system?.entries[1]
    expect(accordion?.kind).toBe('accordion')
    expect(accordion?.kind === 'accordion' && accordion.children.map((c) => c.id)).toEqual(
      MEDIA_CHILD_IDS
    )
    // A media child moves among its siblings, never out into the group.
    const afterMove = arrangedNavGroups(moveNavEntry([], 'media-tts', -1))
      .flatMap((group) => group.entries)
      .find((entry) => entry.kind === 'accordion')
    expect(afterMove?.kind === 'accordion' && afterMove.children.map((c) => c.id)).toEqual([
      'media-embedding',
      'media-tts',
      'media-image',
      'media-stt',
      'media-video',
      'media-systemone',
      'media-web',
    ])
  })

  it('refuses a drop into another container', () => {
    const flat = moveNavEntry([], 'connections', -1)
    expect(dropNavEntry(flat, 'media-tts', 'proxy-pools', true)).toEqual(flat)
  })

  it('keeps the arrangement to one of every row', () => {
    const moved = moveNavEntry(moveNavEntry([], 'security', -1), 'media-tts', 1)
    expect(moved.length).toBe(NAV_ORDER.length)
    expect(new Set(moved).size).toBe(moved.length)
  })

  it('hides a row without losing the place it was moved to', () => {
    const order = moveNavEntry([], 'security', -1)
    const visible = renderedIds({ hidden: [] }, order)
    const slot = visible.indexOf('security')
    // The row keeps the slot it was moved into: what follows it on screen is
    // what follows it in the stored arrangement.
    expect(visible[slot + 1]).toBe(order[order.indexOf('security') + 1])
    // Hidden, the row leaves and its neighbours keep their own slots instead
    // of closing the gap.
    const hidden = renderedIds({ hidden: ['security'] }, order)
    expect(hidden).not.toContain('security')
    expect(hidden[slot]).toBe(order[order.indexOf('security') + 1])
  })
})

describe('sidebar presets', () => {
  it('offers the four sets, each one a valid hide list', () => {
    expect(NAV_PRESETS.map((preset) => preset.id)).toEqual([
      'default',
      'advanced',
      'simple',
      'super-lite',
    ])
    for (const preset of NAV_PRESETS) {
      const hidden = navPresetHidden(preset.id)
      // No preset may hide the page the panel lives on.
      expect(hidden).not.toContain('settings')
      expect(new Set(hidden).size).toBe(hidden.length)
      for (const id of hidden) expect(NAV_ORDER).toContain(id)
    }
  })

  it('starts from the build default and grows to everything', () => {
    expect(navPresetHidden('default').slice().sort()).toEqual([...DEFAULT_HIDDEN_NAV_IDS].sort())
    expect(navPresetHidden('advanced')).toEqual([])
    // Each step reveals more than the one before it.
    expect(navPresetHidden('simple').length).toBeGreaterThan(navPresetHidden('default').length)
    expect(navPresetHidden('super-lite').length).toBeGreaterThan(navPresetHidden('simple').length)
    expect(renderedIds({ hidden: navPresetHidden('super-lite') })).toEqual([
      'endpoint',
      'connections',
      'combos',
      'analytics',
      'security',
      'settings',
    ])
  })

  it('reads back the preset a hide list equals, and custom otherwise', () => {
    for (const preset of NAV_PRESETS) {
      expect(activeNavPreset({ hidden: navPresetHidden(preset.id) })).toBe(preset.id)
    }
    expect(activeNavPreset(undefined)).toBe('default')
    expect(activeNavPreset({ hidden: ['quota'] })).toBe('custom')
    expect(activeNavPreset({ hidden: ['quota', 'skills'] })).toBe('custom')
  })
})
