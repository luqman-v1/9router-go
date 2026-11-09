import type { ActiveTab } from './router'
import { moveBeforeInList, moveInList, normalizeListAgainst } from './orderList'

/**
 * Single source of truth for the dashboard sidebar.
 *
 * The sidebar renders NAV_GROUPS and the Feature Visibility panel in Settings
 * writes the hide list for exactly the same entries. A hand-written nav entry in
 * one place and a toggle list in the other is the phantom-config-path failure
 * mode: the panel saves an id that no sidebar entry ever consumes, the save
 * reports success, and nothing changes.
 */

export interface NavLink {
  kind: 'link'
  id: ActiveTab
  label: string
  icon: string
}

export interface NavAccordion {
  kind: 'accordion'
  /** The id the hide list stores for the whole group. */
  id: string
  label: string
  icon: string
  children: NavLink[]
}

export type NavEntry = NavLink | NavAccordion

export interface NavGroup {
  id: string
  /** Absent for the top group, which renders without a heading. */
  label?: string
  entries: NavEntry[]
}

/** Sidebar entries the panel must never hide: the panel lives behind Settings. */
export const ALWAYS_VISIBLE_NAV_IDS: readonly string[] = ['settings']

/** The Plans entry, shared with NAV_GROUPS and the Settings toggle. */
const PLANS_LINK: NavLink = { kind: 'link', id: 'plans', label: 'Plans', icon: 'workspace_premium' }
/**
 * The entries a deployment hides out of the box. Everything else the sidebar
 * draws is visible until an operator turns it off; these are the exceptions, and
 * the Settings panel offers every entry — hideable or not — for arrangement.
 *
 * Jailbreak is here on purpose: it rewrites outbound request bodies, so it is
 * an advanced surface. The `advanced` preset (NAV_PRESETS below) reveals it
 * alongside Plans, and an operator can also reveal it by hand in Settings.
 */
export const DEFAULT_HIDDEN_NAV_ENTRIES: NavLink[] = [
  PLANS_LINK,
  { kind: 'link', id: 'jailbreak', label: 'Persona', icon: 'masks' },
]

/**
 * What a deployment that never saved a hide list hides. Only an absent or
 * unreadable stored list falls back here — an explicit empty list means the
 * operator restored every entry.
 */
export const DEFAULT_HIDDEN_NAV_IDS: readonly string[] = DEFAULT_HIDDEN_NAV_ENTRIES.map(
  (entry) => entry.id
)

/** Mirrors the server's caps in internal/handlers/dashboard/navvisibility.go. */
const MAX_HIDDEN_NAV_ENTRIES = 32
const MAX_NAV_ORDER_ENTRIES = 64
const NAV_ID_PATTERN = /^[a-z0-9-]{1,32}$/

export const NAV_GROUPS: NavGroup[] = [
  {
    id: 'main',
    entries: [
      { kind: 'link', id: 'endpoint', label: 'Endpoint & Key', icon: 'api' },
      { kind: 'link', id: 'connections', label: 'Providers', icon: 'dns' },
      { kind: 'link', id: 'combos', label: 'Combo & Vision Adapter', icon: 'layers' },
      { kind: 'link', id: 'analytics', label: 'Usage', icon: 'bar_chart' },
      { kind: 'link', id: 'quota', label: 'Quota Tracker', icon: 'data_usage' },
      { kind: 'link', id: 'token-saver', label: 'Token Saver', icon: 'savings' },
      { kind: 'link', id: 'cli-tools', label: 'CLI Tools', icon: 'terminal' },
    ],
  },
  {
    id: 'advanced-features',
    label: 'Advanced Features',
    entries: [
      { kind: 'link', id: 'security', label: 'Security', icon: 'shield_lock' },
      { kind: 'link', id: 'jailbreak', label: 'Persona', icon: 'masks' },
      PLANS_LINK,
    ],
  },
  {
    id: 'system',
    label: 'System',
    entries: [
      {
        kind: 'accordion',
        id: 'media-providers',
        label: 'Media Providers',
        icon: 'perm_media',
        children: [
          { kind: 'link', id: 'media-embedding', label: 'Embedding', icon: 'data_array' },
          { kind: 'link', id: 'media-image', label: 'Text to Image', icon: 'brush' },
          { kind: 'link', id: 'media-tts', label: 'Text To Speech', icon: 'record_voice_over' },
          { kind: 'link', id: 'media-stt', label: 'Speech To Text', icon: 'mic' },
          { kind: 'link', id: 'media-video', label: 'Video', icon: 'movie' },
          { kind: 'link', id: 'media-systemone', label: 'System One', icon: 'psychology' },
          { kind: 'link', id: 'media-web', label: 'Web Fetch & Search', icon: 'travel_explore' },
        ],
      },
      { kind: 'link', id: 'proxy-pools', label: 'Proxy Pools', icon: 'lan' },
      { kind: 'link', id: 'skills', label: 'Skills', icon: 'extension' },
      // Console Log and Settings sit last on purpose: they are the two entries
      // an operator reaches for after everything else, and Settings is the one
      // entry that must never disappear — so it is the final row, out of the
      // way of the delete/arrange gestures above it.
      { kind: 'link', id: 'console-log', label: 'Console Log', icon: 'terminal' },
      { kind: 'link', id: 'settings', label: 'Settings', icon: 'settings' },
    ],
  },
]

/** Every entry id in render order, accordions before their children. */
export const NAV_ORDER: string[] = NAV_GROUPS.flatMap((group) =>
  group.entries.flatMap((entry) =>
    entry.kind === 'accordion' ? [entry.id, ...entry.children.map((child) => child.id)] : [entry.id]
  )
)

/**
 * One arrangement container: a group's own entries, or an accordion's children.
 * A row moves only inside its container — a media child belongs to the Media
 * Providers accordion, and the accordion moves as the one row it is — so the
 * arrangement never has to answer what a child would mean outside its parent.
 */
export interface NavContainer {
  id: string
  /** The heading the arrangement panel prints; absent renders without one. */
  label?: string
  ids: string[]
}

export const NAV_CONTAINERS: NavContainer[] = [
  ...NAV_GROUPS.map((group) => ({
    id: group.id,
    label: group.label,
    ids: group.entries.map((entry) => entry.id),
  })),
  ...NAV_GROUPS.flatMap((group) =>
    group.entries.flatMap((entry) =>
      entry.kind === 'accordion'
        ? [{ id: entry.id, label: entry.label, ids: entry.children.map((child) => child.id) }]
        : []
    )
  ),
]

export type NavPreset = 'default' | 'advanced' | 'simple' | 'super-lite'

export interface NavPresetDef {
  id: NavPreset
  label: string
  hint: string
  /** The entries the preset draws; everything else is hidden. */
  visible: string[]
}

/**
 * The sidebar sets the Settings panel offers. A preset fills the hide list and
 * nothing else: the arrangement stays where the operator put it, because the two
 * answer different questions — what is on the sidebar, and in which order.
 */
export const NAV_PRESETS: NavPresetDef[] = [
  {
    id: 'default',
    label: 'Default',
    hint: 'Plans and Persona hidden, everything else in place',
    visible: NAV_ORDER.filter((id) => !DEFAULT_HIDDEN_NAV_IDS.includes(id)),
  },
  {
    id: 'advanced',
    label: 'Advanced',
    hint: 'Every entry, Plans and Persona included',
    visible: [...NAV_ORDER],
  },
  {
    id: 'simple',
    label: 'Simple',
    hint: 'No media surfaces, no CLI, Skills or console tooling',
    visible: NAV_ORDER.filter(
      (id) =>
        !['media-providers', 'cli-tools', 'skills', 'console-log', 'plans'].includes(id)
    ),
  },
  {
    id: 'super-lite',
    label: 'Super Lite',
    hint: 'Endpoint & Key, Providers, Combos, Usage, Settings, Security',
    visible: ['endpoint', 'connections', 'combos', 'analytics', 'settings', 'security'],
  },
]

const NAV_PRESETS_BY_ID = Object.fromEntries(
  NAV_PRESETS.map((preset) => [preset.id, preset])
) as Record<NavPreset, NavPresetDef>

/** The hide list a preset writes, in render order. */
export function navPresetHidden(preset: NavPreset): string[] {
  const visible = NAV_PRESETS_BY_ID[preset].visible
  return NAV_ORDER.filter((id) => !visible.includes(id))
}

/** The preset the hide list equals, or 'custom' once a switch is moved. */
export function activeNavPreset(hidden: unknown): NavPreset | 'custom' {
  const ids = normalizeHiddenNav(hidden)
  for (const preset of NAV_PRESETS) {
    if (ids.length !== navPresetHidden(preset.id).length) continue
    if (navPresetHidden(preset.id).every((id) => ids.includes(id))) return preset.id
  }
  return 'custom'
}

/** Which sidebar ids govern a tab: its own entry, plus the entry it aliases. */
const TAB_OWNERS: Record<string, string[]> = buildTabOwners()

function buildTabOwners(): Record<string, string[]> {
  const owners: Record<string, string[]> = {}
  const own = (tab: string, id: string) => {
    ;(owners[tab] ??= []).push(id)
  }
  for (const group of NAV_GROUPS) {
    for (const entry of group.entries) {
      if (entry.kind === 'link') {
        own(entry.id, entry.id)
        continue
      }
      own(entry.id, entry.id)
      for (const child of entry.children) {
        own(child.id, child.id)
        // Hiding the accordion hides every child, so the child's tab is gone too.
        own(child.id, entry.id)
      }
    }
  }
  // Aliases the sidebar keeps lit for another path: /dashboard/keys is the
  // legacy Endpoint & Key route, Terminal lives inside Console Log, and the
  // Cache/Compression sections live inside Usage.
  own('keys', 'endpoint')
  own('terminal', 'console-log')
  own('usage-cache', 'analytics')
  own('usage-compression', 'analytics')
  return owners
}

/**
 * One list out of a stored value: the `{ hidden: [...] }` or `{ order: [...] }`
 * object the server returns, or a bare array. The helpers below hand their own
 * working list back in, which is why the bare form is accepted too.
 */
function storedListAt(value: unknown, key: 'hidden' | 'order'): unknown[] | null {
  if (Array.isArray(value)) return value
  const raw = (value as Record<string, unknown> | null | undefined)?.[key]
  return Array.isArray(raw) ? raw : null
}

function storedHiddenIds(value: unknown): unknown[] | null {
  return storedListAt(value, 'hidden')
}

/**
 * Keeps only the ids that address a sidebar row, in stored order. `excluded` ids
 * are dropped — the hide list refuses `settings`, the arrangement does not — and
 * the cap differs because a hide list holds a subset while an arrangement holds
 * every row at most once.
 */
function cleanNavIds(raw: readonly unknown[], excluded: readonly string[], limit: number): string[] {
  const ids: string[] = []
  for (const item of raw) {
    if (typeof item !== 'string') continue
    const id = item.trim().toLowerCase()
    if (!NAV_ID_PATTERN.test(id)) continue
    if (!NAV_ORDER.includes(id)) continue
    if (excluded.includes(id) || ids.includes(id)) continue
    if (ids.length >= limit) break
    ids.push(id)
  }
  return ids
}

/**
 * Coerces whatever the server stored into a usable hide list, mirroring
 * internal/handlers/dashboard/navvisibility.go. The server only checks the
 * shape; the sidebar entry list lives here, so an id that addresses no entry is
 * dropped rather than stored as dead configuration.
 */
export function normalizeHiddenNav(value: unknown): string[] {
  return cleanNavIds(
    storedHiddenIds(value) ?? DEFAULT_HIDDEN_NAV_IDS,
    ALWAYS_VISIBLE_NAV_IDS,
    MAX_HIDDEN_NAV_ENTRIES
  )
}

/** Store the list in render order, so the saved JSON stays stable and readable. */
export function orderHiddenNav(value: unknown): string[] {
  const hidden = normalizeHiddenNav(value)
  return NAV_ORDER.filter((id) => hidden.includes(id))
}

/** The next hide list after the operator flipped one entry's switch. */
export function setNavVisible(hidden: unknown, id: string, visible: boolean): string[] {
  const current = normalizeHiddenNav(hidden)
  if (visible || ALWAYS_VISIBLE_NAV_IDS.includes(id)) {
    return orderHiddenNav(current.filter((entry) => entry !== id))
  }
  // normalizeHiddenNav drops an id that addresses no entry, so this cannot
  // store the panel's own id or a typo.
  return orderHiddenNav([...current, id])
}

/**
 * Every id the stored arrangement holds, in stored order, mirroring the server.
 * An absent or empty list means "the order this build declares", which is what
 * the arrangement panel's Reset writes back.
 */
export function normalizeNavOrder(value: unknown): string[] {
  return cleanNavIds(storedListAt(value, 'order') ?? [], [], MAX_NAV_ORDER_ENTRIES)
}

/**
 * The stored arrangement as one complete list per container, in declaration
 * order. The setting is one flat array; the containers are what the movement
 * helpers work on, and rebuilding all of them on every move keeps the saved JSON
 * stable whatever the operator does.
 */
function navOrdersByContainer(order: unknown): Record<string, string[]> {
  const stored = normalizeNavOrder(order)
  const byContainer: Record<string, string[]> = {}
  for (const container of NAV_CONTAINERS) {
    byContainer[container.id] = normalizeListAgainst(stored, container.ids)
  }
  return byContainer
}

/** The containers' orders flattened back into the one array the server stores. */
function flattenNavOrders(byContainer: Record<string, string[]>): string[] {
  return NAV_CONTAINERS.flatMap((container) => byContainer[container.id])
}

/**
 * The sidebar in the operator's arrangement, hidden entries and all. The panel
 * that edits the arrangement needs to draw the rows it is hiding, so hiding is a
 * separate step (`orderedNavGroups`) rather than part of the arrangement.
 */
export function arrangedNavGroups(order: unknown): NavGroup[] {
  const byContainer = navOrdersByContainer(order)
  return NAV_GROUPS.map((group) => {
    const byId: Record<string, NavEntry> = Object.fromEntries(
      group.entries.map((entry) => [entry.id, entry])
    )
    const entries: NavEntry[] = []
    for (const id of byContainer[group.id]) {
      const entry = byId[id]
      if (!entry) continue
      if (entry.kind !== 'accordion') {
        entries.push(entry)
        continue
      }
      const byChild: Record<string, NavLink> = Object.fromEntries(
        entry.children.map((child) => [child.id, child])
      )
      entries.push({
        ...entry,
        children: byContainer[entry.id].flatMap((childId) => byChild[childId] ?? []),
      })
    }
    return { ...group, entries }
  })
}

/** The sidebar with the hidden entries removed; empty groups disappear too. */
export function orderedNavGroups(hidden: unknown, order: unknown): NavGroup[] {
  const ids = normalizeHiddenNav(hidden)
  const groups: NavGroup[] = []
  for (const group of arrangedNavGroups(order)) {
    const entries: NavEntry[] = []
    for (const entry of group.entries) {
      if (ids.includes(entry.id)) continue
      if (entry.kind === 'accordion') {
        const children = entry.children.filter((child) => !ids.includes(child.id))
        // An accordion whose children are all hidden would render as a header
        // that opens onto nothing, so it is dropped with them.
        if (children.length === 0) continue
        entries.push({ ...entry, children })
        continue
      }
      entries.push(entry)
    }
    if (entries.length > 0) groups.push({ ...group, entries })
  }
  return groups
}

/**
 * Moves one row one place up (-1) or down (+1) inside its container. A move past
 * either end, or an id no container holds, leaves the arrangement unchanged —
 * which is what the disabled arrow buttons rely on.
 */
export function moveNavEntry(order: unknown, id: string, delta: -1 | 1): string[] {
  return moveNavRow(order, id, (list) => moveInList(list, id, delta))
}

/**
 * Drops one row into another's slot. A drop on a row of another container lands
 * nowhere: a media child has no place among the group's own entries, so the
 * arrangement comes back normalized instead.
 */
export function dropNavEntry(
  order: unknown,
  id: string,
  targetId: string,
  before: boolean
): string[] {
  if (navContainerOf(id) !== navContainerOf(targetId)) {
    return flattenNavOrders(navOrdersByContainer(order))
  }
  return moveNavRow(order, id, (list) => moveBeforeInList(list, id, targetId, before))
}

/** The container a row belongs to, or null for an id this build does not draw. */
function navContainerOf(id: string): string | null {
  return NAV_CONTAINERS.find((container) => container.ids.includes(id))?.id ?? null
}

function moveNavRow(order: unknown, id: string, move: (list: string[]) => string[]): string[] {
  const byContainer = navOrdersByContainer(order)
  const container = navContainerOf(id)
  if (!container) return flattenNavOrders(byContainer)
  byContainer[container] = move(byContainer[container])
  return flattenNavOrders(byContainer)
}

/** Is the tab the operator is looking at hidden? */
export function isNavHidden(tab: ActiveTab, hidden: unknown): boolean {
  const ids = normalizeHiddenNav(hidden)
  return (TAB_OWNERS[tab] ?? []).some((owner) => ids.includes(owner))
}

/**
 * The sidebar entry that lights up for a tab. Endpoint & Key stays lit for the
 * legacy /dashboard/keys path, Console Log for Terminal, and Usage for the
 * Cache and Compression sections.
 */
export function isNavLinkActive(id: ActiveTab, activeTab: ActiveTab): boolean {
  if (id === 'endpoint') return activeTab === 'endpoint' || activeTab === 'keys'
  if (id === 'console-log') return activeTab === 'console-log' || activeTab === 'terminal'
  if (id === 'analytics') {
    return (
      activeTab === 'analytics' || activeTab === 'usage-cache' || activeTab === 'usage-compression'
    )
  }
  return activeTab === id
}
