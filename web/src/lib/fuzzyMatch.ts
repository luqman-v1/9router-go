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

/**
 * One catalog row's comparison forms, computed once for the id list.
 *
 * A keystroke runs filterFuzzy over every id, and every id re-derived its
 * lowercase and separator-stripped forms on each of those passes: for a live
 * catalog of ~2,000 ids that is ~2,000 lowercase conversions plus ~2,000 regex
 * replacements per keystroke, which measured 11-95 ms against 0.2-0.8 ms for the
 * substring filter it replaced. On the 1-vCPU box the gateway serves from, that
 * is visible input lag — so the per-id work that never changes between
 * keystrokes is hoisted into this index and built once when the catalog loads.
 *
 * `bare` is the separator-stripped form the edit-distance pass runs over, so
 * the one regex replacement per id is also paid once rather than per keystroke.
 * `charCounts` is the letter/digit multiset of `bare`, which the edit-distance
 * pass uses to reject an id in one pass without running the full window loop
 * (see sharesEnoughChars).
 */
export interface FuzzyIndexEntry {
  /** The id exactly as it was listed, so callers never re-derive it. */
  id: string
  /** Lowercased id, for the exact-substring test. */
  lower: string
  /** Separator-stripped lowercased id, for the spacing/punctuation tests. */
  bare: string
  /** Occurrence count of each letter/digit in `bare`. */
  charCounts: ReadonlyMap<string, number>
}

/** Builds the comparison index for one id list. Call it when the list loads. */
export function buildFuzzyIndex(ids: readonly string[]): FuzzyIndexEntry[] {
  const out: FuzzyIndexEntry[] = new Array(ids.length)
  for (let i = 0; i < ids.length; i++) {
    const id = ids[i]
    const lower = id.toLowerCase()
    const b = lower.replace(/[^a-z0-9]+/g, '')
    const charCounts = new Map<string, number>()
    for (const ch of b) charCounts.set(ch, (charCounts.get(ch) ?? 0) + 1)
    out[i] = { id, lower, bare: b, charCounts }
  }
  return out
}

/**
 * True when `c`'s characters can cover `q`'s, up to the edit budget.
 *
 * A window within `budget` edits of `q` matches at least `q.length - budget`
 * characters of `q` exactly: each allowed edit accounts for at most one
 * mismatched character, whether it is a substitution, an insertion or a
 * deletion. So when the whole id shares fewer characters than that, no window
 * can qualify and the full window loop is skipped.
 *
 * It is a rejection test only — it never rules out a real match (verified
 * against windowedHit over the whole catalog in the test file) — but it removes
 * the Levenshtein pass for most non-matching ids in a single multiset scan.
 * Counting against `q` (consuming its letters once each) is what keeps it a
 * necessary condition rather than a loose heuristic.
 */
function sharesEnoughChars(
  q: string,
  qCounts: ReadonlyMap<string, number>,
  cCounts: ReadonlyMap<string, number>,
  budget: number,
): boolean {
  const need = q.length - budget
  if (need <= 0) return true
  let covered = 0
  for (const [ch, want] of qCounts) {
    const have = cCounts.get(ch)
    if (have === undefined) continue
    covered += Math.min(want, have)
    if (covered >= need) return true
  }
  return covered >= need
}

/**
 * filterFuzzy over a prebuilt index: the ids that match, best-ranked first and
 * capped at `limit`.
 *
 * Same contract as filterFuzzy, but the catalog is indexed once by
 * buildFuzzyIndex instead of re-lowercased and re-stripped per keystroke, and
 * the edit-distance pass is gated by sharesEnoughChars. Use it wherever one list
 * is filtered repeatedly as the operator types (the bulk-arm model picker); use
 * filterFuzzy for one-shot calls over a short list.
 */
export function filterFuzzyIndexed(
  query: string,
  index: readonly FuzzyIndexEntry[],
  limit = 200,
): string[] {
  const raw = query.trim()
  if (!raw) {
    const n = Math.min(limit, index.length)
    const out: string[] = new Array(n)
    for (let i = 0; i < n; i++) out[i] = index[i].id
    return out
  }
  const lowerQ = raw.toLowerCase()
  const q = bare(raw)
  // A query with no letters or digits (a lone "/" or "—-") cannot match on its
  // bare form, but its literal characters can still be a substring of an id, so
  // the exact test still runs. Only the bare-form tests are skipped.
  if (!q) {
    const hits: string[] = []
    for (const row of index) {
      if (row.lower.includes(lowerQ)) hits.push(row.id)
    }
    return hits.slice(0, limit)
  }
  const budget = editBudget(q)
  const tooLong = q.length > 24
  const qCounts = new Map<string, number>()
  for (const ch of q) qCounts.set(ch, (qCounts.get(ch) ?? 0) + 1)
  const hits: Array<{ id: string; rank: number }> = []
  for (const row of index) {
    // The same three tests fuzzyMatch applies, in the same order, against the
    // forms the index already computed. The edit-distance test is the fallback,
    // skipped for a long query, exactly as before, and gated by the multiset
    // check so most non-matching ids never run the window loop.
    let rank = -1
    if (row.lower.includes(lowerQ)) {
      rank = 0
    } else if (row.bare.includes(q)) {
      rank = 1
    } else if (
      !tooLong &&
      sharesEnoughChars(q, qCounts, row.charCounts, budget) &&
      windowedHit(q, row.bare, budget)
    ) {
      rank = 2
    }
    if (rank >= 0) hits.push({ id: row.id, rank })
  }
  // A single pass sorted by rank: Array.prototype.sort is stable, so equal ranks
  // keep catalog order and the list does not reshuffle per keypress.
  return hits
    .sort((a, b) => a.rank - b.rank)
    .map((h) => h.id)
    .slice(0, limit)
}