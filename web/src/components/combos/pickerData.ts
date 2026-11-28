import type { Combo, ProviderConnection, ProviderNode } from '../../api/client'
import { getModelCaps, getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
import { PROVIDER_CATALOG, isChatProvider } from '../../lib/providers'
import { parseSearchQuery, matchesSearch } from '../../lib/modelSearchFilter'

/**
 * Providers whose model list the picker reads live from the gateway instead of
 * the static registry bundle. These are the CLI-tool providers (claude, zed,
 * cursor, cline/clinepass, commandcode): their catalogues change upstream on a
 * cadence the snapshot cannot follow, and the backend already serves the live
 * list with the capability block resolved (GET /api/providers/{id}/models).
 */
export const LIVE_CATALOG_PROVIDERS = [
  'cursor',
  'cline',
  'clinepass',
  'zed',
  'claude',
  'commandcode',
] as const

export type LiveCatalogProvider = (typeof LIVE_CATALOG_PROVIDERS)[number]

/** One live catalogue entry, in the shape the gateway serves it. */
export interface LiveCatalogEntry {
  id?: string
  slug?: string
  model?: string
  name?: string
  contextLength?: number
  maxOutput?: number
  isVL?: boolean
  isReasoning?: boolean
  thinking_levels?: string[]
}

/** Reads a live entry's id under the three spellings the feeds use. */
export function liveEntryId(entry: unknown): string {
  if (typeof entry === 'string') return entry
  if (!entry || typeof entry !== 'object') return ''
  const obj = entry as LiveCatalogEntry
  return obj.id || obj.slug || obj.model || ''
}

/** Live entries of one provider as chat models, or null when it has none. */
function liveEntries(providerId: string, liveModels: PickerExtras['liveModels']): unknown[] | null {
  const entries = liveModels?.[providerId]
  if (!Array.isArray(entries) || entries.length === 0) return null
  return entries.filter((m) => isChatModel(m))
}

/** Minimal connection shape the live fetch needs: an id and its provider. */
interface LiveCatalogConnection {
  id: string
  provider?: string
  isActive?: number | boolean | null
}

/** One live-catalogue fetch: resolves to a connection's models, or [] on error. */
type ConnectionModelsFetcher = (connectionId: string) => Promise<{ models?: unknown[] } | null>

/**
 * Reads every active connection's live catalogue for the given providers,
 * flattens the per-connection lists, and dedupes by model id (keeping the
 * first occurrence). Mirrors upstream useLiveProviderModels
 * (ModelSelectModal.js): two connections of one provider collapse to one row
 * per model id because the picker value (alias/id) is identical. A provider
 * with no active connection is absent from the result, so the caller keeps the
 * static bundle as the fallback instead of emptying the group.
 */
export async function fetchLiveProviderModels(
  connections: LiveCatalogConnection[],
  providers: readonly string[],
  fetchModels: ConnectionModelsFetcher,
): Promise<Record<string, unknown[]>> {
  const activeByProvider = new Map<string, string[]>()
  for (const c of connections) {
    if (!c.provider || !providers.includes(c.provider)) continue
    if (c.isActive === 0 || c.isActive === false) continue
    const ids = activeByProvider.get(c.provider) ?? []
    ids.push(c.id)
    activeByProvider.set(c.provider, ids)
  }

  const result: Record<string, unknown[]> = {}
  await Promise.all(
    [...activeByProvider].map(async ([provider, ids]) => {
      const lists = await Promise.all(ids.map((id) => fetchModels(id).catch(() => null)))
      const seen = new Set<string>()
      const merged: unknown[] = []
      for (const list of lists) {
        for (const m of list?.models ?? []) {
          const id = liveEntryId(m)
          if (!id || seen.has(id)) continue
          seen.add(id)
          merged.push(m)
        }
      }
      if (merged.length > 0) result[provider] = merged
    })
  )
  return result
}

/**
 * liveModelCaps turns a live catalogue entry into the picker's caps block.
 * The backend resolves the capability tables, so the entry states its own
 * numbers; the id heuristics are only the fallback for an entry that predates
 * the enrichment.
 */
export function liveModelCaps(model: LiveCatalogEntry): PickerModel['caps'] {
  const id = model.id || model.slug || model.model || ''
  const fallback = getModelCaps(id)
  return {
    vision: model.isVL ?? fallback.vision,
    audioInput: fallback.audioInput,
    reasoning: model.isReasoning ?? fallback.reasoning,
    ...(model.contextLength ? { contextWindow: model.contextLength } : {}),
    ...(model.maxOutput ? { maxOutput: model.maxOutput } : {}),
    ...(model.thinking_levels?.length ? { thinking_levels: model.thinking_levels } : {}),
  }
}

const MEDIA_KINDS: Record<string, true> = {
  image: true,
  tts: true,
  stt: true,
  embedding: true,
  video: true,
}

function isChatModel(m: unknown): boolean {
  const kind = getModelKind(m)
  if (!kind || kind === 'llm' || kind === 'chat') {
    const obj = typeof m === 'object' && m !== null ? (m as { kind?: string; type?: string }) : null
    return !(obj?.kind && MEDIA_KINDS[obj.kind]) && !(obj?.type && MEDIA_KINDS[obj.type])
  }
  return false
}

export interface PickerModel {
  id: string
  name: string
  value: string
  caps: {
    vision: boolean
    audioInput?: boolean
    reasoning: boolean
    /** Declared context window, when the catalogue states one. */
    contextWindow?: number
    /** Declared output ceiling, when the catalogue states one. */
    maxOutput?: number
    /** Thinking levels the model accepts, when it reasons. */
    thinking_levels?: string[]
  }
  /** True when the gateway confirmed this model is gone upstream. */
  deprecated?: boolean
}

export interface PickerGroup {
  id: string
  name: string
  color?: string
  models: PickerModel[]
}

export interface PickerExtras {
  modelAliases?: Record<string, string>
  customModels?: Array<{ providerAlias?: string; id: string; name?: string; type?: string }>
  disabledModels?: Record<string, string[]>
  /** Deprecations keyed "<provider>/<model>", from /api/models/deprecations. */
  deprecations?: Record<string, unknown>
  /**
   * Live catalogues keyed by provider id, from GET /api/providers/{id}/models.
   * A provider present here is listed from its live entries; one absent falls
   * back to the static bundle, so a failed fetch degrades instead of emptying
   * the group.
   */
  liveModels?: Record<string, unknown[]>
}
export function resolveModelPickerGroups(
  connections: ProviderConnection[] = [],
  providerNodes: ProviderNode[] = [],
  extras: PickerExtras = {}
): PickerGroup[] {
  // Upstream parity (page.js fetchData + ModelSelectModal.js groupedModels):
  // `activeProviders` holds ALL connections (no isActive filter) — every
  // connected provider shows, plus no-auth providers. Inactive connections
  // are still selectable upstream, so the picker mirrors that.
  const connectedProviderIds = new Set<string>()
  for (const c of connections) {
    if (c.provider) {
      connectedProviderIds.add(c.provider)
    }
  }

  const groups: PickerGroup[] = []
  const seenGroupIds = new Set<string>()

  // 1. Catalog providers (active connections or noAuth)
  for (const catItem of PROVIDER_CATALOG) {
    if (!isChatProvider(catItem) && catItem.id !== 'searxng') {
      continue
    }
    const isConnected =
      connectedProviderIds.has(catItem.id) || (catItem.alias && connectedProviderIds.has(catItem.alias))
    // Only the explicit noAuth flag (upstream registry semantics): category
    // "free" providers like kiro/gemini-cli still require a connection.
    const isNoAuth = catItem.noAuth === true
    if (!isConnected && !isNoAuth) {
      continue
    }
    const alias = catItem.alias || PROVIDER_ID_TO_ALIAS[catItem.id] || catItem.id
    // A provider the picker fetched live is listed from that catalogue; the
    // static bundle is the fallback for a provider whose fetch failed.
    const live = liveEntries(catItem.id, extras.liveModels)
    const rawModels = live ? [] : getModelsByProviderId(catItem.id)
    const hardcodedIds = new Set(
      live ? live.map(liveEntryId).filter(Boolean) : (rawModels || []).map((m) => m.id)
    )
    const hasHardcoded = live ? live.length > 0 : (rawModels || []).length > 0

    // Upstream parity (ModelSelectModal.js): custom models registered via
    // /api/models/custom for this provider (only aliasName === modelId shown
    // when hardcoded models exist — the "Add Model" button pattern).
    const customs = (extras.customModels || []).filter(
      (m) => m.providerAlias === alias && m.id && !hardcodedIds.has(m.id)
    )
    const validCustoms = customs.filter((m) => {
      if (!m.type || m.type === 'llm') return true
      return !MEDIA_KINDS[m.type]
    })
    const visibleCustoms = hasHardcoded
      ? validCustoms.filter((m) => (m.name || m.id) === m.id)
      : validCustoms

    // Upstream parity: aliases stored as {aliasName: "alias/modelId"}.
    const aliasEntries = Object.entries(extras.modelAliases || {}).filter(
      ([aliasName, fullModel]) =>
        typeof fullModel === 'string' &&
        fullModel.startsWith(`${alias}/`) &&
        (hasHardcoded ? aliasName === fullModel.replace(`${alias}/`, '') : true) &&
        !hardcodedIds.has(fullModel.replace(`${alias}/`, ''))
    )

    if (!hasHardcoded && customs.length === 0 && aliasEntries.length === 0) {
      continue
    }

    const seenModelIds = new Set<string>()
    const models: PickerModel[] = []
    // A model the gateway saw refused with a 410 is marked, not hidden: the
    // operator may still want it in a combo while they replace it, and a
    // picker that silently dropped it would leave them editing JSON to find
    // out why it disappeared.
    const isDeprecated = (modelId: string) =>
      Boolean(extras.deprecations?.[`${catItem.id.toLowerCase()}/${modelId.toLowerCase()}`])


    for (const m of live || []) {
      const id = liveEntryId(m)
      if (!id || seenModelIds.has(id)) continue
      seenModelIds.add(id)
      const entry = (m && typeof m === 'object' ? m : { id }) as LiveCatalogEntry
      models.push({
        id,
        name: entry.name || id,
        value: `${alias}/${id}`,
        caps: liveModelCaps(entry),
        deprecated: isDeprecated(id),
      })
    }

    for (const m of rawModels || []) {
      if (!m.id || seenModelIds.has(m.id) || !isChatModel(m)) continue
      seenModelIds.add(m.id)
      models.push({
        id: m.id,
        name: m.name || m.id,
        value: `${alias}/${m.id}`,
        caps: getModelCaps(m.id, m),
        deprecated: isDeprecated(m.id),
      })
    }

    for (const m of visibleCustoms) {
      if (seenModelIds.has(m.id)) continue
      seenModelIds.add(m.id)
      models.push({
        id: m.id,
        name: m.name || m.id,
        value: `${alias}/${m.id}`,
        caps: getModelCaps(m.id),
        deprecated: isDeprecated(m.id),
      })
    }

    for (const [aliasName, fullModel] of aliasEntries) {
      const modelId = (fullModel as string).replace(`${alias}/`, '')
      if (!modelId || seenModelIds.has(modelId)) continue
      seenModelIds.add(modelId)
      models.push({
        id: modelId,
        name: aliasName,
        value: fullModel as string,
        caps: getModelCaps(modelId),
        deprecated: isDeprecated(modelId),
      })
    }

    // Upstream parity: filter out disabled models per provider
    // (disabled keyed by storage alias OR providerId).
    const disabled = new Set([
      ...((extras.disabledModels || {})[alias] || []),
      ...((extras.disabledModels || {})[catItem.id] || []),
    ])
    const visible = disabled.size > 0 ? models.filter((m) => !disabled.has(m.id)) : models

    if (visible.length > 0) {
      seenGroupIds.add(catItem.id)
      groups.push({
        id: catItem.id,
        name: catItem.name || catItem.id,
        color: catItem.color,
        models: visible,
      })
    }
  }

  // 2. Custom (openai/anthropic-compatible) nodes — LLM-only. Upstream parity
  // (ModelSelectModal.js isCustomProvider): aliases filtered by raw providerId,
  // values use the display prefix, plus custom models registered for the node id;
  // placeholder when connected but empty.
  for (const node of providerNodes) {
    if (!node.id || seenGroupIds.has(node.id)) continue
    const conn = connections.find((c) => c.provider === node.id)
    const psd = (conn?.providerSpecificData || {}) as Record<string, unknown>
    const nodePrefix = (psd.prefix as string) || node.prefix || node.id
    const displayName = node.name || conn?.name || node.id
    const isCompatibleNode = node.type === 'openai-compatible' || node.type === 'anthropic-compatible'

    const registeredCustom = (extras.customModels || [])
      .filter((m) => m.providerAlias === node.id && m.id)
      .map((m) => ({
        id: m.id,
        name: m.name || m.id,
        value: `${nodePrefix}/${m.id}`,
        caps: getModelCaps(m.id),
        deprecated: Boolean(
          extras.deprecations?.[`${node.id.toLowerCase()}/${m.id.toLowerCase()}`]
        ),
      }))

    // providerNodes lists every node; ones with a connection row are usable as
    // picker values (matches upstream activeProviders, which holds all
    // connections regardless of isActive). Upstream parity (ModelSelectModal.js
    // 6a1573eb): a compatible node that already has user-added custom models is
    // selectable WITHOUT a connection — local / self-hosted endpoints don't
    // always need a stored key, so gating on connections hid models the user had
    // added. Such nodes are LLM-only, which is why they are unioned here, in the
    // LLM-only half of the picker, instead of behind a kind filter. With custom
    // models present there is always something to show, so no placeholder group.
    if (!conn && (!isCompatibleNode || registeredCustom.length === 0)) continue

    const nodeModels = Object.entries(extras.modelAliases || {})
      .filter(([, fullModel]) => typeof fullModel === 'string' && fullModel.startsWith(`${node.id}/`))
      .map(([aliasName, fullModel]) => {
        const modelId = (fullModel as string).replace(`${node.id}/`, '')
        return {
          id: modelId,
          name: aliasName,
          value: `${nodePrefix}/${modelId}`,
          caps: getModelCaps(modelId),
          deprecated: Boolean(
            extras.deprecations?.[`${node.id.toLowerCase()}/${modelId.toLowerCase()}`]
          ),
        }
      })
    const seen = new Set(nodeModels.map((m) => m.value))
    const mergedModels = [...nodeModels, ...registeredCustom.filter((m) => !seen.has(m.value))]

    const modelsToShow =
      mergedModels.length > 0
        ? mergedModels
        : [
            {
              id: `__placeholder__${node.id}`,
              name: `${nodePrefix}/model-id`,
              value: `${nodePrefix}/model-id`,
              caps: { vision: false, audioInput: false, reasoning: false },
            },
          ]

    seenGroupIds.add(node.id)
    groups.push({
      id: node.id,
      name: displayName,
      models: modelsToShow,
    })
  }

  return groups
}

export function resolveFilteredCombos(
  combos: Combo[] = [],
  currentComboName: string | undefined,
  searchQuery: string,
  target: string
): Combo[] {
  if (target === 'vision' || target === 'audio') {
    return []
  }

  const parsed = parseSearchQuery(searchQuery)
  if (parsed.isEmpty) {
    return combos.filter((c) => {
      // Upstream parity (page.js fetchData): webSearch/webFetch combos live
      // under media-providers/web. search-combo is excluded even when its kind
      // field is missing (older rows predate the kind column).
      if (c.kind && c.kind !== 'llm') return false
      if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
      if (currentComboName && c.name === currentComboName) return false
      return true
    })
  }
  return combos.filter((c) => {
    // Upstream parity (page.js fetchData): webSearch/webFetch combos live
    // under media-providers/web. search-combo is excluded even when its kind
    // field is missing (older rows predate the kind column).
    if (c.kind && c.kind !== 'llm') return false
    if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
    if (currentComboName && c.name === currentComboName) return false
    return matchesSearch(c.name, parsed)
  })
}

// resolveFilteredGroups applies the search text and the modality target. The
// already-added models are deliberately not an input: nothing here reads them, but
// passing them from the caller made the signature part of the reactive graph, so
// clicking one pill re-filtered and re-sorted every group and reconciled the
// whole pill list again (#61).
export function resolveFilteredGroups(
  groups: PickerGroup[] = [],
  searchQuery: string,
  target: string
): PickerGroup[] {
  const parsed = parseSearchQuery(searchQuery)

  return groups
    .map((group) => {
      let models = group.models

      // Filter by input-modality capability (vision / audio) matching upstream ModelSelectModal capFilter
      if (target === 'vision') {
        models = models.filter((m) => m.caps.vision)
      } else if (target === 'audio') {
        models = models.filter((m) => m.caps.audioInput)
      }

      if (models.length === 0) return null

      if (!parsed.isEmpty) {
        const groupMatches = matchesSearch(group.name, parsed)
        models = models.filter((m) => {
          if (matchesSearch(m, parsed)) return true
          if (groupMatches) {
            if (parsed.negativeTerms.length > 0) {
              return matchesSearch(m, { ...parsed, positiveTerms: [] })
            }
            return true
          }
          return false
        })
      }

      if (models.length === 0) return null

      // Sort models alphabetically; stable display avoids sudden layout shift and freezing on click
      const sorted = [...models].sort((a, b) => a.name.localeCompare(b.name))

      return {
        ...group,
        models: sorted,
      }
    })
    .filter((g): g is PickerGroup => g !== null)
}
