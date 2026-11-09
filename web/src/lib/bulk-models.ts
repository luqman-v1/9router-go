// Bulk custom-model id parser.
//
// The operator pastes whatever a provider page gave them — a column copied out of
// a pricing table, a comma list from a chat message, one id per line — and the
// paste is the whole input. Splitting on every run of whitespace, commas and
// semicolons covers all three shapes without asking which one it was.
//
// The result is a plan, not a write: `ids` is what the caller should send, and
// `duplicates`/`invalid` are what it should report back. Ids are never silently
// dropped — an id that vanished between the paste and the table looks exactly
// like a typo, and the operator pastes it again.

export interface BulkModelsResult {
  /** Accepted ids, in paste order, deduped, with the alias prefix stripped. */
  ids: string[]
  /** Ids that appeared more than once in the paste; first occurrence wins. */
  duplicates: string[]
  /** Ids the model id rules reject (see isInvalidModelId). */
  invalid: string[]
}

// Separators: space, tab, newline and other whitespace, comma, semicolon.
const SEPARATORS = /[\s,;]+/

// INVALID_PATTERN is what a model id may not contain. The server decides this
// authoritatively (the id becomes half of a "<provider>/<id>" address a client
// copies into its config); the client only refuses the obvious shapes early
// enough to say so before a round trip: a pipe (the kv key separator), quotes
// and angle brackets. Slashes are permitted for namespaced models like
// "deepseek/deepseek-v4-1-flash".
const INVALID_PATTERN = /[|"'`\\<>]/
/**
 * parseModelIds splits a pasted list into the ids worth saving.
 *
 * `alias` strips a leading "<alias>/" from each id, so a paste from a page that
 * already wrote the addresses ("nara/glm-5.3") lands the same rows as a paste of
 * bare ids — the single-id field does the same with stripAlias.
 */
export function parseModelIds(input: string, alias?: string): BulkModelsResult {
  const result: BulkModelsResult = { ids: [], duplicates: [], invalid: [] }
  if (typeof input !== 'string' || input.trim() === '') {
    return result
  }

  const prefix = alias ? `${alias}/` : ''
  const seen = new Set<string>()
  // A rejected id is reported once even when the operator pasted it repeatedly:
  // the list is a set of problems to fix, not a transcript.
  const reportedInvalid = new Set<string>()

  for (const raw of input.split(SEPARATORS)) {
    let id = raw.trim()
    if (id === '') continue
    if (prefix && id.startsWith(prefix)) {
      id = id.slice(prefix.length).trim()
      if (id === '') continue
    }

    if (
      id.length > 200 ||
      INVALID_PATTERN.test(id) ||
      id.startsWith('/') ||
      id.endsWith('/') ||
      id.includes('//')
    ) {
      if (!reportedInvalid.has(id)) {
        reportedInvalid.add(id)
        result.invalid.push(id)
      }
      continue
    }

    if (seen.has(id)) {
      result.duplicates.push(id)
      continue
    }
    seen.add(id)
    result.ids.push(id)
  }

  return result
}

/**
 * summarizeBulkModels renders the result line the modal shows. `added` counts
 * what the server confirmed, which is what makes a partial save visible: the
 * plan's own counts are from before the request.
 */
export function summarizeBulkModels(plan: BulkModelsResult, added: number): string {
  const parts = [`${added} added`]
  const refused = plan.ids.length - added
  if (refused > 0) parts.push(`${refused} refused`)
  if (plan.duplicates.length > 0) parts.push(`${plan.duplicates.length} duplicate`)
  if (plan.invalid.length > 0) parts.push(`${plan.invalid.length} invalid`)
  return parts.join(', ')
}
