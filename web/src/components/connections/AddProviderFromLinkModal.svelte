<script lang="ts">
  // Receiver side of a provider share link. Nothing is written until the
  // operator presses Add — a link is untrusted input, not an instruction.
  //
  // The write sequence is spec §8.3 of
  // docs/superpowers/specs/2026-11-01-provider-share-url-design.md:
  // create-or-adopt the node, store the key, import the catalogue.
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import { api, type ProviderNode } from '../../api/client'
  import {
    applyModelFilters,
    type CatalogModel,
    type KeyEntry,
    type ModelSelection,
    type ProviderShareLink,
  } from '../../lib/providerShare'
  import { planBulkAdd } from '../../lib/bulk-add'

  interface Props {
    isOpen: boolean
    /** One card per provider. A single link is a one-element array. */
    links: ProviderShareLink[]
    onClose: () => void
    onDone: (prov: string, added: number, skipped: number) => void
  }

  let { isOpen, links, onClose, onDone }: Props = $props()

  // Per-card form state, keyed by node id so a re-render cannot cross-wire two
  // cards while the operator is editing the second one.
  interface CardForm {
    baseURL: string
    prefix: string
    apiKey: string
    name: string
    modelMode: 'auto' | 'specified' | 'none'
    modelIDs: string
    revealed: boolean
  }
  let forms = $state<Record<string, CardForm>>({})
  let busy = $state(false)
  let error = $state('')
  let validateState = $state<Record<string, 'checking' | 'valid' | 'invalid' | null>>({})
  let validateError = $state<Record<string, string>>({})
  // A node that already exists here with a different endpoint: the link must not
  // silently rewrite it, so the operator is asked once per card.
  let endpointChoice = $state<Record<string, 'stored' | 'link'>>({})
  let existingNodes = $state<ProviderNode[]>([])
  let existingNames = $state<string[]>([])


  // Seed once per open, keyed on the provider ids the links name. A reopen of
  // the same link reseeds; a poll that replaces `links` with an equal array does
  // not, because the ids are unchanged.
  let seededFor = $state<string | null>(null)
  $effect(() => {
    if (!isOpen || links.length === 0) return
    const key = links.map((l) => l.prov).join('|')
    if (seededFor === key) return
    seededFor = key
    const next: Record<string, CardForm> = {}
    for (const link of links) {
      next[link.prov] = {
        baseURL: link.url,
        prefix: link.prefix,
        // api= and keys= are mutually exclusive, so a keys= link leaves api empty.
        // Seeding from keys[0] keeps the single-key form and its Validate button
        // working on that link instead of showing an empty field.
        apiKey: link.api[0] ?? link.keys[0]?.key ?? '',
        name: link.name || '',
        modelMode: Array.isArray(link.models)
          ? 'specified'
          : link.models === 'none'
            ? 'none'
            : 'auto',
        modelIDs: Array.isArray(link.models) ? link.models.join(', ') : '',
        revealed: false,
      }
    }
    forms = next
    error = ''
    busy = false
    validateState = {}
    validateError = {}
    endpointChoice = {}
    // Two reads the write path needs: the nodes already here (adopt or create)
    // and the connection names (so a bulk name collision becomes Key 2).
    Promise.all([api.getProviderNodes(), api.getConnections()])
      .then(([nodes, conns]) => {
        existingNodes = nodes
        existingNames = conns.map((c) => c.name || '')
      })
      .catch(() => {
        existingNodes = []
        existingNames = []
      })
  })

  function formOf(prov: string): CardForm | undefined {
    return forms[prov]
  }

  /** The tail of a node id: "openai-compatible-chat-myprov" → "myprov". */
  function suffixOf(prov: string): string {
    const literals = ['openai-compatible-chat-', 'openai-compatible-responses-', 'openai-compatible-', 'anthropic-compatible-', 'custom-embedding-']
    for (const literal of literals) {
      if (prov.startsWith(literal)) return prov.slice(literal.length)
    }
    return prov
  }

  /** The id list a card should import, given its current controls. */
  function modelIDsFor(link: ProviderShareLink): string[] {
    const form = formOf(link.prov)
    if (!form) return []
    if (form.modelMode === 'none') return []
    if (form.modelMode === 'specified') {
      return form.modelIDs.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)
    }
    // auto: the filters still apply, but the ids only exist after the fetch.
    return []
  }

  function modelSelectionFor(link: ProviderShareLink): ModelSelection {
    const form = formOf(link.prov)
    if (!form) return 'none'
    if (form.modelMode === 'none') return 'none'
    if (form.modelMode === 'specified') {
      const ids = modelIDsFor(link)
      return ids.length ? ids : 'none'
    }
    return link.models
  }

  async function validate(link: ProviderShareLink) {
    const form = formOf(link.prov)
    if (!form || !form.baseURL.trim() || !form.apiKey) return
    validateState = { ...validateState, [link.prov]: 'checking' }
    validateError = { ...validateError, [link.prov]: '' }
    try {
      const res = await api.validateProviderNode({
        baseUrl: form.baseURL.trim(),
        apiKey: form.apiKey,
        type: link.type,
      })
      validateState = { ...validateState, [link.prov]: res.valid ? 'valid' : 'invalid' }
      if (!res.valid) validateError = { ...validateError, [link.prov]: res.error || 'Rejected by the upstream' }
    } catch (err) {
      validateState = { ...validateState, [link.prov]: 'invalid' }
      validateError = { ...validateError, [link.prov]: err instanceof Error ? err.message : String(err) }
    }
  }

  async function saveModels(alias: string, ids: string[]): Promise<{ added: number; skipped: number }> {
    if (!ids.length) return { added: 0, skipped: 0 }
    const res = await api.saveCustomModelsBulk({
      providerAlias: alias,
      models: ids.map((id) => ({ id, type: 'llm' })),
    })
    return { added: res.added?.length ?? 0, skipped: res.skipped?.length ?? 0 }
  }

  /** Adds one provider card. Returns the outcome so Add all can report per card. */
  async function addOne(link: ProviderShareLink): Promise<{ added: number; skipped: number }> {
    const form = formOf(link.prov)
    if (!form) throw new Error('form not seeded')
    const existing = existingNodes.find((n) => n.id === link.prov)
    const chosen = endpointChoice[link.prov] ?? 'link'
    const baseURL = existing && chosen === 'stored' ? (existing.baseUrl || form.baseURL) : form.baseURL
    if (!existing) {
      await api.createProviderNode({
        name: link.name || link.prov,
        prefix: form.prefix,
        apiType: link.apiType || 'chat',
        baseUrl: baseURL,
        type: link.type,
        urlSuffix: suffixOf(link.prov),
      })
    }
    const created = await api.createConnection({
      provider: link.prov,
      // "compatible", not "apikey": that is what AddConnectionModal sends for a
      // node, and the probe dispatcher branches on it.
      authType: 'compatible',
      apiKey: form.apiKey,
      name: form.name || `${link.name || link.prov} Key`,
      priority: 1,
    })
    let added = 0
    let skipped = 0
    if (form.modelMode === 'auto') {
      const res = await api.getConnectionModels(created.id)
      const raw = (res.models || []).map((m) => (typeof m === 'string' ? m : (m.id || m.model || ''))).filter(Boolean)
      const filtered = applyModelFilters(raw.map((id) => ({ id })) as CatalogModel[], modelSelectionFor(link))
      ;({ added, skipped } = await saveModels(link.prov, filtered.map((m) => m.id)))
    } else {
      ;({ added, skipped } = await saveModels(link.prov, modelIDsFor(link)))
    }
    return { added, skipped }
  }

  /** Bulk scope: the node once, then one connection per key, named collision-free. */
  async function addBulk(link: ProviderShareLink): Promise<{ added: number; skipped: number }> {
    const form = formOf(link.prov)
    if (!form) throw new Error('form not seeded')
    const existing = existingNodes.find((n) => n.id === link.prov)
    if (!existing) {
      await api.createProviderNode({
        name: link.name || link.prov,
        prefix: form.prefix,
        apiType: link.apiType || 'chat',
        baseUrl: form.baseURL,
        type: link.type,
        urlSuffix: suffixOf(link.prov),
      })
    }
    // planBulkAdd gap-fills against the names already here, so a shared "Key 1"
    // becomes "Key 2" instead of replacing the stored row. The list is refreshed
    // as rows land: a set= link runs several cards in sequence, and a stale list
    // would let card 2 propose a name card 1 just took — a 409 the operator
    // cannot explain, because both names came from the same link.
    let names = [...existingNames]
    for (const entry of planBulkAdd(
      link.keys.map((k) => `${k.name || 'Key'}|${k.key}`),
      names,
    )) {
      await api.createConnection({
        provider: link.prov,
        authType: 'compatible',
        apiKey: entry.apiKey,
        name: entry.name,
        priority: 1,
      })
      names.push(entry.name)
    }
    let added = 0
    let skipped = 0
    if (form.modelMode === 'auto') {
      const conns = await api.getConnections()
      const mine = conns.find((c) => c.provider === link.prov)
      if (mine) {
        const res = await api.getConnectionModels(mine.id)
        const raw = (res.models || []).map((m) => (typeof m === 'string' ? m : (m.id || m.model || ''))).filter(Boolean)
        ;({ added, skipped } = await saveModels(link.prov, raw))
      }
    } else {
      ;({ added, skipped } = await saveModels(link.prov, modelIDsFor(link)))
    }
    return { added, skipped }
  }

  async function addAll() {
    if (busy || links.length === 0) return
    busy = true
    error = ''
    let added = 0
    let skipped = 0
    try {
      for (const link of links) {
        // keys.length, not >1: a keys= link with one entry still leaves form.apiKey
        // empty (api= and keys= are mutually exclusive), so addOne would store a
        // blank credential.
        const outcome = link.keys.length > 0 ? await addBulk(link) : await addOne(link)
        added += outcome.added
        skipped += outcome.skipped
      }
      onDone(links[0].prov, added, skipped)
    } catch (err) {
      // A failure mid-sequence leaves the earlier cards written. Say so rather
      // than implying nothing landed.
      error = err instanceof Error ? err.message : String(err)
    } finally {
      busy = false
    }
  }

  function keysOf(link: ProviderShareLink): KeyEntry[] {
    return link.keys
  }
</script>

<Modal {isOpen} {onClose} title="Add Provider from Link" icon="download" size="lg">
  {#snippet children()}
    <div class="flex flex-col gap-4">
      <p class="text-sm text-text-muted">
        {links.length === 1
          ? 'Review what this link will add, then confirm. Nothing is written until you press Add.'
          : `This link adds ${links.length} providers. Review each one, then confirm.`}
      </p>

      {#each links as link (link.prov)}
        {@const form = formOf(link.prov)}
        {@const existing = existingNodes.find((n) => n.id === link.prov)}
        <div class="rounded-lg border border-border-subtle bg-surface-2/40 p-3">
          <div class="mb-2 flex items-center justify-between gap-2">
            <span class="font-mono text-xs text-text-main">{link.prov}</span>
            {#if existing}
              <span class="rounded bg-surface-3 px-2 py-0.5 text-[10px] text-text-muted">already here</span>
            {/if}
          </div>

          {#if link.note}
            <p class="mb-2 text-xs text-text-muted italic">“{link.note}”</p>
          {/if}

          {#if existing && link.url && existing.baseUrl && existing.baseUrl !== link.url}
            <div class="mb-2 rounded border border-warning/40 bg-warning/10 p-2 text-xs text-text-main">
              <p class="mb-1.5">
                This instance already has <strong>{link.prov}</strong> at <span class="font-mono">{existing.baseUrl}</span>,
                but the link points at <span class="font-mono">{link.url}</span>.
              </p>
              <div class="flex gap-2">
                <button
                  type="button"
                  onclick={() => (endpointChoice = { ...endpointChoice, [link.prov]: 'stored' })}
                  class="rounded-[8px] border px-2 py-1 text-[11px] font-medium transition-colors cursor-pointer {(endpointChoice[link.prov] ?? 'link') === 'stored'
                    ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                    : 'border-border bg-surface-2 text-text-muted'}"
                >
                  Keep stored
                </button>
                <button
                  type="button"
                  onclick={() => (endpointChoice = { ...endpointChoice, [link.prov]: 'link' })}
                  class="rounded-[8px] border px-2 py-1 text-[11px] font-medium transition-colors cursor-pointer {(endpointChoice[link.prov] ?? 'link') === 'link'
                    ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                    : 'border-border bg-surface-2 text-text-muted'}"
                >
                  Use link
                </button>
              </div>
            </div>
          {/if}

          {#if form}
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <label class="flex flex-col gap-1">
                <span class="text-xs text-text-muted">Base URL</span>
                <input
                  bind:value={form.baseURL}
                  placeholder="https://api.example.com/v1"
                  class="rounded-[8px] border border-transparent bg-surface-2 px-2 py-1.5 font-mono text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
                />
              </label>
              <label class="flex flex-col gap-1">
                <span class="text-xs text-text-muted">Prefix</span>
                <input
                  bind:value={form.prefix}
                  class="rounded-[8px] border border-transparent bg-surface-2 px-2 py-1.5 font-mono text-xs text-text-main focus:border-brand-500/40 focus:outline-none"
                />
              </label>
              <label class="flex flex-col gap-1 sm:col-span-2">
                <span class="text-xs text-text-muted">API key</span>
                <div class="flex items-center gap-2">
                  <input
                    type={form.revealed ? 'text' : 'password'}
                    bind:value={form.apiKey}
                    class="min-w-0 flex-1 rounded-[8px] border border-transparent bg-surface-2 px-2 py-1.5 font-mono text-xs text-text-main focus:border-brand-500/40 focus:outline-none"
                  />
                  <button
                    type="button"
                    onclick={() => (form.revealed = !form.revealed)}
                    class="shrink-0 rounded p-1 text-text-muted hover:text-text-main"
                    aria-label={form.revealed ? 'Hide key' : 'Show key'}
                  >
                    <span class="material-symbols-outlined text-[16px]">{form.revealed ? 'visibility_off' : 'visibility'}</span>
                  </button>
                </div>
              </label>
              <label class="flex flex-col gap-1 sm:col-span-2">
                <span class="text-xs text-text-muted">Connection name</span>
                <input
                  bind:value={form.name}
                  class="rounded-[8px] border border-transparent bg-surface-2 px-2 py-1.5 text-xs text-text-main focus:border-brand-500/40 focus:outline-none"
                />
              </label>
            </div>

            <!-- Models -->
            <div class="mt-2 flex flex-col gap-1.5">
              <span class="text-xs text-text-muted">Models</span>
              <div class="flex flex-wrap gap-2">
                {#each [{ v: 'auto', l: 'Auto-fetch' }, { v: 'specified', l: 'Specified' }, { v: 'none', l: 'None' }] as opt (opt.v)}
                  <button
                    type="button"
                    onclick={() => (form.modelMode = opt.v as typeof form.modelMode)}
                    class="rounded-[8px] border px-2 py-1 text-[11px] font-medium transition-colors cursor-pointer {form.modelMode === opt.v
                      ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                      : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
                  >
                    {opt.l}
                  </button>
                {/each}
                {#if !Array.isArray(link.models) && typeof link.models === 'object'}
                  <span class="self-center text-[11px] text-text-muted">
                    link asks for {Object.entries(link.models).map(([k, v]) => `${k}=${v}`).join(', ')}
                  </span>
                {/if}
              </div>
              {#if form.modelMode === 'specified'}
                <textarea
                  bind:value={form.modelIDs}
                  rows={3}
                  class="w-full rounded-[8px] border border-transparent bg-surface-2 px-2 py-1.5 font-mono text-xs text-text-main focus:border-brand-500/40 focus:outline-none"
                ></textarea>
              {/if}
            </div>

            <!-- Bulk keys -->
            {#if keysOf(link).length > 1}
              <div class="mt-2 flex flex-col gap-1.5">
                <span class="text-xs text-text-muted">Keys in this link ({keysOf(link).length})</span>
                <div class="flex flex-col gap-1 rounded-lg border border-border-subtle bg-surface-2/60 p-2">
                  {#each keysOf(link) as entry, index (index)}
                    <div class="flex items-center gap-2 text-xs text-text-main">
                      <span class="w-5 shrink-0 font-mono text-[10px] text-text-muted">#{index + 1}</span>
                      <span class="min-w-0 flex-1 truncate">{entry.name || 'unnamed'}</span>
                      {#if entry.pool}<span class="shrink-0 rounded bg-surface-3 px-1.5 text-[10px] text-text-muted">{entry.pool}</span>{/if}
                      <span class="shrink-0 font-mono text-[10px] text-text-muted">{entry.key.slice(0, 4)}…{entry.key.slice(-3)}</span>
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <div class="mt-2 flex items-center gap-2">
              <Button variant="secondary" disabled={validateState[link.prov] === 'checking' || !form.baseURL.trim() || !form.apiKey} onclick={() => validate(link)}>
                {validateState[link.prov] === 'checking' ? 'Checking…' : 'Validate'}
              </Button>
              {#if validateState[link.prov] === 'valid'}
                <span class="text-xs text-success">Endpoint and key accepted</span>
              {:else if validateState[link.prov] === 'invalid'}
                <span class="text-xs text-error">{validateError[link.prov] || 'Rejected'}</span>
              {/if}
            </div>
          {/if}
        </div>
      {/each}

      {#if error}
        <p class="rounded-lg border border-error/40 bg-error/10 p-3 text-xs text-text-main">{error}</p>
      {/if}
    </div>
  {/snippet}

  {#snippet footer()}
    <div class="flex items-center justify-end gap-2">
      <Button variant="secondary" onclick={onClose}>Cancel</Button>
      <Button variant="primary" disabled={busy || links.length === 0} onclick={addAll}>
        {busy ? 'Adding…' : links.length === 1 ? 'Add provider' : `Add all ${links.length}`}
      </Button>
    </div>
  {/snippet}
</Modal>
