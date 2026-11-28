import type { TunnelStatusResponse } from '../types'
import { request } from '../runtime'

const tunnelEndpoints = {
  // Tunnel & Tailscale
  getTunnelStatus: () =>
    request<TunnelStatusResponse>('/api/tunnel/status').catch(() => ({
      tunnel: { enabled: false, running: false, tunnelUrl: '', publicUrl: '' },
      tailscale: { enabled: false, running: false, tunnelUrl: '', loggedIn: false },
    })),
  enableTunnel: (payload?: {
    mode?: string
    shortId?: string
    customDomain?: string
    token?: string
    cfApiToken?: string
    cfAccountId?: string
    cfZone?: string
    workerUrl?: string
  }) =>
    request<{
      success?: boolean
      tunnelUrl?: string
      publicUrl?: string
      error?: string
      mode?: string
      shortId?: string
      customDomain?: string
      token?: string
      cfApiToken?: string
    }>('/api/tunnel/enable', {
      method: 'POST',
      body: JSON.stringify(payload ?? {}),
    }),
  disableTunnel: () =>
    request<{ success?: boolean; error?: string }>('/api/tunnel/disable', {
      method: 'POST',
    }),
  repairTunnelOrigin: () =>
    request<{ success?: boolean; hostname?: string; port?: number; error?: string }>('/api/tunnel/repair-origin', {
      method: 'POST',
    }),
  checkTailscale: () =>
    request<{ installed?: boolean; loggedIn?: boolean; tunnelUrl?: string }>('/api/tunnel/tailscale-check').catch(() => ({
      installed: false,
      loggedIn: false,
    })),
  enableTailscale: (payload?: { authKey?: string; authMode?: string }) =>
    request<{
      success?: boolean
      tunnelUrl?: string
      needsLogin?: boolean
      authUrl?: string
      funnelNotEnabled?: boolean
      error?: string
      repairTunnelOrigin?: boolean
    }>('/api/tunnel/tailscale-enable', {
      method: 'POST',
      body: JSON.stringify(payload ?? {}),
    }),
  disableTailscale: () =>
    request<{ success?: boolean; error?: string }>('/api/tunnel/tailscale-disable', {
      method: 'POST',
    }),
}

export default tunnelEndpoints
