// Pure logic for the "Import from /models" preview panel. Kept out of the
// .svelte file so the matching, sorting and selection rules are testable
// without a component mount, and so the panel and the main list cannot drift
// apart: the search here reuses the same matchesSearch the toolbar's
// ModelSearchBar goes through, and the sort reuses SortMode from
// modelListFilter, so the two lists order the same way for the same pick.
import { matchesSearch } from '../../lib/modelSearchFilter'
import type { SortMode } from './modelListFilter'

export interface ImportPreviewModel {
  id: string
  contextWindow?: number
  maxOutput?: number
  caps?: { vision?: boolean; reasoning?: boolean; free?: boolean }
}

/** Case-insensitive filter over the fetched catalog; an empty query lists everything. */
export function filterImportPreviewModels(
  models: ImportPreviewModel[],
  query: string
): ImportPreviewModel[] {
  const q = query.trim()
  if (!q) return models
  return models.filter((m) => matchesSearch(m.id, q))
}

/**
 * The chip-state rule: a chip renders as "Ada" (green, removable) exactly when
 * its id is on the node's active list. The set spans the three sources a row
 * can come from: the compatible rows (custom + legacy alias), the custom
 * models of a registered provider, and the ids that ship with the build, and
 * because the panel serves both sections.
 */
export function buildImportPreviewActiveIds(
  rowIds: string[],
  customIds: string[],
  builtinIds: string[]
): Set<string> {
  return new Set([...rowIds, ...customIds, ...builtinIds])
}

/** The orders the panel offers. registry/A-Z are the same order here (the
 *  fetch has no registry sequence), so the menu shows A-Z once. */
export type ImportPreviewSort = 'az' | 'za' | 'latency-asc' | 'latency-desc'

export const IMPORT_PREVIEW_SORTS: ReadonlyArray<{ value: ImportPreviewSort; label: string }> = [
  { value: 'az', label: 'A–Z' },
  { value: 'za', label: 'Z–A' },
  { value: 'latency-asc', label: 'Latency: fastest' },
  { value: 'latency-desc', label: 'Latency: slowest' },
]

/**
 * Sort the fetched list for display. Mirrors sortRows in modelListFilter: an
 * unmeasured model sorts last in both latency directions rather than reading
 * as 0 ms, and ties keep the incoming order so the list is stable.
 */
export function sortImportPreviewModels(
  models: ImportPreviewModel[],
  sort: ImportPreviewSort,
  latency: Record<string, number | undefined>
): ImportPreviewModel[] {
  const sorted = [...models]
  if (sort === 'latency-asc' || sort === 'latency-desc') {
    const measured = (m: ImportPreviewModel) => latency[m.id] ?? Number.POSITIVE_INFINITY
    sorted.sort((a, b) => measured(a) - measured(b))
    if (sort === 'latency-desc') {
      // Reverse only the measured prefix: flipping the whole array would move
      // the unmeasured rows to the front, the one place they must not be.
      const count = sorted.filter((m) => latency[m.id] !== undefined).length
      return [...sorted.slice(0, count).reverse(), ...sorted.slice(count)]
    }
    return sorted
  }
  sorted.sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: 'base' }))
  if (sort === 'za') sorted.reverse()
  return sorted
}

/**
 * The header checkbox's state over the filtered set. `indeterminate` is what
 * keeps the box from reading "all selected" while a third of the list is not.
 */
export function importPreviewSelectionState(
  selectedIds: ReadonlySet<string>,
  filtered: ImportPreviewModel[]
): { selected: number; total: number; allSelected: boolean; indeterminate: boolean } {
  const selected = filtered.filter((m) => selectedIds.has(m.id)).length
  const total = filtered.length
  return {
    selected,
    total,
    allSelected: total > 0 && selected === total,
    indeterminate: selected > 0 && selected < total,
  }
}

/**
 * Next selection after the header checkbox is clicked: everything filtered
 * when turning on, nothing filtered when turning off. Ids outside the current
 * search are preserved on purpose: an operator who selected a set, then
 * narrowed the search, still has that set selected.
 */
export function toggleImportPreviewSelectAll(
  selectedIds: ReadonlySet<string>,
  filtered: ImportPreviewModel[],
  selectAll: boolean
): Set<string> {
  const next = new Set(selectedIds)
  for (const m of filtered) {
    if (selectAll) next.add(m.id)
    else next.delete(m.id)
  }
  return next
}

/** What a batch action runs on: the selected ids that are still filtered in. */
export function importPreviewSelectedIds(
  selectedIds: ReadonlySet<string>,
  filtered: ImportPreviewModel[]
): string[] {
  return filtered.filter((m) => selectedIds.has(m.id)).map((m) => m.id)
}

/** SortMode is the main list's vocabulary; this keeps a shared pick honest. */
export type { SortMode }

/** The chip states the panel can narrow to. */
export type ImportPreviewStateFilter = 'all' | 'available' | 'added'

export const IMPORT_PREVIEW_STATE_FILTERS: ReadonlyArray<{
  value: ImportPreviewStateFilter
  label: string
}> = [
  { value: 'all', label: 'All' },
  { value: 'available', label: 'Not added' },
  { value: 'added', label: 'Added' },
]

/**
 * Narrow the fetched catalog to one chip state. `all` passes through unchanged,
 * so the panel's counters and its bulk actions keep agreeing with each other.
 */
export function filterImportPreviewByState(
  models: ImportPreviewModel[],
  state: ImportPreviewStateFilter,
  activeIds: ReadonlySet<string>
): ImportPreviewModel[] {
  if (state === 'all') return models
  const wantAdded = state === 'added'
  return models.filter((m) => activeIds.has(m.id) === wantAdded)
}

/** Items per page in the preview body. */
export const IMPORT_PREVIEW_PAGE_SIZE = 30

/** Page count for a filtered list, always at least 1 so the control is never blank. */
export function importPreviewPageCount(total: number, pageSize = IMPORT_PREVIEW_PAGE_SIZE): number {
  return Math.max(1, Math.ceil(total / pageSize))
}

export type ImportPreviewPageItem = number | 'gap'

/**
 * The page buttons to draw, in order. Below `maxButtons` every page shows; above
 * it the list keeps the first page, the last page and a window around the
 * current one, with a `gap` marking each elided run. Keeping the endpoints means
 * the last page of a 460-model catalogue is one click away, not one scroll.
 */
export function importPreviewPageItems(
  current: number,
  total: number,
  maxButtons = 7
): ImportPreviewPageItem[] {
  const last = Math.max(1, total)
  const clamped = Math.min(Math.max(1, current), last)
  if (last <= maxButtons) return Array.from({ length: last }, (_, i) => i + 1)

  // Reserve room for both endpoints and both gaps.
  const inner = maxButtons - 4
  const start = Math.max(2, Math.min(clamped - Math.floor(inner / 2), last - inner))
  const end = start + inner - 1

  const out: ImportPreviewPageItem[] = [1]
  // A single hidden page is drawn as itself: a gap marker that elides exactly
  // one number is longer to read than the number.
  if (start === 3) out.push(2)
  else if (start > 3) out.push('gap')
  for (let p = start; p <= end; p++) out.push(p)
  if (end === last - 2) out.push(last - 1)
  else if (end < last - 2) out.push('gap')
  out.push(last)
  return out
}
