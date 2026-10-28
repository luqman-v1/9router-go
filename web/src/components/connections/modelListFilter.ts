// Pure search / filter / sort logic for the provider model lists.
//
// Two lists share this pipeline: the registered-provider grid (built-in
// registry models + custom models) and the compatible-node list (custom
// models + legacy aliases). Both render the same row shape, so both go
// through the same functions here — a second convention per list would
// drift, and the batch selector has to mean the same thing on both.
//
// Search reuses modelSearchFilter (positive/negative terms, quoted
// strings, /regex/) so the syntax an operator already types into the
// combo model picker keeps working here.

import { matchesSearch, parseSearchQuery, type ParsedSearchQuery } from '../../lib/modelSearchFilter'

export type ModelSource = 'registry' | 'custom' | 'legacyAlias'

export type SortMode = 'registry' | 'az' | 'za' | 'latency-asc' | 'latency-desc' | 'added-desc' | 'added-asc'

export type ModelTestStatus = 'ok' | 'error' | 'blocked' | 'testing'

/** One row as the toolbar sees it. The caller maps its own list into this. */
export interface ModelRowView {
  id: string
  name?: string
  /** registry = ships with the build; custom = written by the operator; legacyAlias = an alias row. */
  source: ModelSource
  caps: {
    vision: boolean
    reasoning: boolean
    search: boolean
    free: boolean
    /** Uncensored / unrestricted — accepts prompts a filtered model refuses. */
    jailbreak: boolean
  }
  /** Non-empty means the model accepts a reasoning level. */
  thinkingLevels: string[]
  /** Badged dead upstream by the deprecation sync. */
  deprecated: boolean
  /** In the provider's disabled-models list. */
  disabled: boolean
  /** Verdict of the last probe, or undefined when never tested. */
  testStatus?: ModelTestStatus
  /**
   * Round-trip time of the last successful probe, in milliseconds.
   * Undefined when the model has never been probed — which is what makes
   * the latency sorts put the unmeasured rows last rather than treating
   * them as zero.
   */
  latencyMs?: number
  /**
   * When the row was registered, as epoch milliseconds. Only custom rows
   * carry one — a registry model ships with the build and has no add date —
   * so the register-date sorts keep those in registry order at the end
   * rather than pretending they were added at the epoch.
   */
  addedAt?: number
}

export interface ModelListFilter {
  query: string
  sort: SortMode
  caps: {
    thinking: boolean
    vision: boolean
    search: boolean
    free: boolean
    jailbreak: boolean
  }
  status: {
    /** Enabled for this provider — the default state of a listed model. */
    available: boolean
    /** Switched off by the operator. */
    disabled: boolean
    /**
     * Present in the list but not usable: disabled, failed its last probe,
     * or badged deprecated. The complement of "available" is "disabled"
     * alone, which is why this is its own chip — an operator asking for
     * "what can I not use?" wants all three reasons, not one.
     */
    unavailable: boolean
    /** Last probe failed or the account is in cooldown. */
    error: boolean
    /** Never probed. */
    untested: boolean
    /** Badged deprecated. */
    deprecated: boolean
  }
}

export const EMPTY_MODEL_LIST_FILTER: ModelListFilter = {
  query: '',
  sort: 'registry',
  caps: { thinking: false, vision: false, search: false, free: false, jailbreak: false },
  status: {
    available: false,
    disabled: false,
    unavailable: false,
    error: false,
    untested: false,
    deprecated: false,
  },
}

/** True when no chip is on — the common case, and the fast path. */
export function isModelListFilterEmpty(filter: ModelListFilter): boolean {
  return (
    filter.query.trim() === '' &&
    filter.sort === 'registry' &&
    !filter.caps.thinking &&
    !filter.caps.vision &&
    !filter.caps.search &&
    !filter.caps.free &&
    !filter.caps.jailbreak &&
    !filter.status.available &&
    !filter.status.disabled &&
    !filter.status.unavailable &&
    !filter.status.error &&
    !filter.status.untested &&
    !filter.status.deprecated
  )
}

/**
 * A row matches when it satisfies every *group* that has a chip on.
 *
 * Within a group the chips OR (thinking OR vision), across groups they
 * AND (vision AND free). That is what makes the chips useful: "vision +
 * free" narrows, "vision" alone does not hide the non-vision rows behind
 * a second capability the operator never asked about.
 */
function matchesGroups(filter: ModelListFilter, row: ModelRowView): boolean {
  const capChips = [filter.caps.thinking, filter.caps.vision, filter.caps.search, filter.caps.free, filter.caps.jailbreak]
  if (capChips.some(Boolean)) {
    const capOk =
      (filter.caps.thinking && (row.caps.reasoning || row.thinkingLevels.length > 0)) ||
      (filter.caps.vision && row.caps.vision) ||
      (filter.caps.search && row.caps.search) ||
      (filter.caps.free && row.caps.free) ||
      (filter.caps.jailbreak && row.caps.jailbreak)
    if (!capOk) return false
  }

  const statusChips = [
    filter.status.available,
    filter.status.disabled,
    filter.status.unavailable,
    filter.status.error,
    filter.status.untested,
    filter.status.deprecated,
  ]
  if (statusChips.some(Boolean)) {
    const statusOk =
      (filter.status.available && !isRowUnavailable(row)) ||
      (filter.status.disabled && row.disabled) ||
      (filter.status.unavailable && isRowUnavailable(row)) ||
      (filter.status.error && (row.testStatus === 'error' || row.testStatus === 'blocked')) ||
      (filter.status.untested && row.testStatus === undefined) ||
      (filter.status.deprecated && row.deprecated)
    if (!statusOk) return false
  }

  return true
}

/**
 * A row the operator cannot use right now: switched off, failed its last
 * probe, or retired upstream. "Untested" is deliberately NOT part of this —
 * a model nobody has probed yet may well work, and calling it unavailable
 * would send the operator looking for a fault that was never observed.
 */
function isRowUnavailable(row: ModelRowView): boolean {
  return (
    row.disabled ||
    row.deprecated ||
    row.testStatus === 'error' ||
    row.testStatus === 'blocked'
  )
}

/**
 * Search matches id and name only. The combo picker's extractCandidates
 * also reads `provider`, which is constant within one provider's list and
 * would make every row match every query — noise, not a feature.
 */
function matchesQuery(parsed: ParsedSearchQuery, row: ModelRowView): boolean {
  if (parsed.isEmpty) return true
  return matchesSearch({ id: row.id, name: row.name ?? '' }, parsed)
}

function sortRows(rows: ModelRowView[], sort: SortMode): ModelRowView[] {
  if (sort === 'registry') return rows
  const sorted = [...rows]
  if (sort === 'latency-asc' || sort === 'latency-desc') {
    // A model nobody has probed has no latency, and sorting it as zero would
    // put the unmeasured rows at the top of "fastest" — the operator would
    // read a blank as a win. They go last in both directions, and ties keep
    // the incoming order so the sort is stable rather than arbitrary.
    const measured = (r: ModelRowView) => r.latencyMs ?? Number.POSITIVE_INFINITY
    sorted.sort((a, b) => measured(a) - measured(b))
    if (sort === 'latency-desc') {
      // Reverse only the measured prefix: flipping the whole array would move
      // the unmeasured rows to the front, which is the one place they must
      // not be.
      const measuredCount = sorted.filter((r) => r.latencyMs !== undefined).length
      const head = sorted.slice(0, measuredCount).reverse()
      const tail = sorted.slice(measuredCount)
      return [...head, ...tail]
    }
    return sorted
  }
  if (sort === 'added-desc' || sort === 'added-asc') {
    // Registry models have no add date; they sort after every dated row
    // instead of collapsing to 1970. Within the dated rows the newest comes
    // first for 'added-desc', which is what "what did I add recently?" means.
    const dated = (r: ModelRowView) => r.addedAt ?? Number.NEGATIVE_INFINITY
    sorted.sort((a, b) => dated(b) - dated(a))
    if (sort === 'added-asc') {
      // Reverse only the dated prefix: flipping the whole array would move
      // the undated rows to the front, the one place they must not be.
      const datedCount = sorted.filter((r) => r.addedAt !== undefined).length
      const head = sorted.slice(0, datedCount).reverse()
      const tail = sorted.slice(datedCount)
      return [...head, ...tail]
    }
    return sorted
  }
  // Case-insensitive, matching compatibleRows' display sort: ids are
  // lower-case by convention but vendor catalogues are not.
  sorted.sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: 'base' }))
  if (sort === 'za') sorted.reverse()
  return sorted
}

/**
 * The full pipeline: search → filter groups → sort. Pure: the input array
 * is never mutated, so a caller's $derived can re-run it on every keystroke
 * without corrupting the list the check-all sweep reads.
 */
export function applyModelListFilter(rows: ModelRowView[], filter: ModelListFilter): ModelRowView[] {
  const parsed = parseSearchQuery(filter.query)
  const filtered = rows.filter((row) => matchesQuery(parsed, row) && matchesGroups(filter, row))
  return sortRows(filtered, filter.sort)
}

/**
 * Whether any row carries a measured latency. The sort dropdown offers the
 * latency orders only when this is true: picking "fastest first" on a list
 * nobody has probed yet would reorder nothing and look broken.
 */
export function hasLatencyData(rows: ModelRowView[]): boolean {
  return rows.some((r) => r.latencyMs !== undefined)
}

/**
 * Select-all state for a header checkbox over the *filtered* rows.
 *
 * `indeterminate` is what the checkbox renders when the selection covers
 * some but not all visible rows — without it the box reads "all selected"
 * while a third of the list is unchecked.
 */
export interface SelectionState {
  selected: number
  total: number
  allSelected: boolean
  indeterminate: boolean
}

export function selectionState(selectedIds: ReadonlySet<string>, visibleRows: ModelRowView[]): SelectionState {
  const selected = visibleRows.filter((r) => selectedIds.has(r.id)).length
  const total = visibleRows.length
  return {
    selected,
    total,
    allSelected: total > 0 && selected === total,
    indeterminate: selected > 0 && selected < total,
  }
}

/**
 * The next selection after the header checkbox is clicked: everything
 * visible when turning on, nothing when turning off. Ids outside the
 * current filter are preserved on purpose — an operator who checked the
 * errors, then cleared the filter, still has the errors selected.
 */
export function toggleSelectAll(
  selectedIds: ReadonlySet<string>,
  visibleRows: ModelRowView[],
  selectAll: boolean,
): Set<string> {
  const next = new Set(selectedIds)
  for (const row of visibleRows) {
    if (selectAll) next.add(row.id)
    else next.delete(row.id)
  }
  return next
}

/** Ids of the selected rows that are still visible — what a batch acts on. */
export function selectedVisibleIds(selectedIds: ReadonlySet<string>, visibleRows: ModelRowView[]): string[] {
  return visibleRows.filter((r) => selectedIds.has(r.id)).map((r) => r.id)
}
