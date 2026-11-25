<script lang="ts">
  import { api, type APIKey } from '../api/client'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'

  /**
   * The Playground: one chat turn from the dashboard against any model the
   * gateway routes, through the engine's own dispatch.
   *
   * An operator picks a model, types a turn, and reads the reply without
   * minting or pasting an engine key. The reply is the gateway's own Chat
   * Completions response, so the panel shows the message, the model the
   * gateway resolved to, the token usage and the finish reason — the same
   * figures a client application would read, and the reason an operator
   * reaches for this page before wiring a client.
   *
   * `keyId` scopes the turn: the usage row is attributed to that key, its
   * budget is spent and its model allowlist is enforced by the same gate the
   * public lane uses. No key picked is the global scope — a keyless caller —
   * so the turn is not billed to any key.
   */

  interface Props {
    classes?: string
  }

  let { classes = '' }: Props = $props()

  let modelId = $state('')
  let keyId = $state('')
  let prompt = $state('')
  let sending = $state(false)
  let error = $state('')
  let reply = $state('')
  let meta = $state<{ model: string; tokens: number | null; finish: string; ms: number } | null>(null)
  let done = $state(false)

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

  function message(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  /**
   * The reply text from the gateway's own response shape.
   *
   * `choices[0].message.content` is the normal answer; a refusal or a provider
   * error arrives as `error.message` instead, and an empty choice list means
   * the gateway answered but the model said nothing — all three are replies
   * the panel must show rather than render as a blank card.
   */
  function extract(json: unknown): { text: string; tokens: number | null; finish: string } {
    if (json == null || typeof json !== 'object') return { text: '(empty reply)', tokens: null, finish: '' }
    const obj = json as Record<string, any>
    const err = obj.error
    if (err && typeof err === 'object' && typeof err.message === 'string') {
      return { text: err.message, tokens: null, finish: 'error' }
    }
    if (Array.isArray(obj.choices) && obj.choices.length > 0) {
      const msg = obj.choices[0]?.message
      const content = typeof msg?.content === 'string' ? msg.content : ''
      const usage = obj.usage
      const tokens =
        usage && typeof usage === 'object' && typeof usage.total_tokens === 'number'
          ? usage.total_tokens
          : null
      return {
        text: content || '(empty reply)',
        tokens,
        finish: typeof obj.choices[0]?.finish_reason === 'string' ? obj.choices[0].finish_reason : '',
      }
    }
    return { text: '(empty reply)', tokens: null, finish: '' }
  }

  async function send(): Promise<void> {
    if (!modelId.trim() || !prompt.trim()) return
    sending = true
    error = ''
    reply = ''
    meta = null
    const started = performance.now()
    try {
      const json = await api.playgroundChat({
        model: modelId.trim(),
        messages: [{ role: 'user', content: prompt.trim() }],
        stream: false,
        ...(keyId ? { keyId } : {}),
      })
      const out = extract(json)
      reply = out.text
      meta = {
        model: (json as Record<string, any>)?.model || modelId.trim(),
        tokens: out.tokens,
        finish: out.finish,
        ms: Math.round((performance.now() - started) / 100) / 10,
      }
      done = true
    } catch (e) {
      error = message(e)
    } finally {
      sending = false
    }
  }
</script>

<div class="space-y-4 {classes}">
  <p class="text-xs text-text-subtle">
    One turn, any model the gateway routes, from this dashboard session. No engine key needed —
    the call goes through the same dispatch path a client's request takes, so the reply is the
    gateway's own Chat Completions answer.
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
  </div>

  <label class="block space-y-1.5">
    <span class="text-xs text-text-muted">Prompt</span>
    <textarea
      bind:value={prompt}
      rows="4"
      aria-label="Prompt to send"
      placeholder="Ask anything — the reply is what a client would receive."
      class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm font-code placeholder:text-text-subtle outline-none focus:border-brand-500 resize-y"
    ></textarea>
  </label>

  <div class="flex items-center gap-3">
    <Button variant="primary" size="sm" onclick={() => void send()} disabled={sending || !modelId.trim() || !prompt.trim()} icon="send">
      {sending ? 'Sending…' : 'Send'}
    </Button>
    {#if error}
      <p class="text-xs text-error">{error}</p>
    {/if}
  </div>

  {#if done && meta}
    <Card padding="md" class="space-y-2">
      <div class="flex items-center gap-2 text-[11px] text-text-subtle">
        <span class="font-semibold uppercase tracking-wider text-text-muted">Reply</span>
        <span class="font-code">{meta.model}</span>
        <span class="ml-auto">
          {meta.ms}s{meta.tokens != null ? ` · ${meta.tokens} tok` : ''}{meta.finish ? ` · ${meta.finish}` : ''}
        </span>
      </div>
      <p class="text-sm whitespace-pre-wrap break-words text-text-main">{reply}</p>
    </Card>
  {:else if !sending}
    <p class="text-xs text-text-subtle">
      Pick a model and send — the reply carries the model the gateway resolved, the token count
      and the finish reason.
    </p>
  {/if}
</div>
