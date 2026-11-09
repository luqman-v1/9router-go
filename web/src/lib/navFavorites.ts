/**
 * The pinned sidebar entries: which ids, in what order, and the rules that keep
 * the list honest.
 *
 * Pure on purpose — the reactive state and its localStorage read/write live in
 * lib/navFavorites.svelte.ts, so this module can be unit-tested without the
 * Svelte compiler, the way lib/usagePanelPlan.ts and lib/period.ts are.
 */

/** The section is a shortcut, not a second sidebar: eight is already a scroll. */
export const MAX_PINNED_NAV_ENTRIES = 8

/**
 * Keeps only ids this build draws, in the order they arrived, capped.
 *
 * An entry removed from web/src/lib/nav.ts must not leave a pin that renders
 * nothing, and a hand-edited or stale localStorage value must not grow the list
 * without bound.
 */
export function normalizePinnedNav(
  value: unknown,
  known: readonly string[]
): string[] {
  if (!Array.isArray(value)) return []
  const ids: string[] = []
  for (const item of value) {
    if (typeof item !== 'string') continue
    if (!known.includes(item)) continue
    if (ids.includes(item)) continue
    if (ids.length >= MAX_PINNED_NAV_ENTRIES) break
    ids.push(item)
  }
  return ids
}

/**
 * The next list after one pin was flipped.
 *
 * A list already at the cap refuses a new pin rather than dropping the oldest:
 * the operator sees the star stay unlit and can unpin something first, instead
 * of losing a pin they did not touch.
 */
export function togglePinnedNav(
  current: readonly string[],
  id: string,
  known: readonly string[]
): { ids: string[]; pinned: boolean } {
  const normalized = normalizePinnedNav(current, known)
  if (normalized.includes(id)) {
    return { ids: normalized.filter((entry) => entry !== id), pinned: false }
  }
  if (normalized.length >= MAX_PINNED_NAV_ENTRIES) {
    return { ids: normalized, pinned: false }
  }
  return { ids: [...normalized, id], pinned: true }
}
