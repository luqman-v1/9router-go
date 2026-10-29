/**
 * Model filter rules for the jailbreak presets — the browser-side twin of
 * internal/db/jailbreak_keywords.go. The gateway resolves injection with the Go
 * implementation; this one exists so the dashboard can validate a rule and
 * preview its reach before the operator saves it.
 *
 * Three modes, shared by the include and the exclude lists:
 *
 *   word     the rule is a whole token or a run of tokens. "deepseek" matches
 *            "providerx/deepseek-4-pro" and "deepseek-v4" but not "deepseekia".
 *   partial  a case-insensitive substring. "seek" matches "deepseek".
 *   regex    an RE2 pattern on the server, matched unanchored and
 *            case-insensitively unless the pattern opts out with (?-i).
 *
 * Go's RE2 and JavaScript's regexp agree on the constructs an operator uses
 * here (literals, classes, alternation, anchors, quantifiers). A pattern that
 * relies on an engine-specific feature can behave differently in the preview
 * than on the gateway; the gateway is always the authority.
 */

export interface JailbreakKeywordRule {
  text: string
  mode?: string
}

export const JAILBREAK_KEYWORD_WORD = ''
export const JAILBREAK_KEYWORD_PARTIAL = 'partial'
export const JAILBREAK_KEYWORD_REGEX = 'regex'

/** Bounds mirrored from the Go side, so the UI refuses what the API would. */
export const MAX_JAILBREAK_KEYWORDS = 20
export const MAX_JAILBREAK_KEYWORD_CHARS = 200

/** Characters a word-mode rule may sit between. */
const DELIMITERS = '/-_:. '

function wordMatch(id: string, needle: string): boolean {
  const haystack = id.toLowerCase()
  const kw = needle.toLowerCase()
  if (kw === '') return false
  let from = 0
  while (from <= haystack.length - kw.length) {
    const at = haystack.indexOf(kw, from)
    if (at < 0) return false
    const end = at + kw.length
    const leftOk = at === 0 || DELIMITERS.includes(haystack[at - 1])
    const rightOk = end === haystack.length || DELIMITERS.includes(haystack[end])
    if (leftOk && rightOk) return true
    from = at + 1
  }
  return false
}

/**
 * Compiles a regex rule the way the gateway does: case-insensitive by default,
 * with a leading (?-i) opting out. Returns null for a pattern this engine
 * rejects, which the caller treats as "does not match".
 */
function compileRegex(text: string): RegExp | null {
  let pattern = text
  let flags = 'i'
  if (pattern.startsWith('(?-i)')) {
    pattern = pattern.slice('(?-i)'.length)
    flags = ''
  }
  try {
    return new RegExp(pattern, flags)
  } catch {
    return null
  }
}

/** Reports whether id satisfies one rule. Every mode is case-insensitive. */
export function matchJailbreakKeyword(id: string, rule: JailbreakKeywordRule): boolean {
  if (!id || !rule.text) return false
  switch (rule.mode) {
    case JAILBREAK_KEYWORD_PARTIAL:
      return id.toLowerCase().includes(rule.text.toLowerCase())
    case JAILBREAK_KEYWORD_REGEX: {
      const re = compileRegex(rule.text)
      return re !== null && re.test(id)
    }
    default:
      return wordMatch(id, rule.text)
  }
}

/** Reports whether id satisfies any rule in the list. */
export function matchesAnyJailbreakKeyword(id: string, rules: JailbreakKeywordRule[]): boolean {
  return rules.some((rule) => matchJailbreakKeyword(id, rule))
}

export type NormalizeResult =
  | { ok: true; rules: JailbreakKeywordRule[] }
  | { ok: false; error: string }

/**
 * Validates and canonicalises a rule list the way the gateway does, so the
 * dashboard can show the same rejection before the request is sent. Word and
 * partial rules are lowercased; a regex is kept verbatim because lowercasing
 * would corrupt escapes and character classes.
 */
export function normalizeJailbreakKeywords(input: JailbreakKeywordRule[]): NormalizeResult {
  if (!input || input.length === 0) return { ok: true, rules: [] }
  if (input.length > MAX_JAILBREAK_KEYWORDS) {
    return { ok: false, error: `too many rules: ${input.length} (max ${MAX_JAILBREAK_KEYWORDS})` }
  }
  const out: JailbreakKeywordRule[] = []
  const seen = new Set<string>()
  for (const rule of input) {
    const text = (rule.text || '').trim()
    if (text === '') return { ok: false, error: 'rule text is empty' }
    if (text.length > MAX_JAILBREAK_KEYWORD_CHARS) {
      return {
        ok: false,
        error: `rule "${text}" is too long: ${text.length} chars (max ${MAX_JAILBREAK_KEYWORD_CHARS})`,
      }
    }
    const mode = rule.mode || JAILBREAK_KEYWORD_WORD
    let finalText = text
    if (mode === JAILBREAK_KEYWORD_WORD || mode === JAILBREAK_KEYWORD_PARTIAL) {
      finalText = text.toLowerCase()
    } else if (mode === JAILBREAK_KEYWORD_REGEX) {
      if (compileRegex(text) === null) {
        return { ok: false, error: `invalid regex: ${text}` }
      }
    } else {
      return { ok: false, error: `unknown mode "${mode}" (want word, partial or regex)` }
    }
    const key = `${mode}\u0000${finalText}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push({ text: finalText, mode })
  }
  return { ok: true, rules: out }
}
