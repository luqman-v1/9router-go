import type { CacheEntriesResponse, CacheStatsResponse, CompressionAnalyticsSummary, LoginResponse, ModelBreakerStatusResponse, RequireLoginResponse } from '../types'
import { request } from '../runtime'

const authEndpoints = {
  // Auth
  checkRequireLogin: async (): Promise<RequireLoginResponse> => {
    try {
      const res = await fetch('/api/settings/require-login')
      if (res.ok) {
        const data = await res.json()
        return {
          requireLogin: !!data.requireLogin,
          tunnelDashboardAccess: !!data.tunnelDashboardAccess,
          tunnelUrl: data.tunnelUrl,
          tailscaleUrl: data.tailscaleUrl,
          authenticated: !!data.authenticated,
        }
      }
    } catch {}
    try {
      const res = await fetch('/api/auth/status')
      if (res.ok) {
        const data = await res.json()
        return {
          requireLogin: !!data.requireLogin,
          hasPassword: !!data.hasPassword,
          authMode: data.authMode,
          authenticated: !!data.authenticated,
        }
      }
    } catch {}
    return { requireLogin: false }
  },
  // the dashboard password from the browser before the first login. The
  // server only accepts this from a loopback peer while no password is stored.
  getSetup: async (): Promise<{ status: boolean; databaseType: string }> => {
    const res = await fetch('/api/setup')
    if (!res.ok) return { status: false, databaseType: 'sqlite' }
    const data = await res.json()
    return { status: !!data.status, databaseType: data.databaseType || 'sqlite' }
  },
  setup: async (
    password: string,
    advancedMode?: boolean
  ): Promise<{ success: boolean; error?: string }> => {
    const res = await fetch('/api/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password, ...(advancedMode === undefined ? {} : { advancedMode }) }),
    })
    if (res.ok) {
      sessionStorage.setItem('9router_auth', 'true')
      localStorage.setItem('9router_auth', 'true')
      return { success: true }
    }
    let errText = 'Setup failed'
    try {
      const errJson = await res.json()
      errText = errJson.error?.message || errJson.error || errJson.message || errText
    } catch {}
    return { success: false, error: errText }
  },
  // Skip the password step: the dashboard answers without a login (the same
  // switch Settings > Security exposes as "Require login"), so this is only
  // offered on a fresh install from the local machine.
  setupSkip: async (): Promise<{ success: boolean; error?: string }> => {
    const res = await fetch('/api/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ skipPassword: true }),
    })
    if (res.ok) {
      // The server sets the httpOnly auth_token cookie on success; the client
      // gate mirrors what api.login does.
      sessionStorage.setItem('9router_auth', 'true')
      localStorage.setItem('9router_auth', 'true')
      return { success: true }
    }
    let errText = 'Setup failed'
    try {
      const errJson = await res.json()
      errText = errJson.error?.message || errJson.error || errJson.message || errText
    } catch {}
    return { success: false, error: errText }
  },
  login: async (password: string, newPassword?: string): Promise<LoginResponse> => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password, ...(newPassword ? { newPassword } : {}) }),
    })
    if (res.ok) {
      const data = await res.json()
      // The server sets the httpOnly auth_token cookie; this flag only drives
      // the client-side gate (isAuthenticated) since JS cannot read it.
      sessionStorage.setItem('9router_auth', 'true')
      localStorage.setItem('9router_auth', 'true')
      return { success: true, mustChangePassword: !!data.mustChangePassword }
    }
    let errText = 'Invalid password'
    let retryAfter: number | undefined
    let resetHint: string | undefined
    let remainingBeforeLock: number | undefined
    let mustChangePassword = false
    try {
      const errJson = await res.json()
      errText = errJson.error?.message || errJson.error || errJson.message || errText
      if (typeof errJson.retryAfter === 'number') retryAfter = errJson.retryAfter
      else if (typeof errJson.retryAfter === 'string') retryAfter = Number(errJson.retryAfter) || undefined
      if (typeof errJson.resetHint === 'string') resetHint = errJson.resetHint
      if (typeof errJson.remainingBeforeLock === 'number') remainingBeforeLock = errJson.remainingBeforeLock
      if (errJson.mustChangePassword === true) mustChangePassword = true
    } catch {}
    const err = new Error(errText) as Error &
      Pick<LoginResponse, 'retryAfter' | 'resetHint' | 'remainingBeforeLock' | 'mustChangePassword'>
    err.retryAfter = retryAfter
    err.resetHint = resetHint
    err.remainingBeforeLock = remainingBeforeLock
    err.mustChangePassword = mustChangePassword
    throw err
  },
    // Cache Analytics
    /**
     * `period` bounds the totals, the breakdowns and the hourly trend together.
     * It was `trendHours` before, which bounded only the trend — the cards above
     * the chart were always all-time, whatever the chart was showing.
     */
    getCacheStats: (period = '24h') =>
      request<CacheStatsResponse>(`/api/cache?period=${encodeURIComponent(period)}`),
    deleteCache: (params?: { model?: string; signature?: string; staleMs?: number }) => {
      const sp = new URLSearchParams()
      if (params?.model) sp.set('model', params.model)
      if (params?.signature) sp.set('signature', params.signature)
      if (params?.staleMs) sp.set('staleMs', String(params.staleMs))
      const qs = sp.toString()
      return request<{ success: boolean; count?: number }>(`/api/cache${qs ? `?${qs}` : ''}`, {
        method: 'DELETE',
      })
    },
    getCacheEntries: (params?: {
      page?: number
      limit?: number
      search?: string
      model?: string
      sortBy?: string
      sortOrder?: string
    }) => {
      const sp = new URLSearchParams()
      if (params?.page) sp.set('page', String(params.page))
      if (params?.limit) sp.set('limit', String(params.limit))
      if (params?.search) sp.set('search', params.search)
      if (params?.model) sp.set('model', params.model)
      if (params?.sortBy) sp.set('sortBy', params.sortBy)
      if (params?.sortOrder) sp.set('sortOrder', params.sortOrder)
      const qs = sp.toString()
      return request<CacheEntriesResponse>(`/api/cache/entries${qs ? `?${qs}` : ''}`)
    },
    deleteCacheEntry: (id: string) =>
      request<{ success: boolean }>(`/api/cache/entries?id=${encodeURIComponent(id)}`, {
        method: 'DELETE',
      }),
    // Compression Analytics
    getCompressionAnalytics: (period = '24h') =>
      request<CompressionAnalyticsSummary>(
        `/api/analytics/compression?period=${encodeURIComponent(period)}`
      ),
    // Per-model circuit breaker: one provider/model pair, distinct from the
    // provider breaker. `retryAfterMs` is 0 for a pair that does not come
    // back on a timer (closed, or disabled).
    getModelBreakers: () => request<ModelBreakerStatusResponse>('/api/model-breakers'),
    /** Lifts one pair. The only way a disabled model serves again. */
    resetModelBreaker: (provider: string, model: string) =>
      request<{ status: string }>(
        `/api/model-breakers/${encodeURIComponent(provider)}/${encodeURIComponent(model)}/reset`,
        { method: 'POST' }
      ),
    /** Lifts every pair, for after a provider-wide outage. */
    resetAllModelBreakers: () =>
      request<{ status: string }>('/api/model-breakers/reset', { method: 'POST' }),
  logout: async () => {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch {}
    sessionStorage.removeItem('9router_auth')
    localStorage.removeItem('9router_auth')
  },
}

export default authEndpoints
