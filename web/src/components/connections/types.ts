import { api, getAuthHeaders, type ProviderConnection } from '../../api/client'
import { parseCustomModelsResponse, parseDisabledModelsMap } from '../../lib/customModels'
import { getModelCaps, getModelKind } from '../../lib/models'
import { PROVIDER_CATALOG, PROVIDER_CATALOG_MAP } from '../../lib/providers'

export const MEDIA_KINDS: Record<string, true> = {
  image: true,
  tts: true,
  stt: true,
  embedding: true,
  video: true,
}

export function isChatModel(m: unknown): boolean {
  const kind = getModelKind(m)
  if (!kind || kind === 'llm' || kind === 'chat') {
    const obj = typeof m === 'object' && m !== null ? (m as { kind?: string; type?: string }) : null
    return !(obj?.kind && MEDIA_KINDS[obj.kind]) && !(obj?.type && MEDIA_KINDS[obj.type])
  }
  return false
}

export interface ProviderStats {
  total: number
  connected: number
  errorCount: number
  allDisabled: boolean
  latestError?: string | null
  connections: ProviderConnection[]
}

export interface ModelItem {
  id: string
  name?: string
  isCustom?: boolean
  caps: { vision: boolean; reasoning: boolean }
  kind?: string
}

export interface CustomModelData {
  id: string
  name?: string
  providerAlias?: string
  type?: string
  /** Capability flags saved from the Add Custom Model modal (upstream parity). */
  caps?: { vision?: boolean; reasoning?: boolean }
}

export function getIconPath(id?: string | null, apiType?: string): string {
  if (!id) return '/providers/oai-cc.png'
  const clean = id.trim()
  if (clean.startsWith('openai-compatible')) {
    return apiType === 'responses' ? '/providers/oai-r.png' : '/providers/oai-cc.png'
  }
  if (clean.startsWith('anthropic-compatible') || clean.includes('anthropic')) {
    return '/providers/anthropic-m.png'
  }
  if (PROVIDER_CATALOG_MAP.has(clean)) {
    return `/providers/${clean}.png`
  }
  const byAlias = PROVIDER_CATALOG.find((p) => p.alias === clean)
  if (byAlias) {
    return `/providers/${byAlias.id}.png`
  }
  return apiType === 'responses' ? '/providers/oai-r.png' : '/providers/oai-cc.png'
}

// Provider initials badge, used when a provider has no shipped PNG. The
// catalog already carries a brand colour for every entry, so the badge is drawn
// in the provider's own colour rather than a neutral grey — the tile reads as
// intentional instead of as a failed image load.
export function getProviderGlyph(
  id?: string | null
): { name: string; color: string; initials: string } {
  const clean = id?.trim()
  const entry = clean
    ? (PROVIDER_CATALOG_MAP.get(clean) ?? PROVIDER_CATALOG.find((p) => p.alias === clean))
    : undefined
  const name = entry?.name ?? clean ?? 'OpenAI'
  // Take the initials of the leading words, so "B.AI" reads "BA" rather than
  // collapsing to a single letter, and "Dahl Inference" reads "DI".
  const words = name.split(/[\s._/-]+/).filter(Boolean)
  const initials = (words.length > 1 ? words[0][0] + words[1][0] : words[0]?.slice(0, 2) ?? '?').toUpperCase()
  return { name, color: entry?.color ?? '#6B7280', initials }
}

export function getProviderStats(
  connections: ProviderConnection[],
  providerId: string,
  authTypes?: string[]
): ProviderStats {
  const list = connections.filter((c) => {
    if (c.provider !== providerId) return false
    if (authTypes && authTypes.length > 0) {
      return authTypes.includes(c.authType)
    }
    return true
  })

  const total = list.length
  const isConnError = (c: ProviderConnection) =>
    c.testStatus === 'error' || c.testStatus === 'failed' || (c.testStatus !== 'active' && c.testStatus !== 'passed' && !!c.lastError)
  const isConnConnected = (c: ProviderConnection) =>
    c.isActive === 1 && (c.testStatus === 'active' || c.testStatus === 'passed' || (c.testStatus !== 'error' && c.testStatus !== 'failed' && !c.lastError))

  const connected = list.filter(isConnConnected).length
  const errorCount = list.filter(isConnError).length
  const allDisabled = total > 0 && list.every((c) => c.isActive === 0)
  const latestError = list.find(isConnError)?.lastError

  return { total, connected, errorCount, allDisabled, latestError, connections: list }
}

export function matchesFilter(stats: ProviderStats, statusFilter: string, noAuth?: boolean): boolean {
  if (statusFilter === 'all') return true
  if (statusFilter === 'connected') return stats.connected > 0 || (!!noAuth && stats.total === 0)
  if (statusFilter === 'error') return stats.errorCount > 0
  if (statusFilter === 'disabled') return stats.allDisabled
  if (statusFilter === 'not_connected') return stats.total === 0 && !noAuth
  return true
}

// matchesSearch matches a provider by display name, id, and alias.
// Matching the name alone made providers unsearchable by their own identifier:
// typing "bai" returned "Baidu Qianfan" (a substring collision) but never "B.AI",
// and "atria-asi" / "tokenharbor" returned nothing at all — the user then
// concluded the provider was missing and built a custom node instead.
export function matchesSearch(
  name: string,
  searchQuery: string,
  ...identifiers: (string | null | undefined)[]
): boolean {
  const q = searchQuery.trim().toLowerCase()
  if (!q) return true
  return [name, ...identifiers].some((v) => v && v.toLowerCase().includes(q))
}

export async function fetchProviderModelsData(
  providerId: string,
  storageAlias: string
): Promise<{ customModels: CustomModelData[]; disabledModelIds: string[] }> {
  try {
    const [customs, disabled] = await Promise.all([
      api.getCustomModels().catch(() => ({})),
      api.getDisabledModels().catch(() => ({})),
    ])
    // Upstream parity: GET /api/models/custom -> { models: [...] }.
    const customModels = parseCustomModelsResponse(customs) as CustomModelData[]
    // Upstream parity: disabled keyed by storage alias OR providerId.
    const disMap = parseDisabledModelsMap(disabled)
    const disArr = disMap[storageAlias] || disMap[providerId] || []
    return {
      customModels,
      disabledModelIds: Array.isArray(disArr) ? disArr : [],
    }
  } catch {
    return { customModels: [], disabledModelIds: [] }
  }
}

export interface SuggestedModel {
  id: string
  name: string
  contextLength?: number
}

// In-memory cache for suggested-model catalogs (upstream parity:
// providerModelsFetcher.js CACHE_TTL_MS).
const suggestedCache = new Map<string, { data: SuggestedModel[]; expiresAt: number }>()
const SUGGESTED_CACHE_TTL_MS = 5 * 60 * 1000

/** Port of fetchSuggestedModels (upstream providerModelsFetcher.js). */
export async function fetchSuggestedModels(fetcher: {
  url: string
  type: string
}): Promise<SuggestedModel[]> {
  if (!fetcher?.url || !fetcher?.type) return []
  const cached = suggestedCache.get(fetcher.url)
  if (cached && Date.now() < cached.expiresAt) return cached.data
  try {
    const params = new URLSearchParams({ url: fetcher.url, type: fetcher.type })
    const res = await fetch(`/api/providers/suggested-models?${params}`, {
      headers: getAuthHeaders(),
    })
    if (!res.ok) return []
    const json = await res.json()
    const data = Array.isArray(json?.data) ? json.data : []
    suggestedCache.set(fetcher.url, { data, expiresAt: Date.now() + SUGGESTED_CACHE_TTL_MS })
    return data
  } catch {
    return []
  }
}

/**
 * Catalog rows first, custom models appended after — upstream keeps the registry
 * list in its own order and treats custom models as additions to it.
 */
export function buildAvailableModels(
  builtInModels: Array<{ id: string; name?: string; kind?: string; type?: string }>,
  providerCustomModels: CustomModelData[]
): ModelItem[] {
  const list: ModelItem[] = []
  const seen = new Set<string>()

  for (const bm of builtInModels) {
    if (!bm.id || seen.has(bm.id)) continue
    seen.add(bm.id)
    list.push({
      id: bm.id,
      name: bm.name,
      isCustom: false,
      caps: getModelCaps(bm.id, bm),
      kind: getModelKind(bm),
    })
  }
  for (const cm of providerCustomModels) {
    if (!cm.id || seen.has(cm.id)) continue
    seen.add(cm.id)
    list.push({
      id: cm.id,
      name: cm.name || cm.id,
      isCustom: true,
      caps: getModelCaps(cm.id, cm),
      kind: getModelKind(cm),
    })
  }
  return list
}
