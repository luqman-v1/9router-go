<script lang="ts">
  // Share a provider as one link. The codec is web/src/lib/providerShare.ts; the
  // parameter contract is docs/superpowers/specs/2026-11-01-provider-share-url-design.md §5.
  //
  // Three scopes, because "bulk share" is three different features: one link
  // with one key, one link with a block of keys, and one link carrying several
  // providers. The scope radio decides which controls render — not a single form
  // where most fields are usually irrelevant.
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import { copyToClipboard } from '../../lib/clipboard'
  import { api, type ProviderConnection, type ProviderNode } from '../../api/client'
  import {
    buildProviderShareURL,
    encodeKeysBlock,
    encodeSetPayload,
    shareLinkTier,
    type ModelSelection,
    type ProviderShareLink,
  } from '../../lib/providerShare'

  interface Props {
    isOpen: boolean
    providerId: string
    providerName: string
    /** A custom OpenAI/Anthropic-compatible node, which has endpoint fields to share. */
    isCompatibleNode?: boolean
    node?: ProviderNode | null
    connections?: ProviderConnection[]
    /** Every provider the operator could add to a multi-provider set. */
    availableProviders?: Array<{ id: string; name: string; isCompatible: boolean }>
    onClose: () => void
  }

  let {
    isOpen,
    providerId,
    providerName,
    isCompatibleNode = false,
    node = null,
    connections = [],
    availableProviders = [],
    onClose,
  }: Props = $props()

  type Scope = 'single' | 'bulk' | 'set'
  let scope = $state<Scope>('single')
  let credentialScope = $state<'none' | 'single'>('none')
  let pickedKeyId = $state('')
  let modelMode = $state<'auto' | 'ask' | 'none' | 'free' | 'filter' | 'specified'>('auto')
  let modelFilter = $state({ caps: '', ctxAtLeast: '', limit: '' })
  let modelIDs = $state('')
  let includeURL = $state(true)
  let includePrefix = $state(true)
  let includeName = $state(true)
  let note = $state('')
  let copied = $state(false)
  let customModels = $state<Array<{ id: string; providerAlias?: string }>>([])
  // Bulk scope: one row per stored connection, with an editable name because an
  // exported "Key 1" means nothing to a receiver.
  let bulkKeys = $state<Array<{ id: string; name: string; checked: boolean }>>([])
  // Multi-provider set: which providers are checked, and their per-row overrides.
  let setSelection = $state<Record<string, boolean>>({})

  const keyedConnections = $derived(connections.filter((c) => c.isActive !== 0))
  const pickedConnection = $derived(keyedConnections.find((c) => c.id === pickedKeyId) ?? null)
  // The key itself never reaches the browser from the list endpoint (it is
  // masked), so a single-key share reads it from export-keys, which is the same
  // call Export Keys uses. That is also why the modal cannot prefill the key.
  const pickedKey = $derived.by(() => {
    if (credentialScope !== 'single' || !pickedConnection) return ''
    return exportKeys.find((k) => k.id === pickedConnection.id)?.key ?? ''
  })
  let exportKeys = $state<Array<{ id: string; name: string; authType: string; key: string }>>([])
  let exportError = $state('')

  // Seed once per open. The dashboard polls and replaces the connections array,
  // so seeding on identity would reset the form mid-typing.
  let seededFor = $state<string | null>(null)
  $effect(() => {
    if (!isOpen || seededFor === providerId) return
    seededFor = providerId
    scope = 'single'
    credentialScope = 'none'
    pickedKeyId = keyedConnections[0]?.id ?? ''
    modelMode = 'auto'
    modelFilter = { caps: '', ctxAtLeast: '', limit: '' }
    modelIDs = ''
    includeURL = isCompatibleNode
    includePrefix = isCompatibleNode
    includeName = true
    note = ''
    copied = false
    exportError = ''
    exportKeys = []
    bulkKeys = keyedConnections.map((c) => ({ id: c.id, name: c.name || c.id, checked: true }))
    setSelection = {}
    if (isCompatibleNode) {
      api.getCustomModels()
        .then((res) => {
          customModels = (res.models || []).filter((m) => (m.providerAlias || '') === providerId)
          if (!modelIDs) modelIDs = customModels.map((m) => m.id).join(', ')
        })
        .catch(() => {
          customModels = []
        })
    }
  })

  // The bulk table tracks the live connection list: the dashboard polls every
  // 10s, so a connection added while the modal is open has to appear as a row
  // rather than being missing from the link the operator is about to copy.
  // Existing rows keep their edited name and checkbox state.
  $effect(() => {
    if (!isOpen || scope !== 'bulk') return
    const live = keyedConnections
    if (bulkKeys.length === live.length && bulkKeys.every((r, i) => r.id === live[i]?.id)) return
    const byID = new Map(bulkKeys.map((r) => [r.id, r]))
    bulkKeys = live.map((c) => byID.get(c.id) ?? { id: c.id, name: c.name || c.id, checked: true })
  })

  // A credential-carrying scope needs the raw key, which only export-keys returns.
  $effect(() => {
    if (!isOpen) return
    if (scope === 'single' && credentialScope !== 'single') return
    if (scope !== 'bulk' && !pickedConnection) return
    if (exportKeys.length) return
    api.exportProviderKeys(providerId)
      .then((res) => {
        exportKeys = res?.connections || []
      })
      .catch(() => {
        exportError = 'Could not read the stored keys. Copy them from Export Keys instead.'
      })
  })

  const nodeType = $derived(node?.type || 'openai-compatible')
  const nodeApiType = $derived(node?.apiType || 'chat')

  /** The model selection the current controls describe. */
  const models = $derived.by<ModelSelection>(() => {
    if (modelMode === 'filter') {
      const filter: Record<string, string | number> = {}
      if (modelFilter.caps.trim()) filter.caps = modelFilter.caps.trim()
      if (modelFilter.ctxAtLeast.trim()) filter.ctxAtLeast = Number(modelFilter.ctxAtLeast.trim())
      if (modelFilter.limit.trim()) filter.limit = Number(modelFilter.limit.trim())
      return Object.keys(filter).length ? (filter as ModelSelection) : 'auto'
    }
    if (modelMode === 'specified') {
      const ids = modelIDs.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)
      return ids.length ? ids : 'none'
    }
    return modelMode
  })

  const link = $derived.by<ProviderShareLink>(() => ({
    prov: providerId,
    url: includeURL && isCompatibleNode ? (node?.baseUrl || '') : '',
    api: scope === 'single' && credentialScope === 'single' && pickedKey ? [pickedKey] : [],
    keys: scope === 'bulk' ? bulkEntries : [],
    models,
    prefix: includePrefix && isCompatibleNode ? (node?.prefix || '') : '',
    name: includeName ? (providerName || providerId) : '',
    note: note.trim(),
    type: nodeType,
    apiType: nodeApiType,
  }))

  /** The checked rows of the bulk table, as `keys=` entries. */
  const bulkEntries = $derived.by(() =>
    bulkKeys
      .filter((row) => row.checked)
      .map((row, index) => {
        const key = exportKeys.find((k) => k.id === row.id)?.key ?? ''
        return { name: row.name.trim(), key, prio: index + 1 }
      })
      .filter((entry) => entry.key)
  )
  const bulkMissingKeys = $derived(bulkKeys.some((row) => row.checked) && bulkEntries.length < bulkKeys.filter((r) => r.checked).length)

  const builtURL = $derived.by(() => {
    try {
      return buildProviderShareURL(typeof window !== 'undefined' ? window.location.origin : '', link)
    } catch {
      return ''
    }
  })
  const buildError = $derived.by(() => {
    try {
      buildProviderShareURL(typeof window !== 'undefined' ? window.location.origin : '', link)
      return ''
    } catch (err) {
      return err instanceof Error ? err.message : String(err)
    }
  })

  const hasCredential = $derived(link.api.length > 0 || link.keys.length > 0)

  /** A `set=` link: one entry per checked provider, each with its own overrides. */
  const setLinks = $derived.by<ProviderShareLink[]>(() =>
    availableProviders
      .filter((p) => setSelection[p.id])
      .map((p) => {
        // A node's endpoint has to travel: the receiver cannot create
        // "openai-compatible-chat-a" from an id alone, and a registry provider
        // has no endpoint to send. The current node's fields come from `node`;
        // the others are left empty and the receiver fills them in.
        const isCurrent = p.id === providerId
        return {
          prov: p.id,
          url: isCurrent && isCompatibleNode ? (node?.baseUrl || '') : '',
          api: [],
          keys: [],
          models: 'auto' as ModelSelection,
          prefix: isCurrent && isCompatibleNode ? (node?.prefix || '') : '',
          name: p.name,
          note: note.trim(),
          type: isCurrent ? nodeType : 'openai-compatible',
          apiType: isCurrent ? nodeApiType : 'chat',
        }
      })
  )
  const setURL = $derived.by(() => {
    if (scope !== 'set') return ''
    try {
      const encoded = encodeSetPayload(setLinks)
      const origin = typeof window !== 'undefined' ? window.location.origin : ''
      return `${origin.replace(/\/+$/, '')}/add#set=${encodeURIComponent(encoded)}`
    } catch (err) {
      return err instanceof Error ? err.message : ''
    }
  })
  const shownURL = $derived(scope === 'set' ? setURL : builtURL)
  const shownTier = $derived(shareLinkTier(shownURL.length))

  async function copyLink() {
    if (!shownURL) return
    await copyToClipboard(shownURL)
    copied = true
    setTimeout(() => (copied = false), 1500)
  }

  /** The link without its credentials, for the "copy as two parts" tier. */
  const linkWithoutCredentials = $derived.by(() => {
    const origin = typeof window !== 'undefined' ? window.location.origin : ''
    try {
      return buildProviderShareURL(origin, { ...link, api: [], keys: [] })
    } catch {
      return ''
    }
  })
  async function copySplit() {
    await copyToClipboard(`${linkWithoutCredentials}\n\nkeys=${encodeKeysBlock(link.keys)}`)
    copied = true
    setTimeout(() => (copied = false), 1500)
  }
</script>

<Modal {isOpen} {onClose} title="Share Provider" icon="link" size="lg">
  {#snippet children()}
    <div class="flex flex-col gap-4">
      <p class="text-sm text-text-muted">
        Build a link that adds {providerName || providerId} on another 9router-go instance.
      </p>

      <!-- Scope -->
      <div class="flex flex-col gap-1.5">
        <span class="text-sm font-medium text-text-main">What the link carries</span>
        <div class="flex flex-wrap gap-2">
          {#each [{ v: 'single', l: 'This provider' }, { v: 'bulk', l: 'This provider + all keys' }, { v: 'set', l: 'Several providers' }] as opt (opt.v)}
            <button
              type="button"
              onclick={() => (scope = opt.v as Scope)}
              class="rounded-[8px] border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer {scope === opt.v
                ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
            >
              {opt.l}
            </button>
          {/each}
        </div>
      </div>

      {#if scope === 'single'}
        <!-- Credentials -->
        <div class="flex flex-col gap-1.5">
          <span class="text-sm font-medium text-text-main">Credential</span>
          <div class="flex flex-wrap gap-2">
            <button
              type="button"
              onclick={() => (credentialScope = 'none')}
              class="rounded-[8px] border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer {credentialScope === 'none'
                ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
            >
              None
            </button>
            <button
              type="button"
              onclick={() => (credentialScope = 'single')}
              class="rounded-[8px] border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer {credentialScope === 'single'
                ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
            >
              One key
            </button>
          </div>
          {#if credentialScope === 'single'}
            {#if keyedConnections.length === 0}
              <p class="text-xs text-text-muted">This provider has no active connection to share a key from.</p>
            {:else}
              <select
                bind:value={pickedKeyId}
                class="w-full rounded-[10px] border border-transparent bg-surface-2 px-3 py-2 text-sm text-text-main focus:border-brand-500/40 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
              >
                {#each keyedConnections as c (c.id)}
                  <option value={c.id}>{c.name || c.id}</option>
                {/each}
              </select>
              {#if exportError}<p class="text-xs text-error">{exportError}</p>{/if}
              {#if pickedKey}
                <p class="font-mono text-xs text-text-muted">
                  {pickedKey.slice(0, 6)}…{pickedKey.slice(-4)}
                </p>
              {/if}
            {/if}
          {/if}
        </div>

        <!-- Models -->
        <div class="flex flex-col gap-1.5">
          <span class="text-sm font-medium text-text-main">Models</span>
          <div class="flex flex-wrap gap-2">
            {#each [{ v: 'auto', l: 'Auto-fetch' }, { v: 'ask', l: 'Ask receiver' }, { v: 'none', l: 'None' }, { v: 'free', l: 'Free only' }, { v: 'filter', l: 'Filter…' }, { v: 'specified', l: 'Specified…' }] as opt (opt.v)}
              <button
                type="button"
                onclick={() => (modelMode = opt.v as typeof modelMode)}
                class="rounded-[8px] border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer {modelMode === opt.v
                  ? 'border-brand-500 bg-brand-500/10 text-brand-500'
                  : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
              >
                {opt.l}
              </button>
            {/each}
          </div>
          {#if modelMode === 'filter'}
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <input
                bind:value={modelFilter.caps}
                placeholder="capability, e.g. vision"
                class="rounded-[8px] border border-transparent bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
              />
              <input
                bind:value={modelFilter.ctxAtLeast}
                placeholder="min context, e.g. 128k"
                class="rounded-[8px] border border-transparent bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
              />
              <input
                bind:value={modelFilter.limit}
                placeholder="limit, e.g. 50"
                class="rounded-[8px] border border-transparent bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
              />
            </div>
          {/if}
          {#if modelMode === 'specified'}
            <textarea
              bind:value={modelIDs}
              rows={4}
              placeholder="model-id, model-id (one per line or comma-separated)"
              class="w-full rounded-[8px] border border-transparent bg-surface-2 px-3 py-2 font-mono text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
            ></textarea>
          {/if}
        </div>

        <!-- Endpoint fields -->
        {#if isCompatibleNode}
          <div class="flex flex-col gap-1.5">
            <span class="text-sm font-medium text-text-main">Endpoint fields</span>
            <div class="flex flex-wrap gap-4">
              <label class="flex items-center gap-2 text-xs text-text-main">
                <input type="checkbox" bind:checked={includeURL} class="accent-brand-500" />
                Base URL
              </label>
              <label class="flex items-center gap-2 text-xs text-text-main">
                <input type="checkbox" bind:checked={includePrefix} class="accent-brand-500" />
                Prefix
              </label>
              <label class="flex items-center gap-2 text-xs text-text-main">
                <input type="checkbox" bind:checked={includeName} class="accent-brand-500" />
                Display name
              </label>
            </div>
          </div>
        {/if}
      {/if}

      {#if scope === 'bulk'}
        <div class="flex flex-col gap-1.5">
          <span class="text-sm font-medium text-text-main">Keys to include</span>
          {#if bulkKeys.length === 0}
            <p class="text-xs text-text-muted">This provider has no connections to share.</p>
          {:else}
            <div class="flex flex-col gap-1.5 rounded-lg border border-border-subtle bg-surface-2/40 p-2">
              {#each bulkKeys as row, index (row.id)}
                <div class="flex items-center gap-2">
                  <input type="checkbox" bind:checked={row.checked} class="accent-brand-500" />
                  <input
                    bind:value={row.name}
                    aria-label="Connection name"
                    class="min-w-0 flex-1 rounded-[8px] border border-transparent bg-surface-2 px-2 py-1 text-xs text-text-main focus:border-brand-500/40 focus:outline-none"
                  />
                  <span class="shrink-0 font-mono text-[10px] text-text-muted">#{index + 1}</span>
                </div>
              {/each}
            </div>
            <p class="text-xs text-text-muted">
              Each key becomes a connection on the receiver, named as above. The number is its rotation order.
            </p>
            {#if bulkMissingKeys}
              <p class="text-xs text-warning">Reading the stored keys…</p>
            {/if}
          {/if}
        </div>
      {/if}
      {#if scope === 'set'}
        <div class="flex flex-col gap-1.5">
          <span class="text-sm font-medium text-text-main">Providers to include</span>
          {#if availableProviders.length === 0}
            <p class="text-xs text-text-muted">No providers available.</p>
          {:else}
            <div class="max-h-48 overflow-y-auto rounded-lg border border-border-subtle bg-surface-2/40 p-2">
              {#each availableProviders as p (p.id)}
                <label class="flex items-center gap-2 px-1 py-1 text-xs text-text-main">
                  <input type="checkbox" bind:checked={setSelection[p.id]} class="accent-brand-500" />
                  {p.name || p.id}
                  {#if p.id === providerId}<span class="text-text-muted">(current)</span>{/if}
                </label>
              {/each}
            </div>
          {/if}
          <p class="text-xs text-text-muted">
            Each checked provider is added on the receiver with its model catalogue auto-fetched. Credentials are not
            carried — the receiver pastes them.
          </p>
        </div>
      {/if}

      <!-- Note -->
      <div class="flex flex-col gap-1.5">
        <label class="text-sm font-medium text-text-main" for="share-note">Note to the receiver (optional)</label>
        <input
          id="share-note"
          bind:value={note}
          maxlength="200"
          placeholder="e.g. prod account, do not rotate"
          class="w-full rounded-[8px] border border-transparent bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
        />
      </div>

      <!-- Warnings -->
      {#if hasCredential}
        <p class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs text-text-main">
          This link contains a live API key. Anyone who obtains it can spend your quota. Treat it like a password.
        </p>
      {/if}
      {#if scope === 'set'}
        <p class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs text-text-main">
          A multi-provider link is encoded, not encrypted — anyone who obtains it can read everything in it.
        </p>
      {/if}
      {#if shownTier === 'split'}
        <p class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs text-text-main">
          This link is {shownURL.length} characters — too long for many chat clients and browser history. Use
          <strong>Copy in two parts</strong> and send the credentials separately.
        </p>
      {/if}
      {#if shownTier === 'refuse'}
        <p class="rounded-lg border border-error/40 bg-error/10 p-3 text-xs text-text-main">
          This link would be {shownURL.length} characters, which no client can carry reliably. Share the provider link
          without credentials and send the keys through <strong>Export Keys</strong> instead.
        </p>
      {/if}
      {#if buildError}<p class="text-xs text-error">{buildError}</p>{/if}

      <!-- Preview -->
      <div class="flex flex-col gap-1.5">
        <span class="text-sm font-medium text-text-main">Link</span>
        <input
          readonly
          value={shownURL}
          class="w-full rounded-[8px] border border-border-subtle bg-surface-2 px-3 py-2 font-mono text-xs text-text-main"
        />
        <p class="text-xs text-text-muted">{shownURL.length} characters</p>
      </div>
    </div>
  {/snippet}

  {#snippet footer()}
    <div class="flex items-center justify-end gap-2">
      {#if shownTier === 'split'}
        <Button variant="secondary" onclick={copySplit}>
          <span class="material-symbols-outlined text-[16px]">content_copy</span>
          Copy in two parts
        </Button>
      {/if}
      <Button
        variant="primary"
        disabled={!shownURL || shownTier === 'refuse'}
        onclick={copyLink}
      >
        <span class="material-symbols-outlined text-[16px]">{copied ? 'check' : 'content_copy'}</span>
        {copied ? 'Copied' : 'Copy link'}
      </Button>
    </div>
  {/snippet}
</Modal>
