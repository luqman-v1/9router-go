// The provider catalog is served by GET /api/providers/catalog, which reads the
// Go registry in internal/providers. The dashboard used to ship a second copy
// of this data as a TypeScript literal, so a provider added to one and not the
// other either vanished from the UI or rendered as a bare id. One source of
// truth: the registry the data plane already resolves providers from.

export interface ProviderNotice {
  text?: string
  apiKeyUrl?: string
  signupUrl?: string
}

export interface ProviderRegion {
  id: string
  label: string
}

export interface ProviderCatalogItem {
  id: string
  name: string
  category: 'oauth' | 'free' | 'freeTier' | 'apikey' | 'webCookie' | 'custom'
  alias?: string
  /**
   * The prefix this provider's model ids are published under — what
   * /v1/models serves and what the copy button hands out. It is the
   * gateway's own answer, so it always round-trips. It differs from
   * `alias` for a few providers (`freebuff` publishes as "freebuff", its
   * upstream alias is "fb"), so the two are not interchangeable.
   */
  modelPrefix?: string
  color?: string
  icon?: string
  noAuth?: boolean
  priority?: number
  mediaPriority?: number
  hiddenKinds?: string[]
  serviceKinds?: string[]
  /** auth modes from upstream registry (e.g. clinepass ["apikey","oauth"] = dual buttons). */
  authModes?: string[]
  /** hidden from provider list (upstream parity) but detail page stays reachable. */
  hidden?: boolean
  /** Region choices for cluster-specific providers (upstream registry `regions`). */
  regions?: ProviderRegion[]
  defaultRegion?: string
  /** Upstream display.website + display.notice (signup/apiKey links). */
  website?: string
  notice?: ProviderNotice
  /** Upstream registry authType/authHint (cookie login hint, apikey/oauth/none markers). */
  authType?: string
  authHint?: string
  /** Upstream registry modelsFetcher (public catalog for "Suggested free models"). */
  modelsFetcher?: { url: string; type: string }
  /** Upstream registry systemoneConfig */
  systemoneConfig?: { baseUrl?: string; format?: string; headers?: Record<string, string> }
  /** Upstream registry passthroughModels: unknown ids are accepted unvalidated. */
  passthroughModels?: boolean
  /** Upstream registry credentialForm: which credential form the dashboard renders. */
  credentialForm?: string
  /**
   * Upstream registry apiKeyOptionalWith: the providerSpecificData field that stands in
   * for an API key, so the create and validate routes accept an empty key rather than
   * rejecting a documented setup.
   */
  apiKeyOptionalWith?: string
  /** Upstream registry features.usage: the quota tracker can read this provider. */
  usage?: boolean
  searchConfig?: Record<string, unknown>
  fetchConfig?: Record<string, unknown>
  searchViaChat?: {
    defaultModel?: string
    endpoint?: string
    pricingUrl?: string
    freeTier?: string
  }
}

export interface ProviderCatalogSection {
  key: string
  label: string
  count: number
  providers: ProviderCatalogItem[]
}

export interface ProviderCatalog {
  sections: ProviderCatalogSection[]
  noAuth: ProviderCatalogItem[]
  kinds: Record<string, string>
  categories: string[]
  /**
   * Every provider id and resolution alias a provider node may not claim as its
   * prefix. Served by the guard itself so a suggested prefix cannot be one the
   * server answers 409 for; the sections above do not cover this set.
   */
  reservedPrefixes?: string[]
}

export const PROVIDER_CATEGORIES = [
  { id: 'custom', label: 'Compatible Endpoints' },
  { id: 'oauth', label: 'OAuth Providers' },
  { id: 'free', label: 'Free Providers (No Key)' },
  { id: 'freeTier', label: 'Free Tier Providers' },
  { id: 'apikey', label: 'API Key Providers' },
  { id: 'webCookie', label: 'Web Cookie Providers' },
] as const

// ---------------------------------------------------------------------------
// Boot-time load
//
// The catalog is fetched once, before the app renders, and consumers read it
// synchronously afterwards. That is deliberate: the catalog is consulted during
// render (a card's icon, a picker's list), so an async read would make every
// consumer either a loading state or a race.
//
// The two live objects are mutated in place rather than replaced, because
// consumers captured the reference at import time. Replacing them would leave
// those consumers reading the empty array.
// ---------------------------------------------------------------------------


export const PROVIDER_CATALOG: ProviderCatalogItem[] = []
export const PROVIDER_CATALOG_MAP = new Map<string, ProviderCatalogItem>()
let noAuthProviders: ProviderCatalogItem[] = []
/** Provider ids and aliases a node may not claim as a prefix (server guard set). */
let reservedPrefixes: string[] = []
/** `reservedPrefixes` as a set, built on first use (the dialog asks per keystroke). */
let reservedPrefixSet: Set<string> | null = null
let loaded = false
/** Resolves when the catalog has been fetched. Rejects on a failed fetch. */
let inflight: Promise<void> | null = null

export async function loadProviderCatalog(): Promise<void> {
  if (loaded) return
  if (inflight) return inflight
  inflight = (async () => {
    const res = await fetch('/api/providers/catalog')
    if (!res.ok) throw new Error(`provider catalog: HTTP ${res.status}`)
    const data = (await res.json()) as ProviderCatalog
    const seenAlias = new Set<string>()
    PROVIDER_CATALOG.length = 0
    for (const section of data.sections ?? []) {
      for (const p of section.providers ?? []) {
        // An entry whose id is another entry's alias is dropped: `mmf` resolves
        // to `mimo-free`, and serving both offers the same provider twice in
        // every picker.
        if (p.alias && p.alias !== p.id && seenAlias.has(p.alias)) continue
        if (p.alias && p.alias !== p.id) seenAlias.add(p.alias)
        PROVIDER_CATALOG.push(p)
      }
    }
    noAuthProviders = data.noAuth ?? []
    reservedPrefixes = data.reservedPrefixes ?? []
    reservedPrefixSet = null
    PROVIDER_CATALOG_MAP.clear()
    for (const p of PROVIDER_CATALOG) PROVIDER_CATALOG_MAP.set(p.id, p)
    loaded = true
  })()
  return inflight
}

/** Test seam: install a catalog without a network round trip. */
export function __setProviderCatalogForTest(
  items: ProviderCatalogItem[],
  noAuth: ProviderCatalogItem[] = [],
  reserved: string[] = [],
): void {
  PROVIDER_CATALOG.length = 0
  for (const p of items) PROVIDER_CATALOG.push(p)
  noAuthProviders = noAuth
  reservedPrefixes = reserved
  reservedPrefixSet = null
  PROVIDER_CATALOG_MAP.clear()
  for (const p of PROVIDER_CATALOG) PROVIDER_CATALOG_MAP.set(p.id, p)
  loaded = true
}

/** True once the catalog has been fetched (or injected). */
export function isProviderCatalogLoaded(): boolean {
  return loaded
}

/**
 * The set of prefixes a provider node may not claim, or an empty set before the
 * catalog has loaded.
 *
 * Membership is what the server's conflict guard checks, so a prefix suggested
 * from this set is one the create/update will accept. Built lazily and kept
 * until the next load, because the dialog asks on every keystroke of the name
 * field while the set itself changes once per boot.
 */
export function getReservedPrefixes(): Set<string> {
  if (!reservedPrefixSet) reservedPrefixSet = new Set(reservedPrefixes)
  return reservedPrefixSet
}

export function getNoAuthProviders(): ProviderCatalogItem[] {
  return noAuthProviders
}

export function isChatProvider(p: ProviderCatalogItem): boolean {
  return (p.serviceKinds ?? ['llm']).includes('llm')
}

export function getProvidersByKind(kind: string): ProviderCatalogItem[] {
  return PROVIDER_CATALOG.filter((p) => {
    const kinds = p.serviceKinds ?? ['llm']
    if (!kinds.includes(kind)) return false
    if (p.hidden) return false
    if (p.hiddenKinds?.includes(kind)) return false
    return true
  }).sort((a, b) => ((a.priority ?? a.mediaPriority ?? 999) - (b.priority ?? b.mediaPriority ?? 999)))
}

export const MEDIA_PROVIDER_KINDS = [
  'embedding',
  'image',
  'tts',
  'stt',
  'video',
  'webSearch',
  'webFetch',
  'web',
] as const
