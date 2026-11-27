import type { FreebuffInitiateResponse, FreebuffPollResponse, FreebuffSessionStatusResponse, FreebuffSessionSwitchResponse, SystemVersionInfo } from '../types'
import { ApiError, getAuthHeaders, getStoredAPIKey, handleUnauthorized, request, responseErrorMessage } from '../runtime'

const oauthEndpoints = {
  // OAuth Flows
  initiateFreebuff: () => request<FreebuffInitiateResponse>('/api/oauth/freebuff/initiate', { method: 'POST' }),
  pollFreebuff: (fingerprintId: string, fingerprintHash: string, expiresAt?: number | string) =>
    request<FreebuffPollResponse>('/api/oauth/freebuff/poll', {
      method: 'POST',
      body: JSON.stringify({ fingerprintId, fingerprintHash, expiresAt }),
    }),
  getFreebuffSessionStatus: (connectionId?: string) =>
    request<FreebuffSessionStatusResponse>(
      `/api/oauth/freebuff/session${connectionId ? `?connectionId=${encodeURIComponent(connectionId)}` : ''}`
    ),
  switchFreebuffSession: (model: string, connectionId?: string) =>
    request<FreebuffSessionSwitchResponse>('/api/oauth/freebuff/session/switch', {
      method: 'POST',
      body: JSON.stringify({ connectionId, model }),
    }),
  getAntigravityAuthorizeUrl: (redirectUri: string, proxy = false) =>
    request<{ url: string; redirectUrl: string; authUrl: string; state: string; redirectUri: string }>(
      `/api/oauth/antigravity/authorize?redirect_uri=${encodeURIComponent(redirectUri)}${proxy ? '&proxy=1' : ''}`,
    ),
  antigravityExchange: (code: string, redirectUri: string, state?: string) =>
    request<{ success: boolean; error?: string }>('/api/oauth/antigravity/exchange', {
      method: 'POST',
      body: JSON.stringify({ code, redirectUri, state }),
    }),
  getAGYACPAuthorizeUrl: (redirectUri: string, proxy = false) =>
    request<{ url: string; redirectUrl: string; authUrl: string; state: string; redirectUri: string }>(
      `/api/oauth/agy-acp/authorize?redirect_uri=${encodeURIComponent(redirectUri)}${proxy ? '&proxy=1' : ''}`,
    ),
  agyACPExchange: (code: string, redirectUri: string, state?: string) =>
    request<{ success: boolean; error?: string }>('/api/oauth/agy-acp/exchange', {
      method: 'POST',
      body: JSON.stringify({ code, redirectUri, state }),
    }),
  getClineAuthorizeUrl: (provider: string, redirectUri?: string) =>
    request<{ url: string; authUrl: string; state: string; codeVerifier: string; codeChallenge: string; redirectUri: string }>(
      `/api/oauth/cline/authorize?provider=${encodeURIComponent(provider)}${redirectUri ? `&redirect_uri=${encodeURIComponent(redirectUri)}` : ''}`
    ),
  clineExchange: (provider: string, code: string, codeVerifier: string, redirectUri?: string, name?: string) =>
    request<{ status: string; connectionId: string; error?: string }>('/api/oauth/cline/exchange', {
      method: 'POST',
      body: JSON.stringify({ provider, code, codeVerifier, redirectUri, name }),
    }),
  // MiniMax Code lease auth: borrow the desktop app's sign-in over its local
  // broker instead of a browser login. No refresh token is stored.
  minimaxLeaseStatus: (providerId: string, dataDir?: string) =>
    request<{
      found: boolean;
      dataDir?: string;
      status?: string;
      generation?: number;
      expiresAtMs?: number;
      error?: string;
      message?: string;
    }>(
      `/api/oauth/${encodeURIComponent(providerId)}/lease-status${dataDir ? `?data_dir=${encodeURIComponent(dataDir)}` : ''}`
    ),
  minimaxLeaseConnect: (providerId: string, dataDir?: string) =>
    request<{ success: boolean; connection?: { id: string; provider: string }; reused?: boolean }>(
      `/api/oauth/${encodeURIComponent(providerId)}/lease-connect`,
      { method: 'POST', body: JSON.stringify({ dataDir }) },
    ),
  importOAuthToken: (provider: string, accessToken: string, refreshToken?: string, name?: string) =>
    request<{ id: string; connection: string; error?: string }>(`/api/oauth/${encodeURIComponent(provider)}/import`, {
      method: 'POST',
      body: JSON.stringify({ accessToken, refreshToken, name }),
    }),
  pkceAuthorize: (provider: string, opts?: { redirectUri?: string; baseUrl?: string; clientId?: string }) => {
    const q = new URLSearchParams({ provider })
    if (opts?.redirectUri) q.set('redirect_uri', opts.redirectUri)
    if (opts?.baseUrl) q.set('baseUrl', opts.baseUrl)
    if (opts?.clientId) q.set('clientId', opts.clientId)
    return request<{ url: string; authUrl: string; state: string; codeVerifier: string; codeChallenge: string; redirectUri: string }>(
      `/api/oauth/pkce/authorize?${q.toString()}`
    )
  },
  pkceExchange: (payload: { provider: string; code: string; codeVerifier: string; redirectUri?: string; state?: string; baseUrl?: string; clientId?: string; clientSecret?: string; name?: string }) =>
    request<{ status: string; connectionId: string; error?: string }>('/api/oauth/pkce/exchange', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  // Codex's OAuth client has exactly one registered loopback redirect URI, so
  // the callback lands on a fixed-port listener the server owns rather than on
  // the dashboard's /callback page. start-proxy binds it, the login completes
  // server-side, and poll-status reports the outcome.
  codexStartProxy: (params: { appPort: string; state: string; codeVerifier: string; redirectUri: string; name?: string }) => {
    const q = new URLSearchParams({
      app_port: params.appPort,
      state: params.state,
      code_verifier: params.codeVerifier,
      redirect_uri: params.redirectUri,
    })
    if (params.name) q.set('name', params.name)
    return request<{ success: boolean; serverSide?: boolean; port?: number; reason?: string }>(
      `/api/oauth/codex/start-proxy?${q.toString()}`
    )
  },
  codexPollStatus: (state: string) =>
    request<{ status: string; connectionId?: string; email?: string; error?: string }>(
      `/api/oauth/codex/poll-status?state=${encodeURIComponent(state)}`
    ),
  getOAuthCallbackResult: (state: string) =>
    request<{ found: boolean; state?: string; raw?: string; error?: string; errorDesc?: string }>(
      `/api/oauth/callback-result?state=${encodeURIComponent(state)}`
    ),
  codexStopProxy: () => request<{ success: boolean }>('/api/oauth/codex/stop-proxy'),
  authcodeAuthorize: (provider: string, redirectUri?: string) => {
    const q = new URLSearchParams({ provider })
    if (redirectUri) q.set('redirect_uri', redirectUri)
    return request<{ url: string; authUrl: string; state: string; redirectUri: string }>(
      `/api/oauth/authcode/authorize?${q.toString()}`
    )
  },
  authcodeExchange: (payload: { provider: string; code: string; redirectUri?: string; state?: string; name?: string }) =>
    request<{ status: string; connectionId: string; error?: string }>('/api/oauth/authcode/exchange', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  customAuthorize: (provider: 'trae' | 'windsurf' | 'zed', redirectUri?: string) =>
    request<{ url: string; authUrl: string; state?: string; codeVerifier?: string; redirectUri?: string; loginTraceId?: string; systemId?: string }>(
      `/api/oauth/${provider}/authorize${redirectUri ? `?redirect_uri=${encodeURIComponent(redirectUri)}` : ''}`
    ),
  customExchange: (provider: 'trae' | 'windsurf' | 'zed', payload: { code: string; state?: string; codeVerifier?: string; systemId?: string; name?: string }) =>
    request<{ status: string; connectionId: string; error?: string }>(`/api/oauth/${provider}/exchange`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  deviceStart: (provider: string, opts?: { region?: string; startUrl?: string; authMethod?: string }) =>
    request<{ device_code: string; user_code: string; verification_uri: string; verification_uri_complete: string; expires_in: number; interval: number; session: Record<string, unknown> }>(
      '/api/oauth/device/start',
      { method: 'POST', body: JSON.stringify({ provider, ...opts }) }
    ),
  devicePoll: (provider: string, deviceCode: string, session?: Record<string, unknown>) =>
    request<{ status: string; connectionId?: string; error?: string; interval?: number }>('/api/oauth/device/poll', {
      method: 'POST',
      body: JSON.stringify({ provider, device_code: deviceCode, session }),
    }),
  // Free-pi sign-in is a GitHub device flow behind a consent gate, so it gets
  // its own three calls rather than the generic deviceStart/devicePoll pair:
  // initiate refuses to contact upstream until the caller asserts that this
  // user accepted this consent version.
  freePiConsent: () =>
    request<{ provider: string; version: string; text: string }>('/api/oauth/free-pi/consent'),
  freePiInitiate: (consentVersion: string) =>
    request<{
      sessionId: string
      userCode: string
      verificationUri: string
      verificationUriComplete?: string
      interval: number
      expiresIn: number
      consentVersion: string
    }>('/api/oauth/free-pi/initiate', {
      method: 'POST',
      body: JSON.stringify({ consentAccepted: true, consentVersion }),
    }),
  freePiPoll: (sessionId: string) =>
    request<{ status: string; connectionId?: string; retryAfter?: number; message?: string }>(
      '/api/oauth/free-pi/poll',
      { method: 'POST', body: JSON.stringify({ sessionId }) }
    ),
  // The dashboard equivalent of free-pi-cli's /close-other-session.
  freePiResetSession: (connectionId: string) =>
    request<{ released: boolean }>('/api/oauth/free-pi/session/reset', {
      method: 'POST',
      body: JSON.stringify({ connectionId }),
    }),
  // The banner creative. Serving it does not report an impression: that only
  // happens after the component paints it, via freePiImpression.
  freePiAd: (connectionId: string) =>
    request<{
      ad: {
        adId: string
        clickUrl?: string
        source?: string
        creative: { title?: string; body?: string; cta?: string; imageUrl?: string }
      } | null
    }>(`/api/free-pi/ad?connectionId=${encodeURIComponent(connectionId)}`),
  freePiImpression: (connectionId: string, adId: string) =>
    request<{ recorded: boolean }>('/api/free-pi/ad/impression', {
      method: 'POST',
      body: JSON.stringify({ connectionId, adId }),
    }),
  freebuffAd: (connectionId: string) =>
    request<{
      ad: {
        adId: string
        clickUrl?: string
        creative: {
          title?: string
          body?: string
          cta?: string
          imageUrl?: string
          sponsorName?: string
          brandColor?: string
        }
      } | null
    }>(`/api/freebuff/ad?connectionId=${encodeURIComponent(connectionId)}`),
  freebuffImpression: (connectionId: string, adId: string) =>
    request<{ recorded: boolean }>('/api/freebuff/ad/impression', {
      method: 'POST',
      body: JSON.stringify({ connectionId, adId }),
    }),

  kiroImport: (refreshToken: string) =>
    request<{ success: boolean; id?: string; error?: string }>('/api/oauth/kiro/import', {
      method: 'POST',
      body: JSON.stringify({ refreshToken }),
    }),
  kiroImportCliProxy: (jsonPayload: string) =>
    request<{ success: boolean; id?: string; error?: string }>('/api/oauth/kiro/import-cli-proxy', {
      method: 'POST',
      body: JSON.stringify({ json: jsonPayload }),
    }),
  kiroAutoImport: () =>
    request<{ found: boolean; refreshToken?: string; error?: string }>('/api/oauth/kiro/auto-import'),
  kiroApiKey: (apiKey: string, region?: string) =>
    request<{ success: boolean; id?: string; error?: string }>('/api/oauth/kiro/api-key', {
      method: 'POST',
      body: JSON.stringify({ apiKey, region }),
    }),
  kiroSocialAuthorize: (provider: 'google' | 'github') =>
    request<{ authUrl: string; url: string; state: string; codeVerifier: string }>(`/api/oauth/kiro/social-authorize?provider=${provider}`),
  kiroSocialExchange: (code: string, codeVerifier: string, provider?: 'google' | 'github') =>
    request<{ success: boolean; id?: string; error?: string }>('/api/oauth/kiro/social-exchange', {
      method: 'POST',
      body: JSON.stringify({ code, codeVerifier, provider }),
    }),
  cursorImport: (accessToken: string, machineId: string) =>
    request<{ success: boolean; id: string; error?: string }>('/api/oauth/cursor/import', {
      method: 'POST',
      body: JSON.stringify({ accessToken, machineId }),
    }),
  cursorAutoImport: () => request<{ success: boolean; id: string; error?: string }>('/api/oauth/cursor/auto-import'),
  kimchiAuthorize: (redirectUri?: string) => request<{ url: string; authUrl: string; state: string }>(`/api/oauth/kimchi/authorize${redirectUri ? `?redirect_uri=${encodeURIComponent(redirectUri)}` : ''}`),
  kimchiExchange: (code: string) =>
    request<{ status: string; connectionId: string; error?: string }>('/api/oauth/kimchi/exchange', {
      method: 'POST',
      body: JSON.stringify({ code }),
    }),
  gitlabPAT: (token: string, baseUrl?: string) =>
    request<{ success: boolean; error?: string }>('/api/oauth/gitlab/pat', {
      method: 'POST',
      body: JSON.stringify({ token, baseUrl }),
    }),
  iflowCookie: (cookie: string) =>
    request<{ success: boolean; error?: string }>('/api/oauth/iflow/cookie', {
      method: 'POST',
      body: JSON.stringify({ cookie }),
    }),
  mimoAuthorize: (redirectUri?: string) => {
    const q = redirectUri ? `?redirect_uri=${encodeURIComponent(redirectUri)}` : ''
    return request<{ url: string; authUrl: string; codeVerifier: string }>(`/api/oauth/xiaomi-mimo/authorize${q}`)
  },
  mimoExchange: (code: string, codeVerifier: string) =>
    request<{ status: string; connectionId: string; error?: string }>('/api/oauth/xiaomi-mimo/exchange', {
      method: 'POST',
      body: JSON.stringify({ code, codeVerifier }),
    }),
  getSystemVersion: () => request<SystemVersionInfo>('/api/version'),
  /**
   * Uploads the white-label logo as multipart/form-data. The body is a
   * FormData rather than JSON, so it is posted directly instead of through
   * `request`, which always serializes its options as JSON.
   *
   * The auth header is sent without `Content-Type`: fetch sets that itself
   * for a FormData body, together with the multipart boundary. A manually
   * set `application/json` here would override it and leave the server
   * parsing a multipart body as JSON.
   */
  uploadLogo: async (file: File): Promise<{ status: string; appLogoUrl: string }> => {
    const token = getStoredAPIKey()
    const body = new FormData()
    body.append('file', file)
    const res = await fetch('/api/settings/logo', {
      method: 'POST',
      ...(token ? { headers: { Authorization: `Bearer ${token}` } } : {}),
      body,
    })
    if (res.status === 401) handleUnauthorized('/api/settings/logo')
    if (!res.ok) throw new ApiError(await responseErrorMessage(res), res.status)
    return res.json()
  },
  checkUpdate: () => request<SystemVersionInfo>('/api/version/check'),
  triggerUpdate: () =>
    request<{ status: string; message?: string; version?: string }>('/api/version/update', {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  shutdownServer: () =>
    request<{ success?: boolean; status?: string; message?: string }>('/api/version/shutdown', {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  getChangelog: async (): Promise<string> => {
    try {
      const res = await fetch('/api/changelog', { headers: getAuthHeaders() })
      if (res.ok) {
        const text = await res.text()
        if (text && text.trim().length > 0) return text
      }
    } catch {}
    try {
      const res = await fetch('https://raw.githubusercontent.com/luqman-v1/9router-go/main/CHANGELOG.md')
      if (res.ok) {
        const text = await res.text()
        if (text && text.trim().length > 0) return text
      }
    } catch {}
    const res = await fetch('https://raw.githubusercontent.com/decolua/9router/refs/heads/master/CHANGELOG.md')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return await res.text()
  },
}

export default oauthEndpoints
