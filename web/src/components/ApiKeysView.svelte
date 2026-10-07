<script lang="ts">
  import {
    AlertTriangle,
    Check,
    Copy,
    Key,
    Loader2,
    Plus,
    Power,
    Shield,
    Terminal,
    Trash2
  } from 'lucide-svelte'
  import { api, type APIKey } from '../api/client'
  import { copyToClipboard } from '../lib/clipboard'
  import ApiKeyPolicyModal from './keys/ApiKeyPolicyModal.svelte'

  let {
    apiKeys = [],
    onRefresh
  }: {
    apiKeys: APIKey[]
    onRefresh: () => void
  } = $props()

  let isCreateOpen = $state(false)
  let name = $state('')
  let copiedKey = $state<string | null>(null)
  let isCreating = $state(false)
  let policyKey = $state<APIKey | null>(null)
  // The full secret is returned exactly once, by the create call. It is never
  // readable afterwards, so it is held here for the user to copy immediately
  // and cleared as soon as the modal closes.
  let issuedSecret = $state<string | null>(null)

  function handleCopy(text: string, id: string) {
    copyToClipboard(text)
    copiedKey = id
    setTimeout(() => (copiedKey = null), 2000)
  }

  async function handleToggle(key: APIKey) {
    try {
      await api.toggleApiKey(key.id)
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleDelete(id: string) {
    if (!confirm('Are you sure you want to revoke this API key?')) return
    try {
      await api.deleteApiKey(id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleCreate(e: SubmitEvent) {
    e.preventDefault()
    try {
      isCreating = true
      const created = await api.createApiKey({ name: name || 'client-key' })
      // This is the only moment the secret exists outside the request. Surface
      // it now, because no later call can recover it.
      issuedSecret = created.key
      isCreateOpen = false
      name = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to create key: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isCreating = false
    }
  }

  // A key with any limit or expiry configured is governed. The table surfaces
  // which control applies so an operator can spot the constrained keys without
  // opening every row.
  function hasPolicy(k: APIKey): boolean {
    return (
      (k.rateLimitRpm ?? 0) > 0 ||
      (k.rateLimitTpm ?? 0) > 0 ||
      (k.rateLimitConcurrency ?? 0) > 0 ||
      (k.expiresAt ?? '') !== ''
    )
  }

  function isExpired(k: APIKey): boolean {
    if (!k.expiresAt) return false
    const parsed = new Date(k.expiresAt)
    return !Number.isNaN(parsed.getTime()) && parsed.getTime() < Date.now()
  }

  let primaryKey = $derived(apiKeys[0]?.keyDisplay || apiKeys[0]?.key || 'sk-9router-local-token')

</script>

<div class="space-y-6">
  <!-- Page header -->
  <div class="flex flex-col sm:flex-row sm:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-brand-500 px-2 py-0.5 rounded bg-brand-500/10 border border-brand-500/25 font-bold">
          Client Gateway Access
        </span>
        <span class="text-text-subtle">•</span>
        <span class="font-code text-[11px] text-success">
          {apiKeys.filter((k) => k.isActive === 1).length} Active Tokens
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-text-main tracking-tight">
        CLI & Remote Access
      </h1>
      <p class="font-body text-xs sm:text-sm text-text-muted max-w-2xl leading-relaxed">
        Issue and manage Bearer tokens for connecting clients (Cursor IDE, Claude Code CLI, omp, Cline) to the local gateway on port 20130.
      </p>
    </div>

    <button
      type="button"
      onclick={() => (isCreateOpen = true)}
      class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-body text-xs font-bold shadow-md shadow-brand-500/25 transition cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>Generate Client Key</span>
    </button>
  </div>

  {#if issuedSecret}
    <!-- One-time disclosure. This key is stored as an argon2id verifier, so the
         secret cannot be shown again — once this banner is dismissed it is gone
         until the key is revoked and reissued. -->
    <div class="p-4 rounded-xl bg-success/5 border border-success/30 space-y-3">
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <h3 class="font-headline text-xs font-bold text-success flex items-center gap-2">
            <Check class="w-4 h-4" />
            <span>Key created — copy it now</span>
          </h3>
          <p class="text-text-muted mt-1">
            For safety it is stored hashed and shown only once. If you lose it, revoke the key and
            create another.
          </p>
        </div>
        <button
          type="button"
          onclick={() => (issuedSecret = null)}
          class="shrink-0 px-2.5 py-1 rounded-lg border border-border text-[11px] text-text-muted hover:text-text-main transition cursor-pointer"
        >
          Dismiss
        </button>
      </div>
      <div class="flex items-center gap-2">
        <code class="flex-1 px-3 py-2 rounded-lg bg-bg border border-success/30 font-code text-[11px] text-success select-all break-all">
          {issuedSecret}
        </code>
        <button
          type="button"
          onclick={() => issuedSecret && handleCopy(issuedSecret, 'issued')}
          class="p-2 rounded-lg bg-success/10 border border-success/25 text-success hover:bg-success/20 transition cursor-pointer shrink-0"
          title="Copy key"
        >
          {#if copiedKey === 'issued'}
            <Check class="w-3.5 h-3.5" />
          {:else}
            <Copy class="w-3.5 h-3.5" />
          {/if}
        </button>
      </div>
    </div>
  {/if}

  <!-- Keys Table Card -->
  <div class="bg-surface border border-border rounded-xl overflow-hidden shadow-xl">
    <div class="p-4 border-b border-border flex items-center justify-between">
      <h3 class="font-headline text-sm font-bold text-text-main flex items-center gap-2">
        <Key class="w-4 h-4 text-brand-500" />
        <span>Active Access Tokens</span>
      </h3>
      <span class="font-code text-[11px] text-text-subtle">{apiKeys.length} Keys Enrolled</span>
    </div>

    <div class="overflow-x-auto">
      <table class="w-full text-left font-body text-xs">
        <thead>
          <tr class="border-b border-border text-text-subtle font-code uppercase text-[10px] tracking-wider bg-surface-2">
            <th class="py-2.5 px-4">Label Identity</th>
            <th class="py-2.5 px-4">Bearer Token</th>
            <th class="py-2.5 px-4">Policy</th>
            <th class="py-2.5 px-4">Status</th>
            <th class="py-2.5 px-4">Created Date</th>
            <th class="py-2.5 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/50 font-code">
          {#each apiKeys as k (k.id)}
            {@const isActive = k.isActive === 1}
            {@const expired = isExpired(k)}
            <tr class="hover:bg-surface-2/40 transition">
              <td class="py-3 px-4 font-body font-bold text-text-main">{k.name || 'Client Token'}</td>
              <td class="py-3 px-4 text-text-muted">
                <!-- The secret is stored as an argon2id verifier: only the masked
                     display value exists after creation, so there is nothing to
                     copy here. The issued secret is offered once, at creation. -->
                <span class="bg-bg px-2.5 py-1 rounded border border-border text-[11px] text-info">
                  {k.keyDisplay || k.key}
                </span>
              </td>
              <td class="py-3 px-4">
                {#if hasPolicy(k)}
                  <div class="flex flex-wrap gap-1">
                    {#if (k.rateLimitRpm ?? 0) > 0}
                      <span class="px-1.5 py-0.5 rounded text-[10px] bg-info/10 text-info border border-info/20">
                        {k.rateLimitRpm}/min
                      </span>
                    {/if}
                    {#if (k.rateLimitTpm ?? 0) > 0}
                      <span class="px-1.5 py-0.5 rounded text-[10px] bg-info/10 text-info border border-info/20">
                        {(k.rateLimitTpm ?? 0).toLocaleString()} tok/min
                      </span>
                    {/if}
                    {#if (k.rateLimitConcurrency ?? 0) > 0}
                      <span class="px-1.5 py-0.5 rounded text-[10px] bg-info/10 text-info border border-info/20">
                        {(k.rateLimitConcurrency ?? 0).toLocaleString()} conc
                      </span>
                    {/if}
                    {#if (k.expiresAt ?? '') !== ''}
                      <span
                        class="px-1.5 py-0.5 rounded text-[10px] border {expired
                          ? 'bg-danger/10 text-danger border-danger/20'
                          : 'bg-warning/10 text-warning border-warning/20'}"
                      >
                        {expired ? 'EXPIRED' : 'expires'}
                      </span>
                    {/if}
                  </div>
                {:else}
                  <span class="text-text-subtle text-[10px]">unrestricted</span>
                {/if}
              </td>
              <td class="py-3 px-4">
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-bold {isActive
                    ? 'bg-success/10 text-success border border-success/20'
                    : 'bg-surface-2 text-text-subtle'}"
                >
                  {isActive ? 'ACTIVE' : 'REVOKED'}
                </span>
                {#if (k.usedCount ?? 0) > 0}
                  <span class="block mt-1 text-[10px] text-text-subtle">
                    {(k.usedCount ?? 0).toLocaleString()} req
                  </span>
                {/if}
              </td>
              <td class="py-3 px-4 text-text-subtle font-body text-[11px]">
                {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex items-center justify-end gap-1.5">
                  <button
                    type="button"
                    onclick={() => (policyKey = k)}
                    class="p-1.5 rounded-lg border transition cursor-pointer {hasPolicy(k)
                      ? 'bg-brand-500/10 border-brand-500/25 text-brand-500'
                      : 'bg-surface-2 border-border text-text-subtle hover:text-text-main'}"
                    title="Configure rate limits, expiry, model access"
                  >
                    <Shield class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleToggle(k)}
                    class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                      ? 'bg-success/10 border-success/20 text-success'
                      : 'bg-surface-2 border-border text-text-subtle'}"
                    title={isActive ? 'Deactivate' : 'Activate'}
                  >
                    <Power class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleDelete(k.id)}
                    class="p-1.5 rounded-lg text-text-subtle hover:bg-danger/10 hover:text-danger transition cursor-pointer"
                    title="Delete"
                  >
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>

  <!-- Quick Client Snippets -->
  <div class="space-y-3">
    <h3 class="font-headline text-sm font-bold text-text-main flex items-center gap-2">
      <Terminal class="w-4 h-4 text-info" />
      <span>Quick Client Integration Snippets</span>
    </h3>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- Cursor -->
      <div class="p-4 rounded-xl bg-surface border border-border space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-text-main">Cursor IDE</span>
          <span class="font-code text-[10px] text-text-subtle">Settings &gt; Models &gt; OpenAI API Key</span>
        </div>
        <div class="p-3 rounded-lg bg-bg border border-border font-code text-[11px] text-text-main space-y-1 select-all">
          <div>
            <span class="text-text-subtle">Base URL: </span>
            <span class="text-info">http://localhost:20130/v1</span>
          </div>
          <div>
            <span class="text-text-subtle">API Key: </span>
            <span class="text-brand-400">{primaryKey}</span>
          </div>
        </div>
      </div>

      <!-- Claude Code -->
      <div class="p-4 rounded-xl bg-surface border border-border space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-text-main">Claude Code CLI</span>
          <span class="font-code text-[10px] text-text-subtle">Terminal Environment</span>
        </div>
        <div class="p-3 rounded-lg bg-bg border border-border font-code text-[11px] text-text-main space-y-1 select-all">
          <div>export ANTHROPIC_BASE_URL="http://localhost:20130"</div>
          <div>export ANTHROPIC_API_KEY="{primaryKey}"</div>
        </div>
      </div>
    </div>

    <p class="text-text-subtle text-[11px]">
      <AlertTriangle class="w-3.5 h-3.5 inline align-text-bottom text-warning" />
      Keys are stored hashed, so the snippets show the masked form. Paste the value you copied when
      the key was created.
    </p>
  </div>

  <!-- Per-key policy: rate limits, expiry, metadata, model allowlist -->
  {#if policyKey}
    <ApiKeyPolicyModal
      apiKey={policyKey}
      onClose={() => (policyKey = null)}
      onSaved={onRefresh}
    />
  {/if}

  <!-- Create Key Modal -->
  {#if isCreateOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-surface-2 border border-border shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-border">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isCreateOpen = false)}
              class="w-3 h-3 rounded-full bg-[#ff5f56] cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-[#ffbd2e]"></div>
            <div class="w-3 h-3 rounded-full bg-[#27c93f]"></div>
            <span class="ml-2 font-headline text-sm font-bold text-text-main">
              Generate Client Access Token
            </span>
          </div>
        </div>

        <form onsubmit={handleCreate} class="space-y-3 font-body text-xs">
          <div>
            <label for="new-key-label" class="block font-semibold text-text-muted mb-1">Token Label</label>
            <input
              id="new-key-label"
              type="text"
              placeholder="e.g. cursor-mini-pc, claude-cli-laptop"
              bind:value={name}
              class="w-full bg-surface-2 border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
          </div>

          <div class="flex justify-end gap-2 pt-3 border-t border-border">
            <button
              type="button"
              onclick={() => (isCreateOpen = false)}
              class="px-4 py-2 rounded-lg text-text-muted hover:text-text-main cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isCreating}
              class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-bold shadow-md shadow-brand-500/25 cursor-pointer"
            >
              {#if isCreating}
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
              {:else}
                <Check class="w-3.5 h-3.5" />
              {/if}
              <span>Generate Key</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  {/if}
</div>
