/**
 * The Usage page's panel selection and arrangement, as one draft: the Settings
 * card edits it, every Usage section reads it through `isPanelVisible` and
 * `usageBlocks`, and Save persists it.
 *
 * Persistence is a localStorage preference like lib/displayPrefs.ts — not a
 * server setting — so the settings card stages edits in the draft and only writes
 * them when Save is pressed. The draft and the saved copy are tracked separately
 * because of that: `usagePanelsDirty` is what raises the Save button.
 *
 * The state stays private because Svelte 5 forbids exporting a `$state` binding
 * that is later reassigned (svelte.dev/e/state_invalid_export), the same reason
 * lib/currency.svelte.ts keeps `current` private and exports readers.
 */
import {
  USAGE_PANEL_DEFAULT_HIDDEN,
  USAGE_PANEL_FIXED_POSITION,
  USAGE_PANEL_GROUPS,
  USAGE_PANEL_IDS,
  USAGE_PANEL_PRESETS,
  type UsagePanelGroupId,
  type UsagePanelId,
  type UsagePanelPreset,
} from './usagePanel'
import { moveBeforeInList, moveInList, normalizeListAgainst } from './orderList'
import {
  USAGE_PANEL_DEFAULT_ORDER,
  blocksFor,
  type UsagePanelBlock,
} from './usagePanelPlan'

export {
  USAGE_PANEL_FIXED_POSITION,
  USAGE_PANEL_GROUPS,
  USAGE_PANEL_IDS,
  USAGE_PANEL_ORDERABLE_GROUPS,
  USAGE_PANEL_PRESETS,
} from './usagePanel'
export type { UsagePanelGroupId, UsagePanelId, UsagePanelPreset } from './usagePanel'

const STORAGE_KEY = 'usagePanelLayout'

interface Layout {
  hidden: UsagePanelId[]
  order: Record<UsagePanelGroupId, UsagePanelId[]>
}

interface StoredLayout {
  hidden?: unknown
  order?: Record<string, unknown>
}

function isPanelId(value: unknown): value is UsagePanelId {
  return typeof value === 'string' && (USAGE_PANEL_IDS as string[]).includes(value)
}

function defaultLayout(): Layout {
  const order = {} as Record<UsagePanelGroupId, UsagePanelId[]>
  for (const group of USAGE_PANEL_GROUPS) order[group.id] = [...USAGE_PANEL_DEFAULT_ORDER[group.id]]
  return { hidden: [...USAGE_PANEL_DEFAULT_HIDDEN], order }
}

function readStored(): Layout {
  const fallback = defaultLayout()
  if (typeof localStorage === 'undefined') return fallback
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return fallback
    const parsed: unknown = JSON.parse(raw)
    // The first version stored a bare array of hidden ids; keep reading it so an
    // existing selection survives the upgrade instead of silently resetting.
    const source: StoredLayout = Array.isArray(parsed) ? { hidden: parsed } : (parsed as StoredLayout)
    const hidden = Array.isArray(source.hidden) ? source.hidden.filter(isPanelId) : []
    const order = {} as Record<UsagePanelGroupId, UsagePanelId[]>
    for (const group of USAGE_PANEL_GROUPS) {
      const stored = source.order?.[group.id]
      order[group.id] = normalizeListAgainst(
        Array.isArray(stored) ? stored : [],
        USAGE_PANEL_DEFAULT_ORDER[group.id]
      )
    }
    return { hidden, order }
  } catch {
    return fallback
  }
}

let draft = $state<Layout>(readStored())
let saved = $state<Layout>(readStored())

/** The panels currently hidden, as the draft stands. */
export function hiddenUsagePanels(): UsagePanelId[] {
  return draft.hidden
}

/** Whether one panel is drawn right now. */
export function isPanelVisible(id: UsagePanelId): boolean {
  return !draft.hidden.includes(id)
}

export function setPanelVisible(id: UsagePanelId, visible: boolean): void {
  if (visible) {
    draft = { ...draft, hidden: draft.hidden.filter((h) => h !== id) }
    return
  }
  if (!draft.hidden.includes(id)) draft = { ...draft, hidden: [...draft.hidden, id] }
}

/** One section's arrangement, including the panels currently hidden. */
export function panelOrder(group: UsagePanelGroupId): UsagePanelId[] {
  return draft.order[group]
}

/** Moves one panel one place up (-1) or down (+1); a move past an end is a no-op. */
export function movePanel(group: UsagePanelGroupId, id: UsagePanelId, delta: -1 | 1): void {
  draft = { ...draft, order: { ...draft.order, [group]: moveInList(draft.order[group], id, delta) } }
}

/**
 * Drops one panel into another's slot — the drag-and-drop counterpart of
 * movePanel. A drop onto a pinned panel is dropped on the floor, so the rows the
 * Settings card shows as fixed stay where the section draws them.
 */
export function sendPanelTo(
  group: UsagePanelGroupId,
  id: UsagePanelId,
  targetId: UsagePanelId,
  before: boolean
): void {
  if (USAGE_PANEL_FIXED_POSITION[id] || USAGE_PANEL_FIXED_POSITION[targetId]) return
  draft = {
    ...draft,
    order: { ...draft.order, [group]: moveBeforeInList(draft.order[group], id, targetId, before) },
  }
}

/** Drops an arrangement back to the order this build declares. */
export function resetPanelOrder(group: UsagePanelGroupId): void {
  draft = { ...draft, order: { ...draft.order, [group]: [...USAGE_PANEL_DEFAULT_ORDER[group]] } }
}

/**
 * The blocks one section draws, in the arranged order: the two panels that share
 * a layout row stay paired while they are visible and adjacent, everything else is
 * one block. Hidden panels are already left out, so the template never has to
 * re-check visibility.
 */
export function usageBlocks(group: UsagePanelGroupId): UsagePanelBlock[] {
  return blocksFor(draft.order[group].filter((id) => !draft.hidden.includes(id)))
}

/** The preset the draft currently equals, or 'custom' once a toggle is moved. */
export function activeUsagePreset(): UsagePanelPreset | 'custom' {
  for (const preset of USAGE_PANEL_PRESETS) {
    const hiddenByPreset = USAGE_PANEL_IDS.filter((id) => !preset.visible.includes(id))
    const matches =
      hiddenByPreset.length === draft.hidden.length && hiddenByPreset.every((id) => draft.hidden.includes(id))
    if (matches) return preset.id
  }
  return 'custom'
}

/** A preset fills the toggles; the arrangement is left as the operator set it. */
export function applyUsagePreset(preset: UsagePanelPreset): void {
  const def = USAGE_PANEL_PRESETS.find((p) => p.id === preset)
  if (!def) return
  draft = { ...draft, hidden: USAGE_PANEL_IDS.filter((id) => !def.visible.includes(id)) }
}

/** True while the draft differs from what is stored — what raises Save. */
export function usagePanelsDirty(): boolean {
  if (draft.hidden.length !== saved.hidden.length) return true
  if (draft.hidden.some((id) => !saved.hidden.includes(id))) return true
  return USAGE_PANEL_GROUPS.some((group) => {
    const a = draft.order[group.id]
    const b = saved.order[group.id]
    return a.length !== b.length || a.some((id, i) => id !== b[i])
  })
}

export function saveUsagePanels(): void {
  const snapshot: Layout = { hidden: [...draft.hidden], order: { ...defaultLayout().order } }
  for (const group of USAGE_PANEL_GROUPS) snapshot.order[group.id] = [...draft.order[group.id]]
  // Write first, and only then call the layout stored: if the browser refuses
  // the write (storage blocked, quota), the draft stays dirty and the reminder
  // stays up, instead of reporting a save that a reload would not read back.
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ hidden: snapshot.hidden, order: snapshot.order }))
  }
  saved = snapshot
  draft = snapshot
}
