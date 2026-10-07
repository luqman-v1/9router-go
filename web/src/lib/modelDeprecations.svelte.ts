// Shared model-deprecation state.
//
// The gateway learns a model is dead the only way it can learn it: a live
// request drew an HTTP 410 naming it. That knowledge lives server-side, so the
// dashboard reads it here and shares one cache across the provider model list,
// the combo model picker and the combo cards — a model badged in one place must
// read the same in the others, and three independent fetches would let them
// disagree until each refetched.
//
// The cache is $state rather than a plain object so every badge bound to it
// re-renders when a sync revives a model, without a subscription of its own.

import { api, type ModelDeprecation, type ModelDeprecationMap } from '../api/client'

export type { ModelDeprecation, ModelDeprecationMap }

export const DEPRECATIONS_CHANGED_EVENT = 'modelDeprecationsChanged'

/** Builds the cache key from a provider id and a model id. */
export function deprecationKey(provider: string, model: string): string {
  return `${provider.toLowerCase()}/${model.toLowerCase()}`
}

/**
 * Builds the key from a combo entry, which already reads "provider/model".
 * An entry with no slash names no provider, so it cannot be badged.
 */
export function deprecationKeyForEntry(entry: string): string | null {
  const slash = entry.indexOf('/')
  if (slash <= 0 || slash === entry.length - 1) return null
  return deprecationKey(entry.slice(0, slash), entry.slice(slash + 1))
}

const state = $state<{ deprecations: ModelDeprecationMap }>({ deprecations: {} })

let inFlight: Promise<ModelDeprecationMap> | null = null

/** The current cache, without fetching. */
export function cachedDeprecations(): ModelDeprecationMap {
  return state.deprecations
}

/**
 * Loads the deprecations once and shares the request between concurrent
 * callers, so a provider page and a combo picker mounting together cost one
 * round trip rather than two.
 */
export async function loadDeprecations(force = false): Promise<ModelDeprecationMap> {
  if (inFlight && !force) return inFlight
  inFlight = api
    .getModelDeprecations()
    .then((res) => {
      state.deprecations = res?.deprecations ?? {}
      return state.deprecations
    })
    .catch(() => {
      // A failed read leaves the previous cache in place: a network blip must
      // not strip every badge from the page.
      return state.deprecations
    })
    .finally(() => {
      inFlight = null
    })
  return inFlight
}

/** The deprecation recorded for one model, or null when it has none. */
export function deprecationFor(provider: string, model: string): ModelDeprecation | null {
  return state.deprecations[deprecationKey(provider, model)] ?? null
}

/** Re-reads the deprecations and tells every badge on the page to re-render. */
export async function refreshDeprecations(): Promise<ModelDeprecationMap> {
  const next = await loadDeprecations(true)
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(DEPRECATIONS_CHANGED_EVENT))
  }
  return next
}

/** Subscribes a view to reload on focus and after a sync. Returns an unsubscribe. */
export function subscribeDeprecations(load: () => void): () => void {
  if (typeof window === 'undefined') return () => {}
  window.addEventListener('focus', load)
  window.addEventListener(DEPRECATIONS_CHANGED_EVENT, load)
  return () => {
    window.removeEventListener('focus', load)
    window.removeEventListener(DEPRECATIONS_CHANGED_EVENT, load)
  }
}