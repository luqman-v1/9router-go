import type { DatabaseStats, MiniMaxCheckinResult, ModelCapsOverrideResponse, ProviderOverridesResponse, PruneUsageResult, Settings } from '../types'
import { request } from '../runtime'

const settingsEndpoints = {
  // Settings
  getSettings: () => request<Settings>('/api/settings'),
  updateSettings: (settings: Partial<Settings> | Record<string, unknown>) =>
    request<{ success: boolean }>('/api/settings', {
      method: 'PUT',
      body: JSON.stringify(settings),
    }),
  patchSettings: (settings: Partial<Settings> | Record<string, unknown>) =>
    request<Record<string, unknown>>('/api/settings', {
      method: 'PATCH',
      body: JSON.stringify(settings),
    }),
  // MiniMax Code daily check-in: the last outcome per account, and an
  // immediate claim for one connection (magpie's "Check in now").
  getMiniMaxCheckins: () =>
    request<{ checkins: Record<string, MiniMaxCheckinResult> }>('/api/minimax/checkins'),
  miniMaxCheckinNow: (connectionId: string) =>
    request<MiniMaxCheckinResult>(`/api/minimax/checkins/${encodeURIComponent(connectionId)}`, {
      method: 'POST',
    }),
  /**
   * Reads live USD rates for the requested codes and overlays them on the
   * stored table. `codes` is the catalog the dashboard offers
   * (web/src/lib/currency.ts), `ratesStored` counts the rates the provider
   * answered with, and `ratesMissing` names the requested codes it did not
   * publish, whose manual rates were kept.
   */
  refreshCurrencyRates: (payload: { code: string; showBoth: boolean; codes: string[] }) =>
    request<{ displayCurrency: unknown; source: string; ratesStored: number; ratesMissing: string[] }>(
      '/api/settings/currency/refresh',
      {
        method: 'POST',
        body: JSON.stringify(payload),
      }
    ),
  getDatabaseStats: () => request<DatabaseStats>('/api/settings/database/stats'),
  pruneDatabase: () =>
    request<{ success: boolean; pruned: number }>('/api/settings/database/prune', {
      method: 'POST',
    }),
  pruneUsageHistory: (params?: { days?: number; before?: string; dryRun?: boolean }) => {
    const query = new URLSearchParams()
    if (params?.dryRun) query.set('dryRun', '1')
    if (typeof params?.days === 'number') query.set('days', String(params.days))
    if (params?.before) query.set('before', params.before)
    const qs = query.toString()
    return request<PruneUsageResult>(`/api/settings/database/prune-usage${qs ? `?${qs}` : ''}`, {
      method: 'POST',
      body: params?.days || params?.before ? JSON.stringify({ days: params.days, before: params.before }) : undefined,
    })
  },
  vacuumDatabase: () =>
    request<{ success: boolean; databaseSizeBytes: number; reclaimableBytes: number }>(
      '/api/settings/database/vacuum',
      { method: 'POST' }
    ),
  // The body carries the confirmation word: the gateway ignores a restart
  // request that could not set a JSON body, so a cross-site form post cannot
  // stop the process.
  restartGateway: () =>
    request<{ success: boolean; restarting: boolean }>('/api/system/restart', {
      method: 'POST',
      body: JSON.stringify({ confirm: 'restart' }),
    }),
  testRtk: (data: { text: string; config?: Record<string, unknown> }) =>
    request<{
      originalTokens: number
      compressedTokens: number
      savedTokens: number
      savedPct: number
      text: string
      detectedCategory: string
      techniquesUsed: string[]
    }>('/api/tokensaver/rtk/test', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  getRTKFilters: () =>
    request<{
      filters: Array<{
        id: string
        label: string
        category: string
        description: string
        priority: number
        enabled: boolean
      }>
    }>('/api/tokensaver/rtk/filters'),
  testCaveman: (data: { text: string; level?: string; language?: string; mode?: string; preserveKeywords?: string }) =>
    request<{
      text: string
      originalTokens: number
      compressedTokens: number
      savedPct: number
    }>('/api/tokensaver/caveman/test', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  /**
   * Per-provider outbound header overrides. `builtinHeaders` is what the
   * registry already sends, so the UI shows the real baseline instead of an
   * operator guessing; `blockedHeaders` is the set an override may never
   * carry (auth, framing, destination).
   */
  getProviderOverrides: (providerId: string) =>
    request<ProviderOverridesResponse>(`/api/providers/${encodeURIComponent(providerId)}/overrides`),
  saveProviderOverrides: (providerId: string, headers: Record<string, string>) =>
    request<{ provider: string; headers: Record<string, string> }>(
      `/api/providers/${encodeURIComponent(providerId)}/overrides`,
      { method: 'PUT', body: JSON.stringify({ headers }) },
    ),
  /**
   * Per-model capability overrides (the model editor). The model segment is
   * percent-encoded because an id carries slashes and colons.
   */
  getModelCapsOverride: (providerId: string, model: string) =>
    request<ModelCapsOverrideResponse>(
      `/api/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(model)}/caps`,
    ),
  saveModelCapsOverride: (providerId: string, model: string, capsOverride: Record<string, boolean | number>) =>
    request<ModelCapsOverrideResponse>(
      `/api/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(model)}/caps`,
      { method: 'PUT', body: JSON.stringify({ capsOverride }) },
    ),
}

export default settingsEndpoints
