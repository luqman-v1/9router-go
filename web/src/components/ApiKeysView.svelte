<script lang="ts">
  import {
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
      await api.createApiKey({ name: name || 'client-key' })
      isCreateOpen = false
      name = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to create key: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isCreating = false
    }
  }

  let primaryKey = $derived(apiKeys[0]?.key || 'sk-9router-local-token')
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
            <th class="py-2.5 px-4">Status</th>
            <th class="py-2.5 px-4">Created Date</th>
            <th class="py-2.5 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/50 font-code">
          {#each apiKeys as k (k.id)}
            {@const isActive = k.isActive === 1}
            <tr class="hover:bg-surface-2/40 transition">
              <td class="py-3 px-4 font-body font-bold text-text-main">{k.name || 'Client Token'}</td>
              <td class="py-3 px-4 text-text-muted">
                <div class="flex items-center gap-2">
                  <span class="bg-bg px-2.5 py-1 rounded border border-border text-[11px] text-info">
                    {k.key}
                  </span>
                  <button
                    type="button"
                    onclick={() => handleCopy(k.key, k.id)}
                    class="p-1 rounded text-text-subtle hover:text-text-main cursor-pointer"
                    title="Copy Key"
                  >
                    {#if copiedKey === k.id}
                      <Check class="w-3.5 h-3.5 text-success" />
                    {:else}
                      <Copy class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                </div>
              </td>
              <td class="py-3 px-4">
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-bold {isActive
                    ? 'bg-success/10 text-success border border-success/20'
                    : 'bg-surface-2 text-text-subtle'}"
                >
                  {isActive ? 'ACTIVE' : 'REVOKED'}
                </span>
              </td>
              <td class="py-3 px-4 text-text-subtle font-body text-[11px]">
                {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex items-center justify-end gap-1.5">
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
                    class="p-1.5 rounded-lg text-text-subtle hover:bg-danger/10 hover:text-red-600 dark:hover:text-red-400 transition cursor-pointer"
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
            <span class="text-brand-400 truncate">{primaryKey}</span>
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
  </div>

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
