// Shared custom/disabled-models envelope parsing + change notification.
// Upstream parity:
// - GET /api/models/custom -> { models: [...] } (ModelSelectModal fetchCustomModels,
//   ModelsCard fetchData, SttExampleCard loadCustom all read `data.models || []`).
// - GET /api/models/disabled -> { disabled: {...} } (ModelSelectModal) or
//   GET /api/models/disabled?providerAlias=xxx -> { ids: [...] } (provider page).
// - Every add/delete dispatches window "customModelChanged" so sibling cards
//   (STT example, pickers) reload without a full page refresh.

export interface CustomModelEntry {
  id: string
  name?: string
  providerAlias?: string
  type?: string
  kind?: string
  caps?: { vision?: boolean; reasoning?: boolean }
  [key: string]: unknown
}

function isObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function toEntry(m: unknown, fallbackId?: string): CustomModelEntry | null {
  if (!isObject(m)) return null
  const id = typeof m.id === 'string' ? m.id : fallbackId
  if (!id) return null
  return { ...(m as Record<string, unknown>), id } as CustomModelEntry
}

/** Upstream: `(d.models || [])`. Tolerates bare-array / record-map shapes too. */
export function parseCustomModelsResponse(res: unknown): CustomModelEntry[] {
  const raw: unknown = isObject(res) && Array.isArray(res.models) ? res.models : res
  if (Array.isArray(raw)) {
    const out: CustomModelEntry[] = []
    for (const m of raw) {
      const e = toEntry(m)
      if (e) out.push(e)
    }
    return out
  }
  if (isObject(raw)) {
    const out: CustomModelEntry[] = []
    for (const [k, v] of Object.entries(raw)) {
      const e = toEntry(v, k)
      if (e) out.push(e)
    }
    return out
  }
  return []
}

/**
 * Full-map shape: bare `{ alias: [...] }` (go port) or `{ disabled: {...} }`
 * (upstream). Per-provider `{ ids: [...] }` is handled by parseDisabledIds.
 */
export function parseDisabledModelsMap(res: unknown): Record<string, string[]> {
  const raw: unknown = isObject(res) && isObject(res.disabled) ? res.disabled : res
  if (!isObject(raw)) return {}
  const out: Record<string, string[]> = {}
  for (const [k, v] of Object.entries(raw)) {
    const inner = isObject(v) ? v : null
    const arr = Array.isArray(v) ? v : inner && Array.isArray(inner.disabled) ? inner.disabled : inner && Array.isArray(inner.ids) ? inner.ids : []
    out[k] = (arr as unknown[]).filter((x): x is string => typeof x === 'string')
  }
  return out
}

/** Upstream per-provider shape: `{ ids: [...] }`. */
export function parseDisabledIds(res: unknown): string[] {
  if (isObject(res) && Array.isArray(res.ids)) {
    return (res.ids as unknown[]).filter((x): x is string => typeof x === 'string')
  }
  return []
}

export const CUSTOM_MODELS_CHANGED_EVENT = 'customModelChanged'

/** Upstream parity: ModelsCard/page.js dispatch this after every add/delete. */
export function notifyCustomModelsChanged(): void {
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(CUSTOM_MODELS_CHANGED_EVENT))
  }
}

/**
 * Upstream SttExampleCard parity: reload on window focus + customModelChanged.
 * Returns an unsubscribe function for onDestroy.
 */
export function subscribeCustomModelsChanged(load: () => void): () => void {
  if (typeof window === 'undefined') return () => {}
  window.addEventListener('focus', load)
  window.addEventListener(CUSTOM_MODELS_CHANGED_EVENT, load)
  return () => {
    window.removeEventListener('focus', load)
    window.removeEventListener(CUSTOM_MODELS_CHANGED_EVENT, load)
  }
}

/** One row a bulk import wants to store, already keyed for POST /api/models/custom. */
export interface CustomModelWrite {
  /** The model id, so a refusal can name the row it refused. */
  id: string
  key: string
  value: unknown
}

export interface CustomModelImportOutcome {
  imported: number
  /** Ids the server refused, carrying the reason it gave. */
  refused: Array<{ id: string; reason: string }>
}

/**
 * Stores a batch of custom models one call at a time, tolerating a refusal.
 *
 * The server refuses an id that already addresses a model alias or a combo
 * with a typed 409, and a /models listing carries such ids routinely: a
 * bare-name alias usually points at the same model on another node, which the
 * guard cannot tell apart from a genuine collision. Awaiting every save inside
 * one try/catch made the first refusal abort the batch — the ids listed after
 * it were never attempted, which is what "Import from /models" did to a
 * provider whose list held one already-aliased name. Each row is now attempted
 * on its own and the refusals come back as data, so the caller can report
 * exactly which ids to rename instead of losing the rest of the import.
 */
export async function importCustomModelsBestEffort(
  writes: CustomModelWrite[],
  save: (key: string, value: unknown) => Promise<unknown>,
): Promise<CustomModelImportOutcome> {
  let imported = 0
  const refused: Array<{ id: string; reason: string }> = []
  for (const write of writes) {
    try {
      await save(write.key, write.value)
      imported += 1
    } catch (err) {
      refused.push({ id: write.id, reason: err instanceof Error ? err.message : String(err) })
    }
  }
  return { imported, refused }
}
