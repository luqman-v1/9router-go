/**
 * Provider share link — the codec behind `/v1/add#…`.
 *
 * One URL carries a compatible provider node (endpoint, credential, model
 * selection) so a receiver can stand the same provider up on another instance.
 * The credential rides in the *fragment*, which an HTTP client never sends to
 * the server: it therefore never reaches an access log, a proxy log or a
 * Referer. Nothing here may move a credential into the query string.
 *
 * The parameter contract is docs/superpowers/specs/2026-11-01-provider-share-url-design.md §5.
 * Deliberately dependency-free so the unit tests run without a DOM.
 */

export interface KeyEntry {
  name: string
  key: string
  pool?: string
  prio?: number
}

/** A keyword, a filter object, or an explicit id list. */
export type ModelSelection = 'auto' | 'ask' | 'none' | 'free' | ModelFilter | string[]

export interface ModelFilter {
  caps?: string
  ctxAtLeast?: number
  limit?: number
}

export interface ProviderShareLink {
  prov: string
  url: string
  /** Single-key form. Mutually exclusive with `keys`. */
  api: string[]
  /** Bulk-credential form. Mutually exclusive with `api`. */
  keys: KeyEntry[]
  models: ModelSelection
  prefix: string
  name: string
  /** One line of sharer context, ≤200 chars, rendered read-only and escaped. */
  note: string
  type: string
  apiType: string
}

/** The three length tiers from spec §5.7. */
export const SHARE_LINK_TIERS = { splitAt: 2000, refuseAt: 4000 } as const
export type ShareLinkTier = 'link' | 'split' | 'refuse'

/** The `set=` caps from spec §5.6. */
export const SET_CAPS = { maxEntries: 8, maxChars: 4000 } as const

const MODEL_KEYWORDS = ['auto', 'ask', 'none', 'free'] as const
const MAX_NOTE_CHARS = 200

/** Mirrors providerNodeLiteralsForID (internal/handlers/dashboard/provider_node_id.go). */
export function deriveNodeType(prov: string): { type: string; apiType: string } {
  if (prov.startsWith('anthropic-compatible-')) return { type: 'anthropic-compatible', apiType: '' }
  if (prov.startsWith('custom-embedding-')) return { type: 'custom-embedding', apiType: '' }
  if (prov.startsWith('openai-compatible-responses-')) return { type: 'openai-compatible', apiType: 'responses' }
  if (prov.startsWith('openai-compatible-chat-')) return { type: 'openai-compatible', apiType: 'chat' }
  return { type: 'openai-compatible', apiType: 'chat' }
}

function isHTTPURL(raw: string): boolean {
  try {
    const u = new URL(raw)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

// ---------------------------------------------------------------------------
// models=
// ---------------------------------------------------------------------------

/** Parses the context threshold: "128k" → 131072, "1m" → 1048576. */
export function parseContextThreshold(raw: string): number | null {
  const m = /^(\d+(?:\.\d+)?)\s*([km]?)$/i.exec(raw.trim())
  if (!m) return null
  const n = Number.parseFloat(m[1])
  if (!Number.isFinite(n) || n <= 0) return null
  const unit = m[2].toLowerCase()
  return unit === 'k' ? Math.round(n * 1024) : unit === 'm' ? Math.round(n * 1024 * 1024) : Math.round(n)
}

function parseModels(raw: string): ModelSelection {
  const value = raw.trim()
  if (!value) return 'none'
  const lower = value.toLowerCase()
  if (MODEL_KEYWORDS.some((k) => k === lower)) return lower as (typeof MODEL_KEYWORDS)[number]
  if (lower.startsWith('caps:')) {
    const caps = value.slice(5).trim()
    return caps ? { caps } : 'ask'
  }
  if (/^ctx\s*>=/i.test(value)) {
    const threshold = parseContextThreshold(value.replace(/^ctx\s*>=/i, ''))
    // A malformed threshold falls back to ask: an unparseable filter that
    // silently became "import nothing" would look like an upstream with no
    // models, which is a different and much more confusing failure.
    return threshold === null ? 'ask' : { ctxAtLeast: threshold }
  }
  if (/^limit\s*=/i.test(value)) {
    const limit = Number.parseInt(value.replace(/^limit\s*=/i, ''), 10)
    return Number.isFinite(limit) && limit > 0 ? { limit } : 'ask'
  }
  const ids = value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)
  return ids.length ? ids : 'none'
}

function serializeModels(models: ModelSelection): string {
  if (Array.isArray(models)) return models.join(',')
  if (typeof models === 'string') return models === 'none' ? '' : models
  if (models.caps) return `caps:${models.caps}`
  if (models.ctxAtLeast) return `ctx>=${models.ctxAtLeast}`
  if (models.limit) return `limit=${models.limit}`
  return ''
}

// ---------------------------------------------------------------------------
// keys=
// ---------------------------------------------------------------------------

/**
 * Serialises a credential block: `;`-separated entries, `|`-separated fields.
 *
 * The delimiters are written literally — `URLSearchParams.toString()` already
 * percent-encodes `;` and `|` on the way into the URL and decodes them on the
 * way out, so an extra escape here would survive as a literal `%7C` in the
 * value the parser receives.
 */
export function encodeKeysBlock(entries: KeyEntry[]): string {
  return entries
    .map((e) =>
      [
        e.name,
        e.key,
        ...(e.pool ? [`pool=${e.pool}`] : []),
        ...(e.prio ? [`prio=${e.prio}`] : []),
      ].join('|')
    )
    .join(';')
}

/**
 * Parses a `keys=` block. `URLSearchParams` has already percent-decoded the
 * value, so a delimiter that survives decoding is one the encoder never
 * produced — a key that genuinely contains `;` or `|`. Refusing it names the
 * offending value instead of truncating it silently.
 */
export function parseKeysBlock(raw: string): KeyEntry[] {
  const out: KeyEntry[] = []
  for (const entry of raw.split(';')) {
    if (!entry.trim()) continue
    const fields = entry.split('|')
    if (fields.length < 1 || fields.length > 4) {
      throw new Error(
        `share link: credential entry "${entry}" has ${fields.length} fields, expected 1 to 4 (name|key[|pool=…][|prio=…])`
      )
    }
    const name = fields[0].trim()
    const key = fields[1]?.trim() ?? ''
    if (!key) {
      // Reached in two different ways: an entry that really has no key, and an
      // entry whose key contained ';' or '|' and so got split in two. The parser
      // cannot tell them apart after the split, so the message names both rather
      // than guessing — an operator holding a cookie credential with a ';' in it
      // needs to know that is why the link was refused.
      throw new Error(
        `share link: credential entry "${entry}" has no key — a key containing ";" or "|" is split by the keys= block and cannot be carried in a share link`
      )
    }
    if (key.includes(';') || key.includes('|')) {
      throw new Error(
        `share link: credential "${key}" contains ";" or "|", which the keys= block uses as separators — it cannot be carried in a share link`
      )
    }
    const parsed: KeyEntry = { name, key }
    for (const extra of fields.slice(2)) {
      const [k, v] = extra.split('=')
      if (k === 'pool' && v) parsed.pool = v.trim()
      if (k === 'prio' && v && Number.isFinite(Number.parseInt(v, 10))) {
        parsed.prio = Number.parseInt(v, 10)
      }
    }
    out.push(parsed)
  }
  return out
}

// ---------------------------------------------------------------------------
// set=
// ---------------------------------------------------------------------------

function toBase64Url(json: string): string {
  const bytes = new TextEncoder().encode(json)
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function fromBase64Url(encoded: string): string {
  const padded = encoded.replace(/-/g, '+').replace(/_/g, '/')
  const binary = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4))
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

export function encodeSetPayload(links: ProviderShareLink[]): string {
  if (links.length > SET_CAPS.maxEntries) {
    throw new Error(`share link: a set may carry at most ${SET_CAPS.maxEntries} providers`)
  }
  const encoded = toBase64Url(JSON.stringify(links))
  if (encoded.length > SET_CAPS.maxChars) {
    throw new Error(
      `share link: this set encodes to ${encoded.length} characters, over the ${SET_CAPS.maxChars} limit — export the credentials separately and share fewer providers`
    )
  }
  return encoded
}

export function decodeSetPayload(encoded: string): ProviderShareLink[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(fromBase64Url(encoded))
  } catch {
    throw new Error('share link: the set= payload is not valid base64url JSON')
  }
  if (!Array.isArray(parsed)) throw new Error('share link: the set= payload is not a list')
  // The same two caps encodeSetPayload enforces, re-checked here. A link is
  // untrusted input: a hand-edited or truncated payload could otherwise carry
  // fifty providers into a modal that renders fifty forms and then writes
  // fifty nodes, and the operator would have no signal that anything was wrong.
  if (parsed.length > SET_CAPS.maxEntries) {
    throw new Error(
      `share link: the set= payload carries ${parsed.length} providers, over the ${SET_CAPS.maxEntries} limit`
    )
  }
  if (encoded.length > SET_CAPS.maxChars) {
    throw new Error(
      `share link: the set= payload is ${encoded.length} characters, over the ${SET_CAPS.maxChars} limit`
    )
  }
  return parsed.map((entry) => normalizeLink(entry as Record<string, unknown>))
}

// ---------------------------------------------------------------------------
// parse / build
// ---------------------------------------------------------------------------

function normalizeLink(raw: Record<string, unknown>): ProviderShareLink {
  const prov = String(raw.prov ?? '').trim()
  const { type, apiType } = deriveNodeType(prov)
  const api = Array.isArray(raw.api)
    ? raw.api.map((v) => String(v).trim()).filter(Boolean)
    : String(raw.api ?? '').split(',').map((s) => s.trim()).filter(Boolean)
  const keys = Array.isArray(raw.keys)
    ? (raw.keys as KeyEntry[]).map((k) => ({ ...k, name: String(k.name ?? ''), key: String(k.key ?? '') }))
    : []
  return {
    prov,
    url: isHTTPURL(String(raw.url ?? '')) ? String(raw.url ?? '') : '',
    api,
    keys,
    models: typeof raw.models === 'string' ? parseModels(raw.models) : ((raw.models as ModelSelection) ?? 'none'),
    prefix: String(raw.prefix ?? ''),
    name: String(raw.name ?? ''),
    note: String(raw.note ?? '').slice(0, MAX_NOTE_CHARS),
    type: String(raw.type ?? type),
    apiType: String(raw.apiType ?? apiType),
  }
}

/**
 * Parses a share link. Accepts either a bare fragment (`#prov=…`) or a whole
 * URL (`https://host/add#prov=…`) — a caller holding the string it just copied
 * should not have to slice it first.
 *
 * Returns `null` when it is not a share link at all, a single link, or an array
 * for a `set=` link. Throws for a share link that is malformed — a half-parsed
 * link that silently drops a credential would be worse than a loud failure.
 */
export function parseProviderShareHash(input: string): ProviderShareLink | ProviderShareLink[] | null {
  if (!input) return null
  // A full URL carries its fragment after '#'; a bare fragment starts with it.
  // Everything before it is origin + path, which the parser does not read.
  const hashStart = input.indexOf('#')
  const raw = (hashStart >= 0 ? input.slice(hashStart + 1) : input).replace(/^\?/, '')
  const params = new URLSearchParams(raw)
  const prov = (params.get('prov') || '').trim()
  const set = (params.get('set') || '').trim()
  if (!prov && !set) return null
  if (prov && set) {
    throw new Error('share link: prov= and set= are mutually exclusive — carry one provider or a set, not both')
  }
  if (set) return decodeSetPayload(set)

  const apiRaw = params.get('api')
  const keysRaw = params.get('keys')
  if (apiRaw !== null && keysRaw !== null) {
    throw new Error('share link: api= and keys= are mutually exclusive — use api= for one key and keys= for a bulk block')
  }
  return normalizeLink({
    prov,
    url: params.get('url'),
    api: apiRaw ?? '',
    keys: keysRaw ? parseKeysBlock(keysRaw) : [],
    models: params.get('models') ?? 'none',
    prefix: params.get('prefix') ?? '',
    name: params.get('name') ?? '',
    note: params.get('note') ?? '',
  })
}

export function buildProviderShareURL(origin: string, link: ProviderShareLink): string {
  if (!link.prov) throw new Error('share link: prov is required')
  if (link.api.length && link.keys.length) {
    throw new Error('share link: api= and keys= are mutually exclusive')
  }
  const params = new URLSearchParams()
  params.set('prov', link.prov)
  if (link.url) params.set('url', link.url)
  if (link.keys.length) {
    params.set('keys', encodeKeysBlock(link.keys))
  } else if (link.api.length) {
    params.set('api', link.api.join(','))
  }
  const models = serializeModels(link.models)
  if (models) params.set('models', models)
  if (link.prefix) params.set('prefix', link.prefix)
  if (link.name) params.set('name', link.name)
  if (link.note) params.set('note', link.note.trim().slice(0, MAX_NOTE_CHARS))
  return `${origin.replace(/\/+$/, '')}/add#${params.toString()}`
}

/** Which of the three length responses a built link calls for (spec §5.7). */
export function shareLinkTier(urlLength: number): ShareLinkTier {
  if (urlLength > SHARE_LINK_TIERS.refuseAt) return 'refuse'
  if (urlLength > SHARE_LINK_TIERS.splitAt) return 'split'
  return 'link'
}

/** A catalogue entry as GET /api/providers/{id}/models returns it. */
export interface CatalogModel {
  id: string
  name?: string
  contextLength?: number
  capabilities?: Record<string, unknown>
}

/**
 * Applies the `models=` filters to a fetched catalogue. Runs after
 * GET /api/providers/{id}/models, so it needs no backend change.
 *
 * `caps` keeps an entry that declares no capabilities at all: "no data" is not
 * "no capability", and dropping such a row would hide a model the receiver can
 * use.
 */
export function applyModelFilters(models: CatalogModel[], selection: ModelSelection): CatalogModel[] {
  if (typeof selection !== 'object' || Array.isArray(selection)) return models
  let out = models
  if (selection.caps) {
    const wanted = selection.caps
    out = out.filter((m) => m.capabilities === undefined || Object.keys(m.capabilities).includes(wanted))
  }
  if (selection.ctxAtLeast) {
    const floor = selection.ctxAtLeast
    out = out.filter((m) => (m.contextLength ?? 0) >= floor)
  }
  if (selection.limit) out = out.slice(0, selection.limit)
  return out
}
