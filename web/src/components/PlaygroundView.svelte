<script lang="ts">
  import { api, type APIKey } from '../api/client'
  import Button from '../lib/ui/Button.svelte'

  /**
   * The Playground: a conversation with any model the gateway routes, from the
   * dashboard session, through the engine's own dispatch.
   *
   * The turn reaches the gateway the way a client's does (the same
   * `HandleChatCompletions` path the Gemini surface takes), so routing, combo
   * fallback, personas, the limiter and usage accounting are production code
   * rather than a parallel implementation that can drift.
   *
   * `keyId` scopes every turn: the usage rows are attributed to that key, its
   * budget is spent, and its model allowlist is enforced by the gate the public
   * lane uses. No key is the global scope — a keyless caller, billed to nothing.
   *
   * The conversation lives in this component's state and nowhere else. It is
   * deliberately not persisted: no localStorage, no sessionStorage, no rows.
   * Reloading the page starts a fresh conversation, and the only thing a turn
   * writes on the server is the usage row its own attribution requires.
   */

  interface Props {
    classes?: string
  }

  interface Turn {
    role: 'user' | 'assistant'
    content: string
    reasoning?: string
    /** Set on an assistant turn while its stream is still arriving. */
    pending?: boolean
    /** Set when the turn failed or was stopped, so the card says which. */
    note?: string
    ms?: number
    model?: string
    tokens?: number | null
    finish?: string
  }

  let { classes = '' }: Props = $props()

  let modelId = $state('')
  let keyId = $state('')
  let prompt = $state('')
  let turns = $state<Turn[]>([])
  let streaming = $state(false)
  let error = $state('')
  let controller: AbortController | null = null
  let logEl: HTMLDivElement | null = $state(null)

  let keys = $state<APIKey[]>([])
  let catalog = $state<string[]>([])
  let catalogLoaded = $state(false)

  $effect(() => {
    if (keys.length > 0) return
    api.getApiKeys().then(
      (list) => (keys = Array.isArray(list) ? list : []),
      () => (keys = []),
    )
  })
  $effect(() => {
    if (catalogLoaded) return
    catalogLoaded = true
    api.getJailbreakModelCatalog().then(
      (res) => (catalog = Array.isArray(res.models) ? res.models : []),
      () => (catalog = []),
    )
  })

  // Keep the newest turn in view as it streams. Reading `turns` here makes the
  // effect re-run on every delta, which is exactly when the log grows.
  $effect(() => {
    void turns.length
    void turns[turns.length - 1]?.content
    const el = logEl
    if (el) el.scrollTop = el.scrollHeight
  })

  function message(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function stop(): void {
    controller?.abort()
    controller = null
  }

  function reset(): void {
    if (streaming) stop()
    turns = []
    error = ''
  }

  async function send(): Promise<void> {
    const text = prompt.trim()
    if (!text || !modelId.trim() || streaming) return

    error = ''
    // The history the model sees is the completed turns; the turn being sent is
    // appended after, so an assistant placeholder never enters the payload.
    const history = turns
      .filter((t) => t.content.trim())
      .map((t) => ({ role: t.role, content: t.content }))

    const userTurn: Turn = { role: 'user', content: text }
    const replyTurn: Turn = { role: 'assistant', content: '', pending: true }
    turns = [...turns, userTurn, replyTurn]
    prompt = ''
    streaming = true
    const started = performance.now()

    const ac = new AbortController()
    controller = ac

    try {
      const result = await api.playgroundChatStream(
        {
          model: modelId.trim(),
          messages: [...history, { role: 'user', content: text }],
          ...(keyId ? { keyId } : {}),
        },
        {
          signal: ac.signal,
          onDelta: (kind, chunk) => {
            // Mutate the last turn through the array so Svelte sees the change;
            // a per-token array copy would be O(n²) over a long answer.
            const last = turns[turns.length - 1]
            if (!last || !last.pending) return
            if (kind === 'content') last.content += chunk
            else last.reasoning = (last.reasoning || '') + chunk
          },
        },
      )
      const last = turns[turns.length - 1]
      if (last) {
        last.pending = false
        last.model = result.model || modelId.trim()
        last.tokens = result.tokens
        last.finish = result.finish || (ac.signal.aborted ? 'stopped' : '')
        last.ms = Math.round((performance.now() - started) / 100) / 10
        if (ac.signal.aborted) last.note = 'Stopped'
        if (!result.text && !result.reasoning && !last.note) last.note = 'The model returned no text.'
      }
    } catch (e) {
      const last = turns[turns.length - 1]
      if (ac.signal.aborted) {
        // Stop is a user action, not a failure: keep what arrived and say so.
        if (last) {
          last.pending = false
          last.finish = 'stopped'
          last.note = 'Stopped'
          last.ms = Math.round((performance.now() - started) / 100) / 10
        }
      } else {
        error = message(e)
        if (last) {
          last.pending = false
          last.note = message(e)
        }
      }
    } finally {
      if (controller === ac) controller = null
      streaming = false
    }
  }

  function onKeydown(e: KeyboardEvent): void {
    // Enter sends, Shift+Enter is a newline — the convention every chat client
    // uses, and the reason this is a textarea rather than an input.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void send()
    }
  }
</script>

<div class="flex flex-col gap-4 {classes}">
  <p class="text-xs text-text-subtle">
    A conversation with any model the gateway routes, from this dashboard session. No engine key
    needed — each turn goes through the same dispatch path a client's request takes, and the reply
    is the gateway's own answer. Nothing is stored: the conversation ends with this page.
  </p>

  <div class="flex flex-wrap items-end gap-3">
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">API key (optional)</span>
      <select
        bind:value={keyId}
        aria-label="API key to bill the turn to"
        class="min-w-[200px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none focus:border-brand-500 cursor-pointer"
      >
        <option value="">Global scope — no key</option>
        {#each keys as k (k.id)}
          <option value={k.id}>{k.name || k.keyDisplay || k.id}</option>
        {/each}
      </select>
    </label>

    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Model</span>
      <input
        type="text"
        bind:value={modelId}
        list="playground-model-catalog"
        placeholder="deepseek/deepseek-chat"
        aria-label="Model to chat with"
        class="min-w-[240px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs font-code placeholder:text-text-subtle outline-none focus:border-brand-500"
      />
    </label>
    <datalist id="playground-model-catalog">
      {#each catalog as m (m)}
        <option value={m}></option>
      {/each}
    </datalist>

    {#if turns.length > 0}
      <Button variant="ghost" size="sm" onclick={reset} icon="add" title="Start a new conversation">
        New
      </Button>
    {/if}
  </div>

  {#if turns.length > 0}
    <div
      bind:this={logEl}
      class="flex max-h-[420px] flex-col gap-3 overflow-y-auto rounded-[14px] border border-border-subtle bg-surface p-3"
      aria-label="Conversation"
      role="log"
    >
      {#each turns as turn, i (i)}
        <div class="flex flex-col gap-1">
          <div class="flex items-center gap-2 text-[11px] text-text-subtle">
            <span class="font-semibold uppercase tracking-wider text-text-muted">
              {turn.role === 'user' ? 'You' : 'Reply'}
            </span>
            {#if turn.role === 'assistant' && turn.model}
              <span class="font-code">{turn.model}</span>
            {/if}
            {#if turn.role === 'assistant' && turn.pending}
              <span class="text-brand-500">streaming…</span>
            {/if}
            {#if turn.role === 'assistant' && !turn.pending}
              <span class="ml-auto">
                {turn.ms != null ? `${turn.ms}s` : ''}{turn.tokens != null
                  ? ` · ${turn.tokens} tok`
                  : ''}{turn.finish ? ` · ${turn.finish}` : ''}
              </span>
            {/if}
          </div>
          <p class="text-sm whitespace-pre-wrap break-words text-text-main">{turn.content}</p>
          {#if turn.reasoning}
            <details class="text-xs text-text-muted">
              <summary class="cursor-pointer select-none">Reasoning</summary>
              <p class="mt-1 whitespace-pre-wrap break-words font-code text-text-subtle">
                {turn.reasoning}
              </p>
            </details>
          {/if}
          {#if turn.note}
            <p class="text-xs text-warning">{turn.note}</p>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

  <label class="block space-y-1.5">
    <span class="text-xs text-text-muted">Prompt</span>
    <textarea
      bind:value={prompt}
      onkeydown={onKeydown}
      rows="3"
      aria-label="Prompt to send"
      placeholder="Ask anything — Enter sends, Shift+Enter adds a line."
      class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm font-code placeholder:text-text-subtle outline-none focus:border-brand-500 resize-y"
    ></textarea>
  </label>

  <div class="flex items-center gap-3">
    {#if streaming}
      <Button variant="danger" size="sm" onclick={stop} icon="stop">
        Stop
      </Button>
    {:else}
      <Button
        variant="primary"
        size="sm"
        onclick={() => void send()}
        disabled={!modelId.trim() || !prompt.trim()}
        icon="send"
      >
        Send
      </Button>
    {/if}
    {#if error}
      <p class="text-xs text-error">{error}</p>
    {/if}
    {#if turns.length === 0 && !streaming}
      <p class="text-xs text-text-subtle">
        Pick a model and send — the reply streams in as the provider produces it, with the model
        the gateway resolved, the token count and the finish reason.
      </p>
    {/if}
  </div>
</div>
