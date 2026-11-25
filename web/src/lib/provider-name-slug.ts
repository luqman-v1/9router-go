/**
 * Deriving a provider node's prefix and URL suffix from its display name.
 *
 * The add dialog fills both fields as the operator types a name, so a node
 * called "My OpenAI Prod" arrives with prefix `my-openai-prod` and the same
 * string as its URL suffix instead of two fields to invent. Both fields stay
 * editable and stop following the name the moment the operator types in them.
 *
 * Two bounds come from the server and are enforced here rather than discovered
 * through a 409:
 *
 * - A prefix has to stay short. `maxDerivedPrefixLength` in the Go fill helper
 *   is 24, and a prefix sits in front of every model id the node publishes, so
 *   a longer one produces ids worse than the node-id fallback the fill exists
 *   to replace.
 * - A prefix may not claim a registry id or alias. Resolution walks those
 *   first, so a node claiming `gh` answers every `gh/*` request from the wrong
 *   upstream with no error anywhere. The set is served by the catalog
 *   (`reservedPrefixes`) because it is not derivable from the catalog sections.
 */

/** Mirrors `maxDerivedPrefixLength` in internal/handlers/dashboard/provider_node_prefix.go. */
export const MAX_DERIVED_PREFIX_LENGTH = 24

/**
 * Mirrors `nodeIDSuffixPattern` in internal/handlers/dashboard/provider_node_id.go:
 * `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`.
 */
export const MAX_NODE_ID_SUFFIX_LENGTH = 64

/**
 * Slugifies a display name into the alphabet a prefix and a node-id suffix
 * share: lowercase letters, digits, and single `-` separators.
 *
 * Accents are folded rather than dropped, so "Café Interne" keeps its word
 * (`cafe-interne`) instead of collapsing to `caf-interne`, which is what a
 * plain ASCII filter would produce.
 */
export function slugifyProviderName(name: string): string {
  return name
    .normalize('NFD')
    // Strip combining marks left by NFD, keeping the base letter.
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    // Every separator-like run becomes one dash: whitespace, underscores and
    // dots are all things an operator types where a dash belongs.
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/**
 * The prefix to suggest for a name, or '' when the name yields nothing usable.
 *
 * A slug that a registry owns is refused rather than trimmed into something
 * unrelated: shortening `anthropic` to `anthrop` would silently publish under
 * a namespace nobody asked for. Returning '' leaves the field to the server's
 * own fill (URL suffix, then a random token), which is the same value an
 * operator gets by leaving it blank.
 */
export function suggestPrefixFromName(name: string, reserved: ReadonlySet<string> = new Set()): string {
  const slug = slugifyProviderName(name).slice(0, MAX_DERIVED_PREFIX_LENGTH).replace(/-+$/g, '')
  if (slug === '' || reserved.has(slug)) return ''
  return slug
}

/**
 * The URL suffix to suggest for a name, or '' when the name yields nothing.
 *
 * The suffix is not checked against the reserved set: a suffix only has to be
 * unique per composed node id (`openai-compatible-chat-<suffix>`), so
 * `anthropic` is a legal suffix even though it is an owned prefix. The server
 * skips it when deriving a prefix and falls through to its own fill, which is
 * why one name can produce a suffix with no matching prefix.
 */
export function suggestUrlSuffixFromName(name: string): string {
  return slugifyProviderName(name).slice(0, MAX_NODE_ID_SUFFIX_LENGTH).replace(/-+$/g, '')
}
