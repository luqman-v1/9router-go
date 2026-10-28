/**
 * The list primitives the two arrangement editors share: the Usage panel list in
 * Settings and the sidebar list. They are generic over the id type so each
 * consumer keeps its own union, and they never mutate the list they are handed —
 * every caller replaces a `$state` value with the result.
 */

/**
 * normalizeListAgainst repairs a stored order against the ids this build knows:
 * unknown ids (an older or newer build) are dropped, and anything missing is
 * appended in declaration order. Without it, a stored order that predates an item
 * would hide that item from the list for good — for the sidebar arrangement that
 * means a row that can no longer be reached, and for the Usage arrangement a
 * panel that never comes back.
 */
export function normalizeListAgainst<T extends string>(
  stored: readonly string[],
  allowed: readonly T[]
): T[] {
  const known = new Set<string>(allowed)
  const kept: T[] = []
  for (const id of stored) {
    if (known.has(id) && !kept.includes(id as T)) kept.push(id as T)
  }
  for (const id of allowed) {
    if (!kept.includes(id)) kept.push(id)
  }
  return kept
}

/**
 * moveInList returns the list with `id` swapped one place up (-1) or down (+1).
 * A move past either end is a no-op, which is what the disabled arrow buttons
 * rely on rather than each caller re-checking the bounds.
 */
export function moveInList<T extends string>(list: readonly T[], id: T, delta: -1 | 1): T[] {
  const next = [...list]
  const from = next.indexOf(id)
  if (from < 0) return next
  const to = from + delta
  if (to < 0 || to >= next.length) return next
  next[from] = next[to]
  next[to] = id
  return next
}

/**
 * moveBeforeInList returns the list with `id` dropped into `targetId`'s slot —
 * above it when `before`, below it otherwise. A drop that cannot move anything
 * (an id onto itself, or one this build does not know) comes back unchanged,
 * which is what lets a drag handler call this without pre-checking the pair.
 */
export function moveBeforeInList<T extends string>(
  list: readonly T[],
  id: T,
  targetId: T,
  before: boolean
): T[] {
  const next = [...list]
  if (id === targetId) return next
  const from = next.indexOf(id)
  if (from < 0 || next.indexOf(targetId) < 0) return next
  next.splice(from, 1)
  const slot = next.indexOf(targetId) + (before ? 0 : 1)
  if (slot === from) return [...list]
  next.splice(slot, 0, id)
  return next
}
