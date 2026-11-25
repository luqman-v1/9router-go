/** Minimal stored-row shape; CustomModelData and CustomModelEntry both fit. */
export interface StoredCustomModel {
  id: string
  type?: string
}

export interface EditFormValues {
  caps: { vision: boolean; reasoning: boolean; jailbreak: boolean }
  contextWindow: number
  maxOutput: number
  /** undefined = textarea blank, which clears a stored extraBody. */
  extraBody?: Record<string, unknown>
}

/** Pretty-printed stored extraBody for the textarea; '' when none. */
export function extraBodyToText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2)
}

/**
 * Full row for an edit save: the stored row is the base so fields the modal
 * does not edit (name, ...) survive; the stored `type` is kept so the key
 * (alias|id|type) addresses the same row. A cleared extraBody is sent as null.
 */
export function buildEditedRow(stored: StoredCustomModel, form: EditFormValues): Record<string, unknown> {
  const row: Record<string, unknown> = { ...stored }
  const isLlm = (stored.type || 'llm') === 'llm'
  if (isLlm) row.caps = { ...form.caps }
  if (form.contextWindow > 0) row.contextWindow = form.contextWindow
  else delete row.contextWindow
  if (form.maxOutput > 0) row.maxOutput = form.maxOutput
  else delete row.maxOutput
  row.extraBody = form.extraBody ?? null
  return row
}

/**
 * Disabled list minus deleted custom ids. The delete endpoint parks the id in
 * disabledModels (so a live catalog stops re-advertising it); a deleted custom
 * row must not linger in "Disabled models". Ids that are also built-in stay.
 */
export function pruneDisabledAfterDelete(disabled: string[], deletedIds: string[], builtInIds: string[]): string[] {
  const drop = new Set(deletedIds.filter((id) => !builtInIds.includes(id)))
  return disabled.filter((id) => !drop.has(id))
}
