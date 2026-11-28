import type { Combo, ComboRulesPreview, SmartRule } from '../types'
import { ApiError, formatApiError, getAuthHeaders, handleUnauthorized, request, responseErrorMessage } from '../runtime'

const combosEndpoints = {
  // Combos
  getCombos: async () => {
    const res = await request<any>('/api/combos')
    return Array.isArray(res) ? res : (res.combos || [])
  },
  createCombo: (payload: { id?: string; name: string; kind?: string; models: string | string[]; strategy?: string; matchRules?: SmartRule[]; customPrompt?: string }) =>
    request<{ success: boolean; id: string }>('/api/combos', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateCombo: (id: string, payload: Partial<Combo> | any) =>
    request<{ success: boolean }>(`/api/combos/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    }),
  deleteCombo: (id: string) =>
    request<{ success: boolean }>(`/api/combos/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  /** Dry-run a rule list: what it would add, remove and own. Writes nothing. */
  previewComboRules: (id: string, matchRules: SmartRule[]) =>
    request<ComboRulesPreview>(`/api/combos/${encodeURIComponent(id)}/smart/preview`, {
      method: 'POST',
      body: JSON.stringify({ matchRules }),
    }),
  /** Materialise the previewed rules into the combo's membership (merge). */
  loadComboRules: (id: string, matchRules: SmartRule[]) =>
    request<{
      combo: string
      added: string[]
      removed: string[]
      models: string[]
      changed: boolean
    }>(`/api/combos/${encodeURIComponent(id)}/smart/load`, {
      method: 'POST',
      body: JSON.stringify({ matchRules }),
    }),
  /** Apply every combo's rules now, or just the named one. */
  reconcileCombos: (id?: string) =>
    request<{ status: string; reconciled: number; added: string[]; removed: string[] }>(
      id ? `/api/combos/reconcile?id=${encodeURIComponent(id)}` : '/api/combos/reconcile',
      { method: 'POST' },
    ),
  /** Everything the combo editor's sort dropdown knows about each model. */
  getModelMeta: () =>
    request<{
      meta: Record<
        string,
        {
          provider: string
          model: string
          ttftMs: number
          totalMs: number
          samples: number
          contextWindow: number
          kind: string
          vision: boolean
          reasoning: boolean
          audioInput: boolean
        }
      >
    }>('/api/models/meta'),

  /**
   * Playground: a turn taken through the gateway's own dispatch, from the
   * dashboard session rather than an engine key. `keyId` scopes the turn to
   * that key (its model allowlist, budget and usage attribution) and is
   * omitted for the global scope.
   *
   * The gateway's Chat Completions stream is read as it arrives instead of
   * being awaited whole: each `data:` line is one upstream delta, so the caller
   * can render tokens while the provider is still producing them.
   *
   * `signal` is the Stop button: aborting closes the request, and the engine
   * cancels the upstream call it was making on this turn's behalf.
   *
   * Nothing here is persisted — the conversation lives in the caller's state.
   */
  playgroundChatStream: async (
    payload: {
      model: string
      messages: { role: string; content: string }[]
      temperature?: number
      maxTokens?: number
      keyId?: string
    },
    handlers: {
      signal?: AbortSignal
      onDelta?: (kind: 'content' | 'reasoning', chunk: string) => void
    } = {},
  ): Promise<{ model: string; tokens: number | null; finish: string; text: string; reasoning: string }> => {
    const res = await fetch('/api/playground/chat', {
      method: 'POST',
      headers: getAuthHeaders(),
      body: JSON.stringify({ ...payload, stream: true }),
      signal: handlers.signal,
    })
    if (res.status === 401) handleUnauthorized('/api/playground/chat')
    if (!res.ok) throw new ApiError(await responseErrorMessage(res), res.status)

    const reader = res.body?.getReader()
    if (!reader) throw new ApiError('the gateway returned no stream body', res.status)

    const decoder = new TextDecoder()
    let buffer = ''
    let text = ''
    let reasoning = ''
    let model = ''
    let finish = ''
    let tokens: number | null = null

    // One SSE frame per `data:` line: the engine forwards upstream chunks
    // unchanged, so the shape is the OpenAI delta and not a private one.
    const applyFrame = (raw: string) => {
      const frame = JSON.parse(raw) as Record<string, any>
      if (frame?.error) throw new ApiError(formatApiError(frame.error, 'stream error'), res.status)
      if (typeof frame?.model === 'string' && frame.model) model = frame.model
      if (typeof frame?.usage?.total_tokens === 'number') tokens = frame.usage.total_tokens
      const choice = frame?.choices?.[0]
      if (!choice) return
      if (typeof choice.finish_reason === 'string' && choice.finish_reason) finish = choice.finish_reason
      const delta = choice.delta || {}
      const think =
        typeof delta.reasoning_content === 'string'
          ? delta.reasoning_content
          : typeof delta.reasoning === 'string'
            ? delta.reasoning
            : ''
      if (think) {
        reasoning += think
        handlers.onDelta?.('reasoning', think)
      }
      if (typeof delta.content === 'string' && delta.content) {
        text += delta.content
        handlers.onDelta?.('content', delta.content)
      }
    }

    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() || ''
        for (const line of lines) {
          const trimmed = line.trim()
          if (!trimmed.startsWith('data:')) continue
          const data = trimmed.slice(5).trim()
          if (!data || data === '[DONE]') continue
          applyFrame(data)
        }
      }
    } finally {
      // A stopped turn keeps the text that already arrived: that partial answer
      // is what the operator saw, and throwing it away would misreport what the
      // gateway actually did.
      void reader.cancel().catch(() => {})
    }

    return { model, tokens, finish, text, reasoning }
  },
}

export default combosEndpoints
