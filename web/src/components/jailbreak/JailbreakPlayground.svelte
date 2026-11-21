<script lang="ts">
  import { api, type APIKey, type JailbreakDoc, type JailbreakMatch } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'

  // Which scope a column resolved under, and the answer the resolver returned
  // for it. `error` is per column so one failing scope does not blank the
  // other: the whole point of the panel is the comparison.
  interface Column {
    scope: string
    match: JailbreakMatch | null
    error: string
  }
  interface Props {
    /** The live preset library: prompt, position, mode and safety flag per id. */
    presets: JailbreakDoc[]
    classes?: string
  }

  let { presets, classes = '' }: Props = $props()

  let modelId = $state('')
  let keyId = $state('')
  let resolving = $state(false)
  let columns = $state<{ global: Column | null; key: Column | null }>({ global: null, key: null })

  // The key list is fetched here rather than passed in: the parent that mounts
  // this panel does not hold it, and the picker needs the ids to offer. A
  // failed fetch leaves the picker on "Global only" rather than an error
  // banner — the panel still resolves the global column, which is the half an
  // operator cannot see anywhere else.
  let keys = $state<APIKey[]>([])
  $effect(() => {
    if (keys.length > 0) return
    api.getApiKeys().then(
      (list) => (keys = Array.isArray(list) ? list : []),
      () => (keys = []),
    )
  })

  function message(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function label(id: string): string {
    if (id === 'off' || id === 'none') return 'Off'
    const p = presets.find((x) => x.id === id)
    return p ? p.name || p.id || id : id
  }

  // Why the resolver returned the answer it did. The reason strings are the
  // resolver's own vocabulary, not ours, so the table stays pinned to them: a
  // new reason in Go lands as itself rather than as a wrong sentence.
  const WHY: Record<string, string> = {
    'key-binding': "A binding stored for this API key won, over the same model's global binding.",
    'exact-binding': 'An exact binding on this id won, over every keyword rule.',
    keyword: 'A keyword rule matched this id.',
    'global-exclude': 'A global exclude rule matches this id, so nothing is ever injected.',
    'preset-exclude': "The winning preset's own exclude list denies this id.",
    'no-match': 'No preset keyword or exact binding covers this id.',
  }

  function why(reason: string, detail?: string): string {
    return WHY[reason] || detail || reason
  }

  // Resolve both scopes of one id in one round: two independent reads, so they
  // run together and the panel shows them side by side. State is written only
  // after both land, so a half-resolved column is never rendered as one.
  async function resolve() {
    const id = modelId.trim()
    if (!id) {
      columns = { global: { scope: 'global', match: null, error: 'Enter a model id.' }, key: null }
      return
    }
    resolving = true
    columns = { global: null, key: null }
    const run = async (scope: string): Promise<Column> => {
      try {
        return { scope, match: await api.explainJailbreak(id, scope), error: '' }
      } catch (e) {
        return { scope, match: null, error: message(e) }
      }
    }
    try {
      if (keyId) {
        const [globalResult, keyResult] = await Promise.all([run(''), run(keyId)])
        columns = { global: globalResult, key: keyResult }
      } else {
        columns = { global: await run(''), key: null }
      }
    } finally {
      resolving = false
    }
  }

  function preset(id: string | undefined): JailbreakDoc | null {
    if (!id) return null
    return presets.find((p) => p.id === id) ?? null
  }

  function position(doc: JailbreakDoc | null): string {
    if (!doc) return '—'
    if (doc.position === 'append') return 'append — after the original content'
    if (doc.position === 'system') return 'system — prepended as the system prompt'
    return 'system (default) — prepended as the system prompt'
  }

  function mode(doc: JailbreakDoc | null): string {
    if (!doc) return '—'
    return doc.mode === 'chat'
      ? 'chat — a scripted trailing user turn'
      : 'system (default) — the prompt itself is the system prompt'
  }

  let keyName = $derived(keys.find((k) => k.id === keyId)?.name || keyId)

  let verdict = $derived.by(() => {
    if (!columns.global?.match || !columns.key?.match) return ''
    const g = columns.global.match
    const k = columns.key.match
    if (g.presetId && k.presetId && g.presetId === k.presetId) return 'Same preset for both scopes'
    if (k.injected && !g.injected) return 'Only this key injects — global does not'
    if (k.injected && g.injected) return 'This key overrides the global preset'
    if (!k.injected && !g.injected) return 'Neither scope injects'
    return 'Global injects, this key does not — check the key binding'
  })
</script>

<div class="space-y-3 {classes}">
  <p class="text-xs text-text-subtle">
    A key-scoped binding is invisible on the request path unless it resolves. This panel runs the
    gateway's own resolver twice for one model id — once as <span class="text-text-main">global</span>,
    once as the API key you pick — so an operator who just armed a per-key binding sees it outrank
    the global one instead of shipping a persona that never lands.
  </p>

  <div class="flex flex-wrap items-end gap-3">
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">API key</span>
      <select
        bind:value={keyId}
        aria-label="API key to resolve as"
        class="min-w-[220px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none focus:border-brand-500"
      >
        <option value="">Global only (no key scope)</option>
        {#each keys as k (k.id)}
          <option value={k.id}>{k.name || k.keyDisplay || k.id}</option>
        {/each}
      </select>
    </label>
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Model id</span>
      <input
        type="text"
        bind:value={modelId}
        placeholder="providerx/deepseek-4-pro"
        aria-label="Model id to resolve"
        onkeydown={(e) => e.key === 'Enter' && void resolve()}
        class="min-w-[240px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs font-code placeholder:text-text-subtle outline-none focus:border-brand-500"
      />
    </label>
    <Button variant="primary" size="sm" onclick={() => void resolve()} disabled={resolving} icon="troubleshoot">
      {resolving ? 'Resolving…' : 'Resolve'}
    </Button>
  </div>

  {#if resolving}
    <p class="text-xs text-text-subtle">Resolving both scopes…</p>
  {:else if columns.global?.error || columns.key?.error}
    <p class="text-xs text-error">{columns.global?.error || columns.key?.error}</p>
  {:else if !columns.global?.match}
    <p class="text-xs text-text-subtle">
      Enter a model id and resolve — the gateway reports which preset wins in each scope, and why.
    </p>
  {:else}
    <p class="text-xs text-text-muted">
      {verdict}
    </p>

    <div class="grid gap-3 md:grid-cols-2">
      {#each [columns.global, columns.key].filter((c): c is Column => Boolean(c)) as column (column.scope)}
        {@const match = column.match}
        {@const doc = preset(match?.presetId)}
        {@const title = column.scope ? keyName || column.scope : 'Global'}
        {@const scope = column.scope}
        <div
          class="p-3 rounded-[10px] border space-y-2 {match?.injected
            ? 'border-success/40 bg-success/5'
            : 'border-border bg-surface-2/40'}"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs font-semibold text-text-main">{title}</span>
            <span class="text-[10px] text-text-subtle font-code truncate" title={scope || 'global'}>
              {scope ? `key ${scope}` : 'empty scope'}
            </span>
          </div>

          <div class="flex items-center gap-2">
            <span
              class="material-symbols-outlined text-[18px] {match?.injected ? 'text-success' : 'text-text-subtle'}"
            >
              {match?.injected ? 'check_circle' : 'block'}
            </span>
            <span class="text-sm font-medium {match?.injected ? 'text-success' : 'text-text-muted'}">
              {match?.injected ? 'INJECTED' : 'NOT INJECTED'}
            </span>
            {#if match?.reason}
              <span class="text-[10px] text-text-subtle font-code truncate" title={match.reason}>{match.reason}</span>
            {/if}
          </div>

          {#if match}
            <p class="text-[11px] text-text-subtle">{why(match.reason, match.detail)}</p>
          {/if}

          <div class="space-y-1 text-xs text-text-muted">
            <div class="flex gap-2">
              <span class="text-text-subtle shrink-0">Preset</span>
              <span class="text-text-main font-code truncate">
                {match?.presetName || label(match?.presetId || '')}{match?.presetId ? ` (${match.presetId})` : '—'}
              </span>
            </div>
            <div class="flex gap-2">
              <span class="text-text-subtle shrink-0">Matched id</span>
              <span class="text-text-main font-code truncate">{match?.matchedId || '—'}</span>
            </div>
            {#if match?.keyword}
              <div class="flex gap-2">
                <span class="text-text-subtle shrink-0">Keyword</span>
                <span class="text-text-main font-code truncate">{match.keyword}</span>
              </div>
            {/if}
            <div class="flex gap-2">
              <span class="text-text-subtle shrink-0">Safety</span>
              <span class="text-text-main">{doc?.geminiSafety ? 'Gemini safety relaxed' : 'unset'}</span>
            </div>
            <div class="flex gap-2">
              <span class="text-text-subtle shrink-0">Position</span>
              <span class="text-text-main">{position(doc)}</span>
            </div>
            <div class="flex gap-2">
              <span class="text-text-subtle shrink-0">Mode</span>
              <span class="text-text-main">{mode(doc)}</span>
            </div>
          </div>

          {#if doc?.prompt}
            <div class="p-2 rounded-[10px] bg-surface-2 border border-border-subtle">
              <div class="text-[10px] font-semibold uppercase text-text-subtle mb-1">Effective prompt</div>
              <pre class="text-xs text-text-main font-code whitespace-pre-wrap break-all max-h-40 overflow-y-auto">{doc.prompt}</pre>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>