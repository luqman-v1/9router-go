import type { JailbreakDoc, JailbreakExcludes, JailbreakImportEntry, JailbreakImportResult, JailbreakKeyword, JailbreakMatch, JailbreakModelConfig, JailbreakSyncResult, JailbreakSyncSource } from '../types'
import { request } from '../runtime'

const jailbreakEndpoints = {
  // Jailbreak
  getJailbreak: () => request<JailbreakDoc>('/api/jailbreak'),
  saveJailbreak: (doc: {
    prompt: string
    geminiSafety: boolean
    source?: string
    keywords?: JailbreakKeyword[]
    excludes?: JailbreakKeyword[]
  }) =>
    request<{ success: boolean }>('/api/jailbreak', {
      method: 'PUT',
      body: JSON.stringify(doc),
    }),
  listJailbreakPresets: () => request<JailbreakDoc[]>('/api/jailbreak/presets'),
  saveJailbreakPreset: (preset: {
    id?: string
    name: string
    prompt: string
    geminiSafety: boolean
    enabled: boolean
    source?: string
    kind?: string
    keywords?: JailbreakKeyword[]
    excludes?: JailbreakKeyword[]
    order?: number
    syncOrigin?: string
    syncPath?: string
  }) =>
    request<{ success: boolean }>('/api/jailbreak/presets', {
      method: 'PUT',
      body: JSON.stringify(preset),
    }),
  deleteJailbreakPreset: (id: string) =>
    request<{ success: boolean }>(`/api/jailbreak/presets/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  listJailbreakModels: () => request<Record<string, JailbreakModelConfig>>('/api/jailbreak/models'),
  // Preset auto-sync: the GitHub collections the library follows. One pass
  // rewrites only the presets it wrote itself, so an operator's hand-made
  // preset is never touched and a file upstream dropped is reported as
  // orphaned rather than deleted.
  listJailbreakSyncSources: () =>
    request<{ sources: JailbreakSyncSource[] }>('/api/jailbreak/sync/sources'),
  saveJailbreakSyncSources: (sources: JailbreakSyncSource[]) =>
    request<{ sources: JailbreakSyncSource[]; ok: boolean }>('/api/jailbreak/sync/sources', {
      method: 'PUT',
      body: JSON.stringify({ sources }),
    }),
  runJailbreakSync: () =>
    request<{ outcomes: { source: JailbreakSyncSource; result: JailbreakSyncResult }[]; ok: boolean }>(
      '/api/jailbreak/sync/run',
      { method: 'POST' },
    ),
  // GitHub credential and mirror. The api.github.com quota is 60 anonymous
  // requests an hour and 5,000 with a token, so a library following several
  // collections needs one. The token is never returned by the GET — only
  // whether one is set and where it came from.
  getJailbreakGithub: () =>
    request<{
      tokenSet: boolean
      source: 'dashboard' | 'environment' | 'none'
      apiBase: string
      rawBase: string
      envToken: boolean
      envMirror: boolean
      /** Bodies held for a conditional (If-None-Match) re-fetch. */
      cached: number
      cacheMax: number
    }>('/api/jailbreak/github'),
  setJailbreakGithubToken: (token: string) =>
    request<{ ok: boolean; tokenSet: boolean }>('/api/jailbreak/github/token', {
      method: 'PUT',
      body: JSON.stringify({ token }),
    }),
  setJailbreakModel: (id: string, presetId: string, enabled: boolean) =>
    request<{ success: boolean }>('/api/jailbreak/models', {
      method: 'PUT',
      body: JSON.stringify({ id, presetId, enabled }),
    }),
  // One API key's bindings, already filtered to that key: the map is keyed by
  // the bare model id, so a row here is the key's own override of whatever the
  // global binding above says.
  listJailbreakKeyBindings: (keyId: string) =>
    request<Record<string, JailbreakModelConfig>>(
      `/api/jailbreak/keys/bindings?keyId=${encodeURIComponent(keyId)}`,
    ),
  // Arm a preset for one model under one key only. The same `enabled` toggle
  // convention the global write uses: omitted arms it, presetId "off"/"none"
  // means turn it off rather than delete the row.
  setJailbreakKeyModel: (keyId: string, id: string, presetId: string, enabled: boolean) =>
    request<{ ok: boolean; keyId: string; id: string; enabled: boolean; presetId: string }>(
      '/api/jailbreak/keys/model',
      {
        method: 'PUT',
        body: JSON.stringify({ keyId, id, presetId, enabled }),
      },
    ),
  // Drop one key-scoped binding outright. The per-key delete button calls this;
  // unlike setJailbreakKeyModel('off'), which only turns the row off, this
  // removes it. The same model's global binding is untouched.
  deleteJailbreakKeyModel: (keyId: string, id: string) =>
    request<{ ok: boolean; keyId: string; id: string }>('/api/jailbreak/keys/model', {
      method: 'DELETE',
      body: JSON.stringify({ keyId, id }),
    }),
  deleteJailbreakModel: (id: string) =>
    request<{ ok: boolean; id: string }>('/api/jailbreak/models', {
      method: 'DELETE',
      body: JSON.stringify({ id }),
    }),
  getJailbreakExcludes: () => request<JailbreakExcludes>('/api/jailbreak/excludes'),
  saveJailbreakExcludes: (excludes: JailbreakKeyword[]) =>
    request<JailbreakExcludes>('/api/jailbreak/excludes', {
      method: 'PUT',
      body: JSON.stringify({ excludes }),
    }),
  importJailbreak: (target: string, name?: string) =>
    request<JailbreakImportResult>('/api/jailbreak/import', {
      method: 'POST',
      body: JSON.stringify({ target, name: name || undefined }),
    }),
  uploadJailbreakPreset: (file: File) => {
    const form = new FormData()
    form.append('file', file)
    return request<{ ok: boolean; preset: JailbreakImportEntry }>(
      '/api/jailbreak/presets/upload',
      { method: 'POST', body: form },
    )
  },
  previewJailbreak: (id: string, messages: { role: string; content: string }[]) =>
    request<{
      injected: boolean
      reason: string
      presetId?: string
      presetName?: string
      prompt?: string
      messages: { role: string; content: string }[]
    }>('/api/jailbreak/preview', {
      method: 'POST',
      body: JSON.stringify({ id, messages }),
    }),
  /**
   * The same preview, resolved under one client API key's scope.
   *
   * The gateway's preview endpoint already reads `apiKeyId` from the body — the
   * tester needs both scopes of one id to diff them, and passing the key here is
   * what makes the second body the one that key would actually carry.
   */
  previewJailbreakKey: (
    id: string,
    messages: { role: string; content: string }[],
    apiKeyId: string,
  ) =>
    request<{
      injected: boolean
      reason: string
      presetId?: string
      presetName?: string
      prompt?: string
      messages: { role: string; content: string }[]
    }>('/api/jailbreak/preview', {
      method: 'POST',
      body: JSON.stringify({ id, messages, apiKeyId }),
    }),
  reorderJailbreakPresets: (order: string[]) =>
    request<{ ok: boolean }>('/api/jailbreak/presets/order', {
      method: 'PUT',
      body: JSON.stringify({ order }),
    }),
  getJailbreakModelCatalog: () =>
    request<{ models: string[] }>('/api/jailbreak/models/catalog'),
  getJailbreakUsage: () =>
    request<{ usage: Record<string, number> }>('/api/jailbreak/usage'),
  // The scope is the second resolver argument: passing one client key's id
  // re-runs the same resolution under that key, so a key-scoped binding can be
  // shown winning over the same model's global binding.
  explainJailbreak: (id: string, apiKeyId = '') =>
    request<JailbreakMatch>('/api/jailbreak/resolve', {
      method: 'POST',
      body: JSON.stringify({ id, apiKeyId }),
    }),
  duplicateJailbreakPreset: (id: string, newId?: string) =>
    request<{ ok: boolean; preset: JailbreakDoc }>(`/api/jailbreak/presets/${encodeURIComponent(id)}/duplicate`, {
      method: 'POST',
      body: JSON.stringify({ id: newId || '' }),
    }),
  exportProviderKeys: (providerId: string) =>
    request<{ connections: Array<{ id: string; name: string; authType: string; key: string }> }>(
      `/api/providers/${encodeURIComponent(providerId)}/export-keys`,
    ),
  testComboModels: (id: string, opts?: { models?: string[]; kind?: string }) =>
    request<{
      comboId: string
      summary: { total: number; alive: number; dead: number }
      results: Array<{ model: string; ok: boolean; latencyMs: number; error?: string; status?: number }>
    }>(`/api/combos/${encodeURIComponent(id)}/test-models`, {
      method: 'POST',
      body: JSON.stringify(opts || {}),
    }),
}

export default jailbreakEndpoints
