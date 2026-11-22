// OAuth callback handoff protocol (dashboard tab <-> provider callback tab).
//
// Flow: user clicks Login -> dashboard opens provider auth URL in a new tab
// with redirect_uri=<dashboard-origin>/callback (+ a `state`). The provider
// redirects the new tab there; the callback page writes the result to
// localStorage and broadcasts it. The dashboard modal (same origin) picks it
// up, restores the saved PKCE/session values, fills the callback input and
// auto-submits — no manual copy-paste.
//
// Storage ciphers (localStorage keys):
//   pendingKey  -> OAuthPending[]  (written at authorize time)
//   callbackKey -> OAuthCallback   (written by the /callback page, consumed once)
export const OAUTH_CHANNEL = '9router-oauth'
export const OAUTH_PENDING_KEY = '9router.oauth.pending.v1'
export const OAUTH_CALLBACK_KEY = '9router.oauth.callback.v1'
// Pending sessions older than this are ignored (stale Login clicks).
export const PENDING_TTL_MS = 15 * 60 * 1000

export interface OAuthPending {
  provider: string
  state: string
  /** PKCE / session verifier (cline, pkce, zed, mimo). */
  verifier?: string
  /** redirectUri echoed back at exchange (cline, pkce, authcode). */
  redirectUri?: string
  /** custom flow extras (trae loginTraceId, zed systemId, gitlab baseUrl/client). */
  extra?: Record<string, string>
  at: number
}

export interface OAuthCallback {
  state: string
  /** raw pasted value: full query string or bare code/token. */
  raw: string
  error?: string
  errorDesc?: string
  at: number
}

/** Minimal storage surface so the logic is unit-testable without a DOM. */
export interface KVStore {
  getItem(k: string): string | null
  setItem(k: string, v: string): void
  removeItem(k: string): void
}

function readJSON<T>(store: KVStore, key: string): T | null {
  try {
    const raw = store.getItem(key)
    if (!raw) return null
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

/** Save (or refresh) a pending session; prunes stale entries. */
export function savePending(store: KVStore, p: Omit<OAuthPending, 'at'>, now = Date.now()): OAuthPending[] {
  const list = loadPendings(store, now).filter((e) => !(e.provider === p.provider && e.state === p.state))
  list.push({ ...p, at: now })
  try {
    store.setItem(OAUTH_PENDING_KEY, JSON.stringify(list.slice(-10)))
  } catch {
    /* storage full/blocked — handoff degrades to manual paste */
  }
  return list
}

export function loadPendings(store: KVStore, now = Date.now()): OAuthPending[] {
  const list = readJSON<OAuthPending[]>(store, OAUTH_PENDING_KEY)
  if (!Array.isArray(list)) return []
  return list.filter((e) => e && typeof e.state === 'string' && now - (e.at || 0) < PENDING_TTL_MS)
}

export function clearPending(store: KVStore, provider: string, state: string): void {
  const list = loadPendings(store).filter((e) => !(e.provider === provider && e.state === state))
  try {
    store.setItem(OAUTH_PENDING_KEY, JSON.stringify(list))
  } catch {
    /* ignore */
  }
}

/** Find the pending session for an incoming callback: same state wins,
 * otherwise a fresh same-provider session (some flows omit state echo). */
export function matchPending(pendings: OAuthPending[], provider: string, state: string): OAuthPending | null {
  if (state) {
    const byState = pendings.filter((e) => e.state === state)
    if (byState.length > 0) {
      const sameProvider = byState.find((e) => e.provider === provider)
      return sameProvider || byState[byState.length - 1]
    }
  }
  const fresh = pendings.filter((e) => e.provider === provider)
  return fresh.length > 0 ? fresh[fresh.length - 1] : null
}

export function writeCallback(store: KVStore, cb: Omit<OAuthCallback, 'at'>, now = Date.now()): void {
  try {
    store.setItem(OAUTH_CALLBACK_KEY, JSON.stringify({ ...cb, at: now }))
  } catch {
    /* ignore */
  }
}

export function readCallback(store: KVStore): OAuthCallback | null {
  const cb = readJSON<OAuthCallback>(store, OAUTH_CALLBACK_KEY)
  if (!cb || typeof cb.raw !== 'string') return null
  return cb
}

export function clearCallback(store: KVStore): void {
  try {
    store.removeItem(OAUTH_CALLBACK_KEY)
  } catch {
    /* ignore */
  }
}

/** Parse a callback URL (query and/or #fragment) into state/raw/error parts. */
export function parseCallbackURL(href: string): { state: string; raw: string; error: string; errorDesc: string } {
  const out = { state: '', raw: '', error: '', errorDesc: '' }
  let u: URL
  try {
    u = new URL(href)
  } catch {
    return out
  }
  const q = u.searchParams
  const h = new URLSearchParams(u.hash.replace(/^#/, ''))
  const pick = (k: string) => q.get(k) || h.get(k) || ''
  out.error = pick('error') || pick('error_code') || pick('errorCode')
  out.errorDesc = pick('error_description') || pick('error_desc') || pick('message')
  out.state = pick('state')
  if (out.error) return out
  const code = pick('code') || pick('access_token') || pick('token') || pick('refreshToken') || pick('refresh_token')
  // Count non-state params: a lone code/token hands over just the value;
  // multi-param bundles (e.g. Trae refreshToken+loginHost) hand over the full
  // query so per-provider submit parsers see everything.
  let extra = 0
  const known = new Set(['code', 'access_token', 'token', 'refreshToken', 'refresh_token', 'state'])
  for (const [k, v] of [...q.entries(), ...h.entries()]) {
    if (!known.has(k) && v !== '') extra++
    else if (known.has(k) && k !== 'state' && v !== '' && v !== code) extra++
  }
  if (code && extra === 0) {
    out.raw = code
  } else if (u.search.length > 1 || u.hash.length > 1) {
    out.raw = (u.search + u.hash).replace(/^[?#&]+/, '')
  }
  return out
}

/** Callback URL pinned to the dashboard origin so the callback tab is
 * same-origin with the dashboard (localStorage + BroadcastChannel work). */
export function dashboardCallbackURL(origin: string): string {
  return `${origin.replace(/\/+$/, '')}/callback`
}

/** Antigravity mirrors upstream Next.js: OAuth always returns to the browser
 * host's loopback interface while preserving the dashboard's listening port. */
export function oauthLoopbackCallbackURL(port: string, secure: boolean): string {
  const callbackPort = port || (secure ? '443' : '80')
  return `http://localhost:${callbackPort}/callback`
}

/** Swap localhost <-> 127.0.0.1 while keeping scheme and port for cross-origin
 * loopback postMessage between the dashboard and /callback tabs. */
export function loopbackPeerOrigin(origin: string): string | null {
  try {
    const u = new URL(origin)
    if (u.hostname === 'localhost') u.hostname = '127.0.0.1'
    else if (u.hostname === '127.0.0.1') u.hostname = 'localhost'
    else return null
    return u.origin
  } catch {
    return null
  }
}

/** Codex's OAuth client (app_EMoamEEZ73f0CkXaXp7hrann) has exactly one
 * registered loopback redirect URI. auth.openai.com validates redirect_uri
 * during the authorize step and answers `invalid_authorize_request` for any
 * other value — including the dashboard's own /callback URL and the right port
 * with the wrong path. The loopback server behind it is started by
 * `POST /api/oauth/codex/start-proxy`. */
export const CODEX_REDIRECT_URI = 'http://localhost:1455/auth/callback'
