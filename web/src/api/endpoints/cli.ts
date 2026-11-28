import type { CliToolSettingsApplyPayload, CliToolSettingsApplyResponse, CliToolSettingsResponse, HermesApplyPayload, HermesApplyResponse, HermesProfilesResponse, HermesResetResponse, HermesSettingsResponse } from '../types'
import { request } from '../runtime'

const cliEndpoints = {
  // CLI Tools status
  getCliToolsStatuses: () =>
    request<Record<string, { installed?: boolean; version?: string | null; has9Router?: boolean } | null>>(
      '/api/cli-tools/all-statuses'
    ).catch(() => ({})),

  // Hermes per-profile configuration.
  // Hermes is usually absent, so the profile list degrades to an empty list
  // exactly like the status map above — the card hides its profile picker then.
  getHermesProfiles: () =>
    request<HermesProfilesResponse>('/api/cli-tools/hermes-profiles').catch(() => ({ profiles: [] })),
  getHermesSettings: (profile?: string) =>
    request<HermesSettingsResponse>(
      `/api/cli-tools/hermes-settings${profile ? `?profile=${encodeURIComponent(profile)}` : ''}`
    ),
  // `applyToAll` carries the endpoint to every 9router-wired profile; the
  // client splits it out because the two shapes share one endpoint.
  applyHermesSettings: (payload: HermesApplyPayload) =>
    request<HermesApplyResponse>('/api/cli-tools/hermes-settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),
  applyHermesToAll: (payload: HermesApplyPayload) =>
    request<HermesApplyResponse>('/api/cli-tools/hermes-settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...payload, applyToAll: true }),
    }),
  resetHermesSettings: (profile?: string) =>
    request<HermesResetResponse>('/api/cli-tools/hermes-settings', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(profile ? { profile } : {}),
    }),
  // CLI Tools (Claude, Codex, OpenCode, Droid, Crush, Kilo, Cline, Pi, OMP)
  getCliToolSettings: (tool: string) =>
    request<CliToolSettingsResponse>(`/api/cli-tools/${encodeURIComponent(tool)}-settings`),
  applyCliToolSettings: (tool: string, payload: CliToolSettingsApplyPayload) =>
    request<CliToolSettingsApplyResponse>(`/api/cli-tools/${encodeURIComponent(tool)}-settings`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),
  resetCliToolSettings: (tool: string, baseUrl?: string) => {
    const q = baseUrl ? `?baseUrl=${encodeURIComponent(baseUrl)}` : ''
    return request<CliToolSettingsApplyResponse>(
      `/api/cli-tools/${encodeURIComponent(tool)}-settings${q}`,
      {
        method: 'DELETE',
      }
    )
  },
}

export default cliEndpoints
