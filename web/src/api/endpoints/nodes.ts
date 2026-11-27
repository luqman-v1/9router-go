import type { ProviderNode, ProviderNodeAvailability, ProviderNodeLimits, ProviderNodeLimitsPatch } from '../types'
import { request } from '../runtime'

const nodesEndpoints = {
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
  /** Checks a candidate prefix and URL suffix for immediate availability. */
  checkProviderNodeAvailability: (payload: { prefix?: string; urlSuffix?: string; type?: string; apiType?: string }) =>
    request<ProviderNodeAvailability>('/api/provider-nodes/availability', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  deleteProviderNode: (id: string) =>
    request<{ success: boolean }>(`/api/provider-nodes/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  /**
   * Save one node's capacity limits. Separate from updateProviderNode because
   * that call requires name and prefix on every request: routing a limits edit
   * through it would mean an empty field in the UI becomes a 400 for the whole
   * save, and a limits save should never be able to rename a node.
   */
  setProviderNodeLimits: (id: string, patch: ProviderNodeLimitsPatch) =>
    request<{ status: string; id: string; limits: ProviderNodeLimits }>(
      `/api/provider-nodes/${encodeURIComponent(id)}/limits`,
      {
        method: 'PUT',
        body: JSON.stringify(patch),
      }
    ),
  /** Move a node's quota mark to the end of its history. History is kept. */
  resetProviderNodeQuota: (id: string) =>
    request<{ status: string; id: string; quotaResetId: number; limits: ProviderNodeLimits }>(
      `/api/provider-nodes/${encodeURIComponent(id)}/quota/reset`,
      { method: 'POST' }
    ),
  /** List models from a connection's upstream (compatible nodes; upstream GET /api/providers/[id]/models). */
  getConnectionModels: (connectionId: string) =>
    request<{ provider: string; connectionId: string; models: Array<{ id?: string; name?: string; model?: string; context_length?: number; contextLength?: number; max_completion_tokens?: number; maxOutputTokens?: number; capabilities?: { vision?: boolean; reasoning?: boolean } } | string> }>(
      `/api/providers/${encodeURIComponent(connectionId)}/models`,
    ),
}

export default nodesEndpoints
