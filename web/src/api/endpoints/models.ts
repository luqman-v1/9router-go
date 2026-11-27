import type { CustomModelBatchEntry, ModelCaps, ModelDeprecationMap } from '../types'
import { request } from '../runtime'

const modelsEndpoints = {
  // Models — upstream parity: GET /api/models/custom -> { models: [...] },
  // GET /api/models/disabled -> { disabled: {...} } (full map) or { ids: [...] } (per-provider).
  getCustomModels: () => request<{ models: Array<{ id: string; name?: string; providerAlias?: string; type?: string; kind?: string }> }>('/api/models/custom'),
  /**
   * Per-model capabilities and thinking levels for one provider. The dashboard
   * bundles the model *catalog* but capabilities are server-side state (provider
   * registry + capability tables + synced catalog), so they are resolved by the
   * Go backend — the same split upstream keeps (useModelCaps over /api/models).
   */
  getModelCaps: (provider: string) =>
    request<{ provider: string; caps: Record<string, ModelCaps> }>(
      `/api/models/caps?provider=${encodeURIComponent(provider)}`
    ),
  getDisabledModels: () => request<Record<string, unknown>>('/api/models/disabled'),
  saveCustomModel: (key: string, value: unknown) =>
    request<{ success: boolean }>('/api/models/custom', {
      method: 'POST',
      body: JSON.stringify({ key, value }),
    }),
  /**
   * Save a whole pasted list of model ids in one request (D9). The response names
   * every id it stored and every id it did not, so a partial save is visible
   * instead of implied by a count that never came back.
   */
  saveCustomModelsBulk: (payload: {
    providerAlias: string
    type?: string
    models: CustomModelBatchEntry[]
  }) =>
    request<{ status: string; added: string[]; skipped: Array<{ id: string; reason: string; code: string }> }>(
      '/api/models/custom',
      {
        method: 'POST',
        body: JSON.stringify(payload),
      }
    ),
  deleteCustomModel: (key: string) =>
    request<{ success: boolean }>(`/api/models/custom/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    }),
  /**
   * Removes every model one provider node carries (imported custom models plus
   * the aliases pointing at them) in a single request — the undo for
   * "Import from /models", which writes hundreds of rows. Catalog models that
   * ship with the build are not stored per node and are left alone.
   */
  deleteProviderModels: (provider: string) =>
    request<{ status: string; provider: string; removedModels: string[]; removedAliases: string[]; count: number }>(
      `/api/providers/${encodeURIComponent(provider)}/models`,
      { method: 'DELETE' }
    ),
  saveDisabledModels: (provider: string, modelIds: string[]) =>
    request<{ success: boolean }>(`/api/models/disabled/${encodeURIComponent(provider)}`, {
      method: 'PUT',
      body: JSON.stringify(modelIds),
    }),
  /**
   * Folds a list of dead model ids into the provider's disabled list.
   *
   * The merge happens server-side on purpose: the dashboard already holds the
   * verdicts from its own probes, and sending only the failures avoids a
   * read-modify-write from the browser — which is how two open tabs disable
   * each other's models.
   */
  autoDisableModels: (provider: string, models: string[], dryRun = false) =>
    request<{
      provider: string
      disabled: string[]
      alreadyDisabled: string[]
      total: number
      dryRun: boolean
    }>('/api/models/auto-disable', {
      method: 'POST',
      body: JSON.stringify({ provider, models, dryRun }),
    }),
  /** Reads the model-health configuration, the failure counters and the audit log. */
  getModelHealth: () =>
    request<{
      config: {
        enabled: boolean
        intervalMin: number
        threshold: number
        scope: 'history' | 'failed'
        autoEnable: boolean
      }
      audit: Array<{
        provider: string
        model: string
        action: 'disabled' | 'enabled'
        reason?: string
        actor: 'scheduler' | 'operator'
        at: string
      }>
      failures: Record<
        string,
        {
          count: number
          lastError?: string
          lastFailedAt?: string
          disabledByScheduler?: boolean
          resetAt?: string
        }
      >
    }>('/api/models/health'),
  /**
   * Writes the model-health configuration. The server merges the payload into
   * the stored value, so a field this client does not send is carried over
   * rather than reset.
   */
  saveModelHealth: (config: {
    enabled?: boolean
    intervalMin?: number
    threshold?: number
    scope?: 'history' | 'failed'
    autoEnable?: boolean
  }) =>
    request<{
      status: string
      config: {
        enabled: boolean
        intervalMin: number
        threshold: number
        scope: 'history' | 'failed'
        autoEnable: boolean
      }
    }>('/api/models/health', {
      method: 'PUT',
      body: JSON.stringify(config),
    }),
  /** Upstream GET /api/models/alias — full alias map (compatible pages merge custom + legacy rows). */
  getModelAliases: () => request<{ aliases: Record<string, string> }>('/api/models/alias'),
  /** Upstream PUT /api/models/alias {model, alias}. */
  setModelAlias: (model: string, alias: string) =>
    request<{ success: boolean }>('/api/models/alias', {
      method: 'PUT',
      body: JSON.stringify({ model, alias }),
    }),
  /** Upstream DELETE /api/models/alias?alias=. */
  deleteModelAlias: (alias: string) =>
    request<{ success: boolean }>(`/api/models/alias?alias=${encodeURIComponent(alias)}`, {
      method: 'DELETE',
    }),
  /**
   * Models the gateway has confirmed dead upstream, keyed "<provider>/<model>".
   *
   * A client that never sends gateway traffic has no 410s of its own to learn
   * from, so this endpoint is how its model list finds out a model is gone.
   */
  getModelDeprecations: (provider?: string) =>
    request<{ provider: string; deprecations: ModelDeprecationMap }>(
      `/api/models/deprecations${provider ? `?provider=${encodeURIComponent(provider)}` : ''}`,
    ),
  /**
   * Re-reads every connection's upstream catalogue and clears the badge of any
   * model the provider still publishes. Omitting the provider syncs them all.
   */
  syncProviderModels: (provider?: string) =>
    request<{ success: boolean; provider: string; synced: number; failed: number; revived: number; lastError?: string }>(
      `/api/models/sync${provider ? `?provider=${encodeURIComponent(provider)}` : ''}`,
      { method: 'POST' },
    ),
  /**
   * Pings one model the way the page lists it. `kind` picks the endpoint the
   * probe travels on (upstream /api/models/test `kind`): a System One model
   * answers on /v1/systemone, and a chat probe on it returns 500. Omit `kind`
   * for a plain LLM model.
   */
  testModel: (model: string, kind?: string) =>
    request<{ ok: boolean; error?: string; note?: string; latencyMs?: number; status?: number; blocked?: boolean; resetAt?: string }>('/api/models/test', {
      method: 'POST',
      body: JSON.stringify({ model, kind }),
    }),
}

export default modelsEndpoints
