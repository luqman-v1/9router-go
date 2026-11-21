/**
 * Tolerance for "I typed almost the right thing" searches.
 *
 * The bulk-arm model picker used to require a byte-exact substring, so a query
 * that missed one character or split a provider from its model (`deepseek chat`
 * against `deepseek/chat-v3`) matched nothing and read as "this model does not
 * exist". Two tolerances cover both cases without weakening the exact match:
 * comparing with separators removed, and a bounded edit distance measured over a
 * window of the candidate.
 */

/** Everything that is not a letter or a digit is glue for comparison purposes. */
function bare(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '')
}

/** A query of one or two characters is too short to tolerate any edit. */
function editBudget(query: string): number {
  if (query.length <= 2) return 0
  return query.length <= 4 ? 1 : 2
}

/**
 * Levenshtein distance, computed with two rows only — callers run this per row
 * over catalogs of a few hundred entries, so the full matrix is wasted memory
 * for a comparison whose answer never needs more than `budget`.
 */
function editsWithin(a: string, b: string, budget: number): boolean {
  if (Math.abs(a.length - b.length) > budget) return false
  let prev: number[] = []
  let curr: number[] = []
  for (let j = 0; j <= b.length; j++) prev.push(j)
  for (let i = 1; i <= a.length; i++) {
    curr = [i]
    let rowBest = curr[0]
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1
      const v = Math.min(prev[j] + 1, curr[j - 1] + 1, prev[j - 1] + cost)
      curr.push(v)
      if (v < rowBest) rowBest = v
    }
    if (rowBest > budget) return false
    prev = curr
  }
  return prev[b.length] <= budget
}

/**
 * True when some window of `c` is within `budget` edits of `q`.
 *
 * The window is `q.length + budget` wide so a slip inside the id can still land
 * inside it: `glm54` finds `glm5` inside `zhipuglm53`. A whole-string comparison
 * only matches when the query spans the entire id, and an ordered-subsequence
 * scan only when the operator's characters survive in order — neither recovers
 * `deepseek chat` against `deepseek/chat-v3` once the separator is stripped.
 */
function windowedHit(q: string, c: string, budget: number): boolean {
  const span = Math.min(c.length, q.length + budget)
  for (let start = 0; start + q.length <= c.length; start++) {
    if (editsWithin(q, c.slice(start, start + span), budget)) return true
  }
  return false
}

/**
 * True when `query` matches `candidate` exactly, after separator removal, or
 * within a small edit budget over a candidate window.
 *
 * Exact substring still matches immediately (cheapest and strongest). The
 * separator-free comparison covers spacing and punctuation drift, which is the
 * common case for `provider/model` ids. Edit distance is the last resort and is
 * skipped for long queries: tolerating two edits against a 12-character query
 * would make nearly every id a match, which is how a filter stops filtering.
 */
export function fuzzyMatch(query: string, candidate: string): boolean {
  const raw = query.trim()
  if (!raw) return true
  if (candidate.toLowerCase().includes(raw.toLowerCase())) return true

  const q = bare(raw)
  const c = bare(candidate)
  if (!q || !c) return false
  if (c.includes(q)) return true
  if (q.length > 24) return false
  return windowedHit(q, c, editBudget(q))
}

/**
 * Rank a match so the ordering stays predictable: an exact substring sorts
 * before a separator-equal one, which sorts before a tolerance match. Ties keep
 * the original order, so a stable input keeps its positions.
 */
export function matchRank(query: string, candidate: string): number {
  const raw = query.trim().toLowerCase()
  if (!raw) return 0
  if (candidate.toLowerCase().includes(raw)) return 0
  const q = bare(raw)
  const c = bare(candidate)
  if (c.includes(q)) return 1
  return 2
}

/** The ids that match, best-ranked first and capped at `limit`. */
export function filterFuzzy(
  query: string,
  ids: readonly string[],
  limit = 200,
): string[] {
  const raw = query.trim()
  if (!raw) return ids.slice(0, limit).slice()
  const hits: Array<{ id: string; rank: number }> = []
  for (const id of ids) {
    if (fuzzyMatch(raw, id)) hits.push({ id, rank: matchRank(raw, id) })
  }
  // A single pass sorted by rank: Array.prototype.sort is stable, so equal ranks
  // keep catalog order and the list does not reshuffle per keypress.
  return hits
    .sort((a, b) => a.rank - b.rank)
    .map((h) => h.id)
    .slice(0, limit)
}