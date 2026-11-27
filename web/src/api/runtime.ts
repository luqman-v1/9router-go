import type { ProviderConnection } from './types'

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
  // Do not redirect for public auth endpoints, for the storage stats probe, or
  // if already on the login page. The stats probe is read-only and always
  // admin-gated on the server, so on an install whose dashboard is open without
  // a session it answers 401 while every other call succeeds — bouncing the
  // operator to the login screen and clearing the stored key there was wrong;
  // the storage card reports the 401 and offers a retry instead. A genuinely
  // expired session still logs out through any other request.
  if (
    path === '/api/auth/login' ||
    path === '/api/auth/status' ||
    path === '/api/settings/require-login' ||
    path === '/api/changelog' ||
    path === '/api/settings/database/stats'
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

/**
 * A non-2xx API response. `status` rides along with the message because a
 * caller branching on 404 (a vanished resource) cannot recover the code from
 * the text — the message is written for humans and can change.
 */
export class ApiError extends Error {
  readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = { ...getAuthHeaders() }
  // A FormData body must carry its own boundary. Forcing application/json here
  // makes the server fail to parse the upload, so the header is dropped for
  // exactly that case.
  if (options.body instanceof FormData) delete headers['Content-Type']
  Object.assign(headers, (options.headers as Record<string, string>) || {})
  const res = await fetch(path, { ...options, headers })
  if (res.status === 401) {
    handleUnauthorized(path)
  }
  if (!res.ok) throw new ApiError(await responseErrorMessage(res), res.status)
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
    lastErrorModel: stringField(parsed, 'lastErrorModel') ?? stringField(wire, 'lastErrorModel'),
    lastErrorSource: stringField(parsed, 'lastErrorSource') ?? stringField(wire, 'lastErrorSource'),
  } as ProviderConnection
}
