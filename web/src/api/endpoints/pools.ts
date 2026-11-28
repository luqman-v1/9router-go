import type { ProxyPool } from '../types'
import { request } from '../runtime'

const poolsEndpoints = {
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
  deployNetlifyRelay: (payload: { netlifyToken: string; projectName?: string }) =>
    request<{ success?: boolean; proxyUrl?: string; deployUrl?: string; error?: string }>('/api/proxy-pools/netlify-deploy', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
}

export default poolsEndpoints
