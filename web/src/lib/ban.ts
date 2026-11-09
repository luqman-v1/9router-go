import type { ProviderConnection } from '../api/client'

/** Ban signals the Go backend persists in conn.lastError. */
const DISABLED_PHRASES = [
  'this service has been disabled in this account for violation',
  'this service has been disabled in this account',
]

/** Hosts allowed as appeal / verification targets. */
const APPEAL_HOSTS = new Set(['forms.gle', 'accounts.google.com', 'docs.google.com'])

/**
 * Classify a connection as banned from the same persisted signal the Go side
 * uses: a deactivation sentence, or tos_violation together with cloudcode-pa.
 * Never key on a bare tos_violation string.
 */
export function isBannedConnection(conn: ProviderConnection): boolean {
  const err = (conn.lastError || '').toLowerCase()
  if (!err) return false
  if (DISABLED_PHRASES.some((phrase) => err.includes(phrase))) return true
  return err.includes('tos_violation') && err.includes('cloudcode-pa')
}

/**
 * Validate an appeal/verification URL before rendering it.
 * - must be https://
 * - host must be forms.gle, accounts.google.com, or docs.google.com
 * - docs.google.com requires a /forms/ pathname
 * JSON-escaped `\/` is normalized to `/` before checking.
 */
export function isAppealUrlValid(url: string): boolean {
  let parsed: URL
  try {
    parsed = new URL(normalizeSlashes(url))
  } catch {
    return false
  }
  if (parsed.protocol !== 'https:') return false
  if (!APPEAL_HOSTS.has(parsed.hostname)) return false
  if (parsed.hostname === 'docs.google.com' && !parsed.pathname.startsWith('/forms/')) return false
  return true
}

/**
 * Return the connection's verificationUrl when it passes validation, otherwise
 * undefined. Never render a tampered or invalid URL.
 */
export function appealUrl(conn: ProviderConnection): string | undefined {
  const raw = conn.providerSpecificData?.verificationUrl
  if (typeof raw !== 'string' || !raw) return undefined
  const normalized = normalizeSlashes(raw)
  return isAppealUrlValid(normalized) ? normalized : undefined
}

/** Risk notice applies to OAuth / subscription provider connections. */
export function showsProviderRiskNotice(conn: ProviderConnection): boolean {
  return conn.authType === 'oauth'
}

function normalizeSlashes(url: string): string {
  return url.replace(/\\\//g, '/')
}
