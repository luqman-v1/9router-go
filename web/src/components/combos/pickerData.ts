import type { Combo, ProviderConnection, ProviderNode } from '../../api/client'
import { getModelCaps, getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
import { PROVIDER_CATALOG, isChatProvider } from '../../lib/providers'

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
  caps: { vision: boolean; audioInput: boolean; reasoning: boolean }
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
    if (!isChatProvider(catItem)) {
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
    const rawModels = getModelsByProviderId(catItem.id)
    const hardcodedIds = new Set((rawModels || []).map((m) => m.id))
    const hasHardcoded = (rawModels || []).length > 0

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

    if ((!rawModels || rawModels.length === 0) && customs.length === 0 && aliasEntries.length === 0) {
      continue
    }

    const seenModelIds = new Set<string>()
    const models: PickerModel[] = []

    for (const m of rawModels || []) {
      if (!m.id || seenModelIds.has(m.id) || !isChatModel(m)) continue
      seenModelIds.add(m.id)
      models.push({
        id: m.id,
        name: m.name || m.id,
        value: `${alias}/${m.id}`,
        caps: getModelCaps(m.id, m),
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

  // 2. Custom (openai/anthropic-compatible) nodes — LLM-only, always shown
  // when connected. Upstream parity (ModelSelectModal.js isCustomProvider):
  // aliases filtered by raw providerId, values use the display prefix, plus
  // custom models registered for the node id; placeholder when empty.
  for (const node of providerNodes) {
    if (!node.id || seenGroupIds.has(node.id)) continue
    const conn = connections.find((c) => c.provider === node.id)
    // providerNodes lists all nodes; only ones with a connection row are
    // usable as picker values (matches upstream activeProviders, which holds
    // all connections regardless of isActive).
    if (!conn) continue
    const psd = (conn.providerSpecificData || {}) as Record<string, unknown>
    const nodePrefix = (psd.prefix as string) || node.prefix || node.id
    const displayName = node.name || conn.name || node.id

    const nodeModels = Object.entries(extras.modelAliases || {})
      .filter(([, fullModel]) => typeof fullModel === 'string' && fullModel.startsWith(`${node.id}/`))
      .map(([aliasName, fullModel]) => {
        const modelId = (fullModel as string).replace(`${node.id}/`, '')
        return {
          id: modelId,
          name: aliasName,
          value: `${nodePrefix}/${modelId}`,
          caps: getModelCaps(modelId),
        }
      })
    const registeredCustom = (extras.customModels || [])
      .filter((m) => m.providerAlias === node.id && m.id)
      .map((m) => ({
        id: m.id,
        name: m.name || m.id,
        value: `${nodePrefix}/${m.id}`,
        caps: getModelCaps(m.id),
      }))
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

  const query = searchQuery.trim().toLowerCase()
  return combos.filter((c) => {
    // Upstream parity (page.js fetchData): webSearch/webFetch combos live
    // under media-providers/web. search-combo is excluded even when its kind
    // field is missing (older rows predate the kind column).
    if (c.kind && c.kind !== 'llm') return false
    if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
    if (currentComboName && c.name === currentComboName) return false
    if (query) {
      return c.name.toLowerCase().includes(query)
    }
    return true
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
  const query = searchQuery.trim().toLowerCase()

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

      if (query) {
        const groupMatches = group.name.toLowerCase().includes(query)
        const matchedModels = models.filter(
          (m) =>
            m.name.toLowerCase().includes(query) ||
            m.id.toLowerCase().includes(query) ||
            m.value.toLowerCase().includes(query)
        )
        models = matchedModels.length > 0 ? matchedModels : groupMatches ? models : []
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
