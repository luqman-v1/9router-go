// Typed API client for 9router-go Native Dashboard

export interface ProviderConnection {
  id: string
  provider: string
  authType: string
  name: string | null
  email: string | null
  priority: number | null
  isActive: number // 1 or 0
  data: string // JSON string
  createdAt: string
  updatedAt: string
  /** Masked hint of the stored credential; the raw secret is never listed. */
  apiKeyMasked?: string
  testStatus?: string | null
  lastError?: string | null
  displayName?: string | null
  assignedModel?: string | null
  providerSpecificData?: { assignedModel?: string | null; [key: string]: unknown }
}

export interface Combo {
  id: string
  name: string
  kind: string | null
  models: string // JSON array string
  strategy: string
  createdAt: string
  updatedAt: string
}

/** Resolved server-side capabilities for one catalog model. */
export interface ModelCaps {
  vision: boolean
  search: boolean
  reasoning: boolean
  contextWindow: number
  maxOutput: number
  /** Selectable thinking levels, or empty for a model without reasoning. */
  thinkingLevels: string[]
  free?: boolean
}

/**
 * A model the gateway has confirmed is gone upstream.
 *
 * `status` is "gone" when a live request drew an HTTP 410 naming the model,
 * and "retired" when a catalogue sync found it absent from a complete list.
 */
export interface ModelDeprecation {
  provider: string
  model: string
  status: 'gone' | 'retired' | ''
  message?: string
  /** The upstream's own replacement, when it named one. */
  successor?: string
  detectedAt?: string
}

/** Deprecations keyed "<provider>/<model>" — the same key a combo entry uses. */
export type ModelDeprecationMap = Record<string, ModelDeprecation>

export interface APIKey {
  id: string
  /**
   * The stored secret. Empty for a caller authenticated with an engine client
   * key, and for rows created before #199, whose plaintext was hashed away and
   * cannot be recovered — the UI falls back to `keyDisplay` in both cases.
   */
  key: string
  name: string | null
  machineId: string | null
  isActive: number
  createdAt: string
  /** Masked secret, e.g. `sk-6…008f`. Always present. */
  keyDisplay?: string
  /** Rate limits. 0 means unlimited. */
  rateLimitRpm?: number
  rateLimitTpm?: number
  rateLimitConcurrency?: number
  /** RFC3339 UTC. Empty means the key never expires. */
  expiresAt?: string
  lastUsedAt?: string
  usedCount?: number
  /** Free-form JSON object, e.g. { customerId, priceCents }. */
  metadata?: string
}

export interface APIKeyPolicy {
  rateLimitRpm?: number
  rateLimitTpm?: number
  rateLimitConcurrency?: number
  expiresAt?: string
  metadata?: string
  /** Renames the key. An empty string clears the stored name. */
  name?: string
}

export interface GuardrailPolicy {
  id: string
  tenantId: string
  scope: string
  scopeId: string
  name: string
  enabled: boolean
  config: string
  createdAt: string
  updatedAt: string
}

export interface GuardrailLog {
  id: string
  tenantId: string
  apiKeyId?: string
  provider?: string
  model?: string
  detector: string
  direction: string
  action: string
  severity?: string
  reason?: string
  findings?: string
  createdAt: string
}

export interface VaultStatus {
  enabled: boolean
  sealedCount: number
  plaintextCount: number
}

export interface ProviderStrategyConfig {
  proxyPoolId?: string
  rotateStrategy?: string
  strictModelAssignment?: boolean
  [key: string]: unknown
}

export interface Settings {
  requireApiKey?: boolean
  tunnelDashboardAccess?: boolean
  rtkEnabled?: boolean
  rtkMode?: 'simple' | 'advance' | string
  rtkIntensity?: 'minimal' | 'standard' | 'aggressive' | string
  rtkMaxLines?: number
  rtkMaxChars?: number
  rtkDeduplicate?: boolean
  rtkCategories?: Record<string, boolean>
  rtkFilters?: Record<string, boolean>
  rtkRawRetention?: 'never' | 'failures' | 'always' | string
  cavemanEnabled?: boolean
  cavemanMode?: 'simple' | 'advance' | string
  cavemanLevel?: string
  cavemanAutoClarity?: boolean
  cavemanLanguage?: string
  cavemanInputMode?: boolean
  cavemanPreserveKeywords?: string
  ponytailEnabled?: boolean
  ponytailLevel?: string
  adhdEnabled?: boolean
  adhdLevel?: string
  headroomEnabled?: boolean
  headroomUrl?: string
  headroomTimeoutMs?: number
  headroomKompress?: boolean
  semanticCacheEnabled?: boolean
  semanticCacheTTL?: number
  semanticCacheMaxEntries?: number
  autoUpdate?: boolean
  /** Dashboard security & SSO (profile page, Next parity). */
  requireLogin?: boolean
  hasPassword?: boolean
  authMode?: string
  ssoType?: string
  oidcIssuerUrl?: string
  oidcClientId?: string
  oidcScopes?: string
  oidcLoginLabel?: string
  samlEntryPoint?: string
  samlIssuer?: string
  samlCert?: string
  samlLoginLabel?: string
  samlAttributeEmail?: string
  samlAttributeName?: string
  /** Language, routing and network preferences (profile page). */
  language?: string
  fallbackStrategy?: string
  comboStrategy?: string
  stickyRoundRobinLimit?: number
  comboStickyRoundRobinLimit?: number
  /** Serve requests from the account that frees up first when all are cooling down. */
  forceFallback?: boolean
  enableObservability?: boolean
  outboundProxyEnabled?: boolean
  outboundProxyUrl?: string
  outboundNoProxy?: string
  providerStrategies?: Record<string, ProviderStrategyConfig>
  [key: string]: unknown
}

/**
 * One provider's outbound header overrides. `builtinHeaders` is what the
 * registry already sends on this provider's requests; `blockedHeaders` is the
 * set an override may never carry (credentials, framing, destination), so the
 * UI can disable those fields instead of discovering them through a 400.
 */
export interface ProviderOverridesResponse {
  provider: string
  headers: Record<string, string>
  builtinHeaders: Record<string, string>
  blockedHeaders: string[]
}

export interface TunnelStatusResponse {
  tunnel?: {
    enabled?: boolean
    settingsEnabled?: boolean
    tunnelUrl?: string
    publicUrl?: string
    running?: boolean
  }
  tailscale?: {
    enabled?: boolean
    settingsEnabled?: boolean
    tunnelUrl?: string
    running?: boolean
    loggedIn?: boolean
  }
  download?: {
    downloading?: boolean
    progress?: number
  }
}

export interface HeadroomStatusResponse {
  installed?: boolean
  running?: boolean
  python?: string | null
  localUrl?: boolean
  canStart?: boolean
  managedPid?: number | null
  path?: string | null
  version?: string | null
  extras?: { code?: boolean; ml?: boolean }
  url?: string
  error?: string
}

export interface HeadroomExtrasResponse {
  version?: string | null
  extras?: { code?: boolean; ml?: boolean }
  available?: string[]
  log?: string
  error?: string
}

export interface ProxyPool {
  id: string
  name: string
  type: 'http' | 'socks5' | 'vercel' | 'cloudflare' | 'deno' | string
  proxyUrl?: string
  urls?: string[]
  noProxy?: string
  strictProxy?: boolean
  isActive: boolean
  testStatus?: 'passed' | 'failed' | 'unknown' | string
  latency?: number
  lastTestedAt?: string | null
  boundConnectionCount?: number
  createdAt?: string
  updatedAt?: string
  [key: string]: unknown
}

/** Payload accepted by POST /api/connections (upstream POST /api/providers). */
export interface CreateConnectionPayload {
  id?: string
  provider: string
  authType: string
  name?: string
  displayName?: string
  apiKey?: string
  data?: string
  priority?: number
  testStatus?: 'active' | 'unknown' | string
  proxyPoolId?: string | null
  defaultModel?: string
  providerSpecificData?: Record<string, unknown>
}

/** Result of POST /api/providers/validate. supported=false means this backend
 * has no probe for the provider, so the UI must not claim the key is invalid. */
export interface ValidateProviderResult {
  supported: boolean
  valid: boolean
  error?: string
}

export interface ProviderNode {
  id: string
  type: string
  name: string
  prefix?: string
  apiType?: string
  baseUrl?: string
  /**
   * The custom tail of the provider id, without the "openai-compatible-chat-"
   * literal in front of it. Empty for a node created before the field existed.
   */
  urlSuffix?: string
  /** True when the id tail is the random fallback rather than a chosen value. */
  urlSuffixGenerated?: boolean
  createdAt?: string
  updatedAt?: string
}


export interface SystemVersionInfo {
  currentVersion: string
  latestVersion?: string
  hasUpdate?: boolean
  downloadUrl?: string
  releaseNotes?: string
  goVersion?: string
  os?: string
  arch?: string
  checkedAt?: string
}
export interface FreebuffInitiateResponse {
  loginUrl: string
  authCode: string
  fingerprintId: string
  fingerprintHash: string
  expiresAt: string
}

export interface FreebuffPollResponse {
  status: 'authorized' | 'pending' | 'expired'
  connectionId?: string
  user?: {
    id?: string
    name?: string
    email?: string
  }
}

export interface ConnectionQuotaInfo {
  used?: number
  total?: number
  resetAt?: string
  remainingPercentage?: number
  remaining?: number
  unlimited?: boolean
  displayName?: string
  name?: string
  modelKey?: string
}

export interface ConnectionUsageResponse {
  plan?: string
  quotas?: Record<string, ConnectionQuotaInfo> | ConnectionQuotaInfo[]
  error?: string
}

// Codex reset credits. `selectionToken` is the field the dashboard selects
// with; `code` carries the typed refusal the server raised (e.g. no_credit vs
// nothing_to_reset) so the modal can say which happened.
export interface CodexResetCredit {
  selectionToken: string
  resetType?: string
  status?: string
  grantedAt?: string
  expiresAt?: string
  title?: string
  description?: string
}

export interface CodexResetCreditListResponse {
  credits: CodexResetCredit[]
  availableCount: number
  error?: string
  code?: string
}

export interface CodexResetCreditConsumeResponse {
  outcome: 'reset' | 'alreadyRedeemed' | 'error'
  selectionToken?: string
  idempotencyKey?: string
  error?: string
  code?: string
}

export interface FreebuffSessionSwitchResponse {
  status: 'active'
  currentModel: string
  instanceId?: string
  expiresAt?: string
  /** False when the session was already on the requested model. */
  switched: boolean
  /** Freebucks the server credited back for the released session. */
  freebucksRefund?: number
}

export interface FreebuffSessionStatusResponse {
  status: 'active' | 'none' | 'unauthorized' | 'banned' | 'country_blocked'
  /** Account this report describes — set so the UI can name it. */
  connectionId?: string
  connectionName?: string
  currentModel?: string
  instanceId?: string
  expiresAt?: string
  accessTier?: string
  countryCode?: string
  /** Present when the server refuses this region, even on an active session. */
  countryBlockReason?: string
  /** Sessions the account has left today, for the model in currentModel. */
  rateLimit?: {
    model?: string
    limit?: number
    recentCount?: number
    poolLabel?: string
    resetAt?: string
    resetTimeZone?: string
  }
  freebucks?: {
    balance?: number
    daily?: {
      limit?: number
      spent?: number
      remaining?: number
      resetAt?: string
      resetTimeZone?: string
    }
    wallet?: {
      balance?: number
      monthlyBonus?: number
    }
    planId?: string | null
    prices?: Record<string, number>
    [key: string]: unknown
  }
}
export interface RequireLoginResponse {
  requireLogin: boolean
  tunnelDashboardAccess?: boolean
  tunnelUrl?: string
  tailscaleUrl?: string
  hasPassword?: boolean
  authMode?: string
  authenticated?: boolean
}

export interface LoginResponse {
  success: boolean
  mustChangePassword?: boolean
  error?: string
  retryAfter?: number
  resetHint?: string
  remainingBeforeLock?: number
}

export interface SemanticCacheStats {
  memoryEntries: number
  dbEntries: number
  hits: number
  misses: number
  hitRate: string
  tokensSaved: number
}

export interface PromptCacheProviderStats {
  requests: number
  totalRequests?: number
  cachedRequests?: number
  inputTokens: number
  cachedTokens: number
  cacheCreationTokens: number
}

export interface PromptCacheMetrics {
  totalRequests: number
  requestsWithCacheControl: number
  totalInputTokens: number
  totalCachedTokens: number
  totalCacheCreationTokens: number
  tokensSaved: number
  estimatedCostSaved: number
  byProvider: Record<string, PromptCacheProviderStats>
  byModel?: Record<string, PromptCacheProviderStats>
  lastUpdated: string
}

export interface CacheTrendPoint {
  timestamp: string
  requests: number
  cachedRequests: number
  inputTokens: number
  cachedTokens: number
  cacheCreationTokens: number
}

export interface CacheStatsResponse {
  semanticCache: SemanticCacheStats
  promptCache: PromptCacheMetrics | null
  trend: CacheTrendPoint[]
  idempotency: {
    activeKeys: number
    windowMs: number
  }
  config?: {
    semanticCacheEnabled: boolean
  }
}

export interface CacheEntryMeta {
  id: string
  signature: string
  model: string
  hit_count: number
  tokens_saved: number
  created_at: string
  expires_at: string
}

export interface CacheEntriesResponse {
  entries: CacheEntryMeta[]
  pagination: {
    page: number
    limit: number
    total: number
    totalPages: number
  }
}

export interface CompressionModeStats {
  count: number
  tokensSaved: number
  avgSavingsPct: number
  skipped?: number
}

export interface CompressionProviderStats {
  count: number
  tokensSaved: number
}

export interface CompressionModelStats {
  count: number
  tokensSaved: number
  avgSavingsPct: number
}

export interface CompressionHourBucket {
  hour: string
  count: number
  tokensSaved: number
}

export interface CompressionRealUsage {
  requestsWithReceipts: number
  promptTokens: number
  completionTokens: number
  totalTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  estimatedUsdSaved: number
  bySource?: Record<string, number>
}

export interface CompressionTopSaver {
  requestId: string
  timestamp: string
  provider: string
  model: string
  mode: string
  originalTokens: number
  compressedTokens: number
  tokensSaved: number
  savingsPct: number
  durationMs: number
  estimatedUsd: number
}

export interface CompressionAnalyticsSummary {
  totalRequests: number
  totalTokensSaved: number
  avgSavingsPct: number
  avgDurationMs: number
  byMode: Record<string, CompressionModeStats>
  byProvider: Record<string, CompressionProviderStats>
  byModel?: Record<string, CompressionModelStats>
  last24h: CompressionHourBucket[]
  totalSkipped?: number
  bySkipReason?: Record<string, number>
  validationFallbacks: number
  realUsage: CompressionRealUsage
  roiTokensPerMs?: number
  topSavers?: CompressionTopSaver[]
}

export function isAuthenticated(): boolean {
  if (typeof window === 'undefined') return false
  if (sessionStorage.getItem('9router_auth') === 'true' || localStorage.getItem('9router_auth') === 'true') {
    return true
  }
  if (typeof document !== 'undefined' && document.cookie.includes('auth_token=')) {
    return true
  }
  return false
}

// Helper to get an auth header only when a key was explicitly stored.
// Dashboard sessions authenticate with the HttpOnly cookie.
export function isUsableAPIKey(value: string): boolean {
  return /^[!-~]+$/.test(value.trim()) && value.trim().length > 0
}

export function getStoredAPIKey(): string {
  if (typeof localStorage === 'undefined') return ''
  const value = (localStorage.getItem('9router_key') || '').trim()
  return isUsableAPIKey(value) ? value : ''
}

export function getAuthHeaders(): Record<string, string> {
  const token = getStoredAPIKey()
  return {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  }
}

export function formatApiError(value: unknown, fallback = 'Unknown error'): string {
  if (typeof value === 'string' && value.trim()) return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value)) {
    for (const entry of value) {
      const message = formatApiError(entry, '')
      if (message) return message
    }
  } else if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    for (const field of ['message', 'error', 'detail', 'details', 'errors']) {
      if (field in record) {
        const message = formatApiError(record[field], '')
        if (message) return message
      }
    }
  }
  if (value && typeof value === 'object') {
    try {
      return JSON.stringify(value)
    } catch {
      // Fall through to the caller-provided message.
    }
  }
  return fallback
}

export async function responseErrorMessage(
  response: Response,
  fallback = `Request failed with status ${response.status}`,
): Promise<string> {
  let text = ''
  try {
    text = await response.text()
  } catch {
    return fallback
  }
  if (!text.trim()) return fallback
  try {
    return formatApiError(JSON.parse(text), fallback)
  } catch {
    return text
  }
}
export type UnauthorizedListener = () => void
const unauthorizedListeners: Set<UnauthorizedListener> = new Set()

export function onUnauthorized(listener: UnauthorizedListener): () => void {
  unauthorizedListeners.add(listener)
  return () => {
    unauthorizedListeners.delete(listener)
  }
}

let unauthorizedTimer: number | null = null

export function handleUnauthorized(path?: string) {
  // Do not redirect for public auth endpoints or if already on login page
  if (
    path === '/api/auth/login' ||
    path === '/api/auth/status' ||
    path === '/api/settings/require-login' ||
    path === '/api/changelog'
  ) {
    return
  }

  if (typeof window !== 'undefined' && window.location.pathname === '/login') {
    return
  }

  // Clear stale local auth session tokens and invalid stored keys
  if (typeof sessionStorage !== 'undefined') {
    sessionStorage.removeItem('9router_auth')
  }
  if (typeof localStorage !== 'undefined') {
    localStorage.removeItem('9router_auth')
    localStorage.removeItem('9router_key')
  }

  // Debounce multiple concurrent 401 responses
  if (unauthorizedTimer !== null) return
  unauthorizedTimer = setTimeout(() => {
    unauthorizedTimer = null
    if (unauthorizedListeners.size > 0) {
      for (const listener of unauthorizedListeners) {
        try {
          listener()
        } catch (e) {
          console.error('Error in onUnauthorized listener:', e)
        }
      }
    } else if (typeof window !== 'undefined' && window.location.pathname !== '/login') {
      window.history.replaceState({ tab: 'login' }, '', '/login')
    }
  }, 50)
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = {
    ...getAuthHeaders(),
    ...(options.headers as Record<string, string> || {}),
  }
  const res = await fetch(path, { ...options, headers })
  if (res.status === 401) {
    handleUnauthorized(path)
  }
  if (!res.ok) throw new Error(await responseErrorMessage(res))
  return res.json()
}

export function normalizeLastError(err: unknown): string | null {
  if (err == null) return null
  if (typeof err === 'string') return err
  if (typeof err === 'object') {
    const obj = err as Record<string, unknown>
    if (typeof obj.message === 'string' && obj.message) return obj.message
    if (typeof obj.error === 'string' && obj.error) return obj.error
    if (typeof obj.msg === 'string' && obj.msg) return obj.msg
    if (typeof obj.details === 'string' && obj.details) return obj.details
    try {
      return JSON.stringify(err)
    } catch {
      return String(err)
    }
  }
  return String(err)
}

function numberField(source: Record<string, unknown>, key: string): number | null {
  const value = source[key]
  return typeof value === 'number' ? value : null
}

function stringField(source: Record<string, unknown>, key: string): string | null {
  const value = source[key]
  return typeof value === 'string' ? value : null
}

export function normalizeConnection(c: ProviderConnection): ProviderConnection {
  let parsed: Record<string, unknown> = {}
  if (typeof c.data === 'string' && c.data) {
    try {
      parsed = JSON.parse(c.data)
    } catch {}
  }
  const wire = c as unknown as Record<string, unknown>
  const wireSpecific =
    c.providerSpecificData && typeof c.providerSpecificData === 'object'
      ? (c.providerSpecificData as Record<string, unknown>)
      : undefined
  const parsedSpecific =
    parsed.providerSpecificData && typeof parsed.providerSpecificData === 'object'
      ? (parsed.providerSpecificData as Record<string, unknown>)
      : Object.keys(parsed).length > 0
        ? parsed
        : undefined

  const specific = {
    ...(parsedSpecific || {}),
    ...(wireSpecific || {}),
  }
  return {
    ...parsed,
    ...c,
    providerSpecificData: specific,
    lastError: normalizeLastError(c.lastError) || normalizeLastError(parsed.lastError) || null,
    errorCode: numberField(parsed, 'errorCode') ?? numberField(wire, 'errorCode'),
    rateLimitedUntil: stringField(parsed, 'rateLimitedUntil') ?? stringField(wire, 'rateLimitedUntil'),
    testStatus: stringField(wire, 'testStatus') || stringField(parsed, 'testStatus'),
    expiresAt: stringField(parsed, 'expiresAt'),
  } as ProviderConnection
}

// Connections API
export const api = {
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

  // Provider Nodes (Custom Endpoints)
  getProviderNodes: async () => {
    const res = await request<{ nodes: ProviderNode[] }>('/api/provider-nodes')
    return res.nodes || []
  },
  createProviderNode: async (payload: {
    name: string
    prefix: string
    apiType?: string
    baseUrl?: string
    type?: string
    /** Replaces the random uuid in the provider id. Omit to keep it generated. */
    urlSuffix?: string
  }) => {
    const res = await request<{ node: ProviderNode }>('/api/provider-nodes', {
      method: 'POST',
      body: JSON.stringify(payload),
    })
    return res.node
  },
  validateProviderNode: (payload: { baseUrl: string; apiKey: string; type?: string; modelId?: string }) =>
    request<{ valid: boolean; error?: string; method?: string; dimensions?: number }>(
      '/api/provider-nodes/validate',
      {
        method: 'POST',
        body: JSON.stringify(payload),
      }
    ),
  /**
   * Update a node. Sending a different `urlSuffix` renames the provider id and
   * carries the node's connections, models and history with it, so the returned
   * node carries the new id and the caller must navigate to it.
   */
  updateProviderNode: (
    id: string,
    payload: { name: string; prefix: string; apiType?: string; baseUrl: string; urlSuffix?: string }
  ) =>
    request<{ node: ProviderNode }>(`/api/provider-nodes/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  deleteProviderNode: (id: string) =>
    request<{ success: boolean }>(`/api/provider-nodes/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  /** List models from a connection's upstream (compatible nodes; upstream GET /api/providers/[id]/models). */
  getConnectionModels: (connectionId: string) =>
    request<{ provider: string; connectionId: string; models: Array<{ id?: string; name?: string; model?: string } | string> }>(
      `/api/providers/${encodeURIComponent(connectionId)}/models`,
    ),

  // Combos
  getCombos: async () => {
    const res = await request<any>('/api/combos')
    return Array.isArray(res) ? res : (res.combos || [])
  },
  createCombo: (payload: { id?: string; name: string; kind?: string; models: string | string[]; strategy?: string }) =>
    request<{ success: boolean; id: string }>('/api/combos', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateCombo: (id: string, payload: Partial<Combo> | any) =>
    request<{ success: boolean }>(`/api/combos/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  deleteCombo: (id: string) =>
    request<{ success: boolean }>(`/api/combos/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  // API Keys
  getApiKeys: () => request<APIKey[]>('/api/keys'),
  createApiKey: (payload: { name?: string; machineId?: string; key?: string }) =>
    request<{ success: boolean; id: string; key: string }>('/api/keys', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  deleteApiKey: (id: string) =>
    request<{ success: boolean }>(`/api/keys/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  toggleApiKey: (id: string) =>
    request<{ success: boolean; isActive: boolean }>(`/api/keys/${encodeURIComponent(id)}/toggle`, {
      method: 'PUT',
    }),

  /** Mints a replacement secret, keeping the row's id, policy and history. */
  rotateApiKey: (id: string) =>
    request<{ status: string; id: string; key: string }>(`/api/keys/${encodeURIComponent(id)}/rotate`, {
      method: 'POST',
    }),
  setApiKeyActive: (id: string, isActive: boolean) =>
    request<{ status: string; id: string; isActive: boolean }>(
      `/api/keys/${encodeURIComponent(id)}/toggle`,
      { method: 'PUT', body: JSON.stringify({ isActive }) },
    ),

  // F-1/F-14 per-key governance. Omitted fields are left unchanged server-side,
  // so a partial update never resets a limit the operator did not touch.
  updateApiKeyPolicy: (id: string, policy: APIKeyPolicy) =>
    request<{ status: string; id: string }>(`/api/keys/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(policy),
    }),
  getApiKeyModels: (id: string) =>
    request<{ id: string; models: string[] }>(`/api/keys/${encodeURIComponent(id)}/models`),
  setApiKeyModels: (id: string, models: string[]) =>
    request<{ id: string; models: string[] }>(`/api/keys/${encodeURIComponent(id)}/models`, {
      method: 'PUT',
      body: JSON.stringify({ models }),
    }),

  // F-5 credential vault
  getVaultStatus: () => request<VaultStatus>('/api/vault/status'),
  rotateVault: (masterKey: string) =>
    request<{ status: string; rewrapped: number; failures: string[] }>('/api/vault/rotate', {
      method: 'POST',
      body: JSON.stringify({ masterKey }),
    }),

  // F-4 guardrails
  getGuardrailPolicies: (scope: string, scopeId = '') =>
    request<GuardrailPolicy[]>(
      `/api/guardrails/policies?scope=${encodeURIComponent(scope)}&scopeId=${encodeURIComponent(scopeId)}`
    ),
  createGuardrailPolicy: (payload: {
    name: string
    scope: string
    scopeId?: string
    enabled?: boolean
    config: string
  }) =>
    request<GuardrailPolicy>('/api/guardrails/policies', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateGuardrailPolicy: (
    id: string,
    payload: {
      name: string
      scope: string
      scopeId?: string
      enabled?: boolean
      config: string
    }
  ) =>
    request<GuardrailPolicy>(`/api/guardrails/policies/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  deleteGuardrailPolicy: (id: string) =>
    request<{ status: string; id: string }>(
      `/api/guardrails/policies/${encodeURIComponent(id)}`,
      { method: 'DELETE' }
    ),
  getGuardrailLogs: (limit = 100) =>
    request<GuardrailLog[]>(`/api/guardrails/logs?limit=${encodeURIComponent(String(limit))}`),
  getMetrics: () => request<string>('/api/metrics'),

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
  deleteCustomModel: (key: string) =>
    request<{ success: boolean }>(`/api/models/custom/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    }),
  saveDisabledModels: (provider: string, modelIds: string[]) =>
    request<{ success: boolean }>(`/api/models/disabled/${encodeURIComponent(provider)}`, {
      method: 'PUT',
      body: JSON.stringify(modelIds),
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
  getAntigravityAuthorizeUrl: (redirectUri: string) =>
    request<{ url: string; redirectUrl: string; authUrl: string; state: string; redirectUri: string }>(
      `/api/oauth/antigravity/authorize?redirect_uri=${encodeURIComponent(redirectUri)}`,
    ),
  antigravityExchange: (code: string, redirectUri: string, state?: string) =>
    request<{ success: boolean; error?: string }>('/api/oauth/antigravity/exchange', {
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
    request<{ status: string; connectionId?: string; error?: string }>('/api/oauth/device/poll', {
      method: 'POST',
      body: JSON.stringify({ provider, device_code: deviceCode, session }),
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
    request<{ authUrl: string; url: string; state: string }>(`/api/oauth/kiro/social-authorize?provider=${provider}`),
  kiroSocialExchange: (code: string) =>
    request<{ success: boolean; id?: string; error?: string }>('/api/oauth/kiro/social-exchange', {
      method: 'POST',
      body: JSON.stringify({ code }),
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

  // Usage & Telemetry
  getUsageStats: (period = 'today') => request<any>(`/api/usage/stats?period=${encodeURIComponent(period)}`),
  getRequestDetails: (limit = 50, offset = 0) =>
    request<any>(`/api/usage/request-details?limit=${limit}&offset=${offset}`),
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
    const conns = await api.getConnections().catch(() => [])
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
    const fallback = await api.getProvidersClient()
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
  // Tunnel & Tailscale
  getTunnelStatus: () =>
    request<TunnelStatusResponse>('/api/tunnel/status').catch(() => ({
      tunnel: { enabled: false, running: false, tunnelUrl: '', publicUrl: '' },
      tailscale: { enabled: false, running: false, tunnelUrl: '', loggedIn: false },
    })),
  enableTunnel: () =>
    request<{ success?: boolean; tunnelUrl?: string; publicUrl?: string; error?: string }>('/api/tunnel/enable', {
      method: 'POST',
    }),
  disableTunnel: () =>
    request<{ success?: boolean; error?: string }>('/api/tunnel/disable', {
      method: 'POST',
    }),
  checkTailscale: () =>
    request<{ installed?: boolean; loggedIn?: boolean; tunnelUrl?: string }>('/api/tunnel/tailscale-check').catch(() => ({
      installed: false,
      loggedIn: false,
    })),
  enableTailscale: () =>
    request<{ success?: boolean; tunnelUrl?: string; needsLogin?: boolean; authUrl?: string; funnelNotEnabled?: boolean; error?: string }>('/api/tunnel/tailscale-enable', {
      method: 'POST',
    }),
  disableTailscale: () =>
    request<{ success?: boolean; error?: string }>('/api/tunnel/tailscale-disable', {
      method: 'POST',
    }),

  // Headroom
  getHeadroomStatus: () =>
    request<HeadroomStatusResponse>('/api/headroom/status'),
  getHeadroomExtras: (log = false) =>
    request<HeadroomExtrasResponse>(`/api/headroom/extras${log ? '?log=1' : ''}`),
  startHeadroom: () =>
    request<{ success?: boolean; pid?: number; error?: string }>('/api/headroom/start', {
      method: 'POST',
    }),
  stopHeadroom: () =>
    request<{ success?: boolean; error?: string }>('/api/headroom/stop', {
      method: 'POST',
    }),
  restartHeadroom: () =>
    request<{ success?: boolean; pid?: number; error?: string }>('/api/headroom/restart', {
      method: 'POST',
    }),
  installHeadroomExtras: (extras: string[]) =>
    request<HeadroomExtrasResponse>('/api/headroom/extras', {
      method: 'POST',
      body: JSON.stringify({ extras }),
    }),
  uninstallHeadroomExtras: (extras: string[]) =>
    request<HeadroomExtrasResponse>('/api/headroom/extras', {
      method: 'DELETE',
      body: JSON.stringify({ extras }),
    }),
  // Proxy Pools
  getProxyPools: (includeUsage = true) =>
    request<{ proxyPools: ProxyPool[] }>(`/api/proxy-pools${includeUsage ? '?includeUsage=true' : ''}`)
      .then((res) => res.proxyPools || [])
      .catch(() => []),
  createProxyPool: (payload: Partial<ProxyPool>) =>
    request<ProxyPool>('/api/proxy-pools', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateProxyPool: (id: string, payload: Partial<ProxyPool>) =>
    request<{ success: boolean }>(`/api/proxy-pools/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  deleteProxyPool: (id: string) =>
    request<{ success: boolean }>(`/api/proxy-pools/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  testProxyPool: (id: string) =>
    request<{ success: boolean; status?: string; latency?: number; error?: string }>(
      `/api/proxy-pools/${encodeURIComponent(id)}/test`,
      { method: 'POST' }
    ),
  deployVercelRelay: (payload: { vercelToken: string; projectName?: string }) =>
    request<{ success?: boolean; proxyUrl?: string; deployUrl?: string; error?: string }>('/api/proxy-pools/vercel-deploy', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  deployCloudflareRelay: (payload: { accountId: string; apiToken: string; projectName?: string }) =>
    request<{ success?: boolean; proxyUrl?: string; deployUrl?: string; error?: string }>('/api/proxy-pools/cloudflare-deploy', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  deployDenoRelay: (payload: { denoToken: string; orgDomain: string; projectName?: string }) =>
    request<{ success?: boolean; proxyUrl?: string; deployUrl?: string; error?: string }>('/api/proxy-pools/deno-deploy', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  // CLI Tools status
  getCliToolsStatuses: () =>
    request<Record<string, { installed?: boolean; version?: string | null; has9Router?: boolean } | null>>(
      '/api/cli-tools/all-statuses'
    ).catch(() => ({})),
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
    getCacheStats: (trendHours = 24) =>
      request<CacheStatsResponse>(`/api/cache?trendHours=${trendHours}`),
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
    getCompressionAnalytics: (since = '24h') =>
      request<CompressionAnalyticsSummary>(`/api/analytics/compression?since=${since}`),
  logout: async () => {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch {}
    sessionStorage.removeItem('9router_auth')
    localStorage.removeItem('9router_auth')
  },
}
