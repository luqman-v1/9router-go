import type { CreateConnectionPayload, ProviderConnection, ValidateProviderResult } from '../types'
import { normalizeConnection, request } from '../runtime'

const connectionsEndpoints = {
  // Connections
  getConnections: async () => {
    const conns = await request<ProviderConnection[]>('/api/connections')
    return conns.map(normalizeConnection)
  },
  createConnection: (payload: CreateConnectionPayload) =>
    request<{ success: boolean; id: string }>('/api/connections', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  /** Probe a raw API key against the provider (upstream POST /api/providers/validate). */
  validateProvider: async (payload: {
    provider: string
    apiKey?: string
    providerSpecificData?: Record<string, unknown>
  }): Promise<ValidateProviderResult> => {
    try {
      const res = await request<{ valid?: boolean; supported?: boolean; error?: string | null }>(
        '/api/providers/validate',
        { method: 'POST', body: JSON.stringify(payload) }
      )
      return {
        supported: res.supported !== false,
        valid: res.valid === true,
        error: typeof res.error === 'string' && res.error ? res.error : undefined,
      }
    } catch {
      // 400 "Provider validation not supported" and transport errors both land here.
      return { supported: false, valid: false }
    }
  },
  /**
   * Open a throwaway browser and sign in to Gemini Web, so the session is
   * captured without a browser that keeps running and rotating the same
   * cookies. Answers with the cookie header to paste.
   */
  geminiWebBrowserLogin: async (): Promise<{ cookie: string }> =>
    request<{ cookie: string }>('/api/providers/gemini-web/browser-login', { method: 'POST' }),
  /**
   * Open a throwaway browser and sign in to DeepSeek Web, so the credential is
   * captured without a browser that keeps running and rotating the same
   * session.
   *
   * Answers with both halves of the credential: the userToken the API
   * authenticates with, and the site's own cookies the WAF reads.
   */
  deepSeekWebBrowserLogin: async (): Promise<{ userToken: string; cookie: string }> =>
    request<{ userToken: string; cookie: string }>('/api/providers/deepseek-web/browser-login', {
      method: 'POST',
    }),
  /**
   * Read the DeepSeek credential out of a browser that is already signed in,
   * and answer with both halves of it: the userToken the API authenticates
   * with, and the site's own cookies the WAF reads.
   *
   * The warning says what to do about a browser that is still running.
   */
  deepSeekWebImportChrome: async (profile?: string): Promise<{ userToken: string; cookie: string; warning: string }> =>
    request<{ userToken: string; cookie: string; warning: string }>('/api/providers/deepseek-web/import-chrome', {
      method: 'POST',
      body: JSON.stringify({ profile: profile ?? '' }),
    }),
  /**
   * Renew one DeepSeek Web connection's credential.
   *
   * A browser opens and waits for the sign-in, so this call takes as long as
   * the operator takes to sign in. The credential that comes back is written
   * to the row, so there is nothing to paste.
   */
  renewDeepSeekCredential: async (id: string): Promise<{ renewed: boolean }> =>
    request<{ renewed: boolean }>(`/api/connections/${id}/renew-deepseek`, { method: 'POST' }),
  /**
   * Read a session out of a browser that is already signed in, and answer with
   * the cookie header to paste. The warning says what to do about a browser
   * that is still running.
   */
  geminiWebImportChrome: async (profile?: string): Promise<{ cookie: string; warning: string }> =>
    request<{ cookie: string; warning: string }>('/api/providers/gemini-web/import-chrome', {
      method: 'POST',
      body: JSON.stringify({ profile: profile ?? '' }),
    }),
  updateConnection: (
    id: string,
    payload:
      | Partial<ProviderConnection>
      | {
          isActive?: boolean | number
          assignedModel?: string | null
          providerSpecificData?: { assignedModel?: string | null; [key: string]: unknown }
          [key: string]: unknown
        }
  ) => {
    const body: Record<string, unknown> = { ...payload }
    if ('isActive' in body && typeof body.isActive === 'number') {
      body.isActive = body.isActive === 1
    }
    return request<{ success: boolean }>(`/api/connections/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    })
  },
  // reorderConnection moves a connection one slot up or down inside its
  // provider pool. The swap plus the 1..N renumbering happens server-side in a
  // single SQLite transaction, so the pool can never end up with two rows
  // sharing a priority. Reordering client-side with two PUTs could not: a
  // partial failure left a tied pair that no later click could undo.
  reorderConnection: (id: string, direction: 'up' | 'down') =>
    request<{ status: string; id: string; connections: ProviderConnection[] }>(
      `/api/connections/${encodeURIComponent(id)}/reorder`,
      {
        method: 'POST',
        body: JSON.stringify({ direction }),
      }
    ),
  deleteConnection: (id: string) =>
    request<{ success: boolean }>(`/api/connections/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  testConnection: (id: string) =>
    request<{ valid: boolean; error?: string }>(`/api/providers/${encodeURIComponent(id)}/test`, {
      method: 'POST',
    }),
}

export default connectionsEndpoints
