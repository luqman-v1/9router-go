// The API-key row menu and its batch bar collapse seven per-row controls into
// one trigger, so the rules the table depends on are stated once here rather
// than re-derived in the markup.
//
// Batch actions run one request per key on purpose: there is no bulk endpoint, and
// a single failing key must not roll back the keys that already succeeded. The
// outcome is therefore a tally, not a yes/no.

export interface KeyOutcome {
  ok: number
  failed: number
  /** Names or ids of the keys the server rejected, for the message. */
  failedIds: string[]
}

/** Whether a run of batch actions should stay disabled. */
export function isBatchBusy(busy: boolean, keys: string[]): boolean {
  return busy || keys.length === 0
}

/**
 * Runs `action` for each key, counting outcomes instead of stopping at the first
 * failure — a batch that aborts halfway leaves the operator unable to tell which
 * half landed.
 */
export async function runBatch(
  ids: string[],
  action: (id: string) => Promise<unknown>,
  label: (id: string) => string = (id) => id,
): Promise<KeyOutcome> {
  const outcome: KeyOutcome = { ok: 0, failed: 0, failedIds: [] }
  for (const id of ids) {
    try {
      await action(id)
      outcome.ok += 1
    } catch {
      outcome.failed += 1
      outcome.failedIds.push(label(id))
    }
  }
  return outcome
}

/** The message shown once a batch action finishes. */
export function batchSummary(verb: string, outcome: KeyOutcome): string {
  if (outcome.failed === 0) {
    return outcome.ok === 1
      ? `1 key ${verb}.`
      : `${outcome.ok} keys ${verb}.`
  }
  const base =
    outcome.ok === 0 ? `No key was ${verb}.` : `${outcome.ok} ${outcome.ok === 1 ? 'key was' : 'keys were'} ${verb}.`
  return `${base} ${outcome.failed} failed: ${outcome.failedIds.join(', ')}.`
}

/**
 * Whether the checkbox for one row is ticked. A key not in the list is not
 * selected; an empty list means nothing is selected rather than everything,
 * which is what `every()` on an empty array would otherwise report.
 */
export function isSelected(selectedIds: string[], id: string): boolean {
  return selectedIds.includes(id)
}

/** Whether the header checkbox should render as ticked. */
export function isAllSelected(selectedIds: string[], allIds: string[]): boolean {
  return allIds.length > 0 && selectedIds.length === allIds.length
}

/** The header checkbox state: ticked when all, indeterminate when some. */
export function selectAllState(
  selectedIds: string[],
  allIds: string[],
): 'none' | 'some' | 'all' {
  if (allIds.length === 0 || selectedIds.length === 0) return 'none'
  return isAllSelected(selectedIds, allIds) ? 'all' : 'some'
}

/**
 * Toggling the header checkbox selects everything, or clears the selection when
 * everything is already selected.
 */
export function toggleAll(selectedIds: string[], allIds: string[]): string[] {
  return isAllSelected(selectedIds, allIds) ? [] : [...allIds]
}

/** Toggling one row adds or removes it, leaving the rest untouched. */
export function toggleOne(selectedIds: string[], id: string): string[] {
  return selectedIds.includes(id)
    ? selectedIds.filter((current) => current !== id)
    : [...selectedIds, id]
}