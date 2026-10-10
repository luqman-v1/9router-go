/**
 * Pure logic behind the Hermes per-profile card.
 *
 * Kept out of `HermesToolCard.svelte` because all of it is unit-testable
 * without a DOM, and because two of the pieces (the yaml builder and the
 * "is this a 9router endpoint" guard) have to agree with the Go handlers
 * under `internal/handlers/media/` — both sides rewrite `~/.hermes/config.yaml`.
 */

import { isDelegationRole } from './hermesRoles'

export type HermesProfileStatus = 'configured' | 'other' | 'not_configured'

/** Shape of the profile rows the card needs; the API type is wider. */
export interface HermesProfileSummary {
  name: string
  isDefault: boolean
  displayName: string | null
  command: string
  baseUrl: string | null
  has9Router: boolean
}

export interface HermesSelectionMap {
  /** The main model (`model.default` in config.yaml). */
  default: string
  /** Role id -> model, for the roles in `HERMES_ROLES`. */
  roles: Record<string, string>
}

export interface EndpointContext {
  /** Window origin; anything else on this host is not this gateway. */
  localOrigin: string
  tunnelUrl?: string
  tailscaleUrl?: string
  /** Public endpoints the operator configured, if the caller knows them. */
  publicEndpoints?: string[]
}

/**
 * `localhost` is not interchangeable with `127.0.0.1` for a model client: the
 * first resolves to IPv6 `::1` on some hosts, and Hermes fails to connect.
 * Everything else is left untouched.
 */
function normalizeLocalhost(url: string): string {
  return url.replace('://localhost', '://127.0.0.1')
}

/** Trailing-slash-free, lowercase origin+path, for endpoint comparison. */
function endpointKey(url: string): string {
  return normalizeLocalhost(url.trim()).replace(/\/+$/, '').toLowerCase()
}

/**
 * The endpoint as written in `config.yaml`: the server appends `/v1`, so the
 * field the user edits is stored without it.
 */
export function normalizeHermesEndpoint(raw: string, localOrigin: string): string {
  const value = raw.trim() || localOrigin
  const normalized = normalizeLocalhost(value).replace(/\/+$/, '')
  return normalized.endsWith('/v1') ? normalized.slice(0, -'/v1'.length) : normalized
}

/**
 * Whether an endpoint is this gateway: the local origin, a tunnel/Tailscale
 * URL, or a configured public endpoint. "Apply to all" refuses anything else,
 * because the endpoint field mirrors whatever the active profile currently
 * points at and can hold a foreign provider URL (openrouter, ...) whose key
 * would then be written into every other profile.
 */
export function isKnown9RouterEndpoint(raw: string, ctx: EndpointContext): boolean {
  const value = raw.trim()
  if (!value) return false
  const candidates = [ctx.localOrigin, ctx.tunnelUrl, ctx.tailscaleUrl, ...(ctx.publicEndpoints ?? [])]
  const target = endpointKey(normalizeHermesEndpoint(value, ctx.localOrigin))
  // A config may or may not carry the trailing /v1 the server appends, so
  // both spellings are compared.
  const targetWithV1 = `${target}/v1`
  return candidates.some((c) => {
    if (!c) return false
    const key = endpointKey(normalizeHermesEndpoint(c, ctx.localOrigin))
    return key === target || key === targetWithV1
  })
}

/** Home directory of a profile: the default one is `~/.hermes` itself. */
export function hermesHomeDir(profileName: string): string {
  return profileName === 'default' ? '~/.hermes' : `~/.hermes/profiles/${profileName}`
}

/**
 * Three-state status for the dot next to a profile button, matching the rule
 * the backend uses when it flags a profile as 9router-wired.
 */
export function profileStatus(p: HermesProfileSummary, ctx: EndpointContext): HermesProfileStatus {
  if (p.has9Router || (p.baseUrl && isKnown9RouterEndpoint(p.baseUrl, ctx))) return 'configured'
  if (p.baseUrl) return 'other'
  return 'not_configured'
}

/** Roles with a non-blank model, trimmed — the empty ones inherit the default. */
export function activeRoleModels(roles: Record<string, string>): Array<{ role: string; model: string }> {
  return Object.entries(roles)
    .filter(([role, model]) => role !== 'default' && model?.trim())
    .map(([role, model]) => ({ role, model: model.trim() }))
}

/**
 * The `config.yaml` the card shows in the manual-config modal: exactly what
 * the Go handler writes, so the two paths are interchangeable.
 */
export function hermesConfigYaml(selections: HermesSelectionMap, baseUrl: string, runHint: string): string {
  let yaml = `# Run this profile: ${runHint}\n`
  yaml += `model:\n  default: "${selections.default.trim()}"\n  provider: "custom"\n  base_url: "${baseUrl}"\n  api_key: \${OPENAI_API_KEY}\n`

  const roles = activeRoleModels(selections.roles)
  const delegation = roles.find((r) => isDelegationRole(r.role))
  if (delegation) {
    yaml += `delegation:\n  model: "${delegation.model}"\n  provider: "custom"\n  base_url: "${baseUrl}"\n  api_key: \${OPENAI_API_KEY}\n`
  }

  const auxiliary = roles.filter((r) => !isDelegationRole(r.role))
  if (auxiliary.length > 0) {
    yaml += 'auxiliary:\n'
    for (const { role, model } of auxiliary) {
      yaml += `  ${role}:\n    provider: "custom"\n    model: "${model}"\n    base_url: "${baseUrl}"\n    api_key: \${OPENAI_API_KEY}\n`
    }
  }
  return yaml
}

/** The `.env` companion file: the one secret the yaml references by name. */
export function hermesEnvContent(apiKey: string): string {
  return `OPENAI_API_KEY=${apiKey}\n`
}