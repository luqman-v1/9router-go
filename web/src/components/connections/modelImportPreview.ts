// Pure logic for the "Import from /models" preview panel. Kept out of the
// .svelte file so the matching and chip-state rules are testable without a
// component mount, and so the panel and the main list cannot drift apart:
// the search here reuses the same matchesSearch the toolbar's ModelSearchBar
// goes through, so the syntax an operator already types keeps working.
import { matchesSearch } from '../../lib/modelSearchFilter'

export interface ImportPreviewModel {
  id: string
  contextWindow?: number
  maxOutput?: number
  caps?: { vision?: boolean; reasoning?: boolean }
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
 * can come from — the compatible rows (custom + legacy alias), the custom
 * models of a registered provider, and the ids that ship with the build —
 * because the panel serves both sections.
 */
export function buildImportPreviewActiveIds(
  rowIds: string[],
  customIds: string[],
  builtinIds: string[]
): Set<string> {
  return new Set([...rowIds, ...customIds, ...builtinIds])
}
