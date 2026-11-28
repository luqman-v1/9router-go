import type { CodexResetCreditConsumeResponse, CodexResetCreditListResponse, ConnectionUsageResponse, GLMResetCardRedeemResponse, GLMResetCardsResponse, ProviderConnection, UsageByAPIKeyResponse, UsageDashboardResponse } from '../types'
import { normalizeConnection, request } from '../runtime'
import connectionsEndpoints from './connections.ts'

const usageEndpoints = {
  // Usage & Telemetry
  /**
   * Usage aggregates for a window. `views` names the breakdowns to build:
   * `none` is totals only (plus byProvider, which the totals are summed from),
   * and omitting it returns every breakdown — the shape this call always had.
   */
  getUsageStats: (period = 'today', views?: string) =>
    request<any>(
      `/api/usage/stats?period=${encodeURIComponent(period)}${views ? `&views=${encodeURIComponent(views)}` : ''}`
    ),
  /** One chart point: bucket label (HH:MM or MMM D), totals, and the per-model-family token split. */
  getUsageChart: (period = '7d') =>
    request<{ label: string; tokens: number; cost: number; byModel: Record<string, number> }[]>(
      `/api/usage/chart?period=${encodeURIComponent(period)}`
    ),
  /** Details-section dashboard payload: heatmap days, lifetime cards, node health. */
  getUsageDashboard: () => request<UsageDashboardResponse>('/api/usage/dashboard'),
  /**
   * Spend per client API key — the keys created inside the gateway.
   *
   * This is not the Overview's "Usage by API Key" table: that one groups the
   * upstream credential the router spent, this one groups the key the caller
   * presented. An operator who resells the gateway needs this one.
   */
  getUsageByAPIKey: (period = '7d') =>
    request<UsageByAPIKeyResponse>(`/api/usage/api-keys?period=${encodeURIComponent(period)}`),
  /** Paged request rows; period windows the server query (?period=<preset|n>d|n>h). */
  getRequestDetails: (limit = 50, offset = 0, period = '') =>
    request<any>(
      `/api/usage/request-details?limit=${limit}&offset=${offset}${period ? `&period=${encodeURIComponent(period)}` : ''}`
    ),
  /** Full stored payload for one request; the list omits request/response bodies. */
  getRequestDetail: (id: string) => request<any>(`/api/usage/request-details/${encodeURIComponent(id)}`),
  resetHealth: (provider: string, model?: string) =>
    request<{ status: string }>(`/admin/health/reset?provider=${encodeURIComponent(provider)}${model ? `&model=${encodeURIComponent(model)}` : ''}`, {
      method: 'POST',
    }),
  getProvidersClient: async (): Promise<{ connections: ProviderConnection[] }> => {
    try {
      const res = await request<{ connections: ProviderConnection[] }>('/api/providers/client')
      if (res && res.connections) {
        return { ...res, connections: res.connections.map(normalizeConnection) }
      }
    } catch {}
    const conns = await connectionsEndpoints.getConnections().catch(() => [])
    return { connections: conns }
  },
  // Paginated provider fetch with filters (mirrors Next.js /api/providers/client).
  // The Go backend currently returns the full list; pagination is applied client-side.
  getProvidersClientPage: async (
    query: string,
  ): Promise<{
    connections: ProviderConnection[]
    providerOptions?: string[]
    pagination?: { page: number; pageSize: number; total: number; totalPages: number }
    totals?: { eligibleConnections: number; providerFilteredConnections: number }
  }> => {
    try {
      const res = await request<{
        connections: ProviderConnection[]
        providerOptions?: string[]
        pagination?: { page: number; pageSize: number; total: number; totalPages: number }
        totals?: { eligibleConnections: number; providerFilteredConnections: number }
      }>(`/api/providers/client${query ? `?${query}` : ''}`)
      if (res && res.connections) {
        return { ...res, connections: res.connections.map(normalizeConnection) }
      }
    } catch {}
    const fallback = await usageEndpoints.getProvidersClient()
    return { connections: fallback.connections }
  },
  // The quota tracker cancels a superseded pass with `signal`: the reading it
  // would receive belongs to a page the user has already navigated away from.
  getConnectionUsage: async (
    connectionId: string,
    force = false,
    signal?: AbortSignal,
  ): Promise<ConnectionUsageResponse> => {
    return request<ConnectionUsageResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}${force ? '?force=1' : ''}`,
      { signal },
    )
  },
  // Codex reset credits. The list is fetched when the user opens the chooser
  // rather than with the quota poll, so the button stays cheap until it is used.
  listCodexResetCredits: (connectionId: string) =>
    request<CodexResetCreditListResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/reset-credits`
    ),
  consumeCodexResetCredit: (connectionId: string, selectionToken: string, idempotencyKey?: string) =>
    request<CodexResetCreditConsumeResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/reset-credits/consume`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ selectionToken, idempotencyKey }),
      }
    ),
  // Claude reset credits (cedar_ember grants). Only for provider == "claude".
  listClaudeResetCredits: (connectionId: string) =>
    request<CodexResetCreditListResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/claude-reset-credits`
    ),
  consumeClaudeResetCredit: (connectionId: string, selectionToken: string, idempotencyKey?: string) =>
    request<CodexResetCreditConsumeResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/claude-reset-credits/consume`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ selectionToken, idempotencyKey }),
      }
    ),
  // GLM Reset Cards (Z.ai international). Only for provider == "glm".
  getGlmResetCards: (connectionId: string) =>
    request<GLMResetCardsResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/glm-reset-cards`
    ),
  redeemGlmResetCard: (connectionId: string, payload: { recordId: string; type: 'FIVE_HOUR' | 'WEEK' }) =>
    request<GLMResetCardRedeemResponse>(
      `/api/usage/${encodeURIComponent(connectionId)}/glm-reset-cards`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    ),
}

export default usageEndpoints
