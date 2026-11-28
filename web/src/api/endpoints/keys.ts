import type { APIKey, APIKeyPolicy, ApiKeyBudget, GuardrailLog, GuardrailPolicy, Plan, PlanInput, VaultStatus } from '../types'
import { request } from '../runtime'

const keysEndpoints = {
  // API Keys
  getApiKeys: () => request<APIKey[]>('/api/keys'),
  createApiKey: (payload: {
    name?: string
    machineId?: string
    key?: string
    /** Lifetime token budget. 0 means unlimited. */
    tokenQuota?: number
    /** Context window one request from this key may use, in tokens. 0 means the model's own window. */
    contextLimit?: number
  }) =>
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
  /**
   * Clears a key's spent token budget (#365). The server stamps the usage mark
   * the budget is measured from, so the key starts over while its history stays
   * intact, and the response carries the spend it now has (0).
   */
  resetApiKeyQuota: (id: string) =>
    request<{ status: string; id: string; usedTokens: number; tokenQuota: number; quotaResetId: number }>(
      `/api/keys/${encodeURIComponent(id)}/reset-quota`,
      { method: 'POST' },
    ),
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
  /**
   * The key's period budget as the engine sees it — ceiling, open-window spend
   * and status. The spend is read-only context for the policy modal; it is not
   * part of what a save writes.
   */
  getKeyBudget: (id: string) => request<ApiKeyBudget>(`/api/keys/${encodeURIComponent(id)}/budget`),
  setApiKeyModels: (id: string, models: string[]) =>
    request<{ id: string; models: string[] }>(`/api/keys/${encodeURIComponent(id)}/models`, {
      method: 'PUT',
      body: JSON.stringify({ models }),
    }),

  // F-3 plan templates: the reusable policy a key inherits from.
  getPlans: () => request<{ plans: Plan[] }>('/api/plans'),
  getPlan: (id: string) => request<Plan>(`/api/plans/${encodeURIComponent(id)}`),
  createPlan: (plan: PlanInput) =>
    request<Plan>('/api/plans', { method: 'POST', body: JSON.stringify(plan) }),
  updatePlan: (id: string, plan: PlanInput) =>
    request<Plan>(`/api/plans/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(plan),
    }),
  deletePlan: (id: string) =>
    request<{ status: string; id: string }>(`/api/plans/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  getPlanKeys: (id: string) =>
    request<{ id: string; keys: number }>(`/api/plans/${encodeURIComponent(id)}/keys`),

  // F-5 credential vault
  getVaultStatus: () => request<VaultStatus>('/api/vault/status'),
  rotateVault: (masterKey: string) =>
    request<{ status: string; rewrapped: number; failures: string[] }>('/api/vault/rotate', {
      method: 'POST',
      body: JSON.stringify({ masterKey }),
    }),

  // F-4 guardrails
  // No scopeId lists the whole scope (including subject-scoped policies); an
  // explicit value — even an empty one — resolves one exact scope tuple.
  getGuardrailPolicies: (scope: string, scopeId?: string) =>
    request<GuardrailPolicy[]>(
      `/api/guardrails/policies?scope=${encodeURIComponent(scope)}` +
        (scopeId === undefined ? '' : `&scopeId=${encodeURIComponent(scopeId)}`)
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
}

export default keysEndpoints
